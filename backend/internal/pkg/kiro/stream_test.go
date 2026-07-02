package kiro

import (
	"strings"
	"testing"
)

// ---- helpers ----

func filterEvents(events []SSEEvent, name string) []SSEEvent {
	var out []SSEEvent
	for _, e := range events {
		if e.Event == name {
			out = append(out, e)
		}
	}
	return out
}

func firstEvent(events []SSEEvent, name string) (SSEEvent, bool) {
	for _, e := range events {
		if e.Event == name {
			return e, true
		}
	}
	return SSEEvent{}, false
}

func deltaField(e SSEEvent, key string) any {
	d, _ := e.Data["delta"].(map[string]any)
	return d[key]
}

func collectTextDeltas(events []SSEEvent) string {
	var sb strings.Builder
	for _, e := range filterEvents(events, "content_block_delta") {
		if deltaField(e, "type") == "text_delta" {
			if s, ok := deltaField(e, "text").(string); ok {
				sb.WriteString(s)
			}
		}
	}
	return sb.String()
}

func collectThinkingDeltas(events []SSEEvent) string {
	var sb strings.Builder
	for _, e := range filterEvents(events, "content_block_delta") {
		if deltaField(e, "type") == "thinking_delta" {
			if s, ok := deltaField(e, "thinking").(string); ok {
				sb.WriteString(s)
			}
		}
	}
	return sb.String()
}

// ---- SSE format ----

func TestSSEEvent_String(t *testing.T) {
	e := SSEEvent{Event: "message_start", Data: map[string]any{"type": "message_start"}}
	s := e.String()
	if !strings.HasPrefix(s, "event: message_start\n") || !strings.Contains(s, "data: ") || !strings.HasSuffix(s, "\n\n") {
		t.Fatalf("bad SSE format: %q", s)
	}
}

// ---- simple text ----

func TestStreamContext_SimpleText(t *testing.T) {
	withFixedUUID(t)
	c := NewStreamContext("claude-sonnet-4-5", 10, false, nil)

	initial := c.GenerateInitialEvents()
	if _, ok := firstEvent(initial, "message_start"); !ok {
		t.Fatal("missing message_start")
	}
	if start, ok := firstEvent(initial, "content_block_start"); !ok || start.Data["content_block"].(map[string]any)["type"] != "text" {
		t.Fatalf("missing initial text block: %+v", initial)
	}

	mid := c.ProcessKiroEvent(Event{Kind: EventAssistantResponse, Content: "Hello world"})
	if collectTextDeltas(mid) != "Hello world" {
		t.Fatalf("text delta = %q", collectTextDeltas(mid))
	}

	final := c.GenerateFinalEvents()
	md, ok := firstEvent(final, "message_delta")
	if !ok {
		t.Fatal("missing message_delta")
	}
	if got := md.Data["delta"].(map[string]any)["stop_reason"]; got != "end_turn" {
		t.Fatalf("stop_reason = %v, want end_turn", got)
	}
	if _, ok := firstEvent(final, "message_stop"); !ok {
		t.Fatal("missing message_stop")
	}
}

// ---- tool use ----

func TestStreamContext_ToolUseClosesTextAndRestores(t *testing.T) {
	withFixedUUID(t)
	c := NewStreamContext("claude-sonnet-4-5", 5, false, map[string]string{"short_x": "mcp__original__tool"})
	c.GenerateInitialEvents()
	c.ProcessKiroEvent(Event{Kind: EventAssistantResponse, Content: "let me check"})

	toolEvents := c.ProcessKiroEvent(Event{Kind: EventToolUse, ToolUse: ToolUseEvent{
		Name: "short_x", ToolUseID: "tu_1", Input: `{"a":1}`, Stop: true,
	}})

	// 文本块应被关闭
	if len(filterEvents(toolEvents, "content_block_stop")) == 0 {
		t.Fatalf("expected text block to be closed: %+v", toolEvents)
	}
	start, ok := firstEvent(toolEvents, "content_block_start")
	if !ok {
		t.Fatal("missing tool_use content_block_start")
	}
	cb := start.Data["content_block"].(map[string]any)
	if cb["type"] != "tool_use" || cb["name"] != "mcp__original__tool" {
		t.Fatalf("tool_use name not restored: %+v", cb)
	}
	// input_json_delta
	var sawInput bool
	for _, e := range filterEvents(toolEvents, "content_block_delta") {
		if deltaField(e, "type") == "input_json_delta" && deltaField(e, "partial_json") == `{"a":1}` {
			sawInput = true
		}
	}
	if !sawInput {
		t.Fatalf("missing input_json_delta: %+v", toolEvents)
	}

	final := c.GenerateFinalEvents()
	md, _ := firstEvent(final, "message_delta")
	if md.Data["delta"].(map[string]any)["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %v, want tool_use", md.Data["delta"].(map[string]any)["stop_reason"])
	}
}

