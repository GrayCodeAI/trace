package cli

import (
	"fmt"
	"path/filepath"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/paths"
	"github.com/spf13/cobra"
)

func newTraceCmd() *cobra.Command {
	var last int
	var hookFilter string

	cmd := &cobra.Command{
		Use:   "trace",
		Short: "Show hook performance traces",
		Long: `Show timing information for recent hook invocations.

Traces are emitted at DEBUG log level. To enable them, either:
  - Set TRACE_LOG_LEVEL=DEBUG in your shell profile
  - Add "log_level": "DEBUG" to .trace/settings.json

Examples:
  trace trace                     Show the most recent hook trace
  trace trace --last 5            Show the last 5 hook traces
  trace trace --hook post-commit  Show only post-commit hook traces`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if last < 1 {
				return fmt.Errorf("--last must be at least 1, got %d", last)
			}

			repoRoot, err := paths.WorktreeRoot(cmd.Context())
			if err != nil {
				cmd.SilenceUsage = true
				fmt.Fprintln(cmd.ErrOrStderr(), "Not a git repository. Please run from within a git repository.")
				return NewSilentError(fmt.Errorf("not a git repository: %w", err))
			}

			logFile := filepath.Join(repoRoot, logging.LogsDir, "trace.log")

			entries, err := collectTraceEntries(logFile, last, hookFilter)
			if err != nil {
				return fmt.Errorf("collecting trace entries: %w", err)
			}

			renderTraceEntries(cmd.OutOrStdout(), entries)
			return nil
		},
	}

	cmd.Flags().IntVar(&last, "last", 1, "Show last N hook invocations")
	cmd.Flags().StringVar(&hookFilter, "hook", "", "Filter by hook type (e.g. post-commit, prepare-commit-msg, pre-push)")

	return cmd
}
