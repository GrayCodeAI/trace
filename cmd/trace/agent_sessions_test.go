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

func TestAgentSessionsAPIWebAndRedaction(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	if err := initData(root); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/agents", false); err != nil {
		t.Fatal(err)
	}
	repoPath, _ := a.store.repoPath("team/agents")
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("agent workspace\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "base")
	gitAdminPush(t, work, repoPath, "main")
	token := adminToken(t, root)
	server := httptest.NewServer(a)
	defer server.Close()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth("admin", token)
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		return res
	}
	res := request(http.MethodPost, "/api/v1/repos/team/agents/agent-sessions", `{"agent":"builder","ref":"main","summary":"token=hidden secret=gone"}`)
	if res.Code != http.StatusCreated || strings.Contains(res.Body.String(), "hidden") || !strings.Contains(res.Body.String(), "REDACTED") {
		t.Fatalf("agent session create: %d %s", res.Code, res.Body.String())
	}
	var session agentSession
	if err := json.Unmarshal(res.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	res = request(http.MethodPost, "/api/v1/repos/team/agents/agent-sessions/"+fmt.Sprint(session.ID)+"/checkpoints", `{"ref":"main","summary":"checkpoint password=hush","state":"testing"}`)
	if res.Code != http.StatusOK || strings.Contains(res.Body.String(), "hush") || !strings.Contains(res.Body.String(), "REDACTED") {
		t.Fatalf("agent checkpoint: %d %s", res.Code, res.Body.String())
	}
	pageReq := httptest.NewRequest(http.MethodGet, "/repos/team/agents/agents", nil)
	pageReq.SetBasicAuth("admin", token)
	pageRes := httptest.NewRecorder()
	a.ServeHTTP(pageRes, pageReq)
	if pageRes.Code != http.StatusOK || !strings.Contains(pageRes.Body.String(), "Agent sessions") || !strings.Contains(pageRes.Body.String(), "builder") {
		t.Fatalf("agent page: %d", pageRes.Code)
	}
}
