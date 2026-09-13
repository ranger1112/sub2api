package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// codexFingerprintIDsContextKey 是暂存在 gin context 的收敛 ID 集合键。
// 由 Forward（非透传）或 forwardOpenAIPassthrough（透传）解析后写入，请求
// 构造器读取用于出站头改写——请求体与出站头必须共享同一份 IDs，保证
// turn_id 等随机字段一致。
const codexFingerprintIDsContextKey = "codex_fingerprint_ids"

// stageCodexFingerprintIDs 将本 attempt 解析出的收敛 ID 暂存到 gin context。
// 必须无条件覆写（含 nil）：failover 从收敛账号切到 off 账号时，上一账号的
// IDs 不得残留并被误应用到新账号的出站头（typed-nil 由应用侧 nil 守卫吸收）。
func stageCodexFingerprintIDs(c *gin.Context, ids *codexFingerprintIDs) {
	if c != nil {
		c.Set(codexFingerprintIDsContextKey, ids)
	}
}

func stagedCodexFingerprintIDs(c *gin.Context, account *Account) *codexFingerprintIDs {
	if c == nil || account == nil || !account.UsesOpenAICodexProtocol() {
		return nil
	}
	value, ok := c.Get(codexFingerprintIDsContextKey)
	if !ok {
		return nil
	}
	ids, ok := value.(*codexFingerprintIDs)
	if !ok || ids == nil || ids.accountID != account.ID {
		return nil
	}
	return ids
}

// applyStagedCodexFingerprintHeaders 读取 context 暂存的收敛 ID 并改写出站头。
// 非透传与透传两个请求构造器共用本函数，防止应用语义漂移。仅解析该
// snapshot 的 OAuth 账号可读取，避免 stale context 跨账号 failover 泄漏。
func applyStagedCodexFingerprintHeaders(c *gin.Context, account *Account, h http.Header) {
	applyCodexFingerprintHeaders(h, stagedCodexFingerprintIDs(c, account))
}

func applyStagedCodexFingerprintClientMetadata(c *gin.Context, account *Account, reqBody map[string]any) bool {
	return applyCodexFingerprintClientMetadata(reqBody, stagedCodexFingerprintIDs(c, account))
}

// codexFingerprintMode 控制 OAuth 账号出站请求的设备指纹收敛强度。
// 多人共享同一 OAuth 账号时，每个用户的 Codex 客户端会携带各自不同的
// installation_id / session_id / thread_id，上游据此判定设备数和会话数。
// 收敛模式将这些标识改写为账号作用域的派生值，减少上游可见的设备/会话指纹。
// 派生值必须保持 codex-rs 的原生形态与字段间等式（见各 resolveConverged* 的
// 注释），收敛后的标识集合才始终是真实客户端可能产生的组合。
type codexFingerprintMode string

const (
	// codexFingerprintOff 不做任何收敛，原样透传客户端标识。
	// 这是默认值：收敛是显式 opt-in 的（见 GetCodexFingerprintMode）。
	codexFingerprintOff codexFingerprintMode = "off"
	// codexFingerprintDevice 仅收敛 installation_id 为账号级恒定值。
	// 上游看到 1 台设备 + 多会话（每用户各自的 session）。
	codexFingerprintDevice codexFingerprintMode = "device"
	// codexFingerprintSession 收敛 installation_id + session_id，
	// thread_id 按客户端原始 thread-id 确定性派生（每个真实 Codex 线程一个独立线程），
	// subagent 回带的 parent_thread_id 走同一派生函数，父子始终落在同一收敛空间。
	// 上游看到 1 台设备 + 1 会话 + N 线程，最接近正常用户 spawn 子代理的模式。
	codexFingerprintSession codexFingerprintMode = "session"
	// codexFingerprintFull 收敛所有标识：installation_id + session_id + thread_id，
	// 并剥离 parent_thread_id——其唯一原生对应物是根线程，而根线程没有父线程。
	// 上游看到 1 台设备 + 1 会话 + 1 线程，最激进。
	codexFingerprintFull codexFingerprintMode = "full"
)

const (
	codexFingerprintModeExtraKey = "codex_fingerprint_mode"
	codexFingerprintSeedExtraKey = "codex_fingerprint_seed"
)

func canonicalCodexFingerprintSeed(value any) (string, bool) {
	raw, ok := value.(string)
	if !ok {
		return "", false
	}
	trimmed := strings.TrimSpace(raw)
	parsed, err := uuid.Parse(trimmed)
	if err != nil || parsed == uuid.Nil || trimmed != parsed.String() {
		return "", false
	}
	return trimmed, true
}

func newCodexFingerprintSeed() string {
	return uuid.NewString()
}

func stripCodexFingerprintSeed(extra map[string]any) map[string]any {
	if extra == nil {
		return nil
	}
	stripped := maps.Clone(extra)
	delete(stripped, codexFingerprintSeedExtraKey)
	return stripped
}

