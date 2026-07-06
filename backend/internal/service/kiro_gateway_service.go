package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

// KiroRateLimiter 是 Kiro 网关在上游 429 时持久化账号限流冷却所需的最小依赖,
// 由 *RateLimitService 实现(见 wire.Bind)。抽成窄接口便于测试注入 spy。
type KiroRateLimiter interface {
	HandleUpstreamError(ctx context.Context, account *Account, statusCode int, headers http.Header, responseBody []byte, requestedModel ...string) bool
}

// KiroGatewayService 处理 Kiro 账号的 Anthropic /v1/messages 请求。
// Kiro 账号挂在 anthropic 分组下,由 GatewayHandler 按 account.Platform 分流到此。
// 账号选择、并发获取、计费由 handler 负责;本服务只做:取 token → 转换 → 调用上游 → 回写响应。
type KiroGatewayService struct {
	tokenProvider    *KiroTokenProvider
	clientFor        KiroHTTPClientFactory
	cfg              kiro.ClientConfig
	cacheTracker     *kiro.CacheTracker
	rateLimitService KiroRateLimiter
	accountRepo      AccountRepository          // 仅用于熔断 trip 时 SetTempUnschedulable
	breaker          *kiroFailureCircuitBreaker // 重复瞬时失败(5xx/连接级)熔断
}

func NewKiroGatewayService(tokenProvider *KiroTokenProvider, clientFor KiroHTTPClientFactory, cfg kiro.ClientConfig, cacheTracker *kiro.CacheTracker, rateLimitService KiroRateLimiter, accountRepo AccountRepository) *KiroGatewayService {
	if clientFor == nil {
		clientFor = func(string) (*http.Client, error) { return http.DefaultClient, nil }
	}
	return &KiroGatewayService{
		tokenProvider:    tokenProvider,
		clientFor:        clientFor,
		cfg:              cfg,
		cacheTracker:     cacheTracker,
		rateLimitService: rateLimitService,
		accountRepo:      accountRepo,
		breaker:          newKiroFailureCircuitBreaker(),
	}
}

// ProvideKiroCacheTracker 提供进程级单例的合成提示词缓存追踪器(wire）。
func ProvideKiroCacheTracker() *kiro.CacheTracker {
	return kiro.NewCacheTracker()
}

// Forward 处理一次请求。streaming 请求把 Anthropic SSE 写入 c.Writer;
// 非流式请求把拼装后的完整消息 JSON 写入 c.Writer。返回 ForwardResult 供 handler 计费。
func (s *KiroGatewayService) Forward(ctx context.Context, c *gin.Context, account *Account, body []byte, isStickySession bool) (*ForwardResult, error) {
	_ = isStickySession // Kiro 暂不使用粘性会话
	start := time.Now()

	var req kiro.AnthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}

	token, err := s.tokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}

	cred := AccountToKiroCredentials(account)
	if !cred.IsAPIKeyCredential() {
		// 使用 provider 返回的(可能已刷新的)access token 作为 Bearer。
		cred.AccessToken = token
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	client, err := s.clientFor(proxyURL)
	if err != nil {
		return nil, err
	}

	// 合成提示词缓存(路线①:注入客户端 usage 且进 token 计费,与真实 credit 双轨)。
	// 请求前只读 Compute,上游成功后再 Update,避免失败请求污染本地缓存表。
	var cacheProfile *kiro.CacheProfile
	var cacheRes kiro.CacheResult
	var cachePtr *kiro.CacheResult
	if s.cacheTracker != nil {
		cacheProfile = s.cacheTracker.BuildProfile(&req, body, 0)
		cacheRes = s.cacheTracker.Compute(account.ID, cacheProfile)
		cachePtr = &cacheRes
	}

	// 记录 Forward 前已写入字节数,用于 pre-first-byte 不变量判定:
	// 一旦已向客户端写出 SSE 字节,禁止 failover(避免流拼接腐化),与 handler 的
	// writerSizeBeforeForward 保持一致。
	writerSizeBefore := c.Writer.Size()

	if req.Stream {
		h := c.Writer.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		h.Set("X-Accel-Buffering", "no") // 禁止 nginx 等反代缓冲,保证实时流
		res, streamErr := kiro.StreamMessages(ctx, client, &cred, s.cfg, &req, c.Writer, cachePtr)
		if streamErr != nil {
			// 可重试上游错误(429/5xx/连接级)且首字节前:返回 failover 让 handler 跨账号重试。
			if failoverErr := s.forwardUpstreamFailover(c, account, streamErr, writerSizeBefore); failoverErr != nil {
				// 尚未写出任何字节即换号:清掉此前预设的 SSE 头,避免污染后续账号/最终错误响应。
				h.Del("Content-Type")
				h.Del("Cache-Control")
				h.Del("Connection")
				h.Del("X-Accel-Buffering")
				return nil, failoverErr
			}
			// 首字节前失败(如客户端类 4xx):SSE 头尚未提交,改写为带真实上游状态的 JSON 错误。
			// 若流已开始,writeUpstreamError 会自行 no-op,交由上层追加 SSE error 事件。
			s.writeUpstreamError(c, streamErr)
		} else {
			// 上游成功:清零熔断计数,并把本轮可缓存前缀写入缓存表,供后续轮次命中。
			s.breaker.reset(account.ID)
			if cacheProfile != nil {
				s.cacheTracker.Update(account.ID, cacheProfile)
			}
		}
		return s.buildForwardResult(req.Model, res, true, start, cacheRes), streamErr
	}

	msg, res, err := kiro.CollectMessages(ctx, client, &cred, s.cfg, &req, cachePtr)
	if err != nil {
		if failoverErr := s.forwardUpstreamFailover(c, account, err, writerSizeBefore); failoverErr != nil {
			return nil, failoverErr
		}
		s.writeUpstreamError(c, err)
		return s.buildForwardResult(req.Model, res, false, start, cacheRes), err
	}
	// 上游成功:清零熔断计数(即便随后编码/客户端写出失败,上游本身是健康的)。
	s.breaker.reset(account.ID)
	c.Writer.Header().Set("Content-Type", "application/json")
	if encErr := json.NewEncoder(c.Writer).Encode(msg); encErr != nil {
		return s.buildForwardResult(req.Model, res, false, start, cacheRes), encErr
	}
	if cacheProfile != nil {
		s.cacheTracker.Update(account.ID, cacheProfile)
	}
	return s.buildForwardResult(req.Model, res, false, start, cacheRes), nil
}

