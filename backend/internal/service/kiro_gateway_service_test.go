package service

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

// kiroRespRoundTripper 返回预设状态码 / 响应体 / 响应头,用于验证上游头透传(如 Retry-After)。
type kiroRespRoundTripper struct {
	status int
	body   string
	header http.Header
}

func (r *kiroRespRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	h := r.header
	if h == nil {
		h = make(http.Header)
	}
	return &http.Response{
		StatusCode: r.status,
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Header:     h,
	}, nil
}

// kiroErrRoundTripper 让 client.Do 直接返回一个连接级错误。
type kiroErrRoundTripper struct{ err error }

func (r *kiroErrRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, r.err }

func newKiroServiceWithRT(rt http.RoundTripper) *KiroGatewayService {
	clientFor := func(string) (*http.Client, error) { return &http.Client{Transport: rt}, nil }
	return NewKiroGatewayService(NewKiroTokenProvider(nil, nil), clientFor, kiro.DefaultClientConfig(), kiro.NewCacheTracker(), nil)
}

// kiroTestFrame 构造一个可被 pkg/kiro 解码的 AWS Event Stream 帧(用于伪造上游响应)。
func kiroTestFrame(eventType, payload string) []byte {
	var hb []byte
	writeHeader := func(name, val string) {
		hb = append(hb, byte(len(name)))
		hb = append(hb, name...)
		hb = append(hb, 7) // string
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(val)))
		hb = append(hb, l[:]...)
		hb = append(hb, val...)
	}
	writeHeader(":message-type", "event")
	writeHeader(":event-type", eventType)
	total := 12 + len(hb) + len(payload) + 4
	buf := make([]byte, total)
	binary.BigEndian.PutUint32(buf[0:4], uint32(total))
	binary.BigEndian.PutUint32(buf[4:8], uint32(len(hb)))
	binary.BigEndian.PutUint32(buf[8:12], crc32.ChecksumIEEE(buf[0:8]))
	copy(buf[12:], hb)
	copy(buf[12+len(hb):], payload)
	binary.BigEndian.PutUint32(buf[total-4:], crc32.ChecksumIEEE(buf[0:total-4]))
	return buf
}

func newKiroGatewayServiceForTest(body string) *KiroGatewayService {
	rt := &kiroFakeRoundTripper{status: 200, body: body}
	clientFor := func(string) (*http.Client, error) { return &http.Client{Transport: rt}, nil }
	return NewKiroGatewayService(NewKiroTokenProvider(nil, nil), clientFor, kiro.DefaultClientConfig(), kiro.NewCacheTracker(), nil)
}

func kiroAPIKeyAccount() *Account {
	return &Account{Platform: PlatformKiro, Type: AccountTypeAPIKey, Credentials: map[string]any{"kiro_api_key": "ksk"}}
}

func TestKiroGatewayService_ForwardStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	frames := append(kiroTestFrame("assistantResponseEvent", `{"content":"Hi"}`),
		kiroTestFrame("contextUsageEvent", `{"contextUsagePercentage":5.0}`)...)
	svc := newKiroGatewayServiceForTest(string(frames))

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	res, err := svc.Forward(context.Background(), c, kiroAPIKeyAccount(), body, false)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "event: message_start") || !strings.Contains(out, `"text":"Hi"`) || !strings.Contains(out, "event: message_stop") {
		t.Fatalf("SSE output incomplete:\n%s", out)
	}
	if !res.Stream || res.Usage.OutputTokens <= 0 || res.Usage.InputTokens != 10000 {
		t.Fatalf("forward result = %+v", res)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
}

func TestKiroGatewayService_ForwardNonStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newKiroGatewayServiceForTest(string(kiroTestFrame("assistantResponseEvent", `{"content":"Hello"}`)))

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)

	res, err := svc.Forward(context.Background(), c, kiroAPIKeyAccount(), body, false)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	if res.Stream {
		t.Fatal("non-stream request should yield Stream=false")
	}
	var msg map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &msg); err != nil {
		t.Fatalf("non-stream body is not a JSON message: %v\n%s", err, rec.Body.String())
	}
	if msg["role"] != "assistant" {
		t.Fatalf("message envelope wrong: %+v", msg)
	}
	content, _ := msg["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("expected content blocks: %+v", msg)
	}
}