func codexFingerprintModeFromExtra(extra map[string]any) codexFingerprintMode {
	if extra == nil {
		return codexFingerprintOff
	}
	raw, _ := extra[codexFingerprintModeExtraKey].(string)
	switch codexFingerprintMode(strings.TrimSpace(raw)) {
	case codexFingerprintOff, codexFingerprintDevice, codexFingerprintSession, codexFingerprintFull:
		return codexFingerprintMode(strings.TrimSpace(raw))
	default:
		return codexFingerprintOff
	}
}

func codexFingerprintModeRequiresSeed(mode codexFingerprintMode) bool {
	switch mode {
	case codexFingerprintDevice, codexFingerprintSession, codexFingerprintFull:
		return true
	default:
		return false
	}
}

func codexFingerprintSeed(extra map[string]any) (string, bool) {
	if extra == nil {
		return "", false
	}
	return canonicalCodexFingerprintSeed(extra[codexFingerprintSeedExtraKey])
}

func prepareCodexFingerprintExtraForCreate(platform, accountType string, extra map[string]any) map[string]any {
	prepared := stripCodexFingerprintSeed(extra)
	if platform != PlatformOpenAI || (accountType != AccountTypeOAuth && accountType != AccountTypeSetupToken) || !codexFingerprintModeRequiresSeed(codexFingerprintModeFromExtra(prepared)) {
		return prepared
	}
	if prepared == nil {
		prepared = make(map[string]any, 1)
	}
	prepared[codexFingerprintSeedExtraKey] = newCodexFingerprintSeed()
	return prepared
}

func prepareCodexFingerprintExtraForUpdate(account *Account, extra map[string]any) map[string]any {
	prepared := stripCodexFingerprintSeed(extra)
	if account == nil || !account.IsOpenAIOAuthLike() {
		return prepared
	}
	if seed, ok := codexFingerprintSeed(account.Extra); ok {
		if prepared == nil {
			prepared = make(map[string]any, 1)
		}
		prepared[codexFingerprintSeedExtraKey] = seed
		return prepared
	}
	if codexFingerprintModeRequiresSeed(codexFingerprintModeFromExtra(prepared)) {
		if prepared == nil {
			prepared = make(map[string]any, 1)
		}
		prepared[codexFingerprintSeedExtraKey] = newCodexFingerprintSeed()
	}
	return prepared
}

func sanitizedCodexFingerprintExtraUpdates(updates map[string]any) map[string]any {
	if updates == nil {
		return nil
	}
	sanitized := maps.Clone(updates)
	delete(sanitized, codexFingerprintSeedExtraKey)
	return sanitized
}

// ShouldEnsureCodexFingerprintSeedForExtraUpdates reports whether a JSONB key-level
// extra update is enabling Codex fingerprint convergence and therefore must atomically
// preserve or create the system-managed per-account seed in the repository update.
func ShouldEnsureCodexFingerprintSeedForExtraUpdates(updates map[string]any) bool {
	if updates == nil {
		return false
	}
	return codexFingerprintModeRequiresSeed(codexFingerprintModeFromExtra(updates))
}

// GetCodexFingerprintMode 从账号 extra JSON 读取指纹收敛模式。
//
// **收敛是显式 opt-in**：未设置、空值或非法值一律按 off 处理，只有管理员
// 明确配置 device / session / full 才收敛。
//
// 历史：v0.1.175（#5553）把缺省值当作 session，导致升级后存量 OAuth 账号
// （普遍没有这个 extra 键）的每个非透传请求都被静默改写 installation /
// session / thread / turn / window 五类标识；#5555、#5556、#5582 报告的额度
// 缩水都卡在该版本边界，并有"回退 v0.1.173 即恢复"与"新账号开收敛后降额"
// 的 A/B 实测。上游的配额判定策略不可观测，因此这里取兼容安全的一侧：
// 不显式 opt-in 就保持 v0.1.175 之前的客户端身份（#5610）。
func (a *Account) GetCodexFingerprintMode() codexFingerprintMode {
	if a == nil || !a.IsOpenAIOAuthLike() {
		return codexFingerprintOff
	}
	return codexFingerprintModeFromExtra(a.Extra)
}

// deriveStableUUIDv4 从种子确定性派生一个 UUIDv4 格式的字符串。
// 同一种子永远返回同一值。
func deriveStableUUIDv4(seed string) string {
	h := sha256.Sum256([]byte(seed))
	b := h[:16]
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(b[0:4]),
		binary.BigEndian.Uint16(b[4:6]),
		binary.BigEndian.Uint16(b[6:8]),
		binary.BigEndian.Uint16(b[8:10]),
		b[10:16])
}

// resolveConvergedInstallationID 返回账号级恒定的 installation_id。
// 优先使用管理员配置的真实 device_id，无则从系统管理的账号随机种子确定性派生。
func resolveConvergedInstallationID(account *Account, seed string) string {
	if account == nil {
		return ""
	}
	if deviceID := account.GetOpenAIDeviceID(); deviceID != "" {
		return deviceID
	}
	if seed == "" {
		return ""
	}
	return deriveStableUUIDv4("sub2api:codex-install-id:v2:" + seed)
}

