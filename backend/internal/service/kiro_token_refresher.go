package service

import (
	"context"
	"errors"
	"strings"
	"time"
)

const kiroTokenRefreshSkew = time.Hour

// KiroTokenRefresher 实现 TokenRefresher + OAuthRefreshExecutor,处理 Kiro OAuth 账号的 token 刷新。
type KiroTokenRefresher struct {
	oauthService KiroOAuthTokenService
}

func NewKiroTokenRefresher(oauthService KiroOAuthTokenService) *KiroTokenRefresher {
	return &KiroTokenRefresher{oauthService: oauthService}
}

func (r *KiroTokenRefresher) CacheKey(account *Account) string {
	return KiroTokenCacheKey(account)
}

func (r *KiroTokenRefresher) CanRefresh(account *Account) bool {
	return account != nil && account.Platform == PlatformKiro && account.Type == AccountTypeOAuth
}

func (r *KiroTokenRefresher) NeedsRefresh(account *Account, refreshWindow time.Duration) bool {
	if account == nil || strings.TrimSpace(account.GetKiroRefreshToken()) == "" {
		return false
	}
	expiresAt := account.GetCredentialAsTime("expires_at")
	if expiresAt == nil {
		return true
	}
	if refreshWindow < kiroTokenRefreshSkew {
		refreshWindow = kiroTokenRefreshSkew
	}
	return time.Until(*expiresAt) < refreshWindow
}

func (r *KiroTokenRefresher) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	if r == nil || r.oauthService == nil {
		return nil, errors.New("kiro oauth service is not configured")
	}
	newCredentials, err := r.oauthService.RefreshAccountCredentials(ctx, account)
	if err != nil {
		return nil, err
	}
	// 保留原有凭据中的其他字段(如自定义键、_token_version),只覆盖刷新得到的字段。
	return MergeCredentials(account.Credentials, newCredentials), nil
}
