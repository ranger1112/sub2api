package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/tidwall/gjson"
)

// OpenAICapacityQuarantineStore is deliberately independent from the shared
// temporary-unschedulable state. Implementations must be safe for multiple
// gateway instances and must fail open when their backing store is unavailable.
type OpenAICapacityQuarantineStore interface {
	RecordOpenAICapacityEvent(ctx context.Context, accountID int64, requestID string, now time.Time, window time.Duration) (count int, duplicate bool, err error)
	RecordOpenAICapacityGroupEvent(ctx context.Context, groupID int64, model string, accountID int64, now time.Time, window time.Duration) (distinctCount int, err error)
	AcquireOpenAICapacityTripLocks(ctx context.Context, groupIDs []int64, owner string, lease time.Duration) (release func(), acquired bool, err error)
	GetOpenAICapacityQuarantineState(ctx context.Context, accountID int64) (*OpenAICapacityQuarantineState, error)
	OpenOpenAICapacityQuarantine(ctx context.Context, request OpenAICapacityQuarantineOpenRequest) (opened bool, state *OpenAICapacityQuarantineState, err error)
	AcquireOpenAICapacityProbe(ctx context.Context, accountID int64, owner string, lease time.Duration) (acquired bool, err error)
	RenewOpenAICapacityProbe(ctx context.Context, accountID int64, owner string, lease time.Duration) (renewed bool, err error)
	ReleaseOpenAICapacityProbe(ctx context.Context, accountID int64, owner string) error
	CompleteOpenAICapacityProbe(ctx context.Context, accountID int64, owner string, now time.Time) (completed bool, err error)
}

type OpenAICapacityQuarantineState struct {
	AccountID     int64
	State         string
	Generation    int64
	CooldownUntil time.Time
	OpenedAt      time.Time
	RuleID        string
	Owner         string
}

type OpenAICapacityQuarantineOpenRequest struct {
	AccountID int64
	Now       time.Time
	Cooldown  time.Duration
	State     string // open_initial / open_retrip
	RuleID    string
}

const (
	openAICapacityStateOpenInitial = "open_initial"
	openAICapacityStateOpenRetrip  = "open_retrip"
	openAICapacityStateHalfOpen    = "half_open"
	openAICapacityStateClosed      = "closed"
	openAICapacityPolicyCacheTTL   = 5 * time.Second
	// Pool checks consult PostgreSQL eligibility and multiple Redis state keys.
	// Serialize a trip across all affected groups long enough to make its
	// projected pool calculation valid under concurrent gateway instances.
	openAICapacityTripLockLease = 15 * time.Second
)

// openAICapacityQuarantineRuntime owns only Capacity-specific state. It never
// writes accounts.temp_unschedulable_* and therefore cannot clear another
// subsystem's temporary isolation during automatic recovery.
type openAICapacityQuarantineRuntime struct {
	settings *SettingService
	accounts AccountRepository
	store    OpenAICapacityQuarantineStore

	policyMu      sync.Mutex
	policy        *OpenAICapacityQuarantineSettings
	policyExpires time.Time
	policyLoadErr error
}

func newOpenAICapacityQuarantineRuntime(settings *SettingService, accounts AccountRepository, store OpenAICapacityQuarantineStore) *openAICapacityQuarantineRuntime {
	if settings == nil || accounts == nil || store == nil {
		return nil
	}
	return &openAICapacityQuarantineRuntime{settings: settings, accounts: accounts, store: store}
}

func (r *openAICapacityQuarantineRuntime) currentPolicy(ctx context.Context) (*OpenAICapacityQuarantineSettings, error) {
	if r == nil || r.settings == nil {
		return DefaultOpenAICapacityQuarantineSettings(), nil
	}
	now := time.Now()
	r.policyMu.Lock()
	defer r.policyMu.Unlock()
	if r.policy != nil && now.Before(r.policyExpires) {
		copy := *r.policy
		copy.MatchRules = append([]OpenAICapacityMatchRule(nil), r.policy.MatchRules...)
		copy.GroupPolicy = append([]OpenAICapacityGroupPolicy(nil), r.policy.GroupPolicy...)
		return &copy, r.policyLoadErr
	}
	policy, err := r.settings.GetOpenAICapacityQuarantineSettings(ctx)
	if err == nil && policy != nil {
		r.policy = policy
		r.policyLoadErr = nil
	} else {
		// Keep a cached value only for diagnostics; surface the read failure to
		// callers so execution fails open instead of enforcing stale policy.
		if r.policy == nil {
			r.policy = DefaultOpenAICapacityQuarantineSettings()
		}
		r.policyLoadErr = err
	}
	r.policyExpires = now.Add(openAICapacityPolicyCacheTTL)
	copy := *r.policy
	copy.MatchRules = append([]OpenAICapacityMatchRule(nil), r.policy.MatchRules...)
	copy.GroupPolicy = append([]OpenAICapacityGroupPolicy(nil), r.policy.GroupPolicy...)
	return &copy, r.policyLoadErr
}

