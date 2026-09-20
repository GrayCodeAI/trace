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

func cursorHookCLI(t *testing.T, config, input string) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "cursor-hook-*.json")
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
	runErr := agentCaptureCommand([]string{"cursor-stop", "-config", config})
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

func TestCursorStopCaptureQueuesAndDeduplicates(t *testing.T) {
	data := filepath.Join(t.TempDir(), "node")
	if err := initData(data); err != nil {
		t.Fatal(err)
	}
	a, err := newApp(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.createRepo("team/cursor", false); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a)
	defer server.Close()
	work := t.TempDir()
	gitTest(t, work, "init", "--initial-branch=main")
	gitTest(t, work, "config", "user.name", "Admin")
	gitTest(t, work, "config", "user.email", "admin@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("cursor\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "add", ".")
	gitTest(t, work, "commit", "-m", "base")
	config := filepath.Join(t.TempDir(), "capture.json")
	if err := agentCaptureCommand([]string{"configure", "-config", config, "-url", server.URL, "-token-file", filepath.Join(data, tokenFilename), "-repo", "team/cursor", "-worktree", work}); err != nil {
		t.Fatal(err)
	}
	notice := map[string]any{"hook_event_name": "stop", "conversation_id": "conversation-1", "generation_id": "generation-1", "workspace_roots": []string{work}, "status": "completed", "loop_count": 0, "transcript_path": "/private/secret-transcript", "user_email": "private@example.invalid"}
	input, _ := json.Marshal(notice)
	if got := cursorHookCLI(t, config, string(input)); got != "{}" {
		t.Fatalf("Cursor hook must return JSON, got %q", got)
	}
	queued, err := os.ReadFile(config + ".outbox.json")
	if err != nil || !strings.Contains(string(queued), "cursor-stop-") || strings.Contains(string(queued), "secret-transcript") || strings.Contains(string(queued), "private@example.invalid") {
		t.Fatalf("Cursor event not safely queued: %v", err)
	}
	repoPath, _ := a.store.repoPath("team/cursor")
	gitAdminPush(t, work, repoPath, "main")
	if err := agentCaptureCommand([]string{"sync", "-config", config}); err != nil {
		t.Fatal(err)
	}
	if got := cursorHookCLI(t, config, string(input)); got != "{}" {
		t.Fatalf("Cursor retry output: %q", got)
	}
	sessions, err := a.store.listAgentSessions("team/cursor")
	if err != nil || len(sessions) != 1 || sessions[0].Agent != "Cursor" || len(sessions[0].Checkpoints) != 0 {
		t.Fatalf("Cursor capture not idempotent: %v %+v", err, sessions)
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, work, "commit", "-am", "second")
	gitAdminPush(t, work, repoPath, "main")
	notice["generation_id"] = "generation-2"
	input, _ = json.Marshal(notice)
	cursorHookCLI(t, config, string(input))
	sessions, err = a.store.listAgentSessions("team/cursor")
	if err != nil || len(sessions) != 1 || len(sessions[0].Checkpoints) != 1 {
		t.Fatalf("Cursor checkpoint missing: %v %+v", err, sessions)
	}
	notice["status"] = "aborted"
	input, _ = json.Marshal(notice)
	if got := cursorHookCLI(t, config, string(input)); got != "{}" {
		t.Fatalf("ignored Cursor event output: %q", got)
	}
	other := t.TempDir()
	notice["status"] = "completed"
	notice["workspace_roots"] = []string{other}
	input, _ = json.Marshal(notice)
	if got := cursorHookCLI(t, config, string(input)); got != "{}" {
		t.Fatalf("unrelated workspace output: %q", got)
	}
	sessions, err = a.store.listAgentSessions("team/cursor")
	if err != nil || len(sessions) != 1 || len(sessions[0].Checkpoints) != 1 {
		t.Fatalf("unrelated workspace was captured: %v %+v", err, sessions)
	}
}
