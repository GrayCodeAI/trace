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

func TestCommitStatusAPI(t *testing.T) {
	root := t.TempDir()
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/status", false); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Test")
	gitTest(t, work, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("status\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", "README.md")
	gitTest(t, work, "commit", "-m", "initial")
	repoPath, _ := a.store.repoPath("team/status")
	gitAdminPush(t, work, repoPath, "main")
	commit, _, err := gitOutput(repoPath, 100, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	commit = strings.TrimSpace(commit)
	token := adminToken(t, root)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.SetBasicAuth("admin", token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	posted := request(http.MethodPost, "/api/v1/repos/team/status/statuses/"+commit, `{"context":"external/test","state":"success","description":"ok"}`)
	if posted.Code != http.StatusCreated {
		t.Fatalf("status post %d: %s", posted.Code, posted.Body.String())
	}
	listed := request(http.MethodGet, "/api/v1/repos/team/status/statuses/"+commit, "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "external/test") {
		t.Fatalf("status list %d: %s", listed.Code, listed.Body.String())
	}
	var item commitStatus
	if err := json.Unmarshal(posted.Body.Bytes(), &item); err != nil || item.State != "success" || item.Creator != "admin" {
		t.Fatalf("status response %#v err=%v", item, err)
	}
	page := httptest.NewRequest(http.MethodGet, "/repos/team/status?branch=main", nil)
	page.SetBasicAuth("admin", token)
	pageRes := httptest.NewRecorder()
	a.ServeHTTP(pageRes, page)
	if pageRes.Code != http.StatusOK || !strings.Contains(pageRes.Body.String(), "external/test") {
		t.Fatalf("status web view %d: %s", pageRes.Code, pageRes.Body.String())
	}
}
