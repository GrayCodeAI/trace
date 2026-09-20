package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOIDCLoginWithPKCEAndAutoProvision(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	var provider *httptest.Server
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": provider.URL, "authorization_endpoint": provider.URL + "/authorize", "token_endpoint": provider.URL + "/token", "userinfo_endpoint": provider.URL + "/userinfo"})
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "provider-token", "token_type": "Bearer"})
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer provider-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"sub": "subject-1", "preferred_username": "oidc-user"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := oidcConfig{Issuer: provider.URL, ClientID: "trace", ClientSecret: "secret", RedirectURL: "http://127.0.0.1:8787/login/oidc/callback", AutoProvision: true}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(root, oidcConfigFile), append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	start := httptest.NewRequest(http.MethodGet, "/login/oidc", nil)
	startRes := httptest.NewRecorder()
	a.ServeHTTP(startRes, start)
	if startRes.Code != http.StatusFound {
		t.Fatalf("OIDC start: %d %s", startRes.Code, startRes.Body.String())
	}
	location, err := url.Parse(startRes.Header().Get("Location"))
	if err != nil || location.Query().Get("state") == "" || location.Query().Get("code_challenge") == "" {
		t.Fatalf("OIDC redirect missing state/PKCE: %s", startRes.Header().Get("Location"))
	}
	callback := httptest.NewRequest(http.MethodGet, "/login/oidc/callback?code=one-time&state="+url.QueryEscape(location.Query().Get("state")), nil)
	callback.AddCookie(startRes.Result().Cookies()[0])
	callbackRes := httptest.NewRecorder()
	a.ServeHTTP(callbackRes, callback)
	if callbackRes.Code != http.StatusSeeOther || callbackRes.Header().Get("Location") != "/app" {
		t.Fatalf("OIDC callback: %d %s", callbackRes.Code, callbackRes.Body.String())
	}
	db, err := a.store.loadUsers()
	if err != nil || db.Users["oidc-user"].Hash == "" {
		t.Fatalf("OIDC user was not provisioned: %#v err=%v", db.Users, err)
	}
	if strings.Contains(callbackRes.Body.String(), "provider-token") {
		t.Fatal("provider token leaked in callback response")
	}
}
