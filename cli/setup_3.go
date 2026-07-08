package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/external"
	"github.com/GrayCodeAI/trace/cli/interactive"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/session"
	"github.com/GrayCodeAI/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cli/vercelconfig"

	"charm.land/huh/v2"
	"github.com/spf13/cobra"
)

// setupAgentHooksNonInteractive sets up hooks for a specific agent non-interactively.
// If strategyName is provided, it sets the strategy; otherwise uses default.
func setupAgentHooksNonInteractive(ctx context.Context, w io.Writer, ag agent.Agent, opts EnableOptions) error {
	agentName := ag.Name()
	// Check if agent supports hooks
	if _, ok := agent.AsHookSupport(ag); !ok {
		return fmt.Errorf("agent %s does not support hooks", agentName)
	}

	fmt.Fprintf(w, "  Agent: %s\n", ag.Type())

	// Install agent hooks (agent hooks don't depend on settings)
	installedHooks, err := setupAgentHooks(ctx, w, ag, opts.LocalDev, opts.ForceHooks)
	if err != nil {
		return fmt.Errorf("failed to setup %s hooks: %w", agentName, err)
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
	settings.Enabled = true
	if opts.LocalDev {
		settings.LocalDev = true
	}
	if opts.AbsoluteGitHookPath {
		settings.AbsoluteGitHookPath = true
	}

	// Auto-enable external_agents setting if the agent is external.
	if external.IsExternal(ag) {
		settings.ExternalAgents = true
	}

	opts.applyStrategyOptions(settings)

	// Handle telemetry for non-interactive mode
	// Note: if telemetry is nil (not configured), it defaults to disabled
	if !opts.Telemetry || os.Getenv("TRACE_TELEMETRY_OPTOUT") != "" {
		f := false
		settings.Telemetry = &f
	}

	targetFile, configDisplay := settingsTargetFile(ctx, opts.UseLocalSettings, opts.UseProjectSettings)
	if err := saveSettingsToTarget(ctx, settings, targetFile); err != nil {
		return fmt.Errorf("failed to save settings: %w", err)
	}

	// Use settings values (merged from existing config + flags) for hook installation
	// This ensures re-running `trace enable --agent X` without flags preserves existing settings
	if _, err := strategy.InstallGitHook(ctx, true, settings.LocalDev, settings.AbsoluteGitHookPath); err != nil {
		return fmt.Errorf("failed to install git hooks: %w", err)
	}
	strategy.CheckAndWarnHookManagers(ctx, w, settings.LocalDev, settings.AbsoluteGitHookPath)

	if installedHooks == 0 {
		msg := fmt.Sprintf("Hooks for %s already installed", ag.Description())
		if ag.IsPreview() {
			msg += " (Preview)"
		}
		fmt.Fprintf(w, "  %s\n", msg)
	} else {
		msg := fmt.Sprintf("Installed %d hooks for %s", installedHooks, ag.Description())
		if ag.IsPreview() {
			msg += " (Preview)"
		}
		fmt.Fprintf(w, "  %s\n", msg)
	}

	fmt.Fprintln(w, "  ✓ Configured project")
	fmt.Fprintf(w, "    %s\n", configDisplay)

	if _, err := maybePromptVercelDeploymentDisable(ctx, w, targetFile, nil); err != nil {
		return err
	}

	if err := strategy.EnsureSetup(ctx); err != nil {
		return fmt.Errorf("failed to setup strategy: %w", err)
	}

	if opts.SuppressDoneMessage {
		// Bootstrap finalize will print its own completion summary.
		return nil
	}

	fmt.Fprintln(w, "\nReady.")

	if repo, err := strategy.OpenRepository(ctx); err == nil && strategy.IsEmptyRepository(repo) {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Note: Session checkpoints require at least one commit. To get started,")
		fmt.Fprintln(w, "commit the configuration files (e.g. .trace/, .claude/).")
	}

	return nil
}

// validateSetupFlags checks that --local and --project flags are not both specified.
func validateSetupFlags(useLocal, useProject bool) error {
	if useLocal && useProject {
		return errors.New("cannot specify both --project and --local")
	}
	return nil
}

// determineSettingsTarget decides whether to write to settings.local.json based on:
// - Whether settings.json already exists
// - The --local and --project flags
// Returns (useLocal, showNotification).
func determineSettingsTarget(traceDir string, useLocal, useProject bool) (bool, bool) {
	// Explicit --local flag always uses local settings
	if useLocal {
		return true, false
	}

	// Explicit --project flag always uses project settings
	if useProject {
		return false, false
	}

	// No flags specified - check if settings file exists
	settingsPath := filepath.Join(traceDir, paths.SettingsFileName)
	if _, err := os.Stat(settingsPath); err == nil {
		// Settings file exists - auto-redirect to local with notification
		return true, true
	}

	// Settings file doesn't exist - create it
	return false, false
}

// setupTraceDirectory creates the .trace directory and gitignore.
// Returns true if the directory was created, false if it already existed.
func setupTraceDirectory(ctx context.Context) (bool, error) { //nolint:unparam // already present in codebase
	// Get absolute path for the .trace directory
	traceDirAbs, err := paths.AbsPath(ctx, paths.TraceDir)
	if err != nil {
		traceDirAbs = paths.TraceDir // Fallback to relative
	}

	// Check if directory already exists
	created := false
	if _, err := os.Stat(traceDirAbs); os.IsNotExist(err) {
		created = true
	}

	// Create .trace directory
	if err := os.MkdirAll(traceDirAbs, 0o750); err != nil {
		return false, fmt.Errorf("failed to create .trace directory: %w", err)
	}

	// Create/update .gitignore with all required entries
	if err := strategy.EnsureTraceGitignore(ctx); err != nil {
		return false, fmt.Errorf("failed to setup .gitignore: %w", err)
	}

	return created, nil
}

func newCurlBashPostInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "curl-bash-post-install",
		Short:  "Post-install tasks for curl|bash installer",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()
			if err := promptShellCompletion(w); err != nil {
				fmt.Fprintf(w, "Note: Shell completion setup skipped: %v\n", err)
			}
			return nil
		},
	}
}

