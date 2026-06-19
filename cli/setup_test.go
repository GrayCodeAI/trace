package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/GrayCodeAI/trace/cli/agent/claudecode"
	_ "github.com/GrayCodeAI/trace/cli/agent/geminicli"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/session"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cli/testutil"
)

// Note: Tests for hook manipulation functions (addHookToMatcher, hookCommandExists, etc.)
// have been moved to the agent/claudecode package where these functions now reside.
// See cli/agent/claudecode/hooks_test.go for those tests.

// setupTestDir creates a temp directory, changes to it, and returns it.
// It also registers cleanup to restore the original directory.
func setupTestDir(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	hideExternalAgentsFromPath(t)
	t.Chdir(tmpDir)
	paths.ClearWorktreeRootCache()
	session.ClearGitCommonDirCache()
	return tmpDir
}

// setupTestRepo creates a temp directory with a git repo initialized.
func setupTestRepo(t *testing.T) {
	t.Helper()
	tmpDir := setupTestDir(t)
	testutil.InitRepo(t, tmpDir)
}

// writeSettings writes settings content to the settings file.
func writeSettings(t *testing.T, content string) {
	t.Helper()
	settingsDir := filepath.Dir(TraceSettingsFile)
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatalf("Failed to create settings dir: %v", err)
	}
	if err := os.WriteFile(TraceSettingsFile, []byte(content), 0o644); err != nil {
		t.Fatalf("Failed to write settings file: %v", err)
	}
}

func hideExternalAgentsFromPath(t *testing.T) {
	t.Helper()

	pathDir := t.TempDir()
	for _, name := range []string{"git", "sh"} {
		if err := preserveToolOnPath(name, pathDir); err != nil {
			t.Fatalf("preserve %s on PATH: %v", name, err)
		}
	}

	t.Setenv("PATH", pathDir)
}

func TestSetupTestDir_HidesExternalAgentsButKeepsGitAvailable(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}

	sharedDir := t.TempDir()
	if err := copyExecutable(gitPath, filepath.Join(sharedDir, "git")); err != nil {
		t.Fatalf("copy git executable: %v", err)
	}
	writeExternalAgentBinary(t, sharedDir, "ext-shared-dir")
	t.Setenv("PATH", sharedDir)

	setupTestDir(t)

	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("expected git to remain available after test PATH isolation: %v", err)
	}
	if _, err := exec.LookPath("trace-agent-ext-shared-dir"); err == nil {
		t.Fatal("expected external agent to be hidden from PATH")
	}
}

func preserveToolOnPath(name, dstDir string) error {
	src, err := exec.LookPath(name)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil
		}
		return err
	}

	return copyExecutable(src, filepath.Join(dstDir, filepath.Base(src)))
}

func copyExecutable(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}

	if err := os.Symlink(src, dst); err == nil {
		return nil
	}
	if err := os.Link(src, dst); err == nil {
		return nil
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	return os.WriteFile(dst, data, info.Mode())
}

func writeExternalAgentBinary(t *testing.T, dir, name string) {
	t.Helper()

	script := `#!/bin/sh
case "$1" in
  info)
    echo '{"protocol_version":1,"name":"` + name + `","type":"` + name + ` Agent","description":"External test agent","is_preview":false,"protected_dirs":[],"hook_names":["stop"],"capabilities":{"hooks":true}}'
    ;;
  detect)
    if [ "$TRACE_TEST_EXTERNAL_PRESENT" = "1" ]; then
      echo '{"present": true}'
    else
      echo '{"present": false}'
    fi
    ;;
  install-hooks)
    echo '{"hooks_installed": 1}'
    ;;
  uninstall-hooks)
    exit 0
    ;;
  are-hooks-installed)
    echo '{"installed": false}'
    ;;
  *)
    echo '{}'
    ;;
esac
`

	if err := os.WriteFile(filepath.Join(dir, "trace-agent-"+name), []byte(script), 0o755); err != nil {
		t.Fatalf("Failed to write external agent binary: %v", err)
	}
}

