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

func TestTeamAccessAndBranchReview(t *testing.T) {
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
	adminURL := strings.Replace(srv.URL, "http://", "http://admin:"+adminToken(t, root)+"@", 1) + "/git/team/demo.git"
	client := t.TempDir()
	gitTest(t, client, "init", "--initial-branch=main")
	gitTest(t, client, "config", "user.name", "Admin")
	gitTest(t, client, "config", "user.email", "admin@example.invalid")
	if err := os.WriteFile(filepath.Join(client, "README.md"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, client, "add", ".")
	gitTest(t, client, "commit", "-m", "base")
	gitTest(t, client, "remote", "add", "origin", adminURL)
	gitTest(t, client, "push", "-u", "origin", "main")

	writerToken, err := a.store.addUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	readerToken, err := a.store.addUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	noAccessToken, err := a.store.addUser("carol", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.grantUser("alice", "team/demo", "write"); err != nil {
		t.Fatal(err)
	}
	if err := a.store.grantUser("bob", "team/demo", "read"); err != nil {
		t.Fatal(err)
	}
	writerURL := strings.Replace(srv.URL, "http://", "http://alice:"+writerToken+"@", 1) + "/git/team/demo.git"
	work := filepath.Join(t.TempDir(), "work")
	gitTest(t, t.TempDir(), "clone", writerURL, work)
	gitTest(t, work, "config", "user.name", "Alice")
	gitTest(t, work, "config", "user.email", "alice@example.invalid")
	gitTest(t, work, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("base\nteam change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "commit", "-am", "team change")
	gitTest(t, work, "push", "origin", "feature")
	pushMain := exec.Command("git", "push", "origin", "feature:main")
	pushMain.Dir = work
	pushMain.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := pushMain.CombinedOutput(); err == nil {
		t.Fatalf("writer updated protected main: %s", out)
	}

	req := httptest.NewRequest(http.MethodGet, "/repos/team/demo?branch=feature", nil)
	req.SetBasicAuth("alice", writerToken)
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "&#43;team change") {
		t.Fatalf("branch review: status %d, body %s", res.Code, res.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/repos/team/demo?branch=feature&file=README.md", nil)
	req.SetBasicAuth("alice", writerToken)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "team change") {
		t.Fatalf("file preview: got %d", res.Code)
	}
	readURL := strings.Replace(srv.URL, "http://", "http://bob:"+readerToken+"@", 1) + "/git/team/demo.git"
	gitTest(t, t.TempDir(), "clone", readURL, filepath.Join(t.TempDir(), "read-clone"))
	req = httptest.NewRequest(http.MethodGet, "/git/team/demo.git/info/refs?service=git-receive-pack", nil)
	req.SetBasicAuth("bob", readerToken)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("reader push advertisement: got %d", res.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/repos/team/demo", nil)
	req.SetBasicAuth("carol", noAccessToken)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("ungranted user saw repository: got %d", res.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/git/team/demo.git/info/refs?service=git-upload-pack", nil)
	req.SetBasicAuth("carol", noAccessToken)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("ungranted user fetched Git data: got %d", res.Code)
	}
	if err := a.store.grantUser("bob", "team/demo", "none"); err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("bob", readerToken)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("revoked reader still fetched Git data: got %d", res.Code)
	}
	form := url.Values{"name": {"intruder"}, "csrf": {a.csrfFor("alice")}}
	req = httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", adminToken(t, root))
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("another user's form token was accepted: got %d", res.Code)
	}
	repoPath, err := a.store.repoPath("team/demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repoPath, "hooks", "pre-receive")); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/git/team/demo.git/info/refs?service=git-receive-pack", nil)
	req.SetBasicAuth("admin", adminToken(t, root))
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("push allowed without protection hook: got %d", res.Code)
	}

	newToken, err := a.store.rotateUser("alice")
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/app", nil)
	req.SetBasicAuth("alice", writerToken)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("old token still accepted: got %d", res.Code)
	}
	req.SetBasicAuth("alice", newToken)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("rotated token rejected: got %d", res.Code)
	}
}

func adminToken(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, tokenFilename))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestTeamManagementUI(t *testing.T) {
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
	admin := adminToken(t, root)
	req := httptest.NewRequest(http.MethodGet, "/app", nil)
	req.SetBasicAuth("admin", admin)
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "Repository access") {
		t.Fatalf("admin page: got %d, %s", res.Code, res.Body.String())
	}
	form := url.Values{"name": {"dave"}, "csrf": {a.csrfFor("admin")}}
	req = httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", admin)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "New token for") {
		t.Fatalf("add user page: got %d, %s", res.Code, res.Body.String())
	}
	form = url.Values{"user": {"dave"}, "repo": {"team/demo"}, "role": {"write"}, "csrf": {a.csrfFor("admin")}}
	req = httptest.NewRequest(http.MethodPost, "/access", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", admin)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("grant access: got %d, %s", res.Code, res.Body.String())
	}
	db, err := a.store.loadUsers()
	if err != nil || db.Users["dave"].Repos["team/demo"] != "write" {
		t.Fatalf("saved access: %v, %v", db.Users["dave"].Repos, err)
	}
}

