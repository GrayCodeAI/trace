package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectsAPICLIAndWeb(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/board", false); err != nil {
		t.Fatal(err)
	}
	token := adminToken(t, root)
	server := httptest.NewServer(a)
	defer server.Close()
	postJSON := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth("admin", token)
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		return res
	}
	res := postJSON(http.MethodPost, "/api/v1/repos/team/board/projects", `{"name":"Sprint","description":"Ship it"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", res.Code, res.Body.String())
	}
	var p project
	if err := json.Unmarshal(res.Body.Bytes(), &p); err != nil || p.ID != 1 || len(p.Columns) != 3 {
		t.Fatalf("project: %s", res.Body.String())
	}
	res = postJSON(http.MethodPost, "/api/v1/repos/team/board/projects/1/cards", `{"kind":"issue","number":7,"title":"Fix release","column":"Todo"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("add card: %d %s", res.Code, res.Body.String())
	}
	res = postJSON(http.MethodPatch, "/api/v1/repos/team/board/projects/1/cards/1", `{"column":"Done"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("move card: %d %s", res.Code, res.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/api/v1/repos/team/board/projects", nil)
	get.SetBasicAuth("admin", token)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, get)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "Fix release") {
		t.Fatalf("list projects: %d %s", res.Code, res.Body.String())
	}
	form := url.Values{"csrf": {a.csrfFor("admin")}, "action": {"create"}, "name": {"Web board"}}
	webPost := httptest.NewRequest(http.MethodPost, "/repos/team/board/projects", strings.NewReader(form.Encode()))
	webPost.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	webPost.SetBasicAuth("admin", token)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, webPost)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("web create: %d %s", res.Code, res.Body.String())
	}
	page := httptest.NewRequest(http.MethodGet, "/repos/team/board/projects", nil)
	page.SetBasicAuth("admin", token)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, page)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "Web board") || !strings.Contains(res.Body.String(), "Fix release") {
		t.Fatalf("web page: %d %s", res.Code, res.Body.String())
	}
}
