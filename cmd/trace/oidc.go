package main

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const oidcConfigFile = "oidc.json"

type oidcConfig struct {
	Issuer        string   `json:"issuer"`
	ClientID      string   `json:"client_id"`
	ClientSecret  string   `json:"client_secret"`
	RedirectURL   string   `json:"redirect_url"`
	AutoProvision bool     `json:"auto_provision"`
	Scopes        []string `json:"scopes,omitempty"`
	// AllowedEmailDomains, when set, admits only identities whose userinfo
	// reports email_verified and an email in one of these domains.
	AllowedEmailDomains []string `json:"allowed_email_domains,omitempty"`
}

type oidcDiscoveryDocument struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

// oidcHTTPTimeout bounds every request Trace makes to the identity provider,
// so a stalled provider cannot pin sign-in handlers indefinitely.
var oidcHTTPTimeout = 10 * time.Second

// oidcHTTPClient is used for all provider requests. It does not follow
// redirects: discovery, JWKS, token, and userinfo endpoints must answer
// directly at the URLs the provider advertised.
func oidcHTTPClient() *http.Client {
	return &http.Client{Timeout: oidcHTTPTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// oidcMinRSABits is the smallest RSA modulus accepted for ID token signatures.
const oidcMinRSABits = 2048

type oidcTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	IDToken     string `json:"id_token"`
}

func (s *store) loadOIDC() (oidcConfig, error) {
	b, err := os.ReadFile(filepath.Join(s.root, oidcConfigFile))
	if errors.Is(err, os.ErrNotExist) {
		return oidcConfig{}, nil
	}
	if err != nil {
		return oidcConfig{}, err
	}
	var cfg oidcConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return oidcConfig{}, fmt.Errorf("read OIDC config: %w", err)
	}
	if strings.TrimSpace(cfg.Issuer) == "" || strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.RedirectURL) == "" {
		return oidcConfig{}, errors.New("OIDC config requires issuer, client_id, and redirect_url")
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "profile", "email"}
	}
	return cfg, nil
}

func oidcURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && isLoopback(u.Hostname()))
}

