package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobalSearchPermissionsAndPagination(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	memberToken, err := a.store.addUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createTeam("builders"); err != nil {
		t.Fatal(err)
	}
	if err := a.store.updateTeamMember("builders", "alice", true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"team/visible", "team/secret"} {
		if err := a.store.createRepo(name, false); err != nil {
			t.Fatal(err)
		}
		work := t.TempDir()
		gitTest(t, work, "init", "--initial-branch=main")
		gitTest(t, work, "config", "user.name", "Admin")
		gitTest(t, work, "config", "user.email", "admin@example.invalid")
		if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("workspace-needle\n"), 0600); err != nil {
			t.Fatal(err)
		}
		gitTest(t, work, "add", ".")
		gitTest(t, work, "commit", "-m", "workspace-needle history")
		repoPath, _ := a.store.repoPath(name)
		gitAdminPush(t, work, repoPath, "main")
		if _, err := a.store.createAgentSession(name, "builder", "main", "workspace-needle token=hidden-value", "admin"); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.store.grantTeam("builders", "team/visible", "read"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 11; i++ {
		name := fmt.Sprintf("team/extra%02d", i)
		if err := a.store.createRepo(name, false); err != nil {
			t.Fatal(err)
		}
		if err := a.store.grantUser("alice", name, "read"); err != nil {
			t.Fatal(err)
		}
	}
	request := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.SetBasicAuth("alice", memberToken)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	first := request("/api/v1/search?q=workspace-needle")
	if first.Code != http.StatusOK {
		t.Fatalf("first page: %d %s", first.Code, first.Body.String())
	}
	var page globalSearchResponse
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.ReposScanned != maxGlobalSearchRepos || page.NextAfter != "team/extra09" || len(page.Results) != 0 || len(page.Commits) != 0 || len(page.Sessions) != 0 {
		t.Fatalf("unexpected first page: %+v", page)
	}
	second := request("/api/v1/search?q=workspace-needle&after=" + page.NextAfter)
	if second.Code != http.StatusOK {
		t.Fatalf("second page: %d %s", second.Code, second.Body.String())
	}
	page = globalSearchResponse{}
	if err := json.Unmarshal(second.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.ReposScanned != 2 || page.NextAfter != "" || len(page.Results) != 1 || page.Results[0].Repo != "team/visible" || len(page.Commits) != 1 || page.Commits[0].Repo != "team/visible" || len(page.Sessions) != 1 || page.Sessions[0].Repo != "team/visible" || strings.Contains(second.Body.String(), "team/secret") || strings.Contains(second.Body.String(), "hidden-value") {
		t.Fatalf("accessible results or redaction: %+v", page)
	}
	web := request("/search?q=workspace-needle&after=team/extra09")
	if web.Code != http.StatusOK || !strings.Contains(web.Body.String(), "team/visible") || strings.Contains(web.Body.String(), "team/secret") {
		t.Fatalf("web search: %d %s", web.Code, web.Body.String())
	}
	badCursor := request("/api/v1/search?q=workspace-needle&after=../secret")
	if badCursor.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor accepted: %d", badCursor.Code)
	}
	unauthorized := httptest.NewRecorder()
	a.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/search?q=workspace-needle", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous search: %d", unauthorized.Code)
	}
}
