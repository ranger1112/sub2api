package service

import (
	"context"
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

// KiroOAuthTokenService 是 KiroTokenRefresher 依赖的窄口:根据账号刷新凭据。
type KiroOAuthTokenService interface {
	// RefreshAccountCredentials 刷新账号 token,返回新的凭据字段(未与旧凭据合并)。
	RefreshAccountCredentials(ctx context.Context, account *Account) (map[string]any, error)
}

// KiroHTTPClientFactory 根据代理 URL 构造用于刷新调用的 http.Client。
// 代理感知的具体实现由 wire 层注入(参考 repository 的 getSharedReqClient);
// 传入空字符串表示不使用代理。
type KiroHTTPClientFactory func(proxyURL string) (*http.Client, error)

// KiroOAuthService 通过 internal/pkg/kiro 执行 social / idc 令牌刷新。
type KiroOAuthService struct {
	proxyRepo ProxyRepository
	clientFor KiroHTTPClientFactory
	cfg       kiro.ClientConfig
}

// NewKiroOAuthService 创建 Kiro OAuth 服务。clientFor 为 nil 时退化为 http.DefaultClient(无代理)。
func NewKiroOAuthService(proxyRepo ProxyRepository, clientFor KiroHTTPClientFactory, cfg kiro.ClientConfig) *KiroOAuthService {
	if clientFor == nil {
		clientFor = func(string) (*http.Client, error) { return http.DefaultClient, nil }
	}
	return &KiroOAuthService{proxyRepo: proxyRepo, clientFor: clientFor, cfg: cfg}
}

// RefreshAccountCredentials 解析账号代理、构造凭据并调用 kiro.RefreshToken,返回新的凭据字段。
func (s *KiroOAuthService) RefreshAccountCredentials(ctx context.Context, account *Account) (map[string]any, error) {
	if account == nil || account.Platform != PlatformKiro {
		return nil, errors.New("kiro oauth: account is not a Kiro account")
	}
	if account.Type != AccountTypeOAuth {
		return nil, errors.New("kiro oauth: account is not an OAuth account")
	}
	proxyURL, err := s.proxyURL(ctx, account.ProxyID)
	if err != nil {
		return nil, err
	}
	client, err := s.clientFor(proxyURL)
	if err != nil {
		return nil, err
	}
	cred := AccountToKiroCredentials(account)
	updated, err := kiro.RefreshToken(ctx, client, &cred, s.cfg)
	if err != nil {
		return nil, err
	}
	return BuildKiroAccountCredentials(*updated), nil
}

func (s *KiroOAuthService) proxyURL(ctx context.Context, proxyID *int64) (string, error) {
	if proxyID == nil {
		return "", nil
	}
	if s.proxyRepo == nil {
		return "", errors.New("kiro oauth: proxy repository is not available")
	}
	proxy, err := s.proxyRepo.GetByID(ctx, *proxyID)
	if err != nil {
		return "", err
	}
	if proxy == nil {
		return "", nil
	}
	return proxy.URL(), nil
}
