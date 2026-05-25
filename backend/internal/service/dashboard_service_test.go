package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type usageRepoStub struct {
	UsageLogRepository
	stats                  *usagestats.DashboardStats
	rangeStats             *usagestats.DashboardStats
	err                    error
	rangeErr               error
	calls                  int32
	rangeCalls             int32
	rangeStart             time.Time
	rangeEnd               time.Time
	onCall                 chan struct{}
	groupSummaries         []usagestats.GroupUsageSummary
	groupSummaryErr        error
	groupSummaryCalls      int32
	groupSummaryTodayStart time.Time
}

func (s *usageRepoStub) GetDashboardStats(ctx context.Context) (*usagestats.DashboardStats, error) {
	atomic.AddInt32(&s.calls, 1)
	if s.onCall != nil {
		select {
		case s.onCall <- struct{}{}:
		default:
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.stats, nil
}

func (s *usageRepoStub) GetDashboardStatsWithRange(ctx context.Context, start, end time.Time) (*usagestats.DashboardStats, error) {
	atomic.AddInt32(&s.rangeCalls, 1)
	s.rangeStart = start
	s.rangeEnd = end
	if s.rangeErr != nil {
		return nil, s.rangeErr
	}
	if s.rangeStats != nil {
		return s.rangeStats, nil
	}
	return s.stats, nil
}

func (s *usageRepoStub) GetAllGroupUsageSummary(ctx context.Context, todayStart time.Time) ([]usagestats.GroupUsageSummary, error) {
	atomic.AddInt32(&s.groupSummaryCalls, 1)
	s.groupSummaryTodayStart = todayStart
	if s.groupSummaryErr != nil {
		return nil, s.groupSummaryErr
	}
	return s.groupSummaries, nil
}

type dashboardCacheStub struct {
	get                  func(ctx context.Context) (string, error)
	set                  func(ctx context.Context, data string, ttl time.Duration) error
	del                  func(ctx context.Context) error
	getGroupSummary      func(ctx context.Context, todayStart time.Time) (string, error)
	setGroupSummary      func(ctx context.Context, todayStart time.Time, data string, ttl time.Duration) error
	getCalls             int32
	setCalls             int32
	delCalls             int32
	groupSummaryGetCalls int32
	groupSummarySetCalls int32
	lastSetMu            sync.Mutex
	lastSet              string
	lastGroupSummaryMu   sync.Mutex
	lastGroupSummarySet  string
	lastGroupSummaryTTL  time.Duration
	lastGroupSummaryKey  time.Time
}

func (c *dashboardCacheStub) GetDashboardStats(ctx context.Context) (string, error) {
	atomic.AddInt32(&c.getCalls, 1)
	if c.get != nil {
		return c.get(ctx)
	}
	return "", ErrDashboardStatsCacheMiss
}

func (c *dashboardCacheStub) SetDashboardStats(ctx context.Context, data string, ttl time.Duration) error {
	atomic.AddInt32(&c.setCalls, 1)
	c.lastSetMu.Lock()
	c.lastSet = data
	c.lastSetMu.Unlock()
	if c.set != nil {
		return c.set(ctx, data, ttl)
	}
	return nil
}

func (c *dashboardCacheStub) DeleteDashboardStats(ctx context.Context) error {
	atomic.AddInt32(&c.delCalls, 1)
	if c.del != nil {
		return c.del(ctx)
	}
	return nil
}

func (c *dashboardCacheStub) GetGroupUsageSummary(ctx context.Context, todayStart time.Time) (string, error) {
	atomic.AddInt32(&c.groupSummaryGetCalls, 1)
	if c.getGroupSummary != nil {
		return c.getGroupSummary(ctx, todayStart)
	}
	return "", ErrDashboardStatsCacheMiss
}

func (c *dashboardCacheStub) SetGroupUsageSummary(ctx context.Context, todayStart time.Time, data string, ttl time.Duration) error {
	atomic.AddInt32(&c.groupSummarySetCalls, 1)
	c.lastGroupSummaryMu.Lock()
	c.lastGroupSummarySet = data
	c.lastGroupSummaryTTL = ttl
	c.lastGroupSummaryKey = todayStart
	c.lastGroupSummaryMu.Unlock()
	if c.setGroupSummary != nil {
		return c.setGroupSummary(ctx, todayStart, data, ttl)
	}
	return nil
}

type dashboardAggregationRepoStub struct {
	watermark time.Time
	err       error
}

func (s *dashboardAggregationRepoStub) AggregateRange(ctx context.Context, start, end time.Time) error {
	return nil
}

func (s *dashboardAggregationRepoStub) RecomputeRange(ctx context.Context, start, end time.Time) error {
	return nil
}

func (s *dashboardAggregationRepoStub) GetAggregationWatermark(ctx context.Context) (time.Time, error) {
	if s.err != nil {
		return time.Time{}, s.err
	}
	return s.watermark, nil
}

func (s *dashboardAggregationRepoStub) UpdateAggregationWatermark(ctx context.Context, aggregatedAt time.Time) error {
	return nil
}

func (s *dashboardAggregationRepoStub) CleanupAggregates(ctx context.Context, hourlyCutoff, dailyCutoff time.Time) error {
	return nil
}

func (s *dashboardAggregationRepoStub) CleanupUsageLogs(ctx context.Context, cutoff time.Time) error {
	return nil
}

func (s *dashboardAggregationRepoStub) CleanupUsageBillingDedup(ctx context.Context, cutoff time.Time) error {
	return nil
}

func (s *dashboardAggregationRepoStub) EnsureUsageLogsPartitions(ctx context.Context, now time.Time) error {
	return nil
}

func (c *dashboardCacheStub) readLastEntry(t *testing.T) dashboardStatsCacheEntry {
	t.Helper()
	c.lastSetMu.Lock()
	data := c.lastSet
	c.lastSetMu.Unlock()

	var entry dashboardStatsCacheEntry
	err := json.Unmarshal([]byte(data), &entry)
	require.NoError(t, err)
	return entry
}

func TestDashboardService_CacheHitFresh(t *testing.T) {
	stats := &usagestats.DashboardStats{
		TotalUsers:     10,
		StatsUpdatedAt: time.Unix(0, 0).UTC().Format(time.RFC3339),
		StatsStale:     true,
	}
	entry := dashboardStatsCacheEntry{
		Stats:     stats,
		UpdatedAt: time.Now().Unix(),
	}
	payload, err := json.Marshal(entry)
	require.NoError(t, err)

	cache := &dashboardCacheStub{
		get: func(ctx context.Context) (string, error) {
			return string(payload), nil
		},
	}
	repo := &usageRepoStub{
		stats: &usagestats.DashboardStats{TotalUsers: 99},
	}
	aggRepo := &dashboardAggregationRepoStub{watermark: time.Unix(0, 0).UTC()}
	cfg := &config.Config{
		Dashboard: config.DashboardCacheConfig{Enabled: true},
		DashboardAgg: config.DashboardAggregationConfig{
			Enabled: true,
		},
	}
	svc := NewDashboardService(repo, aggRepo, cache, cfg)

	got, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.Equal(t, stats, got)
	require.Equal(t, int32(0), atomic.LoadInt32(&repo.calls))
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.getCalls))
	require.Equal(t, int32(0), atomic.LoadInt32(&cache.setCalls))
}

