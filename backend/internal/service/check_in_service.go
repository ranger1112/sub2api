package service

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// Daily check-in typed errors.
var (
	ErrCheckInDisabled          = infraerrors.Forbidden("CHECKIN_DISABLED", "daily check-in is currently disabled")
	ErrCheckInAlreadyClaimed    = infraerrors.Conflict("CHECKIN_ALREADY_CLAIMED", "already checked in today")
	ErrCheckInNotEligible       = infraerrors.Forbidden("CHECKIN_NOT_ELIGIBLE", "not eligible for daily check-in")
	ErrCheckInBudgetExhausted   = infraerrors.Conflict("CHECKIN_BUDGET_EXHAUSTED", "daily check-in budget exhausted, please try again tomorrow")
	ErrCheckInMonthlyCapReached = infraerrors.Conflict("CHECKIN_MONTHLY_CAP_REACHED", "monthly check-in reward cap reached")
)

// Daily check-in setting defaults (used when a setting is missing or unparseable).
const (
	defaultCheckInEnabled           = true
	defaultCheckInMinReward         = 0.01
	defaultCheckInMaxReward         = 5.00
	defaultCheckInBaseCap           = 0.20
	defaultCheckInWeightRecharge    = 0.5
	defaultCheckInWeightUsage       = 0.25
	defaultCheckInWeightActivity    = 0.25
	defaultCheckInRechargeCap       = 200.0
	defaultCheckInUsageCap          = 50.0
	defaultCheckInStreakCap         = 7
	defaultCheckInBetaMin           = 1.0
	defaultCheckInBetaMax           = 3.0
	defaultCheckInDailyBudget       = 0.0
	defaultCheckInUserMonthlyCap    = 0.0
	defaultCheckInMinAccountAgeDays = 0
	defaultCheckInRequireRecharge   = false

	// checkInAbsoluteMaxReward is a hard sanity backstop against fat-finger
	// misconfiguration of any admin-supplied max_reward (config or tier). No single
	// check-in reward may ever be configured above this ceiling.
	checkInAbsoluteMaxReward = 10000.0

	// checkInDateLayout is the DATE layout shared by app-timezone day boundaries.
	checkInDateLayout = "2006-01-02"
)

// CheckInRecord is the domain view of a single daily check-in row.
type CheckInRecord struct {
	ID               int64
	UserID           int64
	CheckInDate      time.Time
	RewardAmount     float64
	StreakCount      int
	Score            float64
	RechargeSnapshot float64
	UsageSnapshot    float64
	CreatedAt        time.Time
}

// ClaimInput carries the pre-computed claim payload into the atomic repository op.
type ClaimInput struct {
	UserID           int64
	CheckInDate      string // 'YYYY-MM-DD' in app timezone
	RewardAmount     float64
	StreakCount      int
	Score            float64
	RechargeSnapshot float64
	UsageSnapshot    float64
}

// CheckInRepository is the persistence port for daily check-in.
type CheckInRepository interface {
	// GetLatestRecord returns the newest record by check_in_date, or (nil, nil) if none.
	GetLatestRecord(ctx context.Context, userID int64) (*CheckInRecord, error)
	// ClaimDailyReward atomically inserts today's record and credits balance.
	// claimed=false (no error) means the user already claimed today (unique conflict).
	ClaimDailyReward(ctx context.Context, in ClaimInput) (newBalance float64, claimed bool, err error)
	// SumRewardsOnDate returns the global total reward granted on a given date.
	SumRewardsOnDate(ctx context.Context, dateStr string) (float64, error)
	// SumUserRewardsBetween returns a single user's total reward within [start,end].
	SumUserRewardsBetween(ctx context.Context, userID int64, startDateStr, endDateStr string) (float64, error)
	// SumRechargeByUser returns the user's lifetime successful balance recharge.
	SumRechargeByUser(ctx context.Context, userID int64) (float64, error)
	// SumUsageActualCostSince returns the user's actual usage cost since a timestamp.
	SumUsageActualCostSince(ctx context.Context, userID int64, since time.Time) (float64, error)
	// CountActiveDaysSince returns active days since a date; returns (0,nil) gracefully on any error.
	CountActiveDaysSince(ctx context.Context, userID int64, sinceDateStr string) (int, error)
	// ListByUser returns recent records (desc by date).
	ListByUser(ctx context.Context, userID int64, limit int) ([]CheckInRecord, error)
	// GetUserLifetimeReward returns the user's all-time total reward.
	GetUserLifetimeReward(ctx context.Context, userID int64) (float64, error)
	// GetAnalytics returns the admin analytics read model (totals, today/month, 30-day trend).
	GetAnalytics(ctx context.Context, todayStr, monthStartStr, trendStartStr string) (*CheckInAnalytics, error)
	// ListRecords returns a paginated, filtered page of individual check-in records joined to users.
	ListRecords(ctx context.Context, params pagination.PaginationParams, filter CheckInRecordFilter) ([]CheckInRecordDetail, int64, error)
}