// codexSessionRotationBucket 返回 session_id 的轮换桶（UTC 日）。
//
// 真实客户端的 session_id 等于根线程 id，由进程启动时确定——一个真人几周内
// 会攒下几十上百个 session_id。恒定不变的 session_id 会让上游看到一个
// 永不结束的会话，这是收敛后残留的生命周期特征。
//
// 按天轮换是最轻的无状态折中：调度、限流与 turn-state 溯源用的都是入站原始
// 标识（见 resolveCodexFingerprintIDsFromRequest 的调用点），不受影响；只有
// 出站身份里的 WS 粘性键与 prompt_cache_key 默认值会随桶各换一次，等价于
// 原生客户端的「新会话新连接」。
func codexSessionRotationBucket(now time.Time) string {
	return now.UTC().Format("2006-01-02")
}

// resolveConvergedSessionID 返回账号在当前轮换桶内的 session_id。
func resolveConvergedSessionID(seed string) string {
	return resolveConvergedSessionIDAt(seed, time.Now())
}

// resolveConvergedSessionIDAt 是 resolveConvergedSessionID 的可注入时间版本，
// 供测试跨越轮换边界而不依赖真实时钟。
func resolveConvergedSessionIDAt(seed string, now time.Time) string {
	if seed == "" {
		return ""
	}
	return deriveStableUUIDv4("sub2api:codex-session-id:v3:" + seed + ":" + codexSessionRotationBucket(now))
}

// resolveConvergedThreadID 按客户端原始 thread-id 确定性派生 thread_id。
// 每个真实 Codex 线程（主线程与各 subagent 线程）获得一个独立线程，
// 与 codex-rs 的线程模型一致。
//
// 派生输入必须是 thread-id 而非 session-id：真实客户端的 thread-id 头恒为
// 当前线程标识（codex-rs client.rs build_session_headers 的第二个参数），而
// session-id 头的语义是漂移的——根 agent 填的是 prompt_cache_key（可能是
// "{source}:{parent_thread_id}"），只有非根 agent 才填真 session_id。用
// session-id 派生会把父子线程收敛成同一个值，与随请求下发的
// parent_thread_id 自相矛盾。
//
// 前缀 v2→v3 表示派生输入变更：升级后同一客户端线程会得到新的收敛值
// （一次性重排，installation_id / session_id 不受影响）。
func resolveConvergedThreadID(seed, clientThreadID string) string {
	if seed == "" || clientThreadID == "" {
		return ""
	}
	return deriveStableUUIDv4("sub2api:codex-thread-id:v3:" + seed + ":" + clientThreadID)
}

// resolveConvergedParentThreadID 按同一派生函数映射父线程标识。
//
// 必须复用 resolveConvergedThreadID：parent_thread_id 的值就是父线程的
// thread_id，因此父线程自身发起请求时算出的收敛值与子线程回带的 parent
// 收敛值必须逐字节相同，否则上游会看到「父线程指向一个不存在的线程」。
func resolveConvergedParentThreadID(seed, clientParentThreadID string) string {
	return resolveConvergedThreadID(seed, clientParentThreadID)
}

// codexFingerprintIDs 收敛后的完整 ID 集合。
// 由 resolveCodexFingerprintIDs 一次性生成，同一个实例在头改写和体改写之间共享，
// 确保所有载体中的 turn_id 等随机字段一致。体改写时还会补记原始
// client_metadata.session_id，用于识别 root prompt_cache_key 的默认值。
type codexFingerprintIDs struct {
	accountID      int64
	mode           codexFingerprintMode
	installationID string
	sessionID      string
	threadID       string
	parentThreadID string
	// stripThreadReferences 表示本次收敛应剥离（而非改写）全部线程引用字段
	// ——父线程与 fork 来源。full 模式声明「1 设备 1 会话 1 线程」，其唯一原生
	// 对应物是根线程，而 codex-rs 只在非根 agent 上携带 parent_thread_id、只在
	// fork 出的线程上携带 forked_from_thread_id；继续携带会拼出「唯一的线程却有
	// 父线程 / 来源线程」这种原生不存在的形态。
	stripThreadReferences bool
	turnID                string
	windowID              string
	windowNumber          uint64
	// seed 是账号级派生种子。forked_from_thread_id 这类线程引用只存在于请求体
	// （codex-rs 为它只定义了 client_metadata / turn-metadata 键，没有独立 HTTP
	// 头），无法在 resolve 阶段从入站头提取，只能在 body 改写时就地派生。
	seed                          string
	turnStartedAtUnixMs           int64
	originalBodySessionID         string
	originalBodySessionIDCaptured bool
}

