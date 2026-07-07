package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// Check-in reward tier match dimensions.
const (
	CheckInTierMatchRecharge = "recharge"
	CheckInTierMatchScore    = "score"
)

// Daily check-in admin typed errors.
var (
	ErrCheckInTierNotFound = infraerrors.NotFound("CHECKIN_TIER_NOT_FOUND", "check-in reward tier not found")
)

// CheckInRewardTier is the domain view of a single reward-tier row.
type CheckInRewardTier struct {
	ID             int64
	Name           string
	Enabled        bool
	MatchType      string
	MatchThreshold float64
	MinReward      float64
	MaxReward      float64
	BaseCap        float64
	BetaMin        float64
	BetaMax        float64
	SortOrder      int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// CreateCheckInTierInput is the create payload for a reward tier.
type CreateCheckInTierInput struct {
	Name           string
	Enabled        bool
	MatchType      string
	MatchThreshold float64
	MinReward      float64
	MaxReward      float64
	BaseCap        float64
	BetaMin        float64
	BetaMax        float64
	SortOrder      int
}

// UpdateCheckInTierInput is the partial-update payload for a reward tier.
type UpdateCheckInTierInput struct {
	Name           *string
	Enabled        *bool
	MatchType      *string
	MatchThreshold *float64
	MinReward      *float64
	MaxReward      *float64
	BaseCap        *float64
	BetaMin        *float64
	BetaMax        *float64
	SortOrder      *int
}

// CheckInRewardTierRepository is the persistence port for reward tiers.
type CheckInRewardTierRepository interface {
	Create(ctx context.Context, tier *CheckInRewardTier) error
	GetByID(ctx context.Context, id int64) (*CheckInRewardTier, error)
	Update(ctx context.Context, tier *CheckInRewardTier) error
	Delete(ctx context.Context, id int64) error
	// List returns all tiers ordered by sort_order then id.
	List(ctx context.Context) ([]CheckInRewardTier, error)
	// ListEnabled returns only enabled tiers, ordered by sort_order then id.
	ListEnabled(ctx context.Context) ([]CheckInRewardTier, error)
}

// CheckInConfigValues is the read/write view of all 16 daily check-in settings.
type CheckInConfigValues struct {
	Enabled           bool
	MinReward         float64
	MaxReward         float64
	BaseCap           float64
	WeightRecharge    float64
	WeightUsage       float64
	WeightActivity    float64
	RechargeCap       float64
	UsageCap          float64
	StreakCap         int
	BetaMin           float64
	BetaMax           float64
	DailyBudget       float64
	UserMonthlyCap    float64
	MinAccountAgeDays int
	RequireRecharge   bool
}

// CheckInTrendPoint is one day of the check-in analytics trend.
type CheckInTrendPoint struct {
	Date   string  `json:"date"`
	Gifted float64 `json:"gifted"`
	Count  int64   `json:"count"`
}

// CheckInAnalytics is the admin analytics read model for daily check-in.
type CheckInAnalytics struct {
	TotalGifted        float64
	TodayGifted        float64
	MonthGifted        float64
	TotalCheckins      int64
	TodayCheckins      int64
	DistinctUsersToday int64
	DistinctUsersMonth int64
	Trend              []CheckInTrendPoint
}

// CheckInRecordFilter narrows a ListRecords query. All fields are optional; empty
// values mean "no filter on this dimension".
type CheckInRecordFilter struct {
	UserID    *int64 // optional exact user_id match
	StartDate string // optional 'YYYY-MM-DD' inclusive lower bound on check_in_date
	EndDate   string // optional 'YYYY-MM-DD' inclusive upper bound on check_in_date
}

// CheckInRecordDetail is one check_in_records row joined to its user for the admin
// records listing (who got how much, when). User email/username are '' when the
// joined user is missing.
type CheckInRecordDetail struct {
	ID               int64
	UserID           int64
	UserEmail        string
	UserUsername     string
	CheckInDate      time.Time
	RewardAmount     float64
	StreakCount      int
	Score            float64
	RechargeSnapshot float64
	UsageSnapshot    float64
	CreatedAt        time.Time
}

// CheckInAdminService backs the admin-facing check-in config, analytics and tier CRUD.
type CheckInAdminService struct {
	tierRepo    CheckInRewardTierRepository
	settingRepo SettingRepository
	checkInRepo CheckInRepository
}

// NewCheckInAdminService creates a CheckInAdminService.
func NewCheckInAdminService(
	tierRepo CheckInRewardTierRepository,
	settingRepo SettingRepository,
	checkInRepo CheckInRepository,
) *CheckInAdminService {
	return &CheckInAdminService{
		tierRepo:    tierRepo,
		settingRepo: settingRepo,
		checkInRepo: checkInRepo,
	}
}

// ---------------------------------------------------------------------------
// Reward-tier CRUD
// ---------------------------------------------------------------------------

// ListTiers returns all reward tiers ordered by sort_order then id.
func (s *CheckInAdminService) ListTiers(ctx context.Context) ([]CheckInRewardTier, error) {
	return s.tierRepo.List(ctx)
}

// CreateTier validates and persists a new reward tier.
func (s *CheckInAdminService) CreateTier(ctx context.Context, input *CreateCheckInTierInput) (*CheckInRewardTier, error) {
	tier := &CheckInRewardTier{
		Name:           strings.TrimSpace(input.Name),
		Enabled:        input.Enabled,
		MatchType:      strings.TrimSpace(input.MatchType),
		MatchThreshold: input.MatchThreshold,
		MinReward:      input.MinReward,
		MaxReward:      input.MaxReward,
		BaseCap:        input.BaseCap,
		BetaMin:        input.BetaMin,
		BetaMax:        input.BetaMax,
		SortOrder:      input.SortOrder,
	}
	if err := validateCheckInTier(tier); err != nil {
		return nil, err
	}
	if err := s.tierRepo.Create(ctx, tier); err != nil {
		return nil, fmt.Errorf("create check-in reward tier: %w", err)
	}
	return tier, nil
}

// UpdateTier applies a partial update, validates the merged tier, and persists it.
func (s *CheckInAdminService) UpdateTier(ctx context.Context, id int64, input *UpdateCheckInTierInput) (*CheckInRewardTier, error) {
	tier, err := s.tierRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if input.Name != nil {
		tier.Name = strings.TrimSpace(*input.Name)
	}
	if input.Enabled != nil {
		tier.Enabled = *input.Enabled
	}
	if input.MatchType != nil {
		tier.MatchType = strings.TrimSpace(*input.MatchType)
	}
	if input.MatchThreshold != nil {
		tier.MatchThreshold = *input.MatchThreshold
	}
	if input.MinReward != nil {
		tier.MinReward = *input.MinReward
	}
	if input.MaxReward != nil {
		tier.MaxReward = *input.MaxReward
	}
	if input.BaseCap != nil {
		tier.BaseCap = *input.BaseCap
	}
	if input.BetaMin != nil {
		tier.BetaMin = *input.BetaMin
	}
	if input.BetaMax != nil {
		tier.BetaMax = *input.BetaMax
	}
	if input.SortOrder != nil {
		tier.SortOrder = *input.SortOrder
	}

	if err := validateCheckInTier(tier); err != nil {
		return nil, err
	}
	if err := s.tierRepo.Update(ctx, tier); err != nil {
		return nil, fmt.Errorf("update check-in reward tier: %w", err)
	}
	return tier, nil
}

// DeleteTier removes a reward tier by id.
func (s *CheckInAdminService) DeleteTier(ctx context.Context, id int64) error {
	if err := s.tierRepo.Delete(ctx, id); err != nil {
		return fmt.Errorf("delete check-in reward tier: %w", err)
	}
	return nil
}

// validateRewardBand enforces the five shared reward-band invariants (plus the
// absolute-max backstop) common to both a tier and the global config. The caller
// supplies its own reason code so tier and config violations stay distinguishable.
func validateRewardBand(reason string, minReward, maxReward, baseCap, betaMin, betaMax float64) error {
	if minReward <= 0 {
		return infraerrors.BadRequest(reason, "min_reward must be > 0")
	}
	if maxReward < minReward {
		return infraerrors.BadRequest(reason, "max_reward must be >= min_reward")
	}
	if maxReward > checkInAbsoluteMaxReward {
		return infraerrors.BadRequest(reason, "max_reward exceeds the absolute maximum")
	}
	if baseCap < minReward || baseCap > maxReward {
		return infraerrors.BadRequest(reason, "base_cap must be within [min_reward, max_reward]")
	}
	if betaMin < 0 {
		return infraerrors.BadRequest(reason, "beta_min must be >= 0")
	}
	if betaMax < betaMin {
		return infraerrors.BadRequest(reason, "beta_max must be >= beta_min")
	}
	return nil
}

// validateCheckInTier enforces the reward-band invariants for a tier.
func validateCheckInTier(t *CheckInRewardTier) error {
	if strings.TrimSpace(t.Name) == "" {
		return infraerrors.BadRequest("CHECKIN_TIER_INVALID", "name must not be empty")
	}
	if t.MatchType != CheckInTierMatchRecharge && t.MatchType != CheckInTierMatchScore {
		return infraerrors.BadRequest("CHECKIN_TIER_INVALID", "match_type must be 'recharge' or 'score'")
	}
	if t.MatchThreshold < 0 {
		return infraerrors.BadRequest("CHECKIN_TIER_INVALID", "match_threshold must be >= 0")
	}
	return validateRewardBand("CHECKIN_TIER_INVALID", t.MinReward, t.MaxReward, t.BaseCap, t.BetaMin, t.BetaMax)
}

// ---------------------------------------------------------------------------
// Admin config get/put
// ---------------------------------------------------------------------------

// checkInSettingKeys is the canonical ordered list of the 16 check-in setting keys.
func checkInSettingKeys() []string {
	return []string{
		SettingKeyCheckInEnabled,
		SettingKeyCheckInMinReward,
		SettingKeyCheckInMaxReward,
		SettingKeyCheckInBaseCap,
		SettingKeyCheckInWeightRecharge,
		SettingKeyCheckInWeightUsage,
		SettingKeyCheckInWeightActivity,
		SettingKeyCheckInRechargeCap,
		SettingKeyCheckInUsageCap,
		SettingKeyCheckInStreakCap,
		SettingKeyCheckInBetaMin,
		SettingKeyCheckInBetaMax,
		SettingKeyCheckInDailyBudget,
		SettingKeyCheckInUserMonthlyCap,
		SettingKeyCheckInMinAccountAgeDays,
		SettingKeyCheckInRequireRecharge,
	}
}

// GetCheckInConfig reads all 16 settings, defaulting any missing/invalid value.
func (s *CheckInAdminService) GetCheckInConfig(ctx context.Context) (*CheckInConfigValues, error) {
	vals, err := s.settingRepo.GetMultiple(ctx, checkInSettingKeys())
	if err != nil {
		return nil, fmt.Errorf("get check-in config: %w", err)
	}
	return &CheckInConfigValues{
		Enabled:           checkInBoolValue(vals, SettingKeyCheckInEnabled, defaultCheckInEnabled),
		MinReward:         checkInFloatValue(vals, SettingKeyCheckInMinReward, defaultCheckInMinReward),
		MaxReward:         checkInFloatValue(vals, SettingKeyCheckInMaxReward, defaultCheckInMaxReward),
		BaseCap:           checkInFloatValue(vals, SettingKeyCheckInBaseCap, defaultCheckInBaseCap),
		WeightRecharge:    checkInFloatValue(vals, SettingKeyCheckInWeightRecharge, defaultCheckInWeightRecharge),
		WeightUsage:       checkInFloatValue(vals, SettingKeyCheckInWeightUsage, defaultCheckInWeightUsage),
		WeightActivity:    checkInFloatValue(vals, SettingKeyCheckInWeightActivity, defaultCheckInWeightActivity),
		RechargeCap:       checkInFloatValue(vals, SettingKeyCheckInRechargeCap, defaultCheckInRechargeCap),
		UsageCap:          checkInFloatValue(vals, SettingKeyCheckInUsageCap, defaultCheckInUsageCap),
		StreakCap:         checkInIntValue(vals, SettingKeyCheckInStreakCap, defaultCheckInStreakCap),
		BetaMin:           checkInFloatValue(vals, SettingKeyCheckInBetaMin, defaultCheckInBetaMin),
		BetaMax:           checkInFloatValue(vals, SettingKeyCheckInBetaMax, defaultCheckInBetaMax),
		DailyBudget:       checkInFloatValue(vals, SettingKeyCheckInDailyBudget, defaultCheckInDailyBudget),
		UserMonthlyCap:    checkInFloatValue(vals, SettingKeyCheckInUserMonthlyCap, defaultCheckInUserMonthlyCap),
		MinAccountAgeDays: checkInIntValue(vals, SettingKeyCheckInMinAccountAgeDays, defaultCheckInMinAccountAgeDays),
		RequireRecharge:   checkInBoolValue(vals, SettingKeyCheckInRequireRecharge, defaultCheckInRequireRecharge),
	}, nil
}

// UpdateCheckInConfig validates the payload, hardens it via checkInConfig.sanitize()
// for defense-in-depth, then persists all 16 keys atomically via SetMultiple.
func (s *CheckInAdminService) UpdateCheckInConfig(ctx context.Context, cv *CheckInConfigValues) error {
	if err := validateCheckInConfig(cv); err != nil {
		return err
	}

	// Defense-in-depth: run the same runtime sanitizer used on the read path so a
	// value that passed validation but is still degenerate (e.g. baseCap on the
	// boundary) is stored already clamped.
	cfg := checkInConfig{
		enabled:           cv.Enabled,
		minReward:         cv.MinReward,
		maxReward:         cv.MaxReward,
		baseCap:           cv.BaseCap,
		weightRecharge:    cv.WeightRecharge,
		weightUsage:       cv.WeightUsage,
		weightActivity:    cv.WeightActivity,
		rechargeCap:       cv.RechargeCap,
		usageCap:          cv.UsageCap,
		streakCap:         cv.StreakCap,
		betaMin:           cv.BetaMin,
		betaMax:           cv.BetaMax,
		dailyBudget:       cv.DailyBudget,
		userMonthlyCap:    cv.UserMonthlyCap,
		minAccountAgeDays: cv.MinAccountAgeDays,
		requireRecharge:   cv.RequireRecharge,
	}
	cfg.sanitize()

	updates := map[string]string{
		SettingKeyCheckInEnabled:           strconv.FormatBool(cfg.enabled),
		SettingKeyCheckInMinReward:         formatCheckInFloat(cfg.minReward),
		SettingKeyCheckInMaxReward:         formatCheckInFloat(cfg.maxReward),
		SettingKeyCheckInBaseCap:           formatCheckInFloat(cfg.baseCap),
		SettingKeyCheckInWeightRecharge:    formatCheckInFloat(cfg.weightRecharge),
		SettingKeyCheckInWeightUsage:       formatCheckInFloat(cfg.weightUsage),
		SettingKeyCheckInWeightActivity:    formatCheckInFloat(cfg.weightActivity),
		SettingKeyCheckInRechargeCap:       formatCheckInFloat(cfg.rechargeCap),
		SettingKeyCheckInUsageCap:          formatCheckInFloat(cfg.usageCap),
		SettingKeyCheckInStreakCap:         strconv.Itoa(cfg.streakCap),
		SettingKeyCheckInBetaMin:           formatCheckInFloat(cfg.betaMin),
		SettingKeyCheckInBetaMax:           formatCheckInFloat(cfg.betaMax),
		SettingKeyCheckInDailyBudget:       formatCheckInFloat(cfg.dailyBudget),
		SettingKeyCheckInUserMonthlyCap:    formatCheckInFloat(cfg.userMonthlyCap),
		SettingKeyCheckInMinAccountAgeDays: strconv.Itoa(cfg.minAccountAgeDays),
		SettingKeyCheckInRequireRecharge:   strconv.FormatBool(cfg.requireRecharge),
	}
	if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
		return fmt.Errorf("persist check-in config: %w", err)
	}
	return nil
}

