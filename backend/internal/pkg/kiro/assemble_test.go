package kiro

import "testing"

// TestAssembleMessage_NonStreamCacheFromDelta 回归:非流式响应的 usage 缓存字段必须取自
// message_delta(按真实 total reconcile),而非 message_start(按估算 total reconcile)。
// 否则返回体的 cache_read/creation 与 input_tokens/计费不同口径(见 assemble.go 修复)。
func TestAssembleMessage_NonStreamCacheFromDelta(t *testing.T) {
	events := []SSEEvent{
		{Event: "message_start", Data: map[string]any{
			"message": map[string]any{
				"type": "message", "role": "assistant",
				"usage": map[string]any{
					"input_tokens": 1500, "output_tokens": 1,
					"cache_read_input_tokens": 7500, "cache_creation_input_tokens": 1000,
					"cache_creation": map[string]any{"ephemeral_5m_input_tokens": 1000, "ephemeral_1h_input_tokens": 0},
				},
			},
		}},
		{Event: "message_delta", Data: map[string]any{
			"delta": map[string]any{"stop_reason": "end_turn"},
			"usage": map[string]any{
				"input_tokens": 4500, "output_tokens": 50,
				"cache_read_input_tokens": 22500, "cache_creation_input_tokens": 3000,
				"cache_creation": map[string]any{"ephemeral_5m_input_tokens": 3000, "ephemeral_1h_input_tokens": 0},
			},
		}},
	}
	usage, _ := AssembleMessage(events)["usage"].(map[string]any)
	if usage["input_tokens"] != 4500 {
		t.Fatalf("input_tokens = %v, want 4500 (from delta)", usage["input_tokens"])
	}
	if usage["cache_read_input_tokens"] != 22500 {
		t.Fatalf("cache_read = %v, want 22500 (delta, not 7500 from start)", usage["cache_read_input_tokens"])
	}
	if usage["cache_creation_input_tokens"] != 3000 {
		t.Fatalf("cache_creation = %v, want 3000 (delta, not 1000 from start)", usage["cache_creation_input_tokens"])
	}
	cc, _ := usage["cache_creation"].(map[string]any)
	if cc == nil || cc["ephemeral_5m_input_tokens"] != 3000 {
		t.Fatalf("cache_creation detail not from delta: %v", usage["cache_creation"])
	}
}

// TestAssembleMessage_DeltaWithoutCacheClearsStartCache 回归:当 message_delta 的 usage 无缓存字段
// (缓存 reconcile 到 0 时 usageWithCache 会省略),必须清除 message_start 遗留的估算 scale 缓存字段,
// 避免返回体带上与计费不符的过时缓存计数。
func TestAssembleMessage_DeltaWithoutCacheClearsStartCache(t *testing.T) {
	events := []SSEEvent{
		{Event: "message_start", Data: map[string]any{
			"message": map[string]any{"usage": map[string]any{
				"input_tokens": 100, "cache_read_input_tokens": 500, "cache_creation_input_tokens": 50,
			}},
		}},
		{Event: "message_delta", Data: map[string]any{
			"usage": map[string]any{"input_tokens": 1000, "output_tokens": 10}, // 无缓存键
		}},
	}
	usage, _ := AssembleMessage(events)["usage"].(map[string]any)
	if _, ok := usage["cache_read_input_tokens"]; ok {
		t.Fatalf("stale start cache_read must be cleared when delta has none: %v", usage)
	}
	if _, ok := usage["cache_creation_input_tokens"]; ok {
		t.Fatalf("stale start cache_creation must be cleared: %v", usage)
	}
	if usage["input_tokens"] != 1000 {
		t.Fatalf("input_tokens = %v, want 1000", usage["input_tokens"])
	}
}
