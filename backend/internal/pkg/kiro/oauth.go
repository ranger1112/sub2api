package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// oauth2TokenResponse 是标准 OAuth2(RFC 6749 §5.1)token 端点的 snake_case 响应,
// 用于 external_idp(委托外部 IdP,如 Microsoft Entra ID)。与 social/idc 的 camelCase
// refreshResponse 不同;外部 IdP 不返回 profileArn。
type oauth2TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func (r oauth2TokenResponse) toRefreshResponse() refreshResponse {
	return refreshResponse{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		ExpiresIn:    r.ExpiresIn,
	}
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
	case "external_idp", "externalidp":
		return refreshExternalIdpToken(ctx, client, cred, cfg)
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

// refreshExternalIdpToken 刷新委托给外部身份提供商(如 Microsoft Entra ID)的凭据。
// 与 social/idc 不同,这里走标准 OAuth2(RFC 6749 §6):
//   - 请求体为 application/x-www-form-urlencoded(而非 JSON);
//   - 端点取自凭据自身的 tokenEndpoint(完整 URL,而非按 region 拼接);
//   - 响应为标准 snake_case(access_token/refresh_token/expires_in)。
//
// clientSecret 可选:公共客户端(PKCE,如 Kiro 桌面端)不带 secret;机密客户端才带上。
func refreshExternalIdpToken(ctx context.Context, client *http.Client, cred *Credentials, cfg ClientConfig) (*Credentials, error) {
	if cred.TokenEndpoint == "" {
		return nil, errors.New("kiro: external_idp refresh requires tokenEndpoint")
	}
	if cred.ClientID == "" {
		return nil, errors.New("kiro: external_idp refresh requires clientId")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", cred.ClientID)
	form.Set("refresh_token", cred.RefreshToken)
	if cred.Scopes != "" {
		// scope 需包含 offline_access 才能拿到轮换后的新 refresh_token。
		form.Set("scope", cred.Scopes)
	}
	if cred.ClientSecret != "" {
		// 机密客户端才带 secret;公共客户端(PKCE)留空。
		form.Set("client_secret", cred.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cred.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	return doRefreshOAuth2(client, req, cred, "ExternalIdP")
}

// maxRefreshBodySize 限制刷新响应体读取上限,防止恶意/异常上游无限流式响应导致 OOM。
const maxRefreshBodySize = 1 << 20 // 1 MB

// sendRefresh 执行刷新请求,读取响应体(限流),并在非 2xx 时归类错误。
// 成功时返回原始响应体,由各解析器按自身字段命名(camelCase / snake_case)解码。
func sendRefresh(client *http.Client, req *http.Request, kind string) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxRefreshBodySize))
	if err != nil {
		return nil, fmt.Errorf("kiro: %s refresh: read response: %w", kind, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, classifyRefreshError(kind, resp.StatusCode, respBody)
	}
	return respBody, nil
}

// doRefresh 解析 social/idc 的 camelCase 响应。
func doRefresh(client *http.Client, req *http.Request, cred *Credentials, kind string) (*Credentials, error) {
	respBody, err := sendRefresh(client, req, kind)
	if err != nil {
		return nil, err
	}
	var data refreshResponse
	if err := json.Unmarshal(respBody, &data); err != nil {
		return nil, fmt.Errorf("kiro: %s refresh: parse response: %w", kind, err)
	}
	return applyRefresh(cred, data), nil
}

// doRefreshOAuth2 解析标准 OAuth2 的 snake_case 响应(external_idp)。
func doRefreshOAuth2(client *http.Client, req *http.Request, cred *Credentials, kind string) (*Credentials, error) {
	respBody, err := sendRefresh(client, req, kind)
	if err != nil {
		return nil, err
	}
	var data oauth2TokenResponse
	if err := json.Unmarshal(respBody, &data); err != nil {
		return nil, fmt.Errorf("kiro: %s refresh: parse response: %w", kind, err)
	}
	return applyRefresh(cred, data.toRefreshResponse()), nil
}

func classifyRefreshError(kind string, status int, body []byte) error {
	bodyStr := string(body)
	// 400 + invalid_grant 视为 refreshToken 永久失效:
	//   - social(kiro.dev)带具体措辞 "Invalid refresh token provided";
	//   - idc(AWS OIDC)/ external_idp(如 Microsoft Entra,AADSTS 前缀)按 OAuth2
	//     (RFC 6749 §5.2)语义,invalid_grant 即为撤销/过期,永久失效。
	if status == http.StatusBadRequest && strings.Contains(bodyStr, "invalid_grant") &&
		(kind == "IdC" || kind == "ExternalIdP" || strings.Contains(bodyStr, "Invalid refresh token provided")) {
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
		secs := data.ExpiresIn
		if secs > maxExpiresInSeconds { // 上限防护:避免 time.Duration 溢出为负导致过期时间落在过去
			secs = maxExpiresInSeconds
		}
		nc.ExpiresAt = timeNow().Add(time.Duration(secs) * time.Second).UTC().Format(time.RFC3339)
	}
	return &nc
}

// maxExpiresInSeconds 约为 10 年,远小于 time.Duration(int64 纳秒)的溢出阈值。
const maxExpiresInSeconds = 10 * 365 * 24 * 60 * 60
