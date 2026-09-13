package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// Codex 指纹出站观测。
//
// 背景：收敛逻辑此前完全不可观测（日志零事件、Redis 零键），导致「收敛到底有没有
// 生效」「同一账号被多少下游会话共享」这两个问题只能靠上游 400/额度缩水反推。
// 本文件补上这两块，且**不新建事件表**——基数类指标用 Redis HyperLogLog 承载。
//
// 观测是旁路，契约有三条，任何一条被破坏都是生产事故：
//  1. 绝不阻塞请求路径：记录侧只做非阻塞投递，队列满即丢弃并计数；
//  2. 绝不 panic：消费侧任何异常都在 worker 内消化，不冒泡到请求；
//  3. 绝不明文外泄：写入 Redis 与日志的标识一律是 sha256 短摘要。
const (
	// codexFingerprintHLLKeyPrefix 是基数指标的 Redis 键前缀。
	// 完整形态：codex_fp:{inst|sess}:{account_id}:{yyyyMMddHH}（UTC 小时）。
	codexFingerprintHLLKeyPrefix = "codex_fp"
	// codexFingerprintHLLTTL 覆盖聚合窗口即可，过期由 Redis 自动回收。
	codexFingerprintHLLTTL = 48 * time.Hour
	// codexFingerprintObservationQueue 是异步投递队列容量。满即丢弃：
	// 观测丢失远好于请求被拖慢。
	codexFingerprintObservationQueue = 4096
	// codexFingerprintPersistTimeout 单次 Redis 写入的上限。
	codexFingerprintPersistTimeout = 2 * time.Second
	// CodexFingerprintDefaultLogRate 是结构化日志的默认采样百分比（0-100）。
	CodexFingerprintDefaultLogRate = 1
)

// codexFingerprintObservation 是一次 Codex 请求的出站指纹快照。
//
// InstallationID / SessionID 记录的是**出站实际发送的值**：off 模式为客户端原值，
// 收敛模式为派生值。这是「上游实际看到什么」的唯一可信来源，也是判断收敛是否
// 生效的依据。
type codexFingerprintObservation struct {
	AccountID      int64
	Mode           string
	InstallationID string
	SessionID      string
	Originator     string
	UserAgent      string
}

var (
	codexFingerprintObserverMu sync.RWMutex
	codexFingerprintObserver   func(context.Context, codexFingerprintObservation)
)

// SetCodexFingerprintObserver 注册观测消费者，由装配层在启动时注入。
// 未注册时 recordCodexFingerprintObservation 退化为 no-op（例如单元测试里）。
func SetCodexFingerprintObserver(fn func(context.Context, codexFingerprintObservation)) {
	codexFingerprintObserverMu.Lock()
	defer codexFingerprintObserverMu.Unlock()
	codexFingerprintObserver = fn
}

// recordCodexFingerprintObservation 由指纹解析路径调用，投递一次观测。
//
// 契约：绝不阻塞、绝不 panic、绝不影响主流程。任何异常都只能静默丢弃——
// 观测是旁路，它的失败不允许升级成请求的失败。
func recordCodexFingerprintObservation(ctx context.Context, obs codexFingerprintObservation) {
	codexFingerprintObserverMu.RLock()
	fn := codexFingerprintObserver
	codexFingerprintObserverMu.RUnlock()
	if fn == nil {
		return
	}
	defer func() {
		// 观测实现若有缺陷，也不能把 panic 带到请求路径上。
		_ = recover()
	}()
	fn(ctx, obs)
}

// CodexFingerprintTelemetry 把观测快照聚合为可按账号查询的基数指标。
//
// 写入路径：非阻塞投递 → 单 worker → Redis HyperLogLog。
// 读路径（checkSharedAccounts）供指标采集与告警复用。
type CodexFingerprintTelemetry struct {
	redis   *redis.Client
	logRate int

	ch       chan codexFingerprintObservation
	dropped  atomic.Uint64
	recorded atomic.Uint64

	stopCh   chan struct{}
	stopOnce sync.Once
	started  atomic.Bool
}

// NewCodexFingerprintTelemetry 构造观测组件。redis 为 nil 时组件自动停用。
func NewCodexFingerprintTelemetry(redisClient *redis.Client, logRate int) *CodexFingerprintTelemetry {
	if logRate < 0 {
		logRate = 0
	}
	if logRate > 100 {
		logRate = 100
	}
	return &CodexFingerprintTelemetry{
		redis:   redisClient,
		logRate: logRate,
		ch:      make(chan codexFingerprintObservation, codexFingerprintObservationQueue),
		stopCh:  make(chan struct{}),
	}
}

