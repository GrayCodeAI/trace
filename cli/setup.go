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
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cli/strategy"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"
)

// Config path display strings
const (
	configDisplayProject = ".trace/settings.json"
	configDisplayLocal   = ".trace/settings.local.json"
)

// Flag names used across setup commands.
const (
	agentFlagName            = "agent"
	flagCheckpointRemote     = "checkpoint-remote"
	flagSkipPushSessions     = "skip-push-sessions"
	flagSummarizeModel       = "summarize-model"
	flagSummarizeAgent       = "summarize-provider"
	flagTelemetry            = "telemetry"
	flagAbsoluteGitHookPath  = "absolute-git-hook-path"
	flagForce                = "force"
	flagLocalDev             = "local-dev"
	checkpointProviderGitHub = "github"
)

// externalAgentsAutoEnabledNotice is printed when picking an external summary
// provider implicitly turns the external_agents setting on. It tells the user
// that the change applies beyond summary generation, without exposing the
// underlying flag name or discovery mechanics.
const externalAgentsAutoEnabledNotice = "Note: external agents are now enabled for the rest of Trace too — not just summaries."

// EnableOptions holds the flags for `trace enable`.
type EnableOptions struct {
	LocalDev            bool
	UseLocalSettings    bool
	UseProjectSettings  bool
	ForceHooks          bool
	SkipPushSessions    bool
	CheckpointRemote    string
	Telemetry           bool
	AbsoluteGitHookPath bool
	// SuppressDoneMessage tells `runEnableInteractive` to skip its final
	// "Ready." line and the "commit the configuration files" hint. Set
	// when the caller is running the bootstrap flow, which takes over
	// presentation of the final state (commit, push, done).
	SuppressDoneMessage bool
	Yes                 bool
}

// applyStrategyOptions sets strategy_options on settings from CLI flags.
func (opts *EnableOptions) applyStrategyOptions(settings *TraceSettings) {
	if opts.SkipPushSessions {
		if settings.StrategyOptions == nil {
			settings.StrategyOptions = make(map[string]interface{})
		}
		settings.StrategyOptions["push_sessions"] = false
	}
	if opts.CheckpointRemote != "" {
		provider, repo, err := parseCheckpointRemoteFlag(opts.CheckpointRemote)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: invalid --checkpoint-remote format: %v\n", err)
		} else {
			if settings.StrategyOptions == nil {
				settings.StrategyOptions = make(map[string]interface{})
			}
			settings.StrategyOptions["checkpoint_remote"] = map[string]any{
				"provider": provider,
				"repo":     repo,
			}
		}
	}
}

func hasStrategyFlags(cmd *cobra.Command) bool {
	return cmd.Flags().Changed(flagCheckpointRemote) || cmd.Flags().Changed(flagSkipPushSessions)
}

func hasSummaryProviderFlags(cmd *cobra.Command) bool {
	return cmd.Flags().Changed(flagSummarizeAgent) || cmd.Flags().Changed(flagSummarizeModel)
}

// hasGlobalSettingsFlags reports whether any flag affects telemetry or
// the trace-managed git hook (force / absolute path / local-dev).
func hasGlobalSettingsFlags(cmd *cobra.Command) bool {
	return cmd.Flags().Changed(flagTelemetry) ||
		cmd.Flags().Changed(flagAbsoluteGitHookPath) ||
		cmd.Flags().Changed(flagForce) ||
		cmd.Flags().Changed(flagLocalDev)
}

// hasConfigureSettingsFlags reports whether configure was invoked with any
// flag that mutates settings or hooks. Bare invocation prints help instead.
func hasConfigureSettingsFlags(cmd *cobra.Command) bool {
	return hasStrategyFlags(cmd) || hasSummaryProviderFlags(cmd) || hasGlobalSettingsFlags(cmd)
}

// enableUsesSetupFlow reports whether `trace enable` should delegate to the
// setup/configure flow instead of the lightweight re-enable path.
// Bare `enable` and `enable --local/--project` remain state-toggle operations;
// any other setup-mutating flag should share configure's behavior.
func enableUsesSetupFlow(cmd *cobra.Command, agentName string) bool {
	if agentName != "" || hasStrategyFlags(cmd) {
		return true
	}
	return hasGlobalSettingsFlags(cmd) || cmd.Flags().Changed("yes")
}

func enableNeedsAgentManagement(cmd *cobra.Command) bool {
	return hasGlobalSettingsFlags(cmd) || cmd.Flags().Changed("yes")
}

