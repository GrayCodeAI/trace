package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent"
	_ "github.com/GrayCodeAI/trace/cli/agent/claudecode"
	_ "github.com/GrayCodeAI/trace/cli/agent/geminicli"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/settings"
)

func TestUninstallDeselectedAgentHooks(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir
	setupTestRepo(t)

	// Install Claude Code hooks
	writeClaudeHooksFixture(t)

	// Verify hooks are installed
	if !checkClaudeCodeHooksInstalled() {
		t.Fatal("Expected Claude Code hooks to be installed before test")
	}

	// Call uninstallDeselectedAgentHooks with an empty selection (deselect claude-code)
	var buf bytes.Buffer
	err := uninstallDeselectedAgentHooks(context.Background(), &buf, []agent.Agent{})
	if err != nil {
		t.Fatalf("uninstallDeselectedAgentHooks() error = %v", err)
	}

	// Hooks should be uninstalled
	if checkClaudeCodeHooksInstalled() {
		t.Error("Expected Claude Code hooks to be uninstalled after deselection")
	}

	output := buf.String()
	if !strings.Contains(output, "Removed") {
		t.Errorf("Expected output to mention removal, got: %s", output)
	}
}

func TestUninstallDeselectedAgentHooks_KeepsSelectedAgents(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir
	setupTestRepo(t)

	// Install Claude Code hooks
	writeClaudeHooksFixture(t)

	// Call uninstallDeselectedAgentHooks with claude-code still selected
	claudeAgent, err := agent.Get(agent.AgentNameClaudeCode)
	if err != nil {
		t.Fatalf("Failed to get claude-code agent: %v", err)
	}

	var buf bytes.Buffer
	err = uninstallDeselectedAgentHooks(context.Background(), &buf, []agent.Agent{claudeAgent})
	if err != nil {
		t.Fatalf("uninstallDeselectedAgentHooks() error = %v", err)
	}

	// Hooks should still be installed
	if !checkClaudeCodeHooksInstalled() {
		t.Error("Expected Claude Code hooks to remain installed when still selected")
	}

	output := buf.String()
	if strings.Contains(output, "Removed") {
		t.Errorf("Should not mention removal when agent is still selected, got: %s", output)
	}
}

func TestUninstallDeselectedAgentHooks_MultipleInstalled_DeselectOne(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir
	setupTestRepo(t)

	// Install both Claude Code and Gemini hooks
	writeClaudeHooksFixture(t)
	writeGeminiHooksFixture(t)

	// Verify both are installed
	installed := GetAgentsWithHooksInstalled(context.Background())
	if len(installed) < 2 {
		t.Fatalf("Expected at least 2 agents installed, got %d", len(installed))
	}

	// Keep only Claude Code selected (deselect Gemini)
	claudeAgent, err := agent.Get(agent.AgentNameClaudeCode)
	if err != nil {
		t.Fatalf("Failed to get claude-code agent: %v", err)
	}

	var buf bytes.Buffer
	err = uninstallDeselectedAgentHooks(context.Background(), &buf, []agent.Agent{claudeAgent})
	if err != nil {
		t.Fatalf("uninstallDeselectedAgentHooks() error = %v", err)
	}

	// Claude Code hooks should remain
	if !checkClaudeCodeHooksInstalled() {
		t.Error("Expected Claude Code hooks to remain installed")
	}

	// Gemini hooks should be removed
	if checkGeminiCLIHooksInstalled() {
		t.Error("Expected Gemini CLI hooks to be uninstalled after deselection")
	}

	output := buf.String()
	if !strings.Contains(output, "Removed") {
		t.Errorf("Expected output to mention removal, got: %s", output)
	}
}