func writeExternalSummaryAgentBinary(t *testing.T, dir, name string) {
	t.Helper()

	script := `#!/bin/sh
case "$1" in
  info)
    echo '{"protocol_version":1,"name":"` + name + `","type":"` + name + ` Agent","description":"External summary test agent","is_preview":false,"protected_dirs":[],"hook_names":[],"capabilities":{"hooks":false,"transcript_analyzer":false,"transcript_preparer":false,"token_calculator":false,"compact_transcript":false,"text_generator":true,"hook_response_writer":false,"subagent_aware_extractor":false}}'
    ;;
  detect)
    echo '{"present": true}'
    ;;
  generate-text)
    echo '{"text":"{\"intent\":\"Intent\",\"outcome\":\"Outcome\",\"learnings\":{\"repo\":[],\"code\":[],\"workflow\":[]},\"friction\":[],\"open_items\":[]}"}'
    ;;
  *)
    echo '{}'
    ;;
esac
`

	if err := os.WriteFile(filepath.Join(dir, "trace-agent-"+name), []byte(script), 0o755); err != nil {
		t.Fatalf("Failed to write external summary agent binary: %v", err)
	}
}

func TestRunEnable(t *testing.T) {
	setupTestDir(t)
	writeSettings(t, testSettingsDisabled)

	var stdout bytes.Buffer
	if err := runEnable(context.Background(), &stdout, false); err != nil {
		t.Fatalf("runEnable() error = %v", err)
	}

	if !strings.Contains(stdout.String(), "enabled") {
		t.Errorf("Expected output to contain 'enabled', got: %s", stdout.String())
	}

	enabled, err := IsEnabled(context.Background())
	if err != nil {
		t.Fatalf("IsEnabled(context.Background()) error = %v", err)
	}
	if !enabled {
		t.Error("Trace should be enabled after running enable command")
	}
}

func TestRunEnable_AlreadyEnabled(t *testing.T) {
	setupTestDir(t)
	writeSettings(t, testSettingsEnabled)

	var stdout bytes.Buffer
	if err := runEnable(context.Background(), &stdout, false); err != nil {
		t.Fatalf("runEnable() error = %v", err)
	}

	if !strings.Contains(stdout.String(), "enabled") {
		t.Errorf("Expected output to mention enabled state, got: %s", stdout.String())
	}
}

// TestRunEnable_ProjectFlag_ClearsLocalDisable verifies that `trace enable --project`
// after `trace disable` (which writes to local) actually re-enables by updating both files.
func TestRunEnable_ProjectFlag_ClearsLocalDisable(t *testing.T) {
	setupTestDir(t)
	writeSettings(t, testSettingsEnabled)

	// Simulate `trace disable` (writes enabled:false to local)
	var buf bytes.Buffer
	if err := runDisable(context.Background(), &buf, false); err != nil {
		t.Fatalf("runDisable() error = %v", err)
	}

	// Verify it's disabled
	enabled, err := IsEnabled(context.Background())
	if err != nil {
		t.Fatalf("IsEnabled() error = %v", err)
	}
	if enabled {
		t.Fatal("Expected disabled after runDisable")
	}

	// Now re-enable with --project flag
	buf.Reset()
	if err := runEnable(context.Background(), &buf, true); err != nil {
		t.Fatalf("runEnable(project=true) error = %v", err)
	}

	// Must actually be enabled — local override must not win
	enabled, err = IsEnabled(context.Background())
	if err != nil {
		t.Fatalf("IsEnabled() error = %v", err)
	}
	if !enabled {
		t.Error("Expected enabled after runEnable --project, but IsEnabled() returned false (local override not cleared)")
	}
}

// TestRunEnable_DefaultFlag_ClearsLocalDisable verifies that `trace enable`
// (default, no --project) after `trace disable` actually re-enables.
func TestRunEnable_DefaultFlag_ClearsLocalDisable(t *testing.T) {
	setupTestDir(t)
	writeSettings(t, testSettingsEnabled)

	// Simulate `trace disable` (writes enabled:false to local)
	var buf bytes.Buffer
	if err := runDisable(context.Background(), &buf, false); err != nil {
		t.Fatalf("runDisable() error = %v", err)
	}

	// Now re-enable with default (no --project)
	buf.Reset()
	if err := runEnable(context.Background(), &buf, false); err != nil {
		t.Fatalf("runEnable(project=false) error = %v", err)
	}

	enabled, err := IsEnabled(context.Background())
	if err != nil {
		t.Fatalf("IsEnabled() error = %v", err)
	}
	if !enabled {
		t.Error("Expected enabled after runEnable, but IsEnabled() returned false")
	}
}

