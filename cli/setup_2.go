package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/external"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/interactive"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cli/strategy"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func newEnableCmd() *cobra.Command {
	var opts EnableOptions
	var ignoreUntracked bool
	var agentName string
	var bootstrapOpts GitHubBootstrapOptions

	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Enable Trace in current repository",
		Long: `Enable Trace with session tracking for your AI agent workflows.

If Trace is not yet configured, this runs the full configuration flow.
If Trace is already configured but disabled, this re-enables it.

If the current directory is not a git repository, Trace can initialize one
for you and (optionally) create a matching GitHub repository via the gh CLI.`,
		RunE: func(cmd *cobra.Command, _ []string) (runErr error) {
			ctx := cmd.Context()
			// Check if we're in a git repository first. If not, offer to
			// bootstrap one (git init + optional GitHub repo). If the user
			// declines, fall back to the legacy prerequisite error.
			//
			// The bootstrap runs in two phases: phase 1 (git init + identity
			// + gather GitHub choices) before agent setup, phase 2
			// (initial commit + gh repo create + push) after agent setup so
			// the initial commit captures the .trace/, .claude/, hooks, and
			// settings files that setup writes.
			var bootstrap *bootstrapState
			if _, err := paths.WorktreeRoot(ctx); err != nil {
				bootstrapOpts.Yes = opts.Yes
				state, bootstrapErr := runGitHubBootstrapInit(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), bootstrapOpts)
				if errors.Is(bootstrapErr, errBootstrapDeclined) {
					fmt.Fprintln(cmd.ErrOrStderr(), "Not a git repository. Please run 'trace enable' from within a git repository, or pass --init-repo to initialize one here.")
					return NewSilentError(errors.New("not a git repository"))
				}
				if errors.Is(bootstrapErr, errBootstrapInterrupted) {
					fmt.Fprintln(cmd.ErrOrStderr(), "Bootstrap cancelled. A local git repository has been initialized but setup didn't complete. Run `trace enable` again to continue.")
					return NewSilentError(errors.New("bootstrap interrupted"))
				}
				if bootstrapErr != nil {
					return bootstrapErr
				}
				bootstrap = state
				// Let the enable flow know that we'll be handling the final
				// "done" summary from the bootstrap finalize step.
				opts.SuppressDoneMessage = true
				// Re-check after bootstrap.
				if _, err := paths.WorktreeRoot(ctx); err != nil {
					return fmt.Errorf("bootstrap finished but no git repository detected: %w", err)
				}
				// Visual separator between bootstrap init and agent setup.
				printBootstrapSection(cmd.OutOrStdout(), "Enabling Trace")
				// On the way out (if setup succeeded), create the initial
				// commit and push to the GitHub repo. If setup returned an
				// error, skip the finalize — the user can fix the issue and
				// re-run; any partial state is just untracked files.
				defer func() {
					if runErr != nil || bootstrap == nil {
						return
					}
					if err := runGitHubBootstrapFinalize(ctx, cmd.OutOrStdout(), bootstrap); err != nil {
						runErr = err
					}
				}()
			}

			if err := validateSetupFlags(opts.UseLocalSettings, opts.UseProjectSettings); err != nil {
				return err
			}

			// Discover external agent plugins early so --agent can find them.
			// Use DiscoverAndRegisterAlways so that --agent works on fresh repos
			// where the external_agents setting hasn't been persisted yet.
			external.DiscoverAndRegisterAlways(ctx)

			// Non-interactive mode if --agent flag is provided
			if cmd.Flags().Changed(agentFlagName) && agentName == "" {
				printMissingAgentError(cmd.ErrOrStderr())
				return NewSilentError(errors.New("missing agent name"))
			}

			if agentName != "" {
				ag, err := agent.Get(types.AgentName(agentName))
				if err != nil {
					printWrongAgentError(cmd.ErrOrStderr(), agentName)
					return NewSilentError(errors.New("wrong agent name"))
				}
				// --agent is a targeted operation: set up this specific agent without
				// affecting other agents. Unlike the interactive path, it does not
				// uninstall hooks for other previously-enabled agents.
				return setupAgentHooksNonInteractive(ctx, cmd.OutOrStdout(), ag, opts)
			}

			// Any setup-mutating flags should behave like `configure` on repos that
			// are already set up. Bare `enable` remains the lightweight re-enable path.
			if settings.IsSetUpAny(ctx) {
				usedSetupFlow := enableUsesSetupFlow(cmd, agentName)
				if usedSetupFlow {
					if hasStrategyFlags(cmd) {
						if err := updateStrategyOptions(ctx, cmd.OutOrStdout(), opts); err != nil {
							return err
						}
					}
					if enableNeedsAgentManagement(cmd) {
						var selectFn func(available []string) ([]string, error)
						if opts.Yes {
							selectFn = selectAllAgents
						}
						if err := runManageAgents(ctx, cmd.OutOrStdout(), opts, selectFn); err != nil {
							return err
						}
					}
				}

				enabled, err := IsEnabled(ctx)
				if err == nil && enabled {
					w := cmd.OutOrStdout()
					if !usedSetupFlow {
						fmt.Fprintln(w, "Trace is already enabled.")
					}
					printEnabledStatus(ctx, w)
					return nil
				}
				return runEnable(ctx, cmd.OutOrStdout(), opts.UseProjectSettings)
			}

			// Fresh repo — run full setup flow
			return runSetupFlow(ctx, cmd.OutOrStdout(), opts)
		},
	}

	cmd.Flags().BoolVar(&opts.LocalDev, flagLocalDev, false, "Use go run instead of trace binary for hooks")
	cmd.Flags().MarkHidden(flagLocalDev) //nolint:errcheck,gosec // flag is defined above
	cmd.Flags().BoolVar(&ignoreUntracked, "ignore-untracked", false, "Commit all new files without tracking pre-existing untracked files")
	cmd.Flags().MarkHidden("ignore-untracked") //nolint:errcheck,gosec // flag is defined above
	cmd.Flags().BoolVar(&opts.UseLocalSettings, "local", false, "Write settings to .trace/settings.local.json instead of .trace/settings.json")
	cmd.Flags().BoolVar(&opts.UseProjectSettings, "project", false, "Write settings to .trace/settings.json even if it already exists")
	cmd.Flags().StringVar(&agentName, agentFlagName, "", "Agent to set up hooks for (e.g., "+strings.Join(agent.StringList(), ", ")+"; external agents on $PATH are also available). Enables non-interactive mode.")
	cmd.Flags().BoolVarP(&opts.ForceHooks, flagForce, "f", false, "Force reinstall hooks (removes existing Trace hooks first)")
	cmd.Flags().BoolVar(&opts.SkipPushSessions, flagSkipPushSessions, false, "Disable automatic pushing of session logs on git push")
	cmd.Flags().StringVar(&opts.CheckpointRemote, flagCheckpointRemote, "", "Checkpoint remote in provider:owner/repo format (e.g., github:org/checkpoints-repo)")
	cmd.Flags().BoolVar(&opts.Telemetry, flagTelemetry, true, "Enable anonymous usage analytics")
	cmd.Flags().BoolVar(&opts.AbsoluteGitHookPath, flagAbsoluteGitHookPath, false, "Embed full binary path in git hooks (for GUI git clients that don't source shell profiles)")
	cmd.Flags().BoolVarP(&opts.Yes, "yes", "y", false, "Accept all defaults without prompting (in a non-repo directory: init git, create private GitHub repo, commit; then enable all agents and accept telemetry)")

	// Bootstrap flags for non-git-repo folders.
	cmd.Flags().BoolVar(&bootstrapOpts.InitRepo, "init-repo", false, "If not a git repo, initialize one non-interactively")
	cmd.Flags().BoolVar(&bootstrapOpts.NoInitRepo, "no-init-repo", false, "If not a git repo, exit instead of prompting to initialize one")
	cmd.Flags().StringVar(&bootstrapOpts.RepoName, "repo-name", "", "GitHub repository name for the new repo (used when bootstrapping)")
	cmd.Flags().StringVar(&bootstrapOpts.RepoOwner, "repo-owner", "", "GitHub user or organization login for the new repo")
	cmd.Flags().StringVar(&bootstrapOpts.RepoVisibility, "repo-visibility", "", "GitHub repository visibility: public, private, or internal")
	cmd.Flags().BoolVar(&bootstrapOpts.NoGitHub, "no-github", false, "Initialize local git repo only; skip creating a GitHub remote")
	cmd.Flags().StringVar(&bootstrapOpts.InitialCommitMessage, "initial-commit-message", "", "Commit message for the initial commit when bootstrapping a new repo")
	cmd.Flags().BoolVar(&bootstrapOpts.SkipInitialCommit, "skip-initial-commit", false, "Don't create the initial commit when bootstrapping a new repo")
	cmd.MarkFlagsMutuallyExclusive("init-repo", "no-init-repo")
	cmd.MarkFlagsMutuallyExclusive("initial-commit-message", "skip-initial-commit")

	// Provide a helpful error when --agent is used without a value
	defaultFlagErr := cmd.FlagErrorFunc()
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		var valErr *pflag.ValueRequiredError
		if errors.As(err, &valErr) && valErr.GetSpecifiedName() == agentFlagName {
			printMissingAgentError(c.ErrOrStderr())
			return NewSilentError(errors.New("missing agent name"))
		}
		return defaultFlagErr(c, err)
	})

	return cmd
}

