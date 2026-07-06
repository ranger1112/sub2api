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

	// tier-only(无窗口):写 tier + updated_at,不写 usage 键。
	got2 := buildKiroUsageExtraUpdates(&UsageInfo{SubscriptionTier: "FREE"}, now)
	if got2["kiro_subscription_tier"] != "FREE" {
		t.Fatalf("tier-only tier = %v", got2["kiro_subscription_tier"])
	}
	if _, ok := got2["kiro_usage_used"]; ok {
		t.Fatal("tier-only should not write kiro_usage_used")
	}
	if got2["kiro_usage_updated_at"] == nil {
		t.Fatal("kiro_usage_updated_at must always be written")
	}
}
