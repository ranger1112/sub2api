//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type capacityAdmissionSettingRepo struct {
	service.SettingRepository
	mu     sync.Mutex
	values map[string]string
}

func (r *capacityAdmissionSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}
func (r *capacityAdmissionSettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}
func (r *capacityAdmissionSettingRepo) CompareAndSwapSetting(_ context.Context, key, expected string, expectedExists bool, value string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = map[string]string{}
	}
	current, exists := r.values[key]
	if exists != expectedExists || (exists && current != expected) {
		return false, nil
	}
	r.values[key] = value
	return true, nil
}

type capacityAdmissionAccountRepo struct{ service.AccountRepository }

func (r *capacityAdmissionAccountRepo) ListSchedulableByGroupIDAndPlatform(_ context.Context, _ int64, _ string) ([]service.Account, error) {
	return []service.Account{
		{ID: 71, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{1}},
		{ID: 72, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{1}},
	}, nil
}

type capacityAdmissionStore struct {
	mu       sync.Mutex
	state    *service.OpenAICapacityQuarantineState
	probe    string
	released atomic.Int64
}

func (s *capacityAdmissionStore) RecordOpenAICapacityEvent(context.Context, int64, string, time.Time, time.Duration) (int, bool, error) {
	return 0, false, nil
}
func (s *capacityAdmissionStore) RecordOpenAICapacityGroupEvent(context.Context, int64, string, int64, time.Time, time.Duration) (int, error) {
	return 0, nil
}
func (s *capacityAdmissionStore) AcquireOpenAICapacityTripLocks(context.Context, []int64, string, time.Duration) (func(), bool, error) {
	return func() {}, true, nil
}
func (s *capacityAdmissionStore) GetOpenAICapacityQuarantineState(_ context.Context, _ int64) (*service.OpenAICapacityQuarantineState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil {
		return nil, nil
	}
	copy := *s.state
	return &copy, nil
}
func (s *capacityAdmissionStore) OpenOpenAICapacityQuarantine(context.Context, service.OpenAICapacityQuarantineOpenRequest) (bool, *service.OpenAICapacityQuarantineState, error) {
	return false, nil, nil
}
func (s *capacityAdmissionStore) AcquireOpenAICapacityProbe(_ context.Context, _ int64, owner string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil || s.state.State == "closed" {
		return true, nil
	}
	if s.state.CooldownUntil.After(time.Now()) || s.probe != "" {
		return false, nil
	}
	s.probe = owner
	s.state.State = "half_open"
	s.state.Owner = owner
	return true, nil
}
func (s *capacityAdmissionStore) RenewOpenAICapacityProbe(_ context.Context, _ int64, owner string, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.probe == owner, nil
}
func (s *capacityAdmissionStore) ReleaseOpenAICapacityProbe(_ context.Context, _ int64, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.probe == owner {
		s.probe = ""
		s.released.Add(1)
	}
	return nil
}
func (s *capacityAdmissionStore) CompleteOpenAICapacityProbe(_ context.Context, _ int64, owner string, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == nil || s.state.State != "half_open" || s.state.Owner != owner || s.probe != owner {
		return false, nil
	}
	s.probe = ""
	s.state.State = "closed"
	s.state.Owner = ""
	s.state.CooldownUntil = time.Time{}
	return true, nil
}

type capacityAdmissionConcurrencyCache struct {
	service.ConcurrencyCache
	releases atomic.Int64
}

func (c *capacityAdmissionConcurrencyCache) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	return true, nil
}
func (c *capacityAdmissionConcurrencyCache) ReleaseAccountSlot(context.Context, int64, string) error {
	c.releases.Add(1)
	return nil
}