func newDisableCmd() *cobra.Command {
	var useProjectSettings bool
	var uninstall bool
	var force bool

	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Disable Trace in current repository",
		Long: `Disable Trace integrations in the current repository.

By default, this command will disable Trace. Hooks will exit silently and commands will
show a disabled message.

To completely remove Trace integrations from this repository, use --uninstall:
  - .trace/ directory (settings, logs, metadata)
  - Git hooks (prepare-commit-msg, commit-msg, post-commit, pre-push)
  - Session state files (.git/trace-sessions/)
  - Shadow branches (trace/<hash>)
  - Agent hooks`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if uninstall {
				return runUninstall(ctx, cmd.OutOrStdout(), cmd.ErrOrStderr(), force)
			}
			return runDisable(ctx, cmd.OutOrStdout(), useProjectSettings)
		},
	}

	cmd.Flags().BoolVar(&useProjectSettings, "project", false, "Update .trace/settings.json instead of .trace/settings.local.json")
	cmd.Flags().BoolVar(&uninstall, "uninstall", false, "Completely remove Trace from this repository")
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt (use with --uninstall)")

	return cmd
}

// runEnableInteractive runs the interactive enable flow.
// agents must be provided by the caller (via detectOrSelectAgent).
func runEnableInteractive(ctx context.Context, w io.Writer, agents []agent.Agent, opts EnableOptions) error {
	// Uninstall hooks for agents that were previously active but are no longer selected
	if err := uninstallDeselectedAgentHooks(ctx, w, agents); err != nil {
		return fmt.Errorf("failed to clean up deselected agents: %w", err)
	}

	// Setup agent hooks for all selected agents
	for _, ag := range agents {
		if _, err := setupAgentHooks(ctx, w, ag, opts.LocalDev, opts.ForceHooks); err != nil {
			return fmt.Errorf("failed to setup %s hooks: %w", ag.Type(), err)
		}
	}

	// Setup .trace directory
	if _, err := setupTraceDirectory(ctx); err != nil {
		return fmt.Errorf("failed to setup .trace directory: %w", err)
	}

	// Load existing settings to preserve other options (like strategy_options.push)
	settings, err := LoadTraceSettings(ctx)
	if err != nil {
		// If we can't load, start with defaults
		settings = &TraceSettings{}
	}
	// Update the specific fields
	settings.Enabled = true
	if opts.LocalDev {
		settings.LocalDev = true
	}
	if opts.AbsoluteGitHookPath {
		settings.AbsoluteGitHookPath = true
	}

	// Auto-enable external_agents if any selected agent is external.
	for _, ag := range agents {
		if external.IsExternal(ag) {
			settings.ExternalAgents = true
			break
		}
	}

	opts.applyStrategyOptions(settings)

	// Determine which settings file to write to
	// First run always creates settings.json (no prompt)
	traceDirAbs, err := paths.AbsPath(ctx, paths.TraceDir)
	if err != nil {
		traceDirAbs = paths.TraceDir // Fallback to relative
	}
	shouldUseLocal, showNotification := determineSettingsTarget(traceDirAbs, opts.UseLocalSettings, opts.UseProjectSettings)

	if showNotification {
		fmt.Fprintln(w, "Info: Project settings exist. Saving to settings.local.json instead.")
		fmt.Fprintln(w, "  Use --project to update the project settings file.")
	}

	// Save settings to the appropriate file.
	targetFile := TraceSettingsFile
	if shouldUseLocal {
		targetFile = TraceSettingsLocalFile
	}
	saveSettings := func() error {
		return saveSettingsToTarget(ctx, settings, targetFile)
	}
	if err := saveSettings(); err != nil {
		return fmt.Errorf("failed to save settings: %w", err)
	}

	// Use settings values (merged from existing config + flags) for hook installation
	// This ensures re-running `trace enable` without flags preserves existing settings
	if _, err := strategy.InstallGitHook(ctx, true, settings.LocalDev, settings.AbsoluteGitHookPath); err != nil {
		return fmt.Errorf("failed to install git hooks: %w", err)
	}
	strategy.CheckAndWarnHookManagers(ctx, w, settings.LocalDev, settings.AbsoluteGitHookPath)
	fmt.Fprintln(w, "  ✓ Installed hooks")

	configDisplay := configDisplayProject
	if shouldUseLocal {
		configDisplay = configDisplayLocal
	}
	fmt.Fprintln(w, "  ✓ Configured project")
	fmt.Fprintf(w, "    %s\n", configDisplay)

	var vercelPromptFn func() (bool, error)
	if opts.Yes {
		vercelPromptFn = func() (bool, error) { return true, nil }
	}
	if _, err := maybePromptVercelDeploymentDisable(ctx, w, targetFile, vercelPromptFn); err != nil {
		return err
	}

	// Ask about telemetry consent (only if not already asked).
	// --yes skips the interactive prompt but still respects --telemetry=false
	// and TRACE_TELEMETRY_OPTOUT — it only auto-answers the interactive question.
	if opts.Yes {
		if !opts.Telemetry || os.Getenv("TRACE_TELEMETRY_OPTOUT") != "" {
			f := false
			settings.Telemetry = &f
		} else if settings.Telemetry == nil {
			t := true
			settings.Telemetry = &t
		}
	} else if err := promptTelemetryConsent(settings, opts.Telemetry); err != nil {
		return fmt.Errorf("telemetry consent: %w", err)
	}
	// Save again to persist telemetry choice
	if err := saveSettings(); err != nil {
		return fmt.Errorf("failed to save settings: %w", err)
	}

	if err := strategy.EnsureSetup(ctx); err != nil {
		return fmt.Errorf("failed to setup strategy: %w", err)
	}

	if opts.SuppressDoneMessage {
		// Bootstrap finalize will print its own completion summary after
		// making the initial commit and pushing.
		return nil
	}

	fmt.Fprintln(w, "\nReady.")

	// Note about empty repos at the end, after setup is complete
	if repo, err := strategy.OpenRepository(ctx); err == nil && strategy.IsEmptyRepository(repo) {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Note: Session checkpoints require at least one commit. To get started,")
		fmt.Fprintln(w, "commit the configuration files (e.g. .trace/, .claude/).")
	}

	return nil
}

