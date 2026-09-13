package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- 脱敏 ---

func TestCodexFingerprintDigest_IrreversibleAndStable(t *testing.T) {
	const raw = "11111111-2222-4333-8444-555555555555"

	got := codexFingerprintDigest(raw)
	assert.Len(t, got, 16, "摘要应为 sha256 前 8 字节的 hex")
	assert.NotContains(t, got, raw, "摘要不得包含明文")
	assert.Equal(t, got, codexFingerprintDigest(raw), "同一输入必须稳定")
	assert.NotEqual(t, got, codexFingerprintDigest("another-id"), "不同输入必须不同")
	assert.Empty(t, codexFingerprintDigest(""), "空值返回空")
	assert.Empty(t, codexFingerprintDigest("   "), "纯空白返回空")
}

// --- 基数指标键与时间桶 ---

func TestCodexFingerprintHLLKey_Format(t *testing.T) {
	assert.Equal(t, "codex_fp:sess:42:2026091314", codexFingerprintHLLKey("sess", 42, "2026091314"))
	assert.Equal(t, "codex_fp:inst:42:2026091314", codexFingerprintHLLKey("inst", 42, "2026091314"))
}

func TestCodexFingerprintHourBucket_UsesUTC(t *testing.T) {
	utc := time.Date(2026, 9, 13, 14, 30, 0, 0, time.UTC)
	assert.Equal(t, "2026091314", codexFingerprintHourBucket(utc))

	// 同一时刻换到 +08 时区，桶必须不变——否则跨时区部署会分到不同桶里。
	shanghai := utc.In(time.FixedZone("CST", 8*3600))
	assert.Equal(t, "2026091314", codexFingerprintHourBucket(shanghai),
		"时间桶必须按 UTC 计算，不受部署时区影响")
}

// --- 采样 ---

func TestShouldLogCodexFingerprint_RateBoundaries(t *testing.T) {
	digest := codexFingerprintDigest("11111111-2222-4333-8444-555555555555")
	assert.False(t, shouldLogCodexFingerprint(digest, 0), "0% 永不采样")
	assert.True(t, shouldLogCodexFingerprint(digest, 100), "100% 全采样")
	assert.False(t, shouldLogCodexFingerprint("", 50), "空摘要不采样")
	assert.False(t, shouldLogCodexFingerprint(digest, -1), "负数按关闭处理")
}

// --- 观测契约：绝不 panic、绝不阻塞请求路径 ---

func TestRecordCodexFingerprintObservation_NoObserverIsNoop(t *testing.T) {
	SetCodexFingerprintObserver(nil)
	t.Cleanup(func() { SetCodexFingerprintObserver(nil) })

	require.NotPanics(t, func() {
		recordCodexFingerprintObservation(context.Background(), codexFingerprintObservation{AccountID: 1})
	}, "未注册消费者时必须是 no-op")
}

func TestRecordCodexFingerprintObservation_SwallowsConsumerPanic(t *testing.T) {
	SetCodexFingerprintObserver(func(context.Context, codexFingerprintObservation) {
		panic("consumer bug")
	})
	t.Cleanup(func() { SetCodexFingerprintObserver(nil) })

	require.NotPanics(t, func() {
		recordCodexFingerprintObservation(context.Background(), codexFingerprintObservation{AccountID: 1})
	}, "消费者 panic 绝不允许冒泡到请求路径")
}

func TestCodexFingerprintTelemetry_ObserveNeverBlocksWhenQueueFull(t *testing.T) {
	// 不调用 Start（不起 worker）：队列会被填满，投递必须立刻返回而非阻塞。
	telemetry := NewCodexFingerprintTelemetry(nil, 0)

	done := make(chan struct{})
	go func() {
		for i := 0; i < codexFingerprintObservationQueue*3; i++ {
			telemetry.observe(context.Background(), codexFingerprintObservation{AccountID: int64(i)})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("队列打满后 observe 阻塞了请求路径——这违反观测契约")
	}
	assert.Greater(t, telemetry.Dropped(), uint64(0), "溢出必须被计数以便暴露健康度")
}

func TestCodexFingerprintTelemetry_StartWithoutRedisIsNoop(t *testing.T) {
	SetCodexFingerprintObserver(nil)
	t.Cleanup(func() { SetCodexFingerprintObserver(nil) })

	telemetry := NewCodexFingerprintTelemetry(nil, 1)
	require.NotPanics(t, telemetry.Start)

	codexFingerprintObserverMu.RLock()
	registered := codexFingerprintObserver
	codexFingerprintObserverMu.RUnlock()
	assert.Nil(t, registered, "redis 为 nil 时不得注册消费者，否则会做无意义的排队")
}

func TestCodexFingerprintTelemetry_StopIsIdempotent(t *testing.T) {
	telemetry := NewCodexFingerprintTelemetry(nil, 0)
	telemetry.Stop()
	require.NotPanics(t, telemetry.Stop, "Stop 必须可重复调用")
}

// --- 埋点接入：off 与收敛两种模式都要被观测到 ---

func TestResolveCodexFingerprintIDs_RecordsObservationForOffAndConverged(t *testing.T) {
	offAccount := newTestOAuthAccount(7030, nil)
	convergedAccount := newTestOAuthAccount(7031, map[string]any{codexFingerprintModeExtraKey: "session"})

	observed := make(chan codexFingerprintObservation, 4)
	SetCodexFingerprintObserver(func(_ context.Context, obs codexFingerprintObservation) {
		observed <- obs
	})
	t.Cleanup(func() { SetCodexFingerprintObserver(nil) })

	headers := http.Header{}
	headers.Set("session-id", "client-session")
	headers.Set("thread-id", "client-thread")
	headers.Set("x-codex-installation-id", "client-install")
	headers.Set("originator", "codex_cli_rs")
	headers.Set("user-agent", "codex_cli_rs/0.154.0 (X; Y) zsh")

	require.Nil(t, resolveCodexFingerprintIDsFromRequest(offAccount, headers))
	offObs := <-observed
	assert.Equal(t, "off", offObs.Mode)
	assert.Equal(t, int64(7030), offObs.AccountID)
	assert.Equal(t, "client-install", offObs.InstallationID, "off 模式必须记录客户端原值")
	assert.Equal(t, "client-session", offObs.SessionID, "off 模式的 sess 桶必须记录 session-id，不能混入 thread-id")
	assert.Equal(t, "codex_cli_rs", offObs.Originator)

	require.NotNil(t, resolveCodexFingerprintIDsFromRequest(convergedAccount, headers))
	convergedObs := <-observed
	assert.Equal(t, "session", convergedObs.Mode)
	assert.Equal(t, int64(7031), convergedObs.AccountID)
	assert.NotEqual(t, "client-install", convergedObs.InstallationID, "收敛模式必须记录派生值")
	assert.NotEmpty(t, convergedObs.InstallationID)
	assert.NotEmpty(t, convergedObs.SessionID)
}
