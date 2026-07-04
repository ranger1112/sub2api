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
	if req.URL.String() != "https://q.us-east-1.amazonaws.com/getUsageLimits" {
		t.Fatalf("url = %s", req.URL)
	}
	if req.Host != "q.us-east-1.amazonaws.com" {
		t.Fatalf("host = %s", req.Host)
	}
	checks := map[string]string{
		"Authorization":               "Bearer access-tok",
		"x-amzn-codewhisperer-optout": "true",
		"x-amzn-kiro-agent-mode":      "vibe",
		"Content-Type":                "application/json",
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
	if req.URL.String() != "https://q.eu-west-1.amazonaws.com/getUsageLimits" {
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
	if got := req.Header["TokenType"]; len(got) != 1 || got[0] != "EXTERNAL_IDP" {
		t.Fatalf("TokenType header = %v, want [EXTERNAL_IDP]", got)
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
