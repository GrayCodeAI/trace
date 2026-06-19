package strategy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/paths"
)

// clearGlobalHooksPath overrides any global core.hooksPath setting so that
// test repos use their default .git/hooks directory. Setting the local value
// takes precedence over the global one.
func clearGlobalHooksPath(t *testing.T, repoDir string) {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", "config", "--local", "core.hooksPath", filepath.Join(repoDir, ".git", "hooks"))
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set local core.hooksPath: %v", err)
	}
}

// initHooksTestRepo creates a temporary git repository, changes to it, and clears
// the repo root cache. Returns the repo directory path and the hooks directory path.
func initHooksTestRepo(t *testing.T) (string, string) {
	t.Helper()
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}
	clearGlobalHooksPath(t, tmpDir)
	paths.ClearWorktreeRootCache()

	return tmpDir, filepath.Join(tmpDir, ".git", "hooks")
}

func TestGetGitDirInPath_RegularRepo(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	result, err := getGitDirInPath(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := filepath.Join(tmpDir, ".git")

	// Resolve symlinks for comparison (macOS /var -> /private/var)
	resultResolved, err := filepath.EvalSymlinks(result)
	if err != nil {
		t.Fatalf("failed to resolve symlinks for result: %v", err)
	}
	expectedResolved, err := filepath.EvalSymlinks(expected)
	if err != nil {
		t.Fatalf("failed to resolve symlinks for expected: %v", err)
	}

	if resultResolved != expectedResolved {
		t.Errorf("expected %s, got %s", expectedResolved, resultResolved)
	}
}

func TestGetGitDirInPath_Worktree(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	mainRepo := filepath.Join(tmpDir, "main")
	worktreeDir := filepath.Join(tmpDir, "worktree")

	// Initialize main repo
	if err := os.MkdirAll(mainRepo, 0o755); err != nil {
		t.Fatalf("failed to create main repo dir: %v", err)
	}

	ctx := context.Background()

	cmd := exec.CommandContext(ctx, "git", "init")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init main repo: %v", err)
	}
	clearGlobalHooksPath(t, mainRepo)

	// Configure git user for the commit
	cmd = exec.CommandContext(ctx, "git", "config", "user.email", "test@test.com")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to configure git email: %v", err)
	}

	cmd = exec.CommandContext(ctx, "git", "config", "user.name", "Test User")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to configure git name: %v", err)
	}

	// Disable GPG signing for test commits
	cmd = exec.CommandContext(ctx, "git", "config", "commit.gpgsign", "false")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to configure commit.gpgsign: %v", err)
	}

	// Create an initial commit (required for worktree)
	testFile := filepath.Join(mainRepo, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	cmd = exec.CommandContext(ctx, "git", "add", ".")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git add: %v", err)
	}

	cmd = exec.CommandContext(ctx, "git", "commit", "-m", "initial")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git commit: %v", err)
	}

	// Create a worktree
	cmd = exec.CommandContext(ctx, "git", "worktree", "add", worktreeDir, "-b", "feature")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create worktree: %v", err)
	}

	// Test that getGitDirInPath works in the worktree
	result, err := getGitDirInPath(context.Background(), worktreeDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Resolve symlinks for comparison (macOS /var -> /private/var)
	resultResolved, err := filepath.EvalSymlinks(result)
	if err != nil {
		t.Fatalf("failed to resolve symlinks for result: %v", err)
	}
	expectedPrefix, err := filepath.EvalSymlinks(filepath.Join(mainRepo, ".git", "worktrees"))
	if err != nil {
		t.Fatalf("failed to resolve symlinks for expected prefix: %v", err)
	}

	// The git dir for a worktree should be inside main repo's .git/worktrees/
	if !strings.HasPrefix(resultResolved, expectedPrefix) {
		t.Errorf("expected git dir to be under %s, got %s", expectedPrefix, resultResolved)
	}
}

