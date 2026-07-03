package service

import (
	"context"
	"encoding/json"
	"errors"
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
}

func NewKiroGatewayService(tokenProvider *KiroTokenProvider, clientFor KiroHTTPClientFactory, cfg kiro.ClientConfig) *KiroGatewayService {
	if clientFor == nil {
		clientFor = func(string) (*http.Client, error) { return http.DefaultClient, nil }
	}
	return &KiroGatewayService{tokenProvider: tokenProvider, clientFor: clientFor, cfg: cfg}
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

	if req.Stream {
		h := c.Writer.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		h.Set("X-Accel-Buffering", "no") // 禁止 nginx 等反代缓冲,保证实时流
		res, streamErr := kiro.StreamMessages(ctx, client, &cred, s.cfg, &req, c.Writer)
		if streamErr != nil {
			// 首字节前失败(如上游非 2xx):SSE 头尚未提交,改写为带真实上游状态的 JSON 错误。
			s.writeUpstreamError(c, streamErr)
		}
		return s.buildForwardResult(req.Model, res, true, start), streamErr
	}

	msg, res, err := kiro.CollectMessages(ctx, client, &cred, s.cfg, &req)
	if err != nil {
		s.writeUpstreamError(c, err)
		return s.buildForwardResult(req.Model, res, false, start), err
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	if encErr := json.NewEncoder(c.Writer).Encode(msg); encErr != nil {
		return s.buildForwardResult(req.Model, res, false, start), encErr
	}
	return s.buildForwardResult(req.Model, res, false, start), nil
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

func (s *KiroGatewayService) buildForwardResult(requestedModel string, res *kiro.StreamResult, stream bool, start time.Time) *ForwardResult {
	fr := &ForwardResult{Stream: stream, Duration: time.Since(start), Model: requestedModel}
	if res != nil {
		fr.Usage = ClaudeUsage{InputTokens: res.InputTokens, OutputTokens: res.OutputTokens}
		if res.Model != "" && res.Model != requestedModel {
			fr.UpstreamModel = res.Model
		}
	}
	return fr
}
