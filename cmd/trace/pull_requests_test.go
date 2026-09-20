package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPullRequestLifecycle(t *testing.T) {
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
	repoPath, err := a.store.repoPath("team/demo")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "base")
	gitAdminPush(t, work, repoPath, "main")
	gitTest(t, work, "switch", "-c", "feature")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("base\nfeature\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "commit", "-am", "feature")
	gitAdminPush(t, work, repoPath, "feature")

	aliceToken, err := a.store.addUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.grantUser("alice", "team/demo", "write"); err != nil {
		t.Fatal(err)
	}
	pr, err := a.store.createPullRequestWithDraft("team/demo", "Ship feature", "Adds the feature.", "alice", "feature", "main", true)
	if err != nil {
		t.Fatal(err)
	}
	if pr.ID != 1 || pr.BaseSHA == "" || pr.HeadSHA == "" || pr.State != "open" || !pr.Draft {
		t.Fatalf("unexpected pull request: %+v", pr)
	}
	if _, err := a.store.createPullRequest("team/demo", "Duplicate", "", "alice", "feature", "main"); err == nil {
		t.Fatal("duplicate open pull request was accepted")
	}
	anchored, err := a.store.addPullCommentAt("team/demo", pr.ID, "alice", "Please keep this line", "README.md", 2, "new")
	if err != nil {
		t.Fatal(err)
	}
	if len(anchored.Comments) != 1 || anchored.Comments[0].Path != "README.md" || anchored.Comments[0].Line != 2 || anchored.Comments[0].Commit != pr.HeadSHA {
		t.Fatalf("unexpected anchored comment: %+v", anchored.Comments)
	}
	if _, err := a.store.addPullCommentAt("team/demo", pr.ID, "alice", "bad line", "README.md", 99, "new"); err == nil {
		t.Fatal("out-of-range line comment was accepted")
	}
	if _, err := a.store.approvePullRequest("team/demo", pr.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.mergePullRequest("team/demo", pr.ID, "admin"); err == nil || !strings.Contains(err.Error(), "draft") {
		t.Fatalf("draft pull request was mergeable: %v", err)
	}
	if _, err := a.store.markPullRequestReady("team/demo", pr.ID, "alice", false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.mergePullRequest("team/demo", pr.ID, "admin"); err == nil {
		t.Fatal("author approval incorrectly satisfied merge requirement")
	}
	if _, err := a.store.approvePullRequest("team/demo", pr.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	merged, err := a.store.mergePullRequest("team/demo", pr.ID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if merged.State != "merged" {
		t.Fatalf("state after merge: %s", merged.State)
	}
	got := strings.TrimSpace(gitTest(t, t.TempDir(), "--git-dir", repoPath, "rev-parse", "refs/heads/main"))
	if got != pr.HeadSHA {
		t.Fatalf("main points to %s, want %s", got, pr.HeadSHA)
	}

	// The HTTP API exposes the same lifecycle and respects repository access.
	srv := httptest.NewServer(a)
	defer srv.Close()
	request := func(method, path, body, user, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth(user, token)
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		return res
	}
	res := request(http.MethodGet, "/api/v1/repos/team/demo/pulls?state=all", "", "alice", aliceToken)
	if res.Code != http.StatusOK {
		t.Fatalf("pull list: %d %s", res.Code, res.Body.String())
	}
	var listed []pullRequest
	if err := json.Unmarshal(res.Body.Bytes(), &listed); err != nil || len(listed) != 1 || listed[0].State != "merged" {
		t.Fatalf("unexpected pull list: %s", res.Body.String())
	}
	form := url.Values{"title": {"Web follow-up"}, "source": {"feature"}, "target": {"main"}, "csrf": {a.csrfFor("admin")}}
	req := httptest.NewRequest(http.MethodPost, "/repos/team/demo/pulls", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", readToken(t, root))
	webRes := httptest.NewRecorder()
	a.ServeHTTP(webRes, req)
	if webRes.Code != http.StatusSeeOther {
		t.Fatalf("web pull create: %d %s", webRes.Code, webRes.Body.String())
	}
	metadataRes := request(http.MethodPatch, "/api/v1/repos/team/demo/pulls/2/metadata", `{"labels":["enhancement"],"assignees":["alice"],"reviewers":["alice"]}`, "admin", readToken(t, root))
	if metadataRes.Code != http.StatusOK || !strings.Contains(metadataRes.Body.String(), `"enhancement"`) || !strings.Contains(metadataRes.Body.String(), `"alice"`) || !strings.Contains(metadataRes.Body.String(), `"reviewers":["alice"]`) {
		t.Fatalf("pull metadata API: %d %s", metadataRes.Code, metadataRes.Body.String())
	}
	lineRes := request(http.MethodPost, "/api/v1/repos/team/demo/pulls/2/comments", `{"body":"Anchored review","file":"README.md","line":2,"side":"new"}`, "alice", aliceToken)
	if lineRes.Code != http.StatusOK || !strings.Contains(lineRes.Body.String(), `"path":"README.md"`) || !strings.Contains(lineRes.Body.String(), `"line":2`) {
		t.Fatalf("line comment API: %d %s", lineRes.Code, lineRes.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/repos/team/demo/pulls/2", nil)
	req.SetBasicAuth("admin", readToken(t, root))
	webRes = httptest.NewRecorder()
	a.ServeHTTP(webRes, req)
	if webRes.Code != http.StatusOK || !strings.Contains(webRes.Body.String(), "File (optional)") || !strings.Contains(webRes.Body.String(), "README.md:2") {
		t.Fatalf("pull review page: %d %s", webRes.Code, webRes.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/repos/team/demo", nil)
	req.SetBasicAuth("admin", readToken(t, root))
	webRes = httptest.NewRecorder()
	a.ServeHTTP(webRes, req)
	if webRes.Code != http.StatusOK || !strings.Contains(webRes.Body.String(), "Web follow-up") {
		t.Fatalf("web pull page: %d", webRes.Code)
	}
}

func gitAdminPush(t *testing.T, work, repoPath, branch string) {
	t.Helper()
	cmd := exec.Command("git", "push", repoPath, "HEAD:refs/heads/"+branch)
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "TRACE_ADMIN=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("admin git push %s: %v\n%s", branch, err, out)
	}
}

func TestPullRequestSquashMerge(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/squash", false); err != nil {
		t.Fatal(err)
	}
	repoPath, _ := a.store.repoPath("team/squash")
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "base")
	gitAdminPush(t, work, repoPath, "main")
	gitTest(t, work, "switch", "-c", "feature")
	if err := os.WriteFile(filepath.Join(work, "feature.txt"), []byte("feature\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "feature")
	gitAdminPush(t, work, repoPath, "feature")
	pr, err := a.store.createPullRequest("team/squash", "Squash feature", "", "alice", "feature", "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.approvePullRequest("team/squash", pr.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	merged, err := a.store.mergePullRequestWithStrategy("team/squash", pr.ID, "admin", "squash")
	if err != nil || merged.State != "merged" {
		t.Fatalf("squash merge: %+v %v", merged, err)
	}
	parents := strings.Fields(gitTest(t, t.TempDir(), "--git-dir", repoPath, "rev-list", "--parents", "-n", "1", "refs/heads/main"))
	if len(parents) != 2 {
		t.Fatalf("squash commit has %d parents, want 1: %v", len(parents)-1, parents)
	}
}