// shellCompletionComment is the comment preceding the completion line
const shellCompletionComment = "# Trace CLI shell completion"

// errUnsupportedShell is returned when the user's shell is not supported for completion.
var errUnsupportedShell = errors.New("unsupported shell")

// shellCompletionTarget returns the rc file path and completion lines for the
// user's current shell.
func shellCompletionTarget() (shellName, rcFile, completionLine string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	shell := os.Getenv("SHELL")
	switch {
	case strings.Contains(shell, "zsh"):
		return "Zsh",
			filepath.Join(home, ".zshrc"),
			"autoload -Uz compinit && compinit && source <(trace completion zsh)",
			nil
	case strings.Contains(shell, "bash"):
		bashRC := filepath.Join(home, ".bashrc")
		if _, err := os.Stat(filepath.Join(home, ".bash_profile")); err == nil {
			bashRC = filepath.Join(home, ".bash_profile")
		}
		return "Bash",
			bashRC,
			"source <(trace completion bash)",
			nil
	case strings.Contains(shell, "fish"):
		return "Fish",
			filepath.Join(home, ".config", "fish", "config.fish"),
			"trace completion fish | source",
			nil
	default:
		return "", "", "", errUnsupportedShell
	}
}

// promptShellCompletion offers to add shell completion to the user's rc file.
// Only prompts if completion is not already configured.
func promptShellCompletion(w io.Writer) error {
	shellName, rcFile, completionLine, err := shellCompletionTarget()
	if err != nil {
		if errors.Is(err, errUnsupportedShell) {
			fmt.Fprintf(w, "Note: Shell completion not available for your shell. Supported: zsh, bash, fish.\n")
			return nil
		}
		return fmt.Errorf("shell completion: %w", err)
	}

	if isCompletionConfigured(rcFile) {
		fmt.Fprintf(w, "✓ Shell completion already configured in %s\n", rcFile)
		return nil
	}

	var selected string
	form := NewAccessibleForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title(fmt.Sprintf("Enable shell completion? (detected: %s)", shellName)).
				Options(
					huh.NewOption("Yes", "yes"),
					huh.NewOption("No", "no"),
				).
				Value(&selected),
		),
	)

	if err := form.Run(); err != nil {
		//nolint:nilerr // User cancelled - not a fatal error, just skip
		return nil
	}

	if selected != "yes" {
		return nil
	}

	if err := appendShellCompletion(rcFile, completionLine); err != nil {
		return fmt.Errorf("failed to update %s: %w", rcFile, err)
	}

	fmt.Fprintf(w, "✓ Shell completion added to %s\n", rcFile)
	fmt.Fprintln(w, "  Restart your shell to activate")

	return nil
}

