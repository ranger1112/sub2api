package kiro

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestKiroRequest_Serialize(t *testing.T) {
	req := KiroRequest{
		ConversationState: ConversationState{
			ConversationID:  "conv-123",
			AgentTaskType:   "vibe",
			ChatTriggerType: "MANUAL",
			CurrentMessage: CurrentMessage{
				UserInputMessage: UserInputMessage{
					Content: "Hello",
					ModelID: ModelSonnet45,
					Origin:  Origin,
				},
			},
		},
	}
	b, err := req.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	s := string(b)
	for _, want := range []string{
		`"conversationState"`, `"conversationId":"conv-123"`, `"agentTaskType":"vibe"`,
		`"chatTriggerType":"MANUAL"`, `"currentMessage"`, `"userInputMessage"`,
		`"content":"Hello"`, `"modelId":"claude-sonnet-4.5"`, `"origin":"AI_EDITOR"`,
		`"userInputMessageContext":{}`, // 当前消息的上下文始终存在(非指针),空时为 {}
	} {
		if !strings.Contains(s, want) {
			t.Errorf("serialized request missing %s\n got: %s", want, s)
		}
	}
	// 空历史 / 空 profileArn 应被省略
	if strings.Contains(s, `"history"`) || strings.Contains(s, `"profileArn"`) {
		t.Errorf("expected history/profileArn omitted, got: %s", s)
	}
}

func TestMessage_UserVsAssistantKeys(t *testing.T) {
	userMsg := Message{UserInputMessage: &UserMessage{Content: "hi", ModelID: ModelSonnet45, Origin: Origin}}
	ub, _ := json.Marshal(userMsg)
	if !strings.Contains(string(ub), `"userInputMessage"`) || strings.Contains(string(ub), "assistantResponseMessage") {
		t.Fatalf("user history json = %s", ub)
	}
	asstMsg := Message{AssistantResponseMessage: &AssistantMessage{Content: "hello"}}
	ab, _ := json.Marshal(asstMsg)
	if !strings.Contains(string(ab), `"assistantResponseMessage"`) || strings.Contains(string(ab), "userInputMessage") {
		t.Fatalf("assistant history json = %s", ab)
	}
}

func TestToolResult_OmitsIsErrorWhenFalse(t *testing.T) {
	ok := ToolResult{ToolUseID: "tool-1", Content: []map[string]any{{"text": "done"}}, Status: "success"}
	b, _ := json.Marshal(ok)
	s := string(b)
	if !strings.Contains(s, `"toolUseId":"tool-1"`) || !strings.Contains(s, `"status":"success"`) {
		t.Fatalf("tool result json = %s", s)
	}
	if strings.Contains(s, "isError") {
		t.Fatalf("isError should be omitted when false: %s", s)
	}
	bad := ToolResult{ToolUseID: "t2", Content: []map[string]any{{"text": "boom"}}, Status: "error", IsError: true}
	be, _ := json.Marshal(bad)
	if !strings.Contains(string(be), `"isError":true`) {
		t.Fatalf("expected isError:true, got %s", be)
	}
}

func TestTool_Serialize(t *testing.T) {
	tool := Tool{ToolSpecification: ToolSpecification{
		Name:        "read_file",
		Description: "reads a file",
		InputSchema: InputSchema{JSON: json.RawMessage(`{"type":"object","properties":{}}`)},
	}}
	b, _ := json.Marshal(tool)
	s := string(b)
	for _, want := range []string{`"toolSpecification"`, `"name":"read_file"`, `"inputSchema"`, `"json":{"type":"object"`} {
		if !strings.Contains(s, want) {
			t.Errorf("tool json missing %s: %s", want, s)
		}
	}
}

func TestKiroRequest_RoundTrip(t *testing.T) {
	orig := KiroRequest{
		ConversationState: ConversationState{
			ConversationID: "c1",
			CurrentMessage: CurrentMessage{UserInputMessage: UserInputMessage{Content: "hi", ModelID: ModelOpus48}},
			History: []Message{
				{UserInputMessage: &UserMessage{Content: "q", ModelID: ModelOpus48, Origin: Origin}},
				{AssistantResponseMessage: &AssistantMessage{
					Content:  "a",
					ToolUses: []ToolUseEntry{{ToolUseID: "u1", Name: "t", Input: json.RawMessage(`{"x":1}`)}},
				}},
			},
		},
		ProfileArn: "arn:aws:codewhisperer:us-east-1:123:profile/ABC",
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var back KiroRequest
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.ConversationState.ConversationID != "c1" || back.ProfileArn != orig.ProfileArn {
		t.Fatalf("round trip scalar mismatch: %+v", back)
	}
	h := back.ConversationState.History
	if len(h) != 2 || h[0].UserInputMessage == nil || h[1].AssistantResponseMessage == nil {
		t.Fatalf("history round trip wrong: %+v", h)
	}
	if len(h[1].AssistantResponseMessage.ToolUses) != 1 || h[1].AssistantResponseMessage.ToolUses[0].Name != "t" {
		t.Fatalf("tool use round trip wrong: %+v", h[1].AssistantResponseMessage.ToolUses)
	}
}
