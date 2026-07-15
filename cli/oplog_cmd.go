package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/GrayCodeAI/trace/cli/oplog"
	"github.com/GrayCodeAI/trace/cli/paths"

	"github.com/spf13/cobra"
)

func newOplogCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Show trace's operation log (rewinds, resets, forks, cleanups)",
		Long: `Log shows the state-mutating operations trace has recorded, newest first —
the same history 'trace undo' steps back through. This is separate from git
log: it tracks trace's own ref mutations (including ones that never touch
HEAD, like a shadow-branch rewind), not commit history.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if _, err := paths.WorktreeRoot(ctx); err != nil {
				cmd.SilenceUsage = true
				return errors.New("not a git repository")
			}
			return runOplogList(ctx, cmd.OutOrStdout(), limit)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "maximum number of entries to show (0 = all)")
	return cmd
}

func runOplogList(ctx context.Context, w io.Writer, limit int) error {
	repo, err := openRepository(ctx)
	if err != nil {
		return fmt.Errorf("not a git repository: %w", err)
	}

	entries, err := oplog.List(repo, limit)
	if err != nil {
		return fmt.Errorf("failed to read operation log: %w", err)
	}
	if len(entries) == 0 {
		fmt.Fprintln(w, "No operations recorded yet.")
		return nil
	}

	for _, e := range entries {
		fmt.Fprintf(w, "%s  %-11s %s", e.Timestamp.Format("2006-01-02 15:04:05"), e.Op, e.Ref)
		if len(e.BeforeHash) >= 7 && len(e.AfterHash) >= 7 {
			fmt.Fprintf(w, "  %s -> %s", e.BeforeHash[:7], e.AfterHash[:7])
		}
		if e.CheckpointID != "" {
			fmt.Fprintf(w, "  (%s)", e.CheckpointID)
		}
		fmt.Fprintln(w)
	}
	return nil
}
