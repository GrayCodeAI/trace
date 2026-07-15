package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	agentpkg "github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/oplog"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cli/transcript"

	"charm.land/huh/v2"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

// restoreTaskCheckpointTranscript restores a truncated transcript for a task checkpoint.
// Uses GetTaskCheckpointTranscript to fetch the transcript from the strategy.
//
// NOTE: The transcript parsing/truncation/writing pipeline (transcript.ParseFromBytes,
// TruncateTranscriptAtUUID, writeTranscript) assumes Claude's JSONL format.
// This is acceptable because task checkpoints are currently only created by Claude Code's
// PostToolUse hook. If other agents gain sub-agent support, this will need a
// format-aware refactor (agent-specific parsing, truncation, and serialization).
func restoreTaskCheckpointTranscript(ctx context.Context, w io.Writer, start *strategy.ManualCommitStrategy, point strategy.RewindPoint, sessionID, checkpointUUID string, agent agentpkg.Agent) error {
	// Get transcript content from strategy
	content, err := start.GetTaskCheckpointTranscript(ctx, point)
	if err != nil {
		return fmt.Errorf("failed to get task checkpoint transcript: %w", err)
	}

	// Parse the transcript
	parsed, err := transcript.ParseFromBytes(content)
	if err != nil {
		return fmt.Errorf("failed to parse transcript: %w", err)
	}

	// Truncate at checkpoint UUID
	truncated := TruncateTranscriptAtUUID(parsed, checkpointUUID)

	sessionFile, err := resolveTranscriptPath(ctx, sessionID, agent)
	if err != nil {
		return err
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o750); err != nil {
		return fmt.Errorf("failed to create agent session directory: %w", err)
	}

	fmt.Fprintf(w, "Writing truncated transcript to: %s\n", sessionFile)

	if err := writeTranscript(sessionFile, truncated); err != nil {
		return fmt.Errorf("failed to write truncated transcript: %w", err)
	}

	return nil
}

// handleLogsOnlyRewindInteractive handles rewind for logs-only points with a sub-choice menu.
func handleLogsOnlyRewindInteractive(ctx context.Context, w, errW io.Writer, start *strategy.ManualCommitStrategy, point strategy.RewindPoint, shortID string) error {
	var action string

	form := NewAccessibleForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Logs-only point: "+shortID).
				Description("This commit has session logs but no checkpoint state. Choose an action:").
				Options(
					huh.NewOption("Restore logs only (keep current files)", "logs"),
					huh.NewOption("Checkout commit (detached HEAD, for viewing)", "checkout"),
					huh.NewOption("Reset branch to this commit (destructive!)", "reset"),
					huh.NewOption("Cancel", "cancel"),
				).
				Value(&action),
		),
	)

	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil
		}
		return fmt.Errorf("action selection failed: %w", err)
	}

	switch action {
	case "logs":
		return handleLogsOnlyRestore(ctx, w, errW, start, point)
	case "checkout":
		return handleLogsOnlyCheckout(ctx, w, errW, start, point, shortID)
	case "reset":
		return handleLogsOnlyReset(ctx, w, errW, start, point, shortID)
	case "cancel":
		fmt.Fprintln(w, "Rewind cancelled.")
		return nil
	}

	return nil
}

// handleLogsOnlyRestore restores only the session logs without changing files.
func handleLogsOnlyRestore(ctx context.Context, w, errW io.Writer, start *strategy.ManualCommitStrategy, point strategy.RewindPoint) error {
	// Resolve agent once for use throughout
	agent, err := getAgent(point.Agent)
	if err != nil {
		return fmt.Errorf("failed to get agent: %w", err)
	}

	// Initialize logging context with agent from checkpoint
	logCtx := logging.WithComponent(ctx, "rewind")
	logCtx = logging.WithAgent(logCtx, agent.Name())

	logging.Debug(
		logCtx, "logs-only restore started",
		slog.String("checkpoint_id", point.ID),
		slog.String("session_id", point.SessionID),
	)

	// Restore logs
	sessions, err := start.RestoreLogsOnly(ctx, w, errW, point, true) // force=true for explicit rewind
	if err != nil {
		logging.Error(
			logCtx, "logs-only restore failed",
			slog.String("checkpoint_id", point.ID),
			slog.String("error", err.Error()),
		)
		return fmt.Errorf("failed to restore logs: %w", err)
	}

	logging.Debug(
		logCtx, "logs-only restore completed",
		slog.String("checkpoint_id", point.ID),
	)

	// Show resume commands for all sessions
	fmt.Fprintln(w, "✓ Restored session logs.")
	printMultiSessionResumeCommands(w, errW, sessions)
	return nil
}

