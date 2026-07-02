package kiro

import "encoding/json"

// 本文件定义发往 Kiro generateAssistantResponse 端点的请求体结构,
// 字段与 JSON 键严格对照 kiro.rs 的 src/kiro/model/requests(camelCase)。

// Origin 是 Kiro 请求中消息来源的固定取值。
const Origin = "AI_EDITOR"

// KiroRequest 是发往 Kiro API 的顶层请求体。
// profileArn 通常在网关层注入到请求体根,这里也保留字段以便直接序列化。
type KiroRequest struct {
	ConversationState ConversationState `json:"conversationState"`
	ProfileArn        string            `json:"profileArn,omitempty"`
}

// ToJSON 序列化为紧凑 JSON。
func (r *KiroRequest) ToJSON() ([]byte, error) { return json.Marshal(r) }

// ConversationState 是请求核心,包含当前消息与历史记录。
type ConversationState struct {
	AgentContinuationID string         `json:"agentContinuationId,omitempty"`
	AgentTaskType       string         `json:"agentTaskType,omitempty"`
	ChatTriggerType     string         `json:"chatTriggerType,omitempty"`
	CurrentMessage      CurrentMessage `json:"currentMessage"`
	ConversationID      string         `json:"conversationId"`
	History             []Message      `json:"history,omitempty"`
}

// CurrentMessage 是当前轮次消息的容器。
type CurrentMessage struct {
	UserInputMessage UserInputMessage `json:"userInputMessage"`
}

// UserInputMessage 是当前轮次的用户输入消息。
type UserInputMessage struct {
	UserInputMessageContext UserInputMessageContext `json:"userInputMessageContext"`
	Content                 string                  `json:"content"`
	ModelID                 string                  `json:"modelId"`
	Images                  []KiroImage             `json:"images,omitempty"`
	Origin                  string                  `json:"origin,omitempty"`
}

// UserInputMessageContext 承载工具定义与工具执行结果。
type UserInputMessageContext struct {
	ToolResults []ToolResult `json:"toolResults,omitempty"`
	Tools       []Tool       `json:"tools,omitempty"`
}

// IsEmpty 报告上下文是否既无工具也无工具结果(用于历史消息的省略判断)。
func (c UserInputMessageContext) IsEmpty() bool {
	return len(c.Tools) == 0 && len(c.ToolResults) == 0
}

// KiroImage 是 Kiro 请求中的图片。
type KiroImage struct {
	Format string          `json:"format"`
	Source KiroImageSource `json:"source"`
}

// KiroImageSource 是图片数据源(base64)。
type KiroImageSource struct {
	Bytes string `json:"bytes"`
}

// Message 是一条历史消息,恰含用户消息或助手消息之一
// (对应 kiro.rs 的 untagged enum,由不同的 JSON 键区分)。
type Message struct {
	UserInputMessage         *UserMessage      `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *AssistantMessage `json:"assistantResponseMessage,omitempty"`
}

// UserMessage 是历史记录中的用户消息。
type UserMessage struct {
	Content                 string                   `json:"content"`
	ModelID                 string                   `json:"modelId"`
	Origin                  string                   `json:"origin,omitempty"`
	Images                  []KiroImage              `json:"images,omitempty"`
	UserInputMessageContext *UserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

// AssistantMessage 是历史记录中的助手消息。
type AssistantMessage struct {
	Content  string         `json:"content"`
	ToolUses []ToolUseEntry `json:"toolUses,omitempty"`
}

// Tool 是一个工具定义。
type Tool struct {
	ToolSpecification ToolSpecification `json:"toolSpecification"`
}

// ToolSpecification 定义工具名称、描述与输入 schema。
type ToolSpecification struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema 包装工具的 JSON Schema。
type InputSchema struct {
	JSON json.RawMessage `json:"json"`
}

// ToolResult 是一次工具调用的执行结果。
// Content 为对象数组(如 [{"text":"..."}]),与 Kiro 上游一致。
type ToolResult struct {
	ToolUseID string           `json:"toolUseId"`
	Content   []map[string]any `json:"content"`
	Status    string           `json:"status,omitempty"`
	IsError   bool             `json:"isError,omitempty"`
}

// ToolUseEntry 记录历史助手消息中的一次工具调用。
type ToolUseEntry struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}