// printEnabledStatus prints agents and a hint about `trace agent`.
func printEnabledStatus(ctx context.Context, w io.Writer) {
	if displayNames := InstalledAgentDisplayNames(ctx); len(displayNames) > 0 {
		fmt.Fprintf(w, "Agents: %s\n", strings.Join(displayNames, ", "))
	}
	fmt.Fprintln(w, "\nTo add more agents, run `trace agent add <name>`.")
}

// runEnable sets the enabled flag in settings.
// Writes to the target file (local by default, project with --project),
// and also updates the other file if it exists, so they can't get out of sync.
func runEnable(ctx context.Context, w io.Writer, useProjectSettings bool) error {
	s, err := LoadTraceSettings(ctx)
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	s.Enabled = true

	if err := saveEnabledState(ctx, s, useProjectSettings); err != nil {
		return err
	}

	fmt.Fprintln(w, "Trace is now enabled.")
	printEnabledStatus(ctx, w)
	return nil
}

func runDisable(ctx context.Context, w io.Writer, useProjectSettings bool) error {
	s, err := LoadTraceSettings(ctx)
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	s.Enabled = false

	if err := saveEnabledState(ctx, s, useProjectSettings); err != nil {
		return err
	}

	fmt.Fprintln(w, "Trace is now disabled.")
	return nil
}

