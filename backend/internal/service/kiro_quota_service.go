package service

import (
	"context"
	"net/http"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// KiroQuotaService 面向账号 ID 提供 Kiro 配额查询:封装账号加载、代理解析,
// 并委托 KiroQuotaFetcher 调用 getUsageLimits。镜像 GrokQuotaService 的结构与错误码风格,
// 供 admin handler 复用(账号选择、鉴权由上层负责)。
type KiroQuotaService struct {
	accountRepo AccountRepository
	proxyRepo   ProxyRepository
	fetcher     *KiroQuotaFetcher
}

// NewKiroQuotaService 创建 KiroQuotaService。
func NewKiroQuotaService(accountRepo AccountRepository, proxyRepo ProxyRepository, fetcher *KiroQuotaFetcher) *KiroQuotaService {
	return &KiroQuotaService{accountRepo: accountRepo, proxyRepo: proxyRepo, fetcher: fetcher}
}

// FetchUsage 加载并校验账号、解析代理,调用 getUsageLimits 返回配额/用量。
func (s *KiroQuotaService) FetchUsage(ctx context.Context, accountID int64) (*QuotaResult, error) {
	account, err := s.loadKiroAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if s.fetcher == nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "KIRO_QUOTA_NOT_CONFIGURED", "kiro quota service is not configured")
	}
	result, err := s.fetcher.FetchQuota(ctx, account, s.resolveProxyURL(ctx, account))
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "KIRO_QUOTA_FETCH_FAILED", "failed to fetch kiro usage: %v", err)
	}
	return result, nil
}

func (s *KiroQuotaService) resolveProxyURL(ctx context.Context, account *Account) string {
	if account == nil || account.ProxyID == nil {
		return ""
	}
	switch {
	case account.Proxy != nil:
		return account.Proxy.URL()
	case s != nil && s.proxyRepo != nil:
		if proxy, err := s.proxyRepo.GetByID(ctx, *account.ProxyID); err == nil && proxy != nil {
			return proxy.URL()
		}
	}
	return ""
}

func (s *KiroQuotaService) loadKiroAccount(ctx context.Context, accountID int64) (*Account, error) {
	if s == nil || s.accountRepo == nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "KIRO_QUOTA_NOT_CONFIGURED", "kiro quota service is not configured")
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusNotFound, "KIRO_QUOTA_ACCOUNT_NOT_FOUND", "account not found: %v", err)
	}
	if account == nil {
		return nil, infraerrors.New(http.StatusNotFound, "KIRO_QUOTA_ACCOUNT_NOT_FOUND", "account not found")
	}
	if account.Platform != PlatformKiro {
		return nil, infraerrors.New(http.StatusBadRequest, "KIRO_QUOTA_INVALID_PLATFORM", "account is not a Kiro account")
	}
	return account, nil
}
