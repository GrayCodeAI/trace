package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicPagesServesCommittedAssets(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/site", false); err != nil {
		t.Fatal(err)
	}
	if err := a.store.setPublic("team/site", true); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a)
	defer server.Close()
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "index.html"), []byte("<h1>Trace Pages</h1>\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "app.css"), []byte("body{color:green}"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "publish site")
	remote := strings.Replace(server.URL, "http://", "http://admin:"+adminToken(t, root)+"@", 1) + "/git/team/site.git"
	gitTest(t, work, "remote", "add", "origin", remote)
	gitTest(t, work, "push", "origin", "main")
	if err := a.store.setPages("team/site", "main", "", true); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/pages/team/site/", nil)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Trace Pages") {
		t.Fatalf("pages index: %d %s", w.Code, w.Body.String())
	}
	asset := httptest.NewRequest(http.MethodGet, "/pages/team/site/app.css", nil)
	assetW := httptest.NewRecorder()
	a.ServeHTTP(assetW, asset)
	if assetW.Code != http.StatusOK || assetW.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("pages asset: %d type=%q body=%s", assetW.Code, assetW.Header().Get("Content-Type"), assetW.Body.String())
	}
	unsafe := httptest.NewRequest(http.MethodGet, "/pages/team/site/../users.json", nil)
	unsafeW := httptest.NewRecorder()
	a.ServeHTTP(unsafeW, unsafe)
	if unsafeW.Code == http.StatusOK {
		t.Fatal("pages path traversal was served")
	}
}
