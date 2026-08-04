package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type capacityRuntimeStore struct {
	mu          sync.Mutex
	events      map[int64]map[string]time.Time
	groupEvents map[string]map[int64]time.Time
	states      map[int64]*OpenAICapacityQuarantineState
	probes      map[int64]string
	tripLocks   map[int64]string
	generation  map[int64]int64
	fail        error
}

func newCapacityRuntimeStore() *capacityRuntimeStore {
	return &capacityRuntimeStore{
		events:      make(map[int64]map[string]time.Time),
		groupEvents: make(map[string]map[int64]time.Time),
		states:      make(map[int64]*OpenAICapacityQuarantineState),
		probes:      make(map[int64]string),
		tripLocks:   make(map[int64]string),
		generation:  make(map[int64]int64),
	}
}

func (s *capacityRuntimeStore) check() error { return s.fail }

func (s *capacityRuntimeStore) RecordOpenAICapacityEvent(_ context.Context, accountID int64, requestID string, now time.Time, _ time.Duration) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return 0, false, err
	}
	if s.events[accountID] == nil {
		s.events[accountID] = make(map[string]time.Time)
	}
	if _, exists := s.events[accountID][requestID]; exists {
		return len(s.events[accountID]), true, nil
	}
	s.events[accountID][requestID] = now
	return len(s.events[accountID]), false, nil
}

func (s *capacityRuntimeStore) RecordOpenAICapacityGroupEvent(_ context.Context, groupID int64, model string, accountID int64, now time.Time, _ time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return 0, err
	}
	key := string(rune(groupID)) + ":" + model
	if s.groupEvents[key] == nil {
		s.groupEvents[key] = make(map[int64]time.Time)
	}
	s.groupEvents[key][accountID] = now
	return len(s.groupEvents[key]), nil
}

func (s *capacityRuntimeStore) AcquireOpenAICapacityTripLocks(_ context.Context, groupIDs []int64, owner string, _ time.Duration) (func(), bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return nil, false, err
	}
	for _, groupID := range groupIDs {
		if existing := s.tripLocks[groupID]; existing != "" && existing != owner {
			return nil, false, nil
		}
	}
	for _, groupID := range groupIDs {
		s.tripLocks[groupID] = owner
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, groupID := range groupIDs {
				if s.tripLocks[groupID] == owner {
					delete(s.tripLocks, groupID)
				}
			}
		})
	}, true, nil
}

func cloneCapacityState(state *OpenAICapacityQuarantineState) *OpenAICapacityQuarantineState {
	if state == nil {
		return nil
	}
	copy := *state
	return &copy
}

func (s *capacityRuntimeStore) GetOpenAICapacityQuarantineState(_ context.Context, accountID int64) (*OpenAICapacityQuarantineState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return nil, err
	}
	return cloneCapacityState(s.states[accountID]), nil
}

func (s *capacityRuntimeStore) OpenOpenAICapacityQuarantine(_ context.Context, request OpenAICapacityQuarantineOpenRequest) (bool, *OpenAICapacityQuarantineState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return false, nil, err
	}
	if existing := s.states[request.AccountID]; existing != nil && existing.CooldownUntil.After(request.Now) {
		return false, cloneCapacityState(existing), nil
	}
	s.generation[request.AccountID]++
	state := &OpenAICapacityQuarantineState{
		AccountID: request.AccountID, State: request.State, Generation: s.generation[request.AccountID],
		CooldownUntil: request.Now.Add(request.Cooldown), OpenedAt: request.Now, RuleID: request.RuleID,
	}
	s.states[request.AccountID] = state
	return true, cloneCapacityState(state), nil
}

func (s *capacityRuntimeStore) AcquireOpenAICapacityProbe(_ context.Context, accountID int64, owner string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return false, err
	}
	state := s.states[accountID]
	if state == nil || state.State == openAICapacityStateClosed {
		return true, nil
	}
	if state.CooldownUntil.After(time.Now()) {
		return false, nil
	}
	if s.probes[accountID] != "" {
		return false, nil
	}
	s.probes[accountID] = owner
	state.State = openAICapacityStateHalfOpen
	state.Owner = owner
	return true, nil
}

func (s *capacityRuntimeStore) RenewOpenAICapacityProbe(_ context.Context, accountID int64, owner string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return false, err
	}
	return s.probes[accountID] == owner, nil
}

func (s *capacityRuntimeStore) ReleaseOpenAICapacityProbe(_ context.Context, accountID int64, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return err
	}
	if s.probes[accountID] == owner {
		delete(s.probes, accountID)
	}
	return nil
}

