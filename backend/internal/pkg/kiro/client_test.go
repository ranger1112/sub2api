package kiro

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestMachineID_DerivationPaths(t *testing.T) {
	cfg := DefaultClientConfig()

	// OAuth 凭据:基于 refreshToken 派生
	oauth := &Credentials{RefreshToken: "rt-123"}
	if got, want := MachineID(oauth, cfg), sha256Hex("KotlinNativeAPI/rt-123"); got != want {
		t.Fatalf("oauth machineId = %q, want %q", got, want)
	}
	// API Key 凭据:基于 kiroApiKey 派生
	apikey := &Credentials{KiroAPIKey: "ksk_abc"}
	if got, want := MachineID(apikey, cfg), sha256Hex("KiroAPIKey/ksk_abc"); got != want {
		t.Fatalf("apikey machineId = %q, want %q", got, want)
	}
	// 互斥:同时有 apiKey + refreshToken 走 apiKey 分支
	both := &Credentials{KiroAPIKey: "ksk_x", RefreshToken: "should-not-use"}
	if got, want := MachineID(both, cfg), sha256Hex("KiroAPIKey/ksk_x"); got != want {
		t.Fatalf("mutual exclusivity failed: %q", got)
	}
}

func TestMachineID_ExplicitAndConfigOverride(t *testing.T) {
	hex64 := strings.Repeat("a", 64)
	// 凭据级 machineId 优先
	cred := &Credentials{MachineID: hex64, RefreshToken: "rt"}
	if got := MachineID(cred, DefaultClientConfig()); got != hex64 {
		t.Fatalf("explicit machineId not used: %q", got)
	}
	// 凭据级覆盖 config 级
	cfg := DefaultClientConfig()
	cfg.MachineID = strings.Repeat("b", 64)
	cred2 := &Credentials{MachineID: hex64}
	if got := MachineID(cred2, cfg); got != hex64 {
		t.Fatalf("credential machineId should override config: %q", got)
	}
	// UUID 归一化为 64 位
	uuidCred := &Credentials{MachineID: "2582956e-cc88-4669-b546-07adbffcb894"}
	got := MachineID(uuidCred, DefaultClientConfig())
	if len(got) != 64 || got != "2582956ecc884669b54607adbffcb8942582956ecc884669b54607adbffcb894" {
		t.Fatalf("uuid normalize = %q", got)
	}
}

func TestMachineID_FallbackStablePerID(t *testing.T) {
	c := &Credentials{ID: 987654321} // 无派生材料
	a := MachineID(c, DefaultClientConfig())
	b := MachineID(c, DefaultClientConfig())
	if a != b {
		t.Fatalf("fallback not stable: %q vs %q", a, b)
	}
	if len(a) != 64 || !isHex(a) {
		t.Fatalf("fallback not 64-hex: %q", a)
	}
	other := MachineID(&Credentials{ID: 987654322}, DefaultClientConfig())
	if other == a {
		t.Fatal("different credential IDs should get different fallback ids")
	}
}

func TestNormalizeMachineID(t *testing.T) {
	if normalizeMachineID(strings.Repeat("f", 64)) != strings.Repeat("f", 64) {
		t.Fatal("64-hex should pass through")
	}
	if normalizeMachineID("invalid") != "" {
		t.Fatal("invalid should be empty")
	}
	if normalizeMachineID(strings.Repeat("g", 64)) != "" {
		t.Fatal("non-hex should be empty")
	}
}

func TestInjectProfileArn(t *testing.T) {
	body := []byte(`{"conversationState":{"conversationId":"c1"}}`)
	out := InjectProfileArn(body, "arn:aws:x:profile/ABC")
	if gjson.GetBytes(out, "profileArn").String() != "arn:aws:x:profile/ABC" {
		t.Fatalf("profileArn not injected: %s", out)
	}
	if gjson.GetBytes(out, "conversationState.conversationId").String() != "c1" {
		t.Fatalf("original content lost: %s", out)
	}
	// 空 arn → 原样
	if got := InjectProfileArn(body, ""); string(got) != string(body) {
		t.Fatalf("empty arn should return original: %s", got)
	}
	// 覆盖已有
	over := InjectProfileArn([]byte(`{"profileArn":"old"}`), "new")
	if gjson.GetBytes(over, "profileArn").String() != "new" {
		t.Fatalf("should overwrite existing profileArn: %s", over)
	}
	// 非法 JSON → 原样
	if got := InjectProfileArn([]byte("not-json"), "arn"); string(got) != "not-json" {
		t.Fatalf("invalid json should return original: %s", got)
	}
}

func TestBuildAPIRequest_OAuthCredential(t *testing.T) {
	cred := &Credentials{AccessToken: "access-tok", ProfileArn: "arn:profile/X", RefreshToken: "rt"}
	cfg := DefaultClientConfig()
	req, err := BuildAPIRequest(context.Background(), cred, cfg, []byte(`{"conversationState":{}}`))
	if err != nil {
		t.Fatalf("BuildAPIRequest: %v", err)
	}
	if req.Method != "POST" {
		t.Fatalf("method = %s", req.Method)
	}
	if req.URL.String() != "https://q.us-east-1.amazonaws.com/generateAssistantResponse" {
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
	if !strings.Contains(req.Header.Get("x-amz-user-agent"), "KiroIDE-"+DefaultKiroVersion+"-") {
		t.Fatalf("x-amz-user-agent = %q", req.Header.Get("x-amz-user-agent"))
	}
	if req.Header.Get("tokentype") != "" {
		t.Fatalf("oauth credential must NOT set tokentype, got %q", req.Header.Get("tokentype"))
	}
	body, _ := io.ReadAll(req.Body)
	if gjson.GetBytes(body, "profileArn").String() != "arn:profile/X" {
		t.Fatalf("profileArn not injected into body: %s", body)
	}
}

func TestBuildAPIRequest_APIKeyCredential(t *testing.T) {
	cred := &Credentials{KiroAPIKey: "ksk_secret"}
	req, err := BuildAPIRequest(context.Background(), cred, DefaultClientConfig(), []byte(`{}`))
	if err != nil {
		t.Fatalf("BuildAPIRequest: %v", err)
	}
	if req.Header.Get("Authorization") != "Bearer ksk_secret" {
		t.Fatalf("apikey bearer = %q", req.Header.Get("Authorization"))
	}
	if req.Header.Get("tokentype") != "API_KEY" {
		t.Fatalf("apikey credential must set tokentype API_KEY, got %q", req.Header.Get("tokentype"))
	}
}

func TestEffectiveRegions_Precedence(t *testing.T) {
	cfg := ClientConfig{Region: "cfg-region", AuthRegion: "cfg-auth", APIRegion: "cfg-api"}
	// 凭据级优先
	cred := &Credentials{AuthRegion: "cred-auth", APIRegion: "cred-api"}
	if cred.EffectiveAuthRegion(cfg) != "cred-auth" || cred.EffectiveAPIRegion(cfg) != "cred-api" {
		t.Fatal("credential region should win")
	}
	// 凭据 region 回退
	cred2 := &Credentials{Region: "cred-region"}
	if cred2.EffectiveAuthRegion(cfg) != "cred-region" || cred2.EffectiveAPIRegion(cfg) != "cred-region" {
		t.Fatal("credential.Region should be used before config")
	}
	// config 回退
	cred3 := &Credentials{}
	if cred3.EffectiveAuthRegion(cfg) != "cfg-auth" || cred3.EffectiveAPIRegion(cfg) != "cfg-api" {
		t.Fatal("config region fallback failed")
	}
}
