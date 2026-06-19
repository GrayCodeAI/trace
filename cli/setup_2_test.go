package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent"
	_ "github.com/GrayCodeAI/trace/cli/agent/claudecode"
	"github.com/GrayCodeAI/trace/cli/agent/external"
	_ "github.com/GrayCodeAI/trace/cli/agent/geminicli"
	"github.com/GrayCodeAI/trace/cli/agent/types"
)

func TestAppendShellCompletion(t *testing.T) {
	tests := []struct {
		name           string
		rcFileRelPath  string
		completionLine string
		preExisting    string // existing content in rc file; empty means file doesn't exist
		createParent   bool   // whether parent dir already exists
	}{
		{
			name:           "zsh_new_file",
			rcFileRelPath:  ".zshrc",
			completionLine: "source <(trace completion zsh)",
			createParent:   true,
		},
		{
			name:           "zsh_existing_file",
			rcFileRelPath:  ".zshrc",
			completionLine: "source <(trace completion zsh)",
			preExisting:    "# existing zshrc content\n",
			createParent:   true,
		},
		{
			name:           "fish_no_parent_dir",
			rcFileRelPath:  filepath.Join(".config", "fish", "config.fish"),
			completionLine: "trace completion fish | source",
			createParent:   false,
		},
		{
			name:           "fish_existing_dir",
			rcFileRelPath:  filepath.Join(".config", "fish", "config.fish"),
			completionLine: "trace completion fish | source",
			createParent:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			rcFile := filepath.Join(home, tt.rcFileRelPath)

			if tt.createParent {
				if err := os.MkdirAll(filepath.Dir(rcFile), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if tt.preExisting != "" {
				if err := os.WriteFile(rcFile, []byte(tt.preExisting), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			if err := appendShellCompletion(rcFile, tt.completionLine); err != nil {
				t.Fatalf("appendShellCompletion() error: %v", err)
			}

			// Verify the file was created and contains the completion line.
			data, err := os.ReadFile(rcFile)
			if err != nil {
				t.Fatalf("reading rc file: %v", err)
			}
			content := string(data)

			if !strings.Contains(content, shellCompletionComment) {
				t.Errorf("rc file missing comment %q", shellCompletionComment)
			}
			if !strings.Contains(content, tt.completionLine) {
				t.Errorf("rc file missing completion line %q", tt.completionLine)
			}
			if tt.preExisting != "" && !strings.HasPrefix(content, tt.preExisting) {
				t.Errorf("pre-existing content was overwritten")
			}

			// Verify parent directory permissions.
			info, err := os.Stat(filepath.Dir(rcFile))
			if err != nil {
				t.Fatalf("stat parent dir: %v", err)
			}
			if !info.IsDir() {
				t.Fatal("parent path is not a directory")
			}
		})
	}
}

func TestRemoveTraceDirectory_NotExists(t *testing.T) {
	setupTestDir(t)

	// Should not error when directory doesn't exist
	if err := removeTraceDirectory(context.Background()); err != nil {
		t.Fatalf("removeTraceDirectory(context.Background()) should not error when directory doesn't exist: %v", err)
	}
}

func TestPrintMissingAgentError(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printMissingAgentError(&buf)
	output := buf.String()

	if !strings.Contains(output, "Missing agent name") {
		t.Error("expected 'Missing agent name' in output")
	}
	for _, a := range agent.List() {
		if !strings.Contains(output, string(a)) {
			t.Errorf("expected agent %q listed in output", a)
		}
	}
	if !strings.Contains(output, "(default)") {
		t.Error("expected default annotation in output")
	}
	if !strings.Contains(output, "Usage: trace enable --agent") {
		t.Error("expected usage line in output")
	}
}

func TestPrintWrongAgentError(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printWrongAgentError(&buf, "not-an-agent")
	output := buf.String()

	if !strings.Contains(output, `Unknown agent "not-an-agent"`) {
		t.Error("expected unknown agent name in output")
	}
	for _, a := range agent.List() {
		if !strings.Contains(output, string(a)) {
			t.Errorf("expected agent %q listed in output", a)
		}
	}
	if !strings.Contains(output, "(default)") {
		t.Error("expected default annotation in output")
	}
	if !strings.Contains(output, "Usage: trace enable --agent") {
		t.Error("expected usage line in output")
	}
}

func TestEnableCmd_AgentFlagNoValue(t *testing.T) {
	setupTestRepo(t)

	cmd := newEnableCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--agent"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --agent is used without a value")
	}

	output := stderr.String()
	if !strings.Contains(output, "Missing agent name") {
		t.Errorf("expected helpful error message, got: %s", output)
	}
	if !strings.Contains(output, string(agent.DefaultAgentName)) {
		t.Errorf("expected default agent listed, got: %s", output)
	}
	if strings.Contains(output, "flag needs an argument") {
		t.Error("should not contain default cobra/pflag error message")
	}
}

func TestEnableCmd_AgentFlagEmptyValue(t *testing.T) {
	setupTestRepo(t)

	cmd := newEnableCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--agent="})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --agent= is used with empty value")
	}

	output := stderr.String()
	if !strings.Contains(output, "Missing agent name") {
		t.Errorf("expected helpful error message, got: %s", output)
	}
	if strings.Contains(output, "flag needs an argument") {
		t.Error("should not contain default cobra/pflag error message")
	}
}

