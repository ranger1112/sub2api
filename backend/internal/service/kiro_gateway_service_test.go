package service

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

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
	return NewKiroGatewayService(NewKiroTokenProvider(nil, nil), clientFor, kiro.DefaultClientConfig())
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
