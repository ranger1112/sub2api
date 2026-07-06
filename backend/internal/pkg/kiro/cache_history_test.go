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

// TestCacheResult_Reconcile_ScalesUpToRealTotal 回归线上现象:estimateTokens(÷4 字符)对
// code/JSON 系统性低估,前缀累计(read+creation)量纲小于 Kiro 按 contextUsagePercentage
// 换算的真实 total。老的 CapTo 只往下夹,真值更大时量纲差被整块甩进计费 input,账面命中率
// 被压低(截图:read≈112.8K / input≈55.6K,命中率仅 ~67%)。Reconcile 用估算 total 等比放大
// read/creation,量纲差被吸收进缓存而非 input,命中率回到应有水平。
func TestCacheResult_Reconcile_ScalesUpToRealTotal(t *testing.T) {
	// 复刻截图某行:read=112830 / creation=170 / 估算 total=113000;Kiro 真值≈168600。
	const est = 113000
	const realTotal = 168600
	r := CacheResult{
		CacheReadInputTokens:     112830,
		CacheCreationInputTokens: 170,
		CacheCreation5mTokens:    170,
		PromptTotalEstimate:      est,
	}

	// 老口径:CapTo 什么都不做(sum=113000 ≤ 168600),input 吸收整块量纲差。
	capped := r.CapTo(realTotal)
	oldInput := realTotal - capped.CacheReadInputTokens - capped.CacheCreationInputTokens
	if oldInput < 50000 {
		t.Fatalf("precondition: expected large residual input under CapTo, got %d", oldInput)
	}

	// 新口径:Reconcile 等比放大到真值。
	got := r.Reconcile(realTotal)
	newInput := realTotal - got.CacheReadInputTokens - got.CacheCreationInputTokens

	if got.CacheReadInputTokens <= capped.CacheReadInputTokens {
		t.Fatalf("cache_read not scaled up: reconcile=%d capTo=%d", got.CacheReadInputTokens, capped.CacheReadInputTokens)
	}
	if got.CacheReadInputTokens+got.CacheCreationInputTokens > realTotal {
		t.Fatalf("read+creation=%d exceeds realTotal=%d", got.CacheReadInputTokens+got.CacheCreationInputTokens, realTotal)
	}
	if newInput < 0 {
		t.Fatalf("input went negative: %d", newInput)
	}
	// 命中率:read/realTotal 应从 ~67% 升到 >95%。
	hitRate := float64(got.CacheReadInputTokens) / float64(realTotal)
	if hitRate < 0.95 {
		t.Fatalf("hit rate still low after reconcile: %.3f (read=%d realTotal=%d)", hitRate, got.CacheReadInputTokens, realTotal)
	}
	if newInput >= oldInput {
		t.Fatalf("reconcile did not reduce billed input: new=%d old=%d", newInput, oldInput)
	}
	// 5m/1h 明细必须自洽:两者之和等于 creation。
	if got.CacheCreation5mTokens+got.CacheCreation1hTokens != got.CacheCreationInputTokens {
		t.Fatalf("5m+1h != creation: 5m=%d 1h=%d creation=%d",
			got.CacheCreation5mTokens, got.CacheCreation1hTokens, got.CacheCreationInputTokens)
	}
}

// TestCacheResult_Reconcile_FallbackNoEstimate 保证 PromptTotalEstimate<=0 时退化为 CapTo
// 旧语义(单元测试直接构造 CacheResult、无 profile 等场景不受影响)。
func TestCacheResult_Reconcile_FallbackNoEstimate(t *testing.T) {
	// 真值更大且无估算 total:与 CapTo 一样保持原值(不放大)。
	r := CacheResult{CacheReadInputTokens: 12000, CacheCreationInputTokens: 3000}
	if got := r.Reconcile(20000); got != r.CapTo(20000) {
		t.Fatalf("no-estimate reconcile != CapTo: %+v vs %+v", got, r.CapTo(20000))
	}
	// 真值更小:仍按 CapTo 往下夹。
	if got := r.Reconcile(10000); got != r.CapTo(10000) {
		t.Fatalf("no-estimate down-cap reconcile != CapTo: %+v vs %+v", got, r.CapTo(10000))
	}
}

// TestCacheTracker_ReconcilePipeline 端到端:BuildProfile→Compute 回填 PromptTotalEstimate,
// 再对真值(> 估算)Reconcile,验证账面 input 从「量纲差整块」降为「≈0」。
func TestCacheTracker_ReconcilePipeline(t *testing.T) {
	system := `[{"type":"text","text":"` + strings.Repeat("system prompt words. ", 300) +
		`","cache_control":{"type":"ephemeral"}}]`
	userMsg := `{"role":"user","content":[{"type":"text","text":"` +
		strings.Repeat("please analyze this code. ", 300) +
		`","cache_control":{"type":"ephemeral"}}]}`
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":512,"system":` + system +
		`,"messages":[` + userMsg + `]}`)

	var req AnthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	tracker := NewCacheTracker()
	const cred = int64(7)
	p := tracker.BuildProfile(&req, body, 0)
	// Turn 1:写入。Turn 2(同一 prompt):应全量命中。
	_ = tracker.Compute(cred, p)
	tracker.Update(cred, p)
	res := tracker.Compute(cred, p)

	if res.PromptTotalEstimate <= 0 {
		t.Fatalf("Compute did not stamp PromptTotalEstimate: %d", res.PromptTotalEstimate)
	}
	if res.CacheReadInputTokens == 0 {
		t.Fatalf("turn2 not a cache hit: read=0")
	}

	// 模拟 Kiro contextUsage 真值比估算前缀大 ~1.5×(estimateTokens 对内容低估)。
	realTotal := res.PromptTotalEstimate * 3 / 2
	reconciled := res.Reconcile(realTotal)
	billedInput := realTotal - reconciled.CacheReadInputTokens - reconciled.CacheCreationInputTokens

	if billedInput < 0 {
		t.Fatalf("billed input negative: %d", billedInput)
	}
	// 全量命中的同一 prompt:reconcile 后 input 应 ≪ realTotal(量纲差不再进 input)。
	if float64(billedInput) > 0.1*float64(realTotal) {
		t.Fatalf("billed input still too high after reconcile: %d (realTotal=%d)", billedInput, realTotal)
	}
}
