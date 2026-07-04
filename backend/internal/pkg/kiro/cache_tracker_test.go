package kiro

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// 大总量,确保 cap(min(cumulative,total))不夹掉 cache_read,便于确定性断言。
const cacheTestTotal = 10_000_000

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestTracker() (*CacheTracker, *fakeClock) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	tr := NewCacheTracker()
	tr.now = clk.Now
	return tr, clk
}

// bigText 生成足够长的英文文本(estimateTokens 约 4 字符/token),越过 min-cacheable。
func bigText(reps int) string { return strings.Repeat("lorem ipsum dolor sit amet ", reps) }

func textBlock(text, ttl string) map[string]any {
	b := map[string]any{"type": "text", "text": text}
	if ttl != "" {
		cc := map[string]any{"type": "ephemeral"}
		if ttl != "5m" {
			cc["ttl"] = ttl
		}
		b["cache_control"] = cc
	}
	return b
}

func buildReq(t *testing.T, v map[string]any) (*AnthropicRequest, []byte) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var req AnthropicRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	return &req, raw
}

// turn1 请求:system(带 cache_control)+ 一条 user。
func turn1Body(t *testing.T, sysTTL string) (*AnthropicRequest, []byte) {
	return buildReq(t, map[string]any{
		"model":  "claude-sonnet-4.5",
		"system": []any{textBlock(bigText(120), sysTTL)},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{textBlock(bigText(120), "")}},
		},
	})
}

// turn2 请求:与 turn1 完全相同的 system + 第一条 user(逐字节不变),再追加一轮对话。
func turn2Body(t *testing.T, sysTTL string) (*AnthropicRequest, []byte) {
	return buildReq(t, map[string]any{
		"model":  "claude-sonnet-4.5",
		"system": []any{textBlock(bigText(120), sysTTL)},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{textBlock(bigText(120), "")}},
			map[string]any{"role": "assistant", "content": []any{textBlock("sure, here you go", "")}},
			map[string]any{"role": "user", "content": []any{textBlock(bigText(120), "5m")}},
		},
	})
}

func TestCacheTracker_CrossTurnHit(t *testing.T) {
	tr, _ := newTestTracker()

	req1, raw1 := turn1Body(t, "5m")
	p1 := tr.BuildProfile(req1, raw1, cacheTestTotal)
	r1 := tr.Compute(1, p1)
	if r1.CacheReadInputTokens != 0 {
		t.Fatalf("turn1 应无命中,got cache_read=%d", r1.CacheReadInputTokens)
	}
	if r1.CacheCreationInputTokens <= 0 {
		t.Fatalf("turn1 应有 cache_creation,got %d", r1.CacheCreationInputTokens)
	}
	tr.Update(1, p1)

	req2, raw2 := turn2Body(t, "5m")
	p2 := tr.BuildProfile(req2, raw2, cacheTestTotal)
	r2 := tr.Compute(1, p2)
	if r2.CacheReadInputTokens <= 0 {
		t.Fatalf("turn2 应命中历史前缀,got cache_read=%d", r2.CacheReadInputTokens)
	}
	// 命中的前缀 = system + 第一条 user,应小于本轮总量(还有新追加的内容)。
	if r2.CacheReadInputTokens >= p2.blocks[len(p2.blocks)-1].cumulativeTokens {
		t.Fatalf("cache_read=%d 不应覆盖全部 prompt", r2.CacheReadInputTokens)
	}
}

func TestCacheTracker_ComputeIsReadOnly(t *testing.T) {
	tr, _ := newTestTracker()

	req1, raw1 := turn1Body(t, "5m")
	p1 := tr.BuildProfile(req1, raw1, cacheTestTotal)
	// 只 Compute、不 Update:缓存表不应被写入。
	_ = tr.Compute(1, p1)
	_ = tr.Compute(1, p1)

	req2, raw2 := turn2Body(t, "5m")
	p2 := tr.BuildProfile(req2, raw2, cacheTestTotal)
	if r := tr.Compute(1, p2); r.CacheReadInputTokens != 0 {
		t.Fatalf("Compute 不应写缓存,但 turn2 命中了 cache_read=%d", r.CacheReadInputTokens)
	}
}

