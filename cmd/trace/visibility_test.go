package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicRepositoryAllowsAnonymousGitReadOnly(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/public", false); err != nil {
		t.Fatal(err)
	}
	if err := a.store.setPublic("team/public", true); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/git/team/public.git/info/refs?service=git-upload-pack", nil)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("anonymous public clone advertisement: %d %s", w.Code, w.Body.String())
	}
	push := httptest.NewRequest(http.MethodGet, "/git/team/public.git/info/refs?service=git-receive-pack", nil)
	pushW := httptest.NewRecorder()
	a.ServeHTTP(pushW, push)
	if pushW.Code == http.StatusOK {
		t.Fatal("anonymous public push advertisement unexpectedly allowed")
	}
	page := httptest.NewRequest(http.MethodGet, "/repos/team/public", nil)
	pageW := httptest.NewRecorder()
	a.ServeHTTP(pageW, page)
	if pageW.Code != http.StatusOK {
		t.Fatalf("anonymous public web page: %d %s", pageW.Code, pageW.Body.String())
	}
}

// TestPublicRepositoryAnonymousGitCloneAndFetch exercises the full smart-HTTP
// exchange (GET info/refs followed by POST git-upload-pack) with credential
// helpers and prompts disabled, so only genuinely anonymous access can pass.
func TestPublicRepositoryAnonymousGitCloneAndFetch(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"team/public", "team/private"} {
		if err := a.store.createRepo(name, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.store.setPublic("team/public", true); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a)
	defer server.Close()
	token := adminToken(t, root)
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("public\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "public")
	for _, name := range []string{"team/public", "team/private"} {
		gitTest(t, work, "push", strings.Replace(server.URL, "http://", "http://admin:"+token+"@", 1)+"/git/"+name+".git", "main")
	}

	anonymousGit := func(dir string, args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-c", "credential.helper=", "-c", "core.askPass="}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, protocol := range []string{"version=2", "version=0"} {
		clone := filepath.Join(t.TempDir(), "clone")
		if out, err := anonymousGit(t.TempDir(), "-c", "protocol."+protocol, "clone", server.URL+"/git/team/public.git", clone); err != nil {
			t.Fatalf("anonymous clone (%s) of a public repository failed: %v\n%s", protocol, err, out)
		}
		if b, err := os.ReadFile(filepath.Join(clone, "README.md")); err != nil || string(b) != "public\n" {
			t.Fatalf("anonymous clone content: %q %v", b, err)
		}
		if out, err := anonymousGit(clone, "fetch", "origin"); err != nil {
			t.Fatalf("anonymous fetch (%s) of a public repository failed: %v\n%s", protocol, err, out)
		}
	}
	if out, err := anonymousGit(t.TempDir(), "clone", server.URL+"/git/team/private.git", "private"); err == nil {
		t.Fatalf("anonymous clone of a private repository unexpectedly succeeded:\n%s", out)
	}
	pushWork := filepath.Join(t.TempDir(), "push")
	if out, err := anonymousGit(t.TempDir(), "clone", server.URL+"/git/team/public.git", pushWork); err != nil {
		t.Fatalf("clone for push attempt: %v\n%s", err, out)
	}
	gitTest(t, pushWork, "config", "user.name", "Anonymous")
	gitTest(t, pushWork, "config", "user.email", "anonymous@example.invalid")
	gitTest(t, pushWork, "commit", "--allow-empty", "-m", "anonymous write")
	if out, err := anonymousGit(pushWork, "push", "origin", "HEAD:refs/heads/anonymous"); err == nil {
		t.Fatalf("anonymous push to a public repository unexpectedly succeeded:\n%s", out)
	}
	upload := httptest.NewRequest(http.MethodPost, "/git/team/public.git/git-receive-pack", strings.NewReader("0000"))
	upload.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	uploadW := httptest.NewRecorder()
	a.ServeHTTP(uploadW, upload)
	if uploadW.Code == http.StatusOK {
		t.Fatal("anonymous POST git-receive-pack unexpectedly allowed")
	}
	if err := a.store.setArchived("team/public", true); err != nil {
		t.Fatal(err)
	}
	if out, err := anonymousGit(t.TempDir(), "clone", server.URL+"/git/team/public.git", "archived"); err == nil {
		t.Fatalf("anonymous clone of an archived repository unexpectedly succeeded:\n%s", out)
	}
}
