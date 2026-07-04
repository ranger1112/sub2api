package kiro

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ErrEmptyMessages 表示请求 messages 为空(或 prefill 处理后无 user 消息)。
var ErrEmptyMessages = errors.New("kiro: empty messages")

// newUUID 可在测试中替换以获得确定性输出。
var newUUID = uuid.NewString

const (
	toolNameMaxLen          = 63
	maxToolDescriptionChars = 10000
	maxBudgetTokens         = 24576
	defaultBudgetTokens     = 20000
)

// 追加到 Write / Edit 工具描述末尾的分块写入约束,以及追加到系统提示的策略。
// 原文对照 kiro.rs 的常量。
const (
	writeToolDescriptionSuffix = "- IMPORTANT: If the content to write exceeds 150 lines, you MUST only write the first 50 lines using this tool, then use `Edit` tool to append the remaining content in chunks of no more than 50 lines each. If needed, leave a unique placeholder to help append content. Do NOT attempt to write all content at once."
	editToolDescriptionSuffix  = "- IMPORTANT: If the `new_string` content exceeds 50 lines, you MUST split it into multiple Edit calls, each replacing no more than 50 lines at a time. If used to append content, leave a unique placeholder to help append content. On the final chunk, do NOT include the placeholder."
	systemChunkedPolicy        = "When the Write or Edit tool has content size limits, always comply silently. Never suggest bypassing these limits via alternative tools. Never ask the user whether to switch approaches. Complete all chunked operations without commentary."
)

// Convert 把 Anthropic /v1/messages 请求转换为 Kiro 的 ConversationState。
// 模型映射失败返回 *UnsupportedModelError,消息为空返回 ErrEmptyMessages。
func Convert(req *AnthropicRequest) (*ConversionResult, error) {
	modelID, ok := MapModel(req.Model)
	if !ok {
		return nil, &UnsupportedModelError{Model: req.Model}
	}
	if len(req.Messages) == 0 {
		return nil, ErrEmptyMessages
	}

	// prefill 预处理:末尾若非 user(assistant prefill),静默截断到最后一条 user。
	messages := req.Messages
	if messages[len(messages)-1].Role != "user" {
		lastUser := -1
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				lastUser = i
				break
			}
		}
		if lastUser < 0 {
			return nil, ErrEmptyMessages
		}
		messages = messages[:lastUser+1]
	}

	// conversationId 优先取自 metadata.user_id 的 session UUID,否则随机生成。
	conversationID := ""
	if req.Metadata != nil && req.Metadata.UserID != "" {
		if sid, ok := extractSessionID(req.Metadata.UserID); ok {
			conversationID = sid
		}
	}
	if conversationID == "" {
		conversationID = newUUID()
	}

	// 当前消息 = 最后一条(经 prefill 处理后必为 user)。
	last := messages[len(messages)-1]
	textContent, images, toolResults, err := processMessageContent(last.Content)
	if err != nil {
		return nil, err
	}

	toolNameMap := map[string]string{}
	tools := convertTools(req.Tools, toolNameMap)

	history, err := buildHistory(req, messages, modelID, toolNameMap)
	if err != nil {
		return nil, err
	}

	// 校验 tool_use/tool_result 配对:过滤孤立/重复 tool_result,并移除孤立 tool_use。
	validatedToolResults, orphaned := validateToolPairing(history, toolResults)
	removeOrphanedToolUses(history, orphaned)

	// 为历史中引用但未在 tools 定义的工具补占位符(Kiro 要求,名称忽略大小写)。
	existing := map[string]bool{}
	for _, t := range tools {
		existing[strings.ToLower(t.ToolSpecification.Name)] = true
	}
	for _, name := range collectHistoryToolNames(history) {
		if !existing[strings.ToLower(name)] {
			tools = append(tools, placeholderTool(name))
			existing[strings.ToLower(name)] = true
		}
	}

	ctx := UserInputMessageContext{}
	if len(tools) > 0 {
		ctx.Tools = tools
	}
	if len(validatedToolResults) > 0 {
		ctx.ToolResults = validatedToolResults
	}

	userInput := UserInputMessage{
		UserInputMessageContext: ctx,
		Content:                 textContent,
		ModelID:                 modelID,
		Origin:                  Origin,
	}
	if len(images) > 0 {
		userInput.Images = images
	}

	cs := ConversationState{
		AgentContinuationID: newUUID(),
		AgentTaskType:       "vibe",
		ChatTriggerType:     "MANUAL", // AUTO 可能触发 400,固定 MANUAL
		CurrentMessage:      CurrentMessage{UserInputMessage: userInput},
		ConversationID:      conversationID,
		History:             history,
	}

	return &ConversionResult{
		ConversationState: cs,
		ToolNameMap:       toolNameMap,
		ModelID:           modelID,
		Thinking:          req.Thinking.IsEnabled(),
	}, nil
}