// updateStrategyOptions applies strategy flags to settings without re-running agent setup.
// Loads and writes only the target file to avoid leaking settings between layers.
func updateStrategyOptions(ctx context.Context, w io.Writer, opts EnableOptions) error {
	// Validate before doing any I/O so we don't report "Settings updated" on bad input.
	if opts.CheckpointRemote != "" {
		if _, _, err := parseCheckpointRemoteFlag(opts.CheckpointRemote); err != nil {
			return fmt.Errorf("invalid --checkpoint-remote: %w", err)
		}
	}

	targetFile, configDisplay := settingsTargetFile(ctx, opts.UseLocalSettings, opts.UseProjectSettings)

	targetFileAbs, err := paths.AbsPath(ctx, targetFile)
	if err != nil {
		targetFileAbs = targetFile
	}

	s, err := settings.LoadFromFile(targetFileAbs)
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	opts.applyStrategyOptions(s)

	if targetFile == settings.TraceSettingsLocalFile {
		if err := SaveTraceSettingsLocal(ctx, s); err != nil {
			return fmt.Errorf("failed to save settings: %w", err)
		}
	} else {
		if err := SaveTraceSettings(ctx, s); err != nil {
			return fmt.Errorf("failed to save settings: %w", err)
		}
	}

	fmt.Fprintf(w, "✓ Settings updated (%s)\n", configDisplay)
	return nil
}

func updateSummaryGenerationSettings(ctx context.Context, w io.Writer, provider, model string, opts EnableOptions) error {
	if provider == "" && model == "" {
		return errors.New("at least one of --summarize-provider or --summarize-model must be set")
	}

	if provider != "" {
		// Make external agents on $PATH resolvable for --summarize-provider.
		external.DiscoverAndRegisterAlways(ctx)
	}

	targetFile, configDisplay := settingsTargetFile(ctx, opts.UseLocalSettings, opts.UseProjectSettings)
	targetFileAbs, err := paths.AbsPath(ctx, targetFile)
	if err != nil {
		targetFileAbs = targetFile
	}

	s, err := settings.LoadFromFile(targetFileAbs)
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}
	if s.SummaryGeneration == nil {
		s.SummaryGeneration = &settings.SummaryGenerationSettings{}
	}

	if provider != "" {
		if err := validateSummaryProvider(provider); err != nil {
			return err
		}
		if ag, getErr := getSummaryAgent(types.AgentName(provider)); getErr == nil && external.IsExternal(ag) {
			if !s.ExternalAgents {
				s.ExternalAgents = true
				fmt.Fprintln(w, externalAgentsAutoEnabledNotice)
			}
		}
	}
	if model != "" && provider == "" && s.SummaryGeneration.Provider == "" {
		// The target file alone has no provider, but the merged runtime
		// settings might (e.g. provider in project, model override in local).
		// Check the full merged view before rejecting.
		merged, mergeErr := settings.Load(ctx)
		if mergeErr != nil || merged.SummaryGeneration == nil || merged.SummaryGeneration.Provider == "" {
			return errors.New("--summarize-model requires an existing summary provider or --summarize-provider")
		}
	}

	s.SummaryGeneration.SetProvider(provider, model)

	if targetFile == settings.TraceSettingsLocalFile {
		if err := SaveTraceSettingsLocal(ctx, s); err != nil {
			return fmt.Errorf("failed to save settings: %w", err)
		}
	} else {
		if err := SaveTraceSettings(ctx, s); err != nil {
			return fmt.Errorf("failed to save settings: %w", err)
		}
	}

	fmt.Fprintf(w, "✓ Settings updated (%s)\n", configDisplay)
	return nil
}

