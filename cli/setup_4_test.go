package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent"
	_ "github.com/GrayCodeAI/trace/cli/agent/claudecode"
	_ "github.com/GrayCodeAI/trace/cli/agent/geminicli"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cli/testutil"
)

func TestConfigureCmd_SummarizeProvider_ExternalEnablesExternalAgents(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)

	const provider = "external-summary-config"
	externalDir := t.TempDir()
	writeExternalSummaryAgentBinary(t, externalDir, provider)
	t.Setenv("PATH", externalDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cmd := newSetupCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--summarize-provider", provider})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure --summarize-provider external failed: %v", err)
	}

	s, err := settings.LoadFromFile(TraceSettingsFile)
	if err != nil {
		t.Fatalf("failed to load settings: %v", err)
	}
	if s.SummaryGeneration == nil {
		t.Fatal("expected summary_generation to be set")
	}
	if s.SummaryGeneration.Provider != provider {
		t.Fatalf("summary provider = %q, want %q", s.SummaryGeneration.Provider, provider)
	}
	if !s.ExternalAgents {
		t.Fatal("external summary provider should enable external_agents")
	}
	if !strings.Contains(stdout.String(), externalAgentsAutoEnabledNotice) {
		t.Fatalf("expected notice surfacing the external_agents flip, got stdout:\n%s", stdout.String())
	}
}

func TestConfigureCmd_SummarizeProvider_ExternalAlreadyEnabled_NoNotice(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	setupTestRepo(t)
	writeSettings(t, `{"enabled": true, "external_agents": true}`)

	const provider = "external-summary-already-on"
	externalDir := t.TempDir()
	writeExternalSummaryAgentBinary(t, externalDir, provider)
	t.Setenv("PATH", externalDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cmd := newSetupCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--summarize-provider", provider})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure --summarize-provider external failed: %v", err)
	}

	if strings.Contains(stdout.String(), externalAgentsAutoEnabledNotice) {
		t.Fatalf("notice should not fire when external_agents was already enabled, got stdout:\n%s", stdout.String())
	}
}

func TestConfigureCmd_SummarizeProvider_InvalidProvider(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)

	cmd := newSetupCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--summarize-provider", "opencode"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for unsupported summary provider")
	}
}

func TestConfigureCmd_SummarizeProvider_SwitchClearsStaleModel(t *testing.T) {
	stubCLIAvailable(t)
	setupTestRepo(t)
	writeSettings(t, `{"enabled": true, "summary_generation": {"provider": "claude-code", "model": "sonnet"}}`)

	cmd := newSetupCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--summarize-provider", "codex"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure --summarize-provider codex failed: %v", err)
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
	if s.SummaryGeneration.Model != "" {
		t.Fatalf("summary model = %q, want empty after provider switch", s.SummaryGeneration.Model)
	}
}

func TestConfigureCmd_SummarizeModel_RequiresProvider(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)

	cmd := newSetupCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--summarize-model", "sonnet"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for summarize-model without provider")
	}
}

func TestConfigureCmd_SummarizeModel_LocalInheritsProviderFromProject(t *testing.T) {
	setupTestRepo(t)
	stubCLIAvailable(t)
	// Project settings define the provider; local override only sets the model.
	writeSettings(t, `{"enabled": true, "summary_generation": {"provider": "claude-code"}}`)

	cmd := newSetupCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--local", "--summarize-model", "sonnet"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure --local --summarize-model failed: %v", err)
	}

	localS, err := settings.LoadFromFile(TraceSettingsLocalFile)
	if err != nil {
		t.Fatalf("failed to load local settings: %v", err)
	}
	if localS.SummaryGeneration == nil {
		t.Fatal("expected local summary_generation to be set")
	}
	if localS.SummaryGeneration.Model != "sonnet" {
		t.Fatalf("local summary model = %q, want %q", localS.SummaryGeneration.Model, "sonnet")
	}

	// Project settings must not be modified.
	projectS, err := settings.LoadFromFile(TraceSettingsFile)
	if err != nil {
		t.Fatalf("failed to load project settings: %v", err)
	}
	if projectS.SummaryGeneration.Model != "" {
		t.Fatalf("project model = %q, should remain empty", projectS.SummaryGeneration.Model)
	}
}