// processMessageContent 从消息内容(string 或数组)提取文本、图片与工具结果。
func processMessageContent(content json.RawMessage) (text string, images []KiroImage, toolResults []ToolResult, err error) {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s, nil, nil, nil
	}
	var arr []json.RawMessage
	if json.Unmarshal(content, &arr) != nil {
		return "", nil, nil, nil // 其他类型忽略
	}

	var textParts []string
	for _, item := range arr {
		var block AnthropicContentBlock
		if json.Unmarshal(item, &block) != nil {
			continue
		}
		switch block.Type {
		case "text":
			if block.Text != nil {
				textParts = append(textParts, *block.Text)
			}
		case "image":
			if block.Source != nil {
				if format, ok := imageFormat(block.Source.MediaType); ok {
					images = append(images, KiroImage{Format: format, Source: KiroImageSource{Bytes: block.Source.Data}})
				}
			}
		case "tool_result":
			if block.ToolUseID != nil {
				rc := extractToolResultContent(block.Content)
				isErr := block.IsError != nil && *block.IsError
				status := "success"
				if isErr {
					status = "error"
				}
				toolResults = append(toolResults, ToolResult{
					ToolUseID: *block.ToolUseID,
					Content:   []map[string]any{{"text": rc}},
					Status:    status,
					IsError:   isErr,
				})
			}
		}
	}
	return strings.Join(textParts, "\n"), images, toolResults, nil
}

// extractToolResultContent 把 tool_result.content(string / 数组 / 其他)提取为纯文本。
func extractToolResultContent(content json.RawMessage) string {
	if len(content) == 0 || string(content) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var arr []json.RawMessage
	if json.Unmarshal(content, &arr) == nil {
		var parts []string
		for _, item := range arr {
			var obj struct {
				Text *string `json:"text"`
			}
			if json.Unmarshal(item, &obj) == nil && obj.Text != nil {
				parts = append(parts, *obj.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(content)
}

func imageFormat(mediaType string) (string, bool) {
	switch mediaType {
	case "image/jpeg":
		return "jpeg", true
	case "image/png":
		return "png", true
	case "image/gif":
		return "gif", true
	case "image/webp":
		return "webp", true
	default:
		return "", false
	}
}

// convertTools 转换工具定义:追加 Write/Edit 描述后缀、截断描述、缩短超长名、规范化 schema。
func convertTools(tools []AnthropicTool, toolNameMap map[string]string) []Tool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]Tool, 0, len(tools))
	for _, t := range tools {
		desc := t.Description
		switch t.Name {
		case "Write":
			desc += "\n" + writeToolDescriptionSuffix
		case "Edit":
			desc += "\n" + editToolDescriptionSuffix
		}
		desc = truncateRunes(desc, maxToolDescriptionChars)
		out = append(out, Tool{ToolSpecification: ToolSpecification{
			Name:        mapToolName(t.Name, toolNameMap),
			Description: desc,
			InputSchema: InputSchema{JSON: normalizeJSONSchema(t.InputSchema)},
		}})
	}
	return out
}

// normalizeJSONSchema 修复 MCP 工具定义中常见的 schema 问题,避免上游 400。
func normalizeJSONSchema(raw json.RawMessage) json.RawMessage {
	defaultSchema := json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":true}`)
	if len(raw) == 0 {
		return defaultSchema
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return defaultSchema
	}
	if s, ok := obj["type"].(string); !ok || s == "" {
		obj["type"] = "object"
	}
	if _, ok := obj["properties"].(map[string]any); !ok {
		obj["properties"] = map[string]any{}
	}
	if arr, ok := obj["required"].([]any); ok {
		req := make([]any, 0, len(arr))
		for _, v := range arr {
			if s, ok := v.(string); ok {
				req = append(req, s)
			}
		}
		obj["required"] = req
	} else {
		obj["required"] = []any{}
	}
	switch obj["additionalProperties"].(type) {
	case bool, map[string]any:
		// 保留
	default:
		obj["additionalProperties"] = true
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return defaultSchema
	}
	return b
}

func mapToolName(name string, toolNameMap map[string]string) string {
	if len(name) <= toolNameMaxLen {
		return name
	}
	short := shortenToolName(name)
	toolNameMap[short] = name
	return short
}

// shortenToolName 生成确定性短名:前缀 + "_" + 8 位 SHA256 hex。
// 前缀按【字节】截断到 54(= 63-1-8)且落在 UTF-8 边界上,确保结果 <= 63 字节
// (Kiro 的工具名上限是字节数)。这里刻意比 kiro.rs 更严格:后者按字符截断,
// 对多字节名会溢出 63 字节导致上游 400。
func shortenToolName(name string) string {
	sum := sha256.Sum256([]byte(name))
	hashHex := hex.EncodeToString(sum[:])[:8]
	prefix := name[:findCharBoundary(name, toolNameMaxLen-1-8)]
	return prefix + "_" + hashHex
}

// truncateRunes 返回 s 的前 maxRunes 个字符(rune 安全,不会截断多字节字符)。
func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i]
		}
		count++
	}
	return s
}