// TestKiroGatewayService_ForwardRetryableUpstreamErrorFailsOver 验证:可重试上游状态(429)
// 在首字节前返回 *UpstreamFailoverError(供 handler 跨账号 failover),不向客户端写任何字节,
// 并记录一次 ops 上游错误遥测。
func TestKiroGatewayService_ForwardRetryableUpstreamErrorFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newKiroServiceWithRT(&kiroRespRoundTripper{
		status: http.StatusTooManyRequests,
		body:   `{"message":"rate limited","reason":"MONTHLY_REQUEST_COUNT"}`,
	})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"x"}]}`)

	result, err := svc.Forward(context.Background(), c, kiroAPIKeyAccount(), body, false)
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("err = %v, want *UpstreamFailoverError", err)
	}
	if result != nil {
		t.Fatalf("failover path should not return a ForwardResult, got %+v", result)
	}
	if failoverErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("failover status = %d, want 429", failoverErr.StatusCode)
	}
	if !strings.Contains(string(failoverErr.ResponseBody), "rate limited") {
		t.Fatalf("failover ResponseBody = %q, want upstream body", failoverErr.ResponseBody)
	}
	// 首字节前:不得向客户端写出任何字节,且不留下 SSE content-type。
	if rec.Body.Len() != 0 {
		t.Fatalf("failover must not write body, got %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct == "text/event-stream" {
		t.Fatalf("SSE headers should be cleared on failover, got content-type %q", ct)
	}
	// ops 遥测:记录了一条 failover 事件,携带真实上游状态码。
	assertKiroOpsEvent(t, c, http.StatusTooManyRequests, "failover")
	v, ok := c.Get(OpsUpstreamStatusCodeKey)
	vi, _ := v.(int)
	if !ok || vi != http.StatusTooManyRequests {
		t.Fatalf("ops upstream status = %v (ok=%v), want 429", v, ok)
	}
}

// kiroRateLimiterSpy 记录 HandleUpstreamError 的调用,用于验证 429 自动暂停接线。
type kiroRateLimiterSpy struct {
	calls     int
	accountID int64
	status    int
	headers   http.Header
}

func (s *kiroRateLimiterSpy) HandleUpstreamError(_ context.Context, account *Account, statusCode int, headers http.Header, _ []byte, _ ...string) bool {
	s.calls++
	s.accountID = account.ID
	s.status = statusCode
	s.headers = headers
	return false
}

// TestKiroGatewayService_Forward429PersistsRateLimit 验证:上游 429 时,Kiro 网关调用
// RateLimitService.HandleUpstreamError 持久化账号限流冷却(与 ChatGPT 账号一致),
// 且仍返回 *UpstreamFailoverError(冷却与 in-request failover 互补,行为不变)。
func TestKiroGatewayService_Forward429PersistsRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hdr := make(http.Header)
	hdr.Set("Retry-After", "60")
	rt := &kiroRespRoundTripper{status: http.StatusTooManyRequests, body: `{"message":"rate limited"}`, header: hdr}
	clientFor := func(string) (*http.Client, error) { return &http.Client{Transport: rt}, nil }
	spy := &kiroRateLimiterSpy{}
	svc := NewKiroGatewayService(NewKiroTokenProvider(nil, nil), clientFor, kiro.DefaultClientConfig(), kiro.NewCacheTracker(), spy)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	acc := kiroAPIKeyAccount()
	acc.ID = 77
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"x"}]}`)

	_, err := svc.Forward(context.Background(), c, acc, body, false)
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("err = %v, want *UpstreamFailoverError", err)
	}
	if spy.calls != 1 {
		t.Fatalf("HandleUpstreamError calls = %d, want 1 (429 must persist rate-limit cooldown)", spy.calls)
	}
	if spy.status != http.StatusTooManyRequests {
		t.Fatalf("HandleUpstreamError status = %d, want 429", spy.status)
	}
	if spy.accountID != 77 {
		t.Fatalf("HandleUpstreamError account id = %d, want 77", spy.accountID)
	}
	// 上游响应头(含 Retry-After)必须传给 RateLimitService,供其推导冷却窗口。
	if spy.headers.Get("Retry-After") != "60" {
		t.Fatalf("HandleUpstreamError headers Retry-After = %q, want 60", spy.headers.Get("Retry-After"))
	}
}

// TestKiroGatewayService_ForwardServerErrorNoRateLimit 验证:5xx 上游只 failover,不持久化限流冷却
// (只有 429 才暂停账号)。
func TestKiroGatewayService_ForwardServerErrorNoRateLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rt := &kiroRespRoundTripper{status: http.StatusInternalServerError, body: `{"message":"boom"}`}
	clientFor := func(string) (*http.Client, error) { return &http.Client{Transport: rt}, nil }
	spy := &kiroRateLimiterSpy{}
	svc := NewKiroGatewayService(NewKiroTokenProvider(nil, nil), clientFor, kiro.DefaultClientConfig(), kiro.NewCacheTracker(), spy)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"x"}]}`)

	_, err := svc.Forward(context.Background(), c, kiroAPIKeyAccount(), body, false)
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("err = %v, want *UpstreamFailoverError for 500", err)
	}
	if spy.calls != 0 {
		t.Fatalf("HandleUpstreamError calls = %d, want 0 (5xx must not persist rate-limit)", spy.calls)
	}
}

// TestKiroGatewayService_ForwardServerErrorFailsOver 验证 5xx 上游同样触发 failover。
func TestKiroGatewayService_ForwardServerErrorFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newKiroServiceWithRT(&kiroRespRoundTripper{status: http.StatusInternalServerError, body: `{"message":"boom"}`})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":10,"messages":[{"role":"user","content":"x"}]}`)

	_, err := svc.Forward(context.Background(), c, kiroAPIKeyAccount(), body, false)
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("err = %v, want *UpstreamFailoverError for 500", err)
	}
	if failoverErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("failover status = %d, want 500", failoverErr.StatusCode)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("non-stream failover must not write body, got %q", rec.Body.String())
	}
}

