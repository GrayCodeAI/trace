package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestAPIBrowserWritesRequireCSRF checks that a cookie-authenticated API
// write needs X-Trace-CSRF, and that attaching an arbitrary Basic header
// (which does not authenticate the request) does not waive that check.
func TestAPIBrowserWritesRequireCSRF(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := a.store.loadUsers()
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	a.issueSession(login, httptest.NewRequest(http.MethodGet, "/", nil), "admin", db.Users["admin"])
	session := login.Result().Cookies()[0]
	create := func(name string, mutate func(*http.Request)) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/repos", strings.NewReader(`{"name":"`+name+`"}`))
		req.Header.Set("Content-Type", "application/json")
		mutate(req)
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		return res.Code
	}
	if code := create("team/no-csrf", func(r *http.Request) { r.AddCookie(session) }); code != http.StatusForbidden {
		t.Fatalf("cookie write without CSRF: %d", code)
	}
	if code := create("team/bogus-basic", func(r *http.Request) {
		r.AddCookie(session)
		r.SetBasicAuth("admin", "not-the-token")
	}); code != http.StatusForbidden {
		t.Fatalf("cookie write with a non-authenticating Basic header skipped CSRF: %d", code)
	}
	if code := create("team/wrong-csrf", func(r *http.Request) {
		r.AddCookie(session)
		r.Header.Set("X-Trace-CSRF", a.csrfFor("admin")+"x")
	}); code != http.StatusForbidden {
		t.Fatalf("cookie write with a wrong CSRF value: %d", code)
	}
	if code := create("team/with-csrf", func(r *http.Request) {
		r.AddCookie(session)
		r.Header.Set("X-Trace-CSRF", a.csrfFor("admin"))
	}); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("cookie write with CSRF: %d", code)
	}
	if code := create("team/basic", func(r *http.Request) { r.SetBasicAuth("admin", adminToken(t, root)) }); code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("Basic-authenticated write: %d", code)
	}
}