// writeUpstreamError 在尚未写出任何响应体时,把 Kiro 上游错误改写为 Anthropic 风格的 JSON 错误,
// 并带上真实上游状态码。若已有内容写出(流已开始),则不做处理(交由上层追加 SSE error 事件)。
func (s *KiroGatewayService) writeUpstreamError(c *gin.Context, err error) {
	if c.Writer.Size() > 0 {
		return
	}
	status := http.StatusBadGateway
	message := "Upstream request failed"
	var ue *kiro.UpstreamError
	if errors.As(err, &ue) {
		if ue.StatusCode > 0 {
			status = ue.StatusCode
		}
		if m := extractKiroErrorMessage(ue.Body); m != "" {
			message = m
		}
	}
	h := c.Writer.Header()
	h.Set("Content-Type", "application/json")
	h.Del("Cache-Control")
	h.Del("Connection")
	h.Del("X-Accel-Buffering")
	c.Writer.WriteHeader(status)
	_ = json.NewEncoder(c.Writer).Encode(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": kiroErrorType(status), "message": message},
	})
}

// forwardUpstreamFailover 记录 Kiro 上游错误的 ops 遥测,并判断是否应触发 handler 层的跨账号 failover。
//
// 返回非 nil 的 *UpstreamFailoverError 时:错误可重试(429 / 5xx / 连接级错误)且尚未向客户端写出
// 任何字节(pre-first-byte),调用方应把它返回给 handler 触发换号重试,且不得再向客户端写任何内容。
// 返回 nil 时:错误为终止性客户端错误(4xx),或流已开始(已写出 SSE 字节)无法换号,
// 调用方应回退到原有的 writeUpstreamError 逻辑。
//
// 无论是否 failover,都会记录一次 ops 上游错误事件(mirror AntigravityGatewayService)。
func (s *KiroGatewayService) forwardUpstreamFailover(c *gin.Context, account *Account, err error, writerSizeBefore int) *UpstreamFailoverError {
	if c == nil {
		return nil
	}
	status, respBody, respHeaders, retryable := classifyKiroUpstreamError(err)

	// 上游错误消息:UpstreamError 从响应体提取;连接级错误退化为(脱敏后的)错误串。
	var message string
	var ue *kiro.UpstreamError
	if errors.As(err, &ue) {
		message = extractKiroErrorMessage(ue.Body)
	} else {
		message = err.Error()
	}
	message = sanitizeUpstreamErrorMessage(strings.TrimSpace(message))

	// 始终记录上游上下文,供 ops 错误日志捕获真实上游状态(即便随后 failover)。
	setOpsUpstreamError(c, status, message, "")

	// 把有账号级语义的上游错误路由进共享账号健康态机器(与 ChatGPT/OpenAI 账号一致——复用
	// RateLimitService.HandleUpstreamError,尊重池模式/自定义错误码策略并发通知):
	//   401 → 失效 token 缓存 + OAuth 临时下线(缺 refresh_token 或 apikey 则永久禁用);
	//   402 → 计费死号永久禁用;403 → 账号级连续计数临时下线(达阈值才禁用,见 handle403 的 Kiro 分支);
	//   429 → 限流冷却(honor Retry-After / 默认冷却);529 → 过载冷却(overload_until)。
	// 与 in-request failover 互补:failover 处理当前这一发,健康态把坏号排除出后续选号,到点自动恢复。
	// 5xx / 连接级错误不入此机器(视为瞬时,仅请求内 failover)。用 detached context 写入,避免客户端
	// 在 failover 中途断开导致状态写入被取消。
	if s.rateLimitService != nil && kiroStatusEntersHealthState(status) {
		healthCtx := context.Background()
		if c.Request != nil {
			healthCtx = context.WithoutCancel(c.Request.Context())
		}
		s.rateLimitService.HandleUpstreamError(healthCtx, account, status, respHeaders, respBody)
	}

	// 重复瞬时失败(5xx / 连接级,非账号级)熔断:坏号累计到阈值 → 临时下线,避免每请求都强制换号。
	// 账号级状态(401/402/403/429/529)已进健康态机器,不在此重复统计。连接级(status 0)计入「连续」阈值。
	// detached ctx 避免客户端中途断连导致 SetTempUnschedulable 写入被取消。
	if s.breaker != nil && retryable && !kiroStatusEntersHealthState(status) {
		if until, reason, tripped := s.breaker.recordFailure(account.ID, status == 0, time.Now()); tripped && s.accountRepo != nil {
			breakerCtx := context.Background()
			if c.Request != nil {
				breakerCtx = context.WithoutCancel(c.Request.Context())
			}
			_ = s.accountRepo.SetTempUnschedulable(breakerCtx, account.ID, until, reason)
		}
	}

	requestID := kiroUpstreamRequestID(respHeaders)

	// pre-first-byte 不变量:已向客户端写出 SSE 字节时禁止 failover。
	streamAlreadyStarted := c.Writer.Size() != writerSizeBefore

	if retryable && !streamAlreadyStarted {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: status,
			UpstreamRequestID:  requestID,
			Kind:               "failover",
			Message:            message,
		})
		return &UpstreamFailoverError{
			StatusCode:             status,
			ResponseBody:           respBody,
			ResponseHeaders:        respHeaders,
			RetryableOnSameAccount: kiroSameAccountRetryable(status),
		}
	}

	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: status,
		UpstreamRequestID:  requestID,
		Kind:               "http_error",
		Message:            message,
	})
	return nil
}