// handleLogsOnlyCheckout restores logs and checks out the commit (detached HEAD).
func handleLogsOnlyCheckout(ctx context.Context, w, errW io.Writer, start *strategy.ManualCommitStrategy, point strategy.RewindPoint, shortID string) error {
	// Resolve agent once for use throughout
	agent, err := getAgent(point.Agent)
	if err != nil {
		return fmt.Errorf("failed to get agent: %w", err)
	}

	// Initialize logging context with agent from checkpoint
	logCtx := logging.WithComponent(ctx, "rewind")
	logCtx = logging.WithAgent(logCtx, agent.Name())

	logging.Debug(
		logCtx, "logs-only checkout started",
		slog.String("checkpoint_id", point.ID),
		slog.String("session_id", point.SessionID),
	)

	sessions, err := start.RestoreLogsOnly(ctx, w, errW, point, true) // force=true for explicit rewind
	if err != nil {
		logging.Error(
			logCtx, "logs-only checkout failed during log restoration",
			slog.String("checkpoint_id", point.ID),
			slog.String("error", err.Error()),
		)
		return fmt.Errorf("failed to restore logs: %w", err)
	}

	// Show warning about detached HEAD
	var confirm bool
	confirmForm := NewAccessibleForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Create detached HEAD?").
				Description("This will checkout the commit directly. You'll be in 'detached HEAD' state.\nAny uncommitted changes will be lost!").
				Value(&confirm),
		),
	)

	if err := confirmForm.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil
		}
		return fmt.Errorf("confirmation failed: %w", err)
	}

	if !confirm {
		fmt.Fprintln(w, "Checkout cancelled. Session logs were still restored.")
		printMultiSessionResumeCommands(w, errW, sessions)
		return nil
	}

	// Perform git checkout
	if err := CheckoutBranch(ctx, point.ID); err != nil {
		logging.Error(
			logCtx, "logs-only checkout failed during git checkout",
			slog.String("checkpoint_id", point.ID),
			slog.String("error", err.Error()),
		)
		return fmt.Errorf("failed to checkout commit: %w", err)
	}

	logging.Debug(
		logCtx, "logs-only checkout completed",
		slog.String("checkpoint_id", point.ID),
	)

	fmt.Fprintf(w, "✓ Checked out %s (detached HEAD).\n", shortID)
	printMultiSessionResumeCommands(w, errW, sessions)
	return nil
}