// saveEnabledState writes settings to the target file and also updates the
// other settings file if it exists, preventing local/project from getting
// out of sync on the enabled field.
func saveEnabledState(ctx context.Context, s *TraceSettings, useProjectSettings bool) error {
	if useProjectSettings {
		if err := SaveTraceSettings(ctx, s); err != nil {
			return fmt.Errorf("failed to save settings: %w", err)
		}
		// Also update local if it exists, so it doesn't override
		if localExists(ctx) {
			if err := SaveTraceSettingsLocal(ctx, s); err != nil {
				return fmt.Errorf("failed to save local settings: %w", err)
			}
		}
	} else {
		if err := SaveTraceSettingsLocal(ctx, s); err != nil {
			return fmt.Errorf("failed to save local settings: %w", err)
		}
	}
	return nil
}

// localExists checks if settings.local.json exists.
func localExists(ctx context.Context) bool {
	localFile := settings.TraceSettingsLocalFile
	if abs, err := paths.AbsPath(ctx, localFile); err == nil {
		localFile = abs
	}
	_, err := os.Stat(localFile)
	return err == nil
}

// runRemoveAgent removes hooks for a specific agent.
func runRemoveAgent(ctx context.Context, w io.Writer, name string) error {
	ag, err := agent.Get(types.AgentName(name))
	if err != nil {
		printWrongAgentError(w, name)
		return NewSilentError(errors.New("wrong agent name"))
	}

	hookAgent, ok := agent.AsHookSupport(ag)
	if !ok {
		return fmt.Errorf("agent %s does not support hooks", name)
	}

	if !hookAgent.AreHooksInstalled(ctx) {
		fmt.Fprintf(w, "%s hooks are not installed.\n", ag.Type())
		return nil
	}

	if err := hookAgent.UninstallHooks(ctx); err != nil {
		return fmt.Errorf("failed to remove %s hooks: %w", ag.Type(), err)
	}

	fmt.Fprintf(w, "Removed %s hooks.\n", ag.Type())
	return nil
}