func (a *app) oidcDiscovery(cfg oidcConfig) (oidcDiscoveryDocument, error) {
	if !oidcURLAllowed(cfg.Issuer) {
		return oidcDiscoveryDocument{}, errors.New("OIDC issuer must use HTTPS (or loopback HTTP for local development)")
	}
	endpoint := strings.TrimRight(cfg.Issuer, "/") + "/.well-known/openid-configuration"
	ctx, cancel := context.WithTimeout(context.Background(), oidcHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return oidcDiscoveryDocument{}, err
	}
	res, err := oidcHTTPClient().Do(req)
	if err != nil {
		return oidcDiscoveryDocument{}, fmt.Errorf("OIDC discovery: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return oidcDiscoveryDocument{}, fmt.Errorf("OIDC discovery returned %s", res.Status)
	}
	var doc oidcDiscoveryDocument
	if err := json.NewDecoder(io.LimitReader(res.Body, 256<<10)).Decode(&doc); err != nil {
		return oidcDiscoveryDocument{}, err
	}
	if doc.Issuer != "" && strings.TrimRight(doc.Issuer, "/") != strings.TrimRight(cfg.Issuer, "/") {
		return oidcDiscoveryDocument{}, errors.New("OIDC discovery issuer does not match configuration")
	}
	if !oidcURLAllowed(doc.AuthorizationEndpoint) || !oidcURLAllowed(doc.TokenEndpoint) || !oidcURLAllowed(doc.UserinfoEndpoint) {
		return oidcDiscoveryDocument{}, errors.New("OIDC discovery returned unsafe or incomplete endpoints")
	}
	if strings.TrimSpace(doc.JWKSURI) != "" && !oidcURLAllowed(doc.JWKSURI) {
		return oidcDiscoveryDocument{}, errors.New("OIDC discovery returned an unsafe JWKS endpoint")
	}
	return doc, nil
}

type oidcJWTHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type oidcJWTClaims struct {
	Issuer   string          `json:"iss"`
	Audience json.RawMessage `json:"aud"`
	Subject  string          `json:"sub"`
	Expiry   int64           `json:"exp"`
	Nonce    string          `json:"nonce"`
}

type oidcJWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (a *app) verifyOIDCIDToken(cfg oidcConfig, doc oidcDiscoveryDocument, raw, nonce string) (oidcJWTClaims, error) {
	if doc.JWKSURI == "" {
		return oidcJWTClaims{}, errors.New("OIDC provider did not advertise JWKS")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return oidcJWTClaims{}, errors.New("invalid OIDC ID token")
	}
	var header oidcJWTHeader
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(headerBytes, &header) != nil || header.Alg != "RS256" {
		return oidcJWTClaims{}, errors.New("OIDC ID token must use RS256")
	}
	var claims oidcJWTClaims
	claimBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(claimBytes, &claims) != nil || claims.Subject == "" {
		return oidcJWTClaims{}, errors.New("invalid OIDC ID token claims")
	}
	if claims.Issuer == "" || strings.TrimRight(claims.Issuer, "/") != strings.TrimRight(cfg.Issuer, "/") {
		return oidcJWTClaims{}, errors.New("OIDC ID token issuer mismatch")
	}
	// Trace sends a nonce with every authorization request; the ID token
	// must echo it so a token minted for another login cannot be replayed.
	if nonce == "" || !hmac.Equal([]byte(claims.Nonce), []byte(nonce)) {
		return oidcJWTClaims{}, errors.New("OIDC ID token nonce mismatch")
	}
	if claims.Expiry == 0 || time.Now().Unix() >= claims.Expiry {
		return oidcJWTClaims{}, errors.New("OIDC ID token is expired")
	}
	var audience string
	if len(claims.Audience) > 0 && claims.Audience[0] == '"' {
		_ = json.Unmarshal(claims.Audience, &audience)
	} else {
		var audiences []string
		_ = json.Unmarshal(claims.Audience, &audiences)
		for _, candidate := range audiences {
			if candidate == cfg.ClientID {
				audience = candidate
			}
		}
	}
	if audience != cfg.ClientID {
		return oidcJWTClaims{}, errors.New("OIDC ID token audience mismatch")
	}
	ctx, cancel := context.WithTimeout(context.Background(), oidcHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, doc.JWKSURI, nil)
	if err != nil {
		return oidcJWTClaims{}, err
	}
	res, err := oidcHTTPClient().Do(req)
	if err != nil {
		return oidcJWTClaims{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return oidcJWTClaims{}, fmt.Errorf("OIDC JWKS returned %s", res.Status)
	}
	var keys struct {
		Keys []oidcJWK `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&keys); err != nil {
		return oidcJWTClaims{}, err
	}
	var jwk *oidcJWK
	for i := range keys.Keys {
		if keys.Keys[i].Kty == "RSA" && (header.Kid == "" || keys.Keys[i].Kid == header.Kid) {
			jwk = &keys.Keys[i]
			break
		}
	}
	if jwk == nil {
		return oidcJWTClaims{}, errors.New("OIDC signing key not found")
	}
	nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
	if err != nil {
		return oidcJWTClaims{}, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
	if err != nil {
		return oidcJWTClaims{}, err
	}
	e := 0
	for _, b := range eBytes {
		e = e<<8 | int(b)
	}
	if e == 0 {
		return oidcJWTClaims{}, errors.New("invalid OIDC RSA exponent")
	}
	key := &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}
	if key.N.BitLen() < oidcMinRSABits {
		return oidcJWTClaims{}, fmt.Errorf("OIDC signing key is shorter than %d bits", oidcMinRSABits)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return oidcJWTClaims{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return oidcJWTClaims{}, errors.New("OIDC ID token signature is invalid")
	}
	return claims, nil
}

func randomOIDCString(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (a *app) oidcStateCookie(r *http.Request, state, verifier, nonce string) *http.Cookie {
	payload := base64.RawURLEncoding.EncodeToString([]byte(state + "\x00" + verifier + "\x00" + nonce))
	h := hmac.New(sha256.New, a.sessionKey)
	_, _ = h.Write([]byte("trace-oidc\x00" + payload))
	value := payload + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	return &http.Cookie{Name: "trace_oidc_state", Value: value, Path: "/login/oidc", MaxAge: 600, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secureCookie(r)}
}

// readOIDCState returns the state, PKCE verifier, and nonce stored in the
// signed state cookie.
func (a *app) readOIDCState(r *http.Request) (string, string, string, error) {
	cookie, err := r.Cookie("trace_oidc_state")
	if err != nil {
		return "", "", "", errors.New("OIDC state cookie missing")
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 {
		return "", "", "", errors.New("invalid OIDC state")
	}
	h := hmac.New(sha256.New, a.sessionKey)
	_, _ = h.Write([]byte("trace-oidc\x00" + parts[0]))
	got, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(got, h.Sum(nil)) {
		return "", "", "", errors.New("invalid OIDC state signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", "", errors.New("invalid OIDC state payload")
	}
	fields := strings.Split(string(payload), "\x00")
	if len(fields) != 3 || fields[0] == "" || fields[1] == "" || fields[2] == "" {
		return "", "", "", errors.New("invalid OIDC state payload")
	}
	return fields[0], fields[1], fields[2], nil
}

func (a *app) oidcLogin(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.store.loadOIDC()
	if err != nil || cfg.Issuer == "" {
		http.NotFound(w, r)
		return
	}
	doc, err := a.oidcDiscovery(cfg)
	if err != nil {
		http.Error(w, "OIDC is unavailable", http.StatusBadGateway)
		return
	}
	state, err := randomOIDCString(24)
	if err != nil {
		http.Error(w, "cannot create OIDC state", http.StatusInternalServerError)
		return
	}
	verifier, err := randomOIDCString(32)
	if err != nil {
		http.Error(w, "cannot create OIDC verifier", http.StatusInternalServerError)
		return
	}
	nonce, err := randomOIDCString(24)
	if err != nil {
		http.Error(w, "cannot create OIDC nonce", http.StatusInternalServerError)
		return
	}
	hash := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])
	query := url.Values{"response_type": {"code"}, "client_id": {cfg.ClientID}, "redirect_uri": {cfg.RedirectURL}, "scope": {strings.Join(cfg.Scopes, " ")}, "state": {state}, "nonce": {nonce}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	http.SetCookie(w, a.oidcStateCookie(r, state, verifier, nonce))
	http.Redirect(w, r, doc.AuthorizationEndpoint+"?"+query.Encode(), http.StatusFound)
}

func (a *app) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		a.loginPage(w, r, "OIDC sign-in was cancelled: "+providerError, http.StatusUnauthorized)
		return
	}
	state, verifier, nonce, err := a.readOIDCState(r)
	if err != nil || !hmac.Equal([]byte(state), []byte(r.URL.Query().Get("state"))) {
		a.loginPage(w, r, "OIDC state validation failed.", http.StatusForbidden)
		return
	}
	cfg, err := a.store.loadOIDC()
	if err != nil {
		a.loginPage(w, r, "OIDC configuration is invalid.", http.StatusInternalServerError)
		return
	}
	doc, err := a.oidcDiscovery(cfg)
	if err != nil {
		a.loginPage(w, r, "OIDC is unavailable.", http.StatusBadGateway)
		return
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {r.URL.Query().Get("code")}, "redirect_uri": {cfg.RedirectURL}, "client_id": {cfg.ClientID}, "client_secret": {cfg.ClientSecret}, "code_verifier": {verifier}}
	ctx, cancel := context.WithTimeout(r.Context(), oidcHTTPTimeout)
	defer cancel()
	tokenReq, err := http.NewRequestWithContext(ctx, http.MethodPost, doc.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		a.loginPage(w, r, "OIDC token exchange failed.", http.StatusBadGateway)
		return
	}
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := oidcHTTPClient().Do(tokenReq)
	if err != nil {
		a.loginPage(w, r, "OIDC token exchange failed.", http.StatusBadGateway)
		return
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		a.loginPage(w, r, "OIDC token exchange failed.", http.StatusUnauthorized)
		return
	}
	var tokens oidcTokenResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&tokens); err != nil || tokens.AccessToken == "" {
		a.loginPage(w, r, "OIDC token response was invalid.", http.StatusBadGateway)
		return
	}
	var idClaims *oidcJWTClaims
	if tokens.IDToken != "" {
		verified, err := a.verifyOIDCIDToken(cfg, doc, tokens.IDToken, nonce)
		if err != nil {
			a.loginPage(w, r, "OIDC ID token validation failed.", http.StatusUnauthorized)
			return
		}
		idClaims = &verified
	}
	infoReq, err := http.NewRequestWithContext(ctx, http.MethodGet, doc.UserinfoEndpoint, nil)
	if err != nil {
		a.loginPage(w, r, "OIDC user information failed.", http.StatusBadGateway)
		return
	}
	infoReq.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	infoRes, err := oidcHTTPClient().Do(infoReq)
	if err != nil {
		a.loginPage(w, r, "OIDC user information failed.", http.StatusBadGateway)
		return
	}
	defer infoRes.Body.Close()
	var claims oidcUserinfo
	if infoRes.StatusCode < 200 || infoRes.StatusCode >= 300 || json.NewDecoder(io.LimitReader(infoRes.Body, 256<<10)).Decode(&claims) != nil {
		a.loginPage(w, r, "OIDC user information was invalid.", http.StatusUnauthorized)
		return
	}
	// OIDC Core 5.3.2: the userinfo subject must be the ID token's subject.
	if claims.Subject == "" || (idClaims != nil && idClaims.Subject != claims.Subject) {
		a.loginPage(w, r, "OIDC user information does not match the ID token.", http.StatusUnauthorized)
		return
	}
	if !oidcEmailAllowed(cfg, claims) {
		a.loginPage(w, r, "Your OIDC account's verified email domain is not allowed on this Trace node.", http.StatusForbidden)
		return
	}
	issuer := strings.TrimRight(cfg.Issuer, "/")
	name, u, found, err := a.store.oidcAccount(issuer, claims.Subject)
	if err != nil {
		a.loginPage(w, r, "cannot load Trace users.", http.StatusInternalServerError)
		return
	}
	if !found {
		if !cfg.AutoProvision {
			a.loginPage(w, r, "Your OIDC identity is not linked to a Trace account. An administrator can link it with: trace sso oidc link USER "+claims.Subject, http.StatusForbidden)
			return
		}
		name, u, err = a.store.provisionOIDCAccount(issuer, claims)
		if err != nil {
			a.loginPage(w, r, err.Error(), http.StatusForbidden)
			return
		}
	}
	if u.Disabled {
		a.loginPage(w, r, "Your Trace account is disabled.", http.StatusForbidden)
		return
	}
	if u.TOTPEnabled {
		// OIDC sign-in cannot collect Trace's TOTP code, so an account that
		// requires it must use token sign-in instead of bypassing it.
		a.loginPage(w, r, "This Trace account requires two-factor sign-in with its token and code.", http.StatusForbidden)
		return
	}
	a.issueSession(w, r, name, u)
	http.SetCookie(w, &http.Cookie{Name: "trace_oidc_state", Path: "/login/oidc", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secureCookie(r)})
	http.Redirect(w, r, "/app", http.StatusSeeOther)
}

// oidcUserinfo holds the userinfo claims Trace uses. email_verified is kept
// raw because some providers send it as a string.
type oidcUserinfo struct {
	Subject       string          `json:"sub"`
	Name          string          `json:"preferred_username"`
	Email         string          `json:"email"`
	EmailVerified json.RawMessage `json:"email_verified"`
}

func (c oidcUserinfo) emailVerified() bool {
	value := strings.Trim(strings.TrimSpace(string(c.EmailVerified)), `"`)
	return strings.EqualFold(value, "true")
}

func oidcEmailAllowed(cfg oidcConfig, claims oidcUserinfo) bool {
	if len(cfg.AllowedEmailDomains) == 0 {
		return true
	}
	at := strings.LastIndex(claims.Email, "@")
	if !claims.emailVerified() || at < 1 {
		return false
	}
	domain := strings.ToLower(claims.Email[at+1:])
	for _, allowed := range cfg.AllowedEmailDomains {
		if strings.EqualFold(strings.TrimSpace(allowed), domain) {
			return true
		}
	}
	return false
}

// oidcAccount returns the local account bound to (issuer, subject).
func (s *store) oidcAccount(issuer, subject string) (string, userRecord, bool, error) {
	db, err := s.loadUsers()
	if err != nil {
		return "", userRecord{}, false, err
	}
	for name, u := range db.Users {
		if u.OIDCSubject != "" && u.OIDCSubject == subject && strings.TrimRight(u.OIDCIssuer, "/") == issuer {
			return name, u, true, nil
		}
	}
	return "", userRecord{}, false, nil
}

// provisionOIDCAccount creates a new account bound to the identity. It never
// adopts an existing account: a name collision must be resolved by an
// administrator with `trace sso oidc link`.
func (s *store) provisionOIDCAccount(issuer string, claims oidcUserinfo) (string, userRecord, error) {
	name := claims.Name
	if name == "" {
		name = strings.Split(claims.Email, "@")[0]
	}
	if !namePattern.MatchString(name) || name == "git" {
		return "", userRecord{}, errors.New("OIDC account has no valid Trace username.")
	}
	seed, err := newToken()
	if err != nil {
		return "", userRecord{}, errors.New("cannot provision Trace account.")
	}
	record := userRecord{Hash: hashToken(seed), OIDCIssuer: issuer, OIDCSubject: claims.Subject}
	err = s.updateUsers(func(db *userDB) error {
		if _, exists := db.Users[name]; exists {
			return fmt.Errorf("A Trace account named %s already exists and is not linked to your OIDC identity. An administrator can link it with: trace sso oidc link %s %s", name, name, claims.Subject)
		}
		for _, u := range db.Users {
			if u.OIDCSubject == claims.Subject && strings.TrimRight(u.OIDCIssuer, "/") == issuer {
				return errors.New("This OIDC identity is already linked to another Trace account.")
			}
		}
		db.Users[name] = record
		return nil
	})
	if err != nil {
		return "", userRecord{}, err
	}
	return name, record, nil
}

// linkOIDCAccount binds (or, with an empty subject, unbinds) a local account
// to an identity of the configured issuer.
func (s *store) linkOIDCAccount(name, issuer, subject string) error {
	return s.updateUsers(func(db *userDB) error {
		u, ok := db.Users[name]
		if !ok {
			return errors.New("user not found")
		}
		if subject != "" {
			for other, candidate := range db.Users {
				if other != name && candidate.OIDCSubject == subject && strings.TrimRight(candidate.OIDCIssuer, "/") == issuer {
					return fmt.Errorf("OIDC subject is already linked to %s", other)
				}
			}
		}
		u.OIDCIssuer, u.OIDCSubject = issuer, subject
		if subject == "" {
			u.OIDCIssuer = ""
		}
		db.Users[name] = u
		return nil
	})
}

func oidcLoginLink(s *store) string {
	cfg, err := s.loadOIDC()
	if err == nil && cfg.Issuer != "" {
		return "/login/oidc"
	}
	return ""
}

func oidcCommand(args []string) error {
	if len(args) > 0 && (args[0] == "link" || args[0] == "unlink") {
		return oidcLinkCommand(args)
	}
	if len(args) == 0 || (args[0] != "set" && args[0] != "disable") {
		return errors.New("usage: trace sso oidc <set|disable|link|unlink> [-data DIR] ...")
	}
	fs := flag.NewFlagSet("sso oidc "+args[0], flag.ContinueOnError)
	data := fs.String("data", "./data", "data directory")
	issuer := fs.String("issuer", "", "OIDC issuer URL")
	clientID := fs.String("client-id", "", "OIDC client ID")
	clientSecret := fs.String("client-secret", "", "OIDC client secret")
	redirectURL := fs.String("redirect-url", "", "registered callback URL")
	autoProvision := fs.Bool("auto-provision", false, "create local users on first OIDC sign-in")
	scopes := fs.String("scopes", "openid,profile,email", "comma-separated OIDC scopes")
	allowedDomains := fs.String("allowed-email-domains", "", "comma-separated email domains; when set, sign-in requires a verified email in one of them")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	s, err := openStore(*data)
	if err != nil {
		return err
	}
	path := filepath.Join(s.root, oidcConfigFile)
	if args[0] == "disable" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Println("OIDC disabled")
		return nil
	}
	if *issuer == "" || *clientID == "" || *redirectURL == "" {
		return errors.New("-issuer, -client-id, and -redirect-url are required")
	}
	if !oidcURLAllowed(*issuer) || !oidcURLAllowed(*redirectURL) {
		return errors.New("issuer and redirect-url must use HTTPS (or loopback HTTP for local development)")
	}
	var selected []string
	for _, scope := range strings.Split(*scopes, ",") {
		if scope = strings.TrimSpace(scope); scope != "" {
			selected = append(selected, scope)
		}
	}
	var domains []string
	for _, domain := range strings.Split(*allowedDomains, ",") {
		if domain = strings.ToLower(strings.TrimSpace(domain)); domain != "" {
			domains = append(domains, domain)
		}
	}
	cfg := oidcConfig{Issuer: strings.TrimRight(*issuer, "/"), ClientID: *clientID, ClientSecret: *clientSecret, RedirectURL: *redirectURL, AutoProvision: *autoProvision, Scopes: selected, AllowedEmailDomains: domains}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.root, ".oidc-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	fmt.Println("OIDC configured")
	return nil
}

// oidcLinkCommand implements `trace sso oidc link USER SUBJECT` and
// `trace sso oidc unlink USER`, the administrator's way to connect an
// existing local account to an identity of the configured issuer.
func oidcLinkCommand(args []string) error {
	usage := errors.New("usage: trace sso oidc link [-data DIR] USER SUBJECT | trace sso oidc unlink [-data DIR] USER")
	fs := flag.NewFlagSet("sso oidc "+args[0], flag.ContinueOnError)
	data := fs.String("data", "./data", "data directory")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	s, err := openStore(*data)
	if err != nil {
		return err
	}
	if args[0] == "unlink" {
		if fs.NArg() != 1 {
			return usage
		}
		if err := s.linkOIDCAccount(fs.Arg(0), "", ""); err != nil {
			return err
		}
		fmt.Println("unlinked OIDC identity from", fs.Arg(0))
		return nil
	}
	if fs.NArg() != 2 || strings.TrimSpace(fs.Arg(1)) == "" {
		return usage
	}
	cfg, err := s.loadOIDC()
	if err != nil {
		return err
	}
	if cfg.Issuer == "" {
		return errors.New("OIDC is not configured; run trace sso oidc set first")
	}
	if err := s.linkOIDCAccount(fs.Arg(0), strings.TrimRight(cfg.Issuer, "/"), fs.Arg(1)); err != nil {
		return err
	}
	fmt.Printf("linked %s to OIDC subject %s at %s\n", fs.Arg(0), fs.Arg(1), strings.TrimRight(cfg.Issuer, "/"))
	return nil
}
