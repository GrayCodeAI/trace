package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeOIDCProvider is a minimal OpenID provider for tests. It serves
// discovery, JWKS, token, and userinfo endpoints and can sign ID tokens.
type fakeOIDCProvider struct {
	t      *testing.T
	server *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	// userinfo is returned from the userinfo endpoint.
	userinfo map[string]any
	// idToken, when non-nil, is signed and returned from the token endpoint;
	// the special value "$nonce" for "nonce" is replaced by the nonce Trace
	// sent in its authorization request.
	idToken map[string]any
	nonce   string
	// tokenDelay stalls the token endpoint.
	tokenDelay time.Duration
}

var (
	oidcTestKeyOnce sync.Once
	oidcTestKey     *rsa.PrivateKey
)

func newFakeOIDCProvider(t *testing.T) *fakeOIDCProvider {
	t.Helper()
	oidcTestKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		oidcTestKey = key
	})
	p := &fakeOIDCProvider{t: t, key: oidcTestKey}
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	return p
}

func (p *fakeOIDCProvider) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": p.server.URL, "authorization_endpoint": p.server.URL + "/authorize", "token_endpoint": p.server.URL + "/token", "userinfo_endpoint": p.server.URL + "/userinfo", "jwks_uri": p.server.URL + "/jwks"})
	case "/jwks":
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "test", "n": base64.RawURLEncoding.EncodeToString(p.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(p.key.E)).Bytes())}}})
	case "/token":
		if p.tokenDelay > 0 {
			delay := p.tokenDelay
			p.mu.Unlock()
			time.Sleep(delay)
			p.mu.Lock()
		}
		response := map[string]string{"access_token": "provider-token", "token_type": "Bearer"}
		if p.idToken != nil {
			response["id_token"] = p.signIDToken()
		}
		_ = json.NewEncoder(w).Encode(response)
	case "/userinfo":
		if r.Header.Get("Authorization") != "Bearer provider-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(p.userinfo)
	default:
		http.NotFound(w, r)
	}
}

func (p *fakeOIDCProvider) signIDToken() string {
	claims := map[string]any{}
	for k, v := range p.idToken {
		claims[k] = v
	}
	if claims["nonce"] == "$nonce" {
		claims["nonce"] = p.nonce
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, digest[:])
	if err != nil {
		p.t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// validIDToken returns ID token claims that pass verification for subject.
func (p *fakeOIDCProvider) validIDToken(subject string) map[string]any {
	return map[string]any{"iss": p.server.URL, "aud": "trace", "sub": subject, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": "$nonce"}
}

func (p *fakeOIDCProvider) set(userinfo, idToken map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.userinfo, p.idToken = userinfo, idToken
}

// configureOIDC writes an OIDC configuration pointing at the provider.
func configureOIDC(t *testing.T, root string, p *fakeOIDCProvider, autoProvision bool, domains ...string) {
	t.Helper()
	cfg := oidcConfig{Issuer: p.server.URL, ClientID: "trace", ClientSecret: "secret", RedirectURL: "http://127.0.0.1:8787/login/oidc/callback", AutoProvision: autoProvision, AllowedEmailDomains: domains}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(root, oidcConfigFile), append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

// oidcSignIn runs the browser flow: start, then the provider callback.
func oidcSignIn(t *testing.T, a *app, p *fakeOIDCProvider) *httptest.ResponseRecorder {
	t.Helper()
	start := httptest.NewRequest(http.MethodGet, "/login/oidc", nil)
	startRes := httptest.NewRecorder()
	a.ServeHTTP(startRes, start)
	if startRes.Code != http.StatusFound {
		t.Fatalf("OIDC start: %d %s", startRes.Code, startRes.Body.String())
	}
	location, err := url.Parse(startRes.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.nonce = location.Query().Get("nonce")
	p.mu.Unlock()
	callback := httptest.NewRequest(http.MethodGet, "/login/oidc/callback?code=one-time&state="+url.QueryEscape(location.Query().Get("state")), nil)
	for _, cookie := range startRes.Result().Cookies() {
		callback.AddCookie(cookie)
	}
	res := httptest.NewRecorder()
	a.ServeHTTP(res, callback)
	return res
}

func sessionCookieIssued(res *httptest.ResponseRecorder) bool {
	for _, cookie := range res.Result().Cookies() {
		if cookie.Name == "trace_session" && cookie.Value != "" {
			return true
		}
	}
	return false
}
