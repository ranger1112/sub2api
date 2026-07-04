package kiro

import (
	"crypto/sha256"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 中转层提示词缓存(忠实移植 M-JYuan/kiro.rs 的 src/anthropic/cache_tracker.rs)。
//
// Kiro 上游不下发 cache_creation / cache_read token 字段(实测 meteringEvent 只给
// credit 计费量),所以这里在中转层自行模拟"提示词缓存",复现 Anthropic 滑动窗口
// 缓存的「最长公共前缀命中」语义,给客户端一个符合 Anthropic 语义的账面视图:
//
//   - 把 prompt 的稳定前缀按 tools → system → 逐条 message 切成一条递增前缀段链,
//     每段 fingerprint 是「从头累积到该段」的哈希指纹,tokens 是该前缀的累计估算。
//   - 客户端用 cache_control 打的断点(及其后每个 message 边界)成为可缓存断点。
//   - Compute 取最深命中断点 = 最长已缓存前缀 = cache_read;其后到末断点 =
//     cache_creation;完全 miss → cache_read = 0。
//   - 跨轮命中的关键:历史消息逐字节不变,故 Turn N+1 的历史前缀段指纹必然等于
//     Turn N 写入的同一段。按 credential(account.ID)隔离,不同账号互不命中。
//
// 两条铁律(防止失败请求污染):Compute 只读;Update 只在上游成功后调。
// 纯进程内存,重启清空(与 M-JYuan 一致,不依赖 Redis / 落盘)。
//
// 注意:这不是真缓存,不省 credit——Kiro 该扣多少 credit 仍扣多少,cache_control
// 传不到上游。本模块只产出客户端账面用的合成缓存计数。

const (
	// cacheDefaultTTL 是 ephemeral 缓存的默认 TTL(5min)。
	cacheDefaultTTL = 5 * time.Minute
	// cacheOneHourTTL 对齐 Anthropic ttl="1h"。
	cacheOneHourTTL = time.Hour
	// cachePrefixLookbackLimit 限制 Compute 回溯的候选断点数(自最深起)。
	cachePrefixLookbackLimit = 10
	// cacheMaxEntriesPerCred 单 credential 条目上限,防止内存无限增长。
	cacheMaxEntriesPerCred = 4096
)

// CacheResult 是一次请求的合成缓存计数(注入客户端响应 usage,并据此走 token 计费)。
type CacheResult struct {
	CacheReadInputTokens     int
	CacheCreationInputTokens int
	CacheCreation5mTokens    int
	CacheCreation1hTokens    int
}

// cacheBlock 是递增前缀段链中的一段。
type cacheBlock struct {
	prefixFingerprint [32]byte
	cumulativeTokens  int
}

// cacheBreakpoint 标记某段是一个可写入/可命中的缓存断点。
type cacheBreakpoint struct {
	blockIndex int
	ttl        time.Duration
}

// resolvedBreakpoint 是过了 min-cacheable 过滤后的断点(携带累计 token)。
type resolvedBreakpoint struct {
	blockIndex       int
	cumulativeTokens int
	ttl              time.Duration
}

// CacheProfile 是一次请求算出的缓存画像,喂给 Compute / Update。
type CacheProfile struct {
	totalInputTokens   int
	minCacheableTokens int
	blocks             []cacheBlock
	breakpoints        []cacheBreakpoint
}

func (p *CacheProfile) cacheableBreakpoints() []resolvedBreakpoint {
	out := make([]resolvedBreakpoint, 0, len(p.breakpoints))
	for _, bp := range p.breakpoints {
		if bp.blockIndex < 0 || bp.blockIndex >= len(p.blocks) {
			continue
		}
		block := p.blocks[bp.blockIndex]
		if block.cumulativeTokens < p.minCacheableTokens {
			continue
		}
		out = append(out, resolvedBreakpoint{
			blockIndex:       bp.blockIndex,
			cumulativeTokens: block.cumulativeTokens,
			ttl:              bp.ttl,
		})
	}
	return out
}

func (p *CacheProfile) lastCacheableBreakpoint() (resolvedBreakpoint, bool) {
	cbs := p.cacheableBreakpoints()
	if len(cbs) == 0 {
		return resolvedBreakpoint{}, false
	}
	return cbs[len(cbs)-1], true
}

// cacheEntry 是缓存表里的一条记录。expiresAt 从首次写入起算,命中不续期。
type cacheEntry struct {
	tokenCount int
	ttl        time.Duration
	expiresAt  time.Time
}

// CacheTracker 是进程内、按 credential 隔离的前缀缓存模拟器,并发安全。
type CacheTracker struct {
	mu           sync.Mutex
	byCredential map[int64]map[[32]byte]cacheEntry
	maxTTL       time.Duration
	now          func() time.Time // 可注入,便于测试
}

// NewCacheTracker 创建一个空的缓存模拟器。
func NewCacheTracker() *CacheTracker {
	return &CacheTracker{
		byCredential: map[int64]map[[32]byte]cacheEntry{},
		maxTTL:       cacheOneHourTTL,
		now:          time.Now,
	}
}

// BuildProfile 从请求构建缓存画像。rawBody 是原始 /v1/messages 请求体(用于解析
// cache_control 断点,因为 kiro 内部类型不保留该字段);totalInputTokens 是本次
// prompt 的真实/估算 total(cache_read/creation 会 cap 到它以内)。
func (t *CacheTracker) BuildProfile(req *AnthropicRequest, rawBody []byte, totalInputTokens int) *CacheProfile {
	if totalInputTokens <= 0 {
		totalInputTokens = estimateInputTokens(req)
	}
	segments := flattenCacheSegments(rawBody)

	prelude := cachePrelude(req)
	prev := sha256.Sum256(prelude)

	blocks := make([]cacheBlock, 0, len(segments))
	var breakpoints []cacheBreakpoint
	cumulative := 0
	var activeTTL time.Duration
	hasActiveTTL := false
	seen := map[int]bool{}

	for index, seg := range segments {
		cumulative += seg.tokens

		blockHash := sha256.Sum256(seg.content)
		var buf [64]byte
		copy(buf[:32], prev[:])
		copy(buf[32:], blockHash[:])
		fp := sha256.Sum256(buf[:])
		prev = fp

		blocks = append(blocks, cacheBlock{prefixFingerprint: fp, cumulativeTokens: cumulative})

		// 客户端显式 cache_control 断点。
		if seg.breakpointTTL > 0 {
			ttl := seg.breakpointTTL
			if ttl > t.maxTTL {
				ttl = t.maxTTL
			}
			activeTTL = ttl
			hasActiveTTL = true
			if !seen[index] {
				seen[index] = true
				breakpoints = append(breakpoints, cacheBreakpoint{blockIndex: index, ttl: ttl})
			}
		}

		// 出现过 cache_control 后,其后每个 message 边界也成为可缓存断点——
		// 这样历史每一轮都变成一个可复用前缀(跨轮命中的来源)。
		if seg.isMessageEnd && hasActiveTTL && !seen[index] {
			seen[index] = true
			breakpoints = append(breakpoints, cacheBreakpoint{blockIndex: index, ttl: activeTTL})
		}
	}

	return &CacheProfile{
		totalInputTokens:   max(totalInputTokens, 0),
		minCacheableTokens: minimumCacheableTokens(req.Model),
		blocks:             blocks,
		breakpoints:        breakpoints,
	}
}

// Compute 只读地计算本次请求的合成缓存计数(命中/新建),不写入缓存表。
func (t *CacheTracker) Compute(credentialID int64, p *CacheProfile) CacheResult {
	last, ok := p.lastCacheableBreakpoint()
	if !ok {
		return CacheResult{}
	}
	lastTokens := min(last.cumulativeTokens, p.totalInputTokens)

	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneExpired(now)

	creds, hasCreds := t.byCredential[credentialID]
	if !hasCreds {
		// 首次请求,无任何缓存条目 → 全部计入 cache_creation。
		c5m, c1h := computeTTLBreakdown(p, 0)
		return CacheResult{
			CacheReadInputTokens:     0,
			CacheCreationInputTokens: lastTokens,
			CacheCreation5mTokens:    c5m,
			CacheCreation1hTokens:    c1h,
		}
	}

	matched := 0
	cbs := p.cacheableBreakpoints()
	// 自最深断点起回溯,取第一个命中的前缀 = 最长已缓存前缀。
	limit := cachePrefixLookbackLimit
	for i := len(cbs) - 1; i >= 0 && limit > 0; i, limit = i-1, limit-1 {
		block := p.blocks[cbs[i].blockIndex]
		if e, ok := creds[block.prefixFingerprint]; ok && e.expiresAt.After(now) {
			matched = min(cbs[i].cumulativeTokens, p.totalInputTokens)
			break
		}
	}

	newTokens := max(lastTokens-matched, 0)
	c5m, c1h := computeTTLBreakdown(p, matched)
	return CacheResult{
		CacheReadInputTokens:     max(matched, 0),
		CacheCreationInputTokens: newTokens,
		CacheCreation5mTokens:    c5m,
		CacheCreation1hTokens:    c1h,
	}
}

// Update 把本次请求的可缓存断点写入缓存表(只在上游成功后调用)。
// 命中已存在的前缀不刷新 expiresAt:Anthropic 真实 TTL 从首次写入起算。
func (t *CacheTracker) Update(credentialID int64, p *CacheProfile) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneExpired(now)

	creds := t.byCredential[credentialID]
	if creds == nil {
		creds = map[[32]byte]cacheEntry{}
		t.byCredential[credentialID] = creds
	}

	for _, bp := range p.cacheableBreakpoints() {
		block := p.blocks[bp.blockIndex]
		if e, ok := creds[block.prefixFingerprint]; ok {
			// 仅更新单调增长字段,TTL/expiresAt 保留旧值(不续期)。
			if block.cumulativeTokens > e.tokenCount {
				e.tokenCount = block.cumulativeTokens
			}
			if bp.ttl > e.ttl {
				e.ttl = bp.ttl
			}
			creds[block.prefixFingerprint] = e
		} else {
			creds[block.prefixFingerprint] = cacheEntry{
				tokenCount: block.cumulativeTokens,
				ttl:        bp.ttl,
				expiresAt:  now.Add(bp.ttl),
			}
		}
	}

	t.evictIfOverCapacity(creds)
}