func TestDashboardService_CacheMiss_StoresCache(t *testing.T) {
	stats := &usagestats.DashboardStats{
		TotalUsers:     7,
		StatsUpdatedAt: time.Unix(0, 0).UTC().Format(time.RFC3339),
		StatsStale:     true,
	}
	cache := &dashboardCacheStub{
		get: func(ctx context.Context) (string, error) {
			return "", ErrDashboardStatsCacheMiss
		},
	}
	repo := &usageRepoStub{stats: stats}
	aggRepo := &dashboardAggregationRepoStub{watermark: time.Unix(0, 0).UTC()}
	cfg := &config.Config{
		Dashboard: config.DashboardCacheConfig{Enabled: true},
		DashboardAgg: config.DashboardAggregationConfig{
			Enabled: true,
		},
	}
	svc := NewDashboardService(repo, aggRepo, cache, cfg)

	got, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.Equal(t, stats, got)
	require.Equal(t, int32(1), atomic.LoadInt32(&repo.calls))
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.getCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.setCalls))
	entry := cache.readLastEntry(t)
	require.Equal(t, stats, entry.Stats)
	require.WithinDuration(t, time.Now(), time.Unix(entry.UpdatedAt, 0), time.Second)
}

func TestDashboardService_CacheDisabled_SkipsCache(t *testing.T) {
	stats := &usagestats.DashboardStats{
		TotalUsers:     3,
		StatsUpdatedAt: time.Unix(0, 0).UTC().Format(time.RFC3339),
		StatsStale:     true,
	}
	cache := &dashboardCacheStub{
		get: func(ctx context.Context) (string, error) {
			return "", nil
		},
	}
	repo := &usageRepoStub{stats: stats}
	aggRepo := &dashboardAggregationRepoStub{watermark: time.Unix(0, 0).UTC()}
	cfg := &config.Config{
		Dashboard: config.DashboardCacheConfig{Enabled: false},
		DashboardAgg: config.DashboardAggregationConfig{
			Enabled: true,
		},
	}
	svc := NewDashboardService(repo, aggRepo, cache, cfg)

	got, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.Equal(t, stats, got)
	require.Equal(t, int32(1), atomic.LoadInt32(&repo.calls))
	require.Equal(t, int32(0), atomic.LoadInt32(&cache.getCalls))
	require.Equal(t, int32(0), atomic.LoadInt32(&cache.setCalls))
}

