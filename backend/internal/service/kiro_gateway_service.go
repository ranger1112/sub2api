package service

import (
	"context"
	"encoding/json"
	"net/http"
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
		res, streamErr := kiro.StreamMessages(ctx, client, &cred, s.cfg, &req, c.Writer)
		return s.buildForwardResult(req.Model, res, true, start), streamErr
	}

	msg, res, err := kiro.CollectMessages(ctx, client, &cred, s.cfg, &req)
	if err != nil {
		return s.buildForwardResult(req.Model, res, false, start), err
	}
	c.Writer.Header().Set("Content-Type", "application/json")
	if encErr := json.NewEncoder(c.Writer).Encode(msg); encErr != nil {
		return s.buildForwardResult(req.Model, res, false, start), encErr
	}
	return s.buildForwardResult(req.Model, res, false, start), nil
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