// codexFingerprintClient 是入站请求自报的原始标识集合，供收敛一次性解析。
// 与 codexFingerprintIDs（收敛后的出站值）严格区分：本结构只承载客户端原值。
type codexFingerprintClient struct {
	// threadID 取 thread-id 头，是当前线程的稳定标识，thread_id 的派生输入。
	threadID string
	// sessionID 取 session-id 头，仅在客户端缺失 thread-id 时作为派生回落。
	sessionID string
	// parentThreadID 取 x-codex-parent-thread-id 头，subagent 场景下为父线程标识。
	parentThreadID string
	// windowNumber 是客户端原始 window_id 的 ":n" 后缀，缺省为 0。
	windowNumber uint64
}

// formatCodexWindowID 拼出 codex-rs 形态的 window_id："{thread_id}:{window_number}"。
// 后缀与随请求下发的 window_number 字段同源（codex-rs session/mod.rs 的
// current_window 从同一个元组同时产出两者），任何一侧单独硬编码都会让上游
// 看到真实客户端不可能产生的组合。
func formatCodexWindowID(threadID string, windowNumber uint64) string {
	if threadID == "" {
		return ""
	}
	return threadID + ":" + strconv.FormatUint(windowNumber, 10)
}

// codexThreadDerivationInput 选 thread_id 的派生输入：优先 thread-id 头，
// 客户端缺失该头时回落到 session-id（v2 之前的历史行为）。
func codexThreadDerivationInput(client codexFingerprintClient) string {
	if client.threadID != "" {
		return client.threadID
	}
	return client.sessionID
}

// resolveCodexFingerprintIDs 按收敛模式计算出站 ID 集合。
// client 是客户端自报的原始标识（thread / session / parent / window 序号）。
// 返回 nil 表示 off 模式，不需要改写。
// 注意：包含随机生成的 turn_id，调用方必须只调用一次并共享结果给头改写和体改写。
func resolveCodexFingerprintIDs(account *Account, client codexFingerprintClient, mode codexFingerprintMode) *codexFingerprintIDs {
	if account == nil || mode == codexFingerprintOff {
		return nil
	}
	seed, ok := codexFingerprintSeed(account.Extra)
	if !ok {
		return nil
	}

	ids := &codexFingerprintIDs{
		accountID:           account.ID,
		mode:                mode,
		windowNumber:        client.windowNumber,
		seed:                seed,
		turnStartedAtUnixMs: time.Now().UnixMilli(),
	}

	ids.installationID = resolveConvergedInstallationID(account, seed)
	if ids.installationID == "" {
		return nil
	}

	switch mode {
	case codexFingerprintDevice:
		return ids

	case codexFingerprintSession:
		ids.sessionID = resolveConvergedSessionID(seed)
		ids.threadID = resolveConvergedThreadID(seed, codexThreadDerivationInput(client))
		if ids.threadID == "" {
			ids.threadID = ids.sessionID
		}
		ids.parentThreadID = resolveConvergedParentThreadID(seed, client.parentThreadID)
		ids.turnID = uuid.Must(uuid.NewV7()).String()
		ids.windowID = formatCodexWindowID(ids.threadID, ids.windowNumber)
		return ids

	case codexFingerprintFull:
		ids.sessionID = resolveConvergedSessionID(seed)
		ids.threadID = ids.sessionID
		// 剥离而非派生：full 模式把所有线程压成一个，上游看到的只能是原生里
		// 的根线程形态，而 codex-rs 的根线程既不带 parent_thread_id 也不带
		// forked_from_thread_id（两者都只在非根线程上出现）。
		ids.stripThreadReferences = true
		ids.turnID = uuid.Must(uuid.NewV7()).String()
		ids.windowID = formatCodexWindowID(ids.threadID, ids.windowNumber)
		return ids
	}

	return nil
}

// extractClientSessionID 从请求头中提取客户端原始的会话标识。
// 优先取 session-id（连字符形式，Codex CLI 标准），回退到 session_id（下划线形式）。
// 返回的值尚未被 isolateOpenAISessionID 改写，是客户端的真实标识。
func extractClientSessionID(h http.Header) string {
	if v := strings.TrimSpace(h.Get("session-id")); v != "" {
		return v
	}
	return strings.TrimSpace(h.Get("session_id"))
}

// extractClientThreadID 从请求头中提取客户端原始的线程标识。
// 优先取 thread-id（连字符形式，codex-rs build_session_headers 的标准形态），
// 回退到 thread_id（下划线形式）。该值尚未被任何隔离层改写，是客户端的真实标识。
func extractClientThreadID(h http.Header) string {
	if h == nil {
		return ""
	}
	if v := strings.TrimSpace(h.Get("thread-id")); v != "" {
		return v
	}
	return strings.TrimSpace(h.Get("thread_id"))
}

// extractClientParentThreadID 提取 subagent 场景下客户端回带的父线程标识。
// codex-rs 在 x-codex-parent-thread-id 头与 client_metadata 的同名键上同时下发。
func extractClientParentThreadID(h http.Header) string {
	if h == nil {
		return ""
	}
	return strings.TrimSpace(h.Get("x-codex-parent-thread-id"))
}