// isCompletionConfigured checks if shell completion is already in the rc file.
func isCompletionConfigured(rcFile string) bool {
	//nolint:gosec // G304: rcFile is constructed from home dir + known filename, not user input
	// #nosec G304 -- rcFile is constructed from home dir + known filename, not user input
	content, err := os.ReadFile(rcFile)
	if err != nil {
		return false // File doesn't exist or can't read, treat as not configured
	}
	return strings.Contains(string(content), "trace completion")
}

// appendShellCompletion adds the completion line to the rc file.
func appendShellCompletion(rcFile, completionLine string) error {
	if err := os.MkdirAll(filepath.Dir(rcFile), 0o700); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	//nolint:gosec // G302: Shell rc files need 0644 for user readability
	// #nosec G302,G304 -- shell rc files (.zshrc/.bashrc/config.fish) are intentionally user-readable/editable at the standard 0644 mode; rcFile is derived from home dir + known filename, not external input
	f, err := os.OpenFile(rcFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening file: %w", err)
	}
	defer f.Close()

	_, err = f.WriteString("\n" + shellCompletionComment + "\n" + completionLine + "\n")
	if err != nil {
		return fmt.Errorf("writing completion: %w", err)
	}
	return nil
}

// promptTelemetryConsent asks the user if they want to enable telemetry.
// It modifies settings.Telemetry based on the user's choice or flags.
// The caller is responsible for saving settings.
func promptTelemetryConsent(settings *TraceSettings, telemetryFlag bool) error {
	// Handle --telemetry=false flag first (always overrides existing setting)
	if !telemetryFlag {
		f := false
		settings.Telemetry = &f
		return nil
	}

	// Skip if already asked
	if settings.Telemetry != nil {
		return nil
	}

	// Skip if env var disables telemetry (record as disabled)
	if os.Getenv("TRACE_TELEMETRY_OPTOUT") != "" {
		f := false
		settings.Telemetry = &f
		return nil
	}

	consent := true // Default to Yes
	form := NewAccessibleForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Help improve Trace CLI?").
				Description("Share anonymous usage data. No code or personal info collected.").
				Affirmative("Yes").
				Negative("No").
				Value(&consent),
		),
	)

	if err := form.Run(); err != nil {
		return fmt.Errorf("telemetry prompt: %w", err)
	}

	settings.Telemetry = &consent
	return nil
}

func maybePromptVercelDeploymentDisable(ctx context.Context, w io.Writer, targetFile string, promptFn func() (bool, error)) (bool, error) {
	repoRoot, rootErr := paths.WorktreeRoot(ctx)
	if rootErr == nil {
		vercelJSONPath := filepath.Join(repoRoot, "vercel.json")
		hasVercelJSON := false
		if _, err := os.Stat(vercelJSONPath); err == nil {
			hasVercelJSON = true
		} else if !os.IsNotExist(err) {
			fmt.Fprintf(w, "Note: Skipping Vercel deployment update: could not check vercel.json: %v\n", err)
			return false, nil
		}

		hasVercelProject := hasVercelJSON
		if !hasVercelProject {
			for _, path := range []string{
				filepath.Join(repoRoot, ".vercel"),
				filepath.Join(repoRoot, "vercel.ts"),
			} {
				if _, err := os.Stat(path); err == nil {
					hasVercelProject = true
					break
				} else if !os.IsNotExist(err) {
					fmt.Fprintf(w, "Note: Skipping Vercel deployment update: could not check %s: %v\n", path, err)
					return false, nil
				}
			}
		}

		if !hasVercelProject {
			return false, nil
		}

		configDisplay := configDisplayProject
		if targetFile == settings.TraceSettingsLocalFile {
			configDisplay = configDisplayLocal
		}

		targetSettingsPath := filepath.Join(repoRoot, targetFile)
		targetSettings, err := settings.LoadFromFile(targetSettingsPath)
		if err != nil {
			return false, fmt.Errorf("load settings: %w", err)
		}
		if targetSettings.Vercel {
			return false, nil
		}

		if config, alreadyDisabled, loadErr := vercelconfig.Load(vercelJSONPath); loadErr == nil &&
			config != nil && alreadyDisabled {
			targetSettings.Vercel = true
			if err := saveSettingsToTarget(ctx, targetSettings, targetFile); err != nil {
				return false, fmt.Errorf("save settings: %w", err)
			}
			fmt.Fprintf(w, "✓ Updated %s to manage Vercel deployment blocking on `%s`\n", configDisplay, vercelconfig.BranchPattern)
			return true, nil
		}

		if promptFn == nil {
			if !interactive.CanPromptInteractively() {
				fmt.Fprintf(w, "Note: Vercel detected. Run `trace configure` interactively to disable deployments for `%s` branches.\n", vercelconfig.BranchPattern)
				return false, nil
			}
			promptFn = promptVercelDeploymentDisable
		}

		disableDeployments, err := promptFn()
		if err != nil {
			return false, fmt.Errorf("vercel prompt: %w", err)
		}
		if !disableDeployments {
			return false, nil
		}

		targetSettings.Vercel = true
		if err := saveSettingsToTarget(ctx, targetSettings, targetFile); err != nil {
			return false, fmt.Errorf("save settings: %w", err)
		}

		fmt.Fprintf(w, "✓ Updated %s to block Vercel deploys of Trace metadata branch\n", configDisplay)
		return true, nil
	}

	return false, nil
}