// DisabledMessage is the message shown when Trace is disabled
const DisabledMessage = "Trace is disabled. Run `trace enable` to re-enable."

// checkDisabledGuard checks if Trace is disabled and prints a message if so.
// Returns true if the caller should exit (i.e., Trace is disabled).
// On error reading settings, defaults to enabled (returns false).
func checkDisabledGuard(ctx context.Context, w io.Writer) bool {
	enabled, err := IsEnabled(ctx)
	if err != nil {
		// Default to enabled on error
		return false
	}
	if !enabled {
		fmt.Fprintln(w, DisabledMessage)
		return true
	}
	return false
}

// uninstallDeselectedAgentHooks removes hooks for agents that were previously
// installed but are not in the selected list. This handles the case where a user
// re-runs `trace enable` and deselects an agent.
func uninstallDeselectedAgentHooks(ctx context.Context, w io.Writer, selectedAgents []agent.Agent) error {
	installedNames := GetAgentsWithHooksInstalled(ctx)
	if len(installedNames) == 0 {
		return nil
	}

	selectedSet := make(map[types.AgentName]struct{}, len(selectedAgents))
	for _, ag := range selectedAgents {
		selectedSet[ag.Name()] = struct{}{}
	}

	var errs []error
	for _, name := range installedNames {
		if _, selected := selectedSet[name]; selected {
			continue
		}
		ag, err := agent.Get(name)
		if err != nil {
			continue
		}
		hookAgent, ok := agent.AsHookSupport(ag)
		if !ok {
			continue
		}
		if err := hookAgent.UninstallHooks(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to uninstall %s hooks: %w", ag.Type(), err))
		} else {
			fmt.Fprintf(w, "Removed %s hooks\n", ag.Type())
		}
	}
	return errors.Join(errs...)
}