func openAICapacityEnabledPolicies(settings *OpenAICapacityQuarantineSettings, account *Account) []OpenAICapacityGroupPolicy {
	if settings == nil || account == nil || len(settings.GroupPolicy) == 0 {
		return nil
	}
	ids := append([]int64(nil), account.GroupIDs...)
	for _, group := range account.AccountGroups {
		ids = append(ids, group.GroupID)
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id > 0 {
			seen[id] = struct{}{}
		}
	}
	policies := make([]OpenAICapacityGroupPolicy, 0, len(settings.GroupPolicy))
	for _, policy := range settings.GroupPolicy {
		if policy.Enabled {
			if _, ok := seen[policy.GroupID]; ok {
				policies = append(policies, policy)
			}
		}
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].GroupID < policies[j].GroupID })
	return policies
}

func (r *openAICapacityQuarantineRuntime) isEnabledForAccount(ctx context.Context, account *Account) (*OpenAICapacityQuarantineSettings, bool, error) {
	if r == nil || account == nil || !account.IsOpenAI() {
		return nil, false, nil
	}
	policy, err := r.currentPolicy(ctx)
	if err != nil {
		return policy, false, err
	}
	if policy.Mode == OpenAICapacityQuarantineModeDisabled || len(openAICapacityEnabledPolicies(policy, account)) == 0 {
		return policy, false, nil
	}
	return policy, true, nil
}

// ExcludesFromScheduling only excludes an account while its Capacity cooldown
// key is active. An expired cooldown is admitted at the request boundary by
// AcquireAdmission, where the single half-open probe lease is acquired.
func (r *openAICapacityQuarantineRuntime) ExcludesFromScheduling(ctx context.Context, account *Account) bool {
	policy, enabled, err := r.isEnabledForAccount(ctx, account)
	if err != nil || !enabled || policy.Mode != OpenAICapacityQuarantineModeEnforce {
		if err != nil {
			slog.Warn("openai_capacity_policy_read_failed_fail_open", "account_id", accountID(account), "error", err)
		}
		return false
	}
	state, err := r.store.GetOpenAICapacityQuarantineState(ctx, account.ID)
	if err != nil {
		slog.Warn("openai_capacity_state_read_failed_fail_open", "account_id", account.ID, "error", err)
		return false
	}
	return state != nil && !state.CooldownUntil.IsZero() && time.Now().Before(state.CooldownUntil)
}