func TestRunDisable(t *testing.T) {
	setupTestDir(t)
	writeSettings(t, testSettingsEnabled)

	var stdout bytes.Buffer
	if err := runDisable(context.Background(), &stdout, false); err != nil {
		t.Fatalf("runDisable() error = %v", err)
	}

	if !strings.Contains(stdout.String(), "disabled") {
		t.Errorf("Expected output to contain 'disabled', got: %s", stdout.String())
	}

	enabled, err := IsEnabled(context.Background())
	if err != nil {
		t.Fatalf("IsEnabled(context.Background()) error = %v", err)
	}
	if enabled {
		t.Error("Trace should be disabled after running disable command")
	}
}

func TestRunDisable_AlreadyDisabled(t *testing.T) {
	setupTestDir(t)
	writeSettings(t, testSettingsDisabled)

	var stdout bytes.Buffer
	if err := runDisable(context.Background(), &stdout, false); err != nil {
		t.Fatalf("runDisable() error = %v", err)
	}

	if !strings.Contains(stdout.String(), "disabled") {
		t.Errorf("Expected output to mention disabled state, got: %s", stdout.String())
	}
}

func TestCheckDisabledGuard(t *testing.T) {
	setupTestDir(t)

	// No settings file - should not be disabled (defaults to enabled)
	var stdout bytes.Buffer
	if checkDisabledGuard(context.Background(), &stdout) {
		t.Error("checkDisabledGuard() should return false when no settings file exists")
	}
	if stdout.String() != "" {
		t.Errorf("checkDisabledGuard() should not print anything when enabled, got: %s", stdout.String())
	}

	// Settings with enabled: true
	writeSettings(t, testSettingsEnabled)
	stdout.Reset()
	if checkDisabledGuard(context.Background(), &stdout) {
		t.Error("checkDisabledGuard() should return false when enabled")
	}

	// Settings with enabled: false
	writeSettings(t, testSettingsDisabled)
	stdout.Reset()
	if !checkDisabledGuard(context.Background(), &stdout) {
		t.Error("checkDisabledGuard() should return true when disabled")
	}
	output := stdout.String()
	if !strings.Contains(output, "Trace is disabled") {
		t.Errorf("Expected disabled message, got: %s", output)
	}
	if !strings.Contains(output, "trace enable") {
		t.Errorf("Expected message to mention 'trace enable', got: %s", output)
	}
}

// writeLocalSettings writes settings content to the local settings file.
func writeLocalSettings(t *testing.T, content string) {
	t.Helper()
	settingsDir := filepath.Dir(TraceSettingsLocalFile)
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatalf("Failed to create settings dir: %v", err)
	}
	if err := os.WriteFile(TraceSettingsLocalFile, []byte(content), 0o644); err != nil {
		t.Fatalf("Failed to write local settings file: %v", err)
	}
}

func TestRunDisable_WithLocalSettings(t *testing.T) {
	setupTestDir(t)
	// Create both settings files with enabled: true
	writeSettings(t, testSettingsEnabled)
	writeLocalSettings(t, `{"enabled": true}`)

	var stdout bytes.Buffer
	if err := runDisable(context.Background(), &stdout, false); err != nil {
		t.Fatalf("runDisable() error = %v", err)
	}

	// Should be disabled because runDisable updates local settings when it exists
	enabled, err := IsEnabled(context.Background())
	if err != nil {
		t.Fatalf("IsEnabled(context.Background()) error = %v", err)
	}
	if enabled {
		t.Error("Trace should be disabled after running disable command (local settings should be updated)")
	}

	// Verify local settings file was updated
	localContent, err := os.ReadFile(TraceSettingsLocalFile)
	if err != nil {
		t.Fatalf("Failed to read local settings: %v", err)
	}
	if !strings.Contains(string(localContent), `"enabled":false`) && !strings.Contains(string(localContent), `"enabled": false`) {
		t.Errorf("Local settings should have enabled:false, got: %s", localContent)
	}
}

