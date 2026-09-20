package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestRepositoryTransferPreservesGitAndAccess(t *testing.T) {
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
	if _, err := a.store.addUser("alice", false); err != nil {
		t.Fatal(err)
	}
	if err := a.store.grantUser("alice", "team/source", "read"); err != nil {
		t.Fatal(err)
	}
	token := adminToken(t, root)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/repos/team/source/transfer", strings.NewReader(`{"name":"team/renamed"}`))
	req.SetBasicAuth("admin", token)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("transfer status %d: %s", w.Code, w.Body.String())
	}
	oldPath, _ := a.store.repoPath("team/source")
	newPath, _ := a.store.repoPath("team/renamed")
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old repository still exists: %v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new repository missing: %v", err)
	}
	db, err := a.store.loadUsers()
	if err != nil {
		t.Fatal(err)
	}
	if db.Users["alice"].Repos["team/renamed"] != "read" || db.Users["alice"].Repos["team/source"] != "" {
		t.Fatalf("access grants were not transferred: %#v", db.Users["alice"].Repos)
	}
}

func TestRepositoryDeleteAPI(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/delete-me", false); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/repos/team/delete-me", nil)
	req.SetBasicAuth("admin", adminToken(t, root))
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status %d: %s", w.Code, w.Body.String())
	}
	path, _ := a.store.repoPath("team/delete-me")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("repository still exists after delete: %v", err)
	}
}