func (r *openAICapacityQuarantineRuntime) AcquireAdmission(ctx context.Context, account *Account) (release func(), completeSuccess func(), admitted bool) {
	policy, enabled, err := r.isEnabledForAccount(ctx, account)
	if err != nil || !enabled || policy.Mode != OpenAICapacityQuarantineModeEnforce {
		if err != nil {
			slog.Warn("openai_capacity_policy_read_failed_fail_open", "account_id", accountID(account), "error", err)
		}
		return func() {}, func() {}, true
	}
	state, err := r.store.GetOpenAICapacityQuarantineState(ctx, account.ID)
	if err != nil {
		slog.Warn("openai_capacity_state_read_failed_fail_open", "account_id", account.ID, "error", err)
		return func() {}, func() {}, true
	}
	if state != nil && !state.CooldownUntil.IsZero() && time.Now().Before(state.CooldownUntil) {
		return nil, nil, false
	}
	if state == nil || state.State == "" || state.State == openAICapacityStateClosed {
		return func() {}, func() {}, true
	}

	owner := openAICapacityProbeOwner(ctx, account.ID)
	lease := time.Duration(policy.HalfOpen.LeaseSeconds) * time.Second
	acquired, err := r.store.AcquireOpenAICapacityProbe(ctx, account.ID, owner, lease)
	if err != nil {
		slog.Warn("openai_capacity_probe_acquire_failed_fail_open", "account_id", account.ID, "error", err)
		return func() {}, func() {}, true
	}
	if !acquired {
		return nil, nil, false
	}
	// The store reports an already-closed state as admitted to avoid rejecting a
	// race with recovery. In that case there is no owner to renew or complete.
	acquiredState, stateErr := r.store.GetOpenAICapacityQuarantineState(ctx, account.ID)
	if stateErr != nil {
		slog.Warn("openai_capacity_probe_state_read_failed_fail_open", "account_id", account.ID, "error", stateErr)
		return func() {}, func() {}, true
	}
	if acquiredState == nil || acquiredState.State != openAICapacityStateHalfOpen || acquiredState.Owner != owner {
		return func() {}, func() {}, true
	}

	// Long SSE/WebSocket probes keep their lease alive. A lost Redis lease is
	// fail-open for availability, but it never converts into a second owner while
	// the renewal succeeds.
	stop := make(chan struct{})
	done := make(chan struct{})
	renewEvery := time.Duration(policy.HalfOpen.RenewIntervalSecond) * time.Second
	go func() {
		defer close(done)
		ticker := time.NewTicker(renewEvery)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				renewed, renewErr := r.store.RenewOpenAICapacityProbe(context.Background(), account.ID, owner, lease)
				if renewErr != nil {
					slog.Warn("openai_capacity_probe_renew_failed", "account_id", account.ID, "error", renewErr)
					return
				}
				if !renewed {
					return
				}
			}
		}
	}()

	var once sync.Once
	release = func() {
		once.Do(func() {
			close(stop)
			<-done
			if err := r.store.ReleaseOpenAICapacityProbe(context.Background(), account.ID, owner); err != nil {
				slog.Warn("openai_capacity_probe_release_failed", "account_id", account.ID, "error", err)
			}
		})
	}
	var completeOnce sync.Once
	completeSuccess = func() {
		completeOnce.Do(func() {
			r.CompleteSuccessfulProbe(context.Background(), account.ID, owner)
		})
	}
	return release, completeSuccess, true
}

// CompleteSuccessfulProbe is intentionally owner-bound. A late success from a
// request which started before half-open must never recover another request's
// probe lease.
func (r *openAICapacityQuarantineRuntime) CompleteSuccessfulProbe(ctx context.Context, accountID int64, owner string) {
	if r == nil || accountID <= 0 || strings.TrimSpace(owner) == "" {
		return
	}
	state, err := r.store.GetOpenAICapacityQuarantineState(ctx, accountID)
	if err != nil || state == nil || state.State != openAICapacityStateHalfOpen || state.Owner != owner {
		return
	}
	completed, err := r.store.CompleteOpenAICapacityProbe(ctx, accountID, owner, time.Now())
	if err != nil {
		slog.Warn("openai_capacity_probe_complete_failed", "account_id", accountID, "error", err)
		return
	}
	if completed {
		slog.Info("openai_capacity_quarantine_recovered", "account_id", accountID, "generation", state.Generation)
	}
}