func TestDashboardService_CacheHitStale_TriggersAsyncRefresh(t *testing.T) {
	staleStats := &usagestats.DashboardStats{
		TotalUsers:     11,
		StatsUpdatedAt: time.Unix(0, 0).UTC().Format(time.RFC3339),
		StatsStale:     true,
	}
	entry := dashboardStatsCacheEntry{
		Stats:     staleStats,
		UpdatedAt: time.Now().Add(-defaultDashboardStatsFreshTTL * 2).Unix(),
	}
	payload, err := json.Marshal(entry)
	require.NoError(t, err)

	cache := &dashboardCacheStub{
		get: func(ctx context.Context) (string, error) {
			return string(payload), nil
		},
	}
	refreshCh := make(chan struct{}, 1)
	repo := &usageRepoStub{
		stats:  &usagestats.DashboardStats{TotalUsers: 22},
		onCall: refreshCh,
	}
	aggRepo := &dashboardAggregationRepoStub{watermark: time.Unix(0, 0).UTC()}
	cfg := &config.Config{
		Dashboard: config.DashboardCacheConfig{Enabled: true},
		DashboardAgg: config.DashboardAggregationConfig{
			Enabled: true,
		},
	}
	svc := NewDashboardService(repo, aggRepo, cache, cfg)

	got, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.Equal(t, staleStats, got)

	select {
	case <-refreshCh:
	case <-time.After(1 * time.Second):
		t.Fatal("等待异步刷新超时")
	}
	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&cache.setCalls) >= 1
	}, 1*time.Second, 10*time.Millisecond)
}

func TestDashboardService_CacheParseError_EvictsAndRefetches(t *testing.T) {
	cache := &dashboardCacheStub{
		get: func(ctx context.Context) (string, error) {
			return "not-json", nil
		},
	}
	stats := &usagestats.DashboardStats{TotalUsers: 9}
	repo := &usageRepoStub{stats: stats}
	aggRepo := &dashboardAggregationRepoStub{watermark: time.Unix(0, 0).UTC()}
	cfg := &config.Config{
		Dashboard: config.DashboardCacheConfig{Enabled: true},
		DashboardAgg: config.DashboardAggregationConfig{
			Enabled: true,
		},
	}
	svc := NewDashboardService(repo, aggRepo, cache, cfg)

	got, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.Equal(t, stats, got)
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.delCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&repo.calls))
}

func TestDashboardService_CacheParseError_RepoFailure(t *testing.T) {
	cache := &dashboardCacheStub{
		get: func(ctx context.Context) (string, error) {
			return "not-json", nil
		},
	}
	repo := &usageRepoStub{err: errors.New("db down")}
	aggRepo := &dashboardAggregationRepoStub{watermark: time.Unix(0, 0).UTC()}
	cfg := &config.Config{
		Dashboard: config.DashboardCacheConfig{Enabled: true},
		DashboardAgg: config.DashboardAggregationConfig{
			Enabled: true,
		},
	}
	svc := NewDashboardService(repo, aggRepo, cache, cfg)

	_, err := svc.GetDashboardStats(context.Background())
	require.Error(t, err)
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.delCalls))
}

func TestDashboardService_StatsUpdatedAtEpochWhenMissing(t *testing.T) {
	stats := &usagestats.DashboardStats{}
	repo := &usageRepoStub{stats: stats}
	aggRepo := &dashboardAggregationRepoStub{watermark: time.Unix(0, 0).UTC()}
	cfg := &config.Config{Dashboard: config.DashboardCacheConfig{Enabled: false}}
	svc := NewDashboardService(repo, aggRepo, nil, cfg)

	got, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.Equal(t, "1970-01-01T00:00:00Z", got.StatsUpdatedAt)
	require.True(t, got.StatsStale)
}