func TestGetGitDirInPath_NotARepo(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	_, err := getGitDirInPath(context.Background(), tmpDir)
	if err == nil {
		t.Fatal("expected error for non-repo directory, got nil")
	}

	expectedMsg := "not a git repository"
	if err.Error() != expectedMsg {
		t.Errorf("expected error message %q, got %q", expectedMsg, err.Error())
	}
}

func TestGetHooksDirInPath_RegularRepo(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}
	clearGlobalHooksPath(t, tmpDir)

	result, err := getHooksDirInPath(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := filepath.Join(tmpDir, ".git", "hooks")

	resultResolved, err := filepath.EvalSymlinks(result)
	if err != nil {
		t.Fatalf("failed to resolve symlinks for result: %v", err)
	}
	expectedResolved, err := filepath.EvalSymlinks(expected)
	if err != nil {
		t.Fatalf("failed to resolve symlinks for expected: %v", err)
	}

	if resultResolved != expectedResolved {
		t.Errorf("expected %s, got %s", expectedResolved, resultResolved)
	}
}

func TestGetHooksDirInPath_Worktree(t *testing.T) {
	t.Parallel()

	mainRepo, worktreeDir := initHooksWorktreeRepo(t)

	result, err := getHooksDirInPath(context.Background(), worktreeDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := filepath.Join(mainRepo, ".git", "hooks")

	resultResolved, err := filepath.EvalSymlinks(result)
	if err != nil {
		t.Fatalf("failed to resolve symlinks for result: %v", err)
	}
	expectedResolved, err := filepath.EvalSymlinks(expected)
	if err != nil {
		t.Fatalf("failed to resolve symlinks for expected: %v", err)
	}

	// In a linked worktree, hooks should resolve to the common hooks dir.
	if resultResolved != expectedResolved {
		t.Errorf("expected hooks dir %s, got %s", expectedResolved, resultResolved)
	}
}

func TestGetHooksDirInPath_CoreHooksPath(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ctx := context.Background()

	cmd := exec.CommandContext(ctx, "git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Relative core.hooksPath should resolve relative to repo root.
	cmd = exec.CommandContext(ctx, "git", "config", "core.hooksPath", ".githooks")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set relative core.hooksPath: %v", err)
	}
	relativeResult, err := getHooksDirInPath(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("unexpected error for relative hooks path: %v", err)
	}
	relativeExpected := filepath.Join(tmpDir, ".githooks")
	if filepath.Clean(relativeResult) != filepath.Clean(relativeExpected) {
		t.Errorf("relative core.hooksPath expected %s, got %s", relativeExpected, relativeResult)
	}

	// Absolute core.hooksPath should be returned unchanged.
	absHooksPath := filepath.Join(tmpDir, "abs-hooks")
	cmd = exec.CommandContext(ctx, "git", "config", "core.hooksPath", absHooksPath)
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set absolute core.hooksPath: %v", err)
	}
	absoluteResult, err := getHooksDirInPath(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("unexpected error for absolute hooks path: %v", err)
	}
	if filepath.Clean(absoluteResult) != filepath.Clean(absHooksPath) {
		t.Errorf("absolute core.hooksPath expected %s, got %s", absHooksPath, absoluteResult)
	}
}