// evictIfOverCapacity 超过上限时按最早过期淘汰,保证内存有界。
func (t *CacheTracker) evictIfOverCapacity(creds map[[32]byte]cacheEntry) {
	if len(creds) <= cacheMaxEntriesPerCred {
		return
	}
	type kv struct {
		key [32]byte
		exp time.Time
	}
	items := make([]kv, 0, len(creds))
	for k, e := range creds {
		items = append(items, kv{k, e.expiresAt})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].exp.Before(items[j].exp) })
	for i := 0; i < len(items) && len(creds) > cacheMaxEntriesPerCred; i++ {
		delete(creds, items[i].key)
	}
}

func (t *CacheTracker) pruneExpired(now time.Time) {
	for cid, creds := range t.byCredential {
		for fp, e := range creds {
			if !e.expiresAt.After(now) {
				delete(creds, fp)
			}
		}
		if len(creds) == 0 {
			delete(t.byCredential, cid)
		}
	}
}

// computeTTLBreakdown 把新建(cache_creation)部分按末断点的 TTL 归入 5m 或 1h。
func computeTTLBreakdown(p *CacheProfile, matched int) (c5m, c1h int) {
	last, ok := p.lastCacheableBreakpoint()
	if !ok {
		return 0, 0
	}
	newTokens := max(min(last.cumulativeTokens, p.totalInputTokens)-matched, 0)
	if newTokens == 0 {
		return 0, 0
	}
	if last.ttl == cacheOneHourTTL {
		return 0, newTokens
	}
	return newTokens, 0
}