// extractClientWindowNumber 从客户端原始 window_id 头解析窗口序号。
// codex-rs 的 window_id 形如 "{thread_id}:{window_number}"，线程标识本身是 UUID
// （含连字符但不含冒号），故按最后一个冒号切分。解析不出时返回 0——与真实
// 客户端新建会话时的初值一致。
func extractClientWindowNumber(h http.Header) uint64 {
	if h == nil {
		return 0
	}
	raw := strings.TrimSpace(h.Get("x-codex-window-id"))
	idx := strings.LastIndex(raw, ":")
	if idx < 0 || idx == len(raw)-1 {
		return 0
	}
	number, err := strconv.ParseUint(raw[idx+1:], 10, 64)
	if err != nil {
		return 0
	}
	return number
}

// resolveCodexFingerprintIDsFromRequest 从客户端原始请求头中提取身份标识，
// 结合账号配置一次性解析收敛 ID 集合。调用方应将返回的 ids 同时传给
// applyCodexFingerprintHeaders 和 applyCodexFingerprintClientMetadata。
func resolveCodexFingerprintIDsFromRequest(account *Account, clientHeaders http.Header) *codexFingerprintIDs {
	if account == nil {
		return nil
	}
	mode := account.GetCodexFingerprintMode()
	if mode == codexFingerprintOff {
		// off 模式同样要观测：线上一半以上账号处于 off，「它们实际发出什么指纹」
		// 正是风控排查的起点，漏掉这一半等于闭着眼睛做优化。
		recordCodexFingerprintObservationFromClient(account, mode, clientHeaders, nil)
		return nil
	}
	var client codexFingerprintClient
	if clientHeaders != nil {
		client = codexFingerprintClient{
			threadID:       extractClientThreadID(clientHeaders),
			sessionID:      extractClientSessionID(clientHeaders),
			parentThreadID: extractClientParentThreadID(clientHeaders),
			windowNumber:   extractClientWindowNumber(clientHeaders),
		}
	}
	ids := resolveCodexFingerprintIDs(account, client, mode)
	recordCodexFingerprintObservationFromClient(account, mode, clientHeaders, ids)
	return ids
}

// recordCodexFingerprintObservationFromClient 采集一次出站指纹观测。
//
// ids 非 nil 时记录收敛后的派生值；为 nil（off 模式）时记录客户端原值——
// 两者都是「本次请求实际会发出去的那组标识」，因此可直接用于对照收敛前后
// 的基数变化。
//
// 注意：此处记录的是指纹层的输入/输出，**不含** account_identity 层随后的
// 账号级隔离改写；口径在观测侧统一即可，本函数不做跨层拼装。
func recordCodexFingerprintObservationFromClient(account *Account, mode codexFingerprintMode, clientHeaders http.Header, ids *codexFingerprintIDs) {
	if account == nil {
		return
	}
	obs := codexFingerprintObservation{
		AccountID: account.ID,
		Mode:      string(mode),
	}
	if ids != nil {
		obs.InstallationID = ids.installationID
		obs.SessionID = ids.sessionID
	} else if clientHeaders != nil {
		obs.InstallationID = strings.TrimSpace(clientHeaders.Get("x-codex-installation-id"))
		// 客户端侧 thread-id 才是稳定线程标识（session-id 在根 agent 上是
		// prompt_cache_key，语义漂移），故优先取它。
		obs.SessionID = extractClientThreadID(clientHeaders)
		if obs.SessionID == "" {
			obs.SessionID = extractClientSessionID(clientHeaders)
		}
	}
	if clientHeaders != nil {
		obs.Originator = strings.TrimSpace(clientHeaders.Get("originator"))
		obs.UserAgent = strings.TrimSpace(clientHeaders.Get("user-agent"))
	}
	recordCodexFingerprintObservation(context.Background(), obs)
}