func TestInstallGitHook_WorktreeInstallsInCommonHooks(t *testing.T) {
	mainRepo, worktreeDir := initHooksWorktreeRepo(t)
	t.Chdir(worktreeDir)
	paths.ClearWorktreeRootCache()

	count, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("InstallGitHook() in worktree failed: %v", err)
	}
	if count == 0 {
		t.Fatal("InstallGitHook() should install hooks in worktree")
	}

	// Hooks should be installed in common .git/hooks, not in .git/worktrees/<name>/hooks.
	commonHooksDir := filepath.Join(mainRepo, ".git", "hooks")
	for _, hook := range gitHookNames {
		data, readErr := os.ReadFile(filepath.Join(commonHooksDir, hook))
		if readErr != nil {
			t.Fatalf("expected common hook %s to exist: %v", hook, readErr)
		}
		if !strings.Contains(string(data), traceHookMarker) {
			t.Errorf("common hook %s should contain Trace marker", hook)
		}
	}

	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	cmd.Dir = worktreeDir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("failed to get worktree git dir: %v", err)
	}
	worktreeGitDir := strings.TrimSpace(string(output))
	if !filepath.IsAbs(worktreeGitDir) {
		worktreeGitDir = filepath.Join(worktreeDir, worktreeGitDir)
	}
	for _, hook := range gitHookNames {
		wtHookPath := filepath.Join(worktreeGitDir, "hooks", hook)
		if data, readErr := os.ReadFile(wtHookPath); readErr == nil && strings.Contains(string(data), traceHookMarker) {
			t.Errorf("worktree-local hook %s should not contain Trace marker (should install in common hooks dir)", hook)
		}
	}

	if !IsGitHookInstalledInDir(context.Background(), worktreeDir) {
		t.Error("IsGitHookInstalledInDir(worktree) should be true after install")
	}
}

func initHooksWorktreeRepo(t *testing.T) (string, string) {
	t.Helper()

	tmpDir := t.TempDir()
	mainRepo := filepath.Join(tmpDir, "main")
	worktreeDir := filepath.Join(tmpDir, "worktree")

	if err := os.MkdirAll(mainRepo, 0o755); err != nil {
		t.Fatalf("failed to create main repo dir: %v", err)
	}

	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "git", "init")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init main repo: %v", err)
	}
	clearGlobalHooksPath(t, mainRepo)

	cmd = exec.CommandContext(ctx, "git", "config", "user.email", "test@test.com")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to configure git email: %v", err)
	}

	cmd = exec.CommandContext(ctx, "git", "config", "user.name", "Test User")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to configure git name: %v", err)
	}

	cmd = exec.CommandContext(ctx, "git", "config", "commit.gpgsign", "false")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to configure commit.gpgsign: %v", err)
	}

	testFile := filepath.Join(mainRepo, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	cmd = exec.CommandContext(ctx, "git", "add", ".")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git add: %v", err)
	}

	cmd = exec.CommandContext(ctx, "git", "commit", "-m", "initial")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git commit: %v", err)
	}

	cmd = exec.CommandContext(ctx, "git", "worktree", "add", worktreeDir, "-b", "feature")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create worktree: %v", err)
	}

	return mainRepo, worktreeDir
}

// isGitSequenceOperation tests use t.Chdir() so cannot call t.Parallel().

func TestIsGitSequenceOperation_NoOperation(t *testing.T) {
	initHooksTestRepo(t)

	if isGitSequenceOperation(context.Background()) {
		t.Error("isGitSequenceOperation(context.Background()) = true, want false for clean repo")
	}
}

func TestIsGitSequenceOperation_RebaseMerge(t *testing.T) {
	tmpDir, _ := initHooksTestRepo(t)

	if err := os.MkdirAll(filepath.Join(tmpDir, ".git", "rebase-merge"), 0o755); err != nil {
		t.Fatalf("failed to create rebase-merge dir: %v", err)
	}

	if !isGitSequenceOperation(context.Background()) {
		t.Error("isGitSequenceOperation(context.Background()) = false, want true during rebase-merge")
	}
}

func TestIsGitSequenceOperation_RebaseApply(t *testing.T) {
	tmpDir, _ := initHooksTestRepo(t)

	if err := os.MkdirAll(filepath.Join(tmpDir, ".git", "rebase-apply"), 0o755); err != nil {
		t.Fatalf("failed to create rebase-apply dir: %v", err)
	}

	if !isGitSequenceOperation(context.Background()) {
		t.Error("isGitSequenceOperation(context.Background()) = false, want true during rebase-apply")
	}
}

