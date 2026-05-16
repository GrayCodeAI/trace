package cli

import (
	"fmt"
	"runtime"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/paths"
	cliReview "github.com/GrayCodeAI/trace/cmd/trace/cli/review"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/telemetry"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/versioncheck"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/versioninfo"
	"github.com/spf13/cobra"
)

const gettingStarted = `

Getting Started:
  To get started with Trace CLI, run 'trace enable' to enable
  session tracking in your repository, then 'trace agent add <name>'
  to install hooks for a specific agent. For more information, visit:
  https://docs.trace.io/introduction

`

const accessibilityHelp = `
Environment Variables:
  ACCESSIBLE    Set to any value (e.g., ACCESSIBLE=1) to enable accessibility
                mode. This uses simpler text prompts instead of interactive
                TUI elements, which works better with screen readers.
`

func NewRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "trace",
		Short:   "Trace CLI",
		Long:    "The command-line interface for Trace" + gettingStarted + accessibilityHelp,
		Version: versioninfo.Version,
		// Let main.go handle error printing to avoid duplication
		SilenceErrors: true,
		SilenceUsage:  true,
		// Hide completion command from help but keep it functional
		CompletionOptions: cobra.CompletionOptions{
			HiddenDefaultCmd: true,
		},
		PersistentPostRun: func(cmd *cobra.Command, _ []string) {
			// Skip for hidden commands (walk parent chain — Cobra doesn't propagate Hidden)
			for c := cmd; c != nil; c = c.Parent() {
				if c.Hidden {
					return
				}
			}

			// Load settings once for telemetry and version check
			var telemetryEnabled *bool
			settings, err := LoadTraceSettings(cmd.Context())
			if err == nil {
				telemetryEnabled = settings.Telemetry
			}

			// Check if telemetry is enabled
			if telemetryEnabled != nil && *telemetryEnabled {
				// Use detached tracking (non-blocking)
				installedAgents := GetAgentsWithHooksInstalled(cmd.Context())
				agentStr := JoinAgentNames(installedAgents)
				telemetry.TrackCommandDetached(cmd, agentStr, settings.Enabled, versioninfo.Version)
			}

			// Version check and notification (synchronous with 2s timeout)
			// Runs AFTER command completes to avoid interfering with interactive modes
			versioncheck.CheckAndNotify(cmd.Context(), cmd.OutOrStdout(), versioninfo.Version)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			// If we're in a git repo but Trace isn't set up yet, start the setup flow
			if _, err := paths.WorktreeRoot(ctx); err == nil && !settings.IsSetUpAny(ctx) {
				return runSetupFlow(ctx, cmd.OutOrStdout(), EnableOptions{})
			}
			return cmd.Help()
		},
	}

	// Noun groups (canonical homes for subcommands).
	cmd.AddCommand(newSessionsCmd())        // 'session' (with 'sessions' as Cobra alias)
	cmd.AddCommand(newCheckpointGroupCmd()) // 'checkpoint' / 'cp' / 'checkpoints'
	cmd.AddCommand(newAgentGroupCmd())      // 'agent'
	cmd.AddCommand(newAuthCmd())            // 'auth'
	cmd.AddCommand(newDoctorCmd())          // 'doctor' (group: trace/logs/bundle)

	// Top-level lifecycle and standalone commands.
	cmd.AddCommand(newCleanCmd())
	cmd.AddCommand(newSetupCmd()) // 'configure' — non-agent settings; agent CRUD lives under 'agent'
	cmd.AddCommand(newEnableCmd())
	cmd.AddCommand(newDisableCmd())
	cmd.AddCommand(newStatusCmd())
	cmd.AddCommand(newLoginCmd())
	cmd.AddCommand(newLogoutCmd())
	cmd.AddCommand(newVersionCmd())
	cmd.AddCommand(newDispatchCmd())
	cmd.AddCommand(newActivityCmd())
	cmd.AddCommand(newLabsCmd())                                                // 'labs' (experimental workflow discovery)
	cmd.AddCommand(newPluginGroupCmd())                                         // 'plugin' (managed install/list/remove)
	cmd.AddCommand(cliReview.NewCommand(buildReviewDeps(newReviewAttachCmd()))) // hidden during maturation; runs configured review skills
	cmd.AddCommand(newRecapCmd())

	// Hidden top-level shortcuts. Functional but print a deprecation hint.
	cmd.AddCommand(hideAsAlias(newRewindCmd(), "trace checkpoint rewind"))
	cmd.AddCommand(hideAsAlias(newResumeCmd(), "trace session resume"))
	cmd.AddCommand(hideAsAlias(newAttachCmd(), "trace session attach"))
	cmd.AddCommand(hideAsAlias(newExplainCmd(), "trace checkpoint explain"))
	cmd.AddCommand(hideAsAlias(newTraceCmd(), "trace doctor trace"))
	cmd.AddCommand(newSearchCmd()) // 'trace search' = 'checkpoint search' (hidden, no hint)

	// Deprecated top-level alias (functional; reset.go marks it Deprecated).
	cmd.AddCommand(newResetCmd())

	// Hidden infrastructure.
	cmd.AddCommand(newHooksCmd())
	cmd.AddCommand(newTrailCmd())
	cmd.AddCommand(newSendAnalyticsCmd())
	cmd.AddCommand(newCurlBashPostInstallCmd())
	cmd.AddCommand(newMigrateCmd())

	cmd.SetVersionTemplate(versionString())

	// Replace default help command with custom one that supports -t flag
	cmd.SetHelpCommand(NewHelpCmd(cmd))

	return cmd
}

func versionString() string {
	return fmt.Sprintf("Trace CLI %s (%s)\nGo version: %s\nOS/Arch: %s/%s\n",
		versioninfo.Version, versioninfo.Commit, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show build information",
		Run: func(cmd *cobra.Command, _ []string) {
			// Use OutOrStdout explicitly — cobra's cmd.Print() defaults to
			// stderr in v1.10+, but version output should go to stdout.
			fmt.Fprint(cmd.OutOrStdout(), versionString())
		},
	}
}

// newSendAnalyticsCmd creates the hidden command for sending analytics from a detached subprocess.
// This command is invoked by TrackCommandDetached and should not be called directly by users.
func newSendAnalyticsCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "__send_analytics",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		Run: func(_ *cobra.Command, args []string) {
			telemetry.SendEvent(args[0])
		},
	}
}
