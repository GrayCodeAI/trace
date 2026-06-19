package strategy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/paths"
)

func TestRemoveGitHook_RemovesInstalledHooks(t *testing.T) {
	tmpDir, _ := initHooksTestRepo(t)

	// Install hooks first
	installCount, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("InstallGitHook() error = %v", err)
	}
	if installCount == 0 {
		t.Fatal("InstallGitHook() should install hooks")
	}

	// Verify hooks are installed
	if !IsGitHookInstalled(context.Background()) {
		t.Fatal("hooks should be installed before removal test")
	}

	// Remove hooks
	removeCount, err := RemoveGitHook(context.Background())
	if err != nil {
		t.Fatalf("RemoveGitHook(context.Background()) error = %v", err)
	}
	if removeCount != installCount {
		t.Errorf("RemoveGitHook(context.Background()) returned %d, want %d (same as installed)", removeCount, installCount)
	}

	// Verify hooks are removed
	if IsGitHookInstalled(context.Background()) {
		t.Error("hooks should not be installed after removal")
	}

	// Verify hook files no longer exist
	hooksDir := filepath.Join(tmpDir, ".git", "hooks")
	for _, hookName := range gitHookNames {
		hookPath := filepath.Join(hooksDir, hookName)
		if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
			t.Errorf("hook file %s should not exist after removal", hookName)
		}
	}
}

func TestRemoveGitHook_NoHooksInstalled(t *testing.T) {
	initHooksTestRepo(t)

	// Remove hooks when none are installed - should handle gracefully
	removeCount, err := RemoveGitHook(context.Background())
	if err != nil {
		t.Fatalf("RemoveGitHook(context.Background()) error = %v", err)
	}
	if removeCount != 0 {
		t.Errorf("RemoveGitHook(context.Background()) returned %d, want 0 (no hooks to remove)", removeCount)
	}
}

func TestRemoveGitHook_IgnoresNonTraceHooks(t *testing.T) {
	_, hooksDir := initHooksTestRepo(t)

	// Create a non-Trace hook manually
	customHookPath := filepath.Join(hooksDir, "pre-commit")
	customHookContent := "#!/bin/sh\necho 'custom hook'"
	if err := os.WriteFile(customHookPath, []byte(customHookContent), 0o755); err != nil {
		t.Fatalf("failed to create custom hook: %v", err)
	}

	// Remove hooks - should not remove the custom hook
	removeCount, err := RemoveGitHook(context.Background())
	if err != nil {
		t.Fatalf("RemoveGitHook(context.Background()) error = %v", err)
	}
	if removeCount != 0 {
		t.Errorf("RemoveGitHook(context.Background()) returned %d, want 0 (custom hook should not be removed)", removeCount)
	}

	// Verify custom hook still exists
	if _, err := os.Stat(customHookPath); os.IsNotExist(err) {
		t.Error("custom hook should still exist after RemoveGitHook(context.Background())")
	}
}

func TestRemoveGitHook_NotAGitRepo(t *testing.T) {
	// Create a temp directory without git init
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Clear cache so paths resolve correctly
	paths.ClearWorktreeRootCache()

	// Remove hooks in non-git directory - should return error
	_, err := RemoveGitHook(context.Background())
	if err == nil {
		t.Fatal("RemoveGitHook(context.Background()) should return error for non-git directory")
	}
}

func TestInstallGitHook_BacksUpCustomHook(t *testing.T) {
	_, hooksDir := initHooksTestRepo(t)

	// Create a custom prepare-commit-msg hook
	customHookPath := filepath.Join(hooksDir, "prepare-commit-msg")
	customContent := "#!/bin/sh\necho 'my custom hook'\n"
	if err := os.WriteFile(customHookPath, []byte(customContent), 0o755); err != nil {
		t.Fatalf("failed to create custom hook: %v", err)
	}

	count, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("InstallGitHook() error = %v", err)
	}
	if count == 0 {
		t.Error("InstallGitHook() should install hooks")
	}

	// Verify custom hook was backed up
	backupPath := customHookPath + backupSuffix
	backupData, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("backup file should exist at %s: %v", backupPath, err)
	}
	if string(backupData) != customContent {
		t.Errorf("backup content = %q, want %q", string(backupData), customContent)
	}

	// Verify installed hook has our marker and chain call
	hookData, err := os.ReadFile(customHookPath)
	if err != nil {
		t.Fatalf("hook file should exist: %v", err)
	}
	hookContent := string(hookData)
	if !strings.Contains(hookContent, traceHookMarker) {
		t.Error("installed hook should contain Trace marker")
	}
	if !strings.Contains(hookContent, chainComment) {
		t.Error("installed hook should contain chain call")
	}
	if !strings.Contains(hookContent, "prepare-commit-msg"+backupSuffix) {
		t.Error("chain call should reference the backup file")
	}
}

