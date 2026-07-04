package kiro

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 本文件把 Kiro 事件序列转换为 Anthropic Messages SSE 事件流,
// 忠实移植 kiro.rs 的 src/anthropic/stream.rs:SSE 状态机 + thinking 标签流式拆分。

// SSEEvent 是一条 Anthropic SSE 事件。
type SSEEvent struct {
	Event string
	Data  map[string]any
}

// String 返回 SSE 线格式:"event: <e>\ndata: <json>\n\n"。
func (e SSEEvent) String() string {
	return "event: " + e.Event + "\ndata: " + marshalCompact(e.Data) + "\n\n"
}

func marshalCompact(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// ==================== thinking 标签检测(引用字符过滤) ====================

// quoteChars 是包裹 thinking 标签时视为"引用"而非真实标签的字符集合。
var quoteChars = func() [256]bool {
	var m [256]bool
	for _, c := range []byte("`\"'\\#!@$%^&*()-_=+[]{};:<>,.?/") {
		m[c] = true
	}
	return m
}()

func isQuoteChar(buffer string, pos int) bool {
	if pos < 0 || pos >= len(buffer) {
		return false
	}
	return quoteChars[buffer[pos]]
}

// findCharBoundary 返回 <= target 的最近 UTF-8 字符边界,避免切断多字节字符。
func findCharBoundary(s string, target int) int {
	if target >= len(s) {
		return len(s)
	}
	if target <= 0 {
		return 0
	}
	pos := target
	for pos > 0 && !utf8.RuneStart(s[pos]) {
		pos--
	}
	return pos
}

// findRealThinkingStartTag 返回未被引用字符包裹的 <thinking> 位置,无则 -1。
func findRealThinkingStartTag(buffer string) int {
	const tag = "<thinking>"
	searchStart := 0
	for {
		rel := strings.Index(buffer[searchStart:], tag)
		if rel < 0 {
			return -1
		}
		abs := searchStart + rel
		hasQuoteBefore := abs > 0 && isQuoteChar(buffer, abs-1)
		hasQuoteAfter := isQuoteChar(buffer, abs+len(tag))
		if !hasQuoteBefore && !hasQuoteAfter {
			return abs
		}
		searchStart = abs + 1
	}
}

// findRealThinkingEndTag 返回未被引用、且后接 "\n\n" 的 </thinking> 位置,无则 -1。
func findRealThinkingEndTag(buffer string) int {
	const tag = "</thinking>"
	searchStart := 0
	for {
		rel := strings.Index(buffer[searchStart:], tag)
		if rel < 0 {
			return -1
		}
		abs := searchStart + rel
		afterPos := abs + len(tag)
		if (abs > 0 && isQuoteChar(buffer, abs-1)) || isQuoteChar(buffer, afterPos) {
			searchStart = abs + 1
			continue
		}
		after := buffer[afterPos:]
		if len(after) < 2 {
			return -1 // 等待更多内容
		}
		if strings.HasPrefix(after, "\n\n") {
			return abs
		}
		searchStart = abs + 1
	}
}

// findRealThinkingEndTagAtBufferEnd 用于边界场景(结束标签后仅剩空白):返回位置,无则 -1。
func findRealThinkingEndTagAtBufferEnd(buffer string) int {
	const tag = "</thinking>"
	searchStart := 0
	for {
		rel := strings.Index(buffer[searchStart:], tag)
		if rel < 0 {
			return -1
		}
		abs := searchStart + rel
		afterPos := abs + len(tag)
		if (abs > 0 && isQuoteChar(buffer, abs-1)) || isQuoteChar(buffer, afterPos) {
			searchStart = abs + 1
			continue
		}
		if strings.TrimSpace(buffer[afterPos:]) == "" {
			return abs
		}
		searchStart = abs + 1
	}
}

// ExtractThinkingFromCompleteText 从完整文本中提取 thinking 块(非流式响应用),
// 返回 (thinking 内容, 是否检测到, 剩余文本)。
func ExtractThinkingFromCompleteText(text string) (string, bool, string) {
	startPos := findRealThinkingStartTag(text)
	if startPos < 0 {
		return "", false, text
	}
	before := text[:startPos]
	afterOpen := text[startPos+len("<thinking>"):]

	var thinkingRaw, textAfter string
	if endPos := findRealThinkingEndTag(afterOpen); endPos >= 0 {
		thinkingRaw = afterOpen[:endPos]
		textAfter = afterOpen[endPos+len("</thinking>\n\n"):]
	} else if endPos := findRealThinkingEndTagAtBufferEnd(afterOpen); endPos >= 0 {
		thinkingRaw = afterOpen[:endPos]
		textAfter = strings.TrimLeftFunc(afterOpen[endPos+len("</thinking>"):], unicode.IsSpace)
	} else {
		return "", false, text
	}

	thinkingContent := strings.TrimPrefix(thinkingRaw, "\n")
	var remaining strings.Builder
	if strings.TrimSpace(before) != "" {
		_, _ = remaining.WriteString(before)
	}
	_, _ = remaining.WriteString(textAfter)
	if thinkingContent == "" {
		return "", false, remaining.String()
	}
	return thinkingContent, true, remaining.String()
}

// ==================== SSE 状态机 ====================

type blockState struct {
	blockType string
	started   bool
	stopped   bool
}

// sseStateManager 保证 SSE 事件序列符合 Anthropic 规范(message_start 唯一、块先 start 后 delta 再 stop 等)。
type sseStateManager struct {
	messageStarted   bool
	messageDeltaSent bool
	messageEnded     bool
	activeBlocks     map[int]*blockState
	nextBlockIndex   int
	stopReason       string
	hasStopReason    bool
	hasToolUse       bool
}

func newSSEStateManager() *sseStateManager {
	return &sseStateManager{activeBlocks: map[int]*blockState{}}
}

func (m *sseStateManager) isBlockOpenOfType(index int, expected string) bool {
	b, ok := m.activeBlocks[index]
	return ok && b.started && !b.stopped && b.blockType == expected
}

func (m *sseStateManager) allocBlockIndex() int {
	i := m.nextBlockIndex
	m.nextBlockIndex++
	return i
}

func (m *sseStateManager) setStopReason(r string) { m.stopReason = r; m.hasStopReason = true }

func (m *sseStateManager) hasNonThinkingBlocks() bool {
	for _, b := range m.activeBlocks {
		if b.blockType != "thinking" {
			return true
		}
	}
	return false
}

func (m *sseStateManager) getStopReason() string {
	switch {
	case m.hasStopReason:
		return m.stopReason
	case m.hasToolUse:
		return "tool_use"
	default:
		return "end_turn"
	}
}

func (m *sseStateManager) handleMessageStart(data map[string]any) (SSEEvent, bool) {
	if m.messageStarted {
		return SSEEvent{}, false
	}
	m.messageStarted = true
	return SSEEvent{Event: "message_start", Data: data}, true
}

func (m *sseStateManager) handleContentBlockStart(index int, blockType string, data map[string]any) []SSEEvent {
	var events []SSEEvent
	// tool_use 开始前,先关闭仍打开的文本块(按索引升序,保证 SSE 顺序确定且符合 Anthropic 规范)。
	if blockType == "tool_use" {
		m.hasToolUse = true
		for _, bi := range m.sortedBlockIndices() {
			b := m.activeBlocks[bi]
			if b.blockType == "text" && b.started && !b.stopped {
				events = append(events, contentBlockStop(bi))
				b.stopped = true
			}
		}
	}
	if b, ok := m.activeBlocks[index]; ok {
		if b.started {
			return events
		}
		b.started = true
	} else {
		m.activeBlocks[index] = &blockState{blockType: blockType, started: true}
	}
	events = append(events, SSEEvent{Event: "content_block_start", Data: data})
	return events
}

func (m *sseStateManager) handleContentBlockDelta(index int, data map[string]any) (SSEEvent, bool) {
	b, ok := m.activeBlocks[index]
	if !ok || !b.started || b.stopped {
		return SSEEvent{}, false
	}
	return SSEEvent{Event: "content_block_delta", Data: data}, true
}

func (m *sseStateManager) handleContentBlockStop(index int) (SSEEvent, bool) {
	b, ok := m.activeBlocks[index]
	if !ok || b.stopped {
		return SSEEvent{}, false
	}
	b.stopped = true
	return contentBlockStop(index), true
}

func (m *sseStateManager) sortedBlockIndices() []int {
	indices := make([]int, 0, len(m.activeBlocks))
	for index := range m.activeBlocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	return indices
}

func (m *sseStateManager) generateFinalEvents(usage map[string]any) []SSEEvent {
	var events []SSEEvent
	for _, index := range m.sortedBlockIndices() {
		b := m.activeBlocks[index]
		if b.started && !b.stopped {
			events = append(events, contentBlockStop(index))
			b.stopped = true
		}
	}
	if !m.messageDeltaSent {
		m.messageDeltaSent = true
		events = append(events, SSEEvent{Event: "message_delta", Data: map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": m.getStopReason(), "stop_sequence": nil},
			"usage": usage,
		}})
	}
	if !m.messageEnded {
		m.messageEnded = true
		events = append(events, SSEEvent{Event: "message_stop", Data: map[string]any{"type": "message_stop"}})
	}
	return events
}

