package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

// kiroFakeRoundTripper 拦截刷新请求并返回预设响应,免去真实网络。
type kiroFakeRoundTripper struct {
	status       int
	body         string
	capturedBody []byte
}

func (f *kiroFakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		f.capturedBody, _ = io.ReadAll(req.Body)
	}
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     make(http.Header),
	}, nil
}

const kiroValidRefreshToken = "rt_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestKiroOAuthService_RefreshAccountCredentials(t *testing.T) {
	rt := &kiroFakeRoundTripper{status: 200, body: `{"accessToken":"new-access","refreshToken":"new-refresh","profileArn":"arn:new","expiresIn":3600}`}
	clientFor := func(proxyURL string) (*http.Client, error) { return &http.Client{Transport: rt}, nil }
	svc := NewKiroOAuthService(nil, clientFor, kiro.DefaultClientConfig())

	account := &Account{
		ID: 7, Platform: PlatformKiro, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "old", "refresh_token": kiroValidRefreshToken, "auth_method": "social"},
	}
	creds, err := svc.RefreshAccountCredentials(context.Background(), account)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if creds["access_token"] != "new-access" || creds["refresh_token"] != "new-refresh" || creds["profile_arn"] != "arn:new" {
		t.Fatalf("creds not updated: %+v", creds)
	}
	if s, _ := creds["expires_at"].(string); s == "" {
		t.Fatalf("expires_at not set: %+v", creds)
	}
	if !strings.Contains(string(rt.capturedBody), "refreshToken") {
		t.Fatalf("refresh request body should carry refreshToken: %s", rt.capturedBody)
	}
}

func TestKiroOAuthService_RejectsInvalidAccounts(t *testing.T) {
	svc := NewKiroOAuthService(nil, nil, kiro.DefaultClientConfig())
	if _, err := svc.RefreshAccountCredentials(context.Background(), &Account{Platform: PlatformKiro, Type: AccountTypeAPIKey}); err == nil {
		t.Fatal("api-key account should not be refreshable")
	}
	if _, err := svc.RefreshAccountCredentials(context.Background(), &Account{Platform: PlatformGrok, Type: AccountTypeOAuth}); err == nil {
		t.Fatal("non-kiro account should error")
	}
}

// kiroFakeOAuthSvc 是 KiroOAuthTokenService 的测试替身。
type kiroFakeOAuthSvc struct {
	creds map[string]any
	err   error
}

func (f *kiroFakeOAuthSvc) RefreshAccountCredentials(ctx context.Context, account *Account) (map[string]any, error) {
	return f.creds, f.err
}

// TestTokenRefreshService_SetKiroRefresher 验证 #4:注册后 Kiro OAuth 账号被后台刷新服务纳管
// (追加进 refreshers + executors),注册前不纳管,nil 是 no-op。
func TestTokenRefreshService_SetKiroRefresher(t *testing.T) {
	svc := NewTokenRefreshService(nil, nil, nil, nil, nil, nil, nil, &config.Config{}, nil)
	kiroAcc := &Account{ID: 1, Platform: PlatformKiro, Type: AccountTypeOAuth}

	before := len(svc.refreshers)
	execBefore := len(svc.executors)
	for _, r := range svc.refreshers {
		if r.CanRefresh(kiroAcc) {
			t.Fatal("Kiro should not be refreshable before registration")
		}
	}

	svc.SetKiroRefresher(NewKiroTokenRefresher(&kiroFakeOAuthSvc{}))
	if len(svc.refreshers) != before+1 || len(svc.executors) != execBefore+1 {
		t.Fatalf("SetKiroRefresher should append to both lists: refreshers %d->%d, executors %d->%d",
			before, len(svc.refreshers), execBefore, len(svc.executors))
	}

	found := false
	for _, r := range svc.refreshers {
		if r.CanRefresh(kiroAcc) {
			found = true
		}
	}
	if !found {
		t.Fatal("Kiro OAuth account should be refreshable after SetKiroRefresher")
	}

	svc.SetKiroRefresher(nil) // no-op
	if len(svc.refreshers) != before+1 {
		t.Fatalf("nil refresher should be a no-op, got %d", len(svc.refreshers))
	}
}