func TestEnableUsesSetupFlow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		args      []string
		agentName string
		want      bool
	}{
		{name: "bare enable", args: nil, want: false},
		{name: "project only", args: []string{"--project"}, want: false},
		{name: "local only", args: []string{"--local"}, want: false},
		{name: "force", args: []string{"--force"}, want: true},
		{name: "local dev", args: []string{"--local-dev"}, want: true},
		{name: "absolute hook path", args: []string{"--absolute-git-hook-path"}, want: true},
		{name: "telemetry changed", args: []string{"--telemetry=false"}, want: true},
		{name: "checkpoint remote", args: []string{"--checkpoint-remote", "github:org/repo"}, want: true},
		{name: "skip push sessions", args: []string{"--skip-push-sessions"}, want: true},
		{name: "agent flag", args: []string{"--agent", "claude-code"}, agentName: "claude-code", want: true},
		{name: "yes flag", args: []string{"--yes"}, want: true},
		{name: "yes short flag", args: []string{"-y"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cmd := newEnableCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tt.args)
			if err := cmd.ParseFlags(tt.args); err != nil {
				t.Fatalf("ParseFlags() error = %v", err)
			}

			if got := enableUsesSetupFlow(cmd, tt.agentName); got != tt.want {
				t.Fatalf("enableUsesSetupFlow(%v, %q) = %v, want %v", tt.args, tt.agentName, got, tt.want)
			}
		})
	}
}

func TestEnableCmd_ForceOnConfiguredRepo_UsesConfigureFlow(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)
	writeClaudeHooksFixture(t)

	cmd := newEnableCmd()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--force"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("enable --force error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Cannot show agent selection in non-interactive mode.") {
		t.Fatalf("expected enable --force to route to configure flow, got: %s", output)
	}
	if strings.Contains(output, "Trace is already enabled.") {
		t.Fatalf("expected enable --force to avoid the lightweight re-enable path, got: %s", output)
	}
}

func TestEnableCmd_ForceOnConfiguredDisabledRepo_Reenables(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsDisabled)
	writeClaudeHooksFixture(t)

	cmd := newEnableCmd()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--force"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("enable --force error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Cannot show agent selection in non-interactive mode.") {
		t.Fatalf("expected enable --force to route through manage agents before enabling, got: %s", output)
	}
	if !strings.Contains(output, "Trace is now enabled.") {
		t.Fatalf("expected enable --force to still enable the repo, got: %s", output)
	}

	enabled, err := IsEnabled(context.Background())
	if err != nil {
		t.Fatalf("IsEnabled() error = %v", err)
	}
	if !enabled {
		t.Fatal("expected repo to be enabled after enable --force")
	}
}

