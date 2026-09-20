package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitSearchAndDetailAreBranchScoped(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/history", false); err != nil {
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
	repoPath, _ := a.store.repoPath("team/history")
	gitAdminPush(t, work, repoPath, "main")
	gitTest(t, work, "switch", "-c", "feature")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("base\nfeature\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "commit", "-am", "Implement safe history", "-m", "branch-only-needle token=do-not-echo")
	featureCommit := strings.TrimSpace(gitTest(t, work, "rev-parse", "HEAD"))
	gitAdminPush(t, work, repoPath, "feature")
	token := adminToken(t, root)
	request := func(path string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if authenticated {
			r.SetBasicAuth("admin", token)
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	res := request("/api/v1/repos/team/history/search?q=branch-only-needle&scope=commits&ref=feature", true)
	if res.Code != http.StatusOK {
		t.Fatalf("commit search: %d %s", res.Code, res.Body.String())
	}
	var result searchResponse
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Commits) != 1 || result.Commits[0].Commit != featureCommit || len(result.Results) != 0 || len(result.Sessions) != 0 || strings.Contains(res.Body.String(), "do-not-echo") {
		t.Fatalf("commit result or body privacy: %+v", result)
	}
	main := request("/api/v1/repos/team/history/search?q=branch-only-needle&scope=commits&ref=main", true)
	if main.Code != http.StatusOK || strings.Contains(main.Body.String(), featureCommit) {
		t.Fatalf("feature commit leaked into main history: %d", main.Code)
	}
	web := request("/repos/team/history/search?q=branch-only-needle&scope=commits&ref=feature", true)
	if web.Code != http.StatusOK || !strings.Contains(web.Body.String(), "/repos/team/history/commits/"+featureCommit+"?ref=feature") || strings.Contains(web.Body.String(), "do-not-echo") {
		t.Fatalf("commit search web: %d", web.Code)
	}
	overview := request("/repos/team/history?branch=feature", true)
	if overview.Code != http.StatusOK || !strings.Contains(overview.Body.String(), "/repos/team/history/commits/"+featureCommit+"?ref=feature") {
		t.Fatalf("recent commit link: %d", overview.Code)
	}
	detail := request("/repos/team/history/commits/"+featureCommit+"?ref=feature", true)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "branch-only-needle") || !strings.Contains(detail.Body.String(), "&#43;feature") {
		t.Fatalf("commit detail: status=%d", detail.Code)
	}
	if wrongRef := request("/repos/team/history/commits/"+featureCommit+"?ref=main", true); wrongRef.Code != http.StatusNotFound {
		t.Fatalf("commit outside selected branch: %d", wrongRef.Code)
	}
	if anonymous := request("/repos/team/history/commits/"+featureCommit+"?ref=feature", false); anonymous.Code != http.StatusSeeOther {
		t.Fatalf("private commit visible anonymously: %d", anonymous.Code)
	}
	if invalid := request("/repos/team/history/commits/not-a-sha?ref=feature", true); invalid.Code != http.StatusNotFound {
		t.Fatalf("invalid SHA accepted: %d", invalid.Code)
	}
}
