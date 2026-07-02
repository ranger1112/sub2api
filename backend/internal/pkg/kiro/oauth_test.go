package kiro

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

const validRefreshToken = "rt_" + // >= 100 字符,非截断
	"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func fixedNow(t *testing.T, at time.Time) {
	t.Helper()
	orig := timeNow
	timeNow = func() time.Time { return at }
	t.Cleanup(func() { timeNow = orig })
}

func TestRefreshToken_SocialSuccess(t *testing.T) {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	fixedNow(t, now)

	var gotBody []byte
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"accessToken":"new-access","refreshToken":"new-refresh","profileArn":"arn:new","expiresIn":3600}`))
	}))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.socialRefreshURLOverride = srv.URL
	cred := &Credentials{RefreshToken: validRefreshToken, AccessToken: "old", AuthMethod: "social"}

	updated, err := RefreshToken(context.Background(), srv.Client(), cred, cfg)
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if updated.AccessToken != "new-access" || updated.RefreshToken != "new-refresh" || updated.ProfileArn != "arn:new" {
		t.Fatalf("credentials not updated: %+v", updated)
	}
	wantExpiry := now.Add(3600 * time.Second).Format(time.RFC3339)
	if updated.ExpiresAt != wantExpiry {
		t.Fatalf("expiresAt = %q, want %q", updated.ExpiresAt, wantExpiry)
	}
	// 请求体应为 {"refreshToken": "..."}
	if gjson.GetBytes(gotBody, "refreshToken").String() != validRefreshToken {
		t.Fatalf("request body = %s", gotBody)
	}
	if !strings.HasPrefix(gotUA, "KiroIDE-"+DefaultKiroVersion+"-") {
		t.Fatalf("social UA = %q", gotUA)
	}
	// 原始凭据不被修改
	if cred.AccessToken != "old" {
		t.Fatal("input credentials must not be mutated")
	}
}

func TestRefreshToken_InvalidGrantIsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid refresh token provided"}`))
	}))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.socialRefreshURLOverride = srv.URL
	cred := &Credentials{RefreshToken: validRefreshToken, AuthMethod: "social"}

	_, err := RefreshToken(context.Background(), srv.Client(), cred, cfg)
	var invalid *RefreshTokenInvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want RefreshTokenInvalidError", err)
	}
}

func TestRefreshToken_Social401IsGeneric(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`unauthorized`))
	}))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.socialRefreshURLOverride = srv.URL
	cred := &Credentials{RefreshToken: validRefreshToken, AuthMethod: "social"}

	_, err := RefreshToken(context.Background(), srv.Client(), cred, cfg)
	var invalid *RefreshTokenInvalidError
	if err == nil || errors.As(err, &invalid) {
		t.Fatalf("401 should be a generic error, got %v", err)
	}
}

func TestRefreshToken_IdcSuccessAndCamelCaseBody(t *testing.T) {
	now := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	fixedNow(t, now)

	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"accessToken":"idc-access","refreshToken":"idc-refresh","expiresIn":1800}`))
	}))
	defer srv.Close()

	cfg := DefaultClientConfig()
	cfg.idcRefreshURLOverride = srv.URL
	// authMethod 留空,应据 clientId/clientSecret 推断为 idc
	cred := &Credentials{RefreshToken: validRefreshToken, ClientID: "cid", ClientSecret: "csec"}

	updated, err := RefreshToken(context.Background(), srv.Client(), cred, cfg)
	if err != nil {
		t.Fatalf("RefreshToken idc: %v", err)
	}
	if updated.AccessToken != "idc-access" || updated.RefreshToken != "idc-refresh" {
		t.Fatalf("idc update failed: %+v", updated)
	}
	// 请求体应为 camelCase 键 + grantType 值 refresh_token
	for _, kv := range []struct{ k, v string }{
		{"clientId", "cid"}, {"clientSecret", "csec"},
		{"refreshToken", validRefreshToken}, {"grantType", "refresh_token"},
	} {
		if gjson.GetBytes(gotBody, kv.k).String() != kv.v {
			t.Fatalf("idc body missing %s=%s: %s", kv.k, kv.v, gotBody)
		}
	}
}

func TestRefreshToken_IdcRequiresClientCreds(t *testing.T) {
	cfg := DefaultClientConfig()
	cred := &Credentials{RefreshToken: validRefreshToken, AuthMethod: "idc"} // 无 clientId/secret
	if _, err := RefreshToken(context.Background(), http.DefaultClient, cred, cfg); err == nil {
		t.Fatal("idc without clientId should error")
	}
}

func TestRefreshToken_APIKeyNotSupported(t *testing.T) {
	cred := &Credentials{KiroAPIKey: "ksk_x", RefreshToken: validRefreshToken}
	if _, err := RefreshToken(context.Background(), http.DefaultClient, cred, DefaultClientConfig()); err == nil {
		t.Fatal("api key credential must not support refresh")
	}
}

func TestValidateRefreshToken(t *testing.T) {
	if err := validateRefreshToken(&Credentials{RefreshToken: validRefreshToken}); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if err := validateRefreshToken(&Credentials{RefreshToken: ""}); err == nil {
		t.Fatal("empty token should error")
	}
	if err := validateRefreshToken(&Credentials{RefreshToken: "short"}); err == nil {
		t.Fatal("short token should error")
	}
	if err := validateRefreshToken(&Credentials{RefreshToken: strings.Repeat("a", 120) + "..."}); err == nil {
		t.Fatal("truncated token (with ...) should error")
	}
}