func TestKiroTokenRefresher_CanRefresh(t *testing.T) {
	r := NewKiroTokenRefresher(&kiroFakeOAuthSvc{})
	if !r.CanRefresh(&Account{Platform: PlatformKiro, Type: AccountTypeOAuth}) {
		t.Fatal("should refresh kiro oauth")
	}
	if r.CanRefresh(&Account{Platform: PlatformKiro, Type: AccountTypeAPIKey}) {
		t.Fatal("should not refresh kiro api-key")
	}
	if r.CanRefresh(&Account{Platform: PlatformGrok, Type: AccountTypeOAuth}) {
		t.Fatal("should not refresh grok")
	}
}

func TestKiroTokenRefresher_NeedsRefresh(t *testing.T) {
	r := NewKiroTokenRefresher(&kiroFakeOAuthSvc{})
	// 无 refresh token → 不刷新
	if r.NeedsRefresh(&Account{Platform: PlatformKiro, Type: AccountTypeOAuth}, time.Hour) {
		t.Fatal("no refresh token -> should not refresh")
	}
	base := map[string]any{"refresh_token": kiroValidRefreshToken}
	// 有 refresh token 但缺 expires_at → 需要刷新
	if !r.NeedsRefresh(&Account{Platform: PlatformKiro, Type: AccountTypeOAuth, Credentials: base}, time.Hour) {
		t.Fatal("missing expires_at -> should refresh")
	}
	// 远期过期 → 不需要刷新
	future := map[string]any{"refresh_token": kiroValidRefreshToken, "expires_at": time.Now().Add(24 * time.Hour).Format(time.RFC3339)}
	if r.NeedsRefresh(&Account{Platform: PlatformKiro, Type: AccountTypeOAuth, Credentials: future}, time.Hour) {
		t.Fatal("far-future expiry -> should not refresh")
	}
}

func TestKiroTokenRefresher_RefreshMergesCredentials(t *testing.T) {
	r := NewKiroTokenRefresher(&kiroFakeOAuthSvc{creds: map[string]any{"access_token": "new"}})
	acc := &Account{Platform: PlatformKiro, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "old", "custom": "keep"}}
	merged, err := r.Refresh(context.Background(), acc)
	if err != nil {
		t.Fatal(err)
	}
	if merged["access_token"] != "new" {
		t.Fatalf("access_token should be updated: %+v", merged)
	}
	if merged["custom"] != "keep" {
		t.Fatalf("existing field should be preserved: %+v", merged)
	}
}

func TestKiroTokenProvider_APIKeyDirect(t *testing.T) {
	p := NewKiroTokenProvider(nil, nil)
	acc := &Account{Platform: PlatformKiro, Type: AccountTypeAPIKey, Credentials: map[string]any{"kiro_api_key": "ksk_123"}}
	tok, err := p.GetAccessToken(context.Background(), acc)
	if err != nil || tok != "ksk_123" {
		t.Fatalf("api-key direct: got (%q, %v)", tok, err)
	}
	if _, err := p.GetAccessToken(context.Background(), &Account{Platform: PlatformKiro, Type: AccountTypeAPIKey}); err == nil {
		t.Fatal("missing kiro_api_key should error")
	}
}

func TestKiroTokenProvider_RejectsNonKiro(t *testing.T) {
	p := NewKiroTokenProvider(nil, nil)
	if _, err := p.GetAccessToken(context.Background(), &Account{Platform: PlatformGrok}); err == nil {
		t.Fatal("non-kiro account should error")
	}
	if _, err := p.GetAccessToken(context.Background(), nil); err == nil {
		t.Fatal("nil account should error")
	}
}

func TestKiroTokenCacheKey(t *testing.T) {
	if got := KiroTokenCacheKey(&Account{ID: 42}); got != "kiro:account:42" {
		t.Fatalf("cache key = %q", got)
	}
	if got := KiroTokenCacheKey(nil); got != "kiro:account:0" {
		t.Fatalf("nil cache key = %q", got)
	}
}
