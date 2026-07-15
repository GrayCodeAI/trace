package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/oplog"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/strategy"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/spf13/cobra"
)

func newUndoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "undo",
		Short: "Undo the most recent rewind, reset, fork, or checkpoint cleanup",
		Long: `Undo reverts the most recent state-mutating trace operation, using trace's
own operation log — separate from git's reflog, and the only record that
covers a shadow-branch rewind or a checkpoint-cleanup ref rewrite, neither of
which touch HEAD and so never appear in the reflog at all.

Only the single most recent operation is undone. Run 'trace undo' again to
step back further, or 'trace log' to see the full operation history.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if _, err := paths.WorktreeRoot(ctx); err != nil {
				cmd.SilenceUsage = true
				return errors.New("not a git repository")
			}
			return runUndo(ctx, cmd.OutOrStdout())
		},
	}
	return cmd
}

func runUndo(ctx context.Context, w io.Writer) error {
	repo, err := openRepository(ctx)
	if err != nil {
		return fmt.Errorf("not a git repository: %w", err)
	}

	entries, err := oplog.List(repo, 1)
	if err != nil {
		return fmt.Errorf("failed to read operation log: %w", err)
	}
	if len(entries) == 0 {
		fmt.Fprintln(w, "Nothing to undo — the operation log is empty.")
		return nil
	}
	entry := entries[0]

	// Deliberately don't auto-chain: undoing an undo silently could produce
	// a confusing double-reversal. Require the user to look at 'trace log'
	// and act on a specific entry instead.
	if entry.Op == oplog.OpUndo {
		return fmt.Errorf("the most recent operation is itself an undo (recorded %s) — inspect 'trace log' and act deliberately rather than auto-chaining undos", entry.Timestamp.Format("2006-01-02 15:04:05"))
	}

	refName := plumbing.ReferenceName(entry.Ref)
	beforeHash := plumbing.NewHash(entry.BeforeHash)
	originalAfterHash := plumbing.NewHash(entry.AfterHash)

	var summary string
	switch {
	case entry.Op == oplog.OpResetHard:
		// git reset --hard moves the ref, the index, AND the working tree
		// together — restoring only the ref via SetReference would leave
		// files on disk in the post-reset state while the branch pointer
		// claims otherwise, a real desync. Reuse the exact same code path
		// the original operation used so the semantics match precisely.
		// (performGitResetHard shells out to the `git` CLI, which serializes
		// against other git processes on the same repo via its own locking,
		// so it does not need StorerMu here.)
		if err := performGitResetHard(ctx, entry.BeforeHash); err != nil {
			return fmt.Errorf("failed to reset %s back to %s: %w", refName, beforeHash.String()[:7], err)
		}
		summary = fmt.Sprintf("Reset %s back to %s (undoing a reset --hard) — working tree and index restored.", refName.Short(), beforeHash.String()[:7])
	case beforeHash.IsZero():
		// The operation created this ref from nothing (e.g. fork's new
		// branch) — undo removes it rather than trying to point it "back"
		// somewhere that never existed. Direct repo.Storer access, so
		// serialize it against concurrent V2GitStore writes under
		// StorerMu (go-git's storer is not concurrency-safe).
		checkpoint.StorerMu.Lock()
		if err := repo.Storer.RemoveReference(refName); err != nil {
			checkpoint.StorerMu.Unlock()
			return fmt.Errorf("failed to remove ref %s: %w", refName, err)
		}
		checkpoint.StorerMu.Unlock()
		summary = fmt.Sprintf("Removed %s (created by %s, nothing to restore it to).", refName.Short(), entry.Op)
	default:
		checkpoint.StorerMu.Lock()
		if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, beforeHash)); err != nil {
			checkpoint.StorerMu.Unlock()
			return fmt.Errorf("failed to restore ref %s: %w", refName, err)
		}
		checkpoint.StorerMu.Unlock()
		summary = fmt.Sprintf("Restored %s to %s (undoing a %s).", refName.Short(), beforeHash.String()[:7], entry.Op)
	}

	// Record the undo itself, matching jj's operation-log symmetry: undoing
	// is itself an operation, so it's visible in 'trace log' and could in
	// principle be undone too (deliberately not automated, per the guard
	// above). Held under StorerMu so this oplog ref write lands together with
	// the ref mutation above from the perspective of other storer writers.
	checkpoint.StorerMu.Lock()
	logErr := strategy.RecordOplogEntry(ctx, repo, oplog.OpUndo, entry.Ref, originalAfterHash, beforeHash, entry.CheckpointID)
	checkpoint.StorerMu.Unlock()
	if logErr != nil {
		logging.Warn(ctx, "failed to record oplog entry for undo", "error", logErr.Error())
	}

	fmt.Fprintln(w, summary)
	return nil
}