func contentBlockStop(index int) SSEEvent {
	return SSEEvent{Event: "content_block_stop", Data: map[string]any{"type": "content_block_stop", "index": index}}
}

// ==================== StreamContext ====================

// StreamContext 把 Kiro 事件转换为 Anthropic SSE 事件,管理消息与内容块生命周期。
type StreamContext struct {
	state       *sseStateManager
	Model       string
	MessageID   string
	InputTokens int

	contextInputTokens int
	hasContextTokens   bool
	OutputTokens       int
	CreditUsage        float64 // 累加 meteringEvent.usage:本次响应消耗的 credit 总量(Kiro 真实计费口径)
	CreditUnit         string  // meteringEvent.unit(通常 "credit"),随 CreditUsage 透传给客户端

	// 合成提示词缓存计数(由网关经 CacheTracker 算出后 SetCache 注入),注入客户端 usage。
	cacheRead       int
	cacheCreation   int
	cacheCreation5m int
	cacheCreation1h int

	toolBlockIndices map[string]int
	toolNameMap      map[string]string // 短名 → 原名,响应时还原

	thinkingEnabled             bool
	thinkingBuffer              string
	inThinkingBlock             bool
	thinkingExtracted           bool
	thinkingBlockIndex          int
	hasThinkingBlock            bool
	textBlockIndex              int
	hasTextBlock                bool
	stripThinkingLeadingNewline bool
}