func TestRunDisable_WithProjectFlag(t *testing.T) {
	setupTestDir(t)
	// Create both settings files with enabled: true
	writeSettings(t, testSettingsEnabled)
	writeLocalSettings(t, `{"enabled": true}`)

	var stdout bytes.Buffer
	// Use --project flag (useProjectSettings = true)
	if err := runDisable(context.Background(), &stdout, true); err != nil {
		t.Fatalf("runDisable() error = %v", err)
	}

	// Verify project settings file was updated (not local)
	projectContent, err := os.ReadFile(TraceSettingsFile)
	if err != nil {
		t.Fatalf("Failed to read project settings: %v", err)
	}
	if !strings.Contains(string(projectContent), `"enabled":false`) && !strings.Contains(string(projectContent), `"enabled": false`) {
		t.Errorf("Project settings should have enabled:false, got: %s", projectContent)
	}

	// Local settings should also be updated to stay in sync
	localContent, err := os.ReadFile(TraceSettingsLocalFile)
	if err != nil {
		t.Fatalf("Failed to read local settings: %v", err)
	}
	if !strings.Contains(string(localContent), `"enabled":false`) && !strings.Contains(string(localContent), `"enabled": false`) {
		t.Errorf("Local settings should also have enabled:false to stay in sync, got: %s", localContent)
	}
}

// TestRunDisable_CreatesLocalSettingsWhenMissing verifies that running
// `trace disable` without --project creates settings.local.json when it
// doesn't exist, rather than writing to settings.json.
func TestRunDisable_CreatesLocalSettingsWhenMissing(t *testing.T) {
	setupTestDir(t)
	// Only create project settings (no local settings)
	writeSettings(t, testSettingsEnabled)

	var stdout bytes.Buffer
	if err := runDisable(context.Background(), &stdout, false); err != nil {
		t.Fatalf("runDisable() error = %v", err)
	}

	// Should be disabled
	enabled, err := IsEnabled(context.Background())
	if err != nil {
		t.Fatalf("IsEnabled(context.Background()) error = %v", err)
	}
	if enabled {
		t.Error("Trace should be disabled after running disable command")
	}

	// Local settings file should be created with enabled:false
	localContent, err := os.ReadFile(TraceSettingsLocalFile)
	if err != nil {
		t.Fatalf("Local settings file should have been created: %v", err)
	}
	if !strings.Contains(string(localContent), `"enabled":false`) && !strings.Contains(string(localContent), `"enabled": false`) {
		t.Errorf("Local settings should have enabled:false, got: %s", localContent)
	}

	// Project settings should remain unchanged (still enabled)
	projectContent, err := os.ReadFile(TraceSettingsFile)
	if err != nil {
		t.Fatalf("Failed to read project settings: %v", err)
	}
	if !strings.Contains(string(projectContent), `"enabled":true`) && !strings.Contains(string(projectContent), `"enabled": true`) {
		t.Errorf("Project settings should still have enabled:true, got: %s", projectContent)
	}
}

func TestDetermineSettingsTarget_ExplicitLocalFlag(t *testing.T) {
	tmpDir := t.TempDir()

	// Create settings.json
	settingsPath := filepath.Join(tmpDir, paths.SettingsFileName)
	if err := os.WriteFile(settingsPath, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("Failed to create settings file: %v", err)
	}

	// With --local flag, should always use local
	useLocal, showNotification := determineSettingsTarget(tmpDir, true, false)
	if !useLocal {
		t.Error("determineSettingsTarget() should return useLocal=true with --local flag")
	}
	if showNotification {
		t.Error("determineSettingsTarget() should not show notification with explicit --local flag")
	}
}

func TestDetermineSettingsTarget_ExplicitProjectFlag(t *testing.T) {
	tmpDir := t.TempDir()

	// Create settings.json
	settingsPath := filepath.Join(tmpDir, paths.SettingsFileName)
	if err := os.WriteFile(settingsPath, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("Failed to create settings file: %v", err)
	}

	// With --project flag, should always use project
	useLocal, showNotification := determineSettingsTarget(tmpDir, false, true)
	if useLocal {
		t.Error("determineSettingsTarget() should return useLocal=false with --project flag")
	}
	if showNotification {
		t.Error("determineSettingsTarget() should not show notification with explicit --project flag")
	}
}

