//go:build unit

package service

import (
	"testing"
	"time"
)

// TestKiroQuotaExhausted 验证 Kiro 订阅窗口耗尽的主动跳过判定及其自愈:
// 100%+未到重置+快照新鲜 → 跳过;其余(<100 / 重置已过 / 快照陈旧 / 非 Kiro / 无数据)→ 放行。
func TestKiroQuotaExhausted(t *testing.T) {
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	futureReset := now.Add(24 * time.Hour).UTC().Format(time.RFC3339)
	fresh := now.Add(-time.Minute).UTC().Format(time.RFC3339)
	kiro := func(extra map[string]any) *Account { return &Account{Platform: PlatformKiro, Extra: extra} }

	if !kiroQuotaExhausted(kiro(map[string]any{
		"kiro_usage_used_percent": 100.0,
		"kiro_usage_reset_at":     futureReset,
		"kiro_usage_updated_at":   fresh,
	}), now) {
		t.Fatal("100% + future reset + fresh snapshot should be exhausted (skip)")
	}

	if kiroQuotaExhausted(kiro(map[string]any{
		"kiro_usage_used_percent": 99.9,
		"kiro_usage_updated_at":   fresh,
	}), now) {
		t.Fatal("99.9% should NOT be exhausted")
	}

	if kiroQuotaExhausted(kiro(map[string]any{
		"kiro_usage_used_percent": 100.0,
		"kiro_usage_reset_at":     now.Add(-time.Hour).UTC().Format(time.RFC3339),
		"kiro_usage_updated_at":   fresh,
	}), now) {
		t.Fatal("passed reset (window rolled over) should self-heal to schedulable")
	}

	if kiroQuotaExhausted(kiro(map[string]any{
		"kiro_usage_used_percent": 100.0,
		"kiro_usage_reset_at":     futureReset,
		"kiro_usage_updated_at":   now.Add(-3 * time.Hour).UTC().Format(time.RFC3339),
	}), now) {
		t.Fatal("stale snapshot (>2h) should NOT hard-skip (avoid permanent exclusion)")
	}

	if kiroQuotaExhausted(&Account{Platform: PlatformOpenAI, Extra: map[string]any{"kiro_usage_used_percent": 100.0}}, now) {
		t.Fatal("non-Kiro must never be exhausted by Kiro logic")
	}
	if kiroQuotaExhausted(kiro(nil), now) {
		t.Fatal("no extra should not be exhausted")
	}
	if kiroQuotaExhausted(kiro(map[string]any{"kiro_usage_updated_at": fresh}), now) {
		t.Fatal("missing used_percent should not be exhausted")
	}
}
