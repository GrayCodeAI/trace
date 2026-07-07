package strategy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/GrayCodeAI/trace/cli/settings"
)

// Hook marker used to identify Trace CLI hooks
const traceHookMarker = "Trace CLI hooks"

const (
	backupSuffix = ".pre-trace"
	chainComment = "# Chain: run pre-existing hook"
)

// gitHookNames are the git hooks managed by Trace CLI
var gitHookNames = []string{"prepare-commit-msg", "commit-msg", "post-commit", "post-rewrite", "pre-push"}

// ManagedGitHookNames returns the list of git hooks managed by Trace CLI.
// This is useful for tests that need to manipulate hooks.
func ManagedGitHookNames() []string {
	return gitHookNames
}

// hookSpec defines a git hook's name and content template (without chain call).
type hookSpec struct {
	name    string
	content string
}

// GetGitDir returns the actual git directory path by delegating to git itself.
// This handles both regular repositories and worktrees, and inherits git's
// security validation for gitdir references.
func GetGitDir(ctx context.Context) (string, error) {
	return getGitDirInPath(ctx, ".")
}

// hooksDirCache caches the hooks directory to avoid repeated git subprocess spawns.
// Keyed by current working directory to handle directory changes.
var (
	hooksDirMu       sync.RWMutex
	hooksDirCache    string
	hooksDirCacheDir string
)

// GetHooksDir returns the active hooks directory path.
// This respects core.hooksPath and correctly resolves to the common hooks
// directory when called from a linked worktree.
// The result is cached per working directory.
func GetHooksDir(ctx context.Context) (string, error) {
	cwd, err := os.Getwd() //nolint:forbidigo // cache key for hooks dir, same pattern as paths.WorktreeRoot()
	if err != nil {
		cwd = ""
	}

	hooksDirMu.RLock()
	if hooksDirCache != "" && hooksDirCacheDir == cwd {
		cached := hooksDirCache
		hooksDirMu.RUnlock()
		return cached, nil
	}
	hooksDirMu.RUnlock()

	result, err := getHooksDirInPath(ctx, ".")
	if err != nil {
		return "", err
	}

	hooksDirMu.Lock()
	hooksDirCache = result
	hooksDirCacheDir = cwd
	hooksDirMu.Unlock()

	return result, nil
}

// ClearHooksDirCache clears the cached hooks directory.
// This is primarily useful for testing when changing directories.
func ClearHooksDirCache() {
	hooksDirMu.Lock()
	hooksDirCache = ""
	hooksDirCacheDir = ""
	hooksDirMu.Unlock()
}

// getGitDirInPath returns the git directory for a repository at the given path.
// It delegates to `git rev-parse --git-dir` to leverage git's own validation.
func getGitDirInPath(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-dir")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", errors.New("not a git repository")
	}

	gitDir := strings.TrimSpace(string(output))

	// git rev-parse --git-dir returns relative paths from the working directory,
	// so we need to make it absolute if it isn't already
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}

	return filepath.Clean(gitDir), nil
}

// getHooksDirInPath returns the active hooks directory for a repository at the given path.
// It delegates to `git rev-parse --git-path hooks` so Git resolves:
// - linked-worktree common hooks directory
// - core.hooksPath (relative or absolute)
func getHooksDirInPath(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-path", "hooks")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", errors.New("not a git repository")
	}

	hooksDir := strings.TrimSpace(string(output))
	if !filepath.IsAbs(hooksDir) {
		hooksDir = filepath.Join(dir, hooksDir)
	}

	return filepath.Clean(hooksDir), nil
}

// IsGitHookInstalled checks if all generic Trace CLI hooks are installed.
func IsGitHookInstalled(ctx context.Context) bool {
	hooksDir, err := GetHooksDir(ctx)
	if err != nil {
		return false
	}
	return isGitHookInstalledInHooksDir(hooksDir)
}

// IsGitHookInstalledInDir checks if all Trace CLI hooks are installed in the given repo directory.
// This is useful for tests that need to check hooks without changing the working directory.
func IsGitHookInstalledInDir(ctx context.Context, repoDir string) bool {
	hooksDir, err := getHooksDirInPath(ctx, repoDir)
	if err != nil {
		return false
	}
	return isGitHookInstalledInHooksDir(hooksDir)
}

// isGitHookInstalledInHooksDir checks if all hooks are installed in the given hooks directory.
func isGitHookInstalledInHooksDir(hooksDir string) bool {
	for _, hook := range gitHookNames {
		hookPath := filepath.Join(hooksDir, hook)
		// #nosec G304 -- hookPath is constructed from constants (hooksDir + known hook name), not external input
		data, err := os.ReadFile(hookPath) //nolint:gosec // Path is constructed from constants
		if err != nil {
			return false
		}
		if !strings.Contains(string(data), traceHookMarker) {
			return false
		}
	}
	return true
}