// checkInConfig is the resolved runtime configuration for a single request.
type checkInConfig struct {
	enabled           bool
	minReward         float64
	maxReward         float64
	baseCap           float64
	weightRecharge    float64
	weightUsage       float64
	weightActivity    float64
	rechargeCap       float64
	usageCap          float64
	streakCap         int
	betaMin           float64
	betaMax           float64
	dailyBudget       float64
	userMonthlyCap    float64
	minAccountAgeDays int
	requireRecharge   bool
}

// ClaimResult is returned to the handler after a successful claim.
type ClaimResult struct {
	RewardAmount float64
	NewBalance   float64
	Streak       int
	CheckInDate  string
}

// CheckInHistoryItem is one row of check-in history for the status view.
type CheckInHistoryItem struct {
	Date   string
	Reward float64
	Streak int
}

// StatusResult is the read model for GET /checkin.
type StatusResult struct {
	Enabled         bool
	CheckedInToday  bool
	CanCheckIn      bool
	Streak          int
	LastReward      *float64
	LastCheckInDate *string
	TotalReward     float64
	NextAvailableAt *time.Time
	MinReward       float64
	MaxReward       float64
	History         []CheckInHistoryItem
}

// CheckInService implements the daily check-in reward feature.
type CheckInService struct {
	checkInRepo          CheckInRepository
	tierRepo             CheckInRewardTierRepository
	userRepo             UserRepository
	settingRepo          SettingRepository
	billingCacheService  *BillingCacheService
	authCacheInvalidator APIKeyAuthCacheInvalidator
}

// NewCheckInService creates a CheckInService.
func NewCheckInService(
	checkInRepo CheckInRepository,
	tierRepo CheckInRewardTierRepository,
	userRepo UserRepository,
	settingRepo SettingRepository,
	billingCacheService *BillingCacheService,
	authCacheInvalidator APIKeyAuthCacheInvalidator,
) *CheckInService {
	return &CheckInService{
		checkInRepo:          checkInRepo,
		tierRepo:             tierRepo,
		userRepo:             userRepo,
		settingRepo:          settingRepo,
		billingCacheService:  billingCacheService,
		authCacheInvalidator: authCacheInvalidator,
	}
}