func TestStreamContext_TextAfterToolUseRestartsBlock(t *testing.T) {
	withFixedUUID(t)
	c := NewStreamContext("m", 1, false, nil)
	c.GenerateInitialEvents()
	initialIdx := c.textBlockIndex

	toolEvents := c.ProcessKiroEvent(Event{Kind: EventToolUse, ToolUse: ToolUseEvent{Name: "t", ToolUseID: "tool_1", Input: "{}", Stop: false}})
	var closedInitial bool
	for _, e := range filterEvents(toolEvents, "content_block_stop") {
		if idxToInt(e.Data["index"]) == initialIdx {
			closedInitial = true
		}
	}
	if !closedInitial {
		t.Fatal("tool_use should close the initial text block")
	}

	textEvents := c.ProcessKiroEvent(Event{Kind: EventAssistantResponse, Content: "hello"})
	start, ok := firstEvent(textEvents, "content_block_start")
	if !ok || start.Data["content_block"].(map[string]any)["type"] != "text" {
		t.Fatalf("should restart a new text block: %+v", textEvents)
	}
	if idxToInt(start.Data["index"]) == initialIdx {
		t.Fatal("new text block index should differ from the stopped one")
	}
	if collectTextDeltas(textEvents) != "hello" {
		t.Fatalf("text delta = %q", collectTextDeltas(textEvents))
	}
}

// ---- context usage ----

func TestStreamContext_ContextUsageSetsInputTokens(t *testing.T) {
	withFixedUUID(t)
	c := NewStreamContext("claude-sonnet-4-5", 999, false, nil) // 200K window
	c.GenerateInitialEvents()
	c.ProcessKiroEvent(Event{Kind: EventContextUsage, ContextUsagePercentage: 10.0}) // 10% of 200000 = 20000
	final := c.GenerateFinalEvents()
	md, _ := firstEvent(final, "message_delta")
	usage := md.Data["usage"].(map[string]any)
	if usage["input_tokens"] != 20000 {
		t.Fatalf("input_tokens = %v, want 20000", usage["input_tokens"])
	}
}

func TestStreamContext_ContextWindowExceeded(t *testing.T) {
	withFixedUUID(t)
	c := NewStreamContext("claude-sonnet-4-5", 1, false, nil)
	c.GenerateInitialEvents()
	c.ProcessKiroEvent(Event{Kind: EventContextUsage, ContextUsagePercentage: 100.0})
	final := c.GenerateFinalEvents()
	md, _ := firstEvent(final, "message_delta")
	if md.Data["delta"].(map[string]any)["stop_reason"] != "model_context_window_exceeded" {
		t.Fatalf("stop_reason = %v", md.Data["delta"].(map[string]any)["stop_reason"])
	}
}

// ---- buffered backfill ----

func TestBufferedStreamContext_BackfillsInputTokens(t *testing.T) {
	withFixedUUID(t)
	b := NewBufferedStreamContext("claude-sonnet-4-5", 111, false, nil)
	b.ProcessAndBuffer(Event{Kind: EventAssistantResponse, Content: "hi"})
	b.ProcessAndBuffer(Event{Kind: EventContextUsage, ContextUsagePercentage: 5.0}) // 5% of 200000 = 10000
	all := b.FinishAndGetAllEvents()

	ms, ok := firstEvent(all, "message_start")
	if !ok {
		t.Fatal("missing message_start")
	}
	usage := ms.Data["message"].(map[string]any)["usage"].(map[string]any)
	if usage["input_tokens"] != 10000 {
		t.Fatalf("backfilled input_tokens = %v, want 10000", usage["input_tokens"])
	}
}