func (s *capacityRuntimeStore) CompleteOpenAICapacityProbe(_ context.Context, accountID int64, owner string, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return false, err
	}
	state := s.states[accountID]
	if state == nil || state.State != openAICapacityStateHalfOpen || state.Owner != owner || s.probes[accountID] != owner {
		return false, nil
	}
	delete(s.probes, accountID)
	delete(s.events, accountID)
	state.State = openAICapacityStateClosed
	state.Owner = ""
	state.CooldownUntil = time.Time{}
	return true, nil
}

type capacityRuntimeAccountRepo struct {
	AccountRepository
	accounts []Account
	err      error
}

func (r *capacityRuntimeAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, _ int64, _ string) ([]Account, error) {
	if r.err != nil {
		return nil, r.err
	}
	return append([]Account(nil), r.accounts...), nil
}

func capacityRuntimeTestSettings(mode OpenAICapacityQuarantineMode) OpenAICapacityQuarantineSettings {
	settings := *DefaultOpenAICapacityQuarantineSettings()
	settings.Mode = mode
	settings.ErrorThreshold = 2
	settings.InitialCooldownSeconds = 60
	settings.RetripCooldownSeconds = 120
	settings.MaxCooldownSeconds = 120
	settings.GroupPolicy = []OpenAICapacityGroupPolicy{{
		GroupID: 1, Enabled: true, MinRemainingAccounts: 1, MaxQuarantinedFraction: 0.75,
		GlobalSpikeDistinctAccounts: 99, GlobalSpikeWindowSeconds: 60,
	}}
	return settings
}

func newCapacityRuntimeWithSettings(t *testing.T, settings OpenAICapacityQuarantineSettings, accounts []Account, store *capacityRuntimeStore) *openAICapacityQuarantineRuntime {
	t.Helper()
	settingsService := NewSettingService(&capacityPolicySettingRepo{}, nil)
	_, err := settingsService.SetOpenAICapacityQuarantineSettings(context.Background(), &settings)
	require.NoError(t, err)
	return newOpenAICapacityQuarantineRuntime(settingsService, &capacityRuntimeAccountRepo{accounts: accounts}, store)
}

func newCapacityRuntimeForTest(t *testing.T, mode OpenAICapacityQuarantineMode, accounts []Account, store *capacityRuntimeStore) *openAICapacityQuarantineRuntime {
	t.Helper()
	return newCapacityRuntimeWithSettings(t, capacityRuntimeTestSettings(mode), accounts, store)
}

func capacityRuntimeAccount(id int64) Account {
	return Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, GroupIDs: []int64{1}}
}

func capacityRuntimeRequestContext(requestID string) context.Context {
	return context.WithValue(context.Background(), ctxkey.RequestID, requestID)
}

func TestOpenAICapacityQuarantineRuntime_DisabledAndShadowDoNotAffectScheduling(t *testing.T) {
	account := capacityRuntimeAccount(1)
	pool := []Account{account, capacityRuntimeAccount(2)}
	body := []byte(`{"error":{"code":"model_capacity"}}`)

	t.Run("disabled does not record", func(t *testing.T) {
		store := newCapacityRuntimeStore()
		runtime := newCapacityRuntimeForTest(t, OpenAICapacityQuarantineModeDisabled, pool, store)
		require.False(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("disabled"), &account, 503, body, "gpt-5"))
		require.Empty(t, store.events)
		require.False(t, runtime.ExcludesFromScheduling(context.Background(), &account))
	})

	t.Run("shadow records but never excludes", func(t *testing.T) {
		store := newCapacityRuntimeStore()
		runtime := newCapacityRuntimeForTest(t, OpenAICapacityQuarantineModeShadow, pool, store)
		require.False(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("shadow-1"), &account, 503, body, "gpt-5"))
		require.False(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("shadow-2"), &account, 503, body, "gpt-5"))
		require.Len(t, store.events[account.ID], 2)
		require.Nil(t, store.states[account.ID])
		require.False(t, runtime.ExcludesFromScheduling(context.Background(), &account))
	})
}