func TestLandingAndWebLogin(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/private", false); err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	a.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "Where agents and") || strings.Contains(res.Body.String(), "team/private") {
		t.Fatalf("public landing: got %d", res.Code)
	}
	for path, contentType := range map[string]string{"/assets/inter.woff2": "font/woff2", "/assets/space-grotesk.ttf": "font/ttf", "/assets/trace-mark.svg": "image/svg+xml", "/assets/trace-network-hero.webp": "image/webp"} {
		res = httptest.NewRecorder()
		a.ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		if res.Code != http.StatusOK || res.Header().Get("Content-Type") != contentType || res.Body.Len() == 0 {
			t.Fatalf("public asset %s: got %d", path, res.Code)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/app", nil)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/login" || res.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("private dashboard displayed browser login prompt: got %d", res.Code)
	}
	res = httptest.NewRecorder()
	a.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/login", nil))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "Personal token") {
		t.Fatalf("login page: got %d", res.Code)
	}
	var nonce *http.Cookie
	for _, cookie := range res.Result().Cookies() {
		if cookie.Name == "trace_login" {
			nonce = cookie
		}
	}
	if nonce == nil {
		t.Fatal("login nonce cookie missing")
	}
	form := url.Values{"username": {"admin"}, "token": {adminToken(t, root)}, "csrf": {nonce.Value}}
	req = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(nonce)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/app" {
		t.Fatalf("login failed: got %d, %s", res.Code, res.Body.String())
	}
	var session *http.Cookie
	for _, cookie := range res.Result().Cookies() {
		if cookie.Name == "trace_session" {
			session = cookie
		}
	}
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Fatal("session cookie missing or insecure")
	}
	req = httptest.NewRequest(http.MethodGet, "/app", nil)
	req.AddCookie(session)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "team/private") {
		t.Fatalf("session dashboard: got %d", res.Code)
	}
	if _, err := a.store.rotateUser("admin"); err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("rotated token did not revoke session: got %d", res.Code)
	}
}

func TestUserCLI(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"repo", "create", "-data", root, "team/demo"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"user", "add", "-data", root, "erin"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"user", "grant", "-data", root, "erin", "team/demo", "read"}); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := s.loadUsers()
	if err != nil || db.Users["erin"].Repos["team/demo"] != "read" {
		t.Fatalf("CLI grant: %v, %v", db.Users["erin"].Repos, err)
	}
}