func TestManageAgents_DeselectRemovesAgent(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	t.Setenv("TRACE_TEST_TTY", "1")
	writeSettings(t, testSettingsEnabled)

	// Install Claude Code hooks
	writeClaudeHooksFixture(t)

	if !checkClaudeCodeHooksInstalled() {
		t.Fatal("Expected Claude Code hooks to be installed before test")
	}

	// Deselect claude-code, select gemini instead
	selectFn := func(_ []string) ([]string, error) {
		return []string{string(agent.AgentNameGemini)}, nil
	}

	var buf bytes.Buffer
	err := runManageAgents(context.Background(), &buf, EnableOptions{}, selectFn)
	if err != nil {
		t.Fatalf("runManageAgents() error = %v", err)
	}

	output := buf.String()

	// Claude Code hooks should be removed
	if checkClaudeCodeHooksInstalled() {
		t.Error("Expected Claude Code hooks to be uninstalled after deselection")
	}

	if !strings.Contains(output, "Removed agents") {
		t.Errorf("Expected output to mention removed agents, got: %s", output)
	}
}

func TestManageAgents_DeselectAll_RemovesAllAndShowsGuidance(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	t.Setenv("TRACE_TEST_TTY", "1")
	writeSettings(t, testSettingsEnabled)
	writeClaudeHooksFixture(t)

	if !checkClaudeCodeHooksInstalled() {
		t.Fatal("Expected Claude Code hooks to be installed before test")
	}

	selectFn := func(_ []string) ([]string, error) {
		return []string{}, nil
	}

	var buf bytes.Buffer
	err := runManageAgents(context.Background(), &buf, EnableOptions{}, selectFn)
	if err != nil {
		t.Fatalf("runManageAgents() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "All agents have been removed.") {
		t.Errorf("Expected 'All agents have been removed.' message, got: %s", output)
	}
	if !strings.Contains(output, "trace agent add") {
		t.Errorf("Expected guidance on how to re-add agents, got: %s", output)
	}

	if checkClaudeCodeHooksInstalled() {
		t.Error("Expected Claude Code hooks to be uninstalled after deselecting all")
	}
}

func TestManageAgents_NoChanges(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	t.Setenv("TRACE_TEST_TTY", "1")
	writeSettings(t, testSettingsEnabled)
	writeClaudeHooksFixture(t)

	// Keep the same selection
	selectFn := func(_ []string) ([]string, error) {
		return []string{string(agent.AgentNameClaudeCode)}, nil
	}

	var buf bytes.Buffer
	err := runManageAgents(context.Background(), &buf, EnableOptions{}, selectFn)
	if err != nil {
		t.Fatalf("runManageAgents() error = %v", err)
	}

	if !strings.Contains(buf.String(), "No changes made.") {
		t.Errorf("Expected 'No changes made.' output, got: %s", buf.String())
	}
}

func TestManageAgents_NoChanges_StillPersistsVercelSetting(t *testing.T) {
	setupTestRepo(t)
	t.Setenv("TRACE_TEST_TTY", "1")
	writeSettings(t, testSettingsEnabled)
	writeClaudeHooksFixture(t)

	if err := os.WriteFile("vercel.json", []byte(`{
  "git": {
    "deploymentEnabled": {
      "trace/**": false
    }
  }
}`), 0o644); err != nil {
		t.Fatalf("write vercel.json: %v", err)
	}

	selectFn := func(_ []string) ([]string, error) {
		return []string{string(agent.AgentNameClaudeCode)}, nil
	}

	var buf bytes.Buffer
	err := runManageAgents(context.Background(), &buf, EnableOptions{}, selectFn)
	if err != nil {
		t.Fatalf("runManageAgents() error = %v", err)
	}

	if strings.Contains(buf.String(), "No changes made.") {
		t.Fatalf("did not expect no-op output when settings changed, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), ".trace/settings.json") {
		t.Fatalf("expected settings update output, got: %s", buf.String())
	}

	s, err := settings.Load(context.Background())
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if !s.Vercel {
		t.Fatal("expected vercel setting to be enabled")
	}
}

func TestManageAgents_ForceReinstallsSelectedAgentHooks(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	t.Setenv("TRACE_TEST_TTY", "1")
	writeSettings(t, testSettingsEnabled)
	writeClaudeHooksFixture(t)

	// Simulate a stale or locally modified Trace-managed Claude hook.
	modifiedHooksJSON := `{
		"hooks": {
			"Stop": [{"hooks": [{"type": "command", "command": "trace hooks claude-code stop --stale"}]}]
		}
	}`
	if err := os.WriteFile(".claude/settings.json", []byte(modifiedHooksJSON), 0o644); err != nil {
		t.Fatalf("Failed to mutate .claude/settings.json: %v", err)
	}

	selectFn := func(_ []string) ([]string, error) {
		return []string{string(agent.AgentNameClaudeCode)}, nil
	}

	var buf bytes.Buffer
	err := runManageAgents(context.Background(), &buf, EnableOptions{ForceHooks: true}, selectFn)
	if err != nil {
		t.Fatalf("runManageAgents() error = %v", err)
	}

	data, err := os.ReadFile(".claude/settings.json")
	if err != nil {
		t.Fatalf("Failed to read .claude/settings.json: %v", err)
	}
	content := string(data)

	if strings.Contains(content, "stop --stale") {
		t.Errorf("Expected force reinstall to rewrite stale Claude hook, got: %s", content)
	}
	if !strings.Contains(content, "trace hooks claude-code stop") {
		t.Errorf("Expected force reinstall to restore canonical Claude hook, got: %s", content)
	}
	if strings.Contains(buf.String(), "No changes made.") {
		t.Errorf("Force reinstall should not be treated as no-op, got: %s", buf.String())
	}
}

func TestManageAgents_ForceReportsReinstalledAgentsSeparately(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	t.Setenv("TRACE_TEST_TTY", "1")
	writeSettings(t, testSettingsEnabled)
	writeClaudeHooksFixture(t)

	selectFn := func(_ []string) ([]string, error) {
		return []string{string(agent.AgentNameClaudeCode)}, nil
	}

	var buf bytes.Buffer
	err := runManageAgents(context.Background(), &buf, EnableOptions{ForceHooks: true}, selectFn)
	if err != nil {
		t.Fatalf("runManageAgents() error = %v", err)
	}

	if !strings.Contains(buf.String(), "Reinstalled agents") {
		t.Errorf("Expected force reinstall summary to mention reinstalled agents, got: %s", buf.String())
	}
	if strings.Contains(buf.String(), "Added agents") {
		t.Errorf("Force reinstall should not be reported as added agents, got: %s", buf.String())
	}
}

func TestManageAgents_AddAndRemove(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	t.Setenv("TRACE_TEST_TTY", "1")
	writeSettings(t, testSettingsEnabled)

	// Install Claude Code hooks
	writeClaudeHooksFixture(t)

	// Deselect claude-code, add gemini
	selectFn := func(_ []string) ([]string, error) {
		return []string{string(agent.AgentNameGemini)}, nil
	}

	var buf bytes.Buffer
	err := runManageAgents(context.Background(), &buf, EnableOptions{}, selectFn)
	if err != nil {
		t.Fatalf("runManageAgents() error = %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Added agents") {
		t.Errorf("Expected 'Added agents' in output, got: %s", output)
	}
	if !strings.Contains(output, "Removed agents") {
		t.Errorf("Expected 'Removed agents' in output, got: %s", output)
	}

	// Verify hooks on disk: Claude removed, Gemini added
	if checkClaudeCodeHooksInstalled() {
		t.Error("Expected Claude Code hooks to be uninstalled after deselection")
	}
	if !checkGeminiCLIHooksInstalled() {
		t.Error("Expected Gemini CLI hooks to be installed after selection")
	}
}

func TestMaybePromptVercelDeploymentDisable_MergesExistingConfig(t *testing.T) {
	setupTestRepo(t)

	requireWriteFile := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	requireWriteFile("vercel.json", `{
  "cleanUrls": true,
  "git": {
    "deploymentEnabled": {
      "main": true
    }
  }
}`)

	var prompted bool
	var buf bytes.Buffer
	changed, err := maybePromptVercelDeploymentDisable(context.Background(), &buf, settings.TraceSettingsFile, func() (bool, error) {
		prompted = true
		return true, nil
	})
	if err != nil {
		t.Fatalf("maybePromptVercelDeploymentDisable() error = %v", err)
	}
	if !changed {
		t.Fatal("expected Vercel setting change")
	}
	if !prompted {
		t.Fatal("expected Vercel prompt to run")
	}

	projectSettings, err := settings.Load(context.Background())
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if !projectSettings.Vercel {
		t.Fatal("expected vercel setting to be enabled")
	}
}

func TestMaybePromptVercelDeploymentDisable_CreatesConfigWhenVercelDetected(t *testing.T) {
	setupTestRepo(t)

	if err := os.MkdirAll(".vercel", 0o755); err != nil {
		t.Fatalf("mkdir .vercel: %v", err)
	}

	var buf bytes.Buffer
	changed, err := maybePromptVercelDeploymentDisable(context.Background(), &buf, settings.TraceSettingsFile, func() (bool, error) {
		return true, nil
	})
	if err != nil {
		t.Fatalf("maybePromptVercelDeploymentDisable() error = %v", err)
	}
	if !changed {
		t.Fatal("expected Vercel setting change")
	}

	projectSettings, err := settings.Load(context.Background())
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if !projectSettings.Vercel {
		t.Fatal("expected vercel setting to be enabled")
	}
}

func TestMaybePromptVercelDeploymentDisable_SkipsPromptWhenAlreadyDisabledInVercelJSON(t *testing.T) {
	setupTestRepo(t)

	if err := os.WriteFile("vercel.json", []byte(`{
  "git": {
    "deploymentEnabled": {
      "trace/**": false
    }
  }
}`), 0o644); err != nil {
		t.Fatalf("write vercel.json: %v", err)
	}

	promptCalled := false
	var buf bytes.Buffer
	changed, err := maybePromptVercelDeploymentDisable(context.Background(), &buf, settings.TraceSettingsFile, func() (bool, error) {
		promptCalled = true
		return true, nil
	})
	if err != nil {
		t.Fatalf("maybePromptVercelDeploymentDisable() error = %v", err)
	}
	if !changed {
		t.Fatal("expected Vercel setting change from existing vercel.json")
	}
	if promptCalled {
		t.Fatal("expected Vercel prompt to be skipped when already configured")
	}
	if !strings.Contains(buf.String(), ".trace/settings.json") {
		t.Fatalf("expected settings update output, got %q", buf.String())
	}

	projectSettings, err := settings.Load(context.Background())
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if !projectSettings.Vercel {
		t.Fatal("expected vercel setting to be enabled from existing vercel.json")
	}
}

func TestMaybePromptVercelDeploymentDisable_WritesLocalSettingsWhenRequested(t *testing.T) {
	setupTestRepo(t)

	if err := os.MkdirAll(filepath.Dir(settings.TraceSettingsLocalFile), 0o755); err != nil {
		t.Fatalf("mkdir settings dir: %v", err)
	}
	if err := os.WriteFile("vercel.json", []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write vercel.json: %v", err)
	}

	var buf bytes.Buffer
	changed, err := maybePromptVercelDeploymentDisable(context.Background(), &buf, settings.TraceSettingsLocalFile, func() (bool, error) {
		return true, nil
	})
	if err != nil {
		t.Fatalf("maybePromptVercelDeploymentDisable() error = %v", err)
	}
	if !changed {
		t.Fatal("expected Vercel setting change")
	}
	if !strings.Contains(buf.String(), settings.TraceSettingsLocalFile) {
		t.Fatalf("expected local settings update output, got %q", buf.String())
	}

	localSettingsPath := filepath.Join(".", settings.TraceSettingsLocalFile)
	localSettings, err := settings.LoadFromFile(localSettingsPath)
	if err != nil {
		t.Fatalf("load local settings: %v", err)
	}
	if !localSettings.Vercel {
		t.Fatal("expected vercel setting in local settings")
	}

	projectSettingsPath := filepath.Join(".", settings.TraceSettingsFile)
	projectSettings, err := settings.LoadFromFile(projectSettingsPath)
	if err != nil {
		t.Fatalf("load project settings: %v", err)
	}
	if projectSettings.Vercel {
		t.Fatal("expected project settings to remain unchanged")
	}
}

func TestDetectOrSelectAgent_ReRun_NewlyDetectedAgentAvailableNotPreSelected(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	t.Setenv("TRACE_TEST_TTY", "1")

	// Simulate: Claude Code hooks installed from a previous run
	writeClaudeHooksFixture(t)

	// Simulate: user added .gemini directory since last enable (detected but not installed)
	if err := os.MkdirAll(".gemini", 0o755); err != nil {
		t.Fatalf("Failed to create .gemini directory: %v", err)
	}

	// Track which agents the selector receives
	var receivedAvailable []string
	selectFn := func(available []string) ([]string, error) {
		receivedAvailable = available
		// Only select the installed agent (simulate user not checking the new one)
		return []string{string(agent.AgentNameClaudeCode)}, nil
	}

	var buf bytes.Buffer
	agents, err := detectOrSelectAgent(context.Background(), &buf, selectFn)
	if err != nil {
		t.Fatalf("detectOrSelectAgent() error = %v", err)
	}

	// Should have prompted (re-run always prompts)
	if len(receivedAvailable) == 0 {
		t.Fatal("Expected interactive prompt on re-run")
	}

	// Newly detected agent should be available as an option
	if len(receivedAvailable) < 2 {
		t.Errorf("Expected at least 2 available agents (detected agent should be an option), got %d", len(receivedAvailable))
	}

	// Only the installed agent should be returned (user didn't select the new one)
	if len(agents) != 1 || agents[0].Name() != agent.AgentNameClaudeCode {
		t.Errorf("Expected only [claude-code], got %v", agents)
	}
}

func TestDetectOrSelectAgent_ReRun_EmptySelection_ReturnsError(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	t.Setenv("TRACE_TEST_TTY", "1")

	// Install Claude Code hooks (re-run scenario)
	writeClaudeHooksFixture(t)

	selectFn := func(_ []string) ([]string, error) {
		return []string{}, nil // user deselected everything
	}

	var buf bytes.Buffer
	_, err := detectOrSelectAgent(context.Background(), &buf, selectFn)
	if err == nil {
		t.Fatal("Expected error when no agents selected on re-run")
	}
	if !strings.Contains(err.Error(), "no agents selected") {
		t.Errorf("Expected 'no agents selected' error, got: %v", err)
	}
}

// Tests for configure --checkpoint-remote

func TestConfigureCmd_CheckpointRemote_UpdatesProjectSettings(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)

	cmd := newSetupCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--checkpoint-remote", "github:ashtom/zeugs-checkpoints"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure --checkpoint-remote failed: %v", err)
	}

	if !strings.Contains(stdout.String(), "Settings updated") {
		t.Errorf("expected 'Settings updated' output, got: %s", stdout.String())
	}

	// Verify the setting was written to settings.json
	s, err := settings.LoadFromFile(TraceSettingsFile)
	if err != nil {
		t.Fatalf("failed to load settings: %v", err)
	}
	remote := s.GetCheckpointRemote()
	if remote == nil {
		t.Fatal("expected checkpoint_remote to be set")
		return
	}
	if remote.Provider != "github" || remote.Repo != "ashtom/zeugs-checkpoints" {
		t.Errorf("unexpected checkpoint_remote: %+v", remote)
	}
}

