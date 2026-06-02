package cli

import (
	"errors"
	"fmt"

	"charm.land/huh/v2"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/paths"
	"github.com/spf13/cobra"
)

func newResetCmd() *cobra.Command {
	var forceFlag bool
	var sessionFlag string

	cmd := &cobra.Command{
		Use:        "reset",
		Short:      "Reset the shadow branch and session state for current HEAD",
		Deprecated: "use 'trace clean' instead (or 'trace clean --all' for repo-wide cleanup)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			// Check if in git repository before initializing logging,
			// to avoid creating .trace/logs in arbitrary directories.
			if _, err := paths.WorktreeRoot(ctx); err != nil {
				return errors.New("not a git repository")
			}

			// Initialize logging
			logging.SetLogLevelGetter(GetLogLevel)
			if err := logging.Init(ctx, ""); err == nil {
				defer logging.Close()
			}

			start := GetStrategy(ctx)

			// Handle --session flag: delegate to clean's session logic
			if sessionFlag != "" {
				return runCleanSession(ctx, cmd, start, sessionFlag, forceFlag, false, "Reset", "reset")
			}

			// Check for active sessions before bulk reset
			if !forceFlag {
				activeSessions, err := activeSessionsOnCurrentHead(ctx)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not check for active sessions: %v\n", err)
					fmt.Fprintln(cmd.ErrOrStderr(), "Use --force to override.")
					return nil
				}
				if len(activeSessions) > 0 {
					fmt.Fprintln(cmd.ErrOrStderr(), "Active sessions detected on current HEAD:")
					for _, s := range activeSessions {
						fmt.Fprintf(cmd.ErrOrStderr(), "  %s (phase: %s)\n", s.SessionID, s.Phase)
					}
					fmt.Fprintln(cmd.ErrOrStderr(), "Use --force to override or wait for sessions to finish.")
					return nil
				}
			}

			if !forceFlag {
				var confirmed bool

				form := NewAccessibleForm(
					huh.NewGroup(
						huh.NewConfirm().
							Title("Reset session data?").
							Value(&confirmed),
					),
				)

				if err := form.Run(); err != nil {
					return handleFormCancellation(cmd.OutOrStdout(), "Reset", err)
				}

				if !confirmed {
					fmt.Fprintln(cmd.OutOrStdout(), "Reset cancelled.")
					return nil
				}
			}

			if err := start.Reset(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
				return fmt.Errorf("reset failed: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&forceFlag, "force", "f", false, "Skip confirmation prompt and override active session guard")
	cmd.Flags().StringVar(&sessionFlag, "session", "", "Reset a specific session by ID")

	return cmd
}
