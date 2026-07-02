package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// timeNow 可在测试中替换以获得确定性过期时间。
var timeNow = time.Now

// RefreshTokenInvalidError 表示 refreshToken 已被撤销/过期(400 invalid_grant),
// 不应重试,调用方应据此禁用对应凭据。
type RefreshTokenInvalidError struct{ Message string }

func (e *RefreshTokenInvalidError) Error() string { return e.Message }

// refreshResponse 是 social 与 idc 刷新的响应体。字段为 camelCase,对照 kiro.rs 的
// rename_all="camelCase"(即使 idc/OAuth 路径也使用 camelCase,而非标准 snake_case)。
type refreshResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ProfileArn   string `json:"profileArn"`
	ExpiresIn    int64  `json:"expiresIn"`
}

// RefreshToken 刷新凭据的 access token(social 或 idc),返回更新后的 Credentials 副本。
// client 由调用方提供(可携带代理 / TLS 配置)。
func RefreshToken(ctx context.Context, client *http.Client, cred *Credentials, cfg ClientConfig) (*Credentials, error) {
	if cred.IsAPIKeyCredential() {
		return nil, errors.New("kiro: API key credential does not support token refresh")
	}
	if err := validateRefreshToken(cred); err != nil {
		return nil, err
	}

	method := cred.AuthMethod
	if method == "" {
		if cred.ClientID != "" && cred.ClientSecret != "" {
			method = "idc"
		} else {
			method = "social"
		}
	}
	switch strings.ToLower(method) {
	case "idc", "builder-id", "iam":
		return refreshIdcToken(ctx, client, cred, cfg)
	default:
		return refreshSocialToken(ctx, client, cred, cfg)
	}
}

// validateRefreshToken 校验 refreshToken 的基本有效性(非空、未被截断)。
func validateRefreshToken(cred *Credentials) error {
	rt := cred.RefreshToken
	if rt == "" {
		return errors.New("kiro: missing refreshToken")
	}
	if len(rt) < 100 || strings.HasSuffix(rt, "...") || strings.Contains(rt, "...") {
		return fmt.Errorf("kiro: refreshToken appears truncated (length %d); Kiro IDE may have deliberately truncated it", len(rt))
	}
	return nil
}

func (c ClientConfig) socialRefreshURL(region string) string {
	if c.socialRefreshURLOverride != "" {
		return c.socialRefreshURLOverride
	}
	return "https://prod." + region + ".auth.desktop.kiro.dev/refreshToken"
}

func (c ClientConfig) idcRefreshURL(region string) string {
	if c.idcRefreshURLOverride != "" {
		return c.idcRefreshURLOverride
	}
	return "https://oidc." + region + ".amazonaws.com/token"
}

func refreshSocialToken(ctx context.Context, client *http.Client, cred *Credentials, cfg ClientConfig) (*Credentials, error) {
	region := cred.EffectiveAuthRegion(cfg)
	machineID := MachineID(cred, cfg)

	body, _ := json.Marshal(map[string]string{"refreshToken": cred.RefreshToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.socialRefreshURL(region), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Host = "prod." + region + ".auth.desktop.kiro.dev"
	// 注:不手动设置 Accept-Encoding,交由 Go transport 自动 gzip 协商与解压。
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "KiroIDE-"+cfg.kiroVersion()+"-"+machineID)

	return doRefresh(client, req, cred, "Social")
}

func refreshIdcToken(ctx context.Context, client *http.Client, cred *Credentials, cfg ClientConfig) (*Credentials, error) {
	if cred.ClientID == "" {
		return nil, errors.New("kiro: IdC refresh requires clientId")
	}
	if cred.ClientSecret == "" {
		return nil, errors.New("kiro: IdC refresh requires clientSecret")
	}
	region := cred.EffectiveAuthRegion(cfg)

	// 字段名为 camelCase(clientId/clientSecret/refreshToken/grantType),与 kiro.rs 一致
	// (非标准 OAuth2 snake_case);grantType 的值仍是 "refresh_token"。
	body, _ := json.Marshal(map[string]string{
		"clientId":     cred.ClientID,
		"clientSecret": cred.ClientSecret,
		"refreshToken": cred.RefreshToken,
		"grantType":    "refresh_token",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.idcRefreshURL(region), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Host = "oidc." + region + ".amazonaws.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-amz-user-agent", "aws-sdk-js/3.980.0 KiroIDE")
	req.Header.Set("User-Agent", "aws-sdk-js/3.980.0 ua/2.1 os/"+cfg.systemVersion()+
		" lang/js md/nodejs#"+cfg.nodeVersion()+" api/sso-oidc#3.980.0 m/E KiroIDE")
	req.Header.Set("amz-sdk-invocation-id", newUUID())
	req.Header.Set("amz-sdk-request", "attempt=1; max=4")

	return doRefresh(client, req, cred, "IdC")
}

func doRefresh(client *http.Client, req *http.Request, cred *Credentials, kind string) (*Credentials, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, classifyRefreshError(kind, resp.StatusCode, respBody)
	}
	var data refreshResponse
	if err := json.Unmarshal(respBody, &data); err != nil {
		return nil, fmt.Errorf("kiro: %s refresh: parse response: %w", kind, err)
	}
	return applyRefresh(cred, data), nil
}

func classifyRefreshError(kind string, status int, body []byte) error {
	bodyStr := string(body)
	if status == http.StatusBadRequest &&
		strings.Contains(bodyStr, `"invalid_grant"`) &&
		strings.Contains(bodyStr, "Invalid refresh token provided") {
		return &RefreshTokenInvalidError{Message: kind + " refreshToken invalid (invalid_grant): " + bodyStr}
	}
	var reason string
	switch {
	case status == http.StatusUnauthorized:
		reason = "credential expired or invalid, re-auth required"
	case status == http.StatusForbidden:
		reason = "insufficient permission to refresh token"
	case status == http.StatusTooManyRequests:
		reason = "rate limited"
	case status >= 500 && status <= 599:
		reason = "upstream auth server error"
	default:
		reason = "token refresh failed"
	}
	return fmt.Errorf("kiro: %s %s (%d): %s", kind, reason, status, bodyStr)
}

// applyRefresh 基于刷新响应返回更新后的凭据副本(不修改入参)。
func applyRefresh(cred *Credentials, data refreshResponse) *Credentials {
	nc := *cred
	nc.AccessToken = data.AccessToken
	if data.RefreshToken != "" {
		nc.RefreshToken = data.RefreshToken
	}
	if data.ProfileArn != "" {
		nc.ProfileArn = data.ProfileArn
	}
	if data.ExpiresIn > 0 {
		nc.ExpiresAt = timeNow().Add(time.Duration(data.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	return &nc
}
