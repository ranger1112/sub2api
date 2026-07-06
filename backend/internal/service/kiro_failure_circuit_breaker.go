package service

import (
	"fmt"
	"sync"
	"time"
)

// Kiro 重复失败熔断器(进程内,按 account.ID 隔离,忠实移植 OpenAI 的
// openai_responses_failure_circuit_breaker)。背景:Kiro 的 5xx / 连接级失败此前只做请求内
// failover,一个「持续 5xx/断连」的坏号永远不会被下线,每次请求都强制换号。熔断器在坏号累计
// 到阈值后临时下线它(SetTempUnschedulable),成功一次即清零。
//
// 只统计「瞬时」失败(5xx / 连接级);账号级状态(401/402/403/429/529)已进 RateLimitService
// 健康态机器,不重复统计。纯计数,重启清空。
const (
	kiroCircuitWindow          = 5 * time.Minute  // 滑动窗口
	kiroCircuitCooldown        = 30 * time.Minute // trip 后临时下线时长
	kiroCircuitConsecutiveN    = 3                // 连续 N 次连接级失败 → trip
	kiroCircuitWindowThreshold = 5                // 窗口内 M 次任意瞬时失败 → trip
)

type kiroFailureState struct {
	consecutive int         // 连续连接级失败计数(非连接级失败会清零)
	failures    []time.Time // 滑动窗口内的失败时刻
}

type kiroFailureCircuitBreaker struct {
	mu     sync.Mutex
	states map[int64]*kiroFailureState
}

func newKiroFailureCircuitBreaker() *kiroFailureCircuitBreaker {
	return &kiroFailureCircuitBreaker{states: map[int64]*kiroFailureState{}}
}

// recordFailure 记录一次瞬时失败。connLevel 表示连接级(计入「连续」阈值;非连接级会清零连续计数
// 但仍计入窗口)。达到「连续 N」或「窗口内 M」任一阈值即 trip:返回临时下线截止时间、原因、true,
// 并清空该账号状态(避免下线期间重复 trip)。调用方负责实际的 SetTempUnschedulable 写入。
func (b *kiroFailureCircuitBreaker) recordFailure(accountID int64, connLevel bool, now time.Time) (time.Time, string, bool) {
	if b == nil || accountID <= 0 {
		return time.Time{}, "", false
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	st := b.states[accountID]
	if st == nil {
		st = &kiroFailureState{}
		b.states[accountID] = st
	}
	if connLevel {
		st.consecutive++
	} else {
		st.consecutive = 0
	}

	// 滑动窗口:就地过滤掉过期时刻,再追加本次。
	cutoff := now.Add(-kiroCircuitWindow)
	kept := st.failures[:0]
	for _, t := range st.failures {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	st.failures = append(kept, now)

	if st.consecutive >= kiroCircuitConsecutiveN || len(st.failures) >= kiroCircuitWindowThreshold {
		reason := fmt.Sprintf("kiro circuit breaker tripped: consecutive_conn=%d, failures_in_%s=%d",
			st.consecutive, kiroCircuitWindow, len(st.failures))
		delete(b.states, accountID)
		return now.Add(kiroCircuitCooldown), reason, true
	}
	return time.Time{}, "", false
}

// reset 清空某账号的失败计数(上游成功一次即调用)。
func (b *kiroFailureCircuitBreaker) reset(accountID int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.states, accountID)
}
