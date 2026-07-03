package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

const (
	kiroQuotaUpstreamTimeout = 20 * time.Second
	kiroQuotaMaxBody         = 256 * 1024
)

// KiroQuotaFetcher 通过 Kiro getUsageLimits 端点获取账号配额/用量。
// 它实现 QuotaFetcher 接口(CanFetch + FetchQuota),并被 KiroQuotaService 复用。
// token 通过 KiroTokenProvider 获取(API Key 直取 / OAuth 按需刷新);
// 代理感知的 HTTP client 由 KiroHTTPClientFactory 构造。
type KiroQuotaFetcher struct {
	tokenProvider *KiroTokenProvider
	clientFor     KiroHTTPClientFactory
	cfg           kiro.ClientConfig
}

// NewKiroQuotaFetcher 创建 KiroQuotaFetcher。clientFor 为 nil 时退化为 http.DefaultClient(无代理)。
func NewKiroQuotaFetcher(tokenProvider *KiroTokenProvider, clientFor KiroHTTPClientFactory, cfg kiro.ClientConfig) *KiroQuotaFetcher {
	if clientFor == nil {
		clientFor = func(string) (*http.Client, error) { return http.DefaultClient, nil }
	}
	return &KiroQuotaFetcher{tokenProvider: tokenProvider, clientFor: clientFor, cfg: cfg}
}

// CanFetch 检查是否可以获取此账户的额度(Kiro 平台且存在可用凭据)。
func (f *KiroQuotaFetcher) CanFetch(account *Account) bool {
	if account == nil || account.Platform != PlatformKiro {
		return false
	}
	switch account.Type {
	case AccountTypeAPIKey:
		return strings.TrimSpace(account.GetCredential("kiro_api_key")) != ""
	case AccountTypeOAuth:
		return strings.TrimSpace(account.GetKiroAccessToken()) != "" ||
			strings.TrimSpace(account.GetKiroRefreshToken()) != ""
	default:
		return false
	}
}

// FetchQuota 调用 getUsageLimits 并把结果转换为 UsageInfo。
// 401/403/429 作为降级 UsageInfo(带 error_code)返回,不视为 Go 错误(便于展示与状态标记);
// 其余非 2xx / 网络错误返回 error。
func (f *KiroQuotaFetcher) FetchQuota(ctx context.Context, account *Account, proxyURL string) (*QuotaResult, error) {
	if f == nil || f.tokenProvider == nil {
		return nil, errors.New("kiro quota fetcher is not configured")
	}
	if account == nil || account.Platform != PlatformKiro {
		return nil, errors.New("account is not a Kiro account")
	}

	token, err := f.tokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("acquire kiro token: %w", err)
	}
	cred := AccountToKiroCredentials(account)
	if !cred.IsAPIKeyCredential() {
		// 使用 provider 返回的(可能已刷新的)access token 作为 Bearer。
		cred.AccessToken = token
	}

	client, err := f.clientFor(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("build kiro http client: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, kiroQuotaUpstreamTimeout)
	defer cancel()
	req, err := kiro.BuildUsageLimitsRequest(callCtx, &cred, f.cfg, nil)
	if err != nil {
		return nil, fmt.Errorf("build getUsageLimits request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("getUsageLimits request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, kiroQuotaMaxBody))

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return &QuotaResult{UsageInfo: kiroDegradedUsage(resp.StatusCode, bodyBytes)}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("getUsageLimits returned %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(bodyBytes)), 240))
	}

	limits, err := kiro.ParseUsageLimits(bodyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse getUsageLimits response: %w", err)
	}
	return &QuotaResult{
		UsageInfo: buildKiroUsageInfo(limits),
		Raw:       limits.Raw,
	}, nil
}

// kiroDegradedUsage 从上游 401/403/429 构建带机器可读错误码的降级 UsageInfo。
func kiroDegradedUsage(statusCode int, body []byte) *UsageInfo {
	now := time.Now()
	info := &UsageInfo{Source: "active", UpdatedAt: &now}
	msg := truncate(strings.TrimSpace(string(body)), 240)
	switch statusCode {
	case http.StatusUnauthorized:
		info.NeedsReauth = true
		info.ErrorCode = errorCodeUnauthenticated
		info.Error = "Kiro credentials are unauthorized (401)"
	case http.StatusForbidden:
		info.IsForbidden = true
		info.ForbiddenType = forbiddenTypeForbidden
		info.ForbiddenReason = msg
		info.ErrorCode = errorCodeForbidden
		info.Error = "Kiro account is forbidden (403)"
	case http.StatusTooManyRequests:
		info.ErrorCode = errorCodeRateLimited
		info.Error = "Kiro account is rate limited (429)"
	}
	return info
}

// buildKiroUsageInfo 把解析后的 UsageLimits 转换为 UsageInfo。
func buildKiroUsageInfo(limits *kiro.UsageLimits) *UsageInfo {
	now := time.Now()
	info := &UsageInfo{Source: "active", UpdatedAt: &now}
	if limits == nil {
		return info
	}
	if limits.SubscriptionType != "" {
		info.SubscriptionTierRaw = limits.SubscriptionType
		info.SubscriptionTier = normalizeTier(limits.SubscriptionType)
	}
	if primary := limits.Primary(); primary != nil {
		progress := &UsageProgress{
			Utilization:   primary.Utilization(),
			UsedRequests:  int64(primary.Used),
			LimitRequests: int64(primary.Limit),
		}
		if limits.DaysUntilReset != nil {
			resetAt := now.Add(time.Duration(*limits.DaysUntilReset) * 24 * time.Hour)
			progress.ResetsAt = &resetAt
			if remaining := int(time.Until(resetAt).Seconds()); remaining > 0 {
				progress.RemainingSeconds = remaining
			}
		}
		info.FiveHour = progress
	}
	return info
}