// setupAgentHooks sets up hooks for a given agent.
// Returns the number of hooks installed (0 if already installed).
func setupAgentHooks(ctx context.Context, w io.Writer, ag agent.Agent, localDev, forceHooks bool) (int, error) {
	hookAgent, ok := agent.AsHookSupport(ag)
	if !ok {
		return 0, fmt.Errorf("agent %s does not support hooks", ag.Name())
	}

	count, err := hookAgent.InstallHooks(ctx, localDev, forceHooks)
	if err != nil {
		return 0, fmt.Errorf("failed to install %s hooks: %w", ag.Name(), err)
	}

	scaffoldResult, err := scaffoldSearchSubagent(ctx, ag)
	if err != nil {
		return 0, fmt.Errorf("failed to scaffold %s search subagent: %w", ag.Name(), err)
	}
	reportSearchSubagentScaffold(w, ag, scaffoldResult)

	return count, nil
}

// detectOrSelectAgent tries to auto-detect agents, or prompts the user to select.
// Returns the detected/selected agents and any error.
//
// On first run (no hooks installed):
//   - Single detected built-in agent: used automatically
//   - Single detected external agent: interactive multi-select prompt
//   - Multiple/no detected agents: interactive multi-select prompt
//
// On re-run (hooks already installed):
//   - Always shows the interactive multi-select
//   - Pre-selects only agents that have hooks installed (respects prior deselection)
//
// selectFn overrides the interactive prompt for testing. When nil, the real form is used.
// It receives available agent names and returns the selected names.
func detectOrSelectAgent(ctx context.Context, w io.Writer, selectFn func(available []string) ([]string, error)) ([]agent.Agent, error) {
	// Check for agents with hooks already installed (re-run detection)
	installedAgentNames := GetAgentsWithHooksInstalled(ctx)
	hasInstalledHooks := len(installedAgentNames) > 0

	// Try auto-detection
	detected := agent.DetectAll(ctx)

	// First run: use existing auto-detect shortcuts
	if !hasInstalledHooks {
		switch {
		case len(detected) == 1:
			if isBuiltInAgent(detected[0]) {
				// When a selectFn is provided (e.g. --yes), skip the single-agent
				// shortcut so the caller's selection logic runs instead.
				if selectFn == nil {
					fmt.Fprintf(w, "Detected agent: %s\n\n", detected[0].Type())
					return detected, nil
				}
			}

		case len(detected) > 1:
			agentTypes := make([]string, 0, len(detected))
			for _, ag := range detected {
				agentTypes = append(agentTypes, string(ag.Type()))
			}
			fmt.Fprintf(w, "Detected multiple agents: %s\n", strings.Join(agentTypes, ", "))
			fmt.Fprintln(w)
		}
	}

	// When no selectFn is provided, check if we can prompt interactively.
	// A selectFn (e.g. from --yes) bypasses the interactive prompt entirely.
	if selectFn == nil && !interactive.CanPromptInteractively() {
		if hasInstalledHooks {
			// Re-run without TTY — keep currently installed agents
			agents := make([]agent.Agent, 0, len(installedAgentNames))
			for _, name := range installedAgentNames {
				ag, err := agent.Get(name)
				if err != nil {
					continue
				}
				agents = append(agents, ag)
			}
			return agents, nil
		}
		if len(detected) > 0 {
			return detected, nil
		}
		defaultAgent := agent.Default()
		if defaultAgent == nil {
			return nil, errors.New("no default agent available")
		}
		fmt.Fprintf(w, "Agent: %s (use --agent to change)\n\n", defaultAgent.Type())
		return []agent.Agent{defaultAgent}, nil
	}

	// Build pre-selection set.
	// On re-run: only pre-select agents with hooks installed (respect prior deselection).
	// On first run: pre-select detected built-in agents only.
	preSelectedSet := make(map[types.AgentName]struct{})
	if hasInstalledHooks {
		for _, name := range installedAgentNames {
			preSelectedSet[name] = struct{}{}
		}
	} else {
		for _, ag := range detected {
			if isBuiltInAgent(ag) {
				preSelectedSet[ag.Name()] = struct{}{}
			}
		}
	}

	// Build options from registered agents
	agentNames := agent.List()
	options := make([]huh.Option[string], 0, len(agentNames))
	for _, name := range agentNames {
		ag, err := agent.Get(name)
		if err != nil {
			continue
		}
		// Only show agents that support hooks
		if _, ok := agent.AsHookSupport(ag); !ok {
			continue
		}
		// Skip test-only agents (e.g., Vogon canary)
		if to, ok := ag.(agent.TestOnly); ok && to.IsTestOnly() {
			continue
		}
		opt := huh.NewOption(string(ag.Type()), string(name))
		if _, isPreSelected := preSelectedSet[name]; isPreSelected {
			opt = opt.Selected(true)
		}
		options = append(options, opt)
	}

	if len(options) == 0 {
		return nil, errors.New("no agents with hook support available")
	}

	// Collect available agent names for the selector
	availableNames := make([]string, 0, len(options))
	for _, opt := range options {
		availableNames = append(availableNames, opt.Value)
	}

	var selectedAgentNames []string
	if selectFn != nil {
		var err error
		selectedAgentNames, err = selectFn(availableNames)
		if err != nil {
			return nil, err
		}
		if len(selectedAgentNames) == 0 {
			return nil, errors.New("no agents selected")
		}
	} else {
		form := NewAccessibleForm(
			huh.NewGroup(
				huh.NewMultiSelect[string]().
					Title("Select the agents you want to use").
					Description("Use space to select, enter to confirm.").
					Options(options...).
					Validate(func(selected []string) error {
						if len(selected) == 0 {
							return errors.New("please select at least one agent")
						}
						return nil
					}).
					Value(&selectedAgentNames),
			),
		)
		if err := form.Run(); err != nil {
			return nil, fmt.Errorf("agent selection cancelled: %w", err)
		}
	}

	selectedAgents := make([]agent.Agent, 0, len(selectedAgentNames))
	for _, name := range selectedAgentNames {
		selectedAgent, err := agent.Get(types.AgentName(name))
		if err != nil {
			return nil, fmt.Errorf("failed to get selected agent %s: %w", name, err)
		}
		selectedAgents = append(selectedAgents, selectedAgent)
	}

	agentTypes := make([]string, 0, len(selectedAgents))
	for _, ag := range selectedAgents {
		agentTypes = append(agentTypes, string(ag.Type()))
	}
	fmt.Fprintf(w, "  Selected agents: %s\n", strings.Join(agentTypes, ", "))
	return selectedAgents, nil
}

func isBuiltInAgent(ag agent.Agent) bool {
	return !external.IsExternal(ag)
}

// printAgentError writes an error message followed by available agents and usage.
func printAgentError(w io.Writer, message string) {
	agents := agent.List()
	fmt.Fprintf(w, "%s Available agents:\n", message)
	fmt.Fprintln(w)
	for _, a := range agents {
		suffix := ""
		if a == agent.DefaultAgentName {
			suffix = "    (default)"
		}
		fmt.Fprintf(w, "  %s%s\n", a, suffix)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage: trace enable --agent <agent-name>")
}

// printMissingAgentError writes a helpful error listing available agents.
func printMissingAgentError(w io.Writer) {
	printAgentError(w, "Missing agent name.")
}

// printWrongAgentError writes a helpful error when an unknown agent name is provided.
func printWrongAgentError(w io.Writer, name string) {
	printAgentError(w, fmt.Sprintf("Unknown agent %q.", name))
}
