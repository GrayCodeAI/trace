package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func newOIDCTestApp(t *testing.T) (*app, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	return a, root
}

// TestOIDCCannotTakeOverLocalAccounts reproduces the account-mapping flaw: an
// IdP user whose preferred_username (or email local part) is "admin" must not
// receive the local admin session, with or without auto-provisioning.
func TestOIDCCannotTakeOverLocalAccounts(t *testing.T) {
	for _, autoProvision := range []bool{false, true} {
		a, root := newOIDCTestApp(t)
		p := newFakeOIDCProvider(t)
		configureOIDC(t, root, p, autoProvision)
		for _, userinfo := range []map[string]any{
			{"sub": "attacker-1", "preferred_username": "admin"},
			{"sub": "attacker-2", "email": "admin@evil.example"},
		} {
			p.set(userinfo, nil)
			res := oidcSignIn(t, a, p)
			if res.Code == http.StatusSeeOther || sessionCookieIssued(res) {
				t.Fatalf("auto-provision=%v: IdP identity %v signed in to the local admin account", autoProvision, userinfo)
			}
			if !strings.Contains(res.Body.String(), "trace sso oidc link") {
				t.Fatalf("refusal should explain how to link the account: %s", res.Body.String())
			}
		}
		db, _ := a.store.loadUsers()
		if db.Users["admin"].OIDCSubject != "" {
			t.Fatal("the admin account was bound to an attacker identity")
		}
	}
}

func TestOIDCAutoProvisionBindsSubject(t *testing.T) {
	a, root := newOIDCTestApp(t)
	p := newFakeOIDCProvider(t)
	configureOIDC(t, root, p, true)
	p.set(map[string]any{"sub": "subject-1", "preferred_username": "oidc-user"}, nil)
	if res := oidcSignIn(t, a, p); res.Code != http.StatusSeeOther || !sessionCookieIssued(res) {
		t.Fatalf("first sign-in: %d %s", res.Code, res.Body.String())
	}
	db, _ := a.store.loadUsers()
	u := db.Users["oidc-user"]
	if u.OIDCSubject != "subject-1" || u.OIDCIssuer != strings.TrimRight(p.server.URL, "/") {
		t.Fatalf("provisioned account is not bound to its identity: %+v", u)
	}
	// Renaming at the IdP keeps the same account; another subject with the
	// same username does not get it.
	p.set(map[string]any{"sub": "subject-1", "preferred_username": "renamed"}, nil)
	if res := oidcSignIn(t, a, p); res.Code != http.StatusSeeOther {
		t.Fatalf("returning identity: %d %s", res.Code, res.Body.String())
	}
	p.set(map[string]any{"sub": "subject-2", "preferred_username": "oidc-user"}, nil)
	if res := oidcSignIn(t, a, p); res.Code == http.StatusSeeOther || sessionCookieIssued(res) {
		t.Fatal("a different subject reused an existing account by username")
	}
}

func TestOIDCLinkedAccountsAndPolicies(t *testing.T) {
	a, root := newOIDCTestApp(t)
	p := newFakeOIDCProvider(t)
	configureOIDC(t, root, p, false)
	if err := run([]string{"sso", "oidc", "link", "-data", root, "admin", "idp-admin-subject"}); err != nil {
		t.Fatal(err)
	}
	p.set(map[string]any{"sub": "idp-admin-subject", "preferred_username": "someone-else"}, nil)
	if res := oidcSignIn(t, a, p); res.Code != http.StatusSeeOther || !sessionCookieIssued(res) {
		t.Fatalf("linked identity could not sign in: %d %s", res.Code, res.Body.String())
	}
	if err := run([]string{"sso", "oidc", "link", "-data", root, "admin", "idp-admin-subject"}); err != nil {
		t.Fatalf("relinking the same account should be idempotent: %v", err)
	}
	if _, err := a.store.addUser("bob", false); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"sso", "oidc", "link", "-data", root, "bob", "idp-admin-subject"}); err == nil {
		t.Fatal("one IdP subject was linked to two accounts")
	}

	// An ID token whose subject differs from userinfo is rejected.
	p.set(map[string]any{"sub": "idp-admin-subject"}, p.validIDToken("someone-else"))
	if res := oidcSignIn(t, a, p); res.Code == http.StatusSeeOther || sessionCookieIssued(res) {
		t.Fatal("userinfo subject that differs from the ID token subject was accepted")
	}
	p.set(map[string]any{"sub": "idp-admin-subject"}, p.validIDToken("idp-admin-subject"))
	if res := oidcSignIn(t, a, p); res.Code != http.StatusSeeOther {
		t.Fatalf("matching ID token and userinfo subjects were refused: %d %s", res.Code, res.Body.String())
	}

	// Accounts that require Trace TOTP cannot bypass it through OIDC.
	if err := run([]string{"user", "2fa", "enable", "-data", root, "admin"}); err != nil {
		t.Fatal(err)
	}
	p.set(map[string]any{"sub": "idp-admin-subject"}, nil)
	if res := oidcSignIn(t, a, p); res.Code == http.StatusSeeOther || sessionCookieIssued(res) {
		t.Fatal("OIDC sign-in bypassed the account's TOTP requirement")
	}
	if err := run([]string{"sso", "oidc", "unlink", "-data", root, "admin"}); err != nil {
		t.Fatal(err)
	}
	db, _ := a.store.loadUsers()
	if db.Users["admin"].OIDCSubject != "" || db.Users["admin"].OIDCIssuer != "" {
		t.Fatal("unlink kept the OIDC binding")
	}
}

func TestOIDCAllowedEmailDomains(t *testing.T) {
	a, root := newOIDCTestApp(t)
	p := newFakeOIDCProvider(t)
	configureOIDC(t, root, p, true, "example.com")
	for _, userinfo := range []map[string]any{
		{"sub": "d1", "email": "dev@example.com", "email_verified": false},
		{"sub": "d2", "email": "dev@example.com"},
		{"sub": "d3", "email": "dev@other.example", "email_verified": true},
	} {
		p.set(userinfo, nil)
		if res := oidcSignIn(t, a, p); res.Code == http.StatusSeeOther {
			t.Fatalf("identity %v passed the domain allowlist", userinfo)
		}
	}
	for i, verified := range []any{true, "true"} {
		name := []string{"dev-bool", "dev-string"}[i]
		p.set(map[string]any{"sub": "ok-" + name, "preferred_username": name, "email": "Dev@Example.com", "email_verified": verified}, nil)
		if res := oidcSignIn(t, a, p); res.Code != http.StatusSeeOther {
			t.Fatalf("verified allowed-domain identity refused (%v): %d %s", verified, res.Code, res.Body.String())
		}
	}
}
