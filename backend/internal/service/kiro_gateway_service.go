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

// KiroGatewayService 处理 Kiro 账号的 Anthropic /v1/messages 请求。
// Kiro 账号挂在 anthropic 分组下,由 GatewayHandler 按 account.Platform 分流到此。
// 账号选择、并发获取、计费由 handler 负责;本服务只做:取 token → 转换 → 调用上游 → 回写响应。
type KiroGatewayService struct {
	tokenProvider *KiroTokenProvider
	clientFor     KiroHTTPClientFactory
	cfg           kiro.ClientConfig
	cacheTracker  *kiro.CacheTracker
}

func NewKiroGatewayService(tokenProvider *KiroTokenProvider, clientFor KiroHTTPClientFactory, cfg kiro.ClientConfig, cacheTracker *kiro.CacheTracker) *KiroGatewayService {
	if clientFor == nil {
		clientFor = func(string) (*http.Client, error) { return http.DefaultClient, nil }
	}
	return &KiroGatewayService{tokenProvider: tokenProvider, clientFor: clientFor, cfg: cfg, cacheTracker: cacheTracker}
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
		} else if cacheProfile != nil {
			// 上游成功:把本轮可缓存前缀写入缓存表,供后续轮次命中。
			s.cacheTracker.Update(account.ID, cacheProfile)
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
			StatusCode:      status,
			ResponseBody:    respBody,
			ResponseHeaders: respHeaders,
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

// classifyKiroUpstreamError 解析 Kiro Forward 错误,返回上游状态码、响应体、响应头,以及是否可 failover 重试。
// 可重试:429、5xx(>=500)上游状态,或连接级(网络)错误。
// 终止:4xx 客户端错误(400/401/403/404 等)、请求构造/转换错误、context 取消等。
func classifyKiroUpstreamError(err error) (status int, body []byte, headers http.Header, retryable bool) {
	var ue *kiro.UpstreamError
	if errors.As(err, &ue) {
		if ue.Body != "" {
			body = []byte(ue.Body)
		}
		retryable = ue.StatusCode == http.StatusTooManyRequests || ue.StatusCode >= 500
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
	switch {
	case status == http.StatusBadRequest:
		return "invalid_request_error"
	case status == http.StatusUnauthorized:
		return "authentication_error"
	case status == http.StatusForbidden:
		return "permission_error"
	case status == http.StatusNotFound:
		return "not_found_error"
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		return "api_error"
	}
}

func (s *KiroGatewayService) buildForwardResult(requestedModel string, res *kiro.StreamResult, stream bool, start time.Time, cache kiro.CacheResult) *ForwardResult {
	fr := &ForwardResult{Stream: stream, Duration: time.Since(start), Model: requestedModel}
	if res != nil {
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
