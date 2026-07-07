package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// checkInRepository backs service.CheckInRepository. Aggregates and the atomic claim
// go through raw SQL on r.sql (a *sql.DB, which also exposes BeginTx); the ent client
// is retained for parity with sibling repositories.
type checkInRepository struct {
	client *dbent.Client
	sql    *sql.DB
}

// NewCheckInRepository creates the daily check-in repository.
func NewCheckInRepository(client *dbent.Client, sqlDB *sql.DB) service.CheckInRepository {
	return &checkInRepository{client: client, sql: sqlDB}
}

const checkInSelectColumns = `id, user_id, check_in_date, reward_amount, streak_count, score, recharge_snapshot, usage_snapshot, created_at`

func scanCheckInRecord(scan func(dest ...any) error) (service.CheckInRecord, error) {
	var rec service.CheckInRecord
	err := scan(
		&rec.ID,
		&rec.UserID,
		&rec.CheckInDate,
		&rec.RewardAmount,
		&rec.StreakCount,
		&rec.Score,
		&rec.RechargeSnapshot,
		&rec.UsageSnapshot,
		&rec.CreatedAt,
	)
	return rec, err
}

func (r *checkInRepository) GetLatestRecord(ctx context.Context, userID int64) (*service.CheckInRecord, error) {
	query := `SELECT ` + checkInSelectColumns + `
		FROM check_in_records
		WHERE user_id = $1
		ORDER BY check_in_date DESC
		LIMIT 1`
	rec, err := scanCheckInRecord(r.sql.QueryRowContext(ctx, query, userID).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get latest check-in record: %w", err)
	}
	return &rec, nil
}

// ClaimDailyReward atomically inserts today's record and credits the gift balance.
// The INSERT ... ON CONFLICT DO NOTHING enforces one claim per user per day; a conflict
// (sql.ErrNoRows on the RETURNING) is reported as claimed=false without error.
// The balance UPDATE deliberately touches only users.balance (NOT total_recharged) —
// check-in rewards are a give-away, not a recharge.
func (r *checkInRepository) ClaimDailyReward(ctx context.Context, in service.ClaimInput) (newBalance float64, claimed bool, err error) {
	tx, err := r.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, fmt.Errorf("begin check-in tx: %w", err)
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	var recordID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO check_in_records
			(user_id, check_in_date, reward_amount, streak_count, score, recharge_snapshot, usage_snapshot, created_at)
		VALUES ($1, $2::date, $3, $4, $5, $6, $7, NOW())
		ON CONFLICT (user_id, check_in_date) DO NOTHING
		RETURNING id
	`, in.UserID, in.CheckInDate, in.RewardAmount, in.StreakCount, in.Score, in.RechargeSnapshot, in.UsageSnapshot).Scan(&recordID)
	if errors.Is(err, sql.ErrNoRows) {
		// Unique conflict: already claimed today. Not an error.
		_ = tx.Rollback()
		tx = nil
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("insert check-in record: %w", err)
	}

	// Gift credit: only balance, never total_recharged.
	err = tx.QueryRowContext(ctx, `
		UPDATE users
		SET balance = balance + $1, updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
		RETURNING balance
	`, in.RewardAmount, in.UserID).Scan(&newBalance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, service.ErrUserNotFound
	}
	if err != nil {
		return 0, false, fmt.Errorf("credit check-in reward: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit check-in tx: %w", err)
	}
	tx = nil
	return newBalance, true, nil
}

func (r *checkInRepository) SumRewardsOnDate(ctx context.Context, dateStr string) (float64, error) {
	var sum float64
	err := r.sql.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(reward_amount),0) FROM check_in_records WHERE check_in_date = $1::date`,
		dateStr).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum rewards on date: %w", err)
	}
	return sum, nil
}

