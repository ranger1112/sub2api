package service

import "testing"

func TestAccountToKiroCredentials_RoundTrip(t *testing.T) {
	acc := &Account{
		ID: 5, Platform: PlatformKiro, Type: AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "at", "refresh_token": "rt", "profile_arn": "arn", "auth_method": "idc",
			"client_id": "cid", "client_secret": "cs", "region": "us-east-1", "expires_at": "2026-01-01T00:00:00Z",
		},
	}
	cred := AccountToKiroCredentials(acc)
	if cred.ID != 5 || cred.AccessToken != "at" || cred.RefreshToken != "rt" || cred.ProfileArn != "arn" ||
		cred.AuthMethod != "idc" || cred.ClientID != "cid" || cred.ClientSecret != "cs" || cred.Region != "us-east-1" {
		t.Fatalf("mapping wrong: %+v", cred)
	}

	back := BuildKiroAccountCredentials(cred)
	if back["access_token"] != "at" || back["client_secret"] != "cs" || back["auth_method"] != "idc" || back["region"] != "us-east-1" {
		t.Fatalf("build back wrong: %+v", back)
	}
	// 空字段不应出现在结果中
	if _, ok := back["kiro_api_key"]; ok {
		t.Fatalf("empty field should be omitted: %+v", back)
	}
}

func TestAccount_IsKiroHelpers(t *testing.T) {
	oauth := &Account{Platform: PlatformKiro, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "at", "refresh_token": "rt"}}
	if !oauth.IsKiro() || !oauth.IsKiroOAuth() {
		t.Fatal("should be kiro oauth")
	}
	if oauth.GetKiroAccessToken() != "at" || oauth.GetKiroRefreshToken() != "rt" {
		t.Fatalf("getters wrong: at=%q rt=%q", oauth.GetKiroAccessToken(), oauth.GetKiroRefreshToken())
	}

	apikey := &Account{Platform: PlatformKiro, Type: AccountTypeAPIKey}
	if apikey.IsKiroOAuth() {
		t.Fatal("api-key account is not oauth")
	}
	if apikey.GetKiroRefreshToken() != "" {
		t.Fatal("api-key account has no refresh token")
	}

	notKiro := &Account{Platform: PlatformGrok}
	if notKiro.IsKiro() || notKiro.GetKiroAccessToken() != "" {
		t.Fatal("grok account is not kiro")
	}
}
