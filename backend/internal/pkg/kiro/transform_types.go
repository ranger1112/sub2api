package kiro

import "encoding/json"

// 以下为转换所需的 Anthropic /v1/messages 请求输入类型(仅取转换用到的字段)。
// 字段命名对照 Anthropic API,与 kiro.rs 的 src/anthropic/types.rs 一致。

// AnthropicRequest 是 /v1/messages 请求体。
type AnthropicRequest struct {
	Model        string             `json:"model"`
	MaxTokens    int                `json:"max_tokens"`
	Messages     []AnthropicMessage `json:"messages"`
	Stream       bool               `json:"stream"`
	System       json.RawMessage    `json:"system"` // string 或 []{text}
	Tools        []AnthropicTool    `json:"tools"`
	ToolChoice   json.RawMessage    `json:"tool_choice"`
	Thinking     *Thinking          `json:"thinking"`
	OutputConfig *OutputConfig      `json:"output_config"`
	Metadata     *Metadata          `json:"metadata"`
}

// AnthropicMessage 是一条消息,content 可为 string 或 ContentBlock 数组。
type AnthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// AnthropicSystemMessage 是数组形式 system 的元素。
type AnthropicSystemMessage struct {
	Text string `json:"text"`
}

// AnthropicTool 是 Anthropic 工具定义(普通工具或 web_search 工具)。
type AnthropicTool struct {
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	MaxUses     *int            `json:"max_uses,omitempty"`
}

// AnthropicContentBlock 是消息内容块(text/image/tool_use/tool_result/thinking)。
type AnthropicContentBlock struct {
	Type      string                `json:"type"`
	Text      *string               `json:"text,omitempty"`
	Thinking  *string               `json:"thinking,omitempty"`
	ToolUseID *string               `json:"tool_use_id,omitempty"`
	Content   json.RawMessage       `json:"content,omitempty"`
	Name      *string               `json:"name,omitempty"`
	Input     json.RawMessage       `json:"input,omitempty"`
	ID        *string               `json:"id,omitempty"`
	IsError   *bool                 `json:"is_error,omitempty"`
	Source    *AnthropicImageSource `json:"source,omitempty"`
}

// AnthropicImageSource 是图片数据源。
type AnthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// Thinking 是 thinking 配置。BudgetTokens 用指针区分"缺省"与"显式 0"。
type Thinking struct {
	Type         string `json:"type"`
	BudgetTokens *int   `json:"budget_tokens"`
}

// IsEnabled 报告是否启用了 thinking(enabled 或 adaptive)。
func (t *Thinking) IsEnabled() bool {
	return t != nil && (t.Type == "enabled" || t.Type == "adaptive")
}

// OutputConfig 承载 adaptive thinking 的 effort。
type OutputConfig struct {
	Effort string `json:"effort"`
}

// Metadata 是 Claude Code 请求中的元数据,user_id 可含 session 信息。
type Metadata struct {
	UserID string `json:"user_id"`
}

// ConversionResult 是 Anthropic → Kiro 转换的产物。
type ConversionResult struct {
	ConversationState ConversationState
	// ToolNameMap 记录被缩短的超长工具名(短名 → 原名),流式回写时需据此还原。
	ToolNameMap map[string]string
	ModelID     string
	Thinking    bool
}

// UnsupportedModelError 表示模型无法映射到 Kiro 支持的模型。
type UnsupportedModelError struct{ Model string }

func (e *UnsupportedModelError) Error() string { return "kiro: unsupported model: " + e.Model }