func (r *checkInRepository) SumUserRewardsBetween(ctx context.Context, userID int64, startDateStr, endDateStr string) (float64, error) {
	var sum float64
	err := r.sql.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(reward_amount),0) FROM check_in_records
		 WHERE user_id = $1 AND check_in_date BETWEEN $2::date AND $3::date`,
		userID, startDateStr, endDateStr).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum user rewards between: %w", err)
	}
	return sum, nil
}

func (r *checkInRepository) SumRechargeByUser(ctx context.Context, userID int64) (float64, error) {
	var sum float64
	err := r.sql.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount),0) FROM payment_orders
		 WHERE user_id = $1 AND order_type = 'balance' AND status IN ('PAID','RECHARGING','COMPLETED')`,
		userID).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum recharge by user: %w", err)
	}
	return sum, nil
}

func (r *checkInRepository) SumUsageActualCostSince(ctx context.Context, userID int64, since time.Time) (float64, error) {
	var sum float64
	err := r.sql.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(actual_cost),0) FROM usage_logs WHERE user_id = $1 AND created_at >= $2`,
		userID, since).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum usage actual cost: %w", err)
	}
	return sum, nil
}

// CountActiveDaysSince counts distinct active days from the dashboard rollup table.
// This only feeds an activity bonus, so any failure (missing table / non-PG / query
// error) degrades gracefully to 0 rather than breaking check-in.
func (r *checkInRepository) CountActiveDaysSince(ctx context.Context, userID int64, sinceDateStr string) (int, error) {
	var count int
	err := r.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage_dashboard_daily_users WHERE user_id = $1 AND bucket_date >= $2::date`,
		userID, sinceDateStr).Scan(&count)
	if err != nil {
		// Absence of activity data just means a smaller reward; never surface the error.
		return 0, nil
	}
	return count, nil
}

func (r *checkInRepository) ListByUser(ctx context.Context, userID int64, limit int) ([]service.CheckInRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `SELECT ` + checkInSelectColumns + `
		FROM check_in_records
		WHERE user_id = $1
		ORDER BY check_in_date DESC
		LIMIT $2`
	rows, err := r.sql.QueryContext(ctx, query, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list check-in records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]service.CheckInRecord, 0, limit)
	for rows.Next() {
		rec, err := scanCheckInRecord(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan check-in record: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate check-in records: %w", err)
	}
	return out, nil
}

