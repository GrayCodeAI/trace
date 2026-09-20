package main

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func geminiHookCLI(t *testing.T, config, input string) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "gemini-hook-*.json")
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
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	previousIn, previousOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = file, writer
	defer func() { os.Stdin, os.Stdout = previousIn, previousOut }()
	runErr := agentCaptureCommand([]string{"gemini-after-agent", "-config", config})
	os.Stdout = previousOut
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
	return strings.TrimSpace(string(output))
}

func TestGeminiAfterAgentCaptureJSONAndReplay(t *testing.T) {
	data := filepath.Join(t.TempDir(), "node")
	if err := initData(data); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/gemini", false); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a)
	defer server.Close()
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("gemini\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "base")
	config := filepath.Join(t.TempDir(), "capture.json")
	if err := agentCaptureCommand([]string{"configure", "-config", config, "-url", server.URL, "-token-file", filepath.Join(data, tokenFilename), "-repo", "team/gemini", "-worktree", work}); err != nil {
		t.Fatal(err)
	}
	notice := map[string]string{"hook_event_name": "AfterAgent", "session_id": "gemini-session-1", "timestamp": "2026-09-20T12:00:00.123Z", "cwd": work, "prompt": "token=private", "prompt_response": "secret=hidden"}
	input, _ := json.Marshal(notice)
	if got := geminiHookCLI(t, config, string(input)); got != "{}" {
		t.Fatalf("Gemini requires JSON output, got %q", got)
	}
	queued, err := os.ReadFile(config + ".outbox.json")
	if err != nil || !strings.Contains(string(queued), "after-") || strings.Contains(string(queued), "private") || strings.Contains(string(queued), "hidden") {
		t.Fatalf("Gemini event not safely queued: %v %s", err, queued)
	}
	repoPath, _ := a.store.repoPath("team/gemini")
	gitAdminPush(t, work, repoPath, "main")
	if err := agentCaptureCommand([]string{"sync", "-config", config}); err != nil {
		t.Fatal(err)
	}
	if got := geminiHookCLI(t, config, string(input)); got != "{}" {
		t.Fatalf("Gemini retry output: %q", got)
	}
	sessions, err := a.store.listAgentSessions("team/gemini")
	if err != nil || len(sessions) != 1 || sessions[0].Agent != "Gemini CLI" || len(sessions[0].Checkpoints) != 0 {
		t.Fatalf("Gemini capture not idempotent: %v %+v", err, sessions)
	}
	if strings.Contains(sessions[0].Summary, "hidden") {
		t.Fatal("Gemini response captured without opt in")
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "commit", "-am", "second")
	gitAdminPush(t, work, repoPath, "main")
	notice["timestamp"] = "2026-09-20T12:01:00.123Z"
	input, _ = json.Marshal(notice)
	geminiHookCLI(t, config, string(input))
	sessions, err = a.store.listAgentSessions("team/gemini")
	if err != nil || len(sessions) != 1 || len(sessions[0].Checkpoints) != 1 || sessions[0].Checkpoints[0].Commit == sessions[0].Commit {
		t.Fatalf("Gemini checkpoint missing: %v %+v", err, sessions)
	}
	notice["hook_event_name"] = "BeforeAgent"
	input, _ = json.Marshal(notice)
	if got := geminiHookCLI(t, config, string(input)); got != "{}" {
		t.Fatalf("ignored Gemini event must return JSON: %q", got)
	}
}