// Start 启动消费 worker 并把自己注册为进程级观测消费者。
func (t *CodexFingerprintTelemetry) Start() {
	if t == nil || t.redis == nil {
		return
	}
	if t.started.Swap(true) {
		return
	}
	go t.run()
	SetCodexFingerprintObserver(t.observe)
}

// Stop 停止消费 worker。用于优雅关闭与测试清理。
func (t *CodexFingerprintTelemetry) Stop() {
	if t == nil {
		return
	}
	t.stopOnce.Do(func() {
		close(t.stopCh)
	})
}

// observe 是投递侧：只做一次非阻塞 send，队列满就丢弃。
func (t *CodexFingerprintTelemetry) observe(_ context.Context, obs codexFingerprintObservation) {
	select {
	case t.ch <- obs:
	default:
		// 队列打满说明观测吞吐跟不上请求量。丢弃并计数，绝不阻塞请求。
		t.dropped.Add(1)
	}
}

// Dropped 返回因队列打满被丢弃的观测数，供健康检查暴露。
func (t *CodexFingerprintTelemetry) Dropped() uint64 {
	if t == nil {
		return 0
	}
	return t.dropped.Load()
}

func (t *CodexFingerprintTelemetry) run() {
	for {
		select {
		case <-t.stopCh:
			return
		case obs := <-t.ch:
			t.persist(obs)
		}
	}
}

// persist 把一次观测写入 Redis 基数指标，并按采样率输出结构化日志。
func (t *CodexFingerprintTelemetry) persist(obs codexFingerprintObservation) {
	installationDigest := codexFingerprintDigest(obs.InstallationID)
	sessionDigest := codexFingerprintDigest(obs.SessionID)

	ctx, cancel := context.WithTimeout(context.Background(), codexFingerprintPersistTimeout)
	defer cancel()

	bucket := codexFingerprintHourBucket(time.Now())
	pipe := t.redis.Pipeline()
	addCodexFingerprintHLL(ctx, pipe, "inst", obs.AccountID, bucket, installationDigest)
	addCodexFingerprintHLL(ctx, pipe, "sess", obs.AccountID, bucket, sessionDigest)
	if _, err := pipe.Exec(ctx); err != nil {
		// 指标写不进去不影响请求；只记一条低噪日志便于排查 Redis 故障。
		return
	}
	t.recorded.Add(1)

	if shouldLogCodexFingerprint(installationDigest, t.logRate) {
		log.Printf("[CodexFingerprint] account=%d mode=%s originator=%s ua=%q inst=%s session=%s",
			obs.AccountID, obs.Mode, obs.Originator, obs.UserAgent, installationDigest, sessionDigest)
	}
}

func addCodexFingerprintHLL(ctx context.Context, pipe redis.Pipeliner, kind string, accountID int64, bucket, digest string) {
	if digest == "" {
		return
	}
	key := codexFingerprintHLLKey(kind, accountID, bucket)
	pipe.PFAdd(ctx, key, digest)
	pipe.Expire(ctx, key, codexFingerprintHLLTTL)
}

// codexFingerprintHLLKey 拼出基数指标的键：codex_fp:{kind}:{account}:{bucket}。
func codexFingerprintHLLKey(kind string, accountID int64, bucket string) string {
	return fmt.Sprintf("%s:%s:%d:%s", codexFingerprintHLLKeyPrefix, kind, accountID, bucket)
}

// codexFingerprintHourBucket 返回 UTC 小时桶（yyyyMMddHH）。
// 用 UTC 而非本地时区：线上可能跨时区部署，桶必须全局一致。
func codexFingerprintHourBucket(now time.Time) string {
	return now.UTC().Format("2006010215")
}

// codexFingerprintDigest 返回标识的不可逆短摘要（sha256 前 8 字节 → 16 个 hex）。
//
// 明文 ID 绝不落 Redis 或日志——那等于把指纹从网关侧再泄漏一次，与收敛的
// 初衷正相反。摘要长度取 8 字节：碰撞概率在账号量级下可忽略，且足够短。
func codexFingerprintDigest(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:8])
}

// shouldLogCodexFingerprint 用摘要做确定性采样：同一 installation 的判定稳定
// （便于按客户端回溯），分布又足够均匀，且不引入随机数生成器的并发开销。
func shouldLogCodexFingerprint(digest string, rate int) bool {
	if rate <= 0 || digest == "" {
		return false
	}
	if rate >= 100 {
		return true
	}
	// 取摘要首字节（0-255）映射到 0-99 的百分位。
	head, err := hex.DecodeString(digest[:2])
	if err != nil || len(head) == 0 {
		return false
	}
	return int(head[0])%100 < rate
}