func TestEnableCmd_ForceAndStrategyFlagsOnConfiguredDisabledRepo_ReenablesAndUpdatesSettings(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsDisabled)
	writeClaudeHooksFixture(t)

	cmd := newEnableCmd()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--force", "--checkpoint-remote", "github:org/repo", "--skip-push-sessions"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("enable with force and strategy flags error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Settings updated") {
		t.Fatalf("expected strategy flags to be applied, got: %s", output)
	}
	if !strings.Contains(output, "Cannot show agent selection in non-interactive mode.") {
		t.Fatalf("expected force handling to still reach manage agents, got: %s", output)
	}
	if !strings.Contains(output, "Trace is now enabled.") {
		t.Fatalf("expected repo to be enabled after updating settings, got: %s", output)
	}

	enabled, err := IsEnabled(context.Background())
	if err != nil {
		t.Fatalf("IsEnabled() error = %v", err)
	}
	if !enabled {
		t.Fatal("expected repo to be enabled after enable with strategy flags")
	}

	s, err := LoadTraceSettings(context.Background())
	if err != nil {
		t.Fatalf("LoadTraceSettings() error = %v", err)
	}
	if got := s.StrategyOptions["push_sessions"]; got != false {
		t.Fatalf("push_sessions = %v, want false", got)
	}
	checkpointRemote, ok := s.StrategyOptions["checkpoint_remote"].(map[string]interface{})
	if !ok {
		t.Fatalf("checkpoint_remote = %#v, want map", s.StrategyOptions["checkpoint_remote"])
	}
	if checkpointRemote["provider"] != "github" || checkpointRemote["repo"] != "org/repo" {
		t.Fatalf("checkpoint_remote = %#v, want github/org/repo", checkpointRemote)
	}
}

// Tests for detectOrSelectAgent

func TestDetectOrSelectAgent_AgentDetected(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir
	setupTestRepo(t)

	// Create .claude directory so Claude Code agent is detected
	if err := os.MkdirAll(".claude", 0o755); err != nil {
		t.Fatalf("Failed to create .claude directory: %v", err)
	}

	var buf bytes.Buffer
	agents, err := detectOrSelectAgent(context.Background(), &buf, nil)
	if err != nil {
		t.Fatalf("detectOrSelectAgent() error = %v", err)
	}

	// Should detect Claude Code
	if len(agents) != 1 {
		t.Fatalf("detectOrSelectAgent() returned %d agents, want 1", len(agents))
	}
	if agents[0].Name() != agent.AgentNameClaudeCode {
		t.Errorf("detectOrSelectAgent() agent name = %v, want %v", agents[0].Name(), agent.AgentNameClaudeCode)
	}

	output := buf.String()
	if !strings.Contains(output, "Detected agent:") {
		t.Errorf("Expected output to contain 'Detected agent:', got: %s", output)
	}
	if !strings.Contains(output, string(agent.AgentTypeClaudeCode)) {
		t.Errorf("Expected output to contain '%s', got: %s", agent.AgentTypeClaudeCode, output)
	}
}

func TestDetectOrSelectAgent_GeminiDetected(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir
	setupTestRepo(t)

	// Create .gemini directory so Gemini agent is detected
	if err := os.MkdirAll(".gemini", 0o755); err != nil {
		t.Fatalf("Failed to create .gemini directory: %v", err)
	}

	var buf bytes.Buffer
	agents, err := detectOrSelectAgent(context.Background(), &buf, nil)
	if err != nil {
		t.Fatalf("detectOrSelectAgent() error = %v", err)
	}

	// Should detect Gemini
	if len(agents) != 1 {
		t.Fatalf("detectOrSelectAgent() returned %d agents, want 1", len(agents))
	}
	if agents[0].Name() != agent.AgentNameGemini {
		t.Errorf("detectOrSelectAgent() agent name = %v, want %v", agents[0].Name(), agent.AgentNameGemini)
	}

	output := buf.String()
	if !strings.Contains(output, "Detected agent:") {
		t.Errorf("Expected output to contain 'Detected agent:', got: %s", output)
	}
}