func promptVercelDeploymentDisable() (bool, error) {
	disableDeployments := true
	form := NewAccessibleForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Disable Vercel deployments for Trace metadata branch?").
				Description("This automatically creates a vercel.json in the Trace metadata branch.").
				Affirmative("Yes").
				Negative("No").
				Value(&disableDeployments),
		),
	)

	if err := form.Run(); err != nil {
		return false, fmt.Errorf("run vercel deployment disable form: %w", err)
	}

	return disableDeployments, nil
}

// runUninstall completely removes Trace from the repository.
func runUninstall(ctx context.Context, w, errW io.Writer, force bool) error {
	// Check if we're in a git repository
	if _, err := paths.WorktreeRoot(ctx); err != nil {
		fmt.Fprintln(errW, "Not a git repository. Nothing to uninstall.")
		return NewSilentError(errors.New("not a git repository"))
	}

	// Gather counts for display
	sessionStateCount := countSessionStates(ctx)
	shadowBranchCount := countShadowBranches(ctx)
	gitHooksInstalled := strategy.IsGitHookInstalled(ctx)
	agentsWithInstalledHooks := GetAgentsWithHooksInstalled(ctx)
	traceDirExists := checkTraceDirExists(ctx)

	// Check if there's anything to uninstall
	if !traceDirExists && !gitHooksInstalled && sessionStateCount == 0 &&
		shadowBranchCount == 0 && len(agentsWithInstalledHooks) == 0 {
		fmt.Fprintln(w, "Trace is not installed in this repository.")
		return nil
	}

	// Show confirmation prompt unless --force
	if !force {
		fmt.Fprintln(w, "\nThis will completely remove Trace from this repository:")
		if traceDirExists {
			fmt.Fprintln(w, "  - .trace/ directory")
		}
		if gitHooksInstalled {
			fmt.Fprintln(w, "  - Git hooks (prepare-commit-msg, commit-msg, post-commit, pre-push)")
		}
		if sessionStateCount > 0 {
			fmt.Fprintf(w, "  - Session state files (%d)\n", sessionStateCount)
		}
		if shadowBranchCount > 0 {
			fmt.Fprintf(w, "  - Shadow branches (%d)\n", shadowBranchCount)
		}
		if len(agentsWithInstalledHooks) > 0 {
			displayNames := make([]string, 0, len(agentsWithInstalledHooks))
			for _, name := range agentsWithInstalledHooks {
				if ag, err := agent.Get(name); err == nil {
					displayNames = append(displayNames, string(ag.Type()))
				}
			}
			fmt.Fprintf(w, "  - Agent hooks (%s)\n", strings.Join(displayNames, ", "))
		}
		fmt.Fprintln(w)

		var confirmed bool
		form := NewAccessibleForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title("Are you sure you want to uninstall Trace?").
					Affirmative("Yes, uninstall").
					Negative("Cancel").
					Value(&confirmed),
			),
		)

		if err := form.Run(); err != nil {
			return fmt.Errorf("confirmation cancelled: %w", err)
		}

		if !confirmed {
			fmt.Fprintln(w, "Uninstall cancelled.")
			return nil
		}
	}

	fmt.Fprintln(w, "\nUninstalling Trace CLI...")

	// 1. Remove agent hooks (lowest risk)
	if err := removeAgentHooks(ctx, w); err != nil {
		fmt.Fprintf(errW, "Warning: failed to remove agent hooks: %v\n", err)
	}

	// 2. Remove git hooks
	removed, err := strategy.RemoveGitHook(ctx)
	if err != nil {
		fmt.Fprintf(errW, "Warning: failed to remove git hooks: %v\n", err)
	} else if removed > 0 {
		fmt.Fprintf(w, "  Removed git hooks (%d)\n", removed)
	}

	// 3. Remove session state files
	statesRemoved, err := removeAllSessionStates(ctx)
	if err != nil {
		fmt.Fprintf(errW, "Warning: failed to remove session states: %v\n", err)
	} else if statesRemoved > 0 {
		fmt.Fprintf(w, "  Removed session states (%d)\n", statesRemoved)
	}

	// 4. Remove .trace/ directory
	if err := removeTraceDirectory(ctx); err != nil {
		fmt.Fprintf(errW, "Warning: failed to remove .trace directory: %v\n", err)
	} else if traceDirExists {
		fmt.Fprintln(w, "  Removed .trace directory")
	}

	// 5. Remove shadow branches
	branchesRemoved, err := removeAllShadowBranches(ctx)
	if err != nil {
		fmt.Fprintf(errW, "Warning: failed to remove shadow branches: %v\n", err)
	} else if branchesRemoved > 0 {
		fmt.Fprintf(w, "  Removed %d shadow branches\n", branchesRemoved)
	}

	fmt.Fprintln(w, "\nTrace CLI uninstalled successfully.")
	return nil
}