func TestIsGitSequenceOperation_CherryPick(t *testing.T) {
	tmpDir, _ := initHooksTestRepo(t)

	if err := os.WriteFile(filepath.Join(tmpDir, ".git", "CHERRY_PICK_HEAD"), []byte("abc123"), 0o644); err != nil {
		t.Fatalf("failed to create CHERRY_PICK_HEAD: %v", err)
	}

	if !isGitSequenceOperation(context.Background()) {
		t.Error("isGitSequenceOperation(context.Background()) = false, want true during cherry-pick")
	}
}

func TestIsGitSequenceOperation_Revert(t *testing.T) {
	tmpDir, _ := initHooksTestRepo(t)

	if err := os.WriteFile(filepath.Join(tmpDir, ".git", "REVERT_HEAD"), []byte("abc123"), 0o644); err != nil {
		t.Fatalf("failed to create REVERT_HEAD: %v", err)
	}

	if !isGitSequenceOperation(context.Background()) {
		t.Error("isGitSequenceOperation(context.Background()) = false, want true during revert")
	}
}

func TestIsGitSequenceOperation_Worktree(t *testing.T) {
	// Test that detection works in a worktree (git dir is different)
	tmpDir := t.TempDir()
	mainRepo := filepath.Join(tmpDir, "main")
	worktreeDir := filepath.Join(tmpDir, "worktree")

	if err := os.MkdirAll(mainRepo, 0o755); err != nil {
		t.Fatalf("failed to create main repo dir: %v", err)
	}

	ctx := context.Background()

	// Initialize main repo with a commit
	cmd := exec.CommandContext(ctx, "git", "init")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init main repo: %v", err)
	}

	cmd = exec.CommandContext(ctx, "git", "config", "user.email", "test@test.com")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to configure git email: %v", err)
	}

	cmd = exec.CommandContext(ctx, "git", "config", "user.name", "Test User")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to configure git name: %v", err)
	}

	// Disable GPG signing for test commits
	cmd = exec.CommandContext(ctx, "git", "config", "commit.gpgsign", "false")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to configure commit.gpgsign: %v", err)
	}

	testFile := filepath.Join(mainRepo, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	cmd = exec.CommandContext(ctx, "git", "add", ".")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git add: %v", err)
	}

	cmd = exec.CommandContext(ctx, "git", "commit", "-m", "initial")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git commit: %v", err)
	}

	// Create a worktree
	cmd = exec.CommandContext(ctx, "git", "worktree", "add", worktreeDir, "-b", "feature")
	cmd.Dir = mainRepo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create worktree: %v", err)
	}

	// Change to worktree
	t.Chdir(worktreeDir)

	// Should not detect sequence operation in clean worktree
	if isGitSequenceOperation(context.Background()) {
		t.Error("isGitSequenceOperation(context.Background()) = true in clean worktree, want false")
	}

	// Get the worktree's git dir and simulate rebase state there
	cmd = exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	cmd.Dir = worktreeDir
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("failed to get git dir: %v", err)
	}
	gitDir := strings.TrimSpace(string(output))

	rebaseMergeDir := filepath.Join(gitDir, "rebase-merge")
	if err := os.MkdirAll(rebaseMergeDir, 0o755); err != nil {
		t.Fatalf("failed to create rebase-merge dir in worktree: %v", err)
	}

	// Now should detect sequence operation
	if !isGitSequenceOperation(context.Background()) {
		t.Error("isGitSequenceOperation(context.Background()) = false in worktree during rebase, want true")
	}
}

