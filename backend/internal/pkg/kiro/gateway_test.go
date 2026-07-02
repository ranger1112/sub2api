package kiro

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro/eventstream"
)

// encodeKiroFrame 构造一个可被 eventstream.Decoder 解析的 AWS Event Stream 帧,
// 供伪造 Kiro 上游流使用(header 均为 string 类型)。
func encodeKiroFrame(messageType, eventType, payload string) []byte {
	var hb []byte
	writeHeader := func(name, val string) {
		hb = append(hb, byte(len(name)))
		hb = append(hb, name...)
		hb = append(hb, 7) // HeaderString
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(val)))
		hb = append(hb, l[:]...)
		hb = append(hb, val...)
	}
	writeHeader(":message-type", messageType)
	if eventType != "" {
		writeHeader(":event-type", eventType)
	}
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

func kiroEventFrame(eventType, payload string) []byte {
	return encodeKiroFrame("event", eventType, payload)
}

func TestStreamMessages_EndToEnd(t *testing.T) {
	withFixedUUID(t)

	var gotAuth, gotOptOut string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotOptOut = r.Header.Get("x-amzn-codewhisperer-optout")
		gotBody, _ = readAllBody(r)
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		// 一段典型的 Kiro 事件流:两段文本 + 上下文使用率
		w.Write(kiroEventFrame("assistantResponseEvent", `{"content":"Hello "}`))
		w.Write(kiroEventFrame("assistantResponseEvent", `{"content":"world"}`))
		w.Write(kiroEventFrame("contextUsageEvent", `{"contextUsagePercentage":5.0}`)) // 5% of 200000 = 10000
	}))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.apiURLOverride = srv.URL
	cred := &Credentials{AccessToken: "tok-abc", ProfileArn: "arn:profile/Z", RefreshToken: "rt"}

	req := &AnthropicRequest{}
	if err := json.Unmarshal([]byte(`{
		"model": "claude-sonnet-4-5",
		"max_tokens": 100,
		"messages": [{"role":"user","content":"hi there"}]
	}`), req); err != nil {
		t.Fatal(err)
	}

	var sink strings.Builder
	res, err := StreamMessages(context.Background(), srv.Client(), cred, cfg, req, &sink)
	if err != nil {
		t.Fatalf("StreamMessages: %v", err)
	}

	out := sink.String()
	// SSE 生命周期
	for _, want := range []string{
		"event: message_start", "event: content_block_start",
		"event: content_block_delta", "event: message_delta", "event: message_stop",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("SSE output missing %q\n---\n%s", want, out)
		}
	}
	// 文本增量拼接
	if !strings.Contains(out, `"text":"Hello "`) || !strings.Contains(out, `"text":"world"`) {
		t.Fatalf("expected streamed text deltas, got:\n%s", out)
	}
	// 上游请求正确:Authorization + optout 头 + 请求体注入了 profileArn + conversationState
	if gotAuth != "Bearer tok-abc" {
		t.Fatalf("upstream Authorization = %q", gotAuth)
	}
	if gotOptOut != "true" {
		t.Fatalf("optout header = %q", gotOptOut)
	}
	if !strings.Contains(string(gotBody), `"profileArn":"arn:profile/Z"`) {
		t.Fatalf("upstream body missing injected profileArn: %s", gotBody)
	}
	if !strings.Contains(string(gotBody), `"conversationState"`) {
		t.Fatalf("upstream body missing conversationState: %s", gotBody)
	}
	// 结果:input_tokens 采用 contextUsageEvent 计算值(5% * 200000 = 10000)
	if res.Model != ModelSonnet45 {
		t.Fatalf("result model = %q", res.Model)
	}
	if res.InputTokens != 10000 {
		t.Fatalf("result InputTokens = %d, want 10000 (from contextUsageEvent)", res.InputTokens)
	}
	if res.OutputTokens <= 0 {
		t.Fatalf("result OutputTokens = %d, want > 0", res.OutputTokens)
	}
}