func (r *openAICapacityQuarantineRuntime) RecordUpstreamError(ctx context.Context, account *Account, statusCode int, responseBody []byte, canonicalModel string) bool {
	policy, enabled, err := r.isEnabledForAccount(ctx, account)
	if err != nil {
		slog.Warn("openai_capacity_policy_read_failed_fail_open", "account_id", accountID(account), "error", err)
		return false
	}
	if !enabled || policy.Mode == OpenAICapacityQuarantineModeDisabled {
		return false
	}
	input := normalizeOpenAICapacityMatcherInput(statusCode, responseBody)
	match := MatchOpenAICapacityError(*policy, input)
	if !match.Matched {
		return false
	}

	now := time.Now()
	requestID := openAICapacityRequestID(ctx, account.ID, now)
	count, duplicate, err := r.store.RecordOpenAICapacityEvent(ctx, account.ID, requestID, now, time.Duration(policy.WindowSeconds)*time.Second)
	if err != nil {
		slog.Warn("openai_capacity_event_record_failed_fail_open", "account_id", account.ID, "error", err)
		return false
	}
	if duplicate {
		return false
	}

	groupPolicies := openAICapacityEnabledPolicies(policy, account)
	groupDistinctCounts := make(map[int64]int, len(groupPolicies))
	for _, group := range groupPolicies {
		distinct, recordErr := r.store.RecordOpenAICapacityGroupEvent(ctx, group.GroupID, canonicalModel, account.ID, now, time.Duration(group.GlobalSpikeWindowSeconds)*time.Second)
		if recordErr != nil {
			slog.Warn("openai_capacity_group_event_record_failed_fail_open", "account_id", account.ID, "group_id", group.GroupID, "error", recordErr)
			return false
		}
		groupDistinctCounts[group.GroupID] = distinct
	}

	state, err := r.store.GetOpenAICapacityQuarantineState(ctx, account.ID)
	if err != nil {
		slog.Warn("openai_capacity_state_read_failed_fail_open", "account_id", account.ID, "error", err)
		return false
	}
	wasHalfOpen := state != nil && state.State == openAICapacityStateHalfOpen
	// A failed half-open probe must immediately open again. The retrip window
	// decides only which cooldown profile applies; it never leaves a failed
	// probe unprotected while waiting for the normal error threshold.
	shouldTrip := count >= policy.ErrorThreshold || wasHalfOpen
	if !shouldTrip {
		return false
	}
	if policy.Mode == OpenAICapacityQuarantineModeShadow {
		slog.Info("openai_capacity_quarantine_would_trip", "account_id", account.ID, "count", count, "threshold", policy.ErrorThreshold, "rule_id", match.RuleID)
		return false
	}
	if state != nil && !state.CooldownUntil.IsZero() && now.Before(state.CooldownUntil) {
		// A late error from the already-open generation must not extend cooldown.
		return true
	}

	groupIDs := make([]int64, 0, len(groupPolicies))
	for _, group := range groupPolicies {
		groupIDs = append(groupIDs, group.GroupID)
	}
	releaseTripLocks, acquired, lockErr := r.store.AcquireOpenAICapacityTripLocks(
		ctx,
		groupIDs,
		openAICapacityTripLockOwner(ctx, account.ID, now),
		openAICapacityTripLockLease,
	)
	if lockErr != nil {
		slog.Warn("openai_capacity_trip_lock_failed_fail_open", "account_id", account.ID, "error", lockErr)
		return false
	}
	if !acquired {
		// A concurrent request is making the same group-level decision. This
		// request already received a verified Capacity error, so fail over without
		// mutating state rather than racing the pool floor calculation.
		slog.Debug("openai_capacity_trip_lock_contended", "account_id", account.ID)
		return true
	}
	if releaseTripLocks != nil {
		defer releaseTripLocks()
	}

	// The group lock establishes the pool-guard critical section. Re-read after
	// acquiring it: another trip may have completed just before this lock was
	// taken, and opening it again must never extend its cooldown.
	state, err = r.store.GetOpenAICapacityQuarantineState(ctx, account.ID)
	if err != nil {
		slog.Warn("openai_capacity_state_read_failed_fail_open", "account_id", account.ID, "error", err)
		return false
	}
	if state != nil && !state.CooldownUntil.IsZero() && now.Before(state.CooldownUntil) {
		return true
	}
	if suppressed, reason := r.poolGuardSuppresses(ctx, account, groupPolicies, groupDistinctCounts, now); suppressed {
		slog.Warn("openai_capacity_quarantine_pool_guard_suppressed", "account_id", account.ID, "reason", reason, "rule_id", match.RuleID)
		return false
	}

	cooldown := time.Duration(policy.InitialCooldownSeconds) * time.Second
	openState := openAICapacityStateOpenInitial
	if wasHalfOpen && !state.OpenedAt.IsZero() && now.Sub(state.OpenedAt) <= time.Duration(policy.RetripWindowSeconds)*time.Second {
		cooldown = openAICapacityRetripCooldown(state, *policy)
		openState = openAICapacityStateOpenRetrip
	}
	opened, openedState, err := r.store.OpenOpenAICapacityQuarantine(ctx, OpenAICapacityQuarantineOpenRequest{
		AccountID: account.ID, Now: now, Cooldown: cooldown, State: openState, RuleID: match.RuleID,
	})
	if err != nil {
		slog.Warn("openai_capacity_quarantine_open_failed_fail_open", "account_id", account.ID, "error", err)
		return false
	}
	if opened {
		slog.Warn("openai_capacity_quarantine_opened", "account_id", account.ID, "state", openState, "until", openedState.CooldownUntil, "count", count, "threshold", policy.ErrorThreshold, "rule_id", match.RuleID)
	}
	return opened || (openedState != nil && now.Before(openedState.CooldownUntil))
}