func TestDashboardService_StatsStaleFalseWhenFresh(t *testing.T) {
	aggNow := time.Now().UTC().Truncate(time.Second)
	stats := &usagestats.DashboardStats{}
	repo := &usageRepoStub{stats: stats}
	aggRepo := &dashboardAggregationRepoStub{watermark: aggNow}
	cfg := &config.Config{
		Dashboard: config.DashboardCacheConfig{Enabled: false},
		DashboardAgg: config.DashboardAggregationConfig{
			Enabled:         true,
			IntervalSeconds: 60,
			LookbackSeconds: 120,
		},
	}
	svc := NewDashboardService(repo, aggRepo, nil, cfg)

	got, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.Equal(t, aggNow.Format(time.RFC3339), got.StatsUpdatedAt)
	require.False(t, got.StatsStale)
}

func TestDashboardService_AggDisabled_UsesUsageLogsFallback(t *testing.T) {
	expected := &usagestats.DashboardStats{TotalUsers: 42}
	repo := &usageRepoStub{
		rangeStats: expected,
		err:        errors.New("should not call aggregated stats"),
	}
	cfg := &config.Config{
		Dashboard: config.DashboardCacheConfig{Enabled: false},
		DashboardAgg: config.DashboardAggregationConfig{
			Enabled: false,
			Retention: config.DashboardAggregationRetentionConfig{
				UsageLogsDays: 7,
			},
		},
	}
	svc := NewDashboardService(repo, nil, nil, cfg)

	got, err := svc.GetDashboardStats(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(42), got.TotalUsers)
	require.Equal(t, int32(0), atomic.LoadInt32(&repo.calls))
	require.Equal(t, int32(1), atomic.LoadInt32(&repo.rangeCalls))
	require.False(t, repo.rangeEnd.IsZero())
	require.Equal(t, truncateToDayUTC(repo.rangeEnd.AddDate(0, 0, -7)), repo.rangeStart)
}

// --- GroupUsageSummary cache tests (Stage 1) ---

func TestDashboardService_GroupSummary_CacheMissThenFallbackAndStore(t *testing.T) {
	summaries := []usagestats.GroupUsageSummary{
		{GroupID: 1, TotalCost: 12.5, TodayCost: 3.5},
		{GroupID: 2, TotalCost: 0, TodayCost: 0},
	}
	cache := &dashboardCacheStub{
		getGroupSummary: func(ctx context.Context, todayStart time.Time) (string, error) {
			return "", ErrDashboardStatsCacheMiss
		},
	}
	repo := &usageRepoStub{groupSummaries: summaries}
	cfg := &config.Config{
		Dashboard: config.DashboardCacheConfig{
			Enabled:                true,
			GroupSummaryTTLSeconds: 60,
		},
	}
	svc := NewDashboardService(repo, nil, cache, cfg)

	todayStart := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)
	got, err := svc.GetGroupUsageSummary(context.Background(), todayStart)
	require.NoError(t, err)
	require.Equal(t, summaries, got)
	require.Equal(t, int32(1), atomic.LoadInt32(&repo.groupSummaryCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.groupSummaryGetCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.groupSummarySetCalls))

	cache.lastGroupSummaryMu.Lock()
	defer cache.lastGroupSummaryMu.Unlock()
	require.Equal(t, 60*time.Second, cache.lastGroupSummaryTTL)
	require.Equal(t, todayStart, cache.lastGroupSummaryKey)

	var entry groupUsageSummaryCacheEntry
	require.NoError(t, json.Unmarshal([]byte(cache.lastGroupSummarySet), &entry))
	require.Equal(t, summaries, entry.Summaries)
	require.WithinDuration(t, time.Now(), time.Unix(entry.UpdatedAt, 0), 2*time.Second)
}

func TestDashboardService_GroupSummary_CacheHitSkipsRepo(t *testing.T) {
	summaries := []usagestats.GroupUsageSummary{
		{GroupID: 7, TotalCost: 100, TodayCost: 10},
	}
	entry := groupUsageSummaryCacheEntry{
		Summaries: summaries,
		UpdatedAt: time.Now().Unix(),
	}
	payload, err := json.Marshal(entry)
	require.NoError(t, err)

	cache := &dashboardCacheStub{
		getGroupSummary: func(ctx context.Context, todayStart time.Time) (string, error) {
			return string(payload), nil
		},
	}
	repo := &usageRepoStub{
		groupSummaries:  []usagestats.GroupUsageSummary{{GroupID: 999}},
		groupSummaryErr: errors.New("repo must not be called"),
	}
	cfg := &config.Config{
		Dashboard: config.DashboardCacheConfig{
			Enabled:                true,
			GroupSummaryTTLSeconds: 60,
		},
	}
	svc := NewDashboardService(repo, nil, cache, cfg)

	todayStart := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)
	got, err := svc.GetGroupUsageSummary(context.Background(), todayStart)
	require.NoError(t, err)
	require.Equal(t, summaries, got)
	require.Equal(t, int32(0), atomic.LoadInt32(&repo.groupSummaryCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.groupSummaryGetCalls))
	require.Equal(t, int32(0), atomic.LoadInt32(&cache.groupSummarySetCalls))
}