// buildHookSpecs returns the hook specifications for all managed hooks.
func buildHookSpecs(cmdPrefix string) []hookSpec {
	return []hookSpec{
		{
			name: "prepare-commit-msg",
			content: fmt.Sprintf(`#!/bin/sh
# %s
%s hooks git prepare-commit-msg "$1" "$2" 2>>".git/trace-hooks.log" || true
`, traceHookMarker, cmdPrefix),
		},
		{
			name: "commit-msg",
			content: fmt.Sprintf(`#!/bin/sh
# %s
# Commit-msg hook: strip trailer if no user content (allows aborting empty commits)
%s hooks git commit-msg "$1" || exit 1
`, traceHookMarker, cmdPrefix),
		},
		{
			name: "post-commit",
			content: fmt.Sprintf(`#!/bin/sh
# %s
# Post-commit hook: condense session data if commit has Trace-Checkpoint trailer
%s hooks git post-commit 2>>".git/trace-hooks.log" || true
`, traceHookMarker, cmdPrefix),
		},
		{
			name: "post-rewrite",
			content: fmt.Sprintf(`#!/bin/sh
# %s
# Post-rewrite hook: remap session linkage after amend/rebase rewrites
%s hooks git post-rewrite "$1" 2>>".git/trace-hooks.log" || true
`, traceHookMarker, cmdPrefix),
		},
		{
			name: "pre-push",
			content: fmt.Sprintf(`#!/bin/sh
# %s
# Pre-push hook: push session logs alongside user's push
# $1 is the remote name (e.g., "origin")
%s hooks git pre-push "$1" || true
`, traceHookMarker, cmdPrefix),
		},
	}
}

// InstallGitHook installs generic git hooks that delegate to `trace hook` commands.
// These hooks work with any strategy - the strategy is determined at runtime.
// If silent is true, no output is printed (except backup notifications, which always print).
// localDev controls whether hooks use "go run" (true) or the "trace" binary (false).
// absolutePath embeds the full binary path in hooks for GUI git clients.
// Returns the number of hooks that were installed (0 if all already up to date).
func InstallGitHook(ctx context.Context, silent, localDev, absolutePath bool) (int, error) {
	hooksDir, err := GetHooksDir(ctx)
	if err != nil {
		return 0, err
	}

	// #nosec G301 -- git hooks directory must be traversable/executable (standard git hooks dir mode), not private data
	if err := os.MkdirAll(hooksDir, 0o755); err != nil { //nolint:gosec // Git hooks require executable permissions
		return 0, fmt.Errorf("failed to create hooks directory: %w", err)
	}

	cmdPrefix, err := hookCmdPrefix(localDev, absolutePath)
	if err != nil {
		return 0, err
	}
	specs := buildHookSpecs(cmdPrefix)
	installedCount := 0

	for _, spec := range specs {
		hookPath := filepath.Join(hooksDir, spec.name)
		backupPath := hookPath + backupSuffix
		backupExists := fileExists(backupPath)

		// Back up existing non-Trace hooks
		// #nosec G304 -- hookPath is constructed from constants (hooksDir + known hook name), not external input
		existing, existingErr := os.ReadFile(hookPath) //nolint:gosec // path is controlled
		if existingErr == nil && !strings.Contains(string(existing), traceHookMarker) {
			if !backupExists {
				if err := os.Rename(hookPath, backupPath); err != nil {
					return installedCount, fmt.Errorf("failed to back up %s: %w", spec.name, err)
				}
				fmt.Fprintf(os.Stderr, "[trace] Backed up existing %s to %s%s\n", spec.name, spec.name, backupSuffix)
			} else {
				fmt.Fprintf(os.Stderr, "[trace] Warning: replacing %s (backup %s%s already exists from a previous install)\n", spec.name, spec.name, backupSuffix)
			}
			backupExists = true
		}

		// Chain to backup if one exists
		content := spec.content
		if backupExists {
			content = generateChainedContent(spec.content, spec.name)
		}

		written, err := writeHookFile(hookPath, content)
		if err != nil {
			return installedCount, fmt.Errorf("failed to install %s hook: %w", spec.name, err)
		}
		if written {
			installedCount++
		}
	}

	if !silent {
		fmt.Println("✓ Installed git hooks (prepare-commit-msg, commit-msg, post-commit, pre-push)")
		fmt.Println("  Hooks delegate to the current strategy at runtime")
	}

	return installedCount, nil
}

// writeHookFile writes a hook file if it doesn't exist or has different content.
// Returns true if the file was written, false if it already had the same content.
func writeHookFile(path, content string) (bool, error) {
	// Check if file already exists with same content
	// #nosec G304 -- path is constructed from constants (hooksDir + known hook name), not external input
	existing, err := os.ReadFile(path) //nolint:gosec // path is controlled
	if err == nil && string(existing) == content {
		return false, nil // Already up to date
	}

	// Git hooks must be executable (0o755)
	// #nosec G306 -- git hooks must be executable (0755) to run as shell scripts, not private data
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil { //nolint:gosec // Git hooks require executable permissions
		return false, fmt.Errorf("failed to write hook file %s: %w", path, err)
	}
	return true, nil
}