// TestKiroGatewayService_ForwardTerminalClientErrorNoFailover 验证:非重试客户端错误(403)
// 保持终止行为——写出映射后的 JSON 错误,返回原始错误(非 failover),并记录 http_error 遥测。
func TestKiroGatewayService_ForwardTerminalClientErrorNoFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newKiroServiceWithRT(&kiroRespRoundTripper{
		status: http.StatusForbidden,
		body:   `{"message":"forbidden","reason":"MONTHLY_REQUEST_COUNT"}`,
	})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"x"}]}`)

	_, err := svc.Forward(context.Background(), c, kiroAPIKeyAccount(), body, false)
	if err == nil {
		t.Fatal("expected terminal upstream error")
	}
	var failoverErr *UpstreamFailoverError
	if errors.As(err, &failoverErr) {
		t.Fatalf("403 must be terminal, got failover error: %v", err)
	}
	// 真实上游状态码应被透传,且改写为 JSON(非 SSE)。
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want application/json", ct)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("error body not JSON: %s", rec.Body.String())
	}
	e, _ := out["error"].(map[string]any)
	if e["type"] != "permission_error" {
		t.Fatalf("error type = %v, want permission_error", e["type"])
	}
	assertKiroOpsEvent(t, c, http.StatusForbidden, "http_error")
}

// TestKiroGatewayService_ForwardConnectionErrorFailsOver 验证:连接级(网络)错误在首字节前
// 触发 failover,状态码为 0(无上游状态)。
func TestKiroGatewayService_ForwardConnectionErrorFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newKiroServiceWithRT(&kiroErrRoundTripper{err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"x"}]}`)

	_, err := svc.Forward(context.Background(), c, kiroAPIKeyAccount(), body, false)
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("err = %v, want *UpstreamFailoverError for connection error", err)
	}
	if failoverErr.StatusCode != 0 {
		t.Fatalf("connection-error failover status = %d, want 0", failoverErr.StatusCode)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("failover must not write body, got %q", rec.Body.String())
	}
	assertKiroOpsEvent(t, c, 0, "failover")
}

// TestKiroGatewayService_ForwardPropagatesRetryAfterHeader 验证:上游 429 的 Retry-After 头
// 通过 UpstreamError.Headers 传播到 UpstreamFailoverError.ResponseHeaders。
func TestKiroGatewayService_ForwardPropagatesRetryAfterHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hdr := make(http.Header)
	hdr.Set("Retry-After", "42")
	hdr.Set("x-amzn-RequestId", "req-123")
	svc := newKiroServiceWithRT(&kiroRespRoundTripper{status: http.StatusTooManyRequests, body: `{"message":"slow down"}`, header: hdr})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":10,"messages":[{"role":"user","content":"x"}]}`)

	_, err := svc.Forward(context.Background(), c, kiroAPIKeyAccount(), body, false)
	var failoverErr *UpstreamFailoverError
	if !errors.As(err, &failoverErr) {
		t.Fatalf("err = %v, want *UpstreamFailoverError", err)
	}
	if failoverErr.ResponseHeaders.Get("Retry-After") != "42" {
		t.Fatalf("ResponseHeaders Retry-After = %q, want 42", failoverErr.ResponseHeaders.Get("Retry-After"))
	}
	// upstream request id 应进入 ops 遥测。
	if evs := kiroOpsEvents(c); len(evs) != 1 || evs[0].UpstreamRequestID != "req-123" {
		t.Fatalf("ops upstream_request_id = %+v, want req-123", evs)
	}
}

func kiroOpsEvents(c *gin.Context) []*OpsUpstreamErrorEvent {
	v, ok := c.Get(OpsUpstreamErrorsKey)
	if !ok {
		return nil
	}
	evs, _ := v.([]*OpsUpstreamErrorEvent)
	return evs
}

func assertKiroOpsEvent(t *testing.T, c *gin.Context, wantStatus int, wantKind string) {
	t.Helper()
	evs := kiroOpsEvents(c)
	if len(evs) != 1 {
		t.Fatalf("ops upstream errors = %d, want exactly 1", len(evs))
	}
	if evs[0].UpstreamStatusCode != wantStatus {
		t.Fatalf("ops event status = %d, want %d", evs[0].UpstreamStatusCode, wantStatus)
	}
	if evs[0].Kind != wantKind {
		t.Fatalf("ops event kind = %q, want %q", evs[0].Kind, wantKind)
	}
	if evs[0].Platform != PlatformKiro {
		t.Fatalf("ops event platform = %q, want %q", evs[0].Platform, PlatformKiro)
	}
}

func TestKiroGatewayService_ForwardTokenError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := newKiroGatewayServiceForTest("")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	// API Key 账号但缺 kiro_api_key → token 获取失败
	account := &Account{Platform: PlatformKiro, Type: AccountTypeAPIKey}
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":10,"messages":[{"role":"user","content":"x"}]}`)
	if _, err := svc.Forward(context.Background(), c, account, body, false); err == nil {
		t.Fatal("expected token error when kiro_api_key missing")
	}
}