func TestDetectOrSelectAgent_OnlyExternalDetected_WithTTY_PromptsUser(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir, t.Setenv, and global agent registration
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	setupTestRepo(t)
	t.Setenv("TRACE_TEST_TTY", "1")

	externalAgentName := "ext-prompt-pi"
	externalDir := t.TempDir()
	writeExternalAgentBinary(t, externalDir, externalAgentName)
	t.Setenv("TRACE_TEST_EXTERNAL_PRESENT", "1")
	t.Setenv("PATH", externalDir)

	external.DiscoverAndRegisterAlways(context.Background())

	var receivedAvailable []string
	selectFn := func(available []string) ([]string, error) {
		receivedAvailable = available
		return []string{string(agent.AgentNameClaudeCode)}, nil
	}

	var buf bytes.Buffer
	agents, err := detectOrSelectAgent(context.Background(), &buf, selectFn)
	if err != nil {
		t.Fatalf("detectOrSelectAgent() error = %v", err)
	}

	if len(receivedAvailable) == 0 {
		t.Fatal("Expected interactive prompt when only an external agent is detected")
	}
	if !slices.Contains(receivedAvailable, externalAgentName) {
		t.Fatalf("Expected external agent %q in options, got %v", externalAgentName, receivedAvailable)
	}
	if !slices.Contains(receivedAvailable, string(agent.AgentNameClaudeCode)) {
		t.Fatalf("Expected built-in agent options alongside external agent, got %v", receivedAvailable)
	}
	if len(agents) != 1 || agents[0].Name() != agent.AgentNameClaudeCode {
		t.Fatalf("Expected selected Claude Code agent, got %v", agents)
	}
	if strings.Contains(buf.String(), "Detected agent:") {
		t.Errorf("Expected external-only detection to prompt instead of auto-selecting, got output: %s", buf.String())
	}
}

func TestIsBuiltInAgent_ExternalAgent_False(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	setupTestRepo(t)

	externalAgentName := "ext-preselect-pi"
	externalDir := t.TempDir()
	writeExternalAgentBinary(t, externalDir, externalAgentName)
	t.Setenv("TRACE_TEST_EXTERNAL_PRESENT", "1")
	t.Setenv("PATH", externalDir)

	external.DiscoverAndRegisterAlways(context.Background())

	externalAgent, err := agent.Get(types.AgentName(externalAgentName))
	if err != nil {
		t.Fatalf("failed to get external agent %q: %v", externalAgentName, err)
	}

	if isBuiltInAgent(externalAgent) {
		t.Fatalf("expected external agent %q to not be treated as built-in", externalAgentName)
	}
}

func TestIsBuiltInAgent_BuiltInAgent_True(t *testing.T) {
	t.Parallel()

	claudeAgent, err := agent.Get(agent.AgentNameClaudeCode)
	if err != nil {
		t.Fatalf("failed to get claude agent: %v", err)
	}

	if !isBuiltInAgent(claudeAgent) {
		t.Fatal("expected built-in agent to be treated as built-in")
	}
}

func TestDetectOrSelectAgent_NoDetection_NoTTY_FallsBackToDefault(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)

	// No .claude or .gemini directory - detection will fail

	var buf bytes.Buffer
	agents, err := detectOrSelectAgent(context.Background(), &buf, nil)
	if err != nil {
		t.Fatalf("detectOrSelectAgent() error = %v", err)
	}

	// Should fall back to default agent (Claude Code)
	if len(agents) != 1 {
		t.Fatalf("detectOrSelectAgent() returned %d agents, want 1", len(agents))
	}
	if agents[0].Name() != agent.DefaultAgentName {
		t.Errorf("detectOrSelectAgent() agent name = %v, want default %v", agents[0].Name(), agent.DefaultAgentName)
	}

	output := buf.String()
	if !strings.Contains(output, "Agent:") {
		t.Errorf("Expected output to contain 'Agent:', got: %s", output)
	}
	if !strings.Contains(output, "(use --agent to change)") {
		t.Errorf("Expected output to contain '(use --agent to change)', got: %s", output)
	}
}

func TestDetectOrSelectAgent_NoDetection_WithTTY_ShowsPromptMessages(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	t.Setenv("TRACE_TEST_TTY", "1")

	// No .claude or .gemini directory - detection will fail

	// Inject selector to avoid blocking on interactive form.Run().
	// The selector receives available agent names so tests can validate the options.
	selectFn := func(available []string) ([]string, error) {
		if len(available) == 0 {
			t.Error("selectFn received no available agents")
		}
		return []string{string(agent.AgentNameClaudeCode)}, nil
	}

	var buf bytes.Buffer
	agents, err := detectOrSelectAgent(context.Background(), &buf, selectFn)
	if err != nil {
		t.Fatalf("detectOrSelectAgent() error = %v", err)
	}

	// Should return the mock-selected agent
	if len(agents) != 1 {
		t.Fatalf("detectOrSelectAgent() returned %d agents, want 1", len(agents))
	}
	if agents[0].Name() != agent.AgentNameClaudeCode {
		t.Errorf("detectOrSelectAgent() agent = %v, want %v", agents[0].Name(), agent.AgentNameClaudeCode)
	}

	output := buf.String()
	if !strings.Contains(output, "Selected agents:") {
		t.Errorf("Expected output to contain 'Selected agents:', got: %s", output)
	}
}