// countSessionStates returns the number of active session state files.
func countSessionStates(ctx context.Context) int {
	store, err := session.NewStateStore(ctx)
	if err != nil {
		return 0
	}
	states, err := store.List(ctx)
	if err != nil {
		return 0
	}
	return len(states)
}

// countShadowBranches returns the number of shadow branches.
func countShadowBranches(ctx context.Context) int {
	branches, err := strategy.ListShadowBranches(ctx)
	if err != nil {
		return 0
	}
	return len(branches)
}

// checkTraceDirExists checks if the .trace directory exists.
func checkTraceDirExists(ctx context.Context) bool {
	traceDirAbs, err := paths.AbsPath(ctx, paths.TraceDir)
	if err != nil {
		traceDirAbs = paths.TraceDir
	}
	_, err = os.Stat(traceDirAbs)
	return err == nil
}

// removeAgentHooks removes hooks from all agents that support hooks.
func removeAgentHooks(ctx context.Context, w io.Writer) error {
	var errs []error
	for _, name := range agent.List() {
		ag, err := agent.Get(name)
		if err != nil {
			continue
		}
		hs, ok := agent.AsHookSupport(ag)
		if !ok {
			continue
		}
		wasInstalled := hs.AreHooksInstalled(ctx)
		if err := hs.UninstallHooks(ctx); err != nil {
			errs = append(errs, err)
		} else if wasInstalled {
			fmt.Fprintf(w, "  Removed %s hooks\n", ag.Type())
		}
	}
	return errors.Join(errs...)
}

// removeAllSessionStates removes all session state files and the directory.
func removeAllSessionStates(ctx context.Context) (int, error) {
	store, err := session.NewStateStore(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to create state store: %w", err)
	}

	// Count states before removing
	states, err := store.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to list session states: %w", err)
	}
	count := len(states)

	// Remove the trace directory
	if err := store.RemoveAll(); err != nil {
		return 0, fmt.Errorf("failed to remove session states: %w", err)
	}

	return count, nil
}

// removeTraceDirectory removes the .trace directory.
func removeTraceDirectory(ctx context.Context) error {
	traceDirAbs, err := paths.AbsPath(ctx, paths.TraceDir)
	if err != nil {
		traceDirAbs = paths.TraceDir
	}
	if err := os.RemoveAll(traceDirAbs); err != nil {
		return fmt.Errorf("failed to remove .trace directory: %w", err)
	}
	return nil
}

// removeAllShadowBranches removes all shadow branches.
func removeAllShadowBranches(ctx context.Context) (int, error) {
	branches, err := strategy.ListShadowBranches(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to list shadow branches: %w", err)
	}
	if len(branches) == 0 {
		return 0, nil
	}
	deleted, _, err := strategy.DeleteShadowBranches(ctx, branches)
	return len(deleted), err
}