// updateGlobalSettings persists telemetry / hook-mode flags and reinstalls the
// Trace git hook when --force, --absolute-git-hook-path, or --local-dev is set.
func updateGlobalSettings(ctx context.Context, cmd *cobra.Command, w io.Writer, opts EnableOptions) error {
	targetFile, configDisplay := settingsTargetFile(ctx, opts.UseLocalSettings, opts.UseProjectSettings)
	targetFileAbs, err := paths.AbsPath(ctx, targetFile)
	if err != nil {
		targetFileAbs = targetFile
	}
	s, err := settings.LoadFromFile(targetFileAbs)
	if err != nil {
		return fmt.Errorf("failed to load settings: %w", err)
	}

	if cmd.Flags().Changed(flagTelemetry) {
		v := opts.Telemetry
		s.Telemetry = &v
	}
	if cmd.Flags().Changed(flagAbsoluteGitHookPath) {
		s.AbsoluteGitHookPath = opts.AbsoluteGitHookPath
	}
	if cmd.Flags().Changed(flagLocalDev) {
		s.LocalDev = opts.LocalDev
	}

	if err := saveSettingsToTarget(ctx, s, targetFile); err != nil {
		return fmt.Errorf("failed to save settings: %w", err)
	}

	if cmd.Flags().Changed(flagForce) || cmd.Flags().Changed(flagAbsoluteGitHookPath) || cmd.Flags().Changed(flagLocalDev) {
		if _, err := strategy.InstallGitHook(ctx, true, s.LocalDev, s.AbsoluteGitHookPath); err != nil {
			return fmt.Errorf("failed to reinstall git hook: %w", err)
		}
		strategy.CheckAndWarnHookManagers(ctx, w, s.LocalDev, s.AbsoluteGitHookPath)
		fmt.Fprintln(w, "  ✓ Reinstalled git hook")
	}

	fmt.Fprintf(w, "✓ Settings updated (%s)\n", configDisplay)
	return nil
}

// settingsTargetFile determines which settings file to write to based on flags
// and which files exist. Unlike determineSettingsTarget, this correctly handles
// local-only repos by checking for settings.local.json when settings.json is absent.
func settingsTargetFile(ctx context.Context, useLocal, useProject bool) (string, string) {
	if useLocal {
		return settings.TraceSettingsLocalFile, configDisplayLocal
	}
	if useProject {
		return settings.TraceSettingsFile, configDisplayProject
	}

	// No explicit flag — write to whichever file exists.
	// Check project file first, then local.
	projectAbs, err := paths.AbsPath(ctx, settings.TraceSettingsFile)
	if err == nil {
		if _, statErr := os.Stat(projectAbs); statErr == nil {
			return settings.TraceSettingsFile, configDisplayProject
		}
	}
	localAbs, err := paths.AbsPath(ctx, settings.TraceSettingsLocalFile)
	if err == nil {
		if _, statErr := os.Stat(localAbs); statErr == nil {
			return settings.TraceSettingsLocalFile, configDisplayLocal
		}
	}

	// Neither exists — default to project
	return settings.TraceSettingsFile, configDisplayProject
}

func saveSettingsToTarget(ctx context.Context, s *TraceSettings, targetFile string) error {
	switch targetFile {
	case settings.TraceSettingsLocalFile:
		return SaveTraceSettingsLocal(ctx, s)
	case settings.TraceSettingsFile:
		return SaveTraceSettings(ctx, s)
	default:
		return fmt.Errorf("unknown settings target %q", targetFile)
	}
}

// parseCheckpointRemoteFlag parses a "provider:owner/repo" string into its components.
// Supported providers: "github".
func parseCheckpointRemoteFlag(value string) (provider, repo string, err error) {
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected format provider:owner/repo (e.g., github:org/checkpoints-repo), got %q", value)
	}

	provider = parts[0]
	repo = parts[1]

	switch provider {
	case checkpointProviderGitHub:
		// valid
	default:
		return "", "", fmt.Errorf("unsupported provider %q (supported: %s)", provider, checkpointProviderGitHub)
	}

	repoParts := strings.SplitN(repo, "/", 2)
	if len(repoParts) != 2 || repoParts[0] == "" || repoParts[1] == "" {
		return "", "", fmt.Errorf("repo must be in owner/name format, got %q", repo)
	}

	return provider, repo, nil
}

// runSetupFlow runs the first-time setup flow (agent selection + hooks + settings).
// Shared by root command (no args), `trace configure`, and `trace enable` on fresh repos.
func runSetupFlow(ctx context.Context, w io.Writer, opts EnableOptions) error {
	// Discover external agent plugins so they appear in agent selection.
	// Use DiscoverAndRegisterAlways to bypass the external_agents setting —
	// during setup the setting doesn't exist yet.
	external.DiscoverAndRegisterAlways(ctx)

	var selectFn func(available []string) ([]string, error)
	if opts.Yes {
		selectFn = selectAllAgents
	}

	agents, err := detectOrSelectAgent(ctx, w, selectFn)
	if err != nil {
		return fmt.Errorf("agent selection failed: %w", err)
	}

	return runEnableInteractive(ctx, w, agents, opts)
}