func (r *checkInRepository) GetUserLifetimeReward(ctx context.Context, userID int64) (float64, error) {
	var sum float64
	err := r.sql.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(reward_amount),0) FROM check_in_records WHERE user_id = $1`,
		userID).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("get user lifetime reward: %w", err)
	}
	return sum, nil
}

// GetAnalytics aggregates admin analytics for daily check-in. All date parameters are
// 'YYYY-MM-DD' in the app timezone and are compared against the DATE-typed check_in_date
// column (which is indexed).
func (r *checkInRepository) GetAnalytics(ctx context.Context, todayStr, monthStartStr, trendStartStr string) (*service.CheckInAnalytics, error) {
	out := &service.CheckInAnalytics{}

	// Totals across all history.
	if err := r.sql.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(reward_amount),0), COUNT(*) FROM check_in_records`,
	).Scan(&out.TotalGifted, &out.TotalCheckins); err != nil {
		return nil, fmt.Errorf("check-in analytics totals: %w", err)
	}

	// Today.
	if err := r.sql.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(reward_amount),0), COUNT(*), COUNT(DISTINCT user_id)
		 FROM check_in_records WHERE check_in_date = $1::date`,
		todayStr,
	).Scan(&out.TodayGifted, &out.TodayCheckins, &out.DistinctUsersToday); err != nil {
		return nil, fmt.Errorf("check-in analytics today: %w", err)
	}

	// Current month (from month start through today, inclusive).
	if err := r.sql.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(reward_amount),0), COUNT(DISTINCT user_id)
		 FROM check_in_records WHERE check_in_date >= $1::date`,
		monthStartStr,
	).Scan(&out.MonthGifted, &out.DistinctUsersMonth); err != nil {
		return nil, fmt.Errorf("check-in analytics month: %w", err)
	}

	// Last-30-days trend, one row per day that had at least one check-in.
	rows, err := r.sql.QueryContext(ctx,
		`SELECT check_in_date, COALESCE(SUM(reward_amount),0), COUNT(*)
		 FROM check_in_records
		 WHERE check_in_date >= $1::date
		 GROUP BY check_in_date
		 ORDER BY check_in_date ASC`,
		trendStartStr,
	)
	if err != nil {
		return nil, fmt.Errorf("check-in analytics trend: %w", err)
	}
	defer func() { _ = rows.Close() }()

	trend := make([]service.CheckInTrendPoint, 0, 30)
	for rows.Next() {
		var (
			date   time.Time
			gifted float64
			count  int64
		)
		if err := rows.Scan(&date, &gifted, &count); err != nil {
			return nil, fmt.Errorf("scan check-in trend: %w", err)
		}
		trend = append(trend, service.CheckInTrendPoint{
			Date:   date.Format("2006-01-02"),
			Gifted: gifted,
			Count:  count,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate check-in trend: %w", err)
	}
	out.Trend = trend

	return out, nil
}

// ListRecords returns a page of individual check-in records joined to their user
// (for email/username), plus the total count of matching rows. All filter values are
// bound as positional params (never interpolated). The optional date filters compare
// the 'YYYY-MM-DD' strings against the DATE-typed check_in_date column. User
// email/username are COALESCE'd to '' when the LEFT JOIN finds no user.
func (r *checkInRepository) ListRecords(ctx context.Context, params pagination.PaginationParams, filter service.CheckInRecordFilter) ([]service.CheckInRecordDetail, int64, error) {
	// Build the shared WHERE clause with positional bind params.
	conds := make([]string, 0, 3)
	args := make([]any, 0, 5)
	idx := 1
	if filter.UserID != nil {
		conds = append(conds, fmt.Sprintf("cr.user_id = $%d", idx))
		args = append(args, *filter.UserID)
		idx++
	}
	if filter.StartDate != "" {
		conds = append(conds, fmt.Sprintf("cr.check_in_date >= $%d::date", idx))
		args = append(args, filter.StartDate)
		idx++
	}
	if filter.EndDate != "" {
		conds = append(conds, fmt.Sprintf("cr.check_in_date <= $%d::date", idx))
		args = append(args, filter.EndDate)
		idx++
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int64
	if err := r.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM check_in_records cr`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count check-in records: %w", err)
	}
	if total == 0 {
		return []service.CheckInRecordDetail{}, 0, nil
	}

	// Pagination binds follow the filter binds.
	listArgs := append(append([]any{}, args...), params.Limit(), params.Offset())
	query := `SELECT cr.id, cr.user_id,
			COALESCE(u.email, ''), COALESCE(u.username, ''),
			cr.check_in_date, cr.reward_amount, cr.streak_count, cr.score,
			cr.recharge_snapshot, cr.usage_snapshot, cr.created_at
		FROM check_in_records cr
		LEFT JOIN users u ON u.id = cr.user_id` + where +
		fmt.Sprintf(" ORDER BY cr.created_at DESC LIMIT $%d OFFSET $%d", idx, idx+1)

	rows, err := r.sql.QueryContext(ctx, query, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list check-in records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]service.CheckInRecordDetail, 0, params.Limit())
	for rows.Next() {
		var rec service.CheckInRecordDetail
		if err := rows.Scan(
			&rec.ID,
			&rec.UserID,
			&rec.UserEmail,
			&rec.UserUsername,
			&rec.CheckInDate,
			&rec.RewardAmount,
			&rec.StreakCount,
			&rec.Score,
			&rec.RechargeSnapshot,
			&rec.UsageSnapshot,
			&rec.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan check-in record detail: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate check-in records: %w", err)
	}
	return out, total, nil
}