// minimumCacheableTokens 返回模型的最小可缓存 prompt 长度(对齐 Anthropic)。
// 断点累计 token 不足此值则不计入缓存。Kiro 只服务 haiku-4.5,故 haiku → 1024。
func minimumCacheableTokens(model string) int {
	mapped, ok := MapModel(model)
	if !ok {
		mapped = model
	}
	if strings.Contains(strings.ToLower(mapped), "opus") {
		return 4096
	}
	return 1024
}

// ==================== 段展平 + cache_control 解析 ====================

// cacheSegment 是前缀链中一段的原料。
type cacheSegment struct {
	content       []byte        // 规范化、剥离 cache_control、带 kind 标签的指纹原文
	tokens        int           // 该段估算 token
	breakpointTTL time.Duration // >0 表示该段带 cache_control 断点
	isMessageEnd  bool          // 是否是某条 message 的末块
}

// rawCacheRequest 从原始请求体里取出 cache_control 解析所需的 raw 片段。
type rawCacheRequest struct {
	System   json.RawMessage   `json:"system"`
	Messages []json.RawMessage `json:"messages"`
	Tools    []json.RawMessage `json:"tools"`
}

// cachePrelude 是与 prompt 内容无关但影响缓存可复用性的固定配置(model + tool_choice)。
func cachePrelude(req *AnthropicRequest) []byte {
	env := struct {
		Model      string          `json:"model"`
		ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
	}{Model: req.Model, ToolChoice: req.ToolChoice}
	b, err := json.Marshal(env)
	if err != nil {
		return []byte(req.Model)
	}
	return b
}