func TestCollectMessages_EndToEnd(t *testing.T) {
	withFixedUUID(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(kiroEventFrame("assistantResponseEvent", `{"content":"Hello "}`))
		w.Write(kiroEventFrame("assistantResponseEvent", `{"content":"world"}`))
		w.Write(kiroEventFrame("toolUseEvent", `{"name":"read_file","toolUseId":"tu_1","input":"{\"path\":\"/x\"}","stop":true}`))
		w.Write(kiroEventFrame("contextUsageEvent", `{"contextUsagePercentage":5.0}`))
	}))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.apiURLOverride = srv.URL
	cred := &Credentials{AccessToken: "tok", RefreshToken: "rt"}
	req := &AnthropicRequest{}
	_ = json.Unmarshal([]byte(`{"model":"claude-sonnet-4-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`), req)

	msg, res, err := CollectMessages(context.Background(), srv.Client(), cred, cfg, req)
	if err != nil {
		t.Fatalf("CollectMessages: %v", err)
	}
	if msg["role"] != "assistant" || msg["type"] != "message" {
		t.Fatalf("message envelope wrong: %+v", msg)
	}
	content, _ := msg["content"].([]any)
	if len(content) < 2 {
		t.Fatalf("expected text + tool_use blocks, got %d: %+v", len(content), content)
	}
	// 第一块:合并后的文本
	first, _ := content[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "Hello world" {
		t.Fatalf("text block wrong: %+v", first)
	}
	// 存在 tool_use 块,且 input 已解析为对象
	var sawTool bool
	for _, b := range content {
		bm, _ := b.(map[string]any)
		if bm["type"] == "tool_use" {
			sawTool = true
			if bm["name"] != "read_file" {
				t.Fatalf("tool name wrong: %+v", bm)
			}
			input, _ := bm["input"].(map[string]any)
			if input["path"] != "/x" {
				t.Fatalf("tool input not parsed: %+v", bm["input"])
			}
		}
	}
	if !sawTool {
		t.Fatalf("missing tool_use block: %+v", content)
	}
	// usage:input 采用 contextUsageEvent 值,output > 0
	usage, _ := msg["usage"].(map[string]any)
	if eventIndex(usage["input_tokens"]) != 10000 {
		t.Fatalf("usage input_tokens = %v, want 10000", usage["input_tokens"])
	}
	if eventIndex(usage["output_tokens"]) <= 0 {
		t.Fatalf("usage output_tokens = %v, want > 0", usage["output_tokens"])
	}
	if res.Model != ModelSonnet45 {
		t.Fatalf("result model = %q", res.Model)
	}
	// stop_reason 应为 tool_use(有工具调用)
	if msg["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %v, want tool_use", msg["stop_reason"])
	}
}

func TestStreamMessages_UpstreamError(t *testing.T) {
	withFixedUUID(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"reason":"MONTHLY_REQUEST_COUNT"}`))
	}))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.apiURLOverride = srv.URL
	cred := &Credentials{AccessToken: "tok", RefreshToken: "rt"}
	req := &AnthropicRequest{}
	_ = json.Unmarshal([]byte(`{"model":"claude-sonnet-4-5","max_tokens":10,"messages":[{"role":"user","content":"x"}]}`), req)

	var sink strings.Builder
	_, err := StreamMessages(context.Background(), srv.Client(), cred, cfg, req, &sink)
	ue, ok := err.(*UpstreamError)
	if !ok {
		t.Fatalf("err = %v, want *UpstreamError", err)
	}
	if ue.StatusCode != http.StatusForbidden || !strings.Contains(ue.Body, "MONTHLY_REQUEST_COUNT") {
		t.Fatalf("UpstreamError = %+v", ue)
	}
}

func TestStreamMessages_TruncatedStreamErrors(t *testing.T) {
	withFixedUUID(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 一个完整帧 + 一个残缺帧(仅前 8 字节,不足 prelude),然后关闭 → 截断
		w.Write(kiroEventFrame("assistantResponseEvent", `{"content":"partial"}`))
		full := kiroEventFrame("assistantResponseEvent", `{"content":"never arrives"}`)
		w.Write(full[:8])
	}))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.apiURLOverride = srv.URL
	cred := &Credentials{AccessToken: "t", RefreshToken: "rt"}
	req := &AnthropicRequest{}
	_ = json.Unmarshal([]byte(`{"model":"claude-sonnet-4-5","max_tokens":10,"messages":[{"role":"user","content":"x"}]}`), req)

	var sink strings.Builder
	res, err := StreamMessages(context.Background(), srv.Client(), cred, cfg, req, &sink)
	var incomplete *eventstream.IncompleteFrameError
	if !errors.As(err, &incomplete) {
		t.Fatalf("expected IncompleteFrameError (truncation), got %v", err)
	}
	// 计费 result 不应因截断而丢弃;首个完整帧的文本仍应已写出
	if res == nil {
		t.Fatal("result should be returned even on truncation")
	}
	if !strings.Contains(sink.String(), `"text":"partial"`) {
		t.Fatalf("first complete frame should have streamed: %s", sink.String())
	}
}

func TestStreamMessages_UnsupportedModel(t *testing.T) {
	req := &AnthropicRequest{}
	_ = json.Unmarshal([]byte(`{"model":"gpt-4","max_tokens":10,"messages":[{"role":"user","content":"x"}]}`), req)
	var sink strings.Builder
	_, err := StreamMessages(context.Background(), http.DefaultClient, &Credentials{AccessToken: "t"}, DefaultClientConfig(), req, &sink)
	if _, ok := err.(*UnsupportedModelError); !ok {
		t.Fatalf("err = %v, want *UnsupportedModelError (before any upstream call)", err)
	}
}

func readAllBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 512)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}