// openAICapacityRetripCooldown starts from the configured retrip cooldown and
// doubles a prior longer retrip duration, but never exceeds the administrator's
// maximum. With the default retrip == max it remains a constant 30-minute
// retry; raising the maximum enables bounded escalation for repeated failed
// probes without changing the initial cooldown.
func openAICapacityRetripCooldown(state *OpenAICapacityQuarantineState, policy OpenAICapacityQuarantineSettings) time.Duration {
	base := time.Duration(policy.RetripCooldownSeconds) * time.Second
	maximum := time.Duration(policy.MaxCooldownSeconds) * time.Second
	if state != nil && !state.OpenedAt.IsZero() && !state.CooldownUntil.IsZero() {
		previous := state.CooldownUntil.Sub(state.OpenedAt)
		if previous >= base && previous <= maximum/2 {
			base = previous * 2
		} else if previous > maximum/2 {
			base = maximum
		}
	}
	if base > maximum {
		return maximum
	}
	return base
}

func (r *openAICapacityQuarantineRuntime) poolGuardSuppresses(ctx context.Context, account *Account, policies []OpenAICapacityGroupPolicy, groupDistinctCounts map[int64]int, now time.Time) (bool, string) {
	for _, policy := range policies {
		distinct := groupDistinctCounts[policy.GroupID]
		if distinct >= policy.GlobalSpikeDistinctAccounts {
			return true, fmt.Sprintf("global_spike(group=%d,count=%d)", policy.GroupID, distinct)
		}
		accounts, err := r.accounts.ListSchedulableByGroupIDAndPlatform(ctx, policy.GroupID, PlatformOpenAI)
		if err != nil {
			return true, fmt.Sprintf("pool_query_error(group=%d)", policy.GroupID)
		}
		total := len(accounts)
		available := 0
		quarantined := 0
		candidatePresent := false
		for i := range accounts {
			candidate := &accounts[i]
			if candidate.ID == account.ID {
				candidatePresent = true
			}
			state, readErr := r.store.GetOpenAICapacityQuarantineState(ctx, candidate.ID)
			if readErr != nil {
				return true, fmt.Sprintf("pool_state_read_error(group=%d)", policy.GroupID)
			}
			if state != nil && !state.CooldownUntil.IsZero() && now.Before(state.CooldownUntil) {
				quarantined++
				continue
			}
			available++
		}
		if !candidatePresent {
			return true, fmt.Sprintf("candidate_not_in_group_pool(group=%d)", policy.GroupID)
		}
		projectedAvailable := available - 1
		projectedQuarantined := quarantined + 1
		if projectedAvailable < policy.MinRemainingAccounts {
			return true, fmt.Sprintf("min_remaining_accounts(group=%d,remaining=%d)", policy.GroupID, projectedAvailable)
		}
		if total <= 0 || float64(projectedQuarantined)/float64(total) > policy.MaxQuarantinedFraction {
			return true, fmt.Sprintf("max_quarantined_fraction(group=%d)", policy.GroupID)
		}
	}
	return false, ""
}

func normalizeOpenAICapacityMatcherInput(statusCode int, responseBody []byte) OpenAICapacityMatcherInput {
	value := func(paths ...string) string {
		for _, path := range paths {
			if found := strings.TrimSpace(gjson.GetBytes(responseBody, path).String()); found != "" {
				return found
			}
		}
		return ""
	}
	message := sanitizeUpstreamErrorMessage(value("error.message", "response.error.message", "message"))
	if message == "" {
		message = sanitizeUpstreamErrorMessage(extractUpstreamErrorMessage(responseBody))
	}
	return OpenAICapacityMatcherInput{
		HTTPStatus:   statusCode,
		ProviderCode: truncateString(value("error.code", "response.error.code", "code"), 256),
		ProviderType: truncateString(value("error.type", "response.error.type", "type"), 256),
		Message:      truncateString(message, 1024),
	}
}

func openAICapacityRequestID(ctx context.Context, accountID int64, now time.Time) string {
	if ctx != nil {
		for _, key := range []ctxkey.Key{ctxkey.ClientRequestID, ctxkey.RequestID} {
			if value, _ := ctx.Value(key).(string); strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return fmt.Sprintf("local-%d-%d", accountID, now.UnixNano())
}

func openAICapacityProbeOwner(ctx context.Context, accountID int64) string {
	return fmt.Sprintf("%s:probe:%d", openAICapacityRequestID(ctx, accountID, time.Now()), time.Now().UnixNano())
}

func openAICapacityTripLockOwner(ctx context.Context, accountID int64, now time.Time) string {
	return fmt.Sprintf("%s:trip:%d", openAICapacityRequestID(ctx, accountID, now), now.UnixNano())
}

func accountID(account *Account) int64 {
	if account == nil {
		return 0
	}
	return account.ID
}