func TestExistingMirrorFlag(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.createRepo("alice/demo", true); err != nil {
		t.Fatal(err)
	}
	path, err := s.repoPath("alice/demo")
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, path, "config", "--unset", "trace.mirror")
	gitTest(t, path, "config", "refweave.mirror", "true")
	if !isMirror(path) {
		t.Fatal("existing mirror was not recognized")
	}
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
	if err := os.WriteFile(filepath.Join(client, "README.md"), []byte("hello trace\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, client, "add", "README.md")
	gitTest(t, client, "commit", "-m", "first")
	cloneURL := strings.Replace(srv.URL, "http://", "http://admin:"+adminToken(t, root)+"@", 1) + "/git/alice/demo.git"
	gitTest(t, client, "remote", "add", "origin", cloneURL)
	gitTest(t, client, "push", "-u", "origin", "main")
	clone := filepath.Join(t.TempDir(), "clone")
	gitTest(t, t.TempDir(), "clone", cloneURL, clone)
	b, err := os.ReadFile(filepath.Join(clone, "README.md"))
	if err != nil || string(b) != "hello trace\n" {
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
	mirrorToken, err := a.store.addUser("mirror", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.grantUser("mirror", "alice/demo", "read"); err != nil {
		t.Fatal(err)
	}
	credentialFile := filepath.Join(mirrorRoot, "source-token")
	if err := os.WriteFile(credentialFile, []byte(mirrorToken), 0600); err != nil {
		t.Fatal(err)
	}
	if err := mirrorStore.syncMirrorWithManifest("alice/demo", srv.URL+"/git/alice/demo.git", "mirror", credentialFile, true); err != nil {
		t.Fatal(err)
	}
	pins, err := mirrorStore.loadFederationTrust()
	if err != nil || len(pins) != 1 || pins[0].Repo != "alice/demo" {
		t.Fatalf("source identity was not pinned: %+v %v", pins, err)
	}
	identityPath := filepath.Join(root, nodeIdentityPath)
	originalIdentity, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	replacementRoot := t.TempDir()
	if _, err := loadOrCreateNodeSigner(replacementRoot); err != nil {
		t.Fatal(err)
	}
	replacementIdentity, err := os.ReadFile(filepath.Join(replacementRoot, nodeIdentityPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityPath, replacementIdentity, 0600); err != nil {
		t.Fatal(err)
	}
	if err := mirrorStore.syncMirrorWithManifest("alice/demo", srv.URL+"/git/alice/demo.git", "mirror", credentialFile, true); err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("changed signed source identity was accepted: %v", err)
	}
	if err := os.WriteFile(identityPath, originalIdentity, 0600); err != nil {
		t.Fatal(err)
	}
	mirrorApp, err := newApp(mirrorRoot)
	if err != nil {
		t.Fatal(err)
	}
	mirrorServer := httptest.NewServer(mirrorApp)
	defer mirrorServer.Close()
	mirrorURL := strings.Replace(mirrorServer.URL, "http://", "http://admin:"+adminToken(t, mirrorRoot)+"@", 1) + "/git/alice/demo.git"
	mirrorClone := filepath.Join(t.TempDir(), "mirror-clone")
	gitTest(t, t.TempDir(), "clone", mirrorURL, mirrorClone)
	b, err = os.ReadFile(filepath.Join(mirrorClone, "README.md"))
	if err != nil || string(b) != "hello trace\n" {
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
	// An administrator-created local mirror commit must not be silently
	// overwritten by a later signed source update.
	mirrorPath, _ := mirrorStore.repoPath("alice/demo")
	gitAdminPush(t, mirrorClone, mirrorPath, "main")
	if err := os.WriteFile(filepath.Join(client, "README.md"), []byte("source changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, client, "commit", "-am", "source change")
	gitAdminPush(t, client, filepath.Join(root, "repos", "alice", "demo.git"), "main")
	if err := mirrorStore.syncMirrorWithManifest("alice/demo", srv.URL+"/git/alice/demo.git", "mirror", credentialFile, true); err == nil || !strings.Contains(err.Error(), "federation conflict") {
		t.Fatalf("expected federation conflict, got %v", err)
	}
	conflicts, err := mirrorStore.loadFederationConflicts()
	if err != nil || len(conflicts) == 0 {
		t.Fatalf("conflict record missing: %v %+v", err, conflicts)
	}
	conflict := conflicts[len(conflicts)-1]
	if conflict.Ref != "refs/heads/main" || conflict.RemoteSHA == "" {
		t.Fatalf("unexpected branch conflict: %+v", conflict)
	}
	if err := mirrorStore.approveFederationConflict(conflict.Repo, conflict.Source, conflict.Ref, conflict.LocalSHA, conflict.RemoteSHA, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := mirrorStore.syncMirrorWithManifest("alice/demo", srv.URL+"/git/alice/demo.git", "mirror", credentialFile, true); err != nil {
		t.Fatalf("approved signed source did not sync: %v", err)
	}
	mirroredRefs, err := mirrorStore.repositoryRefs("alice/demo")
	if err != nil || mirroredRefs["refs/heads/main"] != conflict.RemoteSHA {
		t.Fatalf("approved branch was not published: %+v %v", mirroredRefs, err)
	}
	conflicts, err = mirrorStore.loadFederationConflicts()
	if err != nil || conflicts[len(conflicts)-1].AppliedAt.IsZero() {
		t.Fatalf("applied conflict was not recorded: %+v %v", conflicts, err)
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
	for _, path := range []string{"/app", "/git/alice/demo.git/info/refs"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		want := http.StatusSeeOther
		if strings.HasPrefix(path, "/git/") {
			want = http.StatusUnauthorized
		}
		if res.Code != want {
			t.Fatalf("%s: got %d, want %d", path, res.Code, want)
		}
	}
	if validRepoName("../demo") || validRepoName("alice/../demo") || validRepoName("alice/.hidden") {
		t.Fatal("unsafe repository name accepted")
	}
	req := httptest.NewRequest(http.MethodGet, "/git/alice/demo.git/config", nil)
	req.SetBasicAuth("admin", adminToken(t, root))
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("unsafe Git path: got %d", res.Code)
	}
	for _, csrf := range []string{"", a.csrfFor("admin")} {
		form := url.Values{"name": {"alice/new"}, "csrf": {csrf}}
		req := httptest.NewRequest(http.MethodPost, "/repos", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetBasicAuth("admin", adminToken(t, root))
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		want := http.StatusForbidden
		if csrf == a.csrfFor("admin") {
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
	err = s.syncMirror("alice/demo", "https://example.com/demo.git", "admin", filepath.Join(root, tokenFilename))
	if err == nil || !strings.Contains(err.Error(), "writable repository") {
		t.Fatalf("expected mirror safety error, got %v", err)
	}
}
