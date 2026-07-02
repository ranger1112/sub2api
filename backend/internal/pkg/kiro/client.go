package kiro

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 默认版本/区域常量,用于伪装 Kiro IDE 的 User-Agent 等(对照 kiro.rs 的 config 默认值)。
const (
	DefaultRegion        = "us-east-1"
	DefaultKiroVersion   = "0.11.107"
	DefaultSystemVersion = "win32#10.0.22631"
	DefaultNodeVersion   = "22.22.0"
)

// ClientConfig 保存构造 Kiro 请求所需的版本与区域配置。
type ClientConfig struct {
	Region        string
	AuthRegion    string
	APIRegion     string
	KiroVersion   string
	SystemVersion string
	NodeVersion   string
	MachineID     string // 全局兜底 machineId

	// 仅用于测试:覆盖刷新端点地址(为空则使用真实 AWS / kiro.dev 地址)。
	socialRefreshURLOverride string
	idcRefreshURLOverride    string
}

// DefaultClientConfig 返回带默认值的配置。
func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		Region:        DefaultRegion,
		KiroVersion:   DefaultKiroVersion,
		SystemVersion: DefaultSystemVersion,
		NodeVersion:   DefaultNodeVersion,
	}
}

func (c ClientConfig) region() string        { return orDefault(c.Region, DefaultRegion) }
func (c ClientConfig) kiroVersion() string   { return orDefault(c.KiroVersion, DefaultKiroVersion) }
func (c ClientConfig) systemVersion() string { return orDefault(c.SystemVersion, DefaultSystemVersion) }
func (c ClientConfig) nodeVersion() string   { return orDefault(c.NodeVersion, DefaultNodeVersion) }

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// Credentials 是一个 Kiro 账号的凭据材料(对应上游 OAuth / API Key)。
type Credentials struct {
	ID           int64
	AccessToken  string
	RefreshToken string
	ProfileArn   string
	AuthMethod   string // "social" | "idc";为空时按 clientId/clientSecret 推断
	ClientID     string
	ClientSecret string
	KiroAPIKey   string // API Key 凭据:直接作 Bearer,无需刷新
	Region       string
	AuthRegion   string
	APIRegion    string
	MachineID    string
	ExpiresAt    string // RFC3339
}

// IsAPIKeyCredential 报告是否为 API Key 凭据(直接用 kiroApiKey 作 Bearer)。
func (c *Credentials) IsAPIKeyCredential() bool { return c.KiroAPIKey != "" }

// BearerToken 返回请求应携带的 Bearer(API Key 凭据用 kiroApiKey,否则用 accessToken)。
func (c *Credentials) BearerToken() string {
	if c.KiroAPIKey != "" {
		return c.KiroAPIKey
	}
	return c.AccessToken
}

// EffectiveAuthRegion:凭据.authRegion > 凭据.region > config.authRegion > config.region。
func (c *Credentials) EffectiveAuthRegion(cfg ClientConfig) string {
	switch {
	case c.AuthRegion != "":
		return c.AuthRegion
	case c.Region != "":
		return c.Region
	case cfg.AuthRegion != "":
		return cfg.AuthRegion
	default:
		return cfg.region()
	}
}

// EffectiveAPIRegion:凭据.apiRegion > 凭据.region > config.apiRegion > config.region。
func (c *Credentials) EffectiveAPIRegion(cfg ClientConfig) string {
	switch {
	case c.APIRegion != "":
		return c.APIRegion
	case c.Region != "":
		return c.Region
	case cfg.APIRegion != "":
		return cfg.APIRegion
	default:
		return cfg.region()
	}
}

// APIURL 返回 generateAssistantResponse 端点 URL。
func (c *Credentials) APIURL(cfg ClientConfig) string {
	return "https://q." + c.EffectiveAPIRegion(cfg) + ".amazonaws.com/generateAssistantResponse"
}

