package cli

import (
	"errors"

	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/spf13/cobra"
)

// newCheckpointGroupCmd builds the `trace checkpoint` parent command and
// registers list/explain/rewind/search as children.
func newCheckpointGroupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "checkpoint",
		Aliases: []string{"cp", "checkpoints"},
		Short:   "Inspect, rewind, and search checkpoints",
		Long: `Operations on checkpoints — the persistent records of agent work tied to commits.

Commands:
  list     List checkpoints on the current branch
  explain  Explain a checkpoint, commit, or session
  rewind   Browse and rewind to a checkpoint
  search   Search checkpoints (semantic + keyword)

Examples:
  trace checkpoint list
  trace checkpoint explain <id|sha>
  trace checkpoint rewind --to <id>
  trace checkpoint search "fix login"`,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := paths.WorktreeRoot(cmd.Context()); err != nil {
				return errors.New("not a git repository")
			}
			return nil
		},
	}

	cmd.AddCommand(newCheckpointListCmd())
	cmd.AddCommand(newExplainCmd())
	cmd.AddCommand(newRewindCmd())
	cmd.AddCommand(newCheckpointSearchCmd())

	return cmd
}

func newCheckpointSearchCmd() *cobra.Command {
	cmd := newSearchCmd()
	cmd.Hidden = false
	return cmd
}

// newCheckpointListCmd wraps the existing branch-default list view.
func newCheckpointListCmd() *cobra.Command {
	var sessionFlag string
	var noPagerFlag bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List checkpoints on the current branch",
		Long: `List checkpoints on the current branch.

Optionally filter by session ID with --session.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if checkDisabledGuard(cmd.Context(), cmd.OutOrStdout()) {
				return nil
			}
			return runExplainBranchWithFilter(cmd.Context(), cmd.OutOrStdout(), noPagerFlag, sessionFlag)
		},
	}

	cmd.Flags().StringVar(&sessionFlag, "session", "", "Filter checkpoints by session ID (or prefix)")
	cmd.Flags().BoolVar(&noPagerFlag, "no-pager", false, "Disable pager output")
	return cmd
}