func TestDetectOrSelectAgent_SelectionCancelled(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	t.Setenv("TRACE_TEST_TTY", "1")

	selectFn := func(_ []string) ([]string, error) {
		return nil, errors.New("user cancelled")
	}

	var buf bytes.Buffer
	_, err := detectOrSelectAgent(context.Background(), &buf, selectFn)
	if err == nil {
		t.Fatal("expected error when selection is cancelled")
	}
	if !strings.Contains(err.Error(), "user cancelled") {
		t.Errorf("expected 'user cancelled' in error, got: %v", err)
	}
}

func TestDetectOrSelectAgent_NoneSelected(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	t.Setenv("TRACE_TEST_TTY", "1")

	selectFn := func(_ []string) ([]string, error) {
		return []string{}, nil // user deselected everything
	}

	var buf bytes.Buffer
	_, err := detectOrSelectAgent(context.Background(), &buf, selectFn)
	if err == nil {
		t.Fatal("expected error when no agents selected")
	}
	if !strings.Contains(err.Error(), "no agents selected") {
		t.Errorf("expected 'no agents selected' in error, got: %v", err)
	}
}

func TestDetectOrSelectAgent_BothDirectoriesExist_PromptsUser(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	t.Setenv("TRACE_TEST_TTY", "1")

	// Create both .claude and .gemini directories
	if err := os.MkdirAll(".claude", 0o755); err != nil {
		t.Fatalf("Failed to create .claude directory: %v", err)
	}
	if err := os.MkdirAll(".gemini", 0o755); err != nil {
		t.Fatalf("Failed to create .gemini directory: %v", err)
	}

	// Inject selector — receives available names, returns both
	selectFn := func(available []string) ([]string, error) {
		if len(available) < 2 {
			t.Errorf("expected at least 2 available agents, got %d", len(available))
		}
		return []string{string(agent.AgentNameClaudeCode), string(agent.AgentNameGemini)}, nil
	}

	var buf bytes.Buffer
	agents, err := detectOrSelectAgent(context.Background(), &buf, selectFn)
	if err != nil {
		t.Fatalf("detectOrSelectAgent() error = %v", err)
	}

	// Should return both selected agents
	if len(agents) != 2 {
		t.Fatalf("detectOrSelectAgent() returned %d agents, want 2", len(agents))
	}

	output := buf.String()
	if !strings.Contains(output, "Detected multiple agents:") {
		t.Errorf("Expected output to contain 'Detected multiple agents:', got: %s", output)
	}
	if !strings.Contains(output, "Claude Code") {
		t.Errorf("Expected output to mention Claude Code, got: %s", output)
	}
	if !strings.Contains(output, "Gemini CLI") {
		t.Errorf("Expected output to mention Gemini CLI, got: %s", output)
	}
	if !strings.Contains(output, "Selected agents:") {
		t.Errorf("Expected output to contain 'Selected agents:', got: %s", output)
	}
}

func TestDetectOrSelectAgent_BothDirectoriesExist_NoTTY_UsesAll(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)

	// Create both .claude and .gemini directories
	if err := os.MkdirAll(".claude", 0o755); err != nil {
		t.Fatalf("Failed to create .claude directory: %v", err)
	}
	if err := os.MkdirAll(".gemini", 0o755); err != nil {
		t.Fatalf("Failed to create .gemini directory: %v", err)
	}

	var buf bytes.Buffer
	agents, err := detectOrSelectAgent(context.Background(), &buf, nil)
	if err != nil {
		t.Fatalf("detectOrSelectAgent() error = %v", err)
	}

	// With no TTY and multiple detected, should return all detected agents
	if len(agents) != 2 {
		t.Errorf("detectOrSelectAgent() returned %d agents, want 2", len(agents))
	}
}