func generateThinkingPrefix(req *AnthropicRequest) string {
	t := req.Thinking
	if t == nil {
		return ""
	}
	switch t.Type {
	case "enabled":
		return fmt.Sprintf("<thinking_mode>enabled</thinking_mode><max_thinking_length>%d</max_thinking_length>", effectiveBudget(t))
	case "adaptive":
		effort := "high"
		if req.OutputConfig != nil && req.OutputConfig.Effort != "" {
			effort = req.OutputConfig.Effort
		}
		return fmt.Sprintf("<thinking_mode>adaptive</thinking_mode><thinking_effort>%s</thinking_effort>", effort)
	}
	return ""
}

func effectiveBudget(t *Thinking) int {
	// 缺省或非正值(0/负数)一律回落到默认预算,避免产生 <max_thinking_length>-5</...> 之类无意义标签。
	if t.BudgetTokens == nil || *t.BudgetTokens <= 0 {
		return defaultBudgetTokens
	}
	if *t.BudgetTokens > maxBudgetTokens {
		return maxBudgetTokens
	}
	return *t.BudgetTokens
}

func hasThinkingTags(content string) bool {
	return strings.Contains(content, "<thinking_mode>") || strings.Contains(content, "<max_thinking_length>")
}

func parseSystemText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var arr []AnthropicSystemMessage
	if json.Unmarshal(raw, &arr) == nil {
		parts := make([]string, 0, len(arr))
		for _, m := range arr {
			parts = append(parts, m.Text)
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// buildHistory 构建 Kiro 历史消息:系统提示配对 + thinking 注入 + 逐轮 user/assistant 合并。
func buildHistory(req *AnthropicRequest, messages []AnthropicMessage, modelID string, toolNameMap map[string]string) ([]Message, error) {
	var history []Message
	thinkingPrefix := generateThinkingPrefix(req)

	systemContent := parseSystemText(req.System)
	if systemContent != "" {
		systemContent = systemContent + "\n" + systemChunkedPolicy
		finalContent := systemContent
		if thinkingPrefix != "" && !hasThinkingTags(systemContent) {
			finalContent = thinkingPrefix + "\n" + systemContent
		}
		history = append(history, historyUser(finalContent, modelID))
		history = append(history, historyAssistant("I will follow these instructions."))
	} else if thinkingPrefix != "" {
		history = append(history, historyUser(thinkingPrefix, modelID))
		history = append(history, historyAssistant("I will follow these instructions."))
	}

	historyEnd := len(messages) - 1 // 最后一条为 currentMessage
	if historyEnd < 0 {
		historyEnd = 0
	}

	var userBuf, asstBuf []AnthropicMessage
	for i := 0; i < historyEnd; i++ {
		msg := messages[i]
		switch msg.Role {
		case "user":
			if len(asstBuf) > 0 {
				merged, err := mergeAssistantMessages(asstBuf, toolNameMap)
				if err != nil {
					return nil, err
				}
				history = append(history, merged)
				asstBuf = nil
			}
			userBuf = append(userBuf, msg)
		case "assistant":
			if len(userBuf) > 0 {
				merged, err := mergeUserMessages(userBuf, modelID)
				if err != nil {
					return nil, err
				}
				history = append(history, merged)
				userBuf = nil
			}
			asstBuf = append(asstBuf, msg)
		}
	}
	if len(asstBuf) > 0 {
		merged, err := mergeAssistantMessages(asstBuf, toolNameMap)
		if err != nil {
			return nil, err
		}
		history = append(history, merged)
	}
	if len(userBuf) > 0 {
		merged, err := mergeUserMessages(userBuf, modelID)
		if err != nil {
			return nil, err
		}
		history = append(history, merged)
		history = append(history, historyAssistant("OK")) // 自动配对
	}
	return history, nil
}

func historyUser(content, modelID string) Message {
	return Message{UserInputMessage: &UserMessage{Content: content, ModelID: modelID, Origin: Origin}}
}

func historyAssistant(content string) Message {
	return Message{AssistantResponseMessage: &AssistantMessage{Content: content}}
}

func mergeUserMessages(msgs []AnthropicMessage, modelID string) (Message, error) {
	var contentParts []string
	var allImages []KiroImage
	var allToolResults []ToolResult
	for _, m := range msgs {
		text, images, toolResults, err := processMessageContent(m.Content)
		if err != nil {
			return Message{}, err
		}
		if text != "" {
			contentParts = append(contentParts, text)
		}
		allImages = append(allImages, images...)
		allToolResults = append(allToolResults, toolResults...)
	}
	um := &UserMessage{Content: strings.Join(contentParts, "\n"), ModelID: modelID, Origin: Origin}
	if len(allImages) > 0 {
		um.Images = allImages
	}
	if len(allToolResults) > 0 {
		um.UserInputMessageContext = &UserInputMessageContext{ToolResults: allToolResults}
	}
	return Message{UserInputMessage: um}, nil
}

func convertAssistantMessage(msg AnthropicMessage, toolNameMap map[string]string) (Message, error) {
	var thinkingContent, textContent strings.Builder
	var toolUses []ToolUseEntry

	var s string
	if json.Unmarshal(msg.Content, &s) == nil {
		_, _ = textContent.WriteString(s)
	} else {
		var arr []json.RawMessage
		if json.Unmarshal(msg.Content, &arr) == nil {
			for _, item := range arr {
				var block AnthropicContentBlock
				if json.Unmarshal(item, &block) != nil {
					continue
				}
				switch block.Type {
				case "thinking":
					if block.Thinking != nil {
						_, _ = thinkingContent.WriteString(*block.Thinking)
					}
				case "text":
					if block.Text != nil {
						_, _ = textContent.WriteString(*block.Text)
					}
				case "tool_use":
					if block.ID != nil && block.Name != nil {
						input := block.Input
						if len(input) == 0 || string(input) == "null" {
							input = json.RawMessage(`{}`)
						}
						toolUses = append(toolUses, ToolUseEntry{
							ToolUseID: *block.ID,
							Name:      mapToolName(*block.Name, toolNameMap),
							Input:     input,
						})
					}
				}
			}
		}
	}

	tc := thinkingContent.String()
	txt := textContent.String()
	var finalContent string
	switch {
	case tc != "":
		if txt != "" {
			finalContent = "<thinking>" + tc + "</thinking>\n\n" + txt
		} else {
			finalContent = "<thinking>" + tc + "</thinking>"
		}
	case txt == "" && len(toolUses) > 0:
		finalContent = " " // Kiro 要求 content 非空
	default:
		finalContent = txt
	}

	am := &AssistantMessage{Content: finalContent}
	if len(toolUses) > 0 {
		am.ToolUses = toolUses
	}
	return Message{AssistantResponseMessage: am}, nil
}

// mergeAssistantMessages 合并连续多条 assistant 消息为一条(应对网络抖动产生的连续 assistant)。
func mergeAssistantMessages(msgs []AnthropicMessage, toolNameMap map[string]string) (Message, error) {
	if len(msgs) == 1 {
		return convertAssistantMessage(msgs[0], toolNameMap)
	}
	var allToolUses []ToolUseEntry
	var contentParts []string
	for _, m := range msgs {
		conv, err := convertAssistantMessage(m, toolNameMap)
		if err != nil {
			return Message{}, err
		}
		am := conv.AssistantResponseMessage
		if strings.TrimSpace(am.Content) != "" {
			contentParts = append(contentParts, am.Content)
		}
		allToolUses = append(allToolUses, am.ToolUses...)
	}
	content := strings.Join(contentParts, "\n\n")
	if content == "" && len(allToolUses) > 0 {
		content = " "
	}
	am := &AssistantMessage{Content: content}
	if len(allToolUses) > 0 {
		am.ToolUses = allToolUses
	}
	return Message{AssistantResponseMessage: am}, nil
}

// validateToolPairing 过滤当前消息中孤立/重复的 tool_result,并返回历史中孤立的 tool_use_id。
func validateToolPairing(history []Message, toolResults []ToolResult) ([]ToolResult, map[string]bool) {
	allToolUseIDs := map[string]bool{}
	historyToolResultIDs := map[string]bool{}
	for _, msg := range history {
		if msg.AssistantResponseMessage != nil {
			for _, tu := range msg.AssistantResponseMessage.ToolUses {
				allToolUseIDs[tu.ToolUseID] = true
			}
		}
		if msg.UserInputMessage != nil && msg.UserInputMessage.UserInputMessageContext != nil {
			for _, tr := range msg.UserInputMessage.UserInputMessageContext.ToolResults {
				historyToolResultIDs[tr.ToolUseID] = true
			}
		}
	}
	unpaired := map[string]bool{}
	for id := range allToolUseIDs {
		if !historyToolResultIDs[id] {
			unpaired[id] = true
		}
	}
	var filtered []ToolResult
	for _, tr := range toolResults {
		switch {
		case unpaired[tr.ToolUseID]:
			filtered = append(filtered, tr)
			delete(unpaired, tr.ToolUseID)
		case allToolUseIDs[tr.ToolUseID]:
			// 已在历史配对过,重复的 tool_result,跳过
		default:
			// 孤立 tool_result,跳过
		}
	}
	return filtered, unpaired // unpaired 剩余项即孤立的 tool_use
}

// removeOrphanedToolUses 从历史 assistant 消息中移除没有对应 tool_result 的 tool_use。
func removeOrphanedToolUses(history []Message, orphaned map[string]bool) {
	if len(orphaned) == 0 {
		return
	}
	for i := range history {
		am := history[i].AssistantResponseMessage
		if am == nil || len(am.ToolUses) == 0 {
			continue
		}
		kept := am.ToolUses[:0:0]
		for _, tu := range am.ToolUses {
			if !orphaned[tu.ToolUseID] {
				kept = append(kept, tu)
			}
		}
		if len(kept) == 0 {
			am.ToolUses = nil
		} else {
			am.ToolUses = kept
		}
	}
}

// collectHistoryToolNames 收集历史 assistant 消息中用到的工具名(保持出现顺序、去重)。
func collectHistoryToolNames(history []Message) []string {
	var names []string
	seen := map[string]bool{}
	for _, msg := range history {
		if msg.AssistantResponseMessage == nil {
			continue
		}
		for _, tu := range msg.AssistantResponseMessage.ToolUses {
			if !seen[tu.Name] {
				seen[tu.Name] = true
				names = append(names, tu.Name)
			}
		}
	}
	return names
}

func placeholderTool(name string) Tool {
	return Tool{ToolSpecification: ToolSpecification{
		Name:        name,
		Description: "Tool used in conversation history",
		InputSchema: InputSchema{JSON: json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{},"required":[],"additionalProperties":true}`)},
	}}
}

// extractSessionID 从 metadata.user_id 提取 session UUID(支持 JSON 与 "session_<uuid>" 两种格式)。
func extractSessionID(userID string) (string, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(userID), &obj) == nil {
		if raw, ok := obj["session_id"]; ok {
			var sid string
			if json.Unmarshal(raw, &sid) == nil && isValidUUID(sid) {
				return sid, true
			}
		}
	}
	if idx := strings.Index(userID, "session_"); idx >= 0 {
		part := userID[idx+len("session_"):]
		if len(part) >= 36 {
			uuidStr := part[:36]
			if isValidUUID(uuidStr) {
				return uuidStr, true
			}
		}
	}
	return "", false
}

// isValidUUID 简单校验:36 字符且含 4 个连字符。
func isValidUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	count := 0
	for _, c := range s {
		if c == '-' {
			count++
		}
	}
	return count == 4
}
