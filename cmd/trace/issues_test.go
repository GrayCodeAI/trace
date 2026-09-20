package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIssueLifecycleAndWeb(t *testing.T) {
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
	adminToken := readToken(t, root)
	writerToken, err := a.store.addUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.grantUser("alice", "team/demo", "write"); err != nil {
		t.Fatal(err)
	}

	item, err := a.store.createIssue("team/demo", "First issue", "Details", "alice", "", []string{"bug", "needs review"})
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != 1 || item.State != "open" || len(item.Labels) != 2 {
		t.Fatalf("unexpected issue: %+v", item)
	}
	if _, err := a.store.addIssueComment("team/demo", item.ID, "admin", "Looking into it"); err != nil {
		t.Fatal(err)
	}
	closed, err := a.store.updateIssue("team/demo", item.ID, "admin", true, "", "", "", "closed", nil)
	if err != nil || closed.State != "closed" {
		t.Fatalf("close issue: %+v %v", closed, err)
	}

	request := func(method, path, body, user, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		req.SetBasicAuth(user, token)
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		return res
	}
	res := request(http.MethodGet, "/api/v1/repos/team/demo/issues?state=all", "", "alice", writerToken)
	if res.Code != http.StatusOK {
		t.Fatalf("list issues: %d %s", res.Code, res.Body.String())
	}
	var listed []issue
	if err := json.Unmarshal(res.Body.Bytes(), &listed); err != nil || len(listed) != 1 || listed[0].State != "closed" {
		t.Fatalf("unexpected issue list: %s", res.Body.String())
	}
	res = request(http.MethodPost, "/api/v1/repos/team/demo/issues", `{"title":"API issue","body":"from api","labels":["enhancement"]}`, "alice", writerToken)
	if res.Code != http.StatusCreated {
		t.Fatalf("create issue API: %d %s", res.Code, res.Body.String())
	}
	res = request(http.MethodPost, "/api/v1/repos/team/demo/issues/2/comments", `{"body":"comment"}`, "admin", adminToken)
	if res.Code != http.StatusOK {
		t.Fatalf("comment issue API: %d %s", res.Code, res.Body.String())
	}

	form := url.Values{"title": {"Web issue"}, "body": {"from web"}, "labels": {"bug"}, "csrf": {a.csrfFor("admin")}}
	req := httptest.NewRequest(http.MethodPost, "/repos/team/demo/issues", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth("admin", adminToken)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("web issue create: %d %s", res.Code, res.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/repos/team/demo/issues", nil)
	req.SetBasicAuth("admin", adminToken)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "Web issue") {
		t.Fatalf("web issue page: %d", res.Code)
	}
	if _, err := os.Stat(filepath.Join(root, issueFile)); err != nil {
		t.Fatalf("issue store missing: %v", err)
	}
}