func TestOpenAICapacityQuarantineRuntime_TripsHalfOpensAndRecoversOnlyOwnedProbe(t *testing.T) {
	account := capacityRuntimeAccount(1)
	pool := []Account{account, capacityRuntimeAccount(2)}
	store := newCapacityRuntimeStore()
	runtime := newCapacityRuntimeForTest(t, OpenAICapacityQuarantineModeEnforce, pool, store)
	body := []byte(`{"error":{"code":"model_capacity"}}`)

	require.False(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("trip-1"), &account, 503, body, "gpt-5"))
	require.True(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("trip-2"), &account, 503, body, "gpt-5"))
	require.True(t, runtime.ExcludesFromScheduling(context.Background(), &account))

	store.mu.Lock()
	store.states[account.ID].CooldownUntil = time.Now().Add(-time.Second)
	store.mu.Unlock()
	release, completeSuccess, admitted := runtime.AcquireAdmission(capacityRuntimeRequestContext("probe-a"), &account)
	require.True(t, admitted)
	require.NotNil(t, release)
	require.NotNil(t, completeSuccess)
	_, _, admitted = runtime.AcquireAdmission(capacityRuntimeRequestContext("probe-b"), &account)
	require.False(t, admitted, "only one half-open probe may forward")

	// A late success from another request has no owner callback and cannot close it.
	runtime.CompleteSuccessfulProbe(context.Background(), account.ID, "not-the-owner")
	state, err := store.GetOpenAICapacityQuarantineState(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, openAICapacityStateHalfOpen, state.State)

	completeSuccess()
	state, err = store.GetOpenAICapacityQuarantineState(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, openAICapacityStateClosed, state.State)
	require.False(t, runtime.ExcludesFromScheduling(context.Background(), &account))
	release()
}

func TestOpenAICapacityQuarantineRuntime_PreservesMandatoryExclusionsAndFailsOpen(t *testing.T) {
	account := capacityRuntimeAccount(1)
	pool := []Account{account, capacityRuntimeAccount(2)}
	body := []byte(`{"error":{"code":"model_capacity","message":"capacity"}}`)

	t.Run("429 is never capacity", func(t *testing.T) {
		store := newCapacityRuntimeStore()
		runtime := newCapacityRuntimeForTest(t, OpenAICapacityQuarantineModeEnforce, pool, store)
		require.False(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("rate-limit"), &account, 429, body, "gpt-5"))
		require.Empty(t, store.events)
	})

	t.Run("store failure is fail-open", func(t *testing.T) {
		store := newCapacityRuntimeStore()
		store.fail = errors.New("redis unavailable")
		runtime := newCapacityRuntimeForTest(t, OpenAICapacityQuarantineModeEnforce, pool, store)
		require.False(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("store-down"), &account, 503, body, "gpt-5"))
		require.False(t, runtime.ExcludesFromScheduling(context.Background(), &account))
		_, _, admitted := runtime.AcquireAdmission(context.Background(), &account)
		require.True(t, admitted)
	})
}

func TestOpenAICapacityQuarantineRuntime_ProtectsMinimumRemainingPool(t *testing.T) {
	account := capacityRuntimeAccount(1)
	store := newCapacityRuntimeStore()
	runtime := newCapacityRuntimeForTest(t, OpenAICapacityQuarantineModeEnforce, []Account{account}, store)
	body := []byte(`{"error":{"code":"model_capacity"}}`)

	require.False(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("floor-1"), &account, 503, body, "gpt-5"))
	require.False(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("floor-2"), &account, 503, body, "gpt-5"))
	require.Nil(t, store.states[account.ID], "last remaining schedulable account must not be quarantined")
}

