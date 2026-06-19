//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/factoryaidroid"
	_ "github.com/GrayCodeAI/trace/cli/agent/opencode" // Register OpenCode agent
)

// TestFactoryAIDroidAgentDetection verifies Factory AI Droid agent detection.
// Not parallel - contains subtests that use os.Chdir which is process-global.
func TestFactoryAIDroidAgentDetection(t *testing.T) {
	t.Run("agent is registered", func(t *testing.T) {
		t.Parallel()

		agents := agent.List()
		found := false
		for _, name := range agents {
			if name == "factoryai-droid" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("agent.List() = %v, want to contain 'factoryai-droid'", agents)
		}
	})

	t.Run("detects presence when .factory exists", func(t *testing.T) {
		// Not parallel - uses os.Chdir which is process-global
		env := NewTestEnv(t)
		env.InitRepo()

		// Create .factory directory
		factoryDir := filepath.Join(env.RepoDir, ".factory")
		if err := os.MkdirAll(factoryDir, 0o755); err != nil {
			t.Fatalf("failed to create .factory dir: %v", err)
		}

		// Change to repo dir for detection
		oldWd, _ := os.Getwd()
		if err := os.Chdir(env.RepoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()

		ag, err := agent.Get("factoryai-droid")
		if err != nil {
			t.Fatalf("Get(factoryai-droid) error = %v", err)
		}

		ctx := context.Background()
		present, err := ag.DetectPresence(ctx)
		if err != nil {
			t.Fatalf("DetectPresence() error = %v", err)
		}
		if !present {
			t.Error("DetectPresence() = false, want true when .factory exists")
		}
	})
}

// TestFactoryAIDroidHookInstallation verifies hook installation via Factory AI Droid agent interface.
// Note: These tests cannot run in parallel because they use os.Chdir which affects the trace process.
func TestFactoryAIDroidHookInstallation(t *testing.T) {
	// Not parallel - tests use os.Chdir which is process-global

	t.Run("installs all required hooks", func(t *testing.T) {
		// Not parallel - uses os.Chdir
		env := NewTestEnv(t)
		env.InitRepo()

		// Change to repo dir
		oldWd, _ := os.Getwd()
		if err := os.Chdir(env.RepoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()

		ag, err := agent.Get("factoryai-droid")
		if err != nil {
			t.Fatalf("Get(factoryai-droid) error = %v", err)
		}

		hookAgent, ok := agent.AsHookSupport(ag)
		if !ok {
			t.Fatal("factoryai-droid agent does not implement HookSupport")
		}

		ctx := context.Background()
		count, err := hookAgent.InstallHooks(ctx, false, false)
		if err != nil {
			t.Fatalf("InstallHooks() error = %v", err)
		}

		// Should install 8 hooks: SessionStart (session-start + user-prompt-submit), SessionEnd,
		// Stop, UserPromptSubmit, PreToolUse[Task], PostToolUse[Task], PreCompact
		if count != 8 {
			t.Errorf("InstallHooks() count = %d, want 8", count)
		}

		// Verify hooks are installed
		if !hookAgent.AreHooksInstalled(ctx) {
			t.Error("AreHooksInstalled() = false after InstallHooks()")
		}

		// Verify settings.json was created
		settingsPath := filepath.Join(env.RepoDir, ".factory", factoryaidroid.FactorySettingsFileName)
		if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
			t.Error("settings.json was not created")
		}

		// Verify hooks structure in settings.json
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatalf("failed to read settings.json: %v", err)
		}
		content := string(data)

		// Verify all hook types are present
		if !strings.Contains(content, "SessionStart") {
			t.Error("settings.json should contain SessionStart hook")
		}
		if !strings.Contains(content, "SessionEnd") {
			t.Error("settings.json should contain SessionEnd hook")
		}
		if !strings.Contains(content, "Stop") {
			t.Error("settings.json should contain Stop hook")
		}
		if !strings.Contains(content, "UserPromptSubmit") {
			t.Error("settings.json should contain UserPromptSubmit hook")
		}
		if !strings.Contains(content, "PreToolUse") {
			t.Error("settings.json should contain PreToolUse hook")
		}
		if !strings.Contains(content, "PostToolUse") {
			t.Error("settings.json should contain PostToolUse hook")
		}
		if !strings.Contains(content, "PreCompact") {
			t.Error("settings.json should contain PreCompact hook")
		}

		// Verify permissions.deny contains metadata deny rule
		if !strings.Contains(content, "Read(./.trace/metadata/**)") {
			t.Error("settings.json should contain permissions.deny rule for .trace/metadata/**")
		}
	})

	t.Run("idempotent - second install returns 0", func(t *testing.T) {
		// Not parallel - uses os.Chdir
		env := NewTestEnv(t)
		env.InitRepo()

		oldWd, _ := os.Getwd()
		if err := os.Chdir(env.RepoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()

		ag, _ := agent.Get("factoryai-droid")
		hookAgent, _ := agent.AsHookSupport(ag)

		ctx := context.Background()
		// First install
		_, err := hookAgent.InstallHooks(ctx, false, false)
		if err != nil {
			t.Fatalf("first InstallHooks() error = %v", err)
		}

		// Second install should be idempotent
		count, err := hookAgent.InstallHooks(ctx, false, false)
		if err != nil {
			t.Fatalf("second InstallHooks() error = %v", err)
		}
		if count != 0 {
			t.Errorf("second InstallHooks() count = %d, want 0 (idempotent)", count)
		}
	})

	t.Run("localDev mode uses go run", func(t *testing.T) {
		// Not parallel - uses os.Chdir
		env := NewTestEnv(t)
		env.InitRepo()

		oldWd, _ := os.Getwd()
		if err := os.Chdir(env.RepoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()

		ag, _ := agent.Get("factoryai-droid")
		hookAgent, _ := agent.AsHookSupport(ag)

		ctx := context.Background()
		_, err := hookAgent.InstallHooks(ctx, true, false) // localDev = true
		if err != nil {
			t.Fatalf("InstallHooks(localDev=true) error = %v", err)
		}

		// Read settings and verify commands use "go run"
		settingsPath := filepath.Join(env.RepoDir, ".factory", factoryaidroid.FactorySettingsFileName)
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatalf("failed to read settings.json: %v", err)
		}

		content := string(data)
		if !strings.Contains(content, "go run") {
			t.Error("localDev hooks should use 'go run', but settings.json doesn't contain it")
		}
		if !strings.Contains(content, "$(git rev-parse --show-toplevel)") {
			t.Error("localDev hooks should use '$(git rev-parse --show-toplevel)', but settings.json doesn't contain it")
		}
	})

	t.Run("production mode uses trace binary", func(t *testing.T) {
		// Not parallel - uses os.Chdir
		env := NewTestEnv(t)
		env.InitRepo()

		oldWd, _ := os.Getwd()
		if err := os.Chdir(env.RepoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()

		ag, _ := agent.Get("factoryai-droid")
		hookAgent, _ := agent.AsHookSupport(ag)

		ctx := context.Background()
		_, err := hookAgent.InstallHooks(ctx, false, false) // localDev = false
		if err != nil {
			t.Fatalf("InstallHooks(localDev=false) error = %v", err)
		}

		// Read settings and verify commands use "trace" binary
		settingsPath := filepath.Join(env.RepoDir, ".factory", factoryaidroid.FactorySettingsFileName)
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatalf("failed to read settings.json: %v", err)
		}

		content := string(data)
		if !strings.Contains(content, "trace hooks factoryai-droid") {
			t.Error("production hooks should use 'trace hooks factoryai-droid', but settings.json doesn't contain it")
		}
	})

	t.Run("force flag reinstalls hooks", func(t *testing.T) {
		// Not parallel - uses os.Chdir
		env := NewTestEnv(t)
		env.InitRepo()

		oldWd, _ := os.Getwd()
		if err := os.Chdir(env.RepoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()

		ag, _ := agent.Get("factoryai-droid")
		hookAgent, _ := agent.AsHookSupport(ag)

		ctx := context.Background()
		// First install
		_, err := hookAgent.InstallHooks(ctx, false, false)
		if err != nil {
			t.Fatalf("first InstallHooks() error = %v", err)
		}

		// Force reinstall should return count > 0
		count, err := hookAgent.InstallHooks(ctx, false, true) // force = true
		if err != nil {
			t.Fatalf("force InstallHooks() error = %v", err)
		}
		if count != 8 {
			t.Errorf("force InstallHooks() count = %d, want 8", count)
		}
	})
}

// TestFactoryAIDroidSessionMethods verifies ReadSession, WriteSession, and GetSessionDir.
func TestFactoryAIDroidSessionMethods(t *testing.T) {
	t.Parallel()

	t.Run("ReadSession reads and parses transcript", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")
		content := `{"type":"message","id":"msg1","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}
{"type":"message","id":"msg2","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`
		if err := os.WriteFile(transcriptPath, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write transcript: %v", err)
		}

		ag, _ := agent.Get("factoryai-droid")
		session, err := ag.ReadSession(&agent.HookInput{
			SessionID:  "test",
			SessionRef: transcriptPath,
		})
		if err != nil {
			t.Fatalf("ReadSession() error = %v", err)
		}
		if session.SessionID != "test" {
			t.Errorf("SessionID = %q, want %q", session.SessionID, "test")
		}
		if len(session.NativeData) == 0 {
			t.Error("NativeData should not be empty")
		}
	})

	t.Run("ReadSession errors on missing file", func(t *testing.T) {
		t.Parallel()

		ag, _ := agent.Get("factoryai-droid")
		_, err := ag.ReadSession(&agent.HookInput{
			SessionID:  "test",
			SessionRef: "/nonexistent/path/transcript.jsonl",
		})
		if err == nil {
			t.Error("ReadSession() should error on missing file")
		}
	})

	t.Run("WriteSession round-trips with ReadSession", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		originalPath := filepath.Join(tmpDir, "original.jsonl")
		restoredPath := filepath.Join(tmpDir, "sub", "restored.jsonl")

		content := `{"type":"message","id":"msg1","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`
		if err := os.WriteFile(originalPath, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write original: %v", err)
		}

		ag, _ := agent.Get("factoryai-droid")
		session, err := ag.ReadSession(&agent.HookInput{
			SessionID:  "test",
			SessionRef: originalPath,
		})
		if err != nil {
			t.Fatalf("ReadSession() error = %v", err)
		}

		session.SessionRef = restoredPath
		ctx := context.Background()
		if err := ag.WriteSession(ctx, session); err != nil {
			t.Fatalf("WriteSession() error = %v", err)
		}

		restored, err := os.ReadFile(restoredPath)
		if err != nil {
			t.Fatalf("failed to read restored: %v", err)
		}
		if string(restored) != content {
			t.Errorf("round-trip mismatch:\n got: %q\nwant: %q", string(restored), content)
		}
	})

	t.Run("GetSessionDir returns factory sessions path", func(t *testing.T) {
		t.Parallel()

		ag, _ := agent.Get("factoryai-droid")
		dir, err := ag.GetSessionDir("/Users/test/my-project")
		if err != nil {
			t.Fatalf("GetSessionDir() error = %v", err)
		}
		if !strings.Contains(dir, filepath.Join(".factory", "sessions")) {
			t.Errorf("GetSessionDir() = %q, want to contain .factory/sessions", dir)
		}
		if !strings.HasSuffix(dir, "-Users-test-my-project") {
			t.Errorf("GetSessionDir() = %q, want to end with sanitized path", dir)
		}
	})
}