// Claim performs a full daily check-in: eligibility, streak, reward, budget guardrails,
// atomic credit, and cache invalidation.
func (s *CheckInService) Claim(ctx context.Context, userID int64) (*ClaimResult, error) {
	cfg := s.loadConfig(ctx)
	if !cfg.enabled {
		return nil, ErrCheckInDisabled
	}

	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	now := timezone.Now()

	// Recharge is needed both for the eligibility gate and as a score input / snapshot.
	recharge, err := s.checkInRepo.SumRechargeByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("sum recharge: %w", err)
	}
	if !checkEligibility(cfg, user, recharge, now) {
		return nil, ErrCheckInNotEligible
	}

	todayStr := now.Format(checkInDateLayout)
	latest, err := s.checkInRepo.GetLatestRecord(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get latest check-in: %w", err)
	}
	if latest != nil && latest.CheckInDate.Format(checkInDateLayout) == todayStr {
		return nil, ErrCheckInAlreadyClaimed
	}

	// Resolve the per-user reward context (streak, score inputs, effective tier band).
	// Shared with GetStatus so eligibility and the advertised band can't drift from
	// what a claim actually draws.
	rc, err := s.computeUserRewardContext(ctx, cfg, userID, now, latest, recharge)
	if err != nil {
		return nil, err
	}

	reward, err := computeCheckInReward(rc.eff, rc.score)
	if err != nil {
		return nil, fmt.Errorf("compute reward: %w", err)
	}

	// Budget guardrails are SOFT caps: concurrent claims across users may marginally
	// overshoot the configured budgets because the check + insert are not globally
	// serialized. This is acceptable for a promotional give-away.
	if cfg.userMonthlyCap > 0 {
		firstOfMonthStr := timezone.StartOfMonth(now).Format(checkInDateLayout)
		monthSum, err := s.checkInRepo.SumUserRewardsBetween(ctx, userID, firstOfMonthStr, todayStr)
		if err != nil {
			return nil, fmt.Errorf("sum monthly rewards: %w", err)
		}
		if monthSum >= cfg.userMonthlyCap {
			return nil, ErrCheckInMonthlyCapReached
		}
		if remaining := cfg.userMonthlyCap - monthSum; reward > remaining {
			reward = remaining
		}
		if reward < rc.eff.minReward {
			return nil, ErrCheckInMonthlyCapReached
		}
	}
	if cfg.dailyBudget > 0 {
		todaySum, err := s.checkInRepo.SumRewardsOnDate(ctx, todayStr)
		if err != nil {
			return nil, fmt.Errorf("sum daily rewards: %w", err)
		}
		if todaySum >= cfg.dailyBudget {
			return nil, ErrCheckInBudgetExhausted
		}
		if remaining := cfg.dailyBudget - todaySum; reward > remaining {
			reward = remaining
		}
		if reward < rc.eff.minReward {
			return nil, ErrCheckInBudgetExhausted
		}
	}
	// Re-round to cents after any budget trimming, then re-clamp so the final
	// credited amount is unconditionally within the RESOLVED tier band [minReward,
	// maxReward] (> 0). rc.eff.maxReward is already <= cfg.maxReward (global clamp in
	// resolveRewardBand), so the global ceiling still holds; using rc.eff prevents a
	// sub-global-min tier from being bumped up above its own ceiling and a
	// budget-trimmed amount from being re-inflated above the remaining budget.
	reward = math.Round(reward*100) / 100
	reward = clampFloat64(reward, rc.eff.minReward, rc.eff.maxReward)

	newBalance, claimed, err := s.checkInRepo.ClaimDailyReward(ctx, ClaimInput{
		UserID:           userID,
		CheckInDate:      todayStr,
		RewardAmount:     reward,
		StreakCount:      rc.newStreak,
		Score:            rc.score,
		RechargeSnapshot: recharge,
		UsageSnapshot:    rc.usage30d,
	})
	if err != nil {
		return nil, fmt.Errorf("claim daily reward: %w", err)
	}
	if !claimed {
		// Lost the race: someone (another request) inserted today's row first.
		return nil, ErrCheckInAlreadyClaimed
	}

	s.invalidateCheckInCaches(ctx, userID)

	return &ClaimResult{
		RewardAmount: reward,
		NewBalance:   newBalance,
		Streak:       rc.newStreak,
		CheckInDate:  todayStr,
	}, nil
}