func TestOpenAICapacityQuarantineRuntime_UsesRetripWindowAndPoolGuards(t *testing.T) {
	body := []byte(`{"error":{"code":"model_capacity"}}`)

	t.Run("failed half-open uses retrip cooldown only inside retrip window", func(t *testing.T) {
		account := capacityRuntimeAccount(1)
		pool := []Account{account, capacityRuntimeAccount(2)}

		store := newCapacityRuntimeStore()
		store.states[account.ID] = &OpenAICapacityQuarantineState{
			AccountID: account.ID, State: openAICapacityStateHalfOpen, Owner: "old-owner",
			CooldownUntil: time.Now().Add(-time.Second), OpenedAt: time.Now().Add(-time.Minute),
		}
		runtime := newCapacityRuntimeForTest(t, OpenAICapacityQuarantineModeEnforce, pool, store)
		require.True(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("retrip-inside"), &account, 503, body, "gpt-5"))
		state, err := store.GetOpenAICapacityQuarantineState(context.Background(), account.ID)
		require.NoError(t, err)
		require.Equal(t, openAICapacityStateOpenRetrip, state.State)
		require.WithinDuration(t, time.Now().Add(120*time.Second), state.CooldownUntil, 2*time.Second)

		store = newCapacityRuntimeStore()
		store.states[account.ID] = &OpenAICapacityQuarantineState{
			AccountID: account.ID, State: openAICapacityStateHalfOpen, Owner: "old-owner",
			CooldownUntil: time.Now().Add(-time.Second), OpenedAt: time.Now().Add(-2 * time.Hour),
		}
		runtime = newCapacityRuntimeForTest(t, OpenAICapacityQuarantineModeEnforce, pool, store)
		require.True(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("retrip-expired"), &account, 503, body, "gpt-5"))
		state, err = store.GetOpenAICapacityQuarantineState(context.Background(), account.ID)
		require.NoError(t, err)
		require.Equal(t, openAICapacityStateOpenInitial, state.State)
		require.WithinDuration(t, time.Now().Add(60*time.Second), state.CooldownUntil, 2*time.Second)

		escalatingSettings := capacityRuntimeTestSettings(OpenAICapacityQuarantineModeEnforce)
		escalatingSettings.MaxCooldownSeconds = 600
		store = newCapacityRuntimeStore()
		store.states[account.ID] = &OpenAICapacityQuarantineState{
			AccountID: account.ID, State: openAICapacityStateHalfOpen, Owner: "old-owner",
			OpenedAt: time.Now().Add(-121 * time.Second), CooldownUntil: time.Now().Add(-time.Second),
		}
		runtime = newCapacityRuntimeWithSettings(t, escalatingSettings, pool, store)
		require.True(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("retrip-escalates"), &account, 503, body, "gpt-5"))
		state, err = store.GetOpenAICapacityQuarantineState(context.Background(), account.ID)
		require.NoError(t, err)
		require.Equal(t, openAICapacityStateOpenRetrip, state.State)
		require.WithinDuration(t, time.Now().Add(240*time.Second), state.CooldownUntil, 2*time.Second)
	})

	t.Run("maximum quarantine fraction and group spike suppress trips", func(t *testing.T) {
		account := capacityRuntimeAccount(1)
		pool := []Account{account, capacityRuntimeAccount(2), capacityRuntimeAccount(3)}

		fractionSettings := capacityRuntimeTestSettings(OpenAICapacityQuarantineModeEnforce)
		fractionSettings.GroupPolicy[0].MaxQuarantinedFraction = 0.25
		store := newCapacityRuntimeStore()
		runtime := newCapacityRuntimeWithSettings(t, fractionSettings, pool, store)
		require.False(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("fraction-1"), &account, 503, body, "gpt-5"))
		require.False(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("fraction-2"), &account, 503, body, "gpt-5"))
		require.Nil(t, store.states[account.ID], "the configured maximum fraction must be a hard guard")

		spikeSettings := capacityRuntimeTestSettings(OpenAICapacityQuarantineModeEnforce)
		spikeSettings.GroupPolicy[0].GlobalSpikeDistinctAccounts = 2
		store = newCapacityRuntimeStore()
		runtime = newCapacityRuntimeWithSettings(t, spikeSettings, pool, store)
		first := pool[0]
		second := pool[1]
		require.False(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("spike-a-1"), &first, 503, body, "gpt-5"))
		require.True(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("spike-a-2"), &first, 503, body, "gpt-5"))
		require.False(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("spike-b-1"), &second, 503, body, "gpt-5"))
		require.False(t, runtime.RecordUpstreamError(capacityRuntimeRequestContext("spike-b-2"), &second, 503, body, "gpt-5"))
		require.Nil(t, store.states[second.ID], "a global spike must stop further per-account trips")
	})
}

type failingCapacityPolicySettingRepo struct {
	*capacityPolicySettingRepo
	err error
}

func (r *failingCapacityPolicySettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return r.capacityPolicySettingRepo.GetValue(ctx, key)
}

func TestOpenAICapacityQuarantineRuntime_SettingsFailureAfterCachedReadFailsOpen(t *testing.T) {
	account := capacityRuntimeAccount(1)
	store := newCapacityRuntimeStore()
	store.states[account.ID] = &OpenAICapacityQuarantineState{
		AccountID: account.ID, State: openAICapacityStateOpenInitial, CooldownUntil: time.Now().Add(time.Minute),
	}
	repo := &failingCapacityPolicySettingRepo{capacityPolicySettingRepo: &capacityPolicySettingRepo{}}
	settingsService := NewSettingService(repo, nil)
	settings := capacityRuntimeTestSettings(OpenAICapacityQuarantineModeEnforce)
	_, err := settingsService.SetOpenAICapacityQuarantineSettings(context.Background(), &settings)
	require.NoError(t, err)
	runtime := newOpenAICapacityQuarantineRuntime(settingsService, &capacityRuntimeAccountRepo{accounts: []Account{account, capacityRuntimeAccount(2)}}, store)

	require.True(t, runtime.ExcludesFromScheduling(context.Background(), &account), "the initial readable enforce policy excludes active cooldown")
	repo.err = errors.New("settings unavailable")
	runtime.policyMu.Lock()
	runtime.policyExpires = time.Now().Add(-time.Second)
	runtime.policyMu.Unlock()
	require.False(t, runtime.ExcludesFromScheduling(context.Background(), &account), "a later settings read failure must not enforce a stale cached policy")
}