func TestConfigureCmd_CheckpointRemote_WritesToLocalFile(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)

	cmd := newSetupCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--local", "--checkpoint-remote", "github:org/repo"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure --local --checkpoint-remote failed: %v", err)
	}

	if !strings.Contains(stdout.String(), "settings.local.json") {
		t.Errorf("expected output to reference settings.local.json, got: %s", stdout.String())
	}

	// Verify the setting was written to settings.local.json, not settings.json
	localS, err := settings.LoadFromFile(TraceSettingsLocalFile)
	if err != nil {
		t.Fatalf("failed to load local settings: %v", err)
	}
	remote := localS.GetCheckpointRemote()
	if remote == nil {
		t.Fatal("expected checkpoint_remote in local settings")
	}

	// Project settings should be unchanged
	projectS, err := settings.LoadFromFile(TraceSettingsFile)
	if err != nil {
		t.Fatalf("failed to load project settings: %v", err)
	}
	if projectS.GetCheckpointRemote() != nil {
		t.Error("checkpoint_remote should not leak into project settings")
	}
}

func TestConfigureCmd_CheckpointRemote_LocalOnlyRepo(t *testing.T) {
	setupTestRepo(t)
	// Only local settings exist — no settings.json
	writeLocalSettings(t, testSettingsEnabled)

	cmd := newSetupCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--checkpoint-remote", "github:org/repo"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure --checkpoint-remote on local-only repo failed: %v", err)
	}

	// Should NOT create settings.json
	if _, err := os.Stat(TraceSettingsFile); err == nil {
		t.Error("settings.json should not be created in a local-only repo")
	}

	// Should write to settings.local.json
	localS, err := settings.LoadFromFile(TraceSettingsLocalFile)
	if err != nil {
		t.Fatalf("failed to load local settings: %v", err)
	}
	if localS.GetCheckpointRemote() == nil {
		t.Error("expected checkpoint_remote in local settings")
	}
}