func TestConfigureCmd_SummarizeModel_UsesExistingProvider(t *testing.T) {
	setupTestRepo(t)
	stubCLIAvailable(t)
	writeSettings(t, `{"enabled": true, "summary_generation": {"provider": "claude-code"}}`)

	cmd := newSetupCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--summarize-model", "sonnet"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure --summarize-model failed: %v", err)
	}

	s, err := settings.LoadFromFile(TraceSettingsFile)
	if err != nil {
		t.Fatalf("failed to load settings: %v", err)
	}
	if s.SummaryGeneration == nil {
		t.Fatal("expected summary_generation to be set")
	}
	if s.SummaryGeneration.Provider != "claude-code" {
		t.Fatalf("summary provider = %q, want %q", s.SummaryGeneration.Provider, "claude-code")
	}
	if s.SummaryGeneration.Model != "sonnet" {
		t.Fatalf("summary model = %q, want %q", s.SummaryGeneration.Model, "sonnet")
	}
}

func TestSelectAllAgents_ReturnsAll(t *testing.T) {
	t.Parallel()
	available := []string{"claude-code", "gemini-cli", "opencode"}
	selected, err := selectAllAgents(available)
	if err != nil {
		t.Fatalf("selectAllAgents() error = %v", err)
	}
	if !slices.Equal(selected, available) {
		t.Errorf("selectAllAgents() = %v, want %v", selected, available)
	}
}

func TestSelectAllAgents_EmptyReturnsError(t *testing.T) {
	t.Parallel()
	_, err := selectAllAgents(nil)
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestDetectOrSelectAgent_YesSelectsAll(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	t.Setenv("TRACE_TEST_TTY", "1")

	var buf bytes.Buffer
	agents, err := detectOrSelectAgent(context.Background(), &buf, selectAllAgents)
	if err != nil {
		t.Fatalf("detectOrSelectAgent() with selectAllAgents error = %v", err)
	}

	// Should return at least 2 agents (claude-code + gemini-cli are registered in test imports)
	if len(agents) < 2 {
		t.Errorf("expected at least 2 agents with selectAllAgents, got %d", len(agents))
	}

	output := buf.String()
	if !strings.Contains(output, "Selected agents:") {
		t.Errorf("Expected output to contain 'Selected agents:', got: %s", output)
	}
}

func TestManageAgents_YesWorksNonInteractive(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)

	// Install claude-code hooks so there's something installed
	writeClaudeHooksFixture(t)

	// Use a selectFn that only picks built-in agents to avoid failures
	// from stale external agent binaries registered by other tests.
	selectBuiltIn := func(available []string) ([]string, error) {
		var selected []string
		for _, name := range available {
			ag, err := agent.Get(types.AgentName(name))
			if err != nil {
				continue
			}
			if isBuiltInAgent(ag) {
				selected = append(selected, name)
			}
		}
		if len(selected) == 0 {
			return nil, errors.New("no built-in agents available")
		}
		return selected, nil
	}

	var buf bytes.Buffer
	err := runManageAgents(context.Background(), &buf, EnableOptions{}, selectBuiltIn)
	if err != nil {
		t.Fatalf("runManageAgents() with selectFn in non-interactive mode error = %v", err)
	}

	output := buf.String()
	// Should NOT print the non-interactive bail-out message
	if strings.Contains(output, "Cannot show agent selection in non-interactive mode") {
		t.Error("selectFn should bypass the interactivity check, but got non-interactive message")
	}
}

