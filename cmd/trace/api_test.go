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

func TestAPIAndSessionForms(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/demo", false); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a)
	defer srv.Close()
	adminToken := readToken(t, root)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/repos", nil)
	req.SetBasicAuth("admin", adminToken)
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("list repos: got %d: %s", res.Code, res.Body.String())
	}
	var repos []apiRepo
	if err := json.Unmarshal(res.Body.Bytes(), &repos); err != nil || len(repos) != 1 || repos[0].Name != "team/demo" {
		t.Fatalf("unexpected repo list: %s", res.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/repos/team/demo/branches", nil)
	req.SetBasicAuth("admin", adminToken)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.HasPrefix(res.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("branches: got %d: %s", res.Code, res.Body.String())
	}

	body := strings.NewReader(`{"name":"team/created"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/repos", body)
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("admin", adminToken)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("create repo: got %d: %s", res.Code, res.Body.String())
	}

	// A browser session must be accepted by the same CSRF-protected form path
	// used by the dashboard; it must not depend on Basic Auth being present.
	loginRecorder := httptest.NewRecorder()
	a.issueSession(loginRecorder, httptest.NewRequest(http.MethodGet, "/login", nil), "admin", userRecord{Hash: hashToken(adminToken), Admin: true})
	cookie := loginRecorder.Result().Cookies()[0]
	form := url.Values{"name": {"team/session-created"}, "csrf": {a.csrfFor("admin")}}
	req = httptest.NewRequest(http.MethodPost, "/app", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("session form: got %d: %s", res.Code, res.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "repos", "team", "session-created.git")); err != nil {
		t.Fatalf("session form did not create repository: %v", err)
	}
}

func TestAPIUserLifecycle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	token := readToken(t, root)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.SetBasicAuth("admin", token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		return res
	}
	res := request(http.MethodPost, "/api/v1/users", `{"name":"alice","admin":false}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("create user: got %d: %s", res.Code, res.Body.String())
	}
	var created struct{ Name, Token string }
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil || created.Name != "alice" || created.Token == "" {
		t.Fatalf("create response did not return one-time token: %s", res.Body.String())
	}
	res = request(http.MethodGet, "/api/v1/users", "")
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), created.Token) {
		t.Fatalf("user listing leaked token or failed: %d %s", res.Code, res.Body.String())
	}
	res = request(http.MethodPatch, "/api/v1/users/alice", `{"disabled":true}`)
	if res.Code != http.StatusOK {
		t.Fatalf("disable user: got %d: %s", res.Code, res.Body.String())
	}
	res = request(http.MethodPost, "/api/v1/users/alice/rotate", "")
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), created.Token) {
		t.Fatalf("rotate user: got %d: %s", res.Code, res.Body.String())
	}
	res = request(http.MethodDelete, "/api/v1/users/alice", "")
	if res.Code != http.StatusNoContent {
		t.Fatalf("remove user: got %d: %s", res.Code, res.Body.String())
	}
}

func readToken(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, tokenFilename))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}