// applyCodexFingerprintHeaders 按预计算的收敛 ID 改写出站 HTTP 头中的设备指纹。
// 在 buildUpstreamRequest 的白名单透传之后、enforceCodexIdentityHeaders 之前调用。
func applyCodexFingerprintHeaders(h http.Header, ids *codexFingerprintIDs) {
	if h == nil || ids == nil {
		return
	}

	// 所有非 off 模式都收敛 installation_id
	h.Set("x-codex-installation-id", ids.installationID)

	if ids.mode == codexFingerprintDevice {
		rewriteCodexTurnMetadataFields(h, map[string]any{
			"installation_id": ids.installationID,
		}, ids)
		return
	}

	// session / full 模式：改写所有相关头
	h.Set("x-codex-window-id", ids.windowID)
	h.Set("x-client-request-id", ids.threadID)
	// 连字符形式和下划线形式都改写，保证一致
	h.Set("session-id", ids.sessionID)
	h.Set("session_id", ids.sessionID)
	h.Set("thread-id", ids.threadID)
	// 父线程标识：full 模式剥离（其原生对应物是根线程，根线程无父线程）；
	// session 模式下与 thread_id 落在同一收敛空间，否则上游会看到父线程指向
	// 一个不存在的线程。只在客户端确实回带时改写，不给非 subagent 请求凭空补。
	if ids.stripThreadReferences {
		h.Del("x-codex-parent-thread-id")
	} else if ids.parentThreadID != "" && strings.TrimSpace(h.Get("x-codex-parent-thread-id")) != "" {
		h.Set("x-codex-parent-thread-id", ids.parentThreadID)
	}

	fields := map[string]any{
		"installation_id":         ids.installationID,
		"session_id":              ids.sessionID,
		"thread_id":               ids.threadID,
		"turn_id":                 ids.turnID,
		"window_id":               ids.windowID,
		"window_number":           ids.windowNumber,
		"turn_started_at_unix_ms": ids.turnStartedAtUnixMs,
	}
	switch {
	case ids.stripThreadReferences:
		fields["parent_thread_id"] = nil // nil 约定为删除该键
	case ids.parentThreadID != "":
		fields["parent_thread_id"] = ids.parentThreadID
	}
	rewriteCodexTurnMetadataFields(h, fields, ids)
}

// codexThreadReferenceKeys 是「值指向某个 thread_id」的 metadata 字段。
// 它们的值必须落在收敛空间内，否则上游会看到指向不存在线程的引用——与
// parent_thread_id 同类的问题。codex-rs 只为它们定义了 client_metadata /
// turn-metadata 键（responses_metadata.rs 的 FORKED_FROM_THREAD_ID_KEY），
// 没有独立 HTTP 头，因此无法在 resolve 阶段从入站头提取，只能就地派生。
var codexThreadReferenceKeys = []string{"forked_from_thread_id"}

// applyCodexThreadReferenceScope 就地把线程引用字段映射到收敛空间。
// full 模式声明只有根线程，而根线程不携带任何线程引用，故整体剥离。
// 返回是否发生改动。
func applyCodexThreadReferenceScope(values map[string]any, ids *codexFingerprintIDs) bool {
	if values == nil || ids == nil {
		return false
	}
	// device 模式不改写线程标识，线程引用自然也不该动。
	if ids.mode != codexFingerprintSession && ids.mode != codexFingerprintFull {
		return false
	}
	changed := false
	for _, key := range codexThreadReferenceKeys {
		if ids.stripThreadReferences {
			if _, ok := values[key]; ok {
				delete(values, key)
				changed = true
			}
			continue
		}
		raw, ok := values[key].(string)
		if !ok || ids.seed == "" {
			continue
		}
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		// 必须复用 thread 派生函数：来源线程自身发起请求时算出的 thread_id，
		// 与调用方在这里算出的引用值必须逐字节相同。
		if scoped := resolveConvergedThreadID(ids.seed, trimmed); scoped != "" && scoped != raw {
			values[key] = scoped
			changed = true
		}
	}
	return changed
}

