package kiro

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// withFixedUUID 把 newUUID 固定为可预测值,便于断言。
func withFixedUUID(t *testing.T) {
	t.Helper()
	orig := newUUID
	n := 0
	newUUID = func() string {
		n++
		return "uuid-" + string(rune('0'+n))
	}
	t.Cleanup(func() { newUUID = orig })
}

func mustConvert(t *testing.T, body string) *ConversionResult {
	t.Helper()
	var req AnthropicRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	res, err := Convert(&req)
	if err != nil {
		t.Fatalf("Convert error: %v", err)
	}
	return res
}

func TestConvert_SimpleText(t *testing.T) {
	withFixedUUID(t)
	res := mustConvert(t, `{
		"model": "claude-sonnet-4-5-20250929",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": "Hello Kiro"}]
	}`)

	cs := res.ConversationState
	if res.ModelID != ModelSonnet45 {
		t.Fatalf("ModelID = %q, want %q", res.ModelID, ModelSonnet45)
	}
	if cs.AgentTaskType != "vibe" || cs.ChatTriggerType != "MANUAL" {
		t.Fatalf("agent/trigger = %q/%q", cs.AgentTaskType, cs.ChatTriggerType)
	}
	uim := cs.CurrentMessage.UserInputMessage
	if uim.Content != "Hello Kiro" || uim.ModelID != ModelSonnet45 || uim.Origin != Origin {
		t.Fatalf("current message = %+v", uim)
	}
	if len(cs.History) != 0 {
		t.Fatalf("history = %d, want 0", len(cs.History))
	}
}

func TestConvert_UnsupportedModel(t *testing.T) {
	var req AnthropicRequest
	_ = json.Unmarshal([]byte(`{"model":"gpt-4","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`), &req)
	_, err := Convert(&req)
	var ume *UnsupportedModelError
	if !errors.As(err, &ume) || ume.Model != "gpt-4" {
		t.Fatalf("err = %v, want UnsupportedModelError{gpt-4}", err)
	}
}

func TestConvert_EmptyMessages(t *testing.T) {
	var req AnthropicRequest
	_ = json.Unmarshal([]byte(`{"model":"claude-sonnet-4-5","max_tokens":1,"messages":[]}`), &req)
	if _, err := Convert(&req); err != ErrEmptyMessages {
		t.Fatalf("err = %v, want ErrEmptyMessages", err)
	}
}

func TestConvert_SystemPromptPairing(t *testing.T) {
	withFixedUUID(t)
	res := mustConvert(t, `{
		"model": "claude-opus-4-8",
		"max_tokens": 100,
		"system": "You are helpful.",
		"messages": [{"role": "user", "content": "hi"}]
	}`)
	h := res.ConversationState.History
	if len(h) != 2 || h[0].UserInputMessage == nil || h[1].AssistantResponseMessage == nil {
		t.Fatalf("history = %+v, want [user, assistant]", h)
	}
	sysContent := h[0].UserInputMessage.Content
	if !strings.HasPrefix(sysContent, "You are helpful.") || !strings.Contains(sysContent, systemChunkedPolicy) {
		t.Fatalf("system content missing prompt/policy: %q", sysContent)
	}
	if h[1].AssistantResponseMessage.Content != "I will follow these instructions." {
		t.Fatalf("assistant ack = %q", h[1].AssistantResponseMessage.Content)
	}
}

func TestConvert_SystemArrayForm(t *testing.T) {
	withFixedUUID(t)
	res := mustConvert(t, `{
		"model": "claude-opus-4-8",
		"max_tokens": 100,
		"system": [{"type":"text","text":"Part A"},{"type":"text","text":"Part B"}],
		"messages": [{"role": "user", "content": "hi"}]
	}`)
	sysContent := res.ConversationState.History[0].UserInputMessage.Content
	if !strings.HasPrefix(sysContent, "Part A\nPart B") {
		t.Fatalf("joined system content = %q", sysContent)
	}
}

func TestConvert_MultiTurnHistory(t *testing.T) {
	withFixedUUID(t)
	res := mustConvert(t, `{
		"model": "claude-sonnet-4-6",
		"max_tokens": 100,
		"messages": [
			{"role": "user", "content": "first"},
			{"role": "assistant", "content": "reply"},
			{"role": "user", "content": "second"}
		]
	}`)
	h := res.ConversationState.History
	if len(h) != 2 || h[0].UserInputMessage.Content != "first" || h[1].AssistantResponseMessage.Content != "reply" {
		t.Fatalf("history = %+v", h)
	}
	if res.ConversationState.CurrentMessage.UserInputMessage.Content != "second" {
		t.Fatalf("current = %q, want second", res.ConversationState.CurrentMessage.UserInputMessage.Content)
	}
}

