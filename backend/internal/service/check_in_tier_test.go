package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeCheckInTierRepo is a configurable stand-in for CheckInRewardTierRepository.
type fakeCheckInTierRepo struct {
	list    []CheckInRewardTier
	enabled []CheckInRewardTier
	getByID *CheckInRewardTier

	getErr    error
	createErr error
	updateErr error
	deleteErr error

	created   *CheckInRewardTier
	updated   *CheckInRewardTier
	deletedID int64
	nextID    int64
}

func (f *fakeCheckInTierRepo) Create(ctx context.Context, t *CheckInRewardTier) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.nextID++
	t.ID = f.nextID
	f.created = t
	return nil
}

func (f *fakeCheckInTierRepo) GetByID(ctx context.Context, id int64) (*CheckInRewardTier, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getByID == nil {
		return nil, ErrCheckInTierNotFound
	}
	clone := *f.getByID
	return &clone, nil
}

func (f *fakeCheckInTierRepo) Update(ctx context.Context, t *CheckInRewardTier) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.updated = t
	return nil
}

func (f *fakeCheckInTierRepo) Delete(ctx context.Context, id int64) error {
	f.deletedID = id
	return f.deleteErr
}

func (f *fakeCheckInTierRepo) List(ctx context.Context) ([]CheckInRewardTier, error) {
	return f.list, nil
}

func (f *fakeCheckInTierRepo) ListEnabled(ctx context.Context) ([]CheckInRewardTier, error) {
	return f.enabled, nil
}

// fakeCheckInAdminSettingRepo backs GetMultiple/SetMultiple with an in-memory map.
type fakeCheckInAdminSettingRepo struct {
	SettingRepository
	store map[string]string
}

func (f *fakeCheckInAdminSettingRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if v, ok := f.store[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func (f *fakeCheckInAdminSettingRepo) SetMultiple(ctx context.Context, settings map[string]string) error {
	if f.store == nil {
		f.store = make(map[string]string)
	}
	for k, v := range settings {
		f.store[k] = v
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func rechargeTier(id int64, threshold, minReward, maxReward, baseCap float64) CheckInRewardTier {
	return CheckInRewardTier{
		ID:             id,
		Name:           "tier",
		Enabled:        true,
		MatchType:      CheckInTierMatchRecharge,
		MatchThreshold: threshold,
		MinReward:      minReward,
		MaxReward:      maxReward,
		BaseCap:        baseCap,
		BetaMin:        1,
		BetaMax:        3,
	}
}

func validCreateTierInput() *CreateCheckInTierInput {
	return &CreateCheckInTierInput{
		Name:           "VIP",
		Enabled:        true,
		MatchType:      CheckInTierMatchRecharge,
		MatchThreshold: 100,
		MinReward:      1,
		MaxReward:      4,
		BaseCap:        2,
		BetaMin:        1,
		BetaMax:        3,
		SortOrder:      1,
	}
}

func validCheckInConfigValues() *CheckInConfigValues {
	return &CheckInConfigValues{
		Enabled:           true,
		MinReward:         0.05,
		MaxReward:         4.0,
		BaseCap:           0.5,
		WeightRecharge:    0.5,
		WeightUsage:       0.25,
		WeightActivity:    0.25,
		RechargeCap:       200,
		UsageCap:          50,
		StreakCap:         7,
		BetaMin:           1,
		BetaMax:           3,
		DailyBudget:       100,
		UserMonthlyCap:    20,
		MinAccountAgeDays: 3,
		RequireRecharge:   false,
	}
}

// ---------------------------------------------------------------------------
// resolveRewardBand
// ---------------------------------------------------------------------------

func TestResolveRewardBand_NoTiersReturnsGlobalConfig(t *testing.T) {
	svc := &CheckInService{}
	cfg := defaultCheckInConfig()

	require.Equal(t, cfg, svc.resolveRewardBand(cfg, nil, 100, 0.5))
	require.Equal(t, cfg, svc.resolveRewardBand(cfg, []CheckInRewardTier{}, 100, 0.5))
}

func TestResolveRewardBand_RechargeTierMatchesAboveThreshold(t *testing.T) {
	svc := &CheckInService{}
	cfg := defaultCheckInConfig()
	tiers := []CheckInRewardTier{rechargeTier(1, 50, 1.0, 4.0, 2.0)}

	// Below threshold: no match, global config unchanged.
	require.Equal(t, cfg, svc.resolveRewardBand(cfg, tiers, 49, 0.5))

	// At/above threshold: tier band applied.
	eff := svc.resolveRewardBand(cfg, tiers, 50, 0.5)
	require.Equal(t, 1.0, eff.minReward)
	require.Equal(t, 4.0, eff.maxReward)
	require.Equal(t, 2.0, eff.baseCap)
}

func TestResolveRewardBand_ScoreTierMatches(t *testing.T) {
	svc := &CheckInService{}
	cfg := defaultCheckInConfig()
	tier := rechargeTier(1, 0.5, 1.5, 4.0, 2.0)
	tier.MatchType = CheckInTierMatchScore
	tiers := []CheckInRewardTier{tier}

	// Recharge is irrelevant for a score tier; below score threshold → no match.
	require.Equal(t, cfg, svc.resolveRewardBand(cfg, tiers, 100000, 0.49))

	eff := svc.resolveRewardBand(cfg, tiers, 0, 0.5)
	require.Equal(t, 1.5, eff.minReward)
}

func TestResolveRewardBand_HighestThresholdWins(t *testing.T) {
	svc := &CheckInService{}
	cfg := defaultCheckInConfig()
	tiers := []CheckInRewardTier{
		rechargeTier(1, 50, 1.0, 4.0, 2.0),
		rechargeTier(2, 100, 2.0, 4.5, 3.0),
		rechargeTier(3, 75, 1.5, 4.2, 2.5),
	}

	// recharge=150 matches all three; the 100-threshold tier (min 2.0) wins.
	eff := svc.resolveRewardBand(cfg, tiers, 150, 0.5)
	require.Equal(t, 2.0, eff.minReward)
}

func TestResolveRewardBand_TieBreakSortOrderThenID(t *testing.T) {
	svc := &CheckInService{}
	cfg := defaultCheckInConfig()

	a := rechargeTier(1, 50, 1.0, 4.0, 2.0)
	a.SortOrder = 1
	b := rechargeTier(2, 50, 2.0, 4.0, 3.0) // same threshold, higher sort_order
	b.SortOrder = 5
	tiers := []CheckInRewardTier{a, b}

	eff := svc.resolveRewardBand(cfg, tiers, 100, 0.5)
	require.Equal(t, 2.0, eff.minReward, "larger sort_order breaks the threshold tie")
}

func TestResolveRewardBand_DisabledTiersIgnored(t *testing.T) {
	svc := &CheckInService{}
	cfg := defaultCheckInConfig()
	tier := rechargeTier(1, 0, 2.0, 4.0, 3.0) // threshold 0 would match everything...
	tier.Enabled = false                      // ...but it is disabled.
	tiers := []CheckInRewardTier{tier}

	require.Equal(t, cfg, svc.resolveRewardBand(cfg, tiers, 100, 0.5))
}

func TestResolveRewardBand_TierMaxClampedToGlobalMax(t *testing.T) {
	svc := &CheckInService{}
	cfg := defaultCheckInConfig() // global max = defaultCheckInMaxReward (5.0)
	// Absurdly high tier max must never lift the ceiling above the global max.
	tiers := []CheckInRewardTier{rechargeTier(1, 0, 1.0, 1_000_000_000, 2.0)}

	eff := svc.resolveRewardBand(cfg, tiers, 100, 0.5)
	require.Equal(t, cfg.maxReward, eff.maxReward)
	require.LessOrEqual(t, eff.maxReward, cfg.maxReward)
}

func TestResolveRewardBand_TierMinAboveGlobalMaxStillClamped(t *testing.T) {
	svc := &CheckInService{}
	cfg := defaultCheckInConfig()
	cfg.maxReward = 3 // admin lowered the global ceiling below the tier's min

	// Tier validation permits min<=max but is blind to the global max: min=4 > global 3.
	// Without pinning min to the clamped ceiling, sanitize() would rewrite maxReward back
	// up to the 5.0 default and defeat the global safety clamp.
	tiers := []CheckInRewardTier{rechargeTier(1, 0, 4, 4, 4)}

	eff := svc.resolveRewardBand(cfg, tiers, 100, 0.5)
	require.LessOrEqual(t, eff.maxReward, cfg.maxReward,
		"a tier must never lift the effective ceiling above the global max, even when tier.min > global.max")
	require.LessOrEqual(t, eff.minReward, eff.maxReward)
}

func TestResolveRewardBand_SortOrderDominatesAcrossMatchTypes(t *testing.T) {
	svc := &CheckInService{}
	cfg := defaultCheckInConfig()

	// A recharge tier (dollar threshold) and a score tier (fractional threshold) both
	// match this user. Selection must follow the admin's explicit sort_order, not the
	// raw threshold magnitude — otherwise the recharge tier's larger number always wins
	// and score tiers can never take effect when mixed.
	rt := rechargeTier(1, 50, 1.0, 4.0, 2.0)
	rt.SortOrder = 1
	st := rechargeTier(2, 0.8, 3.0, 4.5, 3.5)
	st.MatchType = CheckInTierMatchScore
	st.SortOrder = 5 // admin gives the engagement/score tier priority
	tiers := []CheckInRewardTier{rt, st}

	// recharge=100 (>=50) and score=0.9 (>=0.8): both match; higher sort_order wins.
	eff := svc.resolveRewardBand(cfg, tiers, 100, 0.9)
	require.Equal(t, 3.0, eff.minReward,
		"higher sort_order tier wins regardless of match_type / threshold scale")
}

// ---------------------------------------------------------------------------
// Tier CRUD validation
// ---------------------------------------------------------------------------

func TestCreateTier_ValidationRejectsBadInput(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(in *CreateCheckInTierInput)
	}{
		{"empty name", func(in *CreateCheckInTierInput) { in.Name = "  " }},
		{"bad match_type", func(in *CreateCheckInTierInput) { in.MatchType = "nope" }},
		{"negative threshold", func(in *CreateCheckInTierInput) { in.MatchThreshold = -1 }},
		{"zero min_reward", func(in *CreateCheckInTierInput) { in.MinReward = 0 }},
		{"negative min_reward", func(in *CreateCheckInTierInput) { in.MinReward = -1 }},
		{"max below min", func(in *CreateCheckInTierInput) { in.MaxReward = 0.5 }},
		{"max above absolute ceiling", func(in *CreateCheckInTierInput) { in.MaxReward = checkInAbsoluteMaxReward + 1 }},
		{"base_cap below min", func(in *CreateCheckInTierInput) { in.BaseCap = 0.5 }},
		{"base_cap above max", func(in *CreateCheckInTierInput) { in.BaseCap = 10 }},
		{"negative beta_min", func(in *CreateCheckInTierInput) { in.BetaMin = -1 }},
		{"beta_max below beta_min", func(in *CreateCheckInTierInput) { in.BetaMax = 0.5 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeCheckInTierRepo{}
			svc := NewCheckInAdminService(repo, nil, nil)
			in := validCreateTierInput()
			tc.mutate(in)

			tier, err := svc.CreateTier(context.Background(), in)

			require.Nil(t, tier)
			require.Equal(t, "CHECKIN_TIER_INVALID", infraerrors.Reason(err))
			require.Nil(t, repo.created, "invalid tier must never reach the repository")
		})
	}
}

func TestCreateTier_ValidInputAccepted(t *testing.T) {
	repo := &fakeCheckInTierRepo{}
	svc := NewCheckInAdminService(repo, nil, nil)

	tier, err := svc.CreateTier(context.Background(), validCreateTierInput())

	require.NoError(t, err)
	require.NotNil(t, tier)
	require.Greater(t, tier.ID, int64(0))
	require.NotNil(t, repo.created)
	require.Equal(t, "VIP", repo.created.Name)
}

func TestUpdateTier_ValidationRejectsBadMerge(t *testing.T) {
	existing := &CheckInRewardTier{
		ID: 9, Name: "VIP", Enabled: true, MatchType: CheckInTierMatchRecharge,
		MatchThreshold: 100, MinReward: 1, MaxReward: 4, BaseCap: 2, BetaMin: 1, BetaMax: 3,
	}
	repo := &fakeCheckInTierRepo{getByID: existing}
	svc := NewCheckInAdminService(repo, nil, nil)

	badMin := 0.0
	tier, err := svc.UpdateTier(context.Background(), 9, &UpdateCheckInTierInput{MinReward: &badMin})

	require.Nil(t, tier)
	require.Equal(t, "CHECKIN_TIER_INVALID", infraerrors.Reason(err))
	require.Nil(t, repo.updated, "invalid merged tier must never be persisted")
}

func TestUpdateTier_ValidPartialUpdate(t *testing.T) {
	existing := &CheckInRewardTier{
		ID: 9, Name: "VIP", Enabled: true, MatchType: CheckInTierMatchRecharge,
		MatchThreshold: 100, MinReward: 1, MaxReward: 4, BaseCap: 2, BetaMin: 1, BetaMax: 3,
	}
	repo := &fakeCheckInTierRepo{getByID: existing}
	svc := NewCheckInAdminService(repo, nil, nil)

	disabled := false
	newMax := 4.5
	tier, err := svc.UpdateTier(context.Background(), 9, &UpdateCheckInTierInput{
		Enabled:   &disabled,
		MaxReward: &newMax,
	})

	require.NoError(t, err)
	require.NotNil(t, tier)
	require.False(t, tier.Enabled)
	require.Equal(t, 4.5, tier.MaxReward)
	require.Equal(t, 1.0, tier.MinReward, "unset fields are preserved")
	require.NotNil(t, repo.updated)
}

// ---------------------------------------------------------------------------
// Config validation + round-trip
// ---------------------------------------------------------------------------

func TestUpdateCheckInConfig_ValidationRejectsBadInput(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(cv *CheckInConfigValues)
	}{
		{"min <= 0", func(cv *CheckInConfigValues) { cv.MinReward = 0 }},
		{"min > max", func(cv *CheckInConfigValues) { cv.MinReward = 5; cv.MaxReward = 3 }},
		{"max above absolute ceiling", func(cv *CheckInConfigValues) { cv.MaxReward = checkInAbsoluteMaxReward + 1 }},
		{"negative weight", func(cv *CheckInConfigValues) { cv.WeightRecharge = -1 }},
		{"beta_max < beta_min", func(cv *CheckInConfigValues) { cv.BetaMin = 3; cv.BetaMax = 1 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setting := &fakeCheckInAdminSettingRepo{store: map[string]string{}}
			svc := NewCheckInAdminService(nil, setting, nil)
			cv := validCheckInConfigValues()
			tc.mutate(cv)

			err := svc.UpdateCheckInConfig(context.Background(), cv)

			require.Equal(t, "CHECKIN_CONFIG_INVALID", infraerrors.Reason(err))
			require.Empty(t, setting.store, "invalid config must never be persisted")
		})
	}
}

func TestUpdateCheckInConfig_ValidPayloadRoundTrips(t *testing.T) {
	setting := &fakeCheckInAdminSettingRepo{store: map[string]string{}}
	svc := NewCheckInAdminService(nil, setting, nil)
	in := validCheckInConfigValues()

	require.NoError(t, svc.UpdateCheckInConfig(context.Background(), in))

	got, err := svc.GetCheckInConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, *in, *got)
}

// ---------------------------------------------------------------------------
// Claim with an enabled tier: reward stays within the effective band and the
// effective max never exceeds the global max.
// ---------------------------------------------------------------------------

func TestClaim_WithEnabledTierRewardWithinEffectiveBand(t *testing.T) {
	// Global max stays at the default (5.0); the tier requests an absurd max that
	// must be clamped down to the global ceiling.
	const effMin = 1.0
	globalMax := defaultCheckInMaxReward

	tierRepo := &fakeCheckInTierRepo{
		enabled: []CheckInRewardTier{rechargeTier(1, 0, effMin, 1_000_000_000, 2.0)},
	}
	userRepo := &fakeCheckInUserRepo{user: activeUser(30)}

	// Confirm the effective band up front (pure resolution).
	eff := (&CheckInService{}).resolveRewardBand(defaultCheckInConfig(), tierRepo.enabled, 0, 0.5)
	require.Equal(t, effMin, eff.minReward)
	require.Equal(t, globalMax, eff.maxReward)
	require.LessOrEqual(t, eff.maxReward, globalMax)

	for i := 0; i < 200; i++ {
		repo := &fakeCheckInRepo{claimed: true, claimBalance: 100}
		svc := NewCheckInService(repo, tierRepo, userRepo, &fakeCheckInSettingRepo{vals: nil}, nil, nil)

		res, err := svc.Claim(context.Background(), 7)

		require.NoError(t, err)
		require.NotNil(t, res)
		require.GreaterOrEqual(t, res.RewardAmount, effMin-1e-9, "reward below effective min")
		require.LessOrEqual(t, res.RewardAmount, globalMax+1e-9, "reward above global max")
	}
}

// F1 regression: when a matched tier's max is BELOW the global min, the final clamp
// must use the resolved tier band — not the global band — so the reward is never
// bumped UP to the global min above the tier ceiling (overpay above the tier max).
func TestClaim_TierMaxBelowGlobalMin_NoOverpay(t *testing.T) {
	// Global band [1.00, 5.00] via settings; tier band [0.05, 0.20] (recharge >= 0).
	vals := map[string]string{
		SettingKeyCheckInMinReward: "1.00",
		SettingKeyCheckInMaxReward: "5.00",
	}
	tierRepo := &fakeCheckInTierRepo{
		enabled: []CheckInRewardTier{rechargeTier(1, 0, 0.05, 0.20, 0.10)},
	}
	userRepo := &fakeCheckInUserRepo{user: activeUser(30)}

	for i := 0; i < 300; i++ {
		repo := &fakeCheckInRepo{claimed: true, claimBalance: 100}
		svc := NewCheckInService(repo, tierRepo, userRepo, &fakeCheckInSettingRepo{vals: vals}, nil, nil)

		res, err := svc.Claim(context.Background(), 7)

		require.NoError(t, err)
		require.NotNil(t, res)
		require.LessOrEqual(t, res.RewardAmount, 0.20+1e-9,
			"reward must stay within the tier ceiling and never be bumped up to the global min")
	}
}

// F1 regression: a budget-trimmed reward must not be re-inflated by the final clamp
// back up to the global min. With a tier min below the remaining budget, the credited
// reward must never exceed the remaining daily budget.
func TestClaim_BudgetTrimNotReinflated(t *testing.T) {
	// Global band [1.00, 5.00]; daily budget 5.25 with 5.00 already gifted today leaves
	// remaining = 0.25 — below the global min (1.00) but above the tier min (0.05).
	const remaining = 0.25
	vals := map[string]string{
		SettingKeyCheckInMinReward:   "1.00",
		SettingKeyCheckInMaxReward:   "5.00",
		SettingKeyCheckInDailyBudget: "5.25",
	}
	// Tier band [0.05, 0.50] with a high baseCap so draws frequently exceed the
	// remaining budget and exercise the trim path.
	tierRepo := &fakeCheckInTierRepo{
		enabled: []CheckInRewardTier{rechargeTier(1, 0, 0.05, 0.50, 0.45)},
	}
	userRepo := &fakeCheckInUserRepo{user: activeUser(30)}

	for i := 0; i < 300; i++ {
		repo := &fakeCheckInRepo{claimed: true, claimBalance: 100, todaySum: 5.00}
		svc := NewCheckInService(repo, tierRepo, userRepo, &fakeCheckInSettingRepo{vals: vals}, nil, nil)

		res, err := svc.Claim(context.Background(), 7)

		require.NoError(t, err)
		require.NotNil(t, res)
		require.LessOrEqual(t, res.RewardAmount, remaining+1e-9,
			"a budget-trimmed reward must never be re-inflated above the remaining budget")
	}
}

// F3 regression: two matching tiers of DIFFERENT match_type with EQUAL sort_order must
// break the tie deterministically on id — NOT on cross-scale threshold magnitude (which
// would let the recharge tier's dollar threshold always outrank the score tier).
func TestResolveRewardBand_EqualSortOrderDifferentTypeBreaksOnID(t *testing.T) {
	svc := &CheckInService{}
	cfg := defaultCheckInConfig()

	rt := rechargeTier(1, 50, 1.0, 4.0, 2.0) // recharge tier, id 1
	rt.SortOrder = 3
	st := rechargeTier(2, 0.8, 3.0, 4.5, 3.5) // score tier, id 2
	st.MatchType = CheckInTierMatchScore
	st.SortOrder = 3
	tiers := []CheckInRewardTier{rt, st}

	// recharge=100 (>=50) and score=0.9 (>=0.8): both match; equal sort_order + differing
	// match_type must fall through to id, so the higher-id score tier (min 3.0) wins.
	eff := svc.resolveRewardBand(cfg, tiers, 100, 0.9)
	require.Equal(t, 3.0, eff.minReward,
		"equal sort_order across match types must break on id, not threshold magnitude")
}

// F6: deleting a non-existent tier must surface the typed not-found error (which the
// admin handler maps to 404), never a false success.
func TestDeleteTier_NotFoundPropagates(t *testing.T) {
	repo := &fakeCheckInTierRepo{deleteErr: ErrCheckInTierNotFound}
	svc := NewCheckInAdminService(repo, nil, nil)

	err := svc.DeleteTier(context.Background(), 123)

	require.Equal(t, "CHECKIN_TIER_NOT_FOUND", infraerrors.Reason(err))
}