func TestManagedGitHookNames_IncludesPostRewrite(t *testing.T) {
	t.Parallel()

	names := ManagedGitHookNames()
	if !slices.Contains(names, "post-rewrite") {
		t.Fatalf("ManagedGitHookNames() = %v, want post-rewrite included", names)
	}
}

func TestInstallGitHook_InstallsPostRewrite(t *testing.T) {
	_, hooksDir := initHooksTestRepo(t)

	count, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("InstallGitHook() error = %v", err)
	}
	if count == 0 {
		t.Fatal("InstallGitHook() should install hooks")
	}

	hookPath := filepath.Join(hooksDir, "post-rewrite")
	hookData, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("post-rewrite hook should exist: %v", err)
	}

	hookContent := string(hookData)
	if !strings.Contains(hookContent, traceHookMarker) {
		t.Error("installed post-rewrite hook should contain Trace marker")
	}
	if !strings.Contains(hookContent, `trace hooks git post-rewrite "$1" 2>>".git/trace-hooks.log" || true`) {
		t.Errorf("installed post-rewrite hook content missing expected command:\n%s", hookContent)
	}
}

func TestInstallGitHook_DoesNotOverwriteExistingBackup(t *testing.T) {
	_, hooksDir := initHooksTestRepo(t)

	// Create a backup file manually (simulating a previous backup)
	firstBackupContent := "#!/bin/sh\necho 'first custom hook'\n"
	backupPath := filepath.Join(hooksDir, "prepare-commit-msg"+backupSuffix)
	if err := os.WriteFile(backupPath, []byte(firstBackupContent), 0o755); err != nil {
		t.Fatalf("failed to create backup: %v", err)
	}

	// Create a second custom hook at the standard path
	secondCustomContent := "#!/bin/sh\necho 'second custom hook'\n"
	hookPath := filepath.Join(hooksDir, "prepare-commit-msg")
	if err := os.WriteFile(hookPath, []byte(secondCustomContent), 0o755); err != nil {
		t.Fatalf("failed to create second custom hook: %v", err)
	}

	_, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("InstallGitHook() error = %v", err)
	}

	// Verify the original backup was NOT overwritten
	backupData, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("backup should still exist: %v", err)
	}
	if string(backupData) != firstBackupContent {
		t.Errorf("backup content = %q, want original %q", string(backupData), firstBackupContent)
	}

	// Verify our hook was installed with chain call
	hookData, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("hook should exist: %v", err)
	}
	if !strings.Contains(string(hookData), traceHookMarker) {
		t.Error("hook should contain Trace marker")
	}
	if !strings.Contains(string(hookData), chainComment) {
		t.Error("hook should contain chain call since backup exists")
	}
}

func TestInstallGitHook_IdempotentWithChaining(t *testing.T) {
	_, hooksDir := initHooksTestRepo(t)

	// Create a custom hook, then install
	customHookPath := filepath.Join(hooksDir, "prepare-commit-msg")
	if err := os.WriteFile(customHookPath, []byte("#!/bin/sh\necho custom\n"), 0o755); err != nil {
		t.Fatalf("failed to create custom hook: %v", err)
	}

	firstCount, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("first InstallGitHook() error = %v", err)
	}
	if firstCount == 0 {
		t.Error("first install should install hooks")
	}

	// Re-install should return 0 (idempotent)
	secondCount, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("second InstallGitHook() error = %v", err)
	}
	if secondCount != 0 {
		t.Errorf("second InstallGitHook() = %d, want 0 (idempotent)", secondCount)
	}
}

