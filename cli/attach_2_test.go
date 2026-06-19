package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	_ "github.com/GrayCodeAI/trace/cli/agent/claudecode"     // register agent
	_ "github.com/GrayCodeAI/trace/cli/agent/codex"          // register agent
	_ "github.com/GrayCodeAI/trace/cli/agent/cursor"         // register agent
	_ "github.com/GrayCodeAI/trace/cli/agent/factoryaidroid" // register agent
	_ "github.com/GrayCodeAI/trace/cli/agent/geminicli"      // register agent
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/session"
	"github.com/GrayCodeAI/trace/cli/testutil"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

func TestAttach_CursorSuccess(t *testing.T) {
	setupAttachTestRepo(t)

	cursorDir := t.TempDir()
	t.Setenv("TRACE_TEST_CURSOR_PROJECT_DIR", cursorDir)

	sessionID := "test-attach-cursor-session"
	// Cursor uses JSONL format, same as Claude Code
	transcriptContent := `{"type":"user","message":{"role":"user","content":"add dark mode"},"uuid":"u1"}
{"type":"assistant","message":{"role":"assistant","content":"I'll add dark mode support."},"uuid":"a1"}
`
	// Cursor flat layout: <dir>/<id>.jsonl
	if err := os.WriteFile(filepath.Join(cursorDir, sessionID+".jsonl"), []byte(transcriptContent), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := runAttach(context.Background(), &out, sessionID, agent.AgentNameCursor, true)
	if err != nil {
		t.Fatalf("runAttach failed: %v", err)
	}

	if !strings.Contains(out.String(), "Attached session") {
		t.Errorf("expected 'Attached session' in output, got: %s", out.String())
	}

	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state == nil {
		t.Fatal("expected session state to be created")
		return
	}
	if state.AgentType != agent.AgentTypeCursor {
		t.Errorf("AgentType = %q, want %q", state.AgentType, agent.AgentTypeCursor)
	}
	if state.SessionTurnCount != 1 {
		t.Errorf("SessionTurnCount = %d, want 1", state.SessionTurnCount)
	}
}

func TestAttach_CodexSuccess(t *testing.T) {
	setupAttachTestRepo(t)

	codexDir := t.TempDir()
	t.Setenv("TRACE_TEST_CODEX_SESSION_DIR", codexDir)

	sessionID := "019d6c43-1537-7343-9691-1f8cee04fe59"
	transcriptContent := `{"timestamp":"2026-04-08T10:43:48.000Z","type":"session_meta","payload":{"id":"019d6c43-1537-7343-9691-1f8cee04fe59","timestamp":"2026-04-08T10:43:48.000Z"}}
{"timestamp":"2026-04-08T10:43:49.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"investigate attach failure"}]}}
{"timestamp":"2026-04-08T10:43:50.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Looking into it."}]}}
`
	sessionFile := filepath.Join(codexDir, "2026", "04", "08", "rollout-2026-04-08T10-43-48-"+sessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionFile, []byte(transcriptContent), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := runAttach(context.Background(), &out, sessionID, agent.AgentNameCodex, true)
	if err != nil {
		t.Fatalf("runAttach failed: %v", err)
	}

	if !strings.Contains(out.String(), "Attached session") {
		t.Errorf("expected 'Attached session' in output, got: %s", out.String())
	}

	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state == nil {
		t.Fatal("expected session state to be created")
		return
	}
	if state.AgentType != agent.AgentTypeCodex {
		t.Errorf("AgentType = %q, want %q", state.AgentType, agent.AgentTypeCodex)
	}
	if state.TranscriptPath != sessionFile {
		t.Errorf("TranscriptPath = %q, want %q", state.TranscriptPath, sessionFile)
	}
	if state.LastCheckpointID.IsEmpty() {
		t.Error("expected LastCheckpointID to be set after attach")
	}
}

func TestAttach_FactoryAIDroidSuccess(t *testing.T) {
	setupAttachTestRepo(t)

	droidDir := t.TempDir()
	t.Setenv("TRACE_TEST_DROID_PROJECT_DIR", droidDir)

	sessionID := "test-attach-droid-session"
	// Factory AI Droid uses JSONL format
	transcriptContent := `{"type":"user","message":{"role":"user","content":"deploy to staging"},"uuid":"u1"}
{"type":"assistant","message":{"role":"assistant","content":"Deploying to staging now."},"uuid":"a1"}
`
	// Factory AI Droid: flat <dir>/<id>.jsonl
	if err := os.WriteFile(filepath.Join(droidDir, sessionID+".jsonl"), []byte(transcriptContent), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := runAttach(context.Background(), &out, sessionID, agent.AgentNameFactoryAIDroid, true)
	if err != nil {
		t.Fatalf("runAttach failed: %v", err)
	}

	if !strings.Contains(out.String(), "Attached session") {
		t.Errorf("expected 'Attached session' in output, got: %s", out.String())
	}

	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state == nil {
		t.Fatal("expected session state to be created")
		return
	}
	if state.AgentType != agent.AgentTypeFactoryAIDroid {
		t.Errorf("AgentType = %q, want %q", state.AgentType, agent.AgentTypeFactoryAIDroid)
	}
	if state.SessionTurnCount != 1 {
		t.Errorf("SessionTurnCount = %d, want 1", state.SessionTurnCount)
	}
}

func TestAttach_CursorNestedLayout(t *testing.T) {
	setupAttachTestRepo(t)

	cursorDir := t.TempDir()
	t.Setenv("TRACE_TEST_CURSOR_PROJECT_DIR", cursorDir)

	sessionID := "test-cursor-nested-layout"
	transcriptContent := `{"type":"user","message":{"role":"user","content":"hello"},"uuid":"u1"}
`
	// Cursor IDE nested layout: <dir>/<id>/<id>.jsonl
	nestedDir := filepath.Join(cursorDir, sessionID)
	if err := os.MkdirAll(nestedDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, sessionID+".jsonl"), []byte(transcriptContent), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := runAttach(context.Background(), &out, sessionID, agent.AgentNameCursor, true)
	if err != nil {
		t.Fatalf("runAttach failed: %v", err)
	}

	if !strings.Contains(out.String(), "Attached session") {
		t.Errorf("expected 'Attached session' in output, got: %s", out.String())
	}
}

// setupAttachTestRepo creates a temp git repo with one commit and enables Trace.
// Returns the repo directory. Caller must not use t.Parallel() (uses t.Chdir).
func setupAttachTestRepo(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	testutil.InitRepo(t, tmpDir)
	testutil.WriteFile(t, tmpDir, "init.txt", "init")
	testutil.GitAdd(t, tmpDir, "init.txt")
	testutil.GitCommit(t, tmpDir, "init")
	t.Chdir(tmpDir)
	enableTrace(t, tmpDir)
}

// setupClaudeTranscript creates a fake Claude transcript file.
// The file's mtime is backdated so that waitForTranscriptFlush treats it as
// stale and skips the 3-second poll loop.
func setupClaudeTranscript(t *testing.T, sessionID, content string) {
	t.Helper()
	claudeDir := t.TempDir()
	t.Setenv("TRACE_TEST_CLAUDE_PROJECT_DIR", claudeDir)
	fpath := filepath.Join(claudeDir, sessionID+".jsonl")
	if err := os.WriteFile(fpath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-3 * time.Minute)
	if err := os.Chtimes(fpath, stale, stale); err != nil {
		t.Fatal(err)
	}
}

// enableTrace creates the .trace/settings.json file to mark Trace as enabled.
func enableTrace(t *testing.T, repoDir string) {
	t.Helper()
	traceDir := filepath.Join(repoDir, ".trace")
	if err := os.MkdirAll(traceDir, 0o750); err != nil {
		t.Fatal(err)
	}
	settingsContent := `{"enabled": true}`
	if err := os.WriteFile(filepath.Join(traceDir, "settings.json"), []byte(settingsContent), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setAttachCheckpointsV2Enabled(t *testing.T, repoDir string) {
	t.Helper()
	traceDir := filepath.Join(repoDir, ".trace")
	if err := os.MkdirAll(traceDir, 0o750); err != nil {
		t.Fatal(err)
	}
	settingsContent := `{"enabled": true, "strategy_options": {"checkpoints_v2": true}}`
	if err := os.WriteFile(filepath.Join(traceDir, "settings.json"), []byte(settingsContent), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setAttachCheckpointsV2Only(t *testing.T, repoDir string) {
	t.Helper()
	traceDir := filepath.Join(repoDir, ".trace")
	if err := os.MkdirAll(traceDir, 0o750); err != nil {
		t.Fatal(err)
	}
	settingsContent := `{"enabled": true, "strategy_options": {"checkpoints_version": 2}}`
	if err := os.WriteFile(filepath.Join(traceDir, "settings.json"), []byte(settingsContent), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func readFileFromRef(t *testing.T, repo *git.Repository, refName, filePath string) (string, bool) {
	t.Helper()

	ref, err := repo.Reference(plumbing.ReferenceName(refName), true)
	if err != nil {
		return "", false
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return "", false
	}
	tree, err := commit.Tree()
	if err != nil {
		return "", false
	}
	file, err := tree.File(filePath)
	if err != nil {
		return "", false
	}
	content, err := file.Contents()
	if err != nil {
		return "", false
	}
	return content, true
}

// TestAttach_DiscoversExternalAgents verifies that `trace attach --agent <external>`
// gets past the agent registry check when external_agents is enabled and a
// matching binary is on PATH. Without the DiscoverAndRegister call in the
// attach command, this would fail with "unknown agent: <name>".
//
// This test does not verify end-to-end attach behavior — it asserts only
// that discovery ran. The command is expected to fail later (transcript
// resolution) because we don't stand up a real session.
func TestAttach_DiscoversExternalAgents(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	setupAttachTestRepo(t)

	// Overwrite settings to enable external_agents (enableTrace writes the
	// file without it).
	cwd := mustGetwd(t)
	settingsPath := filepath.Join(cwd, ".trace", "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"enabled":true,"external_agents":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Use a unique name so concurrent test runs can't collide in the global
	// agent registry.
	agentName := types.AgentName("attachtest-discovery-agent")

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "trace-agent-"+string(agentName))
	infoJSON := `{
  "protocol_version": 1,
  "name": "` + string(agentName) + `",
  "type": "Attach Test Agent",
  "description": "Agent for attach discovery test",
  "is_preview": false,
  "protected_dirs": [],
  "hook_names": [],
  "capabilities": {}
}`
	script := "#!/bin/sh\nif [ \"$1\" = \"info\" ]; then\n  echo '" + infoJSON + "'\nfi\n"
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("failed to write mock agent binary: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cmd := newAttachCmd()
	// Pass a bogus session ID — the point is to exercise the registry check,
	// not full attach flow.
	cmd.SetArgs([]string{"--agent", string(agentName), "-f", "fake-session-id"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := cmd.Execute()
	// We expect an error (no transcript), but it must not be the
	// registry-lookup error. A regression (removing DiscoverAndRegister)
	// would produce "unknown agent: attachtest-discovery-agent".
	if err == nil {
		t.Fatalf("expected attach to fail on missing transcript, got success\noutput: %s", out.String())
	}
	if strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("attach did not discover external agent — got registry miss: %v", err)
	}

	// Also confirm the agent actually landed in the registry, so the check
	// above is meaningful (not merely passing because some other error
	// short-circuited before the registry lookup).
	if _, lookupErr := agent.Get(agentName); lookupErr != nil {
		t.Errorf("expected external agent %q in registry after attach, got: %v", agentName, lookupErr)
	}
}

func runGitInDir(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}