// --- OpenCode Agent Tests ---

// TestOpenCodeAgentDetection verifies OpenCode agent detection and default behavior.
func TestOpenCodeAgentDetection(t *testing.T) {
	t.Run("opencode agent is registered", func(t *testing.T) {
		t.Parallel()

		agents := agent.List()
		found := false
		for _, name := range agents {
			if name == "opencode" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("agent.List() = %v, want to contain 'opencode'", agents)
		}
	})

	t.Run("opencode detects presence when .opencode exists", func(t *testing.T) {
		// Not parallel - uses os.Chdir which is process-global
		env := NewTestEnv(t)
		env.InitRepo()

		// Create .opencode directory
		opencodeDir := filepath.Join(env.RepoDir, ".opencode")
		if err := os.MkdirAll(opencodeDir, 0o755); err != nil {
			t.Fatalf("failed to create .opencode dir: %v", err)
		}

		// Change to repo dir for detection
		oldWd, _ := os.Getwd()
		if err := os.Chdir(env.RepoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()

		ag, err := agent.Get("opencode")
		if err != nil {
			t.Fatalf("Get(opencode) error = %v", err)
		}

		present, err := ag.DetectPresence(context.Background())
		if err != nil {
			t.Fatalf("DetectPresence() error = %v", err)
		}
		if !present {
			t.Error("DetectPresence() = false, want true when .opencode exists")
		}
	})

	t.Run("opencode detects presence when opencode.json exists", func(t *testing.T) {
		// Not parallel - uses os.Chdir which is process-global
		env := NewTestEnv(t)
		env.InitRepo()

		// Create opencode.json config file
		configPath := filepath.Join(env.RepoDir, "opencode.json")
		if err := os.WriteFile(configPath, []byte(`{}`), 0o644); err != nil {
			t.Fatalf("failed to write opencode.json: %v", err)
		}

		// Change to repo dir for detection
		oldWd, _ := os.Getwd()
		if err := os.Chdir(env.RepoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()

		ag, err := agent.Get("opencode")
		if err != nil {
			t.Fatalf("Get(opencode) error = %v", err)
		}

		present, err := ag.DetectPresence(context.Background())
		if err != nil {
			t.Fatalf("DetectPresence() error = %v", err)
		}
		if !present {
			t.Error("DetectPresence() = false, want true when opencode.json exists")
		}
	})
}

// TestOpenCodeHookInstallation verifies hook installation via OpenCode agent interface.
// Not parallel - uses os.Chdir which is process-global.
func TestOpenCodeHookInstallation(t *testing.T) {
	t.Run("installs plugin file", func(t *testing.T) {
		// Not parallel - uses os.Chdir
		env := NewTestEnv(t)
		env.InitRepo()

		oldWd, _ := os.Getwd()
		if err := os.Chdir(env.RepoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()

		ag, err := agent.Get("opencode")
		if err != nil {
			t.Fatalf("Get(opencode) error = %v", err)
		}

		hookAgent, ok := agent.AsHookSupport(ag)
		if !ok {
			t.Fatal("opencode agent does not implement HookSupport")
		}

		count, err := hookAgent.InstallHooks(context.Background(), false, false)
		if err != nil {
			t.Fatalf("InstallHooks() error = %v", err)
		}

		// Should install 1 plugin file
		if count != 1 {
			t.Errorf("InstallHooks() count = %d, want 1", count)
		}

		// Verify hooks are installed
		if !hookAgent.AreHooksInstalled(context.Background()) {
			t.Error("AreHooksInstalled() = false after InstallHooks()")
		}

		// Verify plugin file was created
		pluginPath := filepath.Join(env.RepoDir, ".opencode", "plugins", "trace.ts")
		if _, err := os.Stat(pluginPath); os.IsNotExist(err) {
			t.Error("trace.ts plugin was not created")
		}
	})

	t.Run("idempotent - second install returns 0", func(t *testing.T) {
		// Not parallel - uses os.Chdir
		env := NewTestEnv(t)
		env.InitRepo()

		oldWd, _ := os.Getwd()
		if err := os.Chdir(env.RepoDir); err != nil {
			t.Fatalf("failed to chdir: %v", err)
		}
		defer func() { _ = os.Chdir(oldWd) }()

		ag, _ := agent.Get("opencode")
		hookAgent, _ := agent.AsHookSupport(ag)

		// First install
		_, err := hookAgent.InstallHooks(context.Background(), false, false)
		if err != nil {
			t.Fatalf("first InstallHooks() error = %v", err)
		}

		// Second install should be idempotent
		count, err := hookAgent.InstallHooks(context.Background(), false, false)
		if err != nil {
			t.Fatalf("second InstallHooks() error = %v", err)
		}
		if count != 0 {
			t.Errorf("second InstallHooks() count = %d, want 0 (idempotent)", count)
		}
	})
}

// TestOpenCodeSessionOperations verifies ReadSession/WriteSession via OpenCode agent interface.
func TestOpenCodeSessionOperations(t *testing.T) {
	t.Parallel()

	t.Run("ReadSession parses export JSON transcript and computes ModifiedFiles", func(t *testing.T) {
		t.Parallel()
		env := NewTestEnv(t)
		env.InitRepo()

		// Create an OpenCode export JSON transcript file
		transcriptPath := filepath.Join(env.RepoDir, "test-transcript.json")
		transcriptContent := `{
			"info": {"id": "test-session"},
			"messages": [
				{"info": {"id": "msg-1", "role": "user", "time": {"created": 1708300000}}, "parts": [{"type": "text", "text": "Fix the bug"}]},
				{"info": {"id": "msg-2", "role": "assistant", "time": {"created": 1708300001, "completed": 1708300005}, "tokens": {"input": 100, "output": 50, "reasoning": 5, "cache": {"read": 3, "write": 10}}}, "parts": [{"type": "text", "text": "I'll fix it."}, {"type": "tool", "tool": "write", "callID": "call-1", "state": {"status": "completed", "input": {"filePath": "main.go"}, "output": "written"}}]},
				{"info": {"id": "msg-3", "role": "user", "time": {"created": 1708300010}}, "parts": [{"type": "text", "text": "Also fix util.go"}]},
				{"info": {"id": "msg-4", "role": "assistant", "time": {"created": 1708300011, "completed": 1708300015}, "tokens": {"input": 120, "output": 60, "reasoning": 3, "cache": {"read": 5, "write": 12}}}, "parts": [{"type": "tool", "tool": "edit", "callID": "call-2", "state": {"status": "completed", "input": {"filePath": "util.go"}, "output": "edited"}}]}
			]
		}`
		if err := os.WriteFile(transcriptPath, []byte(transcriptContent), 0o644); err != nil {
			t.Fatalf("failed to write transcript: %v", err)
		}

		ag, _ := agent.Get("opencode")
		session, err := ag.ReadSession(&agent.HookInput{
			SessionID:  "test-session",
			SessionRef: transcriptPath,
		})
		if err != nil {
			t.Fatalf("ReadSession() error = %v", err)
		}

		// Verify session metadata
		if session.SessionID != "test-session" {
			t.Errorf("SessionID = %q, want %q", session.SessionID, "test-session")
		}
		if session.AgentName != "opencode" {
			t.Errorf("AgentName = %q, want %q", session.AgentName, "opencode")
		}

		// Verify NativeData is populated
		if len(session.NativeData) == 0 {
			t.Error("NativeData is empty, want transcript content")
		}

		// Verify ModifiedFiles computed from tool calls
		if len(session.ModifiedFiles) != 2 {
			t.Errorf("ModifiedFiles = %v, want 2 files (main.go, util.go)", session.ModifiedFiles)
		}
	})

	t.Run("WriteSession validates input", func(t *testing.T) {
		t.Parallel()

		ag, _ := agent.Get("opencode")

		if err := ag.WriteSession(context.Background(), nil); err == nil {
			t.Error("WriteSession(nil) should error")
		}
		if err := ag.WriteSession(context.Background(), &agent.AgentSession{}); err == nil {
			t.Error("WriteSession with empty NativeData should error")
		}
	})
}

// TestOpenCodeHelperMethods verifies OpenCode-specific helper methods.
func TestOpenCodeHelperMethods(t *testing.T) {
	t.Parallel()

	t.Run("FormatResumeCommand returns opencode -s", func(t *testing.T) {
		t.Parallel()

		ag, _ := agent.Get("opencode")
		cmd := ag.FormatResumeCommand("abc123")

		if cmd != "opencode -s abc123" {
			t.Errorf("FormatResumeCommand() = %q, want %q", cmd, "opencode -s abc123")
		}
	})

	t.Run("ProtectedDirs includes .opencode", func(t *testing.T) {
		t.Parallel()

		ag, _ := agent.Get("opencode")
		dirs := ag.ProtectedDirs()

		found := false
		for _, d := range dirs {
			if d == ".opencode" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ProtectedDirs() = %v, want to contain '.opencode'", dirs)
		}
	})

	t.Run("IsPreview returns true", func(t *testing.T) {
		t.Parallel()

		ag, _ := agent.Get("opencode")
		if !ag.IsPreview() {
			t.Error("IsPreview() = false, want true")
		}
	})
}