// kiroStatusEntersHealthState 判定哪些上游状态码应路由进 RateLimitService 的账号健康态机器。
// 只纳入有明确账号级语义的状态(auth / billing / forbidden / rate-limit / overload);
// 5xx 与连接级错误视为瞬时,只做请求内 failover,不改账号状态。与 OpenAI 网关口径一致
// (openai_gateway_service.go 的 401/402/403/429/529)。
func kiroStatusEntersHealthState(status int) bool {
	switch status {
	case http.StatusUnauthorized, // 401
		http.StatusPaymentRequired, // 402
		http.StatusForbidden,       // 403
		http.StatusTooManyRequests, // 429
		529:                        // 529 overload(http 包无此常量,与代码库其余处一致用字面量)
		return true
	}
	return false
}

// kiroSameAccountRetryable 判定「瞬时、非账号级」的可 failover 错误——这类错误在**同一账号**上
// 先重试(而非立刻跨号)能保住该账号的合成前缀缓存:cache_tracker 按 account.ID 隔离,跨号必冷启动
// → 整轮 cache_read 归零。只纳入连接级(status 0:net.OpError/超时,pre-first-byte)与 503(上游
// 瞬时过载);429(限流,重试本号无意义且已进健康态机器冷却)与其他 5xx(持续故障,换号更优)仍跨号。
// 失败循环的每账号 3 次上限 / 500ms 延迟 / pre-first-byte 守卫自动生效(见 handler/failover_loop.go),
// 无需在此加计数或 cap。
func kiroSameAccountRetryable(status int) bool {
	return status == 0 || status == http.StatusServiceUnavailable
}