// GetStatus returns the cheap read model for the check-in page (no RNG, no budget query).
func (s *CheckInService) GetStatus(ctx context.Context, userID int64) (*StatusResult, error) {
	cfg := s.loadConfig(ctx)

	// Disabled: return a minimal read model and skip all per-user queries.
	if !cfg.enabled {
		return &StatusResult{
			Enabled:   false,
			MinReward: cfg.minReward,
			MaxReward: cfg.maxReward,
			History:   []CheckInHistoryItem{},
		}, nil
	}

	now := timezone.Now()
	todayStr := now.Format(checkInDateLayout)
	yesterdayStr := now.AddDate(0, 0, -1).Format(checkInDateLayout)

	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}

	// Recharge feeds both the eligibility gate and the effective-band resolution.
	recharge, err := s.checkInRepo.SumRechargeByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("sum recharge: %w", err)
	}
	eligible := checkEligibility(cfg, user, recharge, now)

	// Derive the latest record from the history list (records[0]) rather than a
	// separate GetLatestRecord query — the history is ordered desc by check_in_date.
	records, err := s.checkInRepo.ListByUser(ctx, userID, 20)
	if err != nil {
		return nil, fmt.Errorf("list check-in history: %w", err)
	}
	var latest *CheckInRecord
	if len(records) > 0 {
		latest = &records[0]
	}
	checkedToday := latest != nil && latest.CheckInDate.Format(checkInDateLayout) == todayStr

	// Effective band: advertise the range a claim would actually draw for this user.
	rc, err := s.computeUserRewardContext(ctx, cfg, userID, now, latest, recharge)
	if err != nil {
		return nil, err
	}

	streak := 0
	var lastReward *float64
	var lastCheckInDate *string
	if latest != nil {
		latestDateStr := latest.CheckInDate.Format(checkInDateLayout)
		if latestDateStr == todayStr || latestDateStr == yesterdayStr {
			streak = latest.StreakCount
		}
		reward := latest.RewardAmount
		lastReward = &reward
		date := latestDateStr
		lastCheckInDate = &date
	}

	totalReward, err := s.checkInRepo.GetUserLifetimeReward(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get lifetime reward: %w", err)
	}

	var nextAvailableAt *time.Time
	if checkedToday {
		next := timezone.StartOfDay(now.AddDate(0, 0, 1))
		nextAvailableAt = &next
	}

	history := make([]CheckInHistoryItem, 0, len(records))
	for i := range records {
		history = append(history, CheckInHistoryItem{
			Date:   records[i].CheckInDate.Format(checkInDateLayout),
			Reward: records[i].RewardAmount,
			Streak: records[i].StreakCount,
		})
	}

	return &StatusResult{
		Enabled:         cfg.enabled,
		CheckedInToday:  checkedToday,
		CanCheckIn:      cfg.enabled && !checkedToday && eligible,
		Streak:          streak,
		LastReward:      lastReward,
		LastCheckInDate: lastCheckInDate,
		TotalReward:     totalReward,
		NextAvailableAt: nextAvailableAt,
		MinReward:       rc.eff.minReward,
		MaxReward:       rc.eff.maxReward,
		History:         history,
	}, nil
}

// userRewardContext bundles the per-user inputs shared by Claim (the reward draw)
// and GetStatus (the advertised band): the resolved effective tier band plus the
// raw score inputs. Computing it once keeps the two paths from drifting.
type userRewardContext struct {
	eff       checkInConfig
	recharge  float64
	usage30d  float64
	newStreak int
	score     float64
}

// checkEligibility applies the three shared claim gates — active status, minimum
// account age, and optional require-recharge — so Claim and GetStatus can't drift.
func checkEligibility(cfg checkInConfig, user *User, recharge float64, now time.Time) bool {
	if user.Status != StatusActive {
		return false
	}
	if cfg.minAccountAgeDays > 0 {
		ageDays := int(now.Sub(user.CreatedAt).Hours() / 24)
		if ageDays < cfg.minAccountAgeDays {
			return false
		}
	}
	if cfg.requireRecharge && recharge <= 0 {
		return false
	}
	return true
}

