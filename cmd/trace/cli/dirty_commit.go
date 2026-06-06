package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/logging"
)

// dirtyCommitMessage is the commit message used for the pre-session
// work-in-progress snapshot. It is intentionally fixed so these commits are
// easy to recognize (and squash/drop) later.
const dirtyCommitMessage = "trace: save work in progress before agent session"

// dirtyCommitsDisabledFlag, when set true (e.g. by --no-dirty-commits), forces
// the pre-session WIP auto-commit off regardless of configuration. It is a
// process-global override set once during command setup before any lifecycle
// hook runs.
var dirtyCommitsDisabledFlag bool

// SetDirtyCommitsDisabled records the per-invocation --no-dirty-commits
// override. When called with true, AutoCommitDirtyWorkingTree becomes a no-op
// for the remainder of the process even if config enables dirty commits.
func SetDirtyCommitsDisabled(disabled bool) {
	dirtyCommitsDisabledFlag = disabled
}

// dirtyCommitsEnabled reports the effective enablement of pre-session WIP
// commits: the --no-dirty-commits flag wins over config; otherwise the
// dirty_commits setting applies (default on).
func dirtyCommitsEnabled(ctx context.Context) bool {
	if dirtyCommitsDisabledFlag {
		return false
	}
	s, err := LoadTraceSettings(ctx)
	if err != nil {
		// On a load error, fall back to the default (enabled) so a malformed
		// settings file does not silently drop the user's WIP snapshot.
		logging.Warn(logging.WithComponent(ctx, "dirty-commit"),
			"failed to load settings for dirty-commit check; using default (enabled)",
			slog.String("error", err.Error()))
		return true
	}
	return s.DirtyCommitsEnabled()
}

// AutoCommitDirtyWorkingTree commits any uncommitted changes (staged, unstaged,
// and untracked) on the current branch as a single "work in progress" snapshot
// before an agent session begins. It is a no-op when dirty commits are disabled
// (via --no-dirty-commits or the dirty_commits config flag) or when the working
// tree is already clean.
//
// The commit is created on the user's current branch using the human git author
// (attribution flags apply only to Trace's own checkpoint commits, not to the
// user's pre-existing work). Returns the new commit hash, or empty string when
// no commit was created.
func AutoCommitDirtyWorkingTree(ctx context.Context) (string, error) {
	logCtx := logging.WithComponent(ctx, "dirty-commit")

	if !dirtyCommitsEnabled(ctx) {
		logging.Debug(logCtx, "dirty commits disabled; skipping pre-session WIP commit")
		return "", nil
	}

	dirty, err := HasUncommittedChanges(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to check working tree: %w", err)
	}
	if !dirty {
		logging.Debug(logCtx, "working tree clean; no WIP commit needed")
		return "", nil
	}

	hash, err := commitWorkingTree(ctx, dirtyCommitMessage)
	if err != nil {
		return "", err
	}

	logging.Info(logCtx, "committed work in progress before agent session",
		slog.String("commit", hash))
	return hash, nil
}

// commitWorkingTree stages all changes and creates a commit with the given
// message using the human git author. It uses the git CLI so that the user's
// global gitignore (core.excludesfile) and hooks behave exactly as they would
// for a manual commit. Returns the resulting commit hash.
func commitWorkingTree(ctx context.Context, message string) (string, error) {
	// Stage everything: modifications, deletions, and untracked files.
	addCmd := exec.CommandContext(ctx, "git", "add", "-A")
	if out, addErr := addCmd.CombinedOutput(); addErr != nil {
		return "", fmt.Errorf("failed to stage changes: %s: %w", strings.TrimSpace(string(out)), addErr)
	}

	// Create the commit. -q keeps output quiet; the WIP message is fixed.
	commitCmd := exec.CommandContext(ctx, "git", "commit", "-q", "-m", message)
	if out, commitErr := commitCmd.CombinedOutput(); commitErr != nil {
		trimmed := strings.TrimSpace(string(out))
		// "nothing to commit" can happen if everything staged was ignored or
		// already committed by a concurrent process — treat as a no-op.
		if strings.Contains(trimmed, "nothing to commit") {
			return "", nil
		}
		return "", fmt.Errorf("failed to create WIP commit: %s: %w", trimmed, commitErr)
	}

	revCmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	out, revErr := revCmd.Output()
	if revErr != nil {
		// The commit succeeded but we couldn't read its hash; report success
		// with an empty hash rather than failing the session start.
		return "", nil
	}
	hash := strings.TrimSpace(string(out))
	if hash == "" {
		return "", errors.New("empty HEAD after WIP commit")
	}
	return hash, nil
}