// flattenCacheSegments 把 tools → system → 逐条 message 展平为递增前缀段链原料。
func flattenCacheSegments(rawBody []byte) []cacheSegment {
	var raw rawCacheRequest
	if json.Unmarshal(rawBody, &raw) != nil {
		return nil
	}

	var segments []cacheSegment

	// 1) tools
	for i, toolRaw := range raw.Tools {
		segments = append(segments, cacheSegment{
			content:       segEnvelope("tool", i, 0, toolRaw),
			tokens:        estimateTokens(toolText(toolRaw)),
			breakpointTTL: cacheControlTTL(toolRaw),
			isMessageEnd:  false,
		})
	}

	// 2) system(string 或 []{text}）
	for i, blockRaw := range systemBlocks(raw.System) {
		segments = append(segments, cacheSegment{
			content:       segEnvelope("system", i, 0, blockRaw),
			tokens:        estimateTokens(systemBlockText(blockRaw)),
			breakpointTTL: cacheControlTTL(blockRaw),
			isMessageEnd:  false,
		})
	}

	// 3) messages(逐条,每条内部按 content block 切,最后一块为 message 末块）
	for mi, msgRaw := range raw.Messages {
		blocks := messageContentBlocks(msgRaw)
		lastIdx := len(blocks) - 1
		for bi, blockRaw := range blocks {
			segments = append(segments, cacheSegment{
				content:       segEnvelope("message", mi, bi, blockRaw),
				tokens:        estimateTokens(blockText(blockRaw)),
				breakpointTTL: cacheControlTTL(blockRaw),
				isMessageEnd:  bi == lastIdx,
			})
		}
	}

	return segments
}

// segEnvelope 把一段原文包成带 kind/index 的规范化信封,避免不同 kind 同文碰撞。
func segEnvelope(kind string, i, j int, body json.RawMessage) []byte {
	env := struct {
		K string          `json:"k"`
		I int             `json:"i"`
		J int             `json:"j"`
		B json.RawMessage `json:"b"`
	}{K: kind, I: i, J: j, B: canonicalStrip(body)}
	b, err := json.Marshal(env)
	if err != nil {
		return []byte(kind + strconv.Itoa(i) + "." + strconv.Itoa(j))
	}
	return b
}

// canonicalStrip 递归剥离 cache_control 后重新序列化(map key 由 encoding/json 排序,
// 保证确定性),使"有无断点"不改变指纹。
func canonicalStrip(raw json.RawMessage) json.RawMessage {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return append(json.RawMessage(nil), raw...)
	}
	v = stripCacheControl(v)
	b, err := json.Marshal(v)
	if err != nil {
		return append(json.RawMessage(nil), raw...)
	}
	return b
}

func stripCacheControl(v any) any {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "cache_control")
		for k := range x {
			x[k] = stripCacheControl(x[k])
		}
		return x
	case []any:
		for i := range x {
			x[i] = stripCacheControl(x[i])
		}
		return x
	}
	return v
}

// cacheControlTTL 解析一段 raw 上的 cache_control(ephemeral),返回其 TTL;无则 0。
func cacheControlTTL(raw json.RawMessage) time.Duration {
	var c struct {
		CacheControl *struct {
			Type string  `json:"type"`
			TTL  *string `json:"ttl"`
		} `json:"cache_control"`
	}
	if json.Unmarshal(raw, &c) != nil || c.CacheControl == nil {
		return 0
	}
	if c.CacheControl.Type != "ephemeral" {
		return 0
	}
	if c.CacheControl.TTL != nil && *c.CacheControl.TTL == "1h" {
		return cacheOneHourTTL
	}
	return cacheDefaultTTL
}

// systemBlocks 把 system(string 或 []{text}）拆成逐块 raw。
func systemBlocks(raw json.RawMessage) []json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		return arr
	}
	// string 形式:整体作为一块。
	return []json.RawMessage{raw}
}

func systemBlockText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var m AnthropicSystemMessage
	if json.Unmarshal(raw, &m) == nil {
		return m.Text
	}
	return ""
}

// messageContentBlocks 取一条 message 的 content,拆成逐块 raw(string content 视为一块)。
func messageContentBlocks(msgRaw json.RawMessage) []json.RawMessage {
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(msgRaw, &msg) != nil || len(msg.Content) == 0 {
		return nil
	}
	var arr []json.RawMessage
	if json.Unmarshal(msg.Content, &arr) == nil {
		return arr
	}
	// string content:整体一块。
	return []json.RawMessage{msg.Content}
}

// blockText 从一个 message content block 提取用于估算 token 的文本。
func blockText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b AnthropicContentBlock
	if json.Unmarshal(raw, &b) != nil {
		return ""
	}
	switch b.Type {
	case "text":
		if b.Text != nil {
			return *b.Text
		}
	case "thinking":
		if b.Thinking != nil {
			return *b.Thinking
		}
	case "tool_result":
		return extractToolResultContent(b.Content)
	case "tool_use":
		return string(b.Input)
	}
	return ""
}

// toolText 把一个工具定义拼成用于估算 token 的文本。
func toolText(raw json.RawMessage) string {
	var tool AnthropicTool
	if json.Unmarshal(raw, &tool) != nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(tool.Name)
	sb.WriteByte('\n')
	sb.WriteString(tool.Description)
	if len(tool.InputSchema) > 0 {
		sb.WriteByte('\n')
		sb.Write(tool.InputSchema)
	}
	return sb.String()
}
