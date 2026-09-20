package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicRawFileAPIAndWebRoute(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/raw", false); err != nil {
		t.Fatal(err)
	}
	if err := a.store.setPublic("team/raw", true); err != nil {
		t.Fatal(err)
	}
	repoPath, _ := a.store.repoPath("team/raw")
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("raw content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "raw")
	gitAdminPush(t, work, repoPath, "main")
	server := httptest.NewServer(a)
	defer server.Close()
	web := httptest.NewRequest(http.MethodGet, "/raw/team/raw/README.md?ref=main", nil)
	webW := httptest.NewRecorder()
	a.ServeHTTP(webW, web)
	if webW.Code != http.StatusOK || webW.Body.String() != "raw content\n" {
		t.Fatalf("raw web: %d %q", webW.Code, webW.Body.String())
	}
	api := httptest.NewRequest(http.MethodGet, "/api/v1/repos/team/raw/raw?ref=main&path=README.md", nil)
	api.SetBasicAuth("admin", adminToken(t, root))
	apiW := httptest.NewRecorder()
	a.ServeHTTP(apiW, api)
	if apiW.Code != http.StatusOK || apiW.Body.String() != "raw content\n" {
		t.Fatalf("raw API: %d %q", apiW.Code, apiW.Body.String())
	}
	unsafe := httptest.NewRequest(http.MethodGet, "/raw/team/raw/../admin-token?ref=main", nil)
	unsafeW := httptest.NewRecorder()
	a.ServeHTTP(unsafeW, unsafe)
	if unsafeW.Code == http.StatusOK || strings.Contains(unsafeW.Body.String(), "raw content") {
		t.Fatal("raw traversal request unexpectedly succeeded")
	}
}