// NewStreamContext 创建流处理上下文。toolNameMap 为 nil 时按空处理。
func NewStreamContext(model string, inputTokens int, thinkingEnabled bool, toolNameMap map[string]string) *StreamContext {
	if toolNameMap == nil {
		toolNameMap = map[string]string{}
	}
	return &StreamContext{
		state:            newSSEStateManager(),
		Model:            model,
		MessageID:        "msg_" + strings.ReplaceAll(newUUID(), "-", ""),
		InputTokens:      inputTokens,
		toolBlockIndices: map[string]int{},
		toolNameMap:      toolNameMap,
		thinkingEnabled:  thinkingEnabled,
	}
}

// SetCache 注入本次请求的合成缓存计数(网关经 CacheTracker 算出),影响客户端 usage:
// message_start / message_delta 会带上 cache_read/creation,且 input_tokens 扣成非缓存部分。
func (c *StreamContext) SetCache(r CacheResult) {
	c.cacheRead = r.CacheReadInputTokens
	c.cacheCreation = r.CacheCreationInputTokens
	c.cacheCreation5m = r.CacheCreation5mTokens
	c.cacheCreation1h = r.CacheCreation1hTokens
}

// usageWithCache 构建 usage 对象:input_tokens 扣掉缓存部分(互斥,避免重复计费),
// 有缓存时附带 cache_read/creation 及嵌套 cache_creation 5m/1h 明细。
// 合成缓存按 estimate 前缀算出,这里按本次上报的真实 total(inputTokens)夹一次,
// 保证 read+creation ≤ total、input 不被夹成负(与计费口径一致)。
func (c *StreamContext) usageWithCache(inputTokens, outputTokens int) map[string]any {
	cr := CacheResult{
		CacheReadInputTokens:     c.cacheRead,
		CacheCreationInputTokens: c.cacheCreation,
		CacheCreation5mTokens:    c.cacheCreation5m,
		CacheCreation1hTokens:    c.cacheCreation1h,
	}.CapTo(inputTokens)
	input := inputTokens - cr.CacheReadInputTokens - cr.CacheCreationInputTokens
	if input < 0 {
		input = 0
	}
	usage := map[string]any{"input_tokens": input, "output_tokens": outputTokens}
	if cr.CacheReadInputTokens > 0 || cr.CacheCreationInputTokens > 0 {
		usage["cache_read_input_tokens"] = cr.CacheReadInputTokens
		usage["cache_creation_input_tokens"] = cr.CacheCreationInputTokens
		usage["cache_creation"] = map[string]any{
			"ephemeral_5m_input_tokens": cr.CacheCreation5mTokens,
			"ephemeral_1h_input_tokens": cr.CacheCreation1hTokens,
		}
	}
	return usage
}