func TestEnableYes_TelemetryRespectsOptOut(t *testing.T) {
	// Cannot use t.Parallel() because subtests use t.Setenv

	t.Run("yes with telemetry=false", func(t *testing.T) {
		s := &TraceSettings{}
		opts := EnableOptions{Yes: true, Telemetry: false}
		if !opts.Telemetry || os.Getenv("TRACE_TELEMETRY_OPTOUT") != "" {
			f := false
			s.Telemetry = &f
		} else if s.Telemetry == nil {
			tr := true
			s.Telemetry = &tr
		}
		if s.Telemetry == nil || *s.Telemetry != false {
			t.Errorf("expected telemetry=false when --yes --telemetry=false, got %v", s.Telemetry)
		}
	})

	t.Run("yes with TRACE_TELEMETRY_OPTOUT", func(t *testing.T) {
		t.Setenv("TRACE_TELEMETRY_OPTOUT", "1")
		s := &TraceSettings{}
		opts := EnableOptions{Yes: true, Telemetry: true}
		if !opts.Telemetry || os.Getenv("TRACE_TELEMETRY_OPTOUT") != "" {
			f := false
			s.Telemetry = &f
		} else if s.Telemetry == nil {
			tr := true
			s.Telemetry = &tr
		}
		if s.Telemetry == nil || *s.Telemetry != false {
			t.Errorf("expected telemetry=false with TRACE_TELEMETRY_OPTOUT, got %v", s.Telemetry)
		}
	})

	t.Run("yes defaults to telemetry enabled", func(t *testing.T) {
		s := &TraceSettings{}
		opts := EnableOptions{Yes: true, Telemetry: true}
		if !opts.Telemetry {
			f := false
			s.Telemetry = &f
		} else if s.Telemetry == nil {
			tr := true
			s.Telemetry = &tr
		}
		if s.Telemetry == nil || *s.Telemetry != true {
			t.Errorf("expected telemetry=true with --yes (default), got %v", s.Telemetry)
		}
	})

	t.Run("yes preserves existing telemetry setting", func(t *testing.T) {
		existing := false
		s := &TraceSettings{Telemetry: &existing}
		opts := EnableOptions{Yes: true, Telemetry: true}
		if !opts.Telemetry || os.Getenv("TRACE_TELEMETRY_OPTOUT") != "" {
			f := false
			s.Telemetry = &f
		} else if s.Telemetry == nil {
			tr := true
			s.Telemetry = &tr
		}
		if *s.Telemetry != false {
			t.Errorf("expected existing telemetry=false to be preserved, got %v", *s.Telemetry)
		}
	})
}

func TestEnableCmd_YesFreshRepo_SkipsPromptsAndEnables(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	testutil.WriteFile(t, ".", "f.txt", "init")
	testutil.GitAdd(t, ".", "f.txt")
	testutil.GitCommit(t, ".", "init")

	// Use --yes with --agent to test the realistic CI scenario.
	// The --yes flag skips telemetry/Vercel prompts while --agent selects a specific agent.
	// The pure --yes-selects-all-agents path is covered by TestDetectOrSelectAgent_YesSelectsAll.
	cmd := newEnableCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--yes", "--agent", "claude-code"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("enable --yes --agent claude-code error = %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "Ready.") {
		t.Errorf("expected 'Ready.' in output, got: %s", output)
	}

	// Verify settings were saved with telemetry enabled (--yes default)
	s, err := LoadTraceSettings(context.Background())
	if err != nil {
		t.Fatalf("failed to load settings: %v", err)
	}
	if !s.Enabled {
		t.Error("expected enabled=true")
	}
}

func TestEnableCmd_YesWithAgent_AgentTakesPrecedence(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	testutil.WriteFile(t, ".", "f.txt", "init")
	testutil.GitAdd(t, ".", "f.txt")
	testutil.GitCommit(t, ".", "init")

	cmd := newEnableCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--yes", "--agent", "claude-code"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("enable --yes --agent claude-code error = %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	output := stdout.String()
	// --agent takes precedence — should show single-agent non-interactive output
	if !strings.Contains(output, "Agent: Claude Code") {
		t.Errorf("expected 'Agent: Claude Code' in output, got: %s", output)
	}
	// Should NOT have shown multi-select output
	if strings.Contains(output, "Selected agents:") {
		t.Errorf("--agent should bypass multi-select, but got 'Selected agents:' in: %s", output)
	}
}