func TestInstallGitHook_NoBackupWhenNoExistingHook(t *testing.T) {
	_, hooksDir := initHooksTestRepo(t)

	_, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("InstallGitHook() error = %v", err)
	}

	// No .pre-trace files should exist
	for _, hook := range gitHookNames {
		backupPath := filepath.Join(hooksDir, hook+backupSuffix)
		if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
			t.Errorf("backup %s should not exist for fresh install", hook+backupSuffix)
		}

		// Hook should not contain chain call
		data, err := os.ReadFile(filepath.Join(hooksDir, hook))
		if err != nil {
			t.Fatalf("hook %s should exist: %v", hook, err)
		}
		if strings.Contains(string(data), chainComment) {
			t.Errorf("hook %s should not contain chain call for fresh install", hook)
		}
	}
}

func TestInstallGitHook_MixedHooks(t *testing.T) {
	_, hooksDir := initHooksTestRepo(t)

	// Only create custom hooks for some hooks
	customHooks := map[string]string{
		"prepare-commit-msg": "#!/bin/sh\necho 'custom pcm'\n",
		"pre-push":           "#!/bin/sh\necho 'custom prepush'\n",
	}
	for name, content := range customHooks {
		hookPath := filepath.Join(hooksDir, name)
		if err := os.WriteFile(hookPath, []byte(content), 0o755); err != nil {
			t.Fatalf("failed to create %s: %v", name, err)
		}
	}

	_, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("InstallGitHook() error = %v", err)
	}

	// Hooks with pre-existing content should have backups and chain calls
	for name := range customHooks {
		backupPath := filepath.Join(hooksDir, name+backupSuffix)
		if _, err := os.Stat(backupPath); os.IsNotExist(err) {
			t.Errorf("backup for %s should exist", name)
		}

		data, err := os.ReadFile(filepath.Join(hooksDir, name))
		if err != nil {
			t.Fatalf("hook %s should exist: %v", name, err)
		}
		if !strings.Contains(string(data), chainComment) {
			t.Errorf("hook %s should contain chain call", name)
		}
	}

	// Hooks without pre-existing content should NOT have backups or chain calls
	noCustom := []string{"commit-msg", "post-commit"}
	for _, name := range noCustom {
		backupPath := filepath.Join(hooksDir, name+backupSuffix)
		if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
			t.Errorf("backup for %s should NOT exist", name)
		}

		data, err := os.ReadFile(filepath.Join(hooksDir, name))
		if err != nil {
			t.Fatalf("hook %s should exist: %v", name, err)
		}
		if strings.Contains(string(data), chainComment) {
			t.Errorf("hook %s should NOT contain chain call", name)
		}
	}
}

func TestRemoveGitHook_RestoresBackup(t *testing.T) {
	_, hooksDir := initHooksTestRepo(t)

	// Create a custom hook, install (backs it up), then remove
	customContent := "#!/bin/sh\necho 'my custom hook'\n"
	hookPath := filepath.Join(hooksDir, "prepare-commit-msg")
	if err := os.WriteFile(hookPath, []byte(customContent), 0o755); err != nil {
		t.Fatalf("failed to create custom hook: %v", err)
	}

	_, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("InstallGitHook() error = %v", err)
	}

	removed, err := RemoveGitHook(context.Background())
	if err != nil {
		t.Fatalf("RemoveGitHook(context.Background()) error = %v", err)
	}
	if removed == 0 {
		t.Error("RemoveGitHook(context.Background()) should remove hooks")
	}

	// Original custom hook should be restored
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("hook should be restored: %v", err)
	}
	if string(data) != customContent {
		t.Errorf("restored hook content = %q, want %q", string(data), customContent)
	}

	// Backup should be gone
	backupPath := hookPath + backupSuffix
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Error("backup should be removed after restore")
	}
}