// computeUserRewardContext derives the streak, loads the score inputs (usage +
// active days), computes the composite score, and overlays the best-matching enabled
// reward tier onto cfg. Best-effort: a tier-table error never breaks the flow (it
// logs and keeps the global config), and CountActiveDaysSince already fails soft.
func (s *CheckInService) computeUserRewardContext(ctx context.Context, cfg checkInConfig, userID int64, now time.Time, latest *CheckInRecord, recharge float64) (userRewardContext, error) {
	yesterdayStr := now.AddDate(0, 0, -1).Format(checkInDateLayout)
	newStreak := 1
	if latest != nil && latest.CheckInDate.Format(checkInDateLayout) == yesterdayStr {
		newStreak = latest.StreakCount + 1
	}

	usage30d, err := s.checkInRepo.SumUsageActualCostSince(ctx, userID, now.AddDate(0, 0, -30))
	if err != nil {
		return userRewardContext{}, fmt.Errorf("sum usage: %w", err)
	}
	// Active-days window: last 7 days including today (today-6days .. today).
	windowStartStr := now.AddDate(0, 0, -6).Format(checkInDateLayout)
	activeDays, aerr := s.checkInRepo.CountActiveDaysSince(ctx, userID, windowStartStr)
	if aerr != nil {
		activeDays = 0
	}

	score := computeCheckInScore(cfg, recharge, usage30d, newStreak, activeDays)

	var tiers []CheckInRewardTier
	if s.tierRepo != nil {
		loaded, terr := s.tierRepo.ListEnabled(ctx)
		if terr != nil {
			logger.LegacyPrintf("service.checkin", "[CheckIn] list enabled reward tiers failed, using global config: %v", terr)
		} else {
			tiers = loaded
		}
	}
	eff := s.resolveRewardBand(cfg, tiers, recharge, score)

	return userRewardContext{
		eff:       eff,
		recharge:  recharge,
		usage30d:  usage30d,
		newStreak: newStreak,
		score:     score,
	}, nil
}

// invalidateCheckInCaches mirrors the balance branch of RedeemService.invalidateRedeemCaches:
// synchronously drop the API-key auth cache, then async-invalidate the billing balance cache.
func (s *CheckInService) invalidateCheckInCaches(ctx context.Context, userID int64) {
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	if s.billingCacheService == nil {
		return
	}
	go func() {
		cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.billingCacheService.InvalidateUserBalance(cacheCtx, userID)
	}()
}

// computeCheckInScore implements the composite score S in [0,1].
//
//	R = clamp( ln(1+recharge) / ln(1+rechargeCap), 0, 1 )
//	U = clamp( ln(1+usage30d) / ln(1+usageCap),   0, 1 )
//	A = 0.5*min(streak,streakCap)/streakCap + 0.5*min(activeDays,7)/7
//	S = clamp( wR*R + wU*U + wA*A, 0, 1 )
func computeCheckInScore(cfg checkInConfig, recharge, usage30d float64, newStreak, activeDays int) float64 {
	// Defensive: recharge and usage30d are non-negative sums. A corrupt/negative
	// aggregate must never reach math.Log (which would yield NaN and could taint a
	// credited balance), so clamp them to >= 0 before the log-scaled terms.
	if recharge < 0 {
		recharge = 0
	}
	if usage30d < 0 {
		usage30d = 0
	}
	R := 0.0
	if cfg.rechargeCap > 0 {
		R = clampFloat64(math.Log(1+recharge)/math.Log(1+cfg.rechargeCap), 0, 1)
	}
	U := 0.0
	if cfg.usageCap > 0 {
		U = clampFloat64(math.Log(1+usage30d)/math.Log(1+cfg.usageCap), 0, 1)
	}

	streakCap := cfg.streakCap
	if streakCap <= 0 {
		streakCap = 1
	}
	streakClamped := newStreak
	if streakClamped > streakCap {
		streakClamped = streakCap
	}
	if streakClamped < 0 {
		streakClamped = 0
	}
	activeClamped := activeDays
	if activeClamped > 7 {
		activeClamped = 7
	}
	if activeClamped < 0 {
		activeClamped = 0
	}
	A := 0.5*float64(streakClamped)/float64(streakCap) + 0.5*float64(activeClamped)/7.0

	return clampFloat64(cfg.weightRecharge*R+cfg.weightUsage*U+cfg.weightActivity*A, 0, 1)
}

