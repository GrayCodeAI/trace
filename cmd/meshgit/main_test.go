package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestGitPushCloneAndMirror(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node-a")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("alice/demo", false); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a)
	defer srv.Close()

	client := t.TempDir()
	gitTest(t, client, "init", "--initial-branch=main")
	gitTest(t, client, "config", "user.name", "Test")
	gitTest(t, client, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(client, "README.md"), []byte("hello meshgit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, client, "add", "README.md")
	gitTest(t, client, "commit", "-m", "first")
	cloneURL := strings.Replace(srv.URL, "http://", "http://git:"+a.token+"@", 1) + "/git/alice/demo.git"
	gitTest(t, client, "remote", "add", "origin", cloneURL)
	gitTest(t, client, "push", "-u", "origin", "main")
	clone := filepath.Join(t.TempDir(), "clone")
	gitTest(t, t.TempDir(), "clone", cloneURL, clone)
	b, err := os.ReadFile(filepath.Join(clone, "README.md"))
	if err != nil || string(b) != "hello meshgit\n" {
		t.Fatalf("clone content: %q, %v", b, err)
	}

	mirrorRoot := filepath.Join(t.TempDir(), "node-b")
	if err := initData(mirrorRoot); err != nil {
		t.Fatal(err)
	}
	mirrorStore, err := openStore(mirrorRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := mirrorStore.syncMirror("alice/demo", srv.URL+"/git/alice/demo.git", filepath.Join(root, tokenFilename)); err != nil {
		t.Fatal(err)
	}
	mirrorApp, err := newApp(mirrorRoot)
	if err != nil {
		t.Fatal(err)
	}
	mirrorServer := httptest.NewServer(mirrorApp)
	defer mirrorServer.Close()
	mirrorURL := strings.Replace(mirrorServer.URL, "http://", "http://git:"+mirrorApp.token+"@", 1) + "/git/alice/demo.git"
	mirrorClone := filepath.Join(t.TempDir(), "mirror-clone")
	gitTest(t, t.TempDir(), "clone", mirrorURL, mirrorClone)
	b, err = os.ReadFile(filepath.Join(mirrorClone, "README.md"))
	if err != nil || string(b) != "hello meshgit\n" {
		t.Fatalf("mirror content: %q, %v", b, err)
	}
	gitTest(t, mirrorClone, "config", "user.name", "Test")
	gitTest(t, mirrorClone, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(mirrorClone, "README.md"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, mirrorClone, "commit", "-am", "change")
	push := exec.Command("git", "push", "origin", "main")
	push.Dir = mirrorClone
	push.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := push.CombinedOutput(); err == nil {
		t.Fatalf("mirror accepted push: %s", out)
	}
}

func TestAuthenticationAndPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/git/alice/demo.git/info/refs"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("%s: got %d, want 401", path, res.Code)
		}
	}
	if validRepoName("../demo") || validRepoName("alice/../demo") || validRepoName("alice/.hidden") {
		t.Fatal("unsafe repository name accepted")
	}
	req := httptest.NewRequest(http.MethodGet, "/git/alice/demo.git/config", nil)
	req.SetBasicAuth("git", a.token)
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("unsafe Git path: got %d", res.Code)
	}
	for _, csrf := range []string{"", a.csrf} {
		form := url.Values{"name": {"alice/new"}, "csrf": {csrf}}
		req := httptest.NewRequest(http.MethodPost, "/repos", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth("git", a.token)
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		want := http.StatusForbidden
		if csrf == a.csrf {
			want = http.StatusSeeOther
		}
		if res.Code != want {
			t.Fatalf("create with csrf %q: got %d, want %d", csrf, res.Code, want)
		}
	}
}

func TestMirrorRefusesWritableRepository(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.createRepo("alice/demo", false); err != nil {
		t.Fatal(err)
	}
	err = s.syncMirror("alice/demo", "https://example.com/demo.git", filepath.Join(root, tokenFilename))
	if err == nil || !strings.Contains(err.Error(), "writable repository") {
		t.Fatalf("expected mirror safety error, got %v", err)
	}
}
