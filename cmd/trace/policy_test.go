package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepositoryPolicyControlsMerge(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/policy", false); err != nil {
		t.Fatal(err)
	}
	repoPath, _ := a.store.repoPath("team/policy")
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("base\nfeature\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "base")
	gitAdminPush(t, work, repoPath, "main")
	gitTest(t, work, "switch", "-c", "feature")
	gitTest(t, work, "commit", "--allow-empty", "-m", "feature")
	gitAdminPush(t, work, repoPath, "feature")
	pr, err := a.store.createPullRequest("team/policy", "Policy", "", "alice", "feature", "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.setRepoPolicy("team/policy", repoPolicy{RequiredApprovals: 2, RequiredChecks: []string{"test"}}); err != nil {
		t.Fatal(err)
	}
	configured, err := a.store.setRepoPolicy("team/policy", repoPolicy{RequiredApprovals: 2, RequiredChecks: []string{"test"}, ProtectedBranches: []string{"main", "release/*"}})
	if err != nil || len(configured.ProtectedBranches) != 2 {
		t.Fatalf("protected branch policy: %+v %v", configured, err)
	}
	gitTest(t, work, "switch", "-c", "release/v1")
	gitTest(t, work, "commit", "--allow-empty", "-m", "release")
	push := exec.Command("git", "-C", work, "push", repoPath, "release/v1")
	if out, err := push.CombinedOutput(); err == nil || !strings.Contains(string(out), "only an administrator") {
		t.Fatalf("protected release branch accepted a direct push: %v %s", err, out)
	}
	if _, err := a.store.approvePullRequest("team/policy", pr.ID, "reviewer"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.mergePullRequest("team/policy", pr.ID, "admin"); err == nil || !strings.Contains(err.Error(), "2 approval") {
		t.Fatalf("approval policy was not enforced: %v", err)
	}
	if _, err := a.store.approvePullRequest("team/policy", pr.ID, "reviewer2"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.mergePullRequest("team/policy", pr.ID, "admin"); err == nil || !strings.Contains(err.Error(), "required checks") {
		t.Fatalf("check policy was not enforced: %v", err)
	}
	if err := a.store.saveActions(actionDB{NextRunID: 2, Runs: []actionRun{{ID: 1, Repo: "team/policy", Commit: pr.HeadSHA, Status: "success", CreatedAt: time.Now().UTC(), Jobs: []actionJob{{ID: 1, Name: "test", Status: "success"}}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.mergePullRequest("team/policy", pr.ID, "admin"); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(a)
	defer server.Close()
	token := adminToken(t, root)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/repos/team/policy/policy", nil)
	req.SetBasicAuth("admin", token)
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("policy API: %d", res.Code)
	}
	var policy repoPolicy
	if err := json.Unmarshal(res.Body.Bytes(), &policy); err != nil || policy.RequiredApprovals != 2 || len(policy.RequiredChecks) != 1 {
		t.Fatalf("policy response: %s", res.Body.String())
	}
	pageReq := httptest.NewRequest(http.MethodGet, "/repos/team/policy/settings/policy", nil)
	pageReq.SetBasicAuth("admin", token)
	pageRes := httptest.NewRecorder()
	a.ServeHTTP(pageRes, pageReq)
	if pageRes.Code != http.StatusOK || !strings.Contains(pageRes.Body.String(), "Required successful action jobs") {
		t.Fatalf("policy web page: %d", pageRes.Code)
	}
	form := strings.NewReader("csrf=" + a.csrfFor("admin") + "&required_approvals=3&required_checks=test%2Clint")
	postReq := httptest.NewRequest(http.MethodPost, "/repos/team/policy/settings/policy", form)
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.SetBasicAuth("admin", token)
	postRes := httptest.NewRecorder()
	a.ServeHTTP(postRes, postReq)
	if postRes.Code != http.StatusOK || !strings.Contains(postRes.Body.String(), "value=\"3\"") {
		t.Fatalf("policy web update: %d %s", postRes.Code, postRes.Body.String())
	}
}