func TestInstallGitHook_Idempotent(t *testing.T) {
	_, hooksDir := initHooksTestRepo(t)

	// First install should install hooks
	firstCount, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("First InstallGitHook() error = %v", err)
	}
	if firstCount == 0 {
		t.Error("First InstallGitHook() should install hooks (count > 0)")
	}

	// Capture hook contents after first install
	firstContents := make(map[string]string)
	for _, hook := range gitHookNames {
		data, err := os.ReadFile(filepath.Join(hooksDir, hook))
		if err != nil {
			t.Fatalf("hook %s should exist after install: %v", hook, err)
		}
		firstContents[hook] = string(data)
		if !strings.Contains(string(data), traceHookMarker) {
			t.Errorf("hook %s should contain Trace marker", hook)
		}
	}

	// Second install should return 0 (all hooks already up to date)
	secondCount, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("Second InstallGitHook() error = %v", err)
	}
	if secondCount != 0 {
		t.Errorf("Second InstallGitHook() returned %d, want 0 (hooks unchanged)", secondCount)
	}

	// Content should be identical after second install
	for _, hook := range gitHookNames {
		data, err := os.ReadFile(filepath.Join(hooksDir, hook))
		if err != nil {
			t.Fatalf("hook %s should exist: %v", hook, err)
		}
		if string(data) != firstContents[hook] {
			t.Errorf("hook %s content changed after idempotent reinstall", hook)
		}
	}
}

func TestInstallGitHook_LocalDevCommandPrefix(t *testing.T) {
	_, hooksDir := initHooksTestRepo(t)

	// Install with localDev=true
	count, err := InstallGitHook(context.Background(), true, true, false)
	if err != nil {
		t.Fatalf("InstallGitHook(localDev=true) error = %v", err)
	}
	if count == 0 {
		t.Fatal("InstallGitHook(localDev=true) should install hooks")
	}

	for _, hook := range gitHookNames {
		data, err := os.ReadFile(filepath.Join(hooksDir, hook))
		if err != nil {
			t.Fatalf("hook %s should exist: %v", hook, err)
		}
		content := string(data)
		if !strings.Contains(content, "go run ./cmd/hawk trace") {
			t.Errorf("hook %s should use 'go run' prefix when localDev=true, got:\n%s", hook, content)
		}
		if strings.Contains(content, "\nhawk trace ") {
			t.Errorf("hook %s should not use bare 'hawk trace' prefix when localDev=true", hook)
		}
	}

	// Reinstall with localDev=false — hooks should update to use "trace" prefix
	count, err = InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("InstallGitHook(localDev=false) error = %v", err)
	}
	if count == 0 {
		t.Fatal("InstallGitHook(localDev=false) should reinstall hooks (content changed)")
	}

	for _, hook := range gitHookNames {
		data, err := os.ReadFile(filepath.Join(hooksDir, hook))
		if err != nil {
			t.Fatalf("hook %s should exist: %v", hook, err)
		}
		content := string(data)
		if strings.Contains(content, "go run") {
			t.Errorf("hook %s should not use 'go run' prefix when localDev=false, got:\n%s", hook, content)
		}
		if !strings.Contains(content, "\nhawk trace ") {
			t.Errorf("hook %s should use bare 'hawk trace' prefix when localDev=false", hook)
		}
	}
}

func TestInstallGitHook_AbsoluteGitHookPath(t *testing.T) {
	_, hooksDir := initHooksTestRepo(t)

	// Install with absolutePath=true
	count, err := InstallGitHook(context.Background(), true, false, true)
	if err != nil {
		t.Fatalf("InstallGitHook(absolutePath=true) error = %v", err)
	}
	if count == 0 {
		t.Fatal("InstallGitHook(absolutePath=true) should install hooks")
	}

	// Get the expected absolute path (shell-quoted)
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatalf("filepath.EvalSymlinks() error = %v", err)
	}
	quoted := shellQuote(resolved)

	for _, hook := range gitHookNames {
		data, err := os.ReadFile(filepath.Join(hooksDir, hook))
		if err != nil {
			t.Fatalf("hook %s should exist: %v", hook, err)
		}
		content := string(data)
		if !strings.Contains(content, quoted) {
			t.Errorf("hook %s should contain shell-quoted absolute path %q, got:\n%s", hook, quoted, content)
		}
		if strings.Contains(content, "\nhawk trace ") {
			t.Errorf("hook %s should not use bare 'hawk trace' prefix when absolutePath=true", hook)
		}
	}
}