// selectAllAgents is a selectFn that selects all available agents.
// Used by --yes to skip the interactive agent selection prompt.
func selectAllAgents(available []string) ([]string, error) {
	if len(available) == 0 {
		return nil, errors.New("no agents available")
	}
	return available, nil
}

// runManageAgents shows which agents are currently enabled and lets the user
// add or remove agents. Deselecting an installed agent removes its hooks.
func runManageAgents(ctx context.Context, w io.Writer, opts EnableOptions, selectFn func(available []string) ([]string, error)) error {
	installedNames := GetAgentsWithHooksInstalled(ctx)

	// Show currently installed agents
	if len(installedNames) > 0 {
		displayNames := make([]string, 0, len(installedNames))
		for _, name := range installedNames {
			if ag, err := agent.Get(name); err == nil {
				displayNames = append(displayNames, string(ag.Type()))
			}
		}
		fmt.Fprintf(w, "Enabled agents: %s\n\n", strings.Join(displayNames, ", "))
	}

	// Build pre-selection set from installed agents
	installedSet := make(map[types.AgentName]struct{}, len(installedNames))
	for _, name := range installedNames {
		installedSet[name] = struct{}{}
	}

	// When no selectFn is provided, check if we can prompt interactively.
	// A selectFn (e.g. from --yes) bypasses the interactive prompt entirely.
	if selectFn == nil && !interactive.CanPromptInteractively() {
		fmt.Fprintln(w, "Cannot show agent selection in non-interactive mode.")
		fmt.Fprintln(w, "Use: trace agent add <name>")
		return nil
	}

	// Discover external agent plugins so they appear in agent selection.
	// Use DiscoverAndRegisterAlways to bypass the external_agents setting —
	// during setup the setting doesn't exist yet.
	external.DiscoverAndRegisterAlways(ctx)

	// Build options from registered agents
	agentNames := agent.List()
	options := make([]huh.Option[string], 0, len(agentNames))
	for _, name := range agentNames {
		ag, err := agent.Get(name)
		if err != nil {
			continue
		}
		if _, ok := agent.AsHookSupport(ag); !ok {
			continue
		}
		if to, ok := ag.(agent.TestOnly); ok && to.IsTestOnly() {
			continue
		}
		opt := huh.NewOption(string(ag.Type()), string(name))
		if _, installed := installedSet[name]; installed {
			opt = opt.Selected(true)
		}
		options = append(options, opt)
	}

	if len(options) == 0 {
		return errors.New("no agents with hook support available")
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
			return fmt.Errorf("agent selection cancelled: %w", err)
		}
	} else {
		form := NewAccessibleForm(
			huh.NewGroup(
				huh.NewMultiSelect[string]().
					Title("Manage agents").
					Description("Use space to select/deselect, enter to confirm.").
					Options(options...).
					Value(&selectedAgentNames),
			),
		)
		if err := form.Run(); err != nil {
			return fmt.Errorf("agent selection cancelled: %w", err)
		}
	}

	// Nothing selected and nothing installed — no-op.
	if len(selectedAgentNames) == 0 && len(installedNames) == 0 {
		targetFile, _ := settingsTargetFile(ctx, opts.UseLocalSettings, opts.UseProjectSettings)
		changed, err := maybePromptVercelDeploymentDisable(ctx, w, targetFile, nil)
		if err != nil {
			return err
		}
		if !changed {
			fmt.Fprintln(w, "No changes made.")
		}
		return nil
	}

	err := applyAgentChanges(ctx, w, selectedAgentNames, installedNames, opts)
	if err == nil && len(selectedAgentNames) == 0 {
		fmt.Fprintln(w, "To add agents again, run: trace agent add <name>")
	}
	return err
}