// computeCheckInReward maps the score to a cents-rounded reward via a power-law draw.
//
//	C = baseCap + (maxReward - baseCap) * S
//	beta = betaMax - (betaMax - betaMin) * S
//	reward = minReward + (C - minReward) * pow(u, beta)   with u ~ U[0,1)
//	reward = clamp(round2(reward), minReward, maxReward)
func computeCheckInReward(cfg checkInConfig, score float64) (float64, error) {
	c := cfg.baseCap + (cfg.maxReward-cfg.baseCap)*score
	beta := cfg.betaMax - (cfg.betaMax-cfg.betaMin)*score
	u, err := cryptoRandFloat64()
	if err != nil {
		return 0, err
	}
	reward := cfg.minReward + (c-cfg.minReward)*math.Pow(u, beta)
	// Belt-and-suspenders: a NaN/Inf draw (e.g. from a degenerate config that slipped
	// past sanitize) must never reach the balance; fall back to the floor before clamp.
	if math.IsNaN(reward) || math.IsInf(reward, 0) {
		reward = cfg.minReward
	}
	reward = math.Round(reward*100) / 100
	// Guarantee reward > 0 and <= maxReward.
	reward = clampFloat64(reward, cfg.minReward, cfg.maxReward)
	return reward, nil
}

// resolveRewardBand selects the best-matching enabled tier and returns an effective
// config with the tier's reward-band parameters overlaid on cfg. The tier's maxReward
// is globally clamped so a tier can never lift the ceiling above the global max. If no
// tier matches, cfg is returned unchanged.
func (s *CheckInService) resolveRewardBand(cfg checkInConfig, tiers []CheckInRewardTier, recharge, score float64) checkInConfig {
	var best *CheckInRewardTier
	for i := range tiers {
		t := &tiers[i]
		if !t.Enabled {
			continue
		}
		matched := (t.MatchType == CheckInTierMatchRecharge && recharge >= t.MatchThreshold) ||
			(t.MatchType == CheckInTierMatchScore && score >= t.MatchThreshold)
		if !matched {
			continue
		}
		if best == nil || checkInTierBetter(t, best) {
			best = t
		}
	}
	if best == nil {
		return cfg
	}

	eff := cfg
	eff.minReward = best.MinReward
	// GLOBAL SAFETY CLAMP: a tier can never lift the ceiling above the global max.
	eff.maxReward = math.Min(best.MaxReward, cfg.maxReward)
	// If the tier's min exceeds the globally-clamped ceiling, pin min to that ceiling.
	// Otherwise sanitize() below would see max < min and rewrite max back up to the
	// default (5.0), silently defeating the global clamp we just applied.
	if eff.minReward > eff.maxReward {
		eff.minReward = eff.maxReward
	}
	eff.baseCap = best.BaseCap
	eff.betaMin = best.BetaMin
	eff.betaMax = best.BetaMax
	eff.sanitize()
	return eff
}

// checkInTierBetter reports whether tier a outranks tier b for band selection.
// Explicit admin priority (sort_order) is compared first so recharge- and score-typed
// tiers — whose thresholds live on different scales (dollars vs a 0..1 score) — can be
// ordered deterministically by the admin rather than by raw threshold magnitude (which
// would let any recharge tier always outrank any score tier). Ties break on larger
// match_threshold (more specific within a type), then larger id.
func checkInTierBetter(a, b *CheckInRewardTier) bool {
	if a.SortOrder != b.SortOrder {
		return a.SortOrder > b.SortOrder
	}
	// Thresholds are only comparable within the same match_type: recharge thresholds
	// are dollars while score thresholds live on a 0..1 scale, so ranking across types
	// by raw magnitude is meaningless. Cross-type ties fall through to id.
	if a.MatchType == b.MatchType && a.MatchThreshold != b.MatchThreshold {
		return a.MatchThreshold > b.MatchThreshold
	}
	return a.ID > b.ID
}

// cryptoRandFloat64 returns a cryptographically-random float in [0,1).
// It reads 8 bytes, keeps the top 53 bits (float64 mantissa), and divides by 2^53.
func cryptoRandFloat64() (float64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	v := binary.BigEndian.Uint64(b[:]) >> 11
	return float64(v) / float64(uint64(1)<<53), nil
}