// classifyKiroUpstreamError 解析 Kiro Forward 错误,返回上游状态码、响应体、响应头,以及是否可 failover 重试。
// 可重试(跨账号):401 / 403 / 429 / 5xx(>=500)上游状态,或连接级(网络)错误——这些是「账号级」
// 问题(token 失效 / 权限 / 限流 / 上游抖动),换个健康账号常能成功(与 AntigravityGatewayService
// 的 shouldFailoverUpstreamError 对齐:401/403/429/529/5xx 均 failover)。401/403 同时也进健康态机器
// 做冷却/禁用(见 kiroStatusEntersHealthState),两者互补。
// 终止(不 failover):400/404 等客户端错误、请求构造/转换错误、context 取消等。
func classifyKiroUpstreamError(err error) (status int, body []byte, headers http.Header, retryable bool) {
	var ue *kiro.UpstreamError
	if errors.As(err, &ue) {
		if ue.Body != "" {
			body = []byte(ue.Body)
		}
		retryable = ue.StatusCode == http.StatusUnauthorized || // 401
			ue.StatusCode == http.StatusForbidden || // 403
			ue.StatusCode == http.StatusTooManyRequests || // 429
			ue.StatusCode >= 500
		return ue.StatusCode, body, ue.Headers, retryable
	}
	if isKiroConnectionError(err) {
		// 连接级错误:无上游状态码,视为可 failover(切换账号/代理后可能成功)。
		return 0, nil, nil, true
	}
	// 其他错误(请求构造 / 模型转换 / context 取消等):终止,不 failover。
	return 0, nil, nil, false
}

// isKiroConnectionError 判断是否为连接级(网络)错误。镜像 isAntigravityConnectionError,
// 但排除 context.Canceled(客户端主动断开,不应换号重试)。
func isKiroConnectionError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

// kiroUpstreamRequestID 尽力从上游响应头提取请求 ID(AWS 事件流网关返回 x-amzn-RequestId)。
func kiroUpstreamRequestID(h http.Header) string {
	if h == nil {
		return ""
	}
	for _, k := range []string{"x-amzn-RequestId", "x-request-id"} {
		if v := strings.TrimSpace(h.Get(k)); v != "" {
			return v
		}
	}
	return ""
}

func extractKiroErrorMessage(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	var obj map[string]any
	if json.Unmarshal([]byte(body), &obj) == nil {
		if m, ok := obj["message"].(string); ok && m != "" {
			return m
		}
		if m, ok := obj["Message"].(string); ok && m != "" {
			return m
		}
	}
	if len(body) > 500 {
		return body[:500]
	}
	return body
}

func kiroErrorType(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		return "api_error"
	}
}

func (s *KiroGatewayService) buildForwardResult(requestedModel string, res *kiro.StreamResult, stream bool, start time.Time, cache kiro.CacheResult) *ForwardResult {
	fr := &ForwardResult{Stream: stream, Duration: time.Since(start), Model: requestedModel}
	if res != nil {
		// 合成缓存按 estimate 前缀算出,这里按真实 total(res.InputTokens)做 Reconcile:
		// 用估算 total 等比对齐真值,消除 estimateTokens(÷4 字符)与 Kiro contextUsage 的
		// 量纲差(否则量纲差被整块甩进 input,账面命中率被压低)。与 SSE 口径一致。
		cache = cache.Reconcile(res.InputTokens)
		// input 扣掉合成缓存部分(input = total − read − creation,互斥,避免重复计费);
		// 与注入客户端 SSE 的 message_delta.usage 口径一致。
		input := res.InputTokens - cache.CacheReadInputTokens - cache.CacheCreationInputTokens
		if input < 0 {
			input = 0
		}
		fr.Usage = ClaudeUsage{
			InputTokens:              input,
			OutputTokens:             res.OutputTokens,
			CacheReadInputTokens:     cache.CacheReadInputTokens,
			CacheCreationInputTokens: cache.CacheCreationInputTokens,
			CacheCreation5mTokens:    cache.CacheCreation5mTokens,
			CacheCreation1hTokens:    cache.CacheCreation1hTokens,
		}
		if res.Model != "" && res.Model != requestedModel {
			fr.UpstreamModel = res.Model
		}
		// 真实 credit 消耗落到 ForwardResult,供 RecordUsage 写入 usage_logs.kiro_credit_usage。
		fr.KiroCreditUsage = res.CreditUsage
		// Kiro 唯一的真实成本口径是 credit(meteringEvent.usage);token 数只能估算。
		// 记录真实 credit 消耗供观测/计费落库(用量面板落库见 usage-log kiro_credit_usage)。
		if res.CreditUsage > 0 {
			slog.Info("kiro.request_credit_usage",
				"model", requestedModel,
				"upstream_model", res.Model,
				"credit_usage", res.CreditUsage,
				"est_input_tokens", res.InputTokens,
				"output_tokens", res.OutputTokens,
				"cache_read_tokens", cache.CacheReadInputTokens,
				"cache_creation_tokens", cache.CacheCreationInputTokens,
				"stream", stream,
			)
		}
	}
	return fr
}