func newCapacityAdmissionHandler(t *testing.T, store *capacityAdmissionStore) (*OpenAIGatewayHandler, *capacityAdmissionConcurrencyCache) {
	t.Helper()
	settings := *service.DefaultOpenAICapacityQuarantineSettings()
	settings.Mode = service.OpenAICapacityQuarantineModeEnforce
	settings.GroupPolicy = []service.OpenAICapacityGroupPolicy{{
		GroupID: 1, Enabled: true, MinRemainingAccounts: 1, MaxQuarantinedFraction: 0.75,
		GlobalSpikeDistinctAccounts: 99, GlobalSpikeWindowSeconds: 60,
	}}
	settingsService := service.NewSettingService(&capacityAdmissionSettingRepo{}, nil)
	_, err := settingsService.SetOpenAICapacityQuarantineSettings(context.Background(), &settings)
	require.NoError(t, err)

	repo := &capacityAdmissionAccountRepo{}
	gateway := service.NewOpenAIGatewayServiceWithCapacity(
		repo, // AccountRepository
		nil,  // UsageLogRepository
		nil,  // UsageBillingRepository
		nil,  // UserRepository
		nil,  // UserSubscriptionRepository
		nil,  // UserGroupRateRepository
		nil,  // GatewayCache
		nil,  // Config
		nil,  // SchedulerSnapshotService
		nil,  // ConcurrencyService
		nil,  // BillingService
		nil,  // RateLimitService
		nil,  // BillingCacheService
		nil,  // HTTPUpstream
		nil,  // DeferredService
		nil,  // OpenAITokenProvider
		nil,  // GrokTokenProvider
		nil,  // ModelPricingResolver
		nil,  // ChannelService
		nil,  // BalanceNotifyService
		settingsService,
		nil, // UserPlatformQuotaRepository
		store,
	)
	cache := &capacityAdmissionConcurrencyCache{}
	return &OpenAIGatewayHandler{
		gatewayService:    gateway,
		concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
	}, cache
}

func capacitySlotSelection() *service.AccountSelectionResult {
	account := &service.Account{
		ID: 71, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{1},
	}
	return &service.AccountSelectionResult{
		Account:  account,
		WaitPlan: &service.AccountWaitPlan{AccountID: account.ID, MaxConcurrency: 1, MaxWaiting: 1, Timeout: time.Second},
	}
}

func TestAcquireResponsesAccountSlot_CapacityVetoReleasesSlotAndOwnedSuccessRecovers(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("active cooldown veto releases ordinary slot without response", func(t *testing.T) {
		store := &capacityAdmissionStore{state: &service.OpenAICapacityQuarantineState{
			AccountID: 71, State: "open_initial", CooldownUntil: time.Now().Add(time.Minute),
		}}
		h, cache := newCapacityAdmissionHandler(t, store)
		c, _ := gin.CreateTestContext(nil)
		c.Request = newHandlerTestRequest(t)
		streamStarted := false

		release, result := h.acquireResponsesAccountSlot(c, nil, "", capacitySlotSelection(), false, &streamStarted, zap.NewNop())
		require.Equal(t, openAISlotAcquireCapacityVetoed, result)
		require.Nil(t, release)
		require.Equal(t, int64(1), cache.releases.Load(), "capacity reject must not leak the ordinary account slot")
	})

	t.Run("half-open success closes only the admitted probe", func(t *testing.T) {
		store := &capacityAdmissionStore{state: &service.OpenAICapacityQuarantineState{
			AccountID: 71, State: "open_initial", CooldownUntil: time.Now().Add(-time.Second),
		}}
		h, cache := newCapacityAdmissionHandler(t, store)
		c, _ := gin.CreateTestContext(nil)
		c.Request = newHandlerTestRequest(t)
		streamStarted := false

		release, result := h.acquireResponsesAccountSlot(c, nil, "", capacitySlotSelection(), false, &streamStarted, zap.NewNop())
		require.Equal(t, openAISlotAcquireOK, result)
		require.NotNil(t, release)
		h.reportOpenAIAccountScheduleResult(c, 71, "gpt-5", true, nil)
		store.mu.Lock()
		state := *store.state
		store.mu.Unlock()
		require.Equal(t, "closed", state.State)
		release()
		require.Equal(t, int64(1), cache.releases.Load())
	})
}

func newHandlerTestRequest(t *testing.T) *http.Request {
	t.Helper()
	return httptest.NewRequest("POST", "/v1/responses", nil)
}
