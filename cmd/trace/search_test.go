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

func TestSearchAPIAndWeb(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/search", false); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main\n// trace-search-marker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "searchable commit")
	repoPath, err := a.store.repoPath("team/search")
	if err != nil {
		t.Fatal(err)
	}
	gitAdminPush(t, work, repoPath, "main")
	session, err := a.store.createAgentSession("team/search", "builder", "main", "Investigate trace-search-marker token=private-value", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.addAgentCheckpoint("team/search", session.ID, "main", "Resolved agent-lookup-marker", "reviewed"); err != nil {
		t.Fatal(err)
	}
	token := adminToken(t, root)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/repos/team/search/search?q=trace-search-marker&ref=main", nil)
	req.SetBasicAuth("admin", token)
	res := httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("search API: %d %s", res.Code, res.Body.String())
	}
	var got searchResponse
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != 1 || got.Results[0].Path != "main.go" || got.Results[0].Line != 2 {
		t.Fatalf("unexpected search result: %+v", got)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].SessionID != session.ID || strings.Contains(res.Body.String(), "private-value") {
		t.Fatalf("combined search or redaction: %+v", got)
	}
	sessionReq := httptest.NewRequest(http.MethodGet, "/api/v1/repos/team/search/search?q=agent-lookup-marker&scope=sessions&ref=missing", nil)
	sessionReq.SetBasicAuth("admin", token)
	sessionRes := httptest.NewRecorder()
	a.ServeHTTP(sessionRes, sessionReq)
	if sessionRes.Code != http.StatusOK {
		t.Fatalf("session-only search: %d %s", sessionRes.Code, sessionRes.Body.String())
	}
	var sessionGot searchResponse
	if err := json.Unmarshal(sessionRes.Body.Bytes(), &sessionGot); err != nil {
		t.Fatal(err)
	}
	if len(sessionGot.Results) != 0 || len(sessionGot.Sessions) != 1 || sessionGot.Sessions[0].CheckpointID != 1 {
		t.Fatalf("session-only result: %+v", sessionGot)
	}
	badScope := httptest.NewRequest(http.MethodGet, "/api/v1/repos/team/search/search?q=marker&scope=unknown", nil)
	badScope.SetBasicAuth("admin", token)
	badScopeRes := httptest.NewRecorder()
	a.ServeHTTP(badScopeRes, badScope)
	if badScopeRes.Code != http.StatusBadRequest {
		t.Fatalf("invalid scope accepted: %d", badScopeRes.Code)
	}
	indexReq := httptest.NewRequest(http.MethodPost, "/api/v1/repos/team/search/search-index?ref=main", nil)
	indexReq.SetBasicAuth("admin", token)
	indexW := httptest.NewRecorder()
	a.ServeHTTP(indexW, indexReq)
	if indexW.Code != http.StatusCreated {
		t.Fatalf("search index rebuild: %d %s", indexW.Code, indexW.Body.String())
	}
	indexed, err := a.store.search("team/search", "main", "trace-search-marker")
	if err != nil || len(indexed.Results) != 1 || indexed.Results[0].Path != "main.go" {
		t.Fatalf("indexed search: %+v %v", indexed, err)
	}
	req = httptest.NewRequest(http.MethodGet, "/repos/team/search/search?q=trace-search-marker&ref=main", nil)
	req.SetBasicAuth("admin", token)
	res = httptest.NewRecorder()
	a.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "main.go:2") || !strings.Contains(res.Body.String(), "session-1") {
		t.Fatalf("search web: %d %s", res.Code, res.Body.String())
	}
}