func (c *StreamContext) createMessageStartEvent() map[string]any {
	return map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            c.MessageID,
			"type":          "message",
			"role":          "assistant",
			"content":       []any{},
			"model":         c.Model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         c.usageWithCache(c.InputTokens, 1),
		},
	}
}

// GenerateInitialEvents 产出 message_start(启用 thinking 时不预建文本块,保证块顺序)。
func (c *StreamContext) GenerateInitialEvents() []SSEEvent {
	var events []SSEEvent
	if ev, ok := c.state.handleMessageStart(c.createMessageStartEvent()); ok {
		events = append(events, ev)
	}
	if c.thinkingEnabled {
		return events
	}
	idx := c.state.allocBlockIndex()
	c.textBlockIndex = idx
	c.hasTextBlock = true
	events = append(events, c.state.handleContentBlockStart(idx, "text", map[string]any{
		"type":          "content_block_start",
		"index":         idx,
		"content_block": map[string]any{"type": "text", "text": ""},
	})...)
	return events
}

// ProcessKiroEvent 处理单个 Kiro 事件并返回对应的 SSE 事件序列。
func (c *StreamContext) ProcessKiroEvent(ev Event) []SSEEvent {
	switch ev.Kind {
	case EventAssistantResponse:
		return c.processAssistantResponse(ev.Content)
	case EventToolUse:
		return c.processToolUse(ev.ToolUse)
	case EventContextUsage:
		window := ContextWindowSize(c.Model)
		c.contextInputTokens = int(ev.ContextUsagePercentage * float64(window) / 100.0)
		c.hasContextTokens = true
		if ev.ContextUsagePercentage >= 100.0 {
			c.state.setStopReason("model_context_window_exceeded")
		}
		return nil
	case EventException:
		if ev.ExceptionType == "ContentLengthExceededException" {
			c.state.setStopReason("max_tokens")
		}
		return nil
	case EventMetering:
		// meteringEvent 携带本次请求的 credit 消耗(不产生 SSE 内容);累加供上层观测/计费。
		c.CreditUsage += ev.MeteringUsage
		if ev.MeteringUnit != "" {
			c.CreditUnit = ev.MeteringUnit
		}
		return nil
	default:
		return nil
	}
}

func (c *StreamContext) processAssistantResponse(content string) []SSEEvent {
	if content == "" {
		return nil
	}
	c.OutputTokens += estimateTokens(content)
	if c.thinkingEnabled {
		return c.processContentWithThinking(content)
	}
	return c.createTextDeltaEvents(content)
}