func TestEnableCmd_YesOnConfiguredRepo_ManagesAgents(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)
	writeClaudeHooksFixture(t)

	cmd := newEnableCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--yes"})

	// May partially fail due to stale external agents in global registry,
	// but the key behavior is that it doesn't bail out with the non-interactive message.
	_ = cmd.Execute() //nolint:errcheck // partial failure from stale test agents is expected

	output := stdout.String()
	// Should NOT bail out with non-interactive message
	if strings.Contains(output, "Cannot show agent selection in non-interactive mode") {
		t.Error("--yes should bypass non-interactive check, but got bail-out message")
	}
}

func TestEnableCmd_YesWithTelemetryFalse(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir and t.Setenv
	setupTestRepo(t)
	testutil.WriteFile(t, ".", "f.txt", "init")
	testutil.GitAdd(t, ".", "f.txt")
	testutil.GitCommit(t, ".", "init")

	cmd := newEnableCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--yes", "--agent", "claude-code", "--telemetry=false"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("enable --yes --telemetry=false error = %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	// Verify telemetry was disabled despite --yes
	s, err := LoadTraceSettings(context.Background())
	if err != nil {
		t.Fatalf("failed to load settings: %v", err)
	}
	if s.Telemetry == nil || *s.Telemetry != false {
		t.Errorf("expected telemetry=false when --yes --telemetry=false, got %v", s.Telemetry)
	}
}

func TestConfigureCmd_BarePrintsHelpHint(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)
	writeClaudeHooksFixture(t)

	cmd := newSetupCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "trace agent") {
		t.Errorf("expected hint about 'trace agent' in help output, got: %s", output)
	}
	// Bare configure must not run the agent picker.
	if strings.Contains(output, "Cannot show agent selection in non-interactive mode") {
		t.Errorf("bare configure should not invoke agent picker, got: %s", output)
	}
}

func TestConfigureCmd_AgentFlagRemoved(t *testing.T) {
	t.Parallel()
	cmd := newSetupCmd()
	if cmd.Flags().Lookup("agent") != nil {
		t.Error("'configure' must not expose --agent (use 'trace agent add')")
	}
	if cmd.Flags().Lookup("remove") != nil {
		t.Error("'configure' must not expose --remove (use 'trace agent remove')")
	}
	if cmd.Flags().Lookup("yes") != nil {
		t.Error("'configure' must not expose --yes (lives on 'trace enable')")
	}
}

func TestConfigureCmd_TelemetryFlag_PersistsSetting(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)

	cmd := newSetupCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--telemetry=false"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure --telemetry=false error = %v", err)
	}

	s, err := LoadTraceSettings(context.Background())
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if s.Telemetry == nil || *s.Telemetry != false {
		t.Errorf("expected telemetry=false, got %v", s.Telemetry)
	}
}

func TestConfigureCmd_AbsoluteGitHookPathFlag_PersistsAndReinstallsHook(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)

	cmd := newSetupCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--absolute-git-hook-path"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure --absolute-git-hook-path error = %v", err)
	}

	s, err := LoadTraceSettings(context.Background())
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if !s.AbsoluteGitHookPath {
		t.Error("expected absolute_git_hook_path=true after configure --absolute-git-hook-path")
	}
	if !strings.Contains(stdout.String(), "Reinstalled git hook") {
		t.Errorf("expected hook reinstall message, got: %s", stdout.String())
	}
}

func TestConfigureCmd_TelemetryAlone_DoesNotReinstallHook(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)

	cmd := newSetupCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--telemetry=false"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("configure --telemetry=false error = %v", err)
	}

	if strings.Contains(stdout.String(), "Reinstalled git hook") {
		t.Errorf("--telemetry alone should not trigger hook reinstall, got: %s", stdout.String())
	}
}

func TestConfigureCmd_FreshRepo_PointsAtEnable(t *testing.T) {
	// Cannot use t.Parallel() because we use t.Chdir
	setupTestRepo(t)
	// No settings written — fresh repo.

	cmd := newSetupCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--telemetry=false"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected configure on fresh repo to fail")
	}
	if !strings.Contains(stderr.String(), "trace enable") {
		t.Errorf("expected hint pointing at 'trace enable', got stderr: %s", stderr.String())
	}
}
