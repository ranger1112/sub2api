//go:build unit

package service

import (
	"context"
	"testing"
)

// TestClaimUpstreamWindowAlert 验证低额度告警的进程内每窗口去重:同窗口只占位一次,
// 窗口(reset 时间)变化则重新武装。
func TestClaimUpstreamWindowAlert(t *testing.T) {
	s := &BalanceNotifyService{}
	const id int64 = 42

	if !s.claimUpstreamWindowAlert(id, "w1") {
		t.Fatal("first claim for window w1 should succeed")
	}
	if s.claimUpstreamWindowAlert(id, "w1") {
		t.Fatal("second claim for same window w1 must be deduped (false)")
	}
	// 窗口滚动(reset 变化)→ 重新武装。
	if !s.claimUpstreamWindowAlert(id, "w2") {
		t.Fatal("claim for a new window w2 should succeed (re-armed)")
	}
	if s.claimUpstreamWindowAlert(id, "w2") {
		t.Fatal("second claim for w2 must be deduped")
	}
	// 另一个账号互不影响。
	if !s.claimUpstreamWindowAlert(99, "w1") {
		t.Fatal("different account should claim independently")
	}
	// 空 reset(无窗口信息):按空串去重,首次成功、再次跳过(每进程一次)。
	if !s.claimUpstreamWindowAlert(7, "") {
		t.Fatal("empty-reset first claim should succeed")
	}
	if s.claimUpstreamWindowAlert(7, "") {
		t.Fatal("empty-reset second claim must be deduped")
	}
}

// TestCheckUpstreamWindowUsage_SafeGuards 验证 nil 依赖 / 低于阈值时安全早返回(不 panic、不占位)。
func TestCheckUpstreamWindowUsage_SafeGuards(t *testing.T) {
	s := &BalanceNotifyService{} // emailService/settingRepo 为 nil → 恒早返回
	s.CheckUpstreamWindowUsage(context.Background(), nil, 95, "w1")
	s.CheckUpstreamWindowUsage(context.Background(), &Account{ID: 1, Platform: PlatformKiro}, 95, "w1")
	s.CheckUpstreamWindowUsage(context.Background(), &Account{ID: 1, Platform: PlatformKiro}, 50, "w1")
	if _, ok := s.upstreamWindowAlerted.Load(int64(1)); ok {
		t.Fatal("must not mark alerted when email deps are nil (nothing sent)")
	}
}