func (c *StreamContext) processContentWithThinking(content string) []SSEEvent {
	var events []SSEEvent
	c.thinkingBuffer += content

	for {
		switch {
		case !c.inThinkingBlock && !c.thinkingExtracted:
			startPos := findRealThinkingStartTag(c.thinkingBuffer)
			if startPos < 0 {
				// 未见 <thinking>,保留可能是部分标签的尾部,其余非空白输出为文本。
				target := len(c.thinkingBuffer) - len("<thinking>")
				if target < 0 {
					target = 0
				}
				safeLen := findCharBoundary(c.thinkingBuffer, target)
				if safeLen > 0 {
					safe := c.thinkingBuffer[:safeLen]
					if safe != "" && strings.TrimSpace(safe) != "" {
						events = append(events, c.createTextDeltaEvents(safe)...)
						c.thinkingBuffer = c.thinkingBuffer[safeLen:]
					}
				}
				return events
			}
			before := c.thinkingBuffer[:startPos]
			if before != "" && strings.TrimSpace(before) != "" {
				events = append(events, c.createTextDeltaEvents(before)...)
			}
			c.inThinkingBlock = true
			c.stripThinkingLeadingNewline = true
			c.thinkingBuffer = c.thinkingBuffer[startPos+len("<thinking>"):]
			idx := c.state.allocBlockIndex()
			c.thinkingBlockIndex = idx
			c.hasThinkingBlock = true
			events = append(events, c.state.handleContentBlockStart(idx, "thinking", map[string]any{
				"type":          "content_block_start",
				"index":         idx,
				"content_block": map[string]any{"type": "thinking", "thinking": ""},
			})...)

		case c.inThinkingBlock:
			if c.stripThinkingLeadingNewline {
				if strings.HasPrefix(c.thinkingBuffer, "\n") {
					c.thinkingBuffer = c.thinkingBuffer[1:]
					c.stripThinkingLeadingNewline = false
				} else if c.thinkingBuffer != "" {
					c.stripThinkingLeadingNewline = false
				}
			}
			endPos := findRealThinkingEndTag(c.thinkingBuffer)
			if endPos < 0 {
				// 未见结束标签:输出安全内容,保留末尾 "</thinking>\n\n" 长度以防拆分。
				target := len(c.thinkingBuffer) - len("</thinking>\n\n")
				if target < 0 {
					target = 0
				}
				safeLen := findCharBoundary(c.thinkingBuffer, target)
				if safeLen > 0 {
					safe := c.thinkingBuffer[:safeLen]
					if safe != "" && c.hasThinkingBlock {
						events = append(events, c.thinkingDeltaEvent(c.thinkingBlockIndex, safe))
					}
					c.thinkingBuffer = c.thinkingBuffer[safeLen:]
				}
				return events
			}
			thinkingContent := c.thinkingBuffer[:endPos]
			if thinkingContent != "" && c.hasThinkingBlock {
				events = append(events, c.thinkingDeltaEvent(c.thinkingBlockIndex, thinkingContent))
			}
			c.inThinkingBlock = false
			c.thinkingExtracted = true
			if c.hasThinkingBlock {
				events = append(events, c.thinkingDeltaEvent(c.thinkingBlockIndex, ""))
				if ev, ok := c.state.handleContentBlockStop(c.thinkingBlockIndex); ok {
					events = append(events, ev)
				}
			}
			c.thinkingBuffer = c.thinkingBuffer[endPos+len("</thinking>\n\n"):]

		default: // thinking 已提取,剩余作为文本
			if c.thinkingBuffer != "" {
				remaining := c.thinkingBuffer
				c.thinkingBuffer = ""
				events = append(events, c.createTextDeltaEvents(remaining)...)
			}
			return events
		}
	}
}

// createTextDeltaEvents 发送 text_delta;若当前文本块已被关闭(如 tool_use 触发),自动重建新文本块。
func (c *StreamContext) createTextDeltaEvents(text string) []SSEEvent {
	var events []SSEEvent
	if c.hasTextBlock && !c.state.isBlockOpenOfType(c.textBlockIndex, "text") {
		c.hasTextBlock = false
	}
	var textIndex int
	if c.hasTextBlock {
		textIndex = c.textBlockIndex
	} else {
		idx := c.state.allocBlockIndex()
		c.textBlockIndex = idx
		c.hasTextBlock = true
		events = append(events, c.state.handleContentBlockStart(idx, "text", map[string]any{
			"type":          "content_block_start",
			"index":         idx,
			"content_block": map[string]any{"type": "text", "text": ""},
		})...)
		textIndex = idx
	}
	if ev, ok := c.state.handleContentBlockDelta(textIndex, map[string]any{
		"type":  "content_block_delta",
		"index": textIndex,
		"delta": map[string]any{"type": "text_delta", "text": text},
	}); ok {
		events = append(events, ev)
	}
	return events
}

func (c *StreamContext) thinkingDeltaEvent(index int, thinking string) SSEEvent {
	return SSEEvent{Event: "content_block_delta", Data: map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]any{"type": "thinking_delta", "thinking": thinking},
	}}
}