// writeClaudeHooksFixture writes a minimal .claude/settings.json with Trace hooks installed.
// Only the Stop hook is needed — AreHooksInstalled() checks for it first.
func writeClaudeHooksFixture(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(".claude", 0o755); err != nil {
		t.Fatalf("Failed to create .claude directory: %v", err)
	}
	hooksJSON := `{
		"hooks": {
			"Stop": [{"hooks": [{"type": "command", "command": "trace hooks claude-code stop"}]}]
		}
	}`
	if err := os.WriteFile(".claude/settings.json", []byte(hooksJSON), 0o644); err != nil {
		t.Fatalf("Failed to write .claude/settings.json: %v", err)
	}
}

// writeGeminiHooksFixture writes a minimal .gemini/settings.json with Trace hooks installed.
// AreHooksInstalled() checks for any hook command starting with "trace ".
func writeGeminiHooksFixture(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(".gemini", 0o755); err != nil {
		t.Fatalf("Failed to create .gemini directory: %v", err)
	}
	hooksJSON := `{
		"hooks": {
			"enabled": true,
			"SessionStart": [{"hooks": [{"type": "command", "command": "trace hooks gemini session-start"}]}]
		}
	}`
	if err := os.WriteFile(".gemini/settings.json", []byte(hooksJSON), 0o644); err != nil {
		t.Fatalf("Failed to write .gemini/settings.json: %v", err)
	}
}

func TestDetectOrSelectAgent_ReRun_AlwaysPromptsWithInstalledPreSelected(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	t.Setenv("TRACE_TEST_TTY", "1")

	// Install Claude Code hooks (simulates a previous `trace enable` run)
	writeClaudeHooksFixture(t)

	// Verify hooks are detected as installed
	installed := GetAgentsWithHooksInstalled(context.Background())
	if len(installed) == 0 {
		t.Fatal("Expected Claude Code hooks to be detected as installed")
	}

	// Track what the selector receives
	var receivedAvailable []string
	selectFn := func(available []string) ([]string, error) {
		receivedAvailable = available
		// User keeps claude-code selected
		return []string{string(agent.AgentNameClaudeCode)}, nil
	}

	var buf bytes.Buffer
	agents, err := detectOrSelectAgent(context.Background(), &buf, selectFn)
	if err != nil {
		t.Fatalf("detectOrSelectAgent() error = %v", err)
	}

	// Should have been prompted (selectFn called) even though only one agent is detected
	if len(receivedAvailable) == 0 {
		t.Fatal("Expected interactive prompt to be shown on re-run, but selectFn was not called")
	}

	// Should return the selected agent
	if len(agents) != 1 || agents[0].Name() != agent.AgentNameClaudeCode {
		t.Errorf("Expected [claude-code], got %v", agents)
	}

	// Should NOT contain "Detected agent:" (the auto-use message for first run)
	output := buf.String()
	if strings.Contains(output, "Detected agent:") {
		t.Errorf("Re-run should not auto-use agent, but got: %s", output)
	}
}

func TestDetectOrSelectAgent_ReRun_NoTTY_KeepsInstalled(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)

	// Install Claude Code hooks
	writeClaudeHooksFixture(t)

	var buf bytes.Buffer
	agents, err := detectOrSelectAgent(context.Background(), &buf, nil)
	if err != nil {
		t.Fatalf("detectOrSelectAgent() error = %v", err)
	}

	// Should keep currently installed agents without prompting
	if len(agents) != 1 {
		t.Fatalf("Expected 1 agent, got %d", len(agents))
	}
	if agents[0].Name() != agent.AgentNameClaudeCode {
		t.Errorf("Expected claude-code, got %v", agents[0].Name())
	}
}

// checkClaudeCodeHooksInstalled checks if Claude Code hooks are installed.
func checkClaudeCodeHooksInstalled() bool {
	ag, err := agent.Get(agent.AgentNameClaudeCode)
	if err != nil {
		return false
	}
	hookAgent, ok := agent.AsHookSupport(ag)
	if !ok {
		return false
	}
	return hookAgent.AreHooksInstalled(context.Background())
}

// checkGeminiCLIHooksInstalled checks if Gemini CLI hooks are installed.
func checkGeminiCLIHooksInstalled() bool {
	ag, err := agent.Get(agent.AgentNameGemini)
	if err != nil {
		return false
	}
	hookAgent, ok := agent.AsHookSupport(ag)
	if !ok {
		return false
	}
	return hookAgent.AreHooksInstalled(context.Background())
}
