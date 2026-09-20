package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBranchLifecycleAPI(t *testing.T) {
	root := t.TempDir()
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
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Test")
	gitTest(t, work, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("demo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", "README.md")
	gitTest(t, work, "commit", "-m", "initial")
	gitAdminPush(t, work, filepath.Join(root, "repos", "team", "demo.git"), "main")
	token := adminToken(t, root)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.SetBasicAuth("admin", token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	if w := request(http.MethodPost, "/api/v1/repos/team/demo/branches", `{"name":"feature","from":"main"}`); w.Code != http.StatusCreated {
		t.Fatalf("create branch status %d: %s", w.Code, w.Body.String())
	}
	for _, operation := range []struct{ action, branch string }{{"create", "web-feature"}, {"delete", "web-feature"}} {
		action, branch := operation.action, operation.branch
		form := url.Values{"csrf": {a.csrfFor("admin")}, "action": {action}, "branch": {branch}, "from": {"main"}}
		r := httptest.NewRequest(http.MethodPost, "/repos/team/demo/branches", strings.NewReader(form.Encode()))
		r.SetBasicAuth("admin", token)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("web %s branch status %d: %s", action, w.Code, w.Body.String())
		}
	}
	if w := request(http.MethodDelete, "/api/v1/repos/team/demo/branches/feature", ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete branch status %d: %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodPost, "/api/v1/repos/team/demo/branches", `{"name":"main","from":"main"}`); w.Code == http.StatusCreated {
		t.Fatal("main branch unexpectedly created")
	}
}
