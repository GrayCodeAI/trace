package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func claudeHookCLI(t *testing.T, config, input string) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "claude-hook-*.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(input); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	previous := os.Stdin
	os.Stdin = file
	defer func() { os.Stdin = previous }()
	if err := agentCaptureCommand([]string{"claude-stop", "-config", config}); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeStopCaptureUsesPromptIDAndQueuesUntilPush(t *testing.T) {
	data := filepath.Join(t.TempDir(), "node")
	if err := initData(data); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/claude", false); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a)
	defer server.Close()
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("claude\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "base")
	config := filepath.Join(t.TempDir(), "capture.json")
	if err := agentCaptureCommand([]string{"configure", "-config", config, "-url", server.URL, "-token-file", filepath.Join(data, tokenFilename), "-repo", "team/claude", "-worktree", work}); err != nil {
		t.Fatal(err)
	}
	notice := map[string]string{"hook_event_name": "Stop", "session_id": "claude-session-1", "prompt_id": "prompt-1", "cwd": work, "last_assistant_message": "password=private"}
	input, _ := json.Marshal(notice)
	claudeHookCLI(t, config, string(input))
	queued, err := os.ReadFile(config + ".outbox.json")
	if err != nil || !strings.Contains(string(queued), "stop-") || strings.Contains(string(queued), "private") {
		t.Fatalf("Claude event not safely queued: %v %s", err, queued)
	}
	repoPath, _ := a.store.repoPath("team/claude")
	gitAdminPush(t, work, repoPath, "main")
	if err := agentCaptureCommand([]string{"sync", "-config", config}); err != nil {
		t.Fatal(err)
	}
	claudeHookCLI(t, config, string(input))
	sessions, err := a.store.listAgentSessions("team/claude")
	if err != nil || len(sessions) != 1 || sessions[0].Agent != "Claude Code" || len(sessions[0].Checkpoints) != 0 {
		t.Fatalf("Claude capture not idempotent: %v %+v", err, sessions)
	}
	if strings.Contains(sessions[0].Summary, "private") {
		t.Fatal("assistant message captured without opt in")
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "commit", "-am", "second")
	gitAdminPush(t, work, repoPath, "main")
	notice["prompt_id"] = "prompt-2"
	input, _ = json.Marshal(notice)
	claudeHookCLI(t, config, string(input))
	sessions, err = a.store.listAgentSessions("team/claude")
	if err != nil || len(sessions) != 1 || len(sessions[0].Checkpoints) != 1 || sessions[0].Checkpoints[0].Commit == sessions[0].Commit {
		t.Fatalf("Claude checkpoint missing: %v %+v", err, sessions)
	}
	notice["hook_event_name"] = "SessionEnd"
	input, _ = json.Marshal(notice)
	claudeHookCLI(t, config, string(input))
	notice["hook_event_name"] = "Stop"
	delete(notice, "prompt_id")
	input, _ = json.Marshal(notice)
	cfg, err := loadAgentCaptureConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	first, matched, err := claudeStopCaptureEvent(input, cfg)
	if err != nil || !matched {
		t.Fatalf("missing prompt_id should be accepted: %v", err)
	}
	second, matched, err := claudeStopCaptureEvent(input, cfg)
	if err != nil || !matched || first.EventID == second.EventID {
		t.Fatal("fallback IDs merged separate old-version turns")
	}
	if err := appendAgentOutbox(config, first); err != nil {
		t.Fatal(err)
	}
	if err := appendAgentOutbox(config, second); err != nil {
		t.Fatal(err)
	}
	if err := agentCaptureCommand([]string{"pending", "-config", config}); err != nil {
		t.Fatal(err)
	}
	if err := agentCaptureCommand([]string{"drop", "-config", config, "-event-id", first.EventID}); err != nil {
		t.Fatal(err)
	}
	remaining, err := os.ReadFile(config + ".outbox.json")
	if err != nil || strings.Contains(string(remaining), first.EventID) || !strings.Contains(string(remaining), second.EventID) {
		t.Fatalf("drop removed wrong event: %v %s", err, remaining)
	}
}
