//go:build unit

package service

import (
	"testing"
	"time"
)

// TestKiroFailureCircuitBreaker_ConsecutiveConnTrips 连续 N 次连接级失败 → trip,冷却 ~30min。
func TestKiroFailureCircuitBreaker_ConsecutiveConnTrips(t *testing.T) {
	b := newKiroFailureCircuitBreaker()
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	for i := 0; i < kiroCircuitConsecutiveN-1; i++ {
		if _, _, tripped := b.recordFailure(1, true, now.Add(time.Duration(i)*time.Second)); tripped {
			t.Fatalf("tripped too early at consecutive %d", i+1)
		}
	}
	until, reason, tripped := b.recordFailure(1, true, now.Add(3*time.Second))
	if !tripped {
		t.Fatal("should trip on Nth consecutive connection failure")
	}
	if reason == "" {
		t.Fatal("trip reason should be non-empty")
	}
	if d := until.Sub(now); d < 29*time.Minute || d > 31*time.Minute {
		t.Fatalf("cooldown = %v, want ~30min", d)
	}
}

// TestKiroFailureCircuitBreaker_NonConnResetsConsecutive 非连接级(5xx)失败清零连续计数。
func TestKiroFailureCircuitBreaker_NonConnResetsConsecutive(t *testing.T) {
	b := newKiroFailureCircuitBreaker()
	now := time.Now()
	b.recordFailure(1, true, now)  // consecutive=1
	b.recordFailure(1, true, now)  // consecutive=2
	b.recordFailure(1, false, now) // 5xx → consecutive=0
	if _, _, tripped := b.recordFailure(1, true, now); tripped {
		t.Fatal("consecutive should have reset after a non-connection failure")
	}
}

// TestKiroFailureCircuitBreaker_WindowThresholdTrips 窗口内 M 次任意瞬时失败 → trip
//（交替 conn/非 conn 使连续计数不达标,靠窗口阈值触发)。
func TestKiroFailureCircuitBreaker_WindowThresholdTrips(t *testing.T) {
	b := newKiroFailureCircuitBreaker()
	now := time.Now()
	var tripped bool
	for i, k := range []bool{true, false, true, false, true} {
		_, _, tripped = b.recordFailure(2, k, now.Add(time.Duration(i)*time.Second))
	}
	if !tripped {
		t.Fatalf("should trip on %d failures within the window", kiroCircuitWindowThreshold)
	}
}

// TestKiroFailureCircuitBreaker_WindowExpiryAvoidsTrip 超出窗口的旧失败不再累计。
func TestKiroFailureCircuitBreaker_WindowExpiryAvoidsTrip(t *testing.T) {
	b := newKiroFailureCircuitBreaker()
	base := time.Now()
	b.recordFailure(3, false, base)
	b.recordFailure(3, false, base.Add(time.Minute))
	b.recordFailure(3, false, base.Add(2*time.Minute))
	b.recordFailure(3, false, base.Add(3*time.Minute))
	// 第 5 次远在窗口之外(base+10min,cutoff=base+5min)→ 旧的全部过期,窗口计数仅 1。
	if _, _, tripped := b.recordFailure(3, false, base.Add(10*time.Minute)); tripped {
		t.Fatal("expired-window failures should not accumulate to a trip")
	}
}

// TestKiroFailureCircuitBreaker_Reset 成功一次清零计数。
func TestKiroFailureCircuitBreaker_Reset(t *testing.T) {
	b := newKiroFailureCircuitBreaker()
	now := time.Now()
	b.recordFailure(1, true, now)
	b.recordFailure(1, true, now)
	b.reset(1)
	if _, _, tripped := b.recordFailure(1, true, now); tripped {
		t.Fatal("reset should clear the consecutive count")
	}
}