func TestShellQuote(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"/usr/local/bin/trace", "'/usr/local/bin/trace'"},
		{"/Users/John O'Brien/bin/trace", "'/Users/John O'\\''Brien/bin/trace'"},
		{"/path with spaces/trace", "'/path with spaces/trace'"},
		{"/simple", "'/simple'"},
	}

	for _, tt := range tests {
		got := shellQuote(tt.input)
		if got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestInstallGitHook_CoreHooksPathRelative(t *testing.T) {
	tmpDir, _ := initHooksTestRepo(t)
	ctx := context.Background()

	// Simulate Husky-style override: hooks live outside .git/hooks.
	cmd := exec.CommandContext(ctx, "git", "config", "core.hooksPath", ".husky/_")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set core.hooksPath: %v", err)
	}

	count, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("InstallGitHook() error = %v", err)
	}
	if count == 0 {
		t.Fatal("InstallGitHook() should install hooks when core.hooksPath is set")
	}

	configuredHooksDir := filepath.Join(tmpDir, ".husky", "_")
	for _, hook := range gitHookNames {
		hookPath := filepath.Join(configuredHooksDir, hook)
		data, readErr := os.ReadFile(hookPath)
		if readErr != nil {
			t.Fatalf("expected hook %s in core.hooksPath dir: %v", hook, readErr)
		}
		if !strings.Contains(string(data), traceHookMarker) {
			t.Errorf("hook %s in core.hooksPath dir should contain Trace marker", hook)
		}
	}

	// Ensure we did not incorrectly write Trace hooks into .git/hooks.
	defaultHooksDir := filepath.Join(tmpDir, ".git", "hooks")
	for _, hook := range gitHookNames {
		defaultHookPath := filepath.Join(defaultHooksDir, hook)
		if data, readErr := os.ReadFile(defaultHookPath); readErr == nil && strings.Contains(string(data), traceHookMarker) {
			t.Errorf("default hook %s should not contain Trace marker when core.hooksPath is set", hook)
		}
	}

	if !IsGitHookInstalledInDir(context.Background(), tmpDir) {
		t.Error("IsGitHookInstalledInDir() should detect hooks installed in core.hooksPath")
	}
}

func TestRemoveGitHook_CoreHooksPathRelative(t *testing.T) {
	tmpDir, _ := initHooksTestRepo(t)
	ctx := context.Background()

	cmd := exec.CommandContext(ctx, "git", "config", "core.hooksPath", ".husky/_")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set core.hooksPath: %v", err)
	}

	installCount, err := InstallGitHook(context.Background(), true, false, false)
	if err != nil {
		t.Fatalf("InstallGitHook() error = %v", err)
	}
	if installCount == 0 {
		t.Fatal("InstallGitHook() should install hooks before removal test")
	}

	// Hooks must be installed in core.hooksPath (not .git/hooks).
	configuredHooksDir := filepath.Join(tmpDir, ".husky", "_")
	for _, hook := range gitHookNames {
		hookPath := filepath.Join(configuredHooksDir, hook)
		if _, statErr := os.Stat(hookPath); statErr != nil {
			t.Fatalf("expected hook %s in core.hooksPath before removal: %v", hook, statErr)
		}
	}

	removeCount, err := RemoveGitHook(context.Background())
	if err != nil {
		t.Fatalf("RemoveGitHook(context.Background()) error = %v", err)
	}
	if removeCount != installCount {
		t.Errorf("RemoveGitHook(context.Background()) returned %d, want %d", removeCount, installCount)
	}

	for _, hook := range gitHookNames {
		hookPath := filepath.Join(configuredHooksDir, hook)
		if _, statErr := os.Stat(hookPath); !os.IsNotExist(statErr) {
			t.Errorf("hook file %s should not exist in core.hooksPath after removal", hook)
		}
	}

	if IsGitHookInstalledInDir(context.Background(), tmpDir) {
		t.Error("IsGitHookInstalledInDir() should be false after removing hooks in core.hooksPath")
	}
}