func TestConvert_ToolUseResultPairing(t *testing.T) {
	withFixedUUID(t)
	res := mustConvert(t, `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 100,
		"messages": [
			{"role": "user", "content": "run tool"},
			{"role": "assistant", "content": [{"type":"tool_use","id":"tu_1","name":"read_file","input":{"path":"/x"}}]},
			{"role": "user", "content": [{"type":"tool_result","tool_use_id":"tu_1","content":"file body"}]}
		]
	}`)
	// 历史里的 tool_use 保留(已配对)
	h := res.ConversationState.History
	var found bool
	for _, m := range h {
		if m.AssistantResponseMessage != nil {
			for _, tu := range m.AssistantResponseMessage.ToolUses {
				if tu.ToolUseID == "tu_1" && tu.Name == "read_file" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("paired tool_use not preserved in history: %+v", h)
	}
	// 当前消息带上被验证的 tool_result
	trs := res.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.ToolResults
	if len(trs) != 1 || trs[0].ToolUseID != "tu_1" || trs[0].Status != "success" {
		t.Fatalf("current tool_results = %+v", trs)
	}
}

func TestConvert_OrphanedToolUseRemoved(t *testing.T) {
	withFixedUUID(t)
	// tool_use tu_x 在历史里,但没有任何 tool_result → 应被移除
	res := mustConvert(t, `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 100,
		"messages": [
			{"role": "user", "content": "go"},
			{"role": "assistant", "content": [{"type":"text","text":"ok"},{"type":"tool_use","id":"tu_x","name":"t","input":{}}]},
			{"role": "user", "content": "never mind"}
		]
	}`)
	for _, m := range res.ConversationState.History {
		if m.AssistantResponseMessage != nil {
			for _, tu := range m.AssistantResponseMessage.ToolUses {
				if tu.ToolUseID == "tu_x" {
					t.Fatalf("orphaned tool_use tu_x should have been removed: %+v", m.AssistantResponseMessage)
				}
			}
		}
	}
}

func TestConvert_Images(t *testing.T) {
	withFixedUUID(t)
	res := mustConvert(t, `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 100,
		"messages": [{"role": "user", "content": [
			{"type":"text","text":"look"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}
		]}]
	}`)
	uim := res.ConversationState.CurrentMessage.UserInputMessage
	if uim.Content != "look" {
		t.Fatalf("content = %q", uim.Content)
	}
	if len(uim.Images) != 1 || uim.Images[0].Format != "png" || uim.Images[0].Source.Bytes != "AAAA" {
		t.Fatalf("images = %+v", uim.Images)
	}
}

func TestConvert_ThinkingEnabledInjectedIntoSystem(t *testing.T) {
	withFixedUUID(t)
	res := mustConvert(t, `{
		"model": "claude-opus-4-8",
		"max_tokens": 100,
		"system": "sys",
		"thinking": {"type":"enabled","budget_tokens":30000},
		"messages": [{"role":"user","content":"hi"}]
	}`)
	if !res.Thinking {
		t.Fatal("result.Thinking should be true")
	}
	sysContent := res.ConversationState.History[0].UserInputMessage.Content
	if !strings.Contains(sysContent, "<thinking_mode>enabled</thinking_mode>") {
		t.Fatalf("thinking prefix not injected: %q", sysContent)
	}
	// budget 应被截顶到 24576
	if !strings.Contains(sysContent, "<max_thinking_length>24576</max_thinking_length>") {
		t.Fatalf("budget not capped: %q", sysContent)
	}
}

func TestConvert_ThinkingWithoutSystemInsertsMessage(t *testing.T) {
	withFixedUUID(t)
	res := mustConvert(t, `{
		"model": "claude-opus-4-8",
		"max_tokens": 100,
		"thinking": {"type":"adaptive"},
		"output_config": {"effort":"medium"},
		"messages": [{"role":"user","content":"hi"}]
	}`)
	h := res.ConversationState.History
	if len(h) != 2 || h[0].UserInputMessage == nil {
		t.Fatalf("history = %+v, want inserted thinking pair", h)
	}
	if !strings.Contains(h[0].UserInputMessage.Content, "<thinking_effort>medium</thinking_effort>") {
		t.Fatalf("adaptive effort not applied: %q", h[0].UserInputMessage.Content)
	}
}

func TestConvert_LongToolNameShortened(t *testing.T) {
	withFixedUUID(t)
	longName := strings.Repeat("a", 80)
	res := mustConvert(t, `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 100,
		"tools": [{"name":"`+longName+`","description":"d","input_schema":{"type":"object","properties":{}}}],
		"messages": [{"role":"user","content":"hi"}]
	}`)
	tools := res.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	short := tools[0].ToolSpecification.Name
	if len(short) > toolNameMaxLen {
		t.Fatalf("shortened name len = %d, want <= %d (%q)", len(short), toolNameMaxLen, short)
	}
	if res.ToolNameMap[short] != longName {
		t.Fatalf("ToolNameMap[%q] = %q, want original long name", short, res.ToolNameMap[short])
	}
}

func TestConvert_WriteToolDescriptionSuffix(t *testing.T) {
	withFixedUUID(t)
	res := mustConvert(t, `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 100,
		"tools": [{"name":"Write","description":"Writes files","input_schema":{"type":"object"}}],
		"messages": [{"role":"user","content":"hi"}]
	}`)
	desc := res.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools[0].ToolSpecification.Description
	if !strings.Contains(desc, "Writes files") || !strings.Contains(desc, writeToolDescriptionSuffix) {
		t.Fatalf("Write tool description missing suffix: %q", desc)
	}
}

func TestConvert_SchemaNormalized(t *testing.T) {
	withFixedUUID(t)
	// required: null / properties 缺失 → 规范化
	res := mustConvert(t, `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 100,
		"tools": [{"name":"t","description":"d","input_schema":{"type":"object","required":null}}],
		"messages": [{"role":"user","content":"hi"}]
	}`)
	schema := res.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools[0].ToolSpecification.InputSchema.JSON
	var m map[string]any
	if err := json.Unmarshal(schema, &m); err != nil {
		t.Fatalf("schema not valid json: %v", err)
	}
	if _, ok := m["properties"].(map[string]any); !ok {
		t.Fatalf("properties not normalized to object: %v", m["properties"])
	}
	if arr, ok := m["required"].([]any); !ok || len(arr) != 0 {
		t.Fatalf("required not normalized to empty array: %v", m["required"])
	}
}

func TestConvert_PrefillDropped(t *testing.T) {
	withFixedUUID(t)
	// 末尾 assistant(prefill)应被丢弃,current 回退到最后一条 user
	res := mustConvert(t, `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 100,
		"messages": [
			{"role":"user","content":"real question"},
			{"role":"assistant","content":"prefill start"}
		]
	}`)
	if res.ConversationState.CurrentMessage.UserInputMessage.Content != "real question" {
		t.Fatalf("current = %q, want 'real question'", res.ConversationState.CurrentMessage.UserInputMessage.Content)
	}
	if len(res.ConversationState.History) != 0 {
		t.Fatalf("history = %+v, want empty", res.ConversationState.History)
	}
}

func TestConvert_SessionIDFromMetadata(t *testing.T) {
	res := mustConvert(t, `{
		"model": "claude-sonnet-4-5",
		"max_tokens": 100,
		"metadata": {"user_id": "user_abc_account__session_0b4445e1-f5be-49e1-87ce-62bbc28ad705"},
		"messages": [{"role":"user","content":"hi"}]
	}`)
	if got := res.ConversationState.ConversationID; got != "0b4445e1-f5be-49e1-87ce-62bbc28ad705" {
		t.Fatalf("conversationID = %q, want extracted session UUID", got)
	}
}

func TestExtractSessionID(t *testing.T) {
	if id, ok := extractSessionID(`{"session_id":"0b4445e1-f5be-49e1-87ce-62bbc28ad705"}`); !ok || id != "0b4445e1-f5be-49e1-87ce-62bbc28ad705" {
		t.Fatalf("json form: got (%q, %v)", id, ok)
	}
	if _, ok := extractSessionID("no session here"); ok {
		t.Fatal("expected no session id")
	}
	if _, ok := extractSessionID("session_not-a-valid-uuid-string-too-short"); ok {
		t.Fatal("invalid uuid should not match")
	}
}
