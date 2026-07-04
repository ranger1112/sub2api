package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// UsageLimitsPath 是 Kiro 用量/配额端点的路径。
const UsageLimitsPath = "/getUsageLimits"

// UsageLimitsURL 返回 getUsageLimits 端点 URL(区域解析与 generateAssistantResponse 一致)。
func (c *Credentials) UsageLimitsURL(cfg ClientConfig) string {
	return "https://q." + c.EffectiveAPIRegion(cfg) + ".amazonaws.com" + UsageLimitsPath
}

// BuildUsageLimitsBody 构造 getUsageLimits 请求体:空对象,若有 profileArn 则注入根对象。
func BuildUsageLimitsBody(cred *Credentials) []byte {
	return InjectProfileArn([]byte("{}"), cred.ProfileArn)
}

// BuildUsageLimitsRequest 构造发往 Kiro getUsageLimits 端点的 HTTP 请求。
//
// 请求头与 BuildAPIRequest 完全一致(同一套伪装头 + Bearer + 可选 tokentype),
// 仅目标路径改为 /getUsageLimits。body 为空时自动使用 BuildUsageLimitsBody。
// 调用方负责用合适的 http.Client(代理 / TLS 指纹)执行返回的请求。
func BuildUsageLimitsRequest(ctx context.Context, cred *Credentials, cfg ClientConfig, body []byte) (*http.Request, error) {
	// external_idp:用量同样走 Kiro 管理网关 management.{region}.kiro.dev(而非 AWS 直连)。
	if strings.EqualFold(cred.AuthMethod, "external_idp") {
		return buildExternalIdpUsageLimitsRequest(ctx, cred, cfg)
	}

	apiRegion := cred.EffectiveAPIRegion(cfg)
	host := "q." + apiRegion + ".amazonaws.com"
	urlStr := "https://" + host + UsageLimitsPath
	machineID := MachineID(cred, cfg)
	if len(body) == 0 {
		body = BuildUsageLimitsBody(cred)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
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

// buildExternalIdpUsageLimitsRequest 为 external_idp 账号构造 getUsageLimits 请求。
//
// 与 AWS 直连不同,external_idp 的用量查询走 Kiro 管理网关
// management.{region}.kiro.dev/getUsageLimits(GET + query 参数),以 external IdP
// (如 Microsoft Entra)令牌直接作 Bearer,并带 TokenType: EXTERNAL_IDP 告知网关。
// 经真机抓包证实:GET,query 携带 origin=AI_EDITOR & profileArn & resourceType=AGENTIC_REQUEST。
func buildExternalIdpUsageLimitsRequest(ctx context.Context, cred *Credentials, cfg ClientConfig) (*http.Request, error) {
	region := cred.EffectiveAPIRegion(cfg)
	host := "management." + region + ".kiro.dev"

	q := url.Values{}
	q.Set("origin", "AI_EDITOR")
	if cred.ProfileArn != "" {
		q.Set("profileArn", cred.ProfileArn)
	}
	q.Set("resourceType", "AGENTIC_REQUEST")
	urlStr := "https://" + host + UsageLimitsPath + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlStr, nil)
	if err != nil {
		return nil, err
	}
	req.Host = host

	h := req.Header
	h.Set("Accept", "application/json")
	h.Set("User-Agent", "KiroAgent")
	h.Set("Authorization", "Bearer "+cred.BearerToken())
	if cred.ProfileArn != "" {
		h.Set("x-amzn-kiro-profile-arn", cred.ProfileArn)
	}
	// 保留抓包观测到的确切大小写 TokenType(避免 canonical 化为 "Tokentype")。
	req.Header["TokenType"] = []string{"EXTERNAL_IDP"}
	return req, nil
}

// UsageLimit 表示一条配额明细。字段为尽力解析,可能缺失(留零值)。
type UsageLimit struct {
	ResourceType string  `json:"resource_type,omitempty"`
	Used         float64 `json:"used"`
	Limit        float64 `json:"limit"`
	Unit         string  `json:"unit,omitempty"`
}

// Remaining 返回剩余额度(limit-used,下限 0);limit<=0 时返回 0。
func (u UsageLimit) Remaining() float64 {
	if u.Limit <= 0 {
		return 0
	}
	if r := u.Limit - u.Used; r > 0 {
		return r
	}
	return 0
}

// Utilization 返回使用率百分比(0-100+);limit<=0 时返回 0。
func (u UsageLimit) Utilization() float64 {
	if u.Limit <= 0 {
		return 0
	}
	return u.Used / u.Limit * 100
}

// UsageLimits 是 getUsageLimits 响应的尽力解析结果。
// 上游 schema 未公开且可能演进,这里对多种候选字段名做宽松解析,并保留原始 JSON。
type UsageLimits struct {
	Breakdown        []UsageLimit   `json:"breakdown,omitempty"`
	DaysUntilReset   *int           `json:"days_until_reset,omitempty"`
	SubscriptionType string         `json:"subscription_type,omitempty"`
	Raw              map[string]any `json:"-"`
}

// Primary 返回用于概览展示的主要明细:优先第一条 limit>0 的明细,否则第一条,均无则 nil。
func (u *UsageLimits) Primary() *UsageLimit {
	if u == nil {
		return nil
	}
	for i := range u.Breakdown {
		if u.Breakdown[i].Limit > 0 {
			return &u.Breakdown[i]
		}
	}
	if len(u.Breakdown) > 0 {
		return &u.Breakdown[0]
	}
	return nil
}

// 候选字段名:上游 schema 未公开,尽力覆盖常见命名。
var (
	usageBreakdownKeys    = []string{"usageBreakdownList", "usageLimits", "breakdowns", "limits", "usages", "usageBreakdown"}
	usageResourceKeys     = []string{"resourceType", "usageLimitType", "type", "name", "breakdownType"}
	usageLimitValueKeys   = []string{"usageLimit", "limit", "usageLimitWithPrecision", "maxUsage", "max", "quota"}
	usageUsedValueKeys    = []string{"currentUsage", "used", "currentUsageWithPrecision", "usage", "consumed", "usedCount"}
	usageUnitKeys         = []string{"unit", "resourceUnit", "usageUnit"}
	usageResetKeys        = []string{"daysUntilReset", "resetInDays", "daysUntilFreeTrialReset"}
	usageSubscriptionKeys = []string{"subscriptionType", "subscriptionTier", "tier", "plan", "planType"}
)

// ParseUsageLimits 尽力解析 getUsageLimits 响应。
// 非 JSON 对象返回错误;字段缺失不报错(留零值)。
func ParseUsageLimits(data []byte) (*UsageLimits, error) {
	root := gjson.ParseBytes(data)
	if !root.IsObject() {
		return nil, fmt.Errorf("kiro: getUsageLimits response is not a JSON object")
	}

	out := &UsageLimits{}
	if raw, ok := jsonToMap(data); ok {
		out.Raw = raw
	}

	var list gjson.Result
	for _, k := range usageBreakdownKeys {
		if r := root.Get(k); r.IsArray() {
			list = r
			break
		}
	}
	list.ForEach(func(_, item gjson.Result) bool {
		if !item.IsObject() {
			return true
		}
		out.Breakdown = append(out.Breakdown, UsageLimit{
			ResourceType: firstString(item, usageResourceKeys),
			Limit:        firstNumber(item, usageLimitValueKeys),
			Used:         firstNumber(item, usageUsedValueKeys),
			Unit:         firstString(item, usageUnitKeys),
		})
		return true
	})

	if v, ok := firstNumberExists(root, usageResetKeys); ok {
		iv := int(v)
		out.DaysUntilReset = &iv
	}
	out.SubscriptionType = firstString(root, usageSubscriptionKeys)
	if out.SubscriptionType == "" {
		// Kiro:订阅信息嵌套在 subscriptionInfo 下。优先取人类可读的 subscriptionTitle
		// (如 "KIRO FREE"),回退到 type(如 "Q_DEVELOPER_STANDALONE_FREE")。
		if si := root.Get("subscriptionInfo"); si.IsObject() {
			out.SubscriptionType = firstString(si, []string{"subscriptionTitle", "type", "tier", "plan"})
		}
	}

	return out, nil
}

func firstString(r gjson.Result, keys []string) string {
	for _, k := range keys {
		if v := r.Get(k); v.Exists() && v.String() != "" {
			return v.String()
		}
	}
	return ""
}

func firstNumber(r gjson.Result, keys []string) float64 {
	if v, ok := firstNumberExists(r, keys); ok {
		return v
	}
	return 0
}

func firstNumberExists(r gjson.Result, keys []string) (float64, bool) {
	for _, k := range keys {
		v := r.Get(k)
		if !v.Exists() {
			continue
		}
		switch v.Type {
		case gjson.Number:
			return v.Float(), true
		case gjson.String:
			if f, err := strconv.ParseFloat(strings.TrimSpace(v.String()), 64); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

func jsonToMap(data []byte) (map[string]any, bool) {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, false
	}
	return m, true
}