func TestRemoveGitHook_RestoresBackupWhenHookAlreadyGone(t *testing.T) {
	_, hooksDir := initHooksTestRepo(t)

	// Create custom hook, install (creates backup), then delete the main hook
	customContent := "#!/bin/sh\necho 'original'\n"
	hookPath := filepath.Join(hooksDir, "prepare-commit-msg")
	if err := os.WriteFile(hookPath, []byte(customContent), 0o755); err != nil {
		t.Fatalf("failed to create custom hook: %v", err)
	}

	_, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("InstallGitHook() error = %v", err)
	}

	// Simulate another tool deleting our hook
	if err := os.Remove(hookPath); err != nil {
		t.Fatalf("failed to remove hook: %v", err)
	}

	_, err = RemoveGitHook(context.Background())
	if err != nil {
		t.Fatalf("RemoveGitHook(context.Background()) error = %v", err)
	}

	// Backup should be restored even though the main hook was already gone
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal("backup should be restored to main hook path")
	}
	if string(data) != customContent {
		t.Errorf("restored hook content = %q, want %q", string(data), customContent)
	}

	// Backup file should be gone
	backupPath := hookPath + backupSuffix
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Error("backup file should not exist after restore")
	}
}

func TestGenerateChainedContent(t *testing.T) {
	t.Parallel()

	base := "#!/bin/sh\n# Trace CLI hooks\ntrace hooks git pre-push \"$1\" || true\n"
	result := generateChainedContent(base, "pre-push")

	// Should start with the base content
	if !strings.HasPrefix(result, base) {
		t.Error("chained content should start with base content")
	}

	// Should contain the chain comment
	if !strings.Contains(result, chainComment) {
		t.Error("chained content should contain chain comment")
	}

	// Should resolve hook directory from $0
	if !strings.Contains(result, `_trace_hook_dir="$(dirname "$0")"`) {
		t.Error("chained content should resolve hook directory from $0")
	}

	// Should check executable permission on backup
	expectedCheck := `[ -x "$_trace_hook_dir/pre-push` + backupSuffix + `" ]`
	if !strings.Contains(result, expectedCheck) {
		t.Errorf("chained content should check -x on backup, got:\n%s", result)
	}

	// Should forward all arguments with "$@"
	expectedExec := `"$_trace_hook_dir/pre-push` + backupSuffix + `" "$@"`
	if !strings.Contains(result, expectedExec) {
		t.Errorf("chained content should execute backup with $@, got:\n%s", result)
	}
}

func TestGenerateChainedContent_PostRewritePreservesStdinForBackup(t *testing.T) {
	t.Parallel()

	base := "#!/bin/sh\n# Trace CLI hooks\n# Post-rewrite hook: remap session linkage after amend/rebase rewrites\ntrace hooks git post-rewrite \"$1\" 2>/dev/null || true\n"
	result := generateChainedContent(base, "post-rewrite")

	if !strings.Contains(result, `_trace_stdin="$(mktemp "${TMPDIR:-/tmp}/trace-post-rewrite.XXXXXX")"`) {
		t.Fatalf("post-rewrite chained content should create temp stdin copy, got:\n%s", result)
	}
	if !strings.Contains(result, `cat > "$_trace_stdin"`) {
		t.Fatalf("post-rewrite chained content should capture stdin once, got:\n%s", result)
	}
	if !strings.Contains(result, `trace hooks git post-rewrite "$1" < "$_trace_stdin" 2>/dev/null || true`) {
		t.Fatalf("post-rewrite chained content should replay stdin into Trace handler, got:\n%s", result)
	}
	if !strings.Contains(result, `"$_trace_hook_dir/post-rewrite`+backupSuffix+`" "$@" < "$_trace_stdin"`) {
		t.Fatalf("post-rewrite chained content should replay stdin into backup hook, got:\n%s", result)
	}
}

