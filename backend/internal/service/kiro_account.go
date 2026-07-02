package service

import "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"

// 本文件在 Account 模型与 internal/pkg/kiro 的 Credentials 之间做映射。
// Kiro 账号凭据以明文 JSONB(account.Credentials map)存储,键名如下:
//   access_token / refresh_token / profile_arn / auth_method / client_id /
//   client_secret / kiro_api_key / region / auth_region / api_region /
//   machine_id / expires_at

// IsKiro 报告账号是否属于 Kiro 平台。
func (a *Account) IsKiro() bool {
	return a != nil && a.Platform == PlatformKiro
}

// IsKiroOAuth 报告账号是否为 Kiro OAuth 账号(需刷新 token)。
func (a *Account) IsKiroOAuth() bool {
	return a.IsKiro() && a.Type == AccountTypeOAuth
}

// GetKiroAccessToken 返回 Kiro 账号的 access_token(OAuth 凭据)。
func (a *Account) GetKiroAccessToken() string {
	if !a.IsKiro() {
		return ""
	}
	return a.GetCredential("access_token")
}

// GetKiroRefreshToken 返回 Kiro OAuth 账号的 refresh_token。
func (a *Account) GetKiroRefreshToken() string {
	if !a.IsKiroOAuth() {
		return ""
	}
	return a.GetCredential("refresh_token")
}

// AccountToKiroCredentials 从账号凭据构造 kiro.Credentials。
func AccountToKiroCredentials(account *Account) kiro.Credentials {
	if account == nil {
		return kiro.Credentials{}
	}
	return kiro.Credentials{
		ID:           account.ID,
		AccessToken:  account.GetCredential("access_token"),
		RefreshToken: account.GetCredential("refresh_token"),
		ProfileArn:   account.GetCredential("profile_arn"),
		AuthMethod:   account.GetCredential("auth_method"),
		ClientID:     account.GetCredential("client_id"),
		ClientSecret: account.GetCredential("client_secret"),
		KiroAPIKey:   account.GetCredential("kiro_api_key"),
		Region:       account.GetCredential("region"),
		AuthRegion:   account.GetCredential("auth_region"),
		APIRegion:    account.GetCredential("api_region"),
		MachineID:    account.GetCredential("machine_id"),
		ExpiresAt:    account.GetCredential("expires_at"),
	}
}

// BuildKiroAccountCredentials 把 kiro.Credentials 转换为账号凭据 map(仅保留非空字段)。
func BuildKiroAccountCredentials(cred kiro.Credentials) map[string]any {
	creds := map[string]any{}
	setIf := func(k, v string) {
		if v != "" {
			creds[k] = v
		}
	}
	setIf("access_token", cred.AccessToken)
	setIf("refresh_token", cred.RefreshToken)
	setIf("profile_arn", cred.ProfileArn)
	setIf("auth_method", cred.AuthMethod)
	setIf("client_id", cred.ClientID)
	setIf("client_secret", cred.ClientSecret)
	setIf("kiro_api_key", cred.KiroAPIKey)
	setIf("region", cred.Region)
	setIf("auth_region", cred.AuthRegion)
	setIf("api_region", cred.APIRegion)
	setIf("machine_id", cred.MachineID)
	setIf("expires_at", cred.ExpiresAt)
	return creds
}
