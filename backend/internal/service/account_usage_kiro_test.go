//go:build unit

package service

import (
	"testing"
	"time"
)

// TestBuildKiroUsageExtraUpdates 验证 Kiro 用量快照落库键的拍平:tier / 窗口有值才写,
// updated_at 始终写,数值/时间格式正确。
func TestBuildKiroUsageExtraUpdates(t *testing.T) {
	reset := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	usage := &UsageInfo{
		SubscriptionTier:    "ULTRA",
		SubscriptionTierRaw: "KIRO POWER",
		FiveHour: &UsageProgress{
			Utilization:   42.5,
			UsedRequests:  425,
			LimitRequests: 1000,
			ResetsAt:      &reset,
		},
	}
	got := buildKiroUsageExtraUpdates(usage, now)

	if got["kiro_subscription_tier_raw"] != "KIRO POWER" {
		t.Fatalf("tier_raw = %v", got["kiro_subscription_tier_raw"])
	}
	if got["kiro_subscription_tier"] != "ULTRA" {
		t.Fatalf("tier = %v", got["kiro_subscription_tier"])
	}
	if got["kiro_usage_used"] != int64(425) || got["kiro_usage_limit"] != int64(1000) {
		t.Fatalf("used/limit = %v/%v", got["kiro_usage_used"], got["kiro_usage_limit"])
	}
	if got["kiro_usage_used_percent"] != 42.5 {
		t.Fatalf("used_percent = %v", got["kiro_usage_used_percent"])
	}
	if got["kiro_usage_reset_at"] != "2026-08-01T00:00:00Z" {
		t.Fatalf("reset_at = %v", got["kiro_usage_reset_at"])
	}
	if got["kiro_usage_updated_at"] != "2026-07-06T12:00:00Z" {
		t.Fatalf("updated_at = %v", got["kiro_usage_updated_at"])
	}

	// nil usage → nil map
	if buildKiroUsageExtraUpdates(nil, now) != nil {
		t.Fatal("nil usage should give nil")
	}

	// tier-only(无有效额度窗口):只写 tier,不写任何 usage 键(含 updated_at 锚点)——否则会让
	// 陈旧 used_percent 的锚点保持新鲜,defeat 掉 kiroQuotaExhausted 的陈旧度自愈。
	got2 := buildKiroUsageExtraUpdates(&UsageInfo{SubscriptionTier: "FREE"}, now)
	if got2["kiro_subscription_tier"] != "FREE" {
		t.Fatalf("tier-only tier = %v", got2["kiro_subscription_tier"])
	}
	if _, ok := got2["kiro_usage_used"]; ok {
		t.Fatal("tier-only should not write kiro_usage_used")
	}
	if _, ok := got2["kiro_usage_updated_at"]; ok {
		t.Fatal("tier-only (no window) must NOT write kiro_usage_updated_at (anchor pairs with used_percent)")
	}

	// limit<=0(OVERAGE 零额度占位):不写 used_percent/updated_at(避免覆盖好快照 + 保留自愈锚点)。
	got3 := buildKiroUsageExtraUpdates(&UsageInfo{
		SubscriptionTier: "FREE",
		FiveHour:         &UsageProgress{Utilization: 0, LimitRequests: 0},
	}, now)
	if _, ok := got3["kiro_usage_used_percent"]; ok {
		t.Fatal("limit<=0 must NOT write kiro_usage_used_percent")
	}
	if _, ok := got3["kiro_usage_updated_at"]; ok {
		t.Fatal("limit<=0 must NOT write kiro_usage_updated_at")
	}
}