// validateCheckInConfig enforces strict admin config invariants.
func validateCheckInConfig(cv *CheckInConfigValues) error {
	if err := validateRewardBand("CHECKIN_CONFIG_INVALID", cv.MinReward, cv.MaxReward, cv.BaseCap, cv.BetaMin, cv.BetaMax); err != nil {
		return err
	}
	if cv.WeightRecharge < 0 || cv.WeightUsage < 0 || cv.WeightActivity < 0 {
		return infraerrors.BadRequest("CHECKIN_CONFIG_INVALID", "weights must be >= 0")
	}
	if cv.RechargeCap < 0 || cv.UsageCap < 0 {
		return infraerrors.BadRequest("CHECKIN_CONFIG_INVALID", "caps must be >= 0")
	}
	if cv.StreakCap < 1 {
		return infraerrors.BadRequest("CHECKIN_CONFIG_INVALID", "streak_cap must be >= 1")
	}
	if cv.DailyBudget < 0 || cv.UserMonthlyCap < 0 {
		return infraerrors.BadRequest("CHECKIN_CONFIG_INVALID", "budgets must be >= 0")
	}
	if cv.MinAccountAgeDays < 0 {
		return infraerrors.BadRequest("CHECKIN_CONFIG_INVALID", "min_account_age_days must be >= 0")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Admin analytics
// ---------------------------------------------------------------------------

// GetAnalytics returns the admin analytics read model in the app timezone.
func (s *CheckInAdminService) GetAnalytics(ctx context.Context) (*CheckInAnalytics, error) {
	now := timezone.Now()
	todayStr := now.Format(checkInDateLayout)
	monthStartStr := timezone.StartOfMonth(now).Format(checkInDateLayout)
	// Trend covers the last 30 days inclusive of today (today-29 .. today).
	trendStartStr := now.AddDate(0, 0, -29).Format(checkInDateLayout)

	analytics, err := s.checkInRepo.GetAnalytics(ctx, todayStr, monthStartStr, trendStartStr)
	if err != nil {
		return nil, fmt.Errorf("get check-in analytics: %w", err)
	}
	return analytics, nil
}

// ListRecords returns a paginated, filtered page of individual check-in records
// (joined to users) plus the total count of matching rows.
func (s *CheckInAdminService) ListRecords(ctx context.Context, params pagination.PaginationParams, filter CheckInRecordFilter) ([]CheckInRecordDetail, int64, error) {
	records, total, err := s.checkInRepo.ListRecords(ctx, params, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("list check-in records: %w", err)
	}
	return records, total, nil
}

// ---------------------------------------------------------------------------
// Parsing / formatting helpers (map-based; mirror the runtime setting parsers)
// ---------------------------------------------------------------------------

func checkInBoolValue(vals map[string]string, key string, def bool) bool {
	raw, ok := vals[key]
	if !ok {
		return def
	}
	switch strings.TrimSpace(raw) {
	case "true":
		return true
	case "false":
		return false
	default:
		return def
	}
}

func checkInFloatValue(vals map[string]string, key string, def float64) float64 {
	raw, ok := vals[key]
	if !ok {
		return def
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	return v
}

func checkInIntValue(vals map[string]string, key string, def int) int {
	raw, ok := vals[key]
	if !ok {
		return def
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

func formatCheckInFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