func TestDashboardService_GroupSummary_TTLZeroDisablesCache(t *testing.T) {
	summaries := []usagestats.GroupUsageSummary{{GroupID: 1, TotalCost: 5, TodayCost: 1}}
	cache := &dashboardCacheStub{
		getGroupSummary: func(ctx context.Context, todayStart time.Time) (string, error) {
			t.Fatal("cache 不应该被读取")
			return "", nil
		},
		setGroupSummary: func(ctx context.Context, todayStart time.Time, data string, ttl time.Duration) error {
			t.Fatal("cache 不应该被写入")
			return nil
		},
	}
	repo := &usageRepoStub{groupSummaries: summaries}
	cfg := &config.Config{
		Dashboard: config.DashboardCacheConfig{
			Enabled:                true,
			GroupSummaryTTLSeconds: 0, // 显式禁用
		},
	}
	svc := NewDashboardService(repo, nil, cache, cfg)

	got, err := svc.GetGroupUsageSummary(context.Background(), time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, summaries, got)
	require.Equal(t, int32(1), atomic.LoadInt32(&repo.groupSummaryCalls))
	require.Equal(t, int32(0), atomic.LoadInt32(&cache.groupSummaryGetCalls))
	require.Equal(t, int32(0), atomic.LoadInt32(&cache.groupSummarySetCalls))
}

func TestDashboardService_GroupSummary_DifferentTodayStartIndependentCache(t *testing.T) {
	// 模拟两个不同时区调用方传入两个不同 todayStart，
	// 验证：cache 读取按 todayStart 区分，且写入时携带的 key 各自独立。
	var observed []time.Time
	var mu sync.Mutex
	cache := &dashboardCacheStub{
		getGroupSummary: func(ctx context.Context, todayStart time.Time) (string, error) {
			mu.Lock()
			observed = append(observed, todayStart)
			mu.Unlock()
			return "", ErrDashboardStatsCacheMiss
		},
	}
	repo := &usageRepoStub{groupSummaries: []usagestats.GroupUsageSummary{{GroupID: 1}}}
	cfg := &config.Config{
		Dashboard: config.DashboardCacheConfig{
			Enabled:                true,
			GroupSummaryTTLSeconds: 60,
		},
	}
	svc := NewDashboardService(repo, nil, cache, cfg)

	utcStart := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)
	shanghaiStart := time.Date(2026, 5, 25, 0, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))

	_, err := svc.GetGroupUsageSummary(context.Background(), utcStart)
	require.NoError(t, err)
	_, err = svc.GetGroupUsageSummary(context.Background(), shanghaiStart)
	require.NoError(t, err)

	require.Equal(t, int32(2), atomic.LoadInt32(&cache.groupSummaryGetCalls))
	require.Equal(t, int32(2), atomic.LoadInt32(&cache.groupSummarySetCalls))
	require.Equal(t, int32(2), atomic.LoadInt32(&repo.groupSummaryCalls))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, observed, 2)
	require.False(t, observed[0].Equal(observed[1]), "两次 todayStart 应该不同，让缓存键自然区分")
}

func TestDashboardService_GroupSummary_CacheParseError_FallbackToRepo(t *testing.T) {
	summaries := []usagestats.GroupUsageSummary{{GroupID: 1, TotalCost: 3, TodayCost: 1}}
	cache := &dashboardCacheStub{
		getGroupSummary: func(ctx context.Context, todayStart time.Time) (string, error) {
			return "not-json", nil
		},
	}
	repo := &usageRepoStub{groupSummaries: summaries}
	cfg := &config.Config{
		Dashboard: config.DashboardCacheConfig{
			Enabled:                true,
			GroupSummaryTTLSeconds: 60,
		},
	}
	svc := NewDashboardService(repo, nil, cache, cfg)

	got, err := svc.GetGroupUsageSummary(context.Background(), time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, summaries, got)
	require.Equal(t, int32(1), atomic.LoadInt32(&repo.groupSummaryCalls))
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.groupSummaryGetCalls))
	// 解析失败也会写回新缓存覆盖坏数据
	require.Equal(t, int32(1), atomic.LoadInt32(&cache.groupSummarySetCalls))
}