// RemoveGitHook removes all Trace CLI git hooks from the repository.
// If a .pre-trace backup exists, it is restored.
// Returns the number of hooks removed.
func RemoveGitHook(ctx context.Context) (int, error) {
	hooksDir, err := GetHooksDir(ctx)
	if err != nil {
		return 0, err
	}

	removed := 0
	var removeErrors []string

	for _, hook := range gitHookNames {
		hookPath := filepath.Join(hooksDir, hook)
		backupPath := hookPath + backupSuffix

		// Remove the hook if it contains our marker
		// #nosec G304 -- hookPath is constructed from constants (hooksDir + known hook name), not external input
		data, err := os.ReadFile(hookPath) //nolint:gosec // path is controlled
		hookIsOurs := err == nil && strings.Contains(string(data), traceHookMarker)
		hookExists := err == nil

		if hookIsOurs {
			if err := os.Remove(hookPath); err != nil {
				removeErrors = append(removeErrors, fmt.Sprintf("%s: %v", hook, err))
				continue
			}
			removed++
		}

		// Restore .pre-trace backup if it exists
		if fileExists(backupPath) {
			if hookExists && !hookIsOurs {
				// A non-Trace hook is present — don't overwrite it with the backup
				fmt.Fprintf(os.Stderr, "[trace] Warning: %s was modified since install; backup %s%s left in place\n", hook, hook, backupSuffix)
			} else {
				if err := os.Rename(backupPath, hookPath); err != nil {
					removeErrors = append(removeErrors, fmt.Sprintf("restore %s%s: %v", hook, backupSuffix, err))
				}
			}
		}
	}

	if len(removeErrors) > 0 {
		return removed, fmt.Errorf("failed to remove hooks: %s", strings.Join(removeErrors, "; "))
	}
	return removed, nil
}

// generateChainedContent appends a chain call to the base hook content,
// so the pre-existing hook (backed up to .pre-trace) is called after our hook.
func generateChainedContent(baseContent, hookName string) string {
	if hookName == "post-rewrite" {
		return generatePostRewriteChainedContent(baseContent)
	}

	return baseContent + fmt.Sprintf(`%s
_trace_hook_dir="$(dirname "$0")"
if [ -x "$_trace_hook_dir/%s%s" ]; then
    "$_trace_hook_dir/%s%s" "$@"
fi
`, chainComment, hookName, backupSuffix, hookName, backupSuffix)
}

func generatePostRewriteChainedContent(baseContent string) string {
	const original = `trace hooks git post-rewrite "$1" 2>/dev/null || true`
	const replacement = `trace hooks git post-rewrite "$1" < "$_trace_stdin" 2>/dev/null || true`

	replayPrefix := `_trace_stdin="$(mktemp "${TMPDIR:-/tmp}/trace-post-rewrite.XXXXXX")"
cat > "$_trace_stdin"
trap 'rm -f "$_trace_stdin"' EXIT
` + replacement

	return strings.Replace(baseContent, original, replayPrefix, 1) + fmt.Sprintf(`
%s
_trace_hook_dir="$(dirname "$0")"
if [ -x "$_trace_hook_dir/post-rewrite%s" ]; then
    "$_trace_hook_dir/post-rewrite%s" "$@" < "$_trace_stdin"
fi
`, chainComment, backupSuffix, backupSuffix)
}

// hookCmdPrefix returns the command prefix for hook scripts and warning messages.
// trace ships inside the hawk binary, so the command surface is "hawk trace ...".
// Returns "go run ./cmd/hawk trace" when local_dev is enabled (runs hawk from source).
// When absolutePath is true, resolves the full binary path via os.Executable()
// (the running hawk binary) and appends " trace"; returns an error if resolution
// fails. This is needed for GUI git clients (Xcode, Tower, etc.) that don't source
// shell profiles.
func hookCmdPrefix(localDev, absolutePath bool) (string, error) {
	if localDev {
		return "go run ./cmd/hawk trace", nil
	}
	if absolutePath {
		exe, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("--absolute-git-hook-path: failed to resolve binary path: %w", err)
		}
		resolved, err := filepath.EvalSymlinks(exe)
		if err != nil {
			return "", fmt.Errorf("--absolute-git-hook-path: failed to resolve symlinks for %s: %w", exe, err)
		}
		return shellQuote(resolved) + " trace", nil
	}
	return "hawk trace", nil
}

// shellQuote wraps a string in single quotes for safe use in #!/bin/sh scripts.
// Handles paths containing spaces, apostrophes, or other shell metacharacters
// (e.g., /Users/John O'Brien/bin/trace).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// hookSettingsFromConfig loads hook-related settings from .trace/settings.json.
// Returns (localDev, absoluteHookPath). On error, both default to false.
func hookSettingsFromConfig(ctx context.Context) (localDev, absoluteHookPath bool) {
	s, err := settings.Load(ctx)
	if err != nil {
		return false, false
	}
	return s.LocalDev, s.AbsoluteGitHookPath
}
