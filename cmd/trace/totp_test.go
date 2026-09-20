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

func TestTOTPVerificationAndBrowserLogin(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	secret := "JBSWY3DPEHPK3PXP"
	if err := a.store.updateUsers(func(db *userDB) error {
		u := db.Users["admin"]
		u.TOTPSecret, u.TOTPEnabled = secret, true
		db.Users["admin"] = u
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, err := totpCode(secret, time.Now())
	if err != nil || !verifyTOTP(secret, code, time.Now()) || verifyTOTP(secret, "000000", time.Now()) {
		t.Fatalf("TOTP verification failed: code=%s err=%v", code, err)
	}
	page := httptest.NewRecorder()
	a.loginPage(page, httptest.NewRequest(http.MethodGet, "/login", nil), "", http.StatusOK)
	cookie := page.Result().Cookies()[0]
	form := url.Values{"csrf": {cookie.Value}, "username": {"admin"}, "token": {adminToken(t, root)}, "totp": {code}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("valid TOTP login: %d %s", res.Code, res.Body.String())
	}
}
