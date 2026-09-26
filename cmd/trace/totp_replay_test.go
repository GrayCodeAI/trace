package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTOTPApp(t *testing.T, secret string) (*app, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.updateUsers(func(db *userDB) error {
		u := db.Users["admin"]
		u.TOTPSecret, u.TOTPEnabled = secret, true
		db.Users["admin"] = u
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return a, root
}

func postLogin(t *testing.T, a *app, token, code string) *httptest.ResponseRecorder {
	t.Helper()
	page := httptest.NewRecorder()
	a.loginPage(page, httptest.NewRequest(http.MethodGet, "/login", nil), "", http.StatusOK)
	cookie := page.Result().Cookies()[0]
	form := url.Values{"csrf": {cookie.Value}, "username": {"admin"}, "token": {token}, "totp": {code}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	return res
}

func TestTOTPCodesCannotBeReplayed(t *testing.T) {
	secret := "JBSWY3DPEHPK3PXP"
	a, root := newTOTPApp(t, secret)
	token := adminToken(t, root)
	code, err := totpCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res := postLogin(t, a, token, code); res.Code != http.StatusSeeOther {
		t.Fatalf("first use of a valid code: %d %s", res.Code, res.Body.String())
	}
	if res := postLogin(t, a, token, code); res.Code == http.StatusSeeOther {
		t.Fatal("the same TOTP code was accepted twice")
	}
	previous, err := totpCode(secret, time.Now().Add(-30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if previous != code {
		if res := postLogin(t, a, token, previous); res.Code == http.StatusSeeOther {
			t.Fatal("an older code from the skew window was accepted after a newer one")
		}
	}
	db, _ := a.store.loadUsers()
	if db.Users["admin"].TOTPLastCounter == 0 {
		t.Fatal("last accepted TOTP counter was not recorded")
	}
}

func TestTOTPFailuresBackOffPerAccount(t *testing.T) {
	secret := "JBSWY3DPEHPK3PXP"
	a, root := newTOTPApp(t, secret)
	token := adminToken(t, root)
	for i := 0; i < totpMaxFailures; i++ {
		if res := postLogin(t, a, token, "000000"); res.Code != http.StatusUnauthorized {
			t.Fatalf("wrong code %d: %d", i+1, res.Code)
		}
	}
	code, err := totpCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if code == "000000" {
		t.Skip("current TOTP code happens to be 000000")
	}
	res := postLogin(t, a, token, code)
	if res.Code != http.StatusTooManyRequests || !strings.Contains(res.Body.String(), "Too many") {
		t.Fatalf("a correct code was accepted during the lockout: %d", res.Code)
	}
	a.totp.reset("admin")
	if res := postLogin(t, a, token, code); res.Code != http.StatusSeeOther {
		t.Fatalf("correct code after the lockout ended: %d %s", res.Code, res.Body.String())
	}
}
