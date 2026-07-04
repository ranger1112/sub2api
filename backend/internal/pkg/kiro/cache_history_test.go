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
//
// 用原始 JSON 字符串构造请求体(而非 map[string]any),避免大量类型断言。
func TestCacheTracker_ToolHeavyHistoryGrows(t *testing.T) {
	system := `[{"type":"text","text":"` + strings.Repeat("Coding agent system. ", 200) +
		`","cache_control":{"type":"ephemeral"}}]`
	tools := `[{"name":"read","description":"` + strings.Repeat("read file. ", 200) +
		`","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}]`
	fileContent := strings.Repeat("file line. ", 400)

	tracker := NewCacheTracker()
	const cred = int64(1)
	var msgs []string // 历史消息(不带 cache_control)
	var reads []CacheResult

	for turn := 1; turn <= 3; turn++ {
		if turn > 1 {
			tid := fmt.Sprintf("toolu_%02d", turn-1)
			// assistant 发起 tool_use;user 回带大量文件内容的 tool_result(历史主体)。
			msgs = append(msgs,
				fmt.Sprintf(`{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":"read","input":{"path":"/x.go"}}]}`, tid),
				fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":%q}]}`, tid, fileContent),
			)
		}
		msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":[{"type":"text","text":"turn %d"}]}`, turn))

		// rolling cache_control:只在最后一条 user 上打断点(替换其无断点版本)。
		withCC := make([]string, len(msgs))
		copy(withCC, msgs)
		withCC[len(withCC)-1] = fmt.Sprintf(
			`{"role":"user","content":[{"type":"text","text":"turn %d","cache_control":{"type":"ephemeral"}}]}`, turn)

		body := []byte(fmt.Sprintf(
			`{"model":"claude-sonnet-4-5","max_tokens":512,"system":%s,"tools":%s,"messages":[%s]}`,
			system, tools, strings.Join(withCC, ",")))

		var req AnthropicRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		p := tracker.BuildProfile(&req, body, 0)
		reads = append(reads, tracker.Compute(cred, p))
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
