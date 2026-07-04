package kiro

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestCacheTracker_ToolHeavyHistoryGrows 回归:coding-agent 对话历史由 tool_use /
// tool_result 主导(processMessageContent 不把它们计入 "text" 估算)。BuildProfile 的
// 兜底 total 若用 estimateInputTokens,会把历史估成 ~0 → Compute 的 min(前缀累计, total)
// 把 cache_read 钉死在静态前缀、cache_creation 恒 0,历史每轮被错报为全新 input(约 4x 超计费)。
// 修复后兜底 total 改用段链自身 token 之和(覆盖工具内容),cache_read 应随历史逐轮增长、
// 且每轮有非零 cache_creation。
func TestCacheTracker_ToolHeavyHistoryGrows(t *testing.T) {
	system := []any{map[string]any{
		"type": "text", "text": strings.Repeat("Coding agent system. ", 200),
		"cache_control": map[string]any{"type": "ephemeral"},
	}}
	tools := []any{
		map[string]any{"name": "read", "description": strings.Repeat("read file. ", 200),
			"input_schema": map[string]any{"type": "object"},
			"cache_control": map[string]any{"type": "ephemeral"}},
	}

	stripCC := func(msgs []any) []any {
		out := make([]any, len(msgs))
		for i, h := range msgs {
			hm := h.(map[string]any)
			blocks := hm["content"].([]any)
			nb := make([]any, len(blocks))
			for j, b := range blocks {
				cp := map[string]any{}
				for k, v := range b.(map[string]any) {
					if k != "cache_control" {
						cp[k] = v
					}
				}
				nb[j] = cp
			}
			out[i] = map[string]any{"role": hm["role"], "content": nb}
		}
		return out
	}

	tracker := NewCacheTracker()
	const cred = int64(1)
	var history []any
	var reads []CacheResult

	for turn := 1; turn <= 3; turn++ {
		if turn > 1 {
			tid := fmt.Sprintf("toolu_%02d", turn-1)
			history = append(history,
				map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "tool_use", "id": tid, "name": "read", "input": map[string]any{"path": "/x.go"}},
				}},
				// tool_result 携带大量文件内容:真实历史主体,但 processMessageContent 计为 0 text。
				map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": tid, "content": strings.Repeat("file line. ", 400)},
				}},
			)
		}
		history = append(history, map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": fmt.Sprintf("turn %d", turn)},
		}})

		msgs := stripCC(history)
		last := msgs[len(msgs)-1].(map[string]any)["content"].([]any)
		last[len(last)-1].(map[string]any)["cache_control"] = map[string]any{"type": "ephemeral"}

		body, _ := json.Marshal(map[string]any{
			"model": "claude-sonnet-4-5", "max_tokens": 512,
			"system": system, "tools": tools, "messages": msgs,
		})
		var req AnthropicRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		p := tracker.BuildProfile(&req, body, 0)
		res := tracker.Compute(cred, p)
		reads = append(reads, res)
		tracker.Update(cred, p)
	}

	// 第一轮无历史 → cache_read 应为 0。
	if reads[0].CacheReadInputTokens != 0 {
		t.Fatalf("turn1 cache_read = %d, want 0", reads[0].CacheReadInputTokens)
	}
	// 关键回归:tool 主导的历史必须被识别为 cache_read,且逐轮增长(而非钉死)。
	if reads[1].CacheReadInputTokens == 0 {
		t.Fatalf("turn2 cache_read = 0: tool-heavy history not cached (regression)")
	}
	if reads[2].CacheReadInputTokens <= reads[1].CacheReadInputTokens {
		t.Fatalf("cache_read not growing across turns: t2=%d t3=%d (history hit-rate bug)",
			reads[1].CacheReadInputTokens, reads[2].CacheReadInputTokens)
	}
	// 每轮新增的工具历史应产生非零 cache_creation(旧 bug 下恒为 0)。
	if reads[1].CacheCreationInputTokens == 0 || reads[2].CacheCreationInputTokens == 0 {
		t.Fatalf("cache_creation stuck at 0 (new tool history not cached): t2=%d t3=%d",
			reads[1].CacheCreationInputTokens, reads[2].CacheCreationInputTokens)
	}
}
