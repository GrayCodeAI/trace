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

func TestTopicsNormalizeAndExposeEverySurface(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/topics", false); err != nil {
		t.Fatal(err)
	}
	if err := a.store.setTopics("team/topics", []string{" Git ", "agents", "git"}); err != nil {
		t.Fatal(err)
	}
	topics, err := a.store.getTopics("team/topics")
	if err != nil || strings.Join(topics, ",") != "agents,git" {
		t.Fatalf("topics=%v err=%v", topics, err)
	}
	if err := a.store.setTopics("team/topics", []string{"-bad"}); err == nil {
		t.Fatal("leading punctuation accepted")
	}
	if _, err := normalizeTopics([]string{"a"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a)
	defer server.Close()
	token := adminToken(t, root)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/repos/team/topics/topics", nil)
	req.SetBasicAuth("admin", token)
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("topics GET: %d %s", res.Code, res.Body.String())
	}
	var response struct {
		Topics []string `json:"topics"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil || strings.Join(response.Topics, ",") != "agents,git" {
		t.Fatalf("topics response: %s", res.Body.String())
	}
	form := url.Values{"csrf": {a.csrfFor("admin")}, "topics": {"go, agents, go"}}
	post := httptest.NewRequest(http.MethodPost, "/repos/team/topics/topics", strings.NewReader(form.Encode()))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	post.SetBasicAuth("admin", token)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, post)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("topics web update: %d %s", res.Code, res.Body.String())
	}
	page := httptest.NewRequest(http.MethodGet, "/repos/team/topics", nil)
	page.SetBasicAuth("admin", token)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, page)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "agents") || !strings.Contains(res.Body.String(), "go") {
		t.Fatalf("topics web page: %d", res.Code)
	}
}