func TestDetermineSettingsTarget_SettingsExists_NoFlags(t *testing.T) {
	tmpDir := t.TempDir()

	// Create settings.json
	settingsPath := filepath.Join(tmpDir, paths.SettingsFileName)
	if err := os.WriteFile(settingsPath, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("Failed to create settings file: %v", err)
	}

	// Without flags, should auto-redirect to local with notification
	useLocal, showNotification := determineSettingsTarget(tmpDir, false, false)
	if !useLocal {
		t.Error("determineSettingsTarget() should return useLocal=true when settings.json exists")
	}
	if !showNotification {
		t.Error("determineSettingsTarget() should show notification when auto-redirecting to local")
	}
}

func TestDetermineSettingsTarget_SettingsNotExists_NoFlags(t *testing.T) {
	tmpDir := t.TempDir()

	// No settings.json exists

	// Should use project settings (create new)
	useLocal, showNotification := determineSettingsTarget(tmpDir, false, false)
	if useLocal {
		t.Error("determineSettingsTarget() should return useLocal=false when settings.json doesn't exist")
	}
	if showNotification {
		t.Error("determineSettingsTarget() should not show notification when creating new settings")
	}
}

// Tests for runUninstall and helper functions

func TestRunUninstall_Force_NothingInstalled(t *testing.T) {
	setupTestRepo(t)

	var stdout, stderr bytes.Buffer
	err := runUninstall(context.Background(), &stdout, &stderr, true)
	if err != nil {
		t.Fatalf("runUninstall() error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "not installed") {
		t.Errorf("Expected output to indicate nothing installed, got: %s", output)
	}
}

func TestRunUninstall_Force_RemovesTraceDirectory(t *testing.T) {
	setupTestRepo(t)

	// Create .trace directory with settings
	writeSettings(t, testSettingsEnabled)

	// Verify directory exists
	traceDir := paths.TraceDir
	if _, err := os.Stat(traceDir); os.IsNotExist(err) {
		t.Fatal(".trace directory should exist before uninstall")
	}

	var stdout, stderr bytes.Buffer
	err := runUninstall(context.Background(), &stdout, &stderr, true)
	if err != nil {
		t.Fatalf("runUninstall() error = %v", err)
	}

	// Verify directory is removed
	if _, err := os.Stat(traceDir); !os.IsNotExist(err) {
		t.Error(".trace directory should be removed after uninstall")
	}

	output := stdout.String()
	if !strings.Contains(output, "uninstalled successfully") {
		t.Errorf("Expected success message, got: %s", output)
	}
}

func TestRunUninstall_Force_RemovesGitHooks(t *testing.T) {
	setupTestRepo(t)

	// Create .trace directory (required for git hooks)
	writeSettings(t, testSettingsEnabled)

	// Install git hooks
	if _, err := strategy.InstallGitHook(context.Background(), true, false, false); err != nil {
		t.Fatalf("InstallGitHook() error = %v", err)
	}

	// Verify hooks are installed
	if !strategy.IsGitHookInstalled(context.Background()) {
		t.Fatal("git hooks should be installed before uninstall")
	}

	var stdout, stderr bytes.Buffer
	err := runUninstall(context.Background(), &stdout, &stderr, true)
	if err != nil {
		t.Fatalf("runUninstall() error = %v", err)
	}

	// Verify hooks are removed
	if strategy.IsGitHookInstalled(context.Background()) {
		t.Error("git hooks should be removed after uninstall")
	}

	output := stdout.String()
	if !strings.Contains(output, "Removed git hooks") {
		t.Errorf("Expected output to mention removed git hooks, got: %s", output)
	}
}

func TestRunUninstall_NotAGitRepo(t *testing.T) {
	// Create a temp directory without git init
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	paths.ClearWorktreeRootCache()

	var stdout, stderr bytes.Buffer
	err := runUninstall(context.Background(), &stdout, &stderr, true)

	// Should return an error (silent error)
	if err == nil {
		t.Fatal("runUninstall() should return error for non-git directory")
	}

	// Should print message to stderr
	errOutput := stderr.String()
	if !strings.Contains(errOutput, "Not a git repository") {
		t.Errorf("Expected error message about not being a git repo, got: %s", errOutput)
	}
}

func TestCheckTraceDirExists(t *testing.T) {
	setupTestDir(t)

	// Should be false when directory doesn't exist
	if checkTraceDirExists(context.Background()) {
		t.Error("checkTraceDirExists(context.Background()) should return false when .trace doesn't exist")
	}

	// Create the directory
	if err := os.MkdirAll(paths.TraceDir, 0o755); err != nil {
		t.Fatalf("Failed to create .trace dir: %v", err)
	}

	// Should be true now
	if !checkTraceDirExists(context.Background()) {
		t.Error("checkTraceDirExists(context.Background()) should return true when .trace exists")
	}
}

func TestCountSessionStates(t *testing.T) {
	setupTestRepo(t)

	// Should be 0 when no session states exist
	count := countSessionStates(context.Background())
	if count != 0 {
		t.Errorf("countSessionStates(context.Background()) = %d, want 0", count)
	}
}

func TestCountShadowBranches(t *testing.T) {
	setupTestRepo(t)

	// Should be 0 when no shadow branches exist
	count := countShadowBranches(context.Background())
	if count != 0 {
		t.Errorf("countShadowBranches(context.Background()) = %d, want 0", count)
	}
}

func TestRemoveTraceDirectory(t *testing.T) {
	setupTestDir(t)

	// Create .trace directory with some files
	traceDir := paths.TraceDir
	if err := os.MkdirAll(filepath.Join(traceDir, "subdir"), 0o755); err != nil {
		t.Fatalf("Failed to create .trace/subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(traceDir, "test.txt"), []byte("test"), 0o644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Remove the directory
	if err := removeTraceDirectory(context.Background()); err != nil {
		t.Fatalf("removeTraceDirectory(context.Background()) error = %v", err)
	}

	// Verify it's removed
	if _, err := os.Stat(traceDir); !os.IsNotExist(err) {
		t.Error(".trace directory should be removed")
	}
}

func TestShellCompletionTarget(t *testing.T) {
	tests := []struct {
		name             string
		shell            string
		createBashProf   bool
		wantShell        string
		wantRCBase       string // basename of rc file
		wantCompletion   string
		wantErrUnsupport bool
	}{
		{
			name:           "zsh",
			shell:          "/bin/zsh",
			wantShell:      "Zsh",
			wantRCBase:     ".zshrc",
			wantCompletion: "autoload -Uz compinit && compinit && source <(trace completion zsh)",
		},
		{
			name:           "bash_no_profile",
			shell:          "/bin/bash",
			wantShell:      "Bash",
			wantRCBase:     ".bashrc",
			wantCompletion: "source <(trace completion bash)",
		},
		{
			name:           "bash_with_profile",
			shell:          "/bin/bash",
			createBashProf: true,
			wantShell:      "Bash",
			wantRCBase:     ".bash_profile",
			wantCompletion: "source <(trace completion bash)",
		},
		{
			name:           "fish",
			shell:          "/usr/bin/fish",
			wantShell:      "Fish",
			wantRCBase:     filepath.Join(".config", "fish", "config.fish"),
			wantCompletion: "trace completion fish | source",
		},
		{
			name:             "empty_shell",
			shell:            "",
			wantErrUnsupport: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("SHELL", tt.shell)

			if tt.createBashProf {
				if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte(""), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			shellName, rcFile, completion, err := shellCompletionTarget()

			if tt.wantErrUnsupport {
				if !errors.Is(err, errUnsupportedShell) {
					t.Fatalf("got err=%v, want errUnsupportedShell", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if shellName != tt.wantShell {
				t.Errorf("shellName = %q, want %q", shellName, tt.wantShell)
			}
			wantRC := filepath.Join(home, tt.wantRCBase)
			if rcFile != wantRC {
				t.Errorf("rcFile = %q, want %q", rcFile, wantRC)
			}
			if completion != tt.wantCompletion {
				t.Errorf("completion = %q, want %q", completion, tt.wantCompletion)
			}
		})
	}
}
