//go:build unit

package service

import (
	"context"
	"testing"
)

// TestUpstreamWindowUsageCrossed 验证低额度告警的跨阈值边沿判定(即天然去重)。
func TestUpstreamWindowUsageCrossed(t *testing.T) {
	const th = kiroUpstreamQuotaAlertThreshold // 90
	cases := []struct {
		name         string
		old, now     float64
		wantCrossing bool
	}{
		{"cross up 85->92", 85, 92, true},
		{"first sample already at threshold 0->90", 0, 90, true},
		{"first sample already high 0->99", 0, 99, true},
		{"already above 92->95 (no re-fire)", 92, 95, false},
		{"exactly at threshold old 90->91", 90, 91, false},
		{"not reached 85->88", 85, 88, false},
		{"dropped 95->80", 95, 80, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := upstreamWindowUsageCrossed(c.old, c.now, th); got != c.wantCrossing {
				t.Fatalf("upstreamWindowUsageCrossed(%v,%v,%v)=%v, want %v", c.old, c.now, th, got, c.wantCrossing)
			}
		})
	}
}

// TestCheckUpstreamWindowUsage_NilGuards 验证 nil 依赖/非跨越时安全早返回(不 panic)。
func TestCheckUpstreamWindowUsage_NilGuards(t *testing.T) {
	s := &BalanceNotifyService{} // emailService/settingRepo 为 nil
	// 不 panic 即通过(早返回)。
	s.CheckUpstreamWindowUsage(context.Background(), nil, 10, 95)
	s.CheckUpstreamWindowUsage(context.Background(), &Account{ID: 1, Platform: PlatformKiro}, 10, 95)
}
