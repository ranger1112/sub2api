//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// kiro429Repo 记录 SetRateLimited 的入参(id + resetAt),验证 Kiro 429 冷却写入。
type kiro429Repo struct {
	mockAccountRepoForGemini
	calls         int
	rateLimitedID int64
	resetAt       time.Time
}

func (r *kiro429Repo) SetRateLimited(_ context.Context, id int64, resetAt time.Time) error {
	r.calls++
	r.rateLimitedID = id
	r.resetAt = resetAt
	return nil
}

// TestHandle429_KiroHonorsRetryAfter 验证:Kiro 账号 429 带 Retry-After 时,按其精确暂停
// (写 rate_limit_reset_at ≈ now + Retry-After),使调度器在该窗口内跳过该账号。
func TestHandle429_KiroHonorsRetryAfter(t *testing.T) {
	repo := &kiro429Repo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 55, Platform: PlatformKiro, Type: AccountTypeOAuth}

	headers := http.Header{}
	headers.Set("Retry-After", "60")

	before := time.Now()
	svc.handle429(context.Background(), account, headers, nil)

	if repo.calls != 1 || repo.rateLimitedID != 55 {
		t.Fatalf("SetRateLimited calls=%d id=%d, want 1 / 55", repo.calls, repo.rateLimitedID)
	}
	if d := repo.resetAt.Sub(before); d < 55*time.Second || d > 65*time.Second {
		t.Fatalf("resetAt delta = %v, want ~60s (honor Retry-After)", d)
	}
}

// TestHandle429_KiroFallbackCooldownWhenNoRetryAfter 验证:Kiro 账号 429 无 Retry-After 时,
// 落到可配置的默认冷却(仍写 rate_limit_reset_at,把账号暂停一段时间)。
func TestHandle429_KiroFallbackCooldownWhenNoRetryAfter(t *testing.T) {
	repo := &kiro429Repo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 56, Platform: PlatformKiro, Type: AccountTypeOAuth}

	before := time.Now()
	svc.handle429(context.Background(), account, http.Header{}, nil)

	if repo.calls != 1 || repo.rateLimitedID != 56 {
		t.Fatalf("SetRateLimited calls=%d id=%d, want 1 / 56 (fallback cooldown)", repo.calls, repo.rateLimitedID)
	}
	if !repo.resetAt.After(before) {
		t.Fatalf("resetAt %v not after %v (expected default cooldown window)", repo.resetAt, before)
	}
}

// TestHandle429_KiroRetryAfterZeroFallsBack 验证:Retry-After:0(立即重试,非未来时间)不写
// no-op 冷却,而是落到默认冷却窗口(未来时间),避免账号被"暂停到当前时刻"= 不暂停。
func TestHandle429_KiroRetryAfterZeroFallsBack(t *testing.T) {
	repo := &kiro429Repo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 57, Platform: PlatformKiro, Type: AccountTypeOAuth}

	headers := http.Header{}
	headers.Set("Retry-After", "0")

	before := time.Now()
	svc.handle429(context.Background(), account, headers, nil)

	if repo.calls != 1 || repo.rateLimitedID != 57 {
		t.Fatalf("SetRateLimited calls=%d id=%d, want 1 / 57", repo.calls, repo.rateLimitedID)
	}
	// 必须是未来的默认冷却,而非 now(no-op)。
	if !repo.resetAt.After(before.Add(time.Second)) {
		t.Fatalf("resetAt %v should be a future fallback cooldown, not now", repo.resetAt)
	}
}
