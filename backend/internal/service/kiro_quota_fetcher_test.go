//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

// kiroRoundTripFunc adapts a function into an http.RoundTripper.
type kiroRoundTripFunc func(*http.Request) (*http.Response, error)

func (f kiroRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// newKiroFetcherWithResponse builds a KiroQuotaFetcher whose HTTP client returns
// the supplied status/body regardless of the request URL (fake RoundTripper).
// It also captures the outgoing request for assertions.
func newKiroFetcherWithResponse(t *testing.T, status int, body string, captured **http.Request) *KiroQuotaFetcher {
	t.Helper()
	rt := kiroRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if captured != nil {
			*captured = req
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})
	clientFor := func(string) (*http.Client, error) {
		return &http.Client{Transport: rt}, nil
	}
	tp := NewKiroTokenProvider(nil, nil) // API Key path needs no repo/cache
	return NewKiroQuotaFetcher(tp, clientFor, kiro.DefaultClientConfig())
}

func kiroQuotaAPIKeyAccount() *Account {
	return &Account{
		ID:       7,
		Platform: PlatformKiro,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"kiro_api_key": "ksk_test_key",
			"profile_arn":  "arn:aws:kiro:profile/ABC",
		},
	}
}

func TestKiroQuotaFetcher_CanFetch(t *testing.T) {
	f := NewKiroQuotaFetcher(NewKiroTokenProvider(nil, nil), nil, kiro.DefaultClientConfig())

	if !f.CanFetch(kiroQuotaAPIKeyAccount()) {
		t.Fatal("apikey account with kiro_api_key should be fetchable")
	}
	oauth := &Account{Platform: PlatformKiro, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "at"}}
	if !f.CanFetch(oauth) {
		t.Fatal("oauth account with access_token should be fetchable")
	}
	if f.CanFetch(&Account{Platform: PlatformGrok, Type: AccountTypeOAuth}) {
		t.Fatal("non-kiro account must not be fetchable")
	}
	if f.CanFetch(&Account{Platform: PlatformKiro, Type: AccountTypeAPIKey}) {
		t.Fatal("kiro apikey without key must not be fetchable")
	}
	if f.CanFetch(nil) {
		t.Fatal("nil account must not be fetchable")
	}
}

func TestKiroQuotaFetcher_FetchQuota_Success(t *testing.T) {
	body := `{
		"subscriptionType": "FREE",
		"daysUntilReset": 10,
		"usageBreakdownList": [
			{"resourceType": "CREDIT", "usageLimit": 200, "currentUsage": 50}
		]
	}`
	var captured *http.Request
	f := newKiroFetcherWithResponse(t, http.StatusOK, body, &captured)

	res, err := f.FetchQuota(context.Background(), kiroQuotaAPIKeyAccount(), "")
	if err != nil {
		t.Fatalf("FetchQuota: %v", err)
	}
	if res == nil || res.UsageInfo == nil {
		t.Fatal("expected non-nil usage info")
	}
	if res.UsageInfo.SubscriptionTierRaw != "FREE" {
		t.Fatalf("subscription raw = %q", res.UsageInfo.SubscriptionTierRaw)
	}
	if res.UsageInfo.FiveHour == nil || res.UsageInfo.FiveHour.LimitRequests != 200 || res.UsageInfo.FiveHour.UsedRequests != 50 {
		t.Fatalf("five hour = %+v", res.UsageInfo.FiveHour)
	}
	if res.UsageInfo.FiveHour.Utilization != 25 {
		t.Fatalf("utilization = %v, want 25", res.UsageInfo.FiveHour.Utilization)
	}
	if res.Raw == nil {
		t.Fatal("raw response should be preserved")
	}
	// The outgoing request must target getUsageLimits with the API_KEY disguise.
	if captured == nil || !strings.HasSuffix(captured.URL.Path, "/getUsageLimits") {
		t.Fatalf("unexpected request path: %v", captured)
	}
	if captured.Header.Get("Authorization") != "Bearer ksk_test_key" {
		t.Fatalf("authorization = %q", captured.Header.Get("Authorization"))
	}
	if captured.Header.Get("tokentype") != "API_KEY" {
		t.Fatalf("tokentype = %q", captured.Header.Get("tokentype"))
	}
}