func TestInstallGitHook_InstallRemoveReinstall(t *testing.T) {
	_, hooksDir := initHooksTestRepo(t)

	// Create a custom hook
	customContent := "#!/bin/sh\necho 'user hook'\n"
	hookPath := filepath.Join(hooksDir, "prepare-commit-msg")
	if err := os.WriteFile(hookPath, []byte(customContent), 0o755); err != nil {
		t.Fatalf("failed to create custom hook: %v", err)
	}

	// Install: should back up and chain
	count, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("first install error: %v", err)
	}
	if count == 0 {
		t.Error("first install should install hooks")
	}
	backupPath := hookPath + backupSuffix
	if !fileExists(backupPath) {
		t.Fatal("backup should exist after install")
	}

	// Remove: should restore backup
	_, err = RemoveGitHook(context.Background())
	if err != nil {
		t.Fatalf("remove error: %v", err)
	}
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal("hook should be restored after remove")
	}
	if string(data) != customContent {
		t.Errorf("restored hook = %q, want %q", string(data), customContent)
	}
	if fileExists(backupPath) {
		t.Error("backup should not exist after remove")
	}

	// Reinstall: should back up again and chain
	count, err = InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("reinstall error: %v", err)
	}
	if count == 0 {
		t.Error("reinstall should install hooks")
	}
	if !fileExists(backupPath) {
		t.Fatal("backup should exist after reinstall")
	}
	data, err = os.ReadFile(hookPath)
	if err != nil {
		t.Fatal("hook should exist after reinstall")
	}
	if !strings.Contains(string(data), traceHookMarker) {
		t.Error("reinstalled hook should contain Trace marker")
	}
	if !strings.Contains(string(data), chainComment) {
		t.Error("reinstalled hook should contain chain call")
	}
}

func TestRemoveGitHook_DoesNotOverwriteReplacedHook(t *testing.T) {
	_, hooksDir := initHooksTestRepo(t)

	// User has custom hook A
	hookPath := filepath.Join(hooksDir, "prepare-commit-msg")
	hookAContent := "#!/bin/sh\necho 'hook A'\n"
	if err := os.WriteFile(hookPath, []byte(hookAContent), 0o755); err != nil {
		t.Fatalf("failed to create hook A: %v", err)
	}

	// trace enable: backs up A, installs our hook with chain
	_, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("InstallGitHook() error = %v", err)
	}

	// User replaces our hook with their own hook B
	hookBContent := "#!/bin/sh\necho 'hook B'\n"
	if err := os.WriteFile(hookPath, []byte(hookBContent), 0o755); err != nil {
		t.Fatalf("failed to create hook B: %v", err)
	}

	// trace disable: should NOT overwrite hook B with backup A
	_, err = RemoveGitHook(context.Background())
	if err != nil {
		t.Fatalf("RemoveGitHook(context.Background()) error = %v", err)
	}

	// Hook B should still be in place
	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal("hook should still exist")
	}
	if string(data) != hookBContent {
		t.Errorf("hook content = %q, want hook B %q (should not be overwritten by backup)", string(data), hookBContent)
	}

	// Backup should still exist (not consumed)
	backupPath := hookPath + backupSuffix
	if !fileExists(backupPath) {
		t.Error("backup should be left in place when hook was modified")
	}
}

func TestRemoveGitHook_PermissionDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("Test cannot run as root (permission checks are bypassed)")
	}

	tmpDir, _ := initHooksTestRepo(t)

	// Install hooks first
	_, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("InstallGitHook() error = %v", err)
	}

	// Remove write permissions from hooks directory to cause permission error
	hooksDir := filepath.Join(tmpDir, ".git", "hooks")
	if err := os.Chmod(hooksDir, 0o555); err != nil {
		t.Fatalf("failed to change hooks dir permissions: %v", err)
	}
	// Restore permissions on cleanup
	t.Cleanup(func() {
		_ = os.Chmod(hooksDir, 0o755) //nolint:errcheck // Cleanup, best-effort
	})

	// Remove hooks should now fail with permission error
	removed, err := RemoveGitHook(context.Background())
	if err == nil {
		t.Fatal("RemoveGitHook(context.Background()) should return error when hooks cannot be deleted")
	}
	if removed != 0 {
		t.Errorf("RemoveGitHook(context.Background()) removed %d hooks, expected 0 when all fail", removed)
	}
	if !strings.Contains(err.Error(), "failed to remove hooks") {
		t.Errorf("error should mention 'failed to remove hooks', got: %v", err)
	}
}