// BuildAPIRequest 构造发往 Kiro generateAssistantResponse 端点的 HTTP 请求:
// 设置全部伪装请求头并把 profileArn 注入请求体根对象。
// 调用方负责用合适的 http.Client(代理 / TLS 指纹)执行返回的请求。
func BuildAPIRequest(ctx context.Context, cred *Credentials, cfg ClientConfig, body []byte) (*http.Request, error) {
	apiRegion := cred.EffectiveAPIRegion(cfg)
	host := "q." + apiRegion + ".amazonaws.com"
	url := "https://" + host + "/generateAssistantResponse"
	machineID := MachineID(cred, cfg)
	body = InjectProfileArn(body, cred.ProfileArn)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Host = host

	xAmzUA := "aws-sdk-js/1.0.34 KiroIDE-" + cfg.kiroVersion() + "-" + machineID
	userAgent := "aws-sdk-js/1.0.34 ua/2.1 os/" + cfg.systemVersion() +
		" lang/js md/nodejs#" + cfg.nodeVersion() +
		" api/codewhispererstreaming#1.0.34 m/E KiroIDE-" + cfg.kiroVersion() + "-" + machineID

	h := req.Header
	h.Set("Content-Type", "application/json")
	h.Set("x-amzn-codewhisperer-optout", "true")
	h.Set("x-amzn-kiro-agent-mode", "vibe")
	h.Set("x-amz-user-agent", xAmzUA)
	h.Set("User-Agent", userAgent)
	h.Set("amz-sdk-invocation-id", newUUID())
	h.Set("amz-sdk-request", "attempt=1; max=3")
	h.Set("Authorization", "Bearer "+cred.BearerToken())
	if cred.IsAPIKeyCredential() {
		h.Set("tokentype", "API_KEY")
	}
	return req, nil
}

// InjectProfileArn 在请求体 JSON 根对象注入 profileArn。arn 为空或 body 非合法 JSON 时原样返回。
// 显式校验合法性:sjson 对非法 JSON 不报错而会直接生成 {"profileArn":...} 丢弃原文,
// 这里守住与 kiro.rs 一致的"非法 JSON 原样返回"契约。
func InjectProfileArn(body []byte, profileArn string) []byte {
	if profileArn == "" || !gjson.ValidBytes(body) {
		return body
	}
	out, err := sjson.SetBytes(body, "profileArn", profileArn)
	if err != nil {
		return body
	}
	return out
}

// MachineID 依据凭据与配置派生 64 位十六进制 machineId(对照 kiro.rs machine_id.rs)。
//
// 优先级:凭据.machineId → config.machineId → 按凭据类型派生(API Key 用 kiroApiKey、
// OAuth 用 refreshToken,两者互斥不回落)→ 随机兜底(按凭据 ID 进程内稳定)。
func MachineID(cred *Credentials, cfg ClientConfig) string {
	if n := normalizeMachineID(cred.MachineID); n != "" {
		return n
	}
	if n := normalizeMachineID(cfg.MachineID); n != "" {
		return n
	}
	if cred.IsAPIKeyCredential() {
		if cred.KiroAPIKey != "" {
			return sha256Hex("KiroAPIKey/" + cred.KiroAPIKey)
		}
	} else if cred.RefreshToken != "" {
		return sha256Hex("KotlinNativeAPI/" + cred.RefreshToken)
	}
	return fallbackMachineID(cred)
}

// normalizeMachineID 接受 64 位十六进制或 UUID(去连字符 32 位十六进制后重复一次到 64 位),否则返回 ""。
func normalizeMachineID(s string) string {
	t := strings.TrimSpace(s)
	if len(t) == 64 && isHex(t) {
		return t
	}
	noDash := strings.ReplaceAll(t, "-", "")
	if len(noDash) == 32 && isHex(noDash) {
		return noDash + noDash
	}
	return ""
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

var (
	fallbackMachineIDs   = map[int64]string{}
	fallbackMachineIDsMu sync.Mutex
)

// fallbackMachineID 为缺少派生材料的凭据生成随机 machineId,按凭据 ID 进程内缓存以保持稳定。
func fallbackMachineID(cred *Credentials) string {
	fallbackMachineIDsMu.Lock()
	defer fallbackMachineIDsMu.Unlock()
	if v, ok := fallbackMachineIDs[cred.ID]; ok {
		return v
	}
	derived := sha256Hex("KiroFallback/" + newUUID())
	fallbackMachineIDs[cred.ID] = derived
	return derived
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
