package service

import (
	"context"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeCheckInRepo is a full, configurable stand-in for CheckInRepository.
type fakeCheckInRepo struct {
	latest    *CheckInRecord
	latestErr error

	recharge   float64
	usage30d   float64
	activeDays int
	monthSum   float64
	todaySum   float64
	lifetime   float64
	history    []CheckInRecord

	claimBalance float64
	claimed      bool
	claimErr     error

	listRecords      []CheckInRecordDetail
	listRecordsTotal int64
	listRecordsErr   error

	// captured for assertions
	claimInput           *ClaimInput
	claimCalls           int
	sumRewardsOnDateHit  bool
	sumUserRewardsHit    bool
	getLatestCalled      bool
	sumRechargeCallCount int
	listRecordsParams    pagination.PaginationParams
	listRecordsFilter    *CheckInRecordFilter
}

func (f *fakeCheckInRepo) GetLatestRecord(ctx context.Context, userID int64) (*CheckInRecord, error) {
	f.getLatestCalled = true
	return f.latest, f.latestErr
}

func (f *fakeCheckInRepo) ClaimDailyReward(ctx context.Context, in ClaimInput) (float64, bool, error) {
	f.claimCalls++
	cp := in
	f.claimInput = &cp
	return f.claimBalance, f.claimed, f.claimErr
}

func (f *fakeCheckInRepo) SumRewardsOnDate(ctx context.Context, dateStr string) (float64, error) {
	f.sumRewardsOnDateHit = true
	return f.todaySum, nil
}

func (f *fakeCheckInRepo) SumUserRewardsBetween(ctx context.Context, userID int64, startDateStr, endDateStr string) (float64, error) {
	f.sumUserRewardsHit = true
	return f.monthSum, nil
}

func (f *fakeCheckInRepo) SumRechargeByUser(ctx context.Context, userID int64) (float64, error) {
	f.sumRechargeCallCount++
	return f.recharge, nil
}

func (f *fakeCheckInRepo) SumUsageActualCostSince(ctx context.Context, userID int64, since time.Time) (float64, error) {
	return f.usage30d, nil
}

func (f *fakeCheckInRepo) CountActiveDaysSince(ctx context.Context, userID int64, sinceDateStr string) (int, error) {
	return f.activeDays, nil
}

func (f *fakeCheckInRepo) ListByUser(ctx context.Context, userID int64, limit int) ([]CheckInRecord, error) {
	return f.history, nil
}

func (f *fakeCheckInRepo) GetUserLifetimeReward(ctx context.Context, userID int64) (float64, error) {
	return f.lifetime, nil
}

func (f *fakeCheckInRepo) GetAnalytics(ctx context.Context, todayStr, monthStartStr, trendStartStr string) (*CheckInAnalytics, error) {
	return &CheckInAnalytics{}, nil
}

func (f *fakeCheckInRepo) ListRecords(ctx context.Context, params pagination.PaginationParams, filter CheckInRecordFilter) ([]CheckInRecordDetail, int64, error) {
	f.listRecordsParams = params
	cp := filter
	f.listRecordsFilter = &cp
	return f.listRecords, f.listRecordsTotal, f.listRecordsErr
}

// fakeCheckInUserRepo overrides only the methods the service uses; any other
// UserRepository call would nil-panic, which is exactly the guard we want —
// notably, a stray UpdateBalance would blow up the test (it must never be used
// for gift credit).
type fakeCheckInUserRepo struct {
	UserRepository
	user   *User
	getErr error

	getByIDCalled       bool
	updateBalanceCalled bool
}

func (f *fakeCheckInUserRepo) GetByID(ctx context.Context, id int64) (*User, error) {
	f.getByIDCalled = true
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.user == nil {
		return nil, ErrUserNotFound
	}
	clone := *f.user
	return &clone, nil
}

func (f *fakeCheckInUserRepo) UpdateBalance(ctx context.Context, id int64, amount float64) error {
	f.updateBalanceCalled = true
	return nil
}

// fakeCheckInSettingRepo drives loadConfig; unset keys fall back to defaults.
type fakeCheckInSettingRepo struct {
	SettingRepository
	vals map[string]string
}

func (f *fakeCheckInSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if v, ok := f.vals[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}

// GetMultiple backs loadConfig's single-round-trip settings fetch; unset keys are
// simply absent from the map so the parsers fall back to defaults.
func (f *fakeCheckInSettingRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if v, ok := f.vals[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func defaultCheckInConfig() checkInConfig {
	cfg := checkInConfig{
		enabled:        true,
		minReward:      defaultCheckInMinReward,
		maxReward:      defaultCheckInMaxReward,
		baseCap:        defaultCheckInBaseCap,
		weightRecharge: defaultCheckInWeightRecharge,
		weightUsage:    defaultCheckInWeightUsage,
		weightActivity: defaultCheckInWeightActivity,
		rechargeCap:    defaultCheckInRechargeCap,
		usageCap:       defaultCheckInUsageCap,
		streakCap:      defaultCheckInStreakCap,
		betaMin:        defaultCheckInBetaMin,
		betaMax:        defaultCheckInBetaMax,
	}
	cfg.sanitize()
	return cfg
}

func newCheckInServiceForTest(repo *fakeCheckInRepo, userRepo *fakeCheckInUserRepo, vals map[string]string) *CheckInService {
	return NewCheckInService(repo, nil, userRepo, &fakeCheckInSettingRepo{vals: vals}, nil, nil)
}

func activeUser(ageDays int) *User {
	return &User{
		ID:        7,
		Balance:   1.0,
		Status:    StatusActive,
		CreatedAt: timezone.Now().AddDate(0, 0, -ageDays),
	}
}

// ---------------------------------------------------------------------------
// Pure-function tests: the money invariant lives here.
// ---------------------------------------------------------------------------

func TestComputeCheckInReward_AlwaysWithinBounds(t *testing.T) {
	adversarial := checkInConfig{
		minReward: -1, maxReward: -2, baseCap: 100,
		betaMin: -5, betaMax: -9,
		rechargeCap: 200, usageCap: 50, streakCap: 7,
		weightRecharge: 0.5, weightUsage: 0.25, weightActivity: 0.25,
	}
	adversarial.sanitize()

	narrow := defaultCheckInConfig()
	narrow.minReward, narrow.maxReward = 0.5, 3
	narrow.sanitize()

	configs := []checkInConfig{defaultCheckInConfig(), narrow, adversarial}

	for ci, cfg := range configs {
		for s := 0.0; s <= 1.0+1e-9; s += 0.05 {
			for i := 0; i < 200; i++ {
				r, err := computeCheckInReward(cfg, s)
				require.NoError(t, err)
				// The hard requirement: a reward is always granted and is strictly positive.
				require.Greater(t, r, 0.0, "cfg %d score %.2f: reward must be > 0", ci, s)
				require.GreaterOrEqual(t, r, cfg.minReward-1e-9, "cfg %d score %.2f: reward below minReward", ci, s)
				require.LessOrEqual(t, r, cfg.maxReward+1e-9, "cfg %d score %.2f: reward above maxReward", ci, s)
			}
		}
	}
}

func TestCheckInConfigSanitize(t *testing.T) {
	t.Run("negative min falls back to default and never permits a deduction", func(t *testing.T) {
		c := checkInConfig{minReward: -0.5, maxReward: 5, baseCap: 0.2, betaMin: 1, betaMax: 3}
		c.sanitize()
		require.Equal(t, defaultCheckInMinReward, c.minReward)
		require.Greater(t, c.minReward, 0.0)
	})
	t.Run("zero min falls back to default", func(t *testing.T) {
		c := checkInConfig{minReward: 0, maxReward: 5, baseCap: 0.2, betaMin: 1, betaMax: 3}
		c.sanitize()
		require.Equal(t, defaultCheckInMinReward, c.minReward)
	})
	t.Run("max below min is repaired so max >= min", func(t *testing.T) {
		c := checkInConfig{minReward: 2, maxReward: 1, baseCap: 0.2, betaMin: 1, betaMax: 3}
		c.sanitize()
		require.GreaterOrEqual(t, c.maxReward, c.minReward)
	})
	t.Run("min above default ceiling collapses to a point", func(t *testing.T) {
		c := checkInConfig{minReward: 9, maxReward: 1, baseCap: 0.2, betaMin: 1, betaMax: 3}
		c.sanitize()
		require.Equal(t, 9.0, c.minReward)
		require.Equal(t, c.minReward, c.maxReward)
	})
	t.Run("baseCap clamped into the reward band", func(t *testing.T) {
		c := checkInConfig{minReward: 0.5, maxReward: 4, baseCap: 100, betaMin: 1, betaMax: 3}
		c.sanitize()
		require.GreaterOrEqual(t, c.baseCap, c.minReward)
		require.LessOrEqual(t, c.baseCap, c.maxReward)
	})
	t.Run("beta exponents kept non-negative and ordered", func(t *testing.T) {
		c := checkInConfig{minReward: 0.01, maxReward: 5, baseCap: 0.2, betaMin: -5, betaMax: -9}
		c.sanitize()
		require.GreaterOrEqual(t, c.betaMin, 0.0)
		require.GreaterOrEqual(t, c.betaMax, c.betaMin)
	})
}

func TestComputeCheckInScore_BoundsAndMonotonic(t *testing.T) {
	cfg := defaultCheckInConfig()

	// Bounds across a wide input matrix.
	for _, recharge := range []float64{0, 10, 200, 5000} {
		for _, usage := range []float64{0, 5, 50, 500} {
			for _, streak := range []int{0, 1, 7, 30} {
				for _, active := range []int{0, 3, 7, 20} {
					s := computeCheckInScore(cfg, recharge, usage, streak, active)
					require.GreaterOrEqual(t, s, 0.0)
					require.LessOrEqual(t, s, 1.0)
				}
			}
		}
	}

	// Recharge is the dominant, monotonic dimension.
	s0 := computeCheckInScore(cfg, 0, 0, 0, 0)
	s1 := computeCheckInScore(cfg, 50, 0, 0, 0)
	s2 := computeCheckInScore(cfg, 500, 0, 0, 0)
	require.Less(t, s0, s1)
	require.Less(t, s1, s2)

	// All dimensions maxed approaches the ceiling; nothing approaches the floor.
	require.InDelta(t, 0.0, s0, 1e-9)
	require.Greater(t, computeCheckInScore(cfg, 100000, 100000, 100, 100), 0.9)
}

func TestCryptoRandFloat64_Range(t *testing.T) {
	for i := 0; i < 5000; i++ {
		u, err := cryptoRandFloat64()
		require.NoError(t, err)
		require.GreaterOrEqual(t, u, 0.0)
		require.Less(t, u, 1.0)
	}
}

// ---------------------------------------------------------------------------
// Claim flow tests
// ---------------------------------------------------------------------------

func TestClaim_DisabledShortCircuits(t *testing.T) {
	repo := &fakeCheckInRepo{}
	userRepo := &fakeCheckInUserRepo{user: activeUser(30)}
	svc := newCheckInServiceForTest(repo, userRepo, map[string]string{SettingKeyCheckInEnabled: "false"})

	res, err := svc.Claim(context.Background(), 7)

	require.Nil(t, res)
	require.Equal(t, "CHECKIN_DISABLED", infraerrors.Reason(err))
	require.False(t, userRepo.getByIDCalled, "must not touch user when disabled")
	require.Equal(t, 0, repo.claimCalls)
}

func TestClaim_InactiveUserNotEligible(t *testing.T) {
	repo := &fakeCheckInRepo{claimed: true}
	u := activeUser(30)
	u.Status = "disabled"
	userRepo := &fakeCheckInUserRepo{user: u}
	svc := newCheckInServiceForTest(repo, userRepo, nil)

	res, err := svc.Claim(context.Background(), 7)

	require.Nil(t, res)
	require.Equal(t, "CHECKIN_NOT_ELIGIBLE", infraerrors.Reason(err))
	require.Equal(t, 0, repo.claimCalls)
}

func TestClaim_AccountTooYoungNotEligible(t *testing.T) {
	repo := &fakeCheckInRepo{claimed: true}
	userRepo := &fakeCheckInUserRepo{user: activeUser(2)} // 2 days old
	svc := newCheckInServiceForTest(repo, userRepo, map[string]string{SettingKeyCheckInMinAccountAgeDays: "7"})

	res, err := svc.Claim(context.Background(), 7)

	require.Nil(t, res)
	require.Equal(t, "CHECKIN_NOT_ELIGIBLE", infraerrors.Reason(err))
	require.Equal(t, 0, repo.claimCalls)
}

func TestClaim_RequireRechargeGate(t *testing.T) {
	repo := &fakeCheckInRepo{claimed: true, recharge: 0}
	userRepo := &fakeCheckInUserRepo{user: activeUser(30)}
	svc := newCheckInServiceForTest(repo, userRepo, map[string]string{SettingKeyCheckInRequireRecharge: "true"})

	res, err := svc.Claim(context.Background(), 7)

	require.Nil(t, res)
	require.Equal(t, "CHECKIN_NOT_ELIGIBLE", infraerrors.Reason(err))
	require.Equal(t, 0, repo.claimCalls)
}

func TestClaim_AlreadyClaimedTodayShortCircuits(t *testing.T) {
	now := timezone.Now()
	repo := &fakeCheckInRepo{
		claimed: true,
		latest:  &CheckInRecord{CheckInDate: now, StreakCount: 4},
	}
	userRepo := &fakeCheckInUserRepo{user: activeUser(30)}
	svc := newCheckInServiceForTest(repo, userRepo, nil)

	res, err := svc.Claim(context.Background(), 7)

	require.Nil(t, res)
	require.Equal(t, "CHECKIN_ALREADY_CLAIMED", infraerrors.Reason(err))
	require.Equal(t, 0, repo.claimCalls, "must not credit when already claimed today")
}

func TestClaim_FirstEverStartsStreakAtOne(t *testing.T) {
	repo := &fakeCheckInRepo{claimed: true, claimBalance: 12.34}
	userRepo := &fakeCheckInUserRepo{user: activeUser(30)}
	svc := newCheckInServiceForTest(repo, userRepo, nil)

	res, err := svc.Claim(context.Background(), 7)

	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, 1, res.Streak)
	require.Equal(t, 12.34, res.NewBalance)
	require.Equal(t, timezone.Now().Format("2006-01-02"), res.CheckInDate)
	// Reward invariant at the service boundary.
	require.Greater(t, res.RewardAmount, 0.0)
	require.LessOrEqual(t, res.RewardAmount, defaultCheckInMaxReward)
	// The credit must be delegated to the atomic claim, never to UpdateBalance.
	require.Equal(t, 1, repo.claimCalls)
	require.False(t, userRepo.updateBalanceCalled, "gift credit must not route through UpdateBalance")
	require.NotNil(t, repo.claimInput)
	require.Equal(t, 1, repo.claimInput.StreakCount)
	require.Equal(t, res.RewardAmount, repo.claimInput.RewardAmount)
}

func TestClaim_StreakIncrementsFromYesterday(t *testing.T) {
	yesterday := timezone.Now().AddDate(0, 0, -1)
	repo := &fakeCheckInRepo{
		claimed: true,
		latest:  &CheckInRecord{CheckInDate: yesterday, StreakCount: 3},
	}
	userRepo := &fakeCheckInUserRepo{user: activeUser(30)}
	svc := newCheckInServiceForTest(repo, userRepo, nil)

	res, err := svc.Claim(context.Background(), 7)

	require.NoError(t, err)
	require.Equal(t, 4, res.Streak)
	require.Equal(t, 4, repo.claimInput.StreakCount)
}

func TestClaim_StreakResetsAfterGap(t *testing.T) {
	threeDaysAgo := timezone.Now().AddDate(0, 0, -3)
	repo := &fakeCheckInRepo{
		claimed: true,
		latest:  &CheckInRecord{CheckInDate: threeDaysAgo, StreakCount: 9},
	}
	userRepo := &fakeCheckInUserRepo{user: activeUser(30)}
	svc := newCheckInServiceForTest(repo, userRepo, nil)

	res, err := svc.Claim(context.Background(), 7)

	require.NoError(t, err)
	require.Equal(t, 1, res.Streak)
	require.Equal(t, 1, repo.claimInput.StreakCount)
}

func TestClaim_LostRaceReportsAlreadyClaimed(t *testing.T) {
	// Passed every pre-check, but the atomic insert hit the unique conflict.
	repo := &fakeCheckInRepo{claimed: false}
	userRepo := &fakeCheckInUserRepo{user: activeUser(30)}
	svc := newCheckInServiceForTest(repo, userRepo, nil)

	res, err := svc.Claim(context.Background(), 7)

	require.Nil(t, res)
	require.Equal(t, "CHECKIN_ALREADY_CLAIMED", infraerrors.Reason(err))
	require.Equal(t, 1, repo.claimCalls)
}

func TestClaim_MonthlyCapRejectsWhenReached(t *testing.T) {
	repo := &fakeCheckInRepo{claimed: true, monthSum: 10}
	userRepo := &fakeCheckInUserRepo{user: activeUser(30)}
	svc := newCheckInServiceForTest(repo, userRepo, map[string]string{SettingKeyCheckInUserMonthlyCap: "10"})

	res, err := svc.Claim(context.Background(), 7)

	require.Nil(t, res)
	require.Equal(t, "CHECKIN_MONTHLY_CAP_REACHED", infraerrors.Reason(err))
	require.True(t, repo.sumUserRewardsHit)
	require.Equal(t, 0, repo.claimCalls)
}

func TestClaim_DailyBudgetRejectsWhenExhausted(t *testing.T) {
	repo := &fakeCheckInRepo{claimed: true, todaySum: 100}
	userRepo := &fakeCheckInUserRepo{user: activeUser(30)}
	svc := newCheckInServiceForTest(repo, userRepo, map[string]string{SettingKeyCheckInDailyBudget: "100"})

	res, err := svc.Claim(context.Background(), 7)

	require.Nil(t, res)
	require.Equal(t, "CHECKIN_BUDGET_EXHAUSTED", infraerrors.Reason(err))
	require.True(t, repo.sumRewardsOnDateHit)
	require.Equal(t, 0, repo.claimCalls)
}

// ---------------------------------------------------------------------------
// GetStatus read-model tests
// ---------------------------------------------------------------------------

func TestGetStatus_CheckedInTodayBlocksClaim(t *testing.T) {
	now := timezone.Now()
	repo := &fakeCheckInRepo{
		history:  []CheckInRecord{{CheckInDate: now, StreakCount: 5, RewardAmount: 1.23}},
		lifetime: 9.99,
	}
	userRepo := &fakeCheckInUserRepo{user: activeUser(30)}
	svc := newCheckInServiceForTest(repo, userRepo, nil)

	st, err := svc.GetStatus(context.Background(), 7)

	require.NoError(t, err)
	require.True(t, st.Enabled)
	require.True(t, st.CheckedInToday)
	require.False(t, st.CanCheckIn)
	require.Equal(t, 5, st.Streak)
	require.NotNil(t, st.NextAvailableAt)
	require.Equal(t, 9.99, st.TotalReward)
	require.Equal(t, defaultCheckInMinReward, st.MinReward)
	require.Equal(t, defaultCheckInMaxReward, st.MaxReward)
}

func TestGetStatus_EligibleNotYetClaimed(t *testing.T) {
	yesterday := timezone.Now().AddDate(0, 0, -1)
	repo := &fakeCheckInRepo{
		history: []CheckInRecord{{CheckInDate: yesterday, StreakCount: 2, RewardAmount: 0.5}},
	}
	userRepo := &fakeCheckInUserRepo{user: activeUser(30)}
	svc := newCheckInServiceForTest(repo, userRepo, nil)

	st, err := svc.GetStatus(context.Background(), 7)

	require.NoError(t, err)
	require.False(t, st.CheckedInToday)
	require.True(t, st.CanCheckIn)
	require.Equal(t, 2, st.Streak, "yesterday's streak is still displayed before today's claim")
	require.Nil(t, st.NextAvailableAt)
}

func TestGetStatus_StreakDisplayResetsAfterGap(t *testing.T) {
	old := timezone.Now().AddDate(0, 0, -4)
	repo := &fakeCheckInRepo{
		history: []CheckInRecord{{CheckInDate: old, StreakCount: 12, RewardAmount: 2.0}},
	}
	userRepo := &fakeCheckInUserRepo{user: activeUser(30)}
	svc := newCheckInServiceForTest(repo, userRepo, nil)

	st, err := svc.GetStatus(context.Background(), 7)

	require.NoError(t, err)
	require.Equal(t, 0, st.Streak, "a stale streak must not be shown as current")
}

func TestGetStatus_DisabledCannotCheckIn(t *testing.T) {
	repo := &fakeCheckInRepo{}
	userRepo := &fakeCheckInUserRepo{user: activeUser(30)}
	svc := newCheckInServiceForTest(repo, userRepo, map[string]string{SettingKeyCheckInEnabled: "false"})

	st, err := svc.GetStatus(context.Background(), 7)

	require.NoError(t, err)
	require.False(t, st.Enabled)
	require.False(t, st.CanCheckIn)
}

// ---------------------------------------------------------------------------
// Admin ListRecords tests
// ---------------------------------------------------------------------------

func TestCheckInAdminListRecords_PassesFilterAndReturns(t *testing.T) {
	want := []CheckInRecordDetail{
		{ID: 1, UserID: 7, UserEmail: "a@b.com", UserUsername: "alice", RewardAmount: 1.5, StreakCount: 3},
	}
	repo := &fakeCheckInRepo{listRecords: want, listRecordsTotal: 42}
	svc := NewCheckInAdminService(nil, nil, repo)

	uid := int64(7)
	params := pagination.PaginationParams{Page: 2, PageSize: 10}
	filter := CheckInRecordFilter{UserID: &uid, StartDate: "2026-07-01", EndDate: "2026-07-07"}

	got, total, err := svc.ListRecords(context.Background(), params, filter)

	require.NoError(t, err)
	require.Equal(t, int64(42), total)
	require.Equal(t, want, got)
	// The service must pass pagination + filter straight through to the repo.
	require.Equal(t, params, repo.listRecordsParams)
	require.NotNil(t, repo.listRecordsFilter)
	require.Equal(t, filter, *repo.listRecordsFilter)
}