// ---- thinking ----

func TestStreamContext_ThinkingSplitsThinkingAndText(t *testing.T) {
	withFixedUUID(t)
	c := NewStreamContext("claude-opus-4-8", 1, true, nil)
	c.GenerateInitialEvents()

	events := c.ProcessKiroEvent(Event{
		Kind:    EventAssistantResponse,
		Content: "<thinking>\nreasoning here</thinking>\n\nfinal answer",
	})

	if got := collectThinkingDeltas(events); got != "reasoning here" {
		t.Fatalf("thinking = %q, want 'reasoning here'", got)
	}
	if got := collectTextDeltas(events); got != "final answer" {
		t.Fatalf("text = %q, want 'final answer'", got)
	}
	// thinking 块应在文本块之前(索引更小)
	var thinkingStartIdx, textStartIdx = -1, -1
	for _, e := range filterEvents(events, "content_block_start") {
		cb := e.Data["content_block"].(map[string]any)
		switch cb["type"] {
		case "thinking":
			thinkingStartIdx = idxToInt(e.Data["index"])
		case "text":
			textStartIdx = idxToInt(e.Data["index"])
		}
	}
	if thinkingStartIdx < 0 || textStartIdx < 0 || thinkingStartIdx >= textStartIdx {
		t.Fatalf("block ordering wrong: thinking=%d text=%d", thinkingStartIdx, textStartIdx)
	}

	final := c.GenerateFinalEvents()
	md, _ := firstEvent(final, "message_delta")
	if md.Data["delta"].(map[string]any)["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason = %v, want end_turn", md.Data["delta"].(map[string]any)["stop_reason"])
	}
}

// ---- thinking tag helpers (对照 kiro.rs 单测) ----

func TestFindRealThinkingStartTag(t *testing.T) {
	if findRealThinkingStartTag("<thinking>") != 0 {
		t.Fatal("plain start tag")
	}
	if findRealThinkingStartTag("prefix<thinking>") != 6 {
		t.Fatal("prefixed start tag")
	}
	if findRealThinkingStartTag("`<thinking>`") != -1 {
		t.Fatal("backtick-wrapped should be skipped")
	}
	if findRealThinkingStartTag("about `<thinking>` tag<thinking>content") != 22 {
		t.Fatal("should skip quoted, find real one")
	}
}

func TestFindRealThinkingEndTag(t *testing.T) {
	if findRealThinkingEndTag("</thinking>\n\n") != 0 {
		t.Fatal("plain end tag")
	}
	if findRealThinkingEndTag("content</thinking>\n\n") != 7 {
		t.Fatal("content before end tag")
	}
	if findRealThinkingEndTag("</thinking>") != -1 {
		t.Fatal("no double newline -> not an end tag yet")
	}
	if findRealThinkingEndTag("`</thinking>`\n\n") != -1 {
		t.Fatal("backtick-wrapped end tag should be skipped")
	}
}

func TestExtractThinkingFromCompleteText(t *testing.T) {
	thinking, ok, remaining := ExtractThinkingFromCompleteText("<thinking>\nmy reasoning</thinking>\n\nthe answer")
	if !ok || thinking != "my reasoning" || remaining != "the answer" {
		t.Fatalf("got (%q, %v, %q)", thinking, ok, remaining)
	}
	// 无 thinking 标签 → 原样返回
	if th, ok, rem := ExtractThinkingFromCompleteText("just text"); ok || th != "" || rem != "just text" {
		t.Fatalf("no-thinking case: (%q, %v, %q)", th, ok, rem)
	}
}

func TestEstimateTokens(t *testing.T) {
	if estimateTokens("Hello") <= 0 || estimateTokens("你好") <= 0 || estimateTokens("") != 1 {
		t.Fatal("estimateTokens should be >=1")
	}
	// 中文比同字符数英文占更多 token
	if estimateTokens("你好世界你好世界") <= estimateTokens("abcdefgh") {
		t.Fatalf("chinese token weight unexpected")
	}
}

func idxToInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return -1
	}
}