// rewriteCodexTurnMetadataFields 解析 x-codex-turn-metadata 头中的 JSON，
// 替换或删除指定字段后回写。合法对象保留未指定字段（如 sandbox、thread_source）；
// 非法/非对象值重建为最小合法 metadata，避免 flat 与 embedded identity 分裂。
// fields 中 value 为 nil 表示删除该键（full 模式剥离线程引用要用）。
func rewriteCodexTurnMetadataFields(h http.Header, fields map[string]any, ids *codexFingerprintIDs) {
	raw := strings.TrimSpace(h.Get("x-codex-turn-metadata"))
	if raw == "" {
		return
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata == nil {
		metadata = make(map[string]any, len(fields))
	}
	for k, v := range fields {
		if v == nil {
			delete(metadata, k)
			continue
		}
		metadata[k] = v
	}
	applyCodexThreadReferenceScope(metadata, ids)
	rebuilt, err := json.Marshal(metadata)
	if err != nil {
		return
	}
	h.Set("x-codex-turn-metadata", string(rebuilt))
}

// applyCodexFingerprintClientMetadata 按预计算的收敛 ID 改写请求体中的 client_metadata。
// 使用与头改写相同的 ids 实例，确保 turn_id 等随机字段一致。
func applyCodexFingerprintClientMetadata(reqBody map[string]any, ids *codexFingerprintIDs) bool {
	if reqBody == nil || ids == nil {
		return false
	}

	captureCodexFingerprintOriginalBodySessionID(ids, reqBody["client_metadata"])
	existing, _ := reqBody["client_metadata"].(map[string]any)
	if existing == nil {
		existing = make(map[string]any)
	}

	modified := false
	if applyCodexFingerprintToClientMetadataMap(existing, ids) {
		reqBody["client_metadata"] = existing
		modified = true
	}
	if applyCodexFingerprintPromptCacheKey(reqBody, ids) {
		modified = true
	}
	return modified
}

// nativeCodexClientMetadataTopLevelKeys 是 codex-rs 会在 client_metadata **顶层**
// 产出的全部键（responses_metadata.rs 的 client_metadata() 构造）。
//
// 上游把这一层当作 map<string,string> 校验，因此收敛只允许在集合内改写：
//   - 写入原生不存在的键会被上游 400 拒绝（2026-09-13 的 window_number 事故：
//     window_number 只存在于内嵌 turn-metadata JSON，顶层没有这个键）；
//   - 写入非字符串值同样会被拒绝。
//
// 新增字段前必须在 codex-rs 侧确认它确实出现在顶层构造里，不能凭「同一个
// metadata 里见过」就推断它在顶层。锁由
// TestCodexFingerprint_ClientMetadataTopLevelStaysWithinNativeKeySet 执行。
var nativeCodexClientMetadataTopLevelKeys = map[string]bool{
	"x-codex-installation-id":  true,
	"session_id":               true,
	"thread_id":                true,
	"x-codex-window-id":        true,
	"turn_id":                  true,
	"x-openai-subagent":        true,
	"x-codex-parent-thread-id": true,
	"parent_turn_id":           true,
	"root_turn_id":             true,
	"x-codex-turn-metadata":    true,
}

// applyCodexFingerprintToClientMetadataMap 是 client_metadata 改写的共享核心，
// map 版（非透传，body 已解码）与 raw 字节版（透传热路径）都经由它，保证两条
// 路径的收敛语义永不漂移。
func applyCodexFingerprintToClientMetadataMap(existing map[string]any, ids *codexFingerprintIDs) bool {
	if existing == nil || ids == nil {
		return false
	}

	modified := false

	if ids.installationID != "" {
		existing["x-codex-installation-id"] = ids.installationID
		modified = true
	}

	if ids.mode == codexFingerprintDevice {
		rewriteClientMetadataEmbeddedTurnMetadata(existing, map[string]any{
			"installation_id": ids.installationID,
		}, ids)
		return modified
	}

	// session / full 模式
	existing["session_id"] = ids.sessionID
	existing["thread_id"] = ids.threadID
	existing["turn_id"] = ids.turnID
	existing["x-codex-window-id"] = ids.windowID
	// 注意：window_number **不是** client_metadata 顶层的字段。codex-rs 的
	// responses_metadata.rs 只在内嵌的 turn-metadata JSON 里产出它，顶层
	// client_metadata 是 map<string,string>；往顶层写这个键会被上游以
	// "expected a string, but got an integer" 直接 400 拒绝（2026-09-13 事故）。
	// 顶层与内嵌两个载体的字段集由 TestCodexFingerprint_ClientMetadataTopLevelStaysWithinNativeKeySet 锁定。
	// 父线程标识。codex-rs 在 client_metadata 顶层用的键是头名
	// （x-codex-parent-thread-id），内嵌 turn-metadata JSON 里才是 parent_thread_id。
	if ids.stripThreadReferences {
		delete(existing, "x-codex-parent-thread-id")
	} else if ids.parentThreadID != "" {
		if raw, ok := existing["x-codex-parent-thread-id"].(string); ok && strings.TrimSpace(raw) != "" {
			existing["x-codex-parent-thread-id"] = ids.parentThreadID
		}
	}

	fields := map[string]any{
		"installation_id":         ids.installationID,
		"session_id":              ids.sessionID,
		"thread_id":               ids.threadID,
		"turn_id":                 ids.turnID,
		"window_id":               ids.windowID,
		"window_number":           ids.windowNumber,
		"turn_started_at_unix_ms": ids.turnStartedAtUnixMs,
	}
	switch {
	case ids.stripThreadReferences:
		fields["parent_thread_id"] = nil // nil 约定为删除该键
	case ids.parentThreadID != "":
		fields["parent_thread_id"] = ids.parentThreadID
	}
	// 线程引用字段（forked_from_thread_id）与 thread_id 同域，且只存在于
	// metadata 载体里——client_metadata 顶层与内嵌 turn-metadata JSON 都要处理。
	applyCodexThreadReferenceScope(existing, ids)
	rewriteClientMetadataEmbeddedTurnMetadata(existing, fields, ids)
	return true
}

func captureCodexFingerprintOriginalBodySessionID(ids *codexFingerprintIDs, clientMetadata any) {
	if ids == nil || ids.originalBodySessionIDCaptured {
		return
	}
	ids.originalBodySessionIDCaptured = true
	if clientMetadata == nil {
		return
	}
	switch metadata := clientMetadata.(type) {
	case map[string]any:
		if sessionID, ok := metadata["session_id"].(string); ok {
			ids.originalBodySessionID = strings.TrimSpace(sessionID)
		}
	case map[string]string:
		ids.originalBodySessionID = strings.TrimSpace(metadata["session_id"])
	}
}

func captureCodexFingerprintOriginalBodySessionIDRaw(ids *codexFingerprintIDs, value gjson.Result) {
	if ids == nil || ids.originalBodySessionIDCaptured {
		return
	}
	ids.originalBodySessionIDCaptured = true
	if value.Exists() && value.Type == gjson.String {
		ids.originalBodySessionID = strings.TrimSpace(value.String())
	}
}

func shouldRewriteCodexFingerprintPromptCacheKey(ids *codexFingerprintIDs, promptCacheKey string) bool {
	if ids == nil || !ids.originalBodySessionIDCaptured || ids.originalBodySessionID == "" || ids.sessionID == "" {
		return false
	}
	if ids.mode != codexFingerprintSession && ids.mode != codexFingerprintFull {
		return false
	}
	return promptCacheKey == ids.originalBodySessionID
}

func applyCodexFingerprintPromptCacheKey(reqBody map[string]any, ids *codexFingerprintIDs) bool {
	if reqBody == nil {
		return false
	}
	promptCacheKey, ok := reqBody["prompt_cache_key"].(string)
	if !ok || strings.TrimSpace(promptCacheKey) == "" || !shouldRewriteCodexFingerprintPromptCacheKey(ids, promptCacheKey) {
		return false
	}
	if promptCacheKey == ids.sessionID {
		return false
	}
	reqBody["prompt_cache_key"] = ids.sessionID
	return true
}

// applyCodexFingerprintClientMetadataRaw 在原始 JSON 字节上改写 client_metadata，
// 供透传路径使用——透传是热路径，禁止对可能高达数十 MB 的 body 做全量
// Unmarshal（见 forwardOpenAIPassthrough 的轻量提取注释）。实现为：gjson 提取
// client_metadata 小对象单独解码，经共享核心改写后 sjson 一次性拼回，body
// 其余字节原样保留；root prompt_cache_key 仅在可证明是 body session 默认值时
// 做标量改写。语义与 applyCodexFingerprintClientMetadata 逐点一致（含
// "非对象值整体替换为收敛集合"的行为）。
func applyCodexFingerprintClientMetadataRaw(body []byte, ids *codexFingerprintIDs) ([]byte, bool, error) {
	if len(body) == 0 || ids == nil {
		return body, false, nil
	}
	// 非 JSON 对象的 body（数组/标量/畸形）没有 client_metadata 语义，
	// sjson 在这类根上写字段会改写整体结构，直接放行保持原样。
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		captureCodexFingerprintOriginalBodySessionIDRaw(ids, gjson.Result{})
		return body, false, nil
	}

	existing := map[string]any{}
	if cm := gjson.GetBytes(body, "client_metadata"); cm.IsObject() {
		captureCodexFingerprintOriginalBodySessionIDRaw(ids, gjson.GetBytes(body, "client_metadata.session_id"))
		if err := json.Unmarshal([]byte(cm.Raw), &existing); err != nil {
			return body, false, fmt.Errorf("decode client_metadata for fingerprint: %w", err)
		}
	} else {
		captureCodexFingerprintOriginalBodySessionIDRaw(ids, gjson.Result{})
	}

	next := body
	modified := false
	if applyCodexFingerprintToClientMetadataMap(existing, ids) {
		raw, err := json.Marshal(existing)
		if err != nil {
			return body, false, fmt.Errorf("encode converged client_metadata: %w", err)
		}
		var setErr error
		next, setErr = sjson.SetRawBytes(body, "client_metadata", raw)
		if setErr != nil {
			return body, false, fmt.Errorf("splice converged client_metadata: %w", setErr)
		}
		modified = true
	}
	promptCacheKey := gjson.GetBytes(body, "prompt_cache_key")
	if promptCacheKey.Exists() && promptCacheKey.Type == gjson.String && strings.TrimSpace(promptCacheKey.String()) != "" && shouldRewriteCodexFingerprintPromptCacheKey(ids, promptCacheKey.String()) {
		rewritten, err := sjson.SetBytes(next, "prompt_cache_key", ids.sessionID)
		if err != nil {
			return body, false, fmt.Errorf("splice converged prompt_cache_key: %w", err)
		}
		next = rewritten
		modified = true
	}
	return next, modified, nil
}

// rewriteClientMetadataEmbeddedTurnMetadata 改写 client_metadata 中内嵌的
// x-codex-turn-metadata JSON 字符串里的指定字段。非法/非对象值会重建，
// 避免 flat client_metadata 与 embedded metadata 暴露两套身份。
func rewriteClientMetadataEmbeddedTurnMetadata(clientMetadata map[string]any, fields map[string]any, ids *codexFingerprintIDs) {
	raw, ok := clientMetadata["x-codex-turn-metadata"].(string)
	if !ok || raw == "" {
		return
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata == nil {
		metadata = make(map[string]any, len(fields))
	}
	for k, v := range fields {
		if v == nil {
			delete(metadata, k)
			continue
		}
		metadata[k] = v
	}
	applyCodexThreadReferenceScope(metadata, ids)
	if rebuilt, err := json.Marshal(metadata); err == nil {
		clientMetadata["x-codex-turn-metadata"] = string(rebuilt)
	}
}