// loadConfig resolves all check-in settings in a single GetMultiple round-trip,
// falling back to defaults on missing/invalid values. Parsing reuses the map-based
// parsers in checkin_reward_tier.go (one source of truth for setting parsing).
func (s *CheckInService) loadConfig(ctx context.Context) checkInConfig {
	var vals map[string]string
	if s.settingRepo != nil {
		if loaded, err := s.settingRepo.GetMultiple(ctx, checkInSettingKeys()); err == nil {
			vals = loaded
		}
	}
	cfg := checkInConfig{
		enabled:           checkInBoolValue(vals, SettingKeyCheckInEnabled, defaultCheckInEnabled),
		minReward:         checkInFloatValue(vals, SettingKeyCheckInMinReward, defaultCheckInMinReward),
		maxReward:         checkInFloatValue(vals, SettingKeyCheckInMaxReward, defaultCheckInMaxReward),
		baseCap:           checkInFloatValue(vals, SettingKeyCheckInBaseCap, defaultCheckInBaseCap),
		weightRecharge:    checkInFloatValue(vals, SettingKeyCheckInWeightRecharge, defaultCheckInWeightRecharge),
		weightUsage:       checkInFloatValue(vals, SettingKeyCheckInWeightUsage, defaultCheckInWeightUsage),
		weightActivity:    checkInFloatValue(vals, SettingKeyCheckInWeightActivity, defaultCheckInWeightActivity),
		rechargeCap:       checkInFloatValue(vals, SettingKeyCheckInRechargeCap, defaultCheckInRechargeCap),
		usageCap:          checkInFloatValue(vals, SettingKeyCheckInUsageCap, defaultCheckInUsageCap),
		streakCap:         checkInIntValue(vals, SettingKeyCheckInStreakCap, defaultCheckInStreakCap),
		betaMin:           checkInFloatValue(vals, SettingKeyCheckInBetaMin, defaultCheckInBetaMin),
		betaMax:           checkInFloatValue(vals, SettingKeyCheckInBetaMax, defaultCheckInBetaMax),
		dailyBudget:       checkInFloatValue(vals, SettingKeyCheckInDailyBudget, defaultCheckInDailyBudget),
		userMonthlyCap:    checkInFloatValue(vals, SettingKeyCheckInUserMonthlyCap, defaultCheckInUserMonthlyCap),
		minAccountAgeDays: checkInIntValue(vals, SettingKeyCheckInMinAccountAgeDays, defaultCheckInMinAccountAgeDays),
		requireRecharge:   checkInBoolValue(vals, SettingKeyCheckInRequireRecharge, defaultCheckInRequireRecharge),
	}
	cfg.sanitize()
	return cfg
}

// sanitize hardens the reward-bound invariant against admin misconfiguration.
// Since a reward is ALWAYS credited (raw balance UPDATE), the "0 < reward <= maxReward"
// guarantee must hold in code, not rely on settings being sane. Without this, a
// negative min_reward would DEDUCT balance on check-in, and min > max would credit
// above the intended ceiling.
func (c *checkInConfig) sanitize() {
	if c.minReward <= 0 {
		c.minReward = defaultCheckInMinReward
	}
	// Absolute backstop against fat-finger misconfig; applied before the min/max repair
	// below so the max >= min invariant still holds afterward.
	c.maxReward = math.Min(c.maxReward, checkInAbsoluteMaxReward)
	if c.maxReward < c.minReward {
		// Prefer the sane default ceiling; if minReward itself exceeds it, collapse to a point.
		c.maxReward = defaultCheckInMaxReward
	}
	if c.maxReward < c.minReward {
		c.maxReward = c.minReward
	}
	// baseCap drives C = baseCap + (maxReward-baseCap)*S; keep it inside the reward band.
	c.baseCap = clampFloat64(c.baseCap, c.minReward, c.maxReward)
	// Power-law exponents only shape the distribution, but keep them non-negative and
	// ordered so pow(u, beta) can't blow the draw past the final clamp.
	if c.betaMin < 0 {
		c.betaMin = 0
	}
	if c.betaMax < c.betaMin {
		c.betaMax = c.betaMin
	}
}
