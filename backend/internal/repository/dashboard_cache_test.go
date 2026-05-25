package repository

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestNewDashboardCacheKeyPrefix(t *testing.T) {
	cache := NewDashboardCache(nil, &config.Config{
		Dashboard: config.DashboardCacheConfig{
			KeyPrefix: "prod",
		},
	})
	impl, ok := cache.(*dashboardCache)
	require.True(t, ok)
	require.Equal(t, "prod:", impl.keyPrefix)

	cache = NewDashboardCache(nil, &config.Config{
		Dashboard: config.DashboardCacheConfig{
			KeyPrefix: "staging:",
		},
	})
	impl, ok = cache.(*dashboardCache)
	require.True(t, ok)
	require.Equal(t, "staging:", impl.keyPrefix)
}

func TestDashboardCacheGroupSummaryKey(t *testing.T) {
	cache := NewDashboardCache(nil, &config.Config{
		Dashboard: config.DashboardCacheConfig{KeyPrefix: "prod"},
	}).(*dashboardCache)

	utcStart := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)
	shanghaiStart := time.Date(2026, 5, 25, 0, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*3600))

	utcKey := cache.buildGroupSummaryKey(utcStart)
	shanghaiKey := cache.buildGroupSummaryKey(shanghaiStart)

	require.Contains(t, utcKey, "prod:dashboard:group-summary:v1:")
	require.Contains(t, shanghaiKey, "prod:dashboard:group-summary:v1:")
	require.NotEqual(t, utcKey, shanghaiKey, "不同时区当日起点必须生成不同 cache key，避免数据互相覆盖")

	// 同一时区相同 todayStart 应得到相同 key（幂等）
	require.Equal(t, utcKey, cache.buildGroupSummaryKey(utcStart))
}

func TestDashboardCacheGroupSummaryKeyNoPrefix(t *testing.T) {
	cache := NewDashboardCache(nil, &config.Config{}).(*dashboardCache)
	cache.keyPrefix = ""

	utcStart := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)
	key := cache.buildGroupSummaryKey(utcStart)
	require.True(t, len(key) > len("dashboard:group-summary:v1:"))
	require.Contains(t, key, "dashboard:group-summary:v1:")
}
