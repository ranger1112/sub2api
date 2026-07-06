package kiro

import (
	"context"
	"io"
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildUsageLimitsRequest_OAuthCredential(t *testing.T) {
	cred := &Credentials{AccessToken: "access-tok", ProfileArn: "arn:profile/X", RefreshToken: "rt"}
	req, err := BuildUsageLimitsRequest(context.Background(), cred, DefaultClientConfig(), nil)
	if err != nil {
		t.Fatalf("BuildUsageLimitsRequest: %v", err)
	}
	if req.Method != "POST" {
		t.Fatalf("method = %s", req.Method)
	}
	// AWS 直连:AWS-JSON 1.0 RPC(POST 根路径 + x-amz-target),非 REST /getUsageLimits。
	if req.URL.String() != "https://q.us-east-1.amazonaws.com/" {
		t.Fatalf("url = %s", req.URL)
	}
	if req.Host != "q.us-east-1.amazonaws.com" {
		t.Fatalf("host = %s", req.Host)
	}
	checks := map[string]string{
		"Authorization":               "Bearer access-tok",
		"x-amzn-codewhisperer-optout": "true",
		"x-amzn-kiro-agent-mode":      "vibe",
		"Content-Type":                "application/x-amz-json-1.0",
		"x-amz-target":                "AmazonCodeWhispererService.GetUsageLimits",
		"amz-sdk-request":             "attempt=1; max=3",
	}
	for k, want := range checks {
		if got := req.Header.Get(k); got != want {
			t.Fatalf("header %s = %q, want %q", k, got, want)
		}
	}
	if req.Header.Get("tokentype") != "" {
		t.Fatalf("oauth credential must NOT set tokentype, got %q", req.Header.Get("tokentype"))
	}
	body, _ := io.ReadAll(req.Body)
	if gjson.GetBytes(body, "profileArn").String() != "arn:profile/X" {
		t.Fatalf("profileArn not injected into body: %s", body)
	}
}

func TestBuildUsageLimitsRequest_APIKeyCredential(t *testing.T) {
	cred := &Credentials{KiroAPIKey: "ksk_secret", APIRegion: "eu-west-1"}
	req, err := BuildUsageLimitsRequest(context.Background(), cred, DefaultClientConfig(), nil)
	if err != nil {
		t.Fatalf("BuildUsageLimitsRequest: %v", err)
	}
	if req.URL.String() != "https://q.eu-west-1.amazonaws.com/" {
		t.Fatalf("url = %s (region precedence not honored)", req.URL)
	}
	if req.Header.Get("Authorization") != "Bearer ksk_secret" {
		t.Fatalf("apikey bearer = %q", req.Header.Get("Authorization"))
	}
	if req.Header.Get("tokentype") != "API_KEY" {
		t.Fatalf("apikey credential must set tokentype API_KEY, got %q", req.Header.Get("tokentype"))
	}
}

func TestBuildUsageLimitsRequest_ExternalIdp(t *testing.T) {
	cred := &Credentials{
		AccessToken:  "ms-jwt-tok",
		AuthMethod:   "external_idp",
		ProfileArn:   "arn:aws:codewhisperer:us-east-1:123:profile/ABC",
		RefreshToken: "rt",
	}
	req, err := BuildUsageLimitsRequest(context.Background(), cred, DefaultClientConfig(), nil)
	if err != nil {
		t.Fatalf("BuildUsageLimitsRequest: %v", err)
	}
	// external_idp 用量走管理网关(GET),而非 AWS 直连(POST)。
	if req.Method != "GET" {
		t.Fatalf("method = %s, want GET", req.Method)
	}
	if req.Host != "management.us-east-1.kiro.dev" {
		t.Fatalf("host = %s, want management.us-east-1.kiro.dev", req.Host)
	}
	if req.URL.Path != "/getUsageLimits" {
		t.Fatalf("path = %s", req.URL.Path)
	}
	q := req.URL.Query()
	for k, want := range map[string]string{
		"origin":       "AI_EDITOR",
		"profileArn":   "arn:aws:codewhisperer:us-east-1:123:profile/ABC",
		"resourceType": "AGENTIC_REQUEST",
	} {
		if got := q.Get(k); got != want {
			t.Fatalf("query %s = %q, want %q", k, got, want)
		}
	}
	if req.Header.Get("Authorization") != "Bearer ms-jwt-tok" {
		t.Fatalf("bearer = %q", req.Header.Get("Authorization"))
	}
	if got := req.Header.Get("TokenType"); got != "EXTERNAL_IDP" {
		t.Fatalf("TokenType header = %q, want EXTERNAL_IDP", got)
	}
	if req.Header.Get("x-amzn-kiro-profile-arn") != "arn:aws:codewhisperer:us-east-1:123:profile/ABC" {
		t.Fatalf("missing x-amzn-kiro-profile-arn header")
	}
	// 不应触达 AWS 直连。
	if req.URL.Host == "q.us-east-1.amazonaws.com" {
		t.Fatalf("external_idp must not hit AWS-direct host")
	}
}

func TestParseUsageLimits_Breakdown(t *testing.T) {
	body := []byte(`{
		"subscriptionType": "FREE",
		"daysUntilReset": 12,
		"usageBreakdownList": [
			{"resourceType": "CREDIT", "usageLimit": 1000, "currentUsage": 250, "unit": "REQUEST"},
			{"resourceType": "OVERAGE", "usageLimit": 0, "currentUsage": 0}
		]
	}`)
	limits, err := ParseUsageLimits(body)
	if err != nil {
		t.Fatalf("ParseUsageLimits: %v", err)
	}
	if limits.SubscriptionType != "FREE" {
		t.Fatalf("subscription = %q", limits.SubscriptionType)
	}
	if limits.DaysUntilReset == nil || *limits.DaysUntilReset != 12 {
		t.Fatalf("daysUntilReset = %v", limits.DaysUntilReset)
	}
	if len(limits.Breakdown) != 2 {
		t.Fatalf("breakdown len = %d", len(limits.Breakdown))
	}
	primary := limits.Primary()
	if primary == nil || primary.ResourceType != "CREDIT" {
		t.Fatalf("primary = %+v", primary)
	}
	if primary.Limit != 1000 || primary.Used != 250 {
		t.Fatalf("primary limit/used = %v/%v", primary.Limit, primary.Used)
	}
	if got := primary.Utilization(); got != 25 {
		t.Fatalf("utilization = %v, want 25", got)
	}
	if got := primary.Remaining(); got != 750 {
		t.Fatalf("remaining = %v, want 750", got)
	}
	if limits.Raw == nil {
		t.Fatal("raw should be populated")
	}
}

func TestParseUsageLimits_LenientAndAlternateKeys(t *testing.T) {
	// 备选字段名 + 数字以字符串编码,均应被宽松解析。
	body := []byte(`{
		"tier": "PRO",
		"limits": [
			{"name": "AGENTIC_REQUEST", "max": "500", "used": "125"}
		]
	}`)
	limits, err := ParseUsageLimits(body)
	if err != nil {
		t.Fatalf("ParseUsageLimits: %v", err)
	}
	if limits.SubscriptionType != "PRO" {
		t.Fatalf("subscription = %q", limits.SubscriptionType)
	}
	p := limits.Primary()
	if p == nil || p.Limit != 500 || p.Used != 125 {
		t.Fatalf("primary = %+v", p)
	}
	if limits.DaysUntilReset != nil {
		t.Fatalf("daysUntilReset should be nil when absent, got %v", *limits.DaysUntilReset)
	}
}

func TestParseUsageLimits_NonObject(t *testing.T) {
	for _, in := range []string{"[]", "42", `"x"`, "not-json"} {
		if _, err := ParseUsageLimits([]byte(in)); err == nil {
			t.Fatalf("expected error for non-object input %q", in)
		}
	}
}

func TestParseUsageLimits_EmptyObject(t *testing.T) {
	limits, err := ParseUsageLimits([]byte(`{}`))
	if err != nil {
		t.Fatalf("empty object should parse: %v", err)
	}
	if len(limits.Breakdown) != 0 || limits.Primary() != nil {
		t.Fatalf("expected empty breakdown, got %+v", limits.Breakdown)
	}
}

func TestParseUsageLimits_KiroSubscriptionInfo(t *testing.T) {
	// Kiro getUsageLimits:订阅信息嵌套在 subscriptionInfo 下,优先取可读的 subscriptionTitle。
	body := []byte(`{
		"nextDateReset": "2026-08-01T00:00:00.000Z",
		"usageBreakdownList": [{"resourceType":"CREDIT","usageLimit":5000,"currentUsage":20,"unit":"INVOCATIONS"}],
		"subscriptionInfo": {"type":"Q_DEVELOPER_STANDALONE_PRO","subscriptionTitle":"KIRO PRO MAX"}
	}`)
	limits, err := ParseUsageLimits(body)
	if err != nil {
		t.Fatalf("ParseUsageLimits: %v", err)
	}
	if limits.SubscriptionType != "KIRO PRO MAX" {
		t.Fatalf("subscription = %q, want KIRO PRO MAX (from subscriptionInfo.subscriptionTitle)", limits.SubscriptionType)
	}
	if p := limits.Primary(); p == nil || p.Limit != 5000 || p.Used != 20 {
		t.Fatalf("primary = %+v", p)
	}
}

// TestParseUsageLimits_RealBuilderIdFree 用真机(BuilderId/free)抓到的 AWS 直连
// AmazonCodeWhispererService.GetUsageLimits 响应验证解析(userId 已脱敏)。
func TestParseUsageLimits_RealBuilderIdFree(t *testing.T) {
	body := []byte(`{"daysUntilReset":0,"limits":[],"nextDateReset":1.7855424E9,` +
		`"overageConfiguration":{"overageStatus":"DISABLED"},` +
		`"subscriptionInfo":{"overageCapability":"OVERAGE_INCAPABLE","subscriptionTitle":"KIRO FREE","type":"Q_DEVELOPER_STANDALONE_FREE","upgradeCapability":"UPGRADE_CAPABLE"},` +
		`"usageBreakdownList":[{"currency":"USD","currentUsage":14,"currentUsageWithPrecision":14.04,"displayName":"Credit","overageCap":10000,"overageRate":0.04,"resourceType":"CREDIT","unit":"INVOCATIONS","usageLimit":50,"usageLimitWithPrecision":50.0}],` +
		`"userInfo":{"userId":"d-xxxx.xxxx"}}`)
	limits, err := ParseUsageLimits(body)
	if err != nil {
		t.Fatalf("ParseUsageLimits: %v", err)
	}
	if limits.SubscriptionType != "KIRO FREE" {
		t.Fatalf("subscription = %q, want KIRO FREE", limits.SubscriptionType)
	}
	if limits.DaysUntilReset == nil || *limits.DaysUntilReset != 0 {
		t.Fatalf("daysUntilReset = %v, want 0", limits.DaysUntilReset)
	}
	p := limits.Primary()
	if p == nil || p.ResourceType != "CREDIT" || p.Limit != 50 || p.Used != 14 {
		t.Fatalf("primary = %+v, want CREDIT 14/50", p)
	}
	if got := p.Utilization(); got < 27.99 || got > 28.01 {
		t.Fatalf("utilization = %v, want ~28", got)
	}
}