func TestCacheTracker_CredentialIsolation(t *testing.T) {
	tr, _ := newTestTracker()

	req1, raw1 := turn1Body(t, "5m")
	p1 := tr.BuildProfile(req1, raw1, cacheTestTotal)
	tr.Update(1, p1) // 写入 credential=1

	req2, raw2 := turn2Body(t, "5m")
	p2 := tr.BuildProfile(req2, raw2, cacheTestTotal)
	if r := tr.Compute(2, p2); r.CacheReadInputTokens != 0 {
		t.Fatalf("credential=2 不应命中 credential=1 的缓存,got %d", r.CacheReadInputTokens)
	}
	if r := tr.Compute(1, p2); r.CacheReadInputTokens <= 0 {
		t.Fatalf("credential=1 应命中自己的缓存,got %d", r.CacheReadInputTokens)
	}
}

func TestCacheTracker_MinCacheableFilter(t *testing.T) {
	tr, _ := newTestTracker()

	// 极短 prompt(远低于 1024 token 最小可缓存量),即便打了 cache_control 也不计入缓存。
	req, raw := buildReq(t, map[string]any{
		"model":  "claude-sonnet-4.5",
		"system": []any{textBlock("you are helpful", "5m")},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{textBlock("hi", "5m")}},
		},
	})
	p := tr.BuildProfile(req, raw, cacheTestTotal)
	tr.Update(1, p)
	r := tr.Compute(1, p)
	if r.CacheReadInputTokens != 0 || r.CacheCreationInputTokens != 0 {
		t.Fatalf("短 prompt 不应产生缓存计数,got read=%d creation=%d",
			r.CacheReadInputTokens, r.CacheCreationInputTokens)
	}
}

func TestCacheTracker_TTLBreakdown(t *testing.T) {
	// 5m 断点 → creation 归 5m 桶。
	tr, _ := newTestTracker()
	req5, raw5 := turn1Body(t, "5m")
	r5 := tr.Compute(1, tr.BuildProfile(req5, raw5, cacheTestTotal))
	if r5.CacheCreation5mTokens <= 0 || r5.CacheCreation1hTokens != 0 {
		t.Fatalf("5m 应归 5m 桶,got 5m=%d 1h=%d", r5.CacheCreation5mTokens, r5.CacheCreation1hTokens)
	}

	// 1h 断点 → creation 归 1h 桶。
	tr2, _ := newTestTracker()
	req1h, raw1h := turn1Body(t, "1h")
	r1h := tr2.Compute(1, tr2.BuildProfile(req1h, raw1h, cacheTestTotal))
	if r1h.CacheCreation1hTokens <= 0 || r1h.CacheCreation5mTokens != 0 {
		t.Fatalf("1h 应归 1h 桶,got 5m=%d 1h=%d", r1h.CacheCreation5mTokens, r1h.CacheCreation1hTokens)
	}
}

func TestCacheTracker_Expiry(t *testing.T) {
	tr, clk := newTestTracker()

	req1, raw1 := turn1Body(t, "5m")
	p1 := tr.BuildProfile(req1, raw1, cacheTestTotal)
	tr.Update(1, p1)

	// 5m TTL,推进 6 分钟后应过期,不再命中。
	clk.advance(6 * time.Minute)

	req2, raw2 := turn2Body(t, "5m")
	p2 := tr.BuildProfile(req2, raw2, cacheTestTotal)
	if r := tr.Compute(1, p2); r.CacheReadInputTokens != 0 {
		t.Fatalf("缓存应已过期,但仍命中 cache_read=%d", r.CacheReadInputTokens)
	}
}

func TestCacheTracker_NoBreakpointNoCache(t *testing.T) {
	tr, _ := newTestTracker()

	// 没有任何 cache_control → 无断点 → 无缓存计数。
	req, raw := buildReq(t, map[string]any{
		"model":  "claude-sonnet-4.5",
		"system": []any{textBlock(bigText(120), "")},
		"messages": []any{
			map[string]any{"role": "user", "content": []any{textBlock(bigText(120), "")}},
		},
	})
	p := tr.BuildProfile(req, raw, cacheTestTotal)
	tr.Update(1, p)
	if r := tr.Compute(1, p); r.CacheReadInputTokens != 0 || r.CacheCreationInputTokens != 0 {
		t.Fatalf("无断点不应有缓存,got read=%d creation=%d",
			r.CacheReadInputTokens, r.CacheCreationInputTokens)
	}
}
