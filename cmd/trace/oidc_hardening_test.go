package main

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestOIDCAuthorizationRequestCarriesNonce(t *testing.T) {
	a, root := newOIDCTestApp(t)
	p := newFakeOIDCProvider(t)
	configureOIDC(t, root, p, true)
	req, _ := http.NewRequest(http.MethodGet, "/login/oidc", nil)
	res := newRecorder()
	a.ServeHTTP(res, req)
	location, err := url.Parse(res.Header().Get("Location"))
	if err != nil || len(location.Query().Get("nonce")) < 20 {
		t.Fatalf("authorization request has no nonce: %s", res.Header().Get("Location"))
	}
}

// TestOIDCIDTokenValidation checks the ID token rules: the nonce from the
// authorization request, a non-empty matching issuer, and a signing key of
// at least 2048 bits.
func TestOIDCIDTokenValidation(t *testing.T) {
	a, root := newOIDCTestApp(t)
	p := newFakeOIDCProvider(t)
	configureOIDC(t, root, p, true)
	userinfo := map[string]any{"sub": "subject-1", "preferred_username": "oidc-user"}

	cases := map[string]func(map[string]any){
		"wrong nonce":   func(c map[string]any) { c["nonce"] = "not-the-nonce" },
		"missing nonce": func(c map[string]any) { delete(c, "nonce") },
		"empty issuer":  func(c map[string]any) { c["iss"] = "" },
		"other issuer":  func(c map[string]any) { c["iss"] = "https://idp.example" },
		"expired":       func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() },
		"other client":  func(c map[string]any) { c["aud"] = "someone-else" },
	}
	for name, mutate := range cases {
		claims := p.validIDToken("subject-1")
		mutate(claims)
		p.set(userinfo, claims)
		if res := oidcSignIn(t, a, p); res.Code == http.StatusSeeOther || sessionCookieIssued(res) {
			t.Fatalf("%s: invalid ID token was accepted", name)
		}
	}
	p.set(userinfo, p.validIDToken("subject-1"))
	if res := oidcSignIn(t, a, p); res.Code != http.StatusSeeOther {
		t.Fatalf("valid ID token refused: %d %s", res.Code, res.Body.String())
	}

	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.key = weak
	p.mu.Unlock()
	p.set(userinfo, p.validIDToken("subject-1"))
	if res := oidcSignIn(t, a, p); res.Code == http.StatusSeeOther {
		t.Fatal("ID token signed with a 1024-bit key was accepted")
	}
}

func TestOIDCProviderRequestsAreBounded(t *testing.T) {
	previous := oidcHTTPTimeout
	oidcHTTPTimeout = 300 * time.Millisecond
	defer func() { oidcHTTPTimeout = previous }()
	a, root := newOIDCTestApp(t)
	p := newFakeOIDCProvider(t)
	configureOIDC(t, root, p, true)
	p.set(map[string]any{"sub": "subject-1", "preferred_username": "oidc-user"}, nil)
	p.mu.Lock()
	p.tokenDelay = 3 * time.Second
	p.mu.Unlock()
	started := time.Now()
	res := oidcSignIn(t, a, p)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("a stalled token endpoint held the callback for %s", elapsed)
	}
	if res.Code == http.StatusSeeOther {
		t.Fatal("sign-in succeeded although the token request timed out")
	}
}