// handleLogsOnlyReset restores logs and resets the branch to the commit (destructive).
func handleLogsOnlyReset(ctx context.Context, w, errW io.Writer, start *strategy.ManualCommitStrategy, point strategy.RewindPoint, shortID string) error {
	// Resolve agent once for use throughout
	agent, agentErr := getAgent(point.Agent)
	if agentErr != nil {
		return fmt.Errorf("failed to get agent: %w", agentErr)
	}

	// Initialize logging context with agent from checkpoint
	logCtx := logging.WithComponent(ctx, "rewind")
	logCtx = logging.WithAgent(logCtx, agent.Name())

	logging.Debug(
		logCtx, "logs-only reset (interactive) started",
		slog.String("checkpoint_id", point.ID),
		slog.String("session_id", point.SessionID),
	)

	sessions, restoreErr := start.RestoreLogsOnly(ctx, w, errW, point, true) // force=true for explicit rewind
	if restoreErr != nil {
		logging.Error(
			logCtx, "logs-only reset failed during log restoration",
			slog.String("checkpoint_id", point.ID),
			slog.String("error", restoreErr.Error()),
		)
		return fmt.Errorf("failed to restore logs: %w", restoreErr)
	}

	// Get current HEAD before reset (for recovery message)
	currentHead, err := getCurrentHeadHash(ctx)
	if err != nil {
		// Non-fatal - just won't show recovery message
		currentHead = ""
	}

	// Get detailed uncommitted changes warning from strategy
	var uncommittedWarning string
	if _, warn, err := start.CanRewind(ctx); err == nil {
		uncommittedWarning = warn
	}

	// Check for safety issues
	warnings, err := checkResetSafety(ctx, point.ID, uncommittedWarning)
	if err != nil {
		return fmt.Errorf("failed to check reset safety: %w", err)
	}

	// Build confirmation message based on warnings
	var confirmTitle, confirmDesc string
	if len(warnings) > 0 {
		confirmTitle = "⚠️  Reset branch with warnings?"
		confirmDesc = "WARNING - the following issues were detected:\n" +
			strings.Join(warnings, "\n") +
			"\n\nThis will move your branch to " + shortID + " and DISCARD commits after it!"
	} else {
		confirmTitle = "Reset branch to " + shortID + "?"
		confirmDesc = "This will move your branch pointer to this commit.\nCommits after this point will be orphaned (but recoverable via reflog)."
	}

	var confirm bool
	confirmForm := NewAccessibleForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(confirmTitle).
				Description(confirmDesc).
				Value(&confirm),
		),
	)

	if err := confirmForm.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil
		}
		return fmt.Errorf("confirmation failed: %w", err)
	}

	if !confirm {
		fmt.Fprintln(w, "Reset cancelled. Session logs were still restored.")
		printMultiSessionResumeCommands(w, errW, sessions)
		return nil
	}

	// Perform git reset --hard
	if err := performGitResetHard(ctx, point.ID); err != nil {
		logging.Error(
			logCtx, "logs-only reset failed during git reset",
			slog.String("checkpoint_id", point.ID),
			slog.String("error", err.Error()),
		)
		return fmt.Errorf("failed to reset branch: %w", err)
	}

	recordResetOplogEntry(logCtx, currentHead, point.ID)

	logging.Debug(
		logCtx, "logs-only reset (interactive) completed",
		slog.String("checkpoint_id", point.ID),
	)

	fmt.Fprintf(w, "✓ Reset branch to %s.\n", shortID)
	printMultiSessionResumeCommands(w, errW, sessions)

	// Show recovery instructions
	if currentHead != "" && currentHead != point.ID {
		currentShort := currentHead
		if len(currentShort) > 7 {
			currentShort = currentShort[:7]
		}
		fmt.Fprintf(w, "\nTo undo this reset: git reset --hard %s\n", currentShort)
	}

	return nil
}

// getCurrentHeadHash returns the current HEAD commit hash.
func getCurrentHeadHash(ctx context.Context) (string, error) {
	repo, err := openRepository(ctx)
	if err != nil {
		return "", err
	}

	head, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("failed to get HEAD: %w", err)
	}

	return head.Hash().String(), nil
}

// checkResetSafety checks for potential issues before a git reset --hard.
// Returns a list of warning messages (empty if safe to proceed without warnings).
// If uncommittedChangesWarning is provided, it will be used instead of a generic warning.
func checkResetSafety(ctx context.Context, targetCommitHash string, uncommittedChangesWarning string) ([]string, error) {
	var warnings []string

	repo, err := openRepository(ctx)
	if err != nil {
		return nil, err
	}

	// Check for uncommitted changes
	if uncommittedChangesWarning != "" {
		// Use the detailed warning from strategy's CanRewind()
		warnings = append(warnings, uncommittedChangesWarning)
	} else {
		// Fall back to generic check
		worktree, err := repo.Worktree()
		if err != nil {
			return nil, fmt.Errorf("failed to get worktree: %w", err)
		}

		status, err := worktree.Status()
		if err != nil {
			return nil, fmt.Errorf("failed to get status: %w", err)
		}

		if !status.IsClean() {
			warnings = append(warnings, "• You have uncommitted changes that will be LOST")
		}
	}

	// Check if current HEAD is ahead of target (we'd be discarding commits)
	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}

	targetHash := plumbing.NewHash(targetCommitHash)

	// Count commits between target and HEAD
	commitsAhead, err := countCommitsBetween(repo, targetHash, head.Hash())
	if err != nil {
		// Non-fatal - just can't show commit count
		commitsAhead = -1
	}

	if commitsAhead > 0 {
		warnings = append(warnings, fmt.Sprintf("• %d commit(s) after this point will be orphaned", commitsAhead))
	}

	return warnings, nil
}

