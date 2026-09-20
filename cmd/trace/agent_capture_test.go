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

func TestCodexCaptureQueuesUntilCommitIsPushedAndDeduplicates(t *testing.T) {
	data := filepath.Join(t.TempDir(), "node")
	if err := initData(data); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/project", false); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a)
	defer server.Close()
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "base")
	config := filepath.Join(t.TempDir(), "capture.json")
	if err := agentCaptureCommand([]string{"configure", "-config", config, "-url", server.URL, "-user", "admin", "-token-file", filepath.Join(data, tokenFilename), "-repo", "team/project", "-worktree", work}); err != nil {
		t.Fatal(err)
	}
	notice := map[string]string{"type": "agent-turn-complete", "thread-id": "thread-1", "turn-id": "turn-1", "cwd": work, "last-assistant-message": "finished token=private"}
	encoded, _ := json.Marshal(notice)
	if err := agentCaptureCommand([]string{"codex-notify", "-config", config, string(encoded)}); err != nil {
		t.Fatal(err)
	}
	queued, err := os.ReadFile(config + ".outbox.json")
	if err != nil || !strings.Contains(string(queued), "turn-") || strings.Contains(string(queued), "private") {
		t.Fatalf("event not queued safely: %v %s", err, queued)
	}
	if err := agentCaptureCommand([]string{"sync", "-config", config}); err == nil {
		t.Fatal("sync succeeded before commit reached server")
	}
	repoPath, _ := a.store.repoPath("team/project")
	gitAdminPush(t, work, repoPath, "main")
	if err := agentCaptureCommand([]string{"sync", "-config", config}); err != nil {
		t.Fatal(err)
	}
	if err := agentCaptureCommand([]string{"codex-notify", "-config", config, string(encoded)}); err != nil {
		t.Fatal(err)
	}
	sessions, err := a.store.listAgentSessions("team/project")
	if err != nil || len(sessions) != 1 || sessions[0].Agent != "Codex" || sessions[0].CaptureSessionKey == "" || len(sessions[0].Checkpoints) != 0 {
		t.Fatalf("unexpected first capture: %v %+v", err, sessions)
	}
	if strings.Contains(sessions[0].Summary, "private") {
		t.Fatal("assistant message captured without opt in")
	}
	pageRequest := httptest.NewRequest(http.MethodGet, "/repos/team/project/agents", nil)
	pageRequest.SetBasicAuth("admin", adminToken(t, data))
	pageResponse := httptest.NewRecorder()
	a.ServeHTTP(pageResponse, pageRequest)
	if pageResponse.Code != http.StatusOK || !strings.Contains(pageResponse.Body.String(), "authorship unverified") {
		t.Fatalf("captured provenance missing in web view: %d", pageResponse.Code)
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "commit", "-am", "second")
	gitAdminPush(t, work, repoPath, "main")
	notice["turn-id"] = "turn-2"
	encoded, _ = json.Marshal(notice)
	if err := agentCaptureCommand([]string{"codex-notify", "-config", config, string(encoded)}); err != nil {
		t.Fatal(err)
	}
	if err := agentCaptureCommand([]string{"codex-notify", "-config", config, string(encoded)}); err != nil {
		t.Fatal(err)
	}
	sessions, err = a.store.listAgentSessions("team/project")
	if err != nil || len(sessions) != 1 || len(sessions[0].Checkpoints) != 1 || sessions[0].Checkpoints[0].CaptureEventID == "" {
		t.Fatalf("capture not idempotent: %v %+v", err, sessions)
	}
	if _, _, err := a.store.captureAgentEvent("team/project", "Codex", sessions[0].CaptureSessionKey, sessions[0].Checkpoints[0].CaptureEventID, sessions[0].Commit, "changed", "admin"); err == nil {
		t.Fatal("reused event ID accepted changed content")
	}
	if err := agentCaptureCommand([]string{"codex-notify", "-config", config, `{"type":"other"}`}); err != nil {
		t.Fatal(err)
	}
}