// applyAgentChanges computes added/removed agent sets from the selection and
// installs or uninstalls hooks accordingly.
func applyAgentChanges(ctx context.Context, w io.Writer, selectedAgentNames []string, installedNames []types.AgentName, opts EnableOptions) error {
	installedSet := make(map[types.AgentName]struct{}, len(installedNames))
	for _, name := range installedNames {
		installedSet[name] = struct{}{}
	}

	selectedSet := make(map[string]struct{}, len(selectedAgentNames))
	for _, name := range selectedAgentNames {
		selectedSet[name] = struct{}{}
	}

	// Collect errors so partial successes are visible to the user.
	var errs []error

	var addedAgents []agent.Agent
	var reinstalledAgents []agent.Agent
	for _, name := range selectedAgentNames {
		ag, err := agent.Get(types.AgentName(name))
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to get agent %s: %w", name, err))
			continue
		}
		if _, wasInstalled := installedSet[types.AgentName(name)]; wasInstalled {
			if opts.ForceHooks {
				reinstalledAgents = append(reinstalledAgents, ag)
			}
			continue
		}
		addedAgents = append(addedAgents, ag)
	}

	var removedAgents []agent.Agent
	for _, name := range installedNames {
		if _, stillSelected := selectedSet[string(name)]; stillSelected {
			continue
		}
		ag, err := agent.Get(name)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to load deselected agent %s: %w", name, err))
			continue
		}
		removedAgents = append(removedAgents, ag)
	}

	if len(addedAgents) == 0 && len(reinstalledAgents) == 0 && len(removedAgents) == 0 && len(errs) == 0 {
		targetFile, _ := settingsTargetFile(ctx, opts.UseLocalSettings, opts.UseProjectSettings)
		changed, err := maybePromptVercelDeploymentDisable(ctx, w, targetFile, nil)
		if err != nil {
			return err
		}
		if !changed {
			fmt.Fprintln(w, "No changes made.")
		}
		return nil
	}
	var successfullyAddedAgents []agent.Agent
	for _, ag := range addedAgents {
		if _, err := setupAgentHooks(ctx, w, ag, opts.LocalDev, opts.ForceHooks); err != nil {
			errs = append(errs, fmt.Errorf("failed to setup %s hooks: %w", ag.Type(), err))
		} else {
			successfullyAddedAgents = append(successfullyAddedAgents, ag)
		}
	}

	var successfullyReinstalledAgents []agent.Agent
	for _, ag := range reinstalledAgents {
		if _, err := setupAgentHooks(ctx, w, ag, opts.LocalDev, opts.ForceHooks); err != nil {
			errs = append(errs, fmt.Errorf("failed to setup %s hooks: %w", ag.Type(), err))
		} else {
			successfullyReinstalledAgents = append(successfullyReinstalledAgents, ag)
		}
	}

	var uninstalledAgents []agent.Agent
	for _, ag := range removedAgents {
		hookAgent, ok := agent.AsHookSupport(ag)
		if !ok {
			logging.Warn(ctx, "installed agent does not support hooks, skipping removal",
				"agent", string(ag.Name()))
			continue
		}
		if err := hookAgent.UninstallHooks(ctx); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove %s hooks: %w", ag.Type(), err))
		} else {
			uninstalledAgents = append(uninstalledAgents, ag)
		}
	}

	// Auto-enable external_agents setting if any new agent is external.
	for _, ag := range append(successfullyAddedAgents, successfullyReinstalledAgents...) {
		if external.IsExternal(ag) {
			s, loadErr := LoadTraceSettings(ctx)
			if loadErr != nil {
				s = &TraceSettings{}
			}
			if !s.ExternalAgents {
				s.ExternalAgents = true
				var saveErr error
				if opts.UseLocalSettings {
					saveErr = SaveTraceSettingsLocal(ctx, s)
				} else {
					saveErr = SaveTraceSettings(ctx, s)
				}
				if saveErr != nil {
					errs = append(errs, fmt.Errorf("failed to save external_agents setting: %w", saveErr))
				}
			}
			break
		}
	}

	// Print summary of what succeeded
	if len(successfullyAddedAgents) > 0 {
		names := make([]string, 0, len(successfullyAddedAgents))
		for _, ag := range successfullyAddedAgents {
			names = append(names, string(ag.Type()))
		}
		fmt.Fprintf(w, "✓ Added agents: %s\n", strings.Join(names, ", "))
	}
	if len(successfullyReinstalledAgents) > 0 {
		names := make([]string, 0, len(successfullyReinstalledAgents))
		for _, ag := range successfullyReinstalledAgents {
			names = append(names, string(ag.Type()))
		}
		fmt.Fprintf(w, "✓ Reinstalled agents: %s\n", strings.Join(names, ", "))
	}
	if len(uninstalledAgents) > 0 {
		if len(successfullyAddedAgents) == 0 && len(successfullyReinstalledAgents) == 0 && len(addedAgents) == 0 && len(removedAgents) == len(installedNames) {
			fmt.Fprintln(w, "All agents have been removed.")
		} else {
			names := make([]string, 0, len(uninstalledAgents))
			for _, ag := range uninstalledAgents {
				names = append(names, string(ag.Type()))
			}
			fmt.Fprintf(w, "✓ Removed agents: %s\n", strings.Join(names, ", "))
		}
	}

	vercelSettingsTarget, _ := settingsTargetFile(ctx, opts.UseLocalSettings, opts.UseProjectSettings)
	if _, err := maybePromptVercelDeploymentDisable(ctx, w, vercelSettingsTarget, nil); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