// countCommitsBetween counts commits between ancestor and descendant.
// Returns 0 if ancestor == descendant, -1 on error.
func countCommitsBetween(repo *git.Repository, ancestor, descendant plumbing.Hash) (int, error) {
	if ancestor == descendant {
		return 0, nil
	}

	// Walk from descendant back to ancestor
	count := 0
	current := descendant

	for count < strategy.MaxCommitTraversalDepth { // Safety limit
		if current == ancestor {
			return count, nil
		}

		commit, err := repo.CommitObject(current)
		if err != nil {
			return -1, fmt.Errorf("failed to get commit: %w", err)
		}

		if commit.NumParents() == 0 {
			// Reached root without finding ancestor - ancestor not in history
			return -1, nil
		}

		count++
		current = commit.ParentHashes[0] // Follow first parent
	}

	return -1, nil
}

// performGitResetHard performs a git reset --hard to the specified commit.
// Uses the git CLI instead of go-git because go-git's HardReset incorrectly
// deletes untracked directories (like .trace/) even when they're in .gitignore.
func performGitResetHard(ctx context.Context, commitHash string) error {
	if strings.HasPrefix(commitHash, "-") {
		return fmt.Errorf("reset failed: invalid commit hash %q", commitHash)
	}
	cmd := exec.CommandContext(ctx, "git", "reset", "--hard", commitHash)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("reset failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

// recordResetOplogEntry appends an oplog entry for a completed git reset
// --hard, resolving the reset branch's ref name via HEAD (reset --hard
// moves whatever ref HEAD currently points to, branch or detached).
// Best-effort: failures are logged, not propagated — the reset itself
// already succeeded by the time this is called.
func recordResetOplogEntry(ctx context.Context, beforeHex, afterHex string) {
	if beforeHex == "" {
		// getCurrentHeadHash() failed before the reset; nothing to record.
		return
	}
	repo, err := openRepository(ctx)
	if err != nil {
		logging.Warn(ctx, "failed to open repository for oplog entry", "error", err.Error())
		return
	}
	head, err := repo.Head()
	if err != nil {
		logging.Warn(ctx, "failed to resolve HEAD for oplog entry", "error", err.Error())
		return
	}
	if err := strategy.RecordOplogEntry(
		ctx, repo, oplog.OpResetHard, head.Name().String(),
		plumbing.NewHash(beforeHex), plumbing.NewHash(afterHex), "",
	); err != nil {
		logging.Warn(ctx, "failed to record oplog entry for reset --hard", "error", err.Error())
	}
}

// sanitizeForTerminal removes or replaces characters that cause rendering issues
// in terminal UI components. This includes emojis with skin-tone modifiers and
// other multi-codepoint characters that confuse width calculations.
func sanitizeForTerminal(s string) string {
	var result strings.Builder
	result.Grow(len(s))

	for _, r := range s {
		// Skip emoji skin tone modifiers (U+1F3FB to U+1F3FF)
		if r >= 0x1F3FB && r <= 0x1F3FF {
			continue
		}
		// Skip zero-width joiners used in emoji sequences
		if r == 0x200D {
			continue
		}
		// Skip variation selectors (U+FE00 to U+FE0F)
		if r >= 0xFE00 && r <= 0xFE0F {
			continue
		}
		// Keep printable characters and common whitespace
		if unicode.IsPrint(r) || r == '\t' || r == '\n' {
			result.WriteRune(r)
		}
	}

	return result.String()
}

// printMultiSessionResumeCommands prints resume commands for restored sessions.
// Each session may have a different agent, so per-session agent resolution is used.
func printMultiSessionResumeCommands(w, errW io.Writer, sessions []strategy.RestoredSession) {
	if len(sessions) == 0 {
		return
	}

	if len(sessions) > 1 {
		fmt.Fprintf(w, "\n✓ Restored %d sessions. To continue:\n", len(sessions))
	} else {
		fmt.Fprintf(w, "✓ Restored session %s.\n", sessions[0].SessionID)
		fmt.Fprintf(w, "\nTo continue this session:\n")
	}

	isMulti := len(sessions) > 1
	for i, sess := range sessions {
		ag, err := strategy.ResolveAgentForRewind(sess.Agent)
		if err != nil {
			fmt.Fprintf(errW, "  Warning: could not resolve agent %q for session %s, skipping\n", sess.Agent, sess.SessionID)
			continue
		}
		printSessionCommand(w, ag.FormatResumeCommand(sess.SessionID), sess.Prompt, isMulti, i == len(sessions)-1)
	}
}