func (c *StreamContext) processToolUse(tu ToolUseEvent) []SSEEvent {
	var events []SSEEvent
	c.state.hasToolUse = true

	// 边界场景:thinking 结束标签紧跟 tool_use(无 \n\n),先识别并过滤结束标签。
	if c.thinkingEnabled && c.inThinkingBlock {
		if endPos := findRealThinkingEndTagAtBufferEnd(c.thinkingBuffer); endPos >= 0 {
			thinkingContent := c.thinkingBuffer[:endPos]
			if thinkingContent != "" && c.hasThinkingBlock {
				events = append(events, c.thinkingDeltaEvent(c.thinkingBlockIndex, thinkingContent))
			}
			c.inThinkingBlock = false
			c.thinkingExtracted = true
			if c.hasThinkingBlock {
				events = append(events, c.thinkingDeltaEvent(c.thinkingBlockIndex, ""))
				if ev, ok := c.state.handleContentBlockStop(c.thinkingBlockIndex); ok {
					events = append(events, ev)
				}
			}
			remaining := strings.TrimLeftFunc(c.thinkingBuffer[endPos+len("</thinking>"):], unicode.IsSpace)
			c.thinkingBuffer = ""
			if remaining != "" {
				events = append(events, c.createTextDeltaEvents(remaining)...)
			}
		}
	}

	// thinking 模式下探测标签而暂存的短文本,需在开 tool_use 块前 flush,避免被"吞字"。
	if c.thinkingEnabled && !c.inThinkingBlock && !c.thinkingExtracted && c.thinkingBuffer != "" {
		buffered := c.thinkingBuffer
		c.thinkingBuffer = ""
		events = append(events, c.createTextDeltaEvents(buffered)...)
	}

	blockIndex, ok := c.toolBlockIndices[tu.ToolUseID]
	if !ok {
		blockIndex = c.state.allocBlockIndex()
		c.toolBlockIndices[tu.ToolUseID] = blockIndex
	}

	originalName := tu.Name
	if orig, ok := c.toolNameMap[tu.Name]; ok {
		originalName = orig
	}

	events = append(events, c.state.handleContentBlockStart(blockIndex, "tool_use", map[string]any{
		"type":  "content_block_start",
		"index": blockIndex,
		"content_block": map[string]any{
			"type": "tool_use", "id": tu.ToolUseID, "name": originalName, "input": map[string]any{},
		},
	})...)

	if tu.Input != "" {
		c.OutputTokens += (len(tu.Input) + 3) / 4
		if ev, ok := c.state.handleContentBlockDelta(blockIndex, map[string]any{
			"type":  "content_block_delta",
			"index": blockIndex,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": tu.Input},
		}); ok {
			events = append(events, ev)
		}
	}
	if tu.Stop {
		if ev, ok := c.state.handleContentBlockStop(blockIndex); ok {
			events = append(events, ev)
		}
	}
	return events
}

// FinalInputTokens 返回最终 input_tokens:优先取 contextUsageEvent 计算值,否则用初始估算值。
func (c *StreamContext) FinalInputTokens() int {
	if c.hasContextTokens {
		return c.contextInputTokens
	}
	return c.InputTokens
}