func TestConfigureCmd_CheckpointRemote_InvalidFormat(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)

	cmd := newSetupCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--checkpoint-remote", "invalid-format"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid --checkpoint-remote format")
	}
}

func TestConfigureCmd_CheckpointRemote_DoesNotLeakMergedSettings(t *testing.T) {
	setupTestRepo(t)
	// Project has enabled=true, local has log_level override
	writeSettings(t, testSettingsEnabled)
	writeLocalSettings(t, `{"log_level": "debug"}`)

	cmd := newSetupCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--project", "--checkpoint-remote", "github:org/repo"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure --project --checkpoint-remote failed: %v", err)
	}

	// Project settings should NOT contain log_level from local
	data, err := os.ReadFile(TraceSettingsFile)
	if err != nil {
		t.Fatalf("failed to read settings: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to parse settings: %v", err)
	}
	if _, exists := raw["log_level"]; exists {
		t.Error("log_level from local settings leaked into project settings")
	}
}

func stubCLIAvailable(t *testing.T) {
	t.Helper()
	orig := isSummaryCLIAvailable
	isSummaryCLIAvailable = func(types.AgentName) bool { return true }
	t.Cleanup(func() { isSummaryCLIAvailable = orig })
}

func TestConfigureCmd_SummarizeProvider_UpdatesProjectSettings(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)
	stubCLIAvailable(t)

	cmd := newSetupCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--summarize-provider", "codex", "--summarize-model", "gpt-5"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure --summarize-provider failed: %v", err)
	}

	if !strings.Contains(stdout.String(), "Settings updated") {
		t.Errorf("expected 'Settings updated' output, got: %s", stdout.String())
	}

	s, err := settings.LoadFromFile(TraceSettingsFile)
	if err != nil {
		t.Fatalf("failed to load settings: %v", err)
	}
	if s.SummaryGeneration == nil {
		t.Fatal("expected summary_generation to be set")
	}
	if s.SummaryGeneration.Provider != "codex" {
		t.Fatalf("summary provider = %q, want %q", s.SummaryGeneration.Provider, "codex")
	}
	if s.SummaryGeneration.Model != "gpt-5" {
		t.Fatalf("summary model = %q, want %q", s.SummaryGeneration.Model, "gpt-5")
	}
}

func TestConfigureCmd_SummarizeProvider_WritesToLocalFile(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)
	stubCLIAvailable(t)

	cmd := newSetupCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--local", "--summarize-provider", "claude-code", "--summarize-model", "sonnet"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure --local --summarize-provider failed: %v", err)
	}

	if !strings.Contains(stdout.String(), "settings.local.json") {
		t.Errorf("expected output to reference settings.local.json, got: %s", stdout.String())
	}

	localS, err := settings.LoadFromFile(TraceSettingsLocalFile)
	if err != nil {
		t.Fatalf("failed to load local settings: %v", err)
	}
	if localS.SummaryGeneration == nil {
		t.Fatal("expected local summary_generation to be set")
	}
	if localS.SummaryGeneration.Provider != "claude-code" {
		t.Fatalf("local summary provider = %q, want %q", localS.SummaryGeneration.Provider, "claude-code")
	}

	projectS, err := settings.LoadFromFile(TraceSettingsFile)
	if err != nil {
		t.Fatalf("failed to load project settings: %v", err)
	}
	if projectS.SummaryGeneration != nil {
		t.Fatal("summary_generation should not leak into project settings")
	}
}
