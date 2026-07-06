package kiro

import (
	"encoding/json"
	"strings"
)

// AssembleMessage 把一次请求产生的 SSE 事件序列(message_start / content_block_* /
// message_delta)拼装成一条完整的 Anthropic Messages 响应对象,用于非流式响应。
func AssembleMessage(events []SSEEvent) map[string]any {
	type block struct {
		data      map[string]any // content_block 对象
		isTool    bool
		toolInput strings.Builder // tool_use 的 input_json_delta 累积
	}

	var message map[string]any
	blocks := map[int]*block{}
	var order []int
	var stopReason, stopSequence any
	var inputTokens any
	var creditUsage, creditUnit any
	var cacheReadTokens, cacheCreationTokens, cacheCreationDetail any
	var haveDeltaUsage bool
	outputTokens := 0

	for _, e := range events {
		switch e.Event {
		case "message_start":
			if m, ok := e.Data["message"].(map[string]any); ok {
				message = cloneJSONMap(m)
			}
		case "content_block_start":
			idx := eventIndex(e.Data["index"])
			cb, _ := e.Data["content_block"].(map[string]any)
			b := &block{data: cloneJSONMap(cb)}
			if t, _ := b.data["type"].(string); t == "tool_use" {
				b.isTool = true
				b.data["input"] = map[string]any{}
			}
			blocks[idx] = b
			order = append(order, idx)
		case "content_block_delta":
			b := blocks[eventIndex(e.Data["index"])]
			if b == nil {
				continue
			}
			d, _ := e.Data["delta"].(map[string]any)
			switch d["type"] {
			case "text_delta":
				if s, ok := d["text"].(string); ok {
					b.data["text"] = asString(b.data["text"]) + s
				}
			case "thinking_delta":
				if s, ok := d["thinking"].(string); ok {
					b.data["thinking"] = asString(b.data["thinking"]) + s
				}
			case "input_json_delta":
				if s, ok := d["partial_json"].(string); ok {
					_, _ = b.toolInput.WriteString(s)
				}
			}
		case "content_block_stop":
			b := blocks[eventIndex(e.Data["index"])]
			if b != nil && b.isTool {
				raw := b.toolInput.String()
				if strings.TrimSpace(raw) == "" {
					raw = "{}"
				}
				var parsed any
				if json.Unmarshal([]byte(raw), &parsed) == nil {
					b.data["input"] = parsed
				}
			}
		case "message_delta":
			if d, ok := e.Data["delta"].(map[string]any); ok {
				stopReason = d["stop_reason"]
				stopSequence = d["stop_sequence"]
			}
			if u, ok := e.Data["usage"].(map[string]any); ok {
				haveDeltaUsage = true
				if v, ok := u["input_tokens"]; ok {
					inputTokens = v
				}
				outputTokens = eventIndex(u["output_tokens"])
				if v, ok := u["credit_usage"]; ok {
					creditUsage = v
				}
				if v, ok := u["credit_unit"]; ok {
					creditUnit = v
				}
				// 缓存计数以 message_delta 为权威口径(按真实 total reconcile);缺失记为 nil 以便覆盖。
				cacheReadTokens = u["cache_read_input_tokens"]
				cacheCreationTokens = u["cache_creation_input_tokens"]
				cacheCreationDetail = u["cache_creation"]
			}
		}
	}

	if message == nil {
		message = map[string]any{"type": "message", "role": "assistant"}
	}

	content := make([]any, 0, len(order))
	for _, idx := range order {
		content = append(content, blocks[idx].data)
	}
	message["content"] = content
	message["stop_reason"] = stopReason
	message["stop_sequence"] = stopSequence

	usage, _ := message["usage"].(map[string]any)
	if usage == nil {
		usage = map[string]any{}
	}
	if inputTokens != nil {
		usage["input_tokens"] = inputTokens
	}
	usage["output_tokens"] = outputTokens
	if creditUsage != nil {
		usage["credit_usage"] = creditUsage
	}
	if creditUnit != nil {
		usage["credit_unit"] = creditUnit
	}
	// 非流式:message_start.usage 的缓存字段按「估算 total」reconcile,而 message_delta 按「真实
	// total」reconcile(与计费一致)。用 delta 的缓存字段覆盖 start 的,避免返回体里 cache_* 与
	// input_tokens 不同口径、且与账单对不上(缺失即删除,不留下过时的估算 scale 值)。
	if haveDeltaUsage {
		assignOrDelete(usage, "cache_read_input_tokens", cacheReadTokens)
		assignOrDelete(usage, "cache_creation_input_tokens", cacheCreationTokens)
		assignOrDelete(usage, "cache_creation", cacheCreationDetail)
	}
	message["usage"] = usage

	return message
}

// assignOrDelete 存在(非 nil)则赋值,不存在则删除——用权威来源覆盖旧字段。
func assignOrDelete(m map[string]any, k string, v any) {
	if v == nil {
		delete(m, k)
		return
	}
	m[k] = v
}

// eventIndex 把 SSE 事件里的数值字段(int / int64 / float64)转成 int。
func eventIndex(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// cloneJSONMap 浅拷贝一层 map,避免拼装时修改原事件数据。
func cloneJSONMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