// GenerateFinalEvents 冲刷残留缓冲并产出 message_delta / message_stop。
func (c *StreamContext) GenerateFinalEvents() []SSEEvent {
	var events []SSEEvent

	if c.thinkingEnabled && c.thinkingBuffer != "" {
		if c.inThinkingBlock {
			if endPos := findRealThinkingEndTagAtBufferEnd(c.thinkingBuffer); endPos >= 0 {
				thinkingContent := c.thinkingBuffer[:endPos]
				if thinkingContent != "" && c.hasThinkingBlock {
					events = append(events, c.thinkingDeltaEvent(c.thinkingBlockIndex, thinkingContent))
				}
				if c.hasThinkingBlock {
					events = append(events, c.thinkingDeltaEvent(c.thinkingBlockIndex, ""))
					if ev, ok := c.state.handleContentBlockStop(c.thinkingBlockIndex); ok {
						events = append(events, ev)
					}
				}
				remaining := strings.TrimLeftFunc(c.thinkingBuffer[endPos+len("</thinking>"):], unicode.IsSpace)
				c.inThinkingBlock = false
				c.thinkingExtracted = true
				if remaining != "" {
					events = append(events, c.createTextDeltaEvents(remaining)...)
				}
			} else if c.hasThinkingBlock {
				events = append(events, c.thinkingDeltaEvent(c.thinkingBlockIndex, c.thinkingBuffer))
				events = append(events, c.thinkingDeltaEvent(c.thinkingBlockIndex, ""))
				if ev, ok := c.state.handleContentBlockStop(c.thinkingBlockIndex); ok {
					events = append(events, ev)
				}
			}
		} else {
			events = append(events, c.createTextDeltaEvents(c.thinkingBuffer)...)
		}
		c.thinkingBuffer = ""
	}

	// 全程只有 thinking、无 text/tool_use:补一个空格文本块,stop_reason=max_tokens。
	if c.thinkingEnabled && c.hasThinkingBlock && !c.state.hasNonThinkingBlocks() {
		c.state.setStopReason("max_tokens")
		events = append(events, c.createTextDeltaEvents(" ")...)
	}

	finalInputTokens := c.InputTokens
	if c.hasContextTokens {
		finalInputTokens = c.contextInputTokens
	}
	usage := c.usageWithCache(finalInputTokens, c.OutputTokens)
	// Kiro 真实成本口径:把 meteringEvent 的 credit 透传给客户端(与合成缓存双轨并存)。
	if c.CreditUsage > 0 {
		usage["credit_usage"] = c.CreditUsage
		if c.CreditUnit != "" {
			usage["credit_unit"] = c.CreditUnit
		}
	}
	events = append(events, c.state.generateFinalEvents(usage)...)
	return events
}

// ==================== BufferedStreamContext ====================

// BufferedStreamContext 缓冲全部事件,待流结束后用 contextUsageEvent 计算的 input_tokens 回填 message_start。
type BufferedStreamContext struct {
	inner                *StreamContext
	buffer               []SSEEvent
	estimatedInputTokens int
	initialGenerated     bool
}

// NewBufferedStreamContext 创建缓冲式流上下文。
func NewBufferedStreamContext(model string, estimatedInputTokens int, thinkingEnabled bool, toolNameMap map[string]string) *BufferedStreamContext {
	return &BufferedStreamContext{
		inner:                NewStreamContext(model, estimatedInputTokens, thinkingEnabled, toolNameMap),
		estimatedInputTokens: estimatedInputTokens,
	}
}

// ProcessAndBuffer 处理一个事件并缓冲其 SSE 输出。
func (b *BufferedStreamContext) ProcessAndBuffer(ev Event) {
	if !b.initialGenerated {
		b.buffer = append(b.buffer, b.inner.GenerateInitialEvents()...)
		b.initialGenerated = true
	}
	b.buffer = append(b.buffer, b.inner.ProcessKiroEvent(ev)...)
}

// FinishAndGetAllEvents 收尾并返回全部事件,同时用最终 input_tokens 回填 message_start。
func (b *BufferedStreamContext) FinishAndGetAllEvents() []SSEEvent {
	if !b.initialGenerated {
		b.buffer = append(b.buffer, b.inner.GenerateInitialEvents()...)
		b.initialGenerated = true
	}
	b.buffer = append(b.buffer, b.inner.GenerateFinalEvents()...)

	finalInputTokens := b.estimatedInputTokens
	if b.inner.hasContextTokens {
		finalInputTokens = b.inner.contextInputTokens
	}
	for i := range b.buffer {
		if b.buffer[i].Event != "message_start" {
			continue
		}
		if msg, ok := b.buffer[i].Data["message"].(map[string]any); ok {
			if usage, ok := msg["usage"].(map[string]any); ok {
				usage["input_tokens"] = finalInputTokens
			}
		}
	}
	out := b.buffer
	b.buffer = nil
	return out
}

// estimateTokens 粗略估算 token 数(中文约 1.5 字符/token,其他约 4 字符/token)。
func estimateTokens(text string) int {
	chineseCount, otherCount := 0, 0
	for _, r := range text {
		if r >= '一' && r <= '鿿' { // CJK 统一表意文字
			chineseCount++
		} else {
			otherCount++
		}
	}
	chineseTokens := (chineseCount*2 + 2) / 3
	otherTokens := (otherCount + 3) / 4
	if t := chineseTokens + otherTokens; t > 1 {
		return t
	}
	return 1
}
