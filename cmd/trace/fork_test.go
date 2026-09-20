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

func TestForkAPIClonesRepositoryAndGrantsOwner(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/source", false); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Test")
	gitTest(t, work, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("fork me\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", "README.md")
	gitTest(t, work, "commit", "-m", "initial")
	gitAdminPush(t, work, filepath.Join(root, "repos", "team", "source.git"), "main")
	token := adminToken(t, root)
	body, _ := json.Marshal(map[string]string{"name": "admin/fork"})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/repos/team/source/fork", strings.NewReader(string(body)))
	r.SetBasicAuth("admin", token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("fork status %d: %s", w.Code, w.Body.String())
	}
	forkPath := filepath.Join(root, "repos", "admin", "fork.git")
	if _, err := os.Stat(forkPath); err != nil {
		t.Fatal(err)
	}
	refs, err := a.store.repositoryRefs("admin/fork")
	if err != nil || refs["refs/heads/main"] == "" {
		t.Fatalf("fork refs=%v err=%v", refs, err)
	}
	users, err := a.store.loadUsers()
	if err != nil || users.Users["admin"].Repos["admin/fork"] != "write" {
		t.Fatalf("fork owner access not granted: %#v err=%v", users.Users["admin"], err)
	}
}

func TestForkWebAction(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/source", false); err != nil {
		t.Fatal(err)
	}
	token := adminToken(t, root)
	form := url.Values{"csrf": {a.csrfFor("admin")}, "target": {"admin/web-fork"}}
	r := httptest.NewRequest(http.MethodPost, "/repos/team/source/fork", strings.NewReader(form.Encode()))
	r.SetBasicAuth("admin", token)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/repos/admin/web-fork" {
		t.Fatalf("web fork status %d location=%q body=%s", w.Code, w.Header().Get("Location"), w.Body.String())
	}
}