func newSetupCmd() *cobra.Command {
	var opts EnableOptions
	var summarizeProvider string
	var summarizeModel string

	cmd := &cobra.Command{
		Use:   "configure",
		Short: "Update Trace settings in the current repository",
		Long: `Update non-agent Trace settings in the current repository.

Manages telemetry, git-hook installation mode, strategy options, and summary
provider configuration. Agent installation is handled by 'trace agent'.

Examples:
  trace configure                                # Show this help
  trace configure --telemetry=false              # Opt out of telemetry
  trace configure --absolute-git-hook-path       # Reinstall git hook with absolute path
  trace configure --force                        # Reinstall git hook
  trace configure --checkpoint-remote github:org/checkpoints
  trace configure --summarize-provider claude-code`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			if _, err := paths.WorktreeRoot(ctx); err != nil {
				cmd.SilenceUsage = true
				fmt.Fprintln(cmd.ErrOrStderr(), "Not a git repository. Please run 'trace configure' from within a git repository.")
				return NewSilentError(errors.New("not a git repository"))
			}

			if !hasConfigureSettingsFlags(cmd) {
				if err := cmd.Help(); err != nil {
					return fmt.Errorf("failed to render help: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "\nFor agent setup, use 'trace agent' (e.g. 'trace agent add claude-code').")
				return nil
			}

			if !settings.IsSetUpAny(ctx) {
				cmd.SilenceUsage = true
				fmt.Fprintln(cmd.ErrOrStderr(), "Trace is not configured in this repository yet. Run 'trace enable' first.")
				return NewSilentError(errors.New("trace not configured"))
			}

			if hasStrategyFlags(cmd) {
				if err := updateStrategyOptions(ctx, cmd.OutOrStdout(), opts); err != nil {
					return err
				}
			}
			if hasSummaryProviderFlags(cmd) {
				if err := updateSummaryGenerationSettings(ctx, cmd.OutOrStdout(), summarizeProvider, summarizeModel, opts); err != nil {
					return err
				}
			}
			if hasGlobalSettingsFlags(cmd) {
				if err := updateGlobalSettings(ctx, cmd, cmd.OutOrStdout(), opts); err != nil {
					return err
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&opts.LocalDev, flagLocalDev, false, "Use go run instead of trace binary for hooks")
	cmd.Flags().MarkHidden(flagLocalDev) //nolint:errcheck,gosec // flag is defined above
	cmd.Flags().BoolVar(&opts.UseLocalSettings, "local", false, "Write settings to .trace/settings.local.json instead of .trace/settings.json")
	cmd.Flags().BoolVar(&opts.UseProjectSettings, "project", false, "Write settings to .trace/settings.json even if it already exists")
	cmd.Flags().BoolVarP(&opts.ForceHooks, flagForce, "f", false, "Reinstall the Trace git hook")
	cmd.Flags().BoolVar(&opts.SkipPushSessions, flagSkipPushSessions, false, "Disable automatic pushing of session logs on git push")
	cmd.Flags().StringVar(&opts.CheckpointRemote, flagCheckpointRemote, "", "Checkpoint remote in provider:owner/repo format (e.g., github:org/checkpoints-repo)")
	cmd.Flags().StringVar(&summarizeProvider, flagSummarizeAgent, "", "Set the provider used by explain --generate (e.g., claude-code, codex, gemini, cursor, copilot-cli)")
	cmd.Flags().StringVar(&summarizeModel, flagSummarizeModel, "", "Set the model hint used by explain --generate")
	cmd.Flags().BoolVar(&opts.Telemetry, flagTelemetry, true, "Enable anonymous usage analytics")
	cmd.Flags().BoolVar(&opts.AbsoluteGitHookPath, flagAbsoluteGitHookPath, false, "Embed full binary path in git hooks (for GUI git clients that don't source shell profiles)")

	return cmd
}