func TestKiroQuotaFetcher_FetchQuota_Forbidden(t *testing.T) {
	f := newKiroFetcherWithResponse(t, http.StatusForbidden, `{"message":"blocked"}`, nil)

	res, err := f.FetchQuota(context.Background(), kiroQuotaAPIKeyAccount(), "")
	if err != nil {
		t.Fatalf("403 should be a degraded result, not an error: %v", err)
	}
	if res == nil || res.UsageInfo == nil {
		t.Fatal("expected degraded usage info")
	}
	if !res.UsageInfo.IsForbidden || res.UsageInfo.ErrorCode != errorCodeForbidden {
		t.Fatalf("expected forbidden markers, got %+v", res.UsageInfo)
	}
}

func TestKiroQuotaFetcher_FetchQuota_Unauthorized(t *testing.T) {
	f := newKiroFetcherWithResponse(t, http.StatusUnauthorized, `{"message":"nope"}`, nil)

	res, err := f.FetchQuota(context.Background(), kiroQuotaAPIKeyAccount(), "")
	if err != nil {
		t.Fatalf("401 should be a degraded result, not an error: %v", err)
	}
	if !res.UsageInfo.NeedsReauth || res.UsageInfo.ErrorCode != errorCodeUnauthenticated {
		t.Fatalf("expected reauth markers, got %+v", res.UsageInfo)
	}
}

func TestKiroQuotaFetcher_FetchQuota_ServerError(t *testing.T) {
	f := newKiroFetcherWithResponse(t, http.StatusInternalServerError, "boom", nil)

	if _, err := f.FetchQuota(context.Background(), kiroQuotaAPIKeyAccount(), ""); err == nil {
		t.Fatal("5xx should return an error")
	}
}

// TestKiroAccountTestConnection_Success exercises the account-test branch end to end
// via an in-memory gin context. The test-connection now issues a real minimal
// generation (generateAssistantResponse), so the mock upstream returns an AWS
// EventStream frame carrying the assistant reply; the SSE stream must surface that
// reply text and report success.
func TestKiroAccountTestConnection_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := string(kiroTestFrame("assistantResponseEvent", `{"content":"OK"}`))
	f := newKiroFetcherWithResponse(t, http.StatusOK, body, nil)
	svc := &AccountTestService{kiroQuotaFetcher: f}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)

	if err := svc.testKiroAccountConnection(c, kiroQuotaAPIKeyAccount(), "claude-sonnet-4-5"); err != nil {
		t.Fatalf("testKiroAccountConnection: %v", err)
	}
	out := w.Body.String()
	if !strings.Contains(out, `"type":"test_complete"`) || !strings.Contains(out, `"success":true`) {
		t.Fatalf("expected success completion, got: %s", out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("expected upstream reply text in output, got: %s", out)
	}
}

func TestKiroAccountTestConnection_ForbiddenReportsError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newKiroFetcherWithResponse(t, http.StatusForbidden, `{"message":"blocked"}`, nil)
	svc := &AccountTestService{kiroQuotaFetcher: f}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)

	err := svc.testKiroAccountConnection(c, kiroQuotaAPIKeyAccount(), "")
	if err == nil {
		t.Fatal("forbidden account test should return an error")
	}
	if !strings.Contains(w.Body.String(), `"type":"error"`) {
		t.Fatalf("expected error event, got: %s", w.Body.String())
	}
}

func TestKiroAccountTestConnection_UnsupportedType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &AccountTestService{kiroQuotaFetcher: NewKiroQuotaFetcher(NewKiroTokenProvider(nil, nil), nil, kiro.DefaultClientConfig())}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)

	acct := &Account{Platform: PlatformKiro, Type: AccountTypeSetupToken}
	if err := svc.testKiroAccountConnection(c, acct, ""); err == nil {
		t.Fatal("unsupported kiro account type should error")
	}
}
