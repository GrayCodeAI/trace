package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentpkg "github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/external"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/jsonutil"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/strategy"

	"charm.land/huh/v2"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/spf13/cobra"
)

// unknownSessionID is the fallback session ID used when no session ID is provided.
const unknownSessionID = "unknown"

// getAgent returns an agent by type, falling back to the default agent for empty types.
func getAgent(agentType types.AgentType) (agentpkg.Agent, error) {
	ag, err := strategy.ResolveAgentForRewind(agentType)
	if err != nil {
		return nil, fmt.Errorf("resolving agent: %w", err)
	}
	return ag, nil
}

func newRewindCmd() *cobra.Command {
	var listFlag bool
	var toFlag string
	var logsOnlyFlag bool
	var resetFlag bool
	var dryRunFlag bool

	cmd := &cobra.Command{
		Use:   "rewind",
		Short: "Browse checkpoints and rewind your session",
		Long: `Interactive command for rewinding and managing agent sessions.

This command will show you an interactive list of recent checkpoints.  You'll be
able to select one for Trace to rewind your branch state, including your code and
your agent's context.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Check if Trace is disabled
			if checkDisabledGuard(cmd.Context(), cmd.OutOrStdout()) {
				return nil
			}

			ctx := cmd.Context()

			// Only initialize logging when inside a git worktree to avoid
			// creating .trace/logs/ in arbitrary directories.
			if _, err := paths.WorktreeRoot(ctx); err == nil {
				logging.SetLogLevelGetter(GetLogLevel)
				if err := logging.Init(ctx, ""); err == nil {
					defer logging.Close()
				}
			}

			// Discover external agents so checkpoints from external agents can be resolved.
			external.DiscoverAndRegister(ctx)
			w := cmd.OutOrStdout()
			errW := cmd.ErrOrStderr()
			if listFlag {
				return runRewindList(ctx, w)
			}
			if dryRunFlag {
				return runRewindDryRun(ctx, w, toFlag)
			}
			if toFlag != "" {
				return runRewindToWithOptions(ctx, w, errW, toFlag, logsOnlyFlag, resetFlag)
			}
			return runRewindInteractive(ctx, w, errW)
		},
	}

	cmd.Flags().BoolVar(&listFlag, "list", false, "List available rewind points (JSON output)")
	cmd.Flags().StringVar(&toFlag, "to", "", "Rewind to specific commit ID (non-interactive)")
	cmd.Flags().BoolVar(&logsOnlyFlag, "logs-only", false, "Only restore logs, don't modify working directory (for logs-only points)")
	cmd.Flags().BoolVar(&resetFlag, "reset", false, "Reset branch to commit (destructive, for logs-only points)")
	cmd.Flags().BoolVar(&dryRunFlag, "dry-run", false, "Print what would be restored without actually doing it")

	return cmd
}

func runRewindInteractive(ctx context.Context, w, errW io.Writer) error { //nolint:maintidx // already present in codebase
	// Get the configured strategy
	start := GetStrategy(ctx)

	// Check for uncommitted changes first
	canRewind, changeMsg, err := start.CanRewind(ctx)
	if err != nil {
		return fmt.Errorf("failed to check for uncommitted changes: %w", err)
	}
	if !canRewind {
		fmt.Fprintln(w, changeMsg)
		return nil
	}

	// Get rewind points from strategy
	points, err := start.GetRewindPoints(ctx, 20)
	if err != nil {
		return fmt.Errorf("failed to find rewind points: %w", err)
	}

	if len(points) == 0 {
		fmt.Fprintln(w, "No rewind points found.")
		fmt.Fprintln(w, "Rewind points are created automatically when agent sessions end.")
		return nil
	}

	// Check if there are multiple sessions (to show session identifier)
	sessionIDs := make(map[string]bool)
	for _, p := range points {
		if p.SessionID != "" {
			sessionIDs[p.SessionID] = true
		}
	}
	hasMultipleSessions := len(sessionIDs) > 1

	// Build options for the select menu
	options := make([]huh.Option[string], 0, len(points)+1)
	for _, p := range points {
		var label string
		timestamp := p.Date.Format("2006-01-02 15:04")

		// Build session identifier for display when multiple sessions exist
		sessionLabel := ""
		if hasMultipleSessions && p.SessionPrompt != "" {
			// Show truncated prompt to identify the session
			sessionLabel = fmt.Sprintf(" [%s]", sanitizeForTerminal(p.SessionPrompt))
		}

		switch {
		case p.IsLogsOnly:
			// Committed checkpoint - show commit sha (this is the real user commit)
			shortID := p.ID
			if len(shortID) >= 7 {
				shortID = shortID[:7]
			}
			label = fmt.Sprintf("%s (%s) %s%s", shortID, timestamp, sanitizeForTerminal(p.Message), sessionLabel)
		case p.IsTaskCheckpoint:
			// Task checkpoint (uncommitted) - no sha shown
			label = fmt.Sprintf("        (%s) [Task] %s%s", timestamp, sanitizeForTerminal(p.Message), sessionLabel)
		default:
			// Shadow checkpoint (uncommitted) - no sha shown (internal commit)
			label = fmt.Sprintf("        (%s) %s%s", timestamp, sanitizeForTerminal(p.Message), sessionLabel)
		}
		options = append(options, huh.NewOption(label, p.ID))
	}
	options = append(options, huh.NewOption("Cancel", "cancel"))

	var selectedID string
	form := NewAccessibleForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select a checkpoint to restore").
				Description("Your working directory will be restored to this checkpoint's state").
				Options(options...).
				Value(&selectedID),
		),
	)

	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil
		}
		return fmt.Errorf("selection failed: %w", err)
	}

	if selectedID == "cancel" {
		fmt.Fprintln(w, "Rewind cancelled.")
		return nil
	}

	// Find the selected point
	var selectedPoint *strategy.RewindPoint
	for _, p := range points {
		if p.ID == selectedID {
			pointCopy := p
			selectedPoint = &pointCopy
			break
		}
	}

	if selectedPoint == nil {
		return errors.New("rewind point not found")
	}

	shortID := selectedPoint.ID
	if len(shortID) > 7 {
		shortID = shortID[:7]
	}

	// Show what was selected
	switch {
	case selectedPoint.IsLogsOnly:
		// Committed checkpoint - show sha
		fmt.Fprintf(w, "\nSelected: %s %s\n", shortID, sanitizeForTerminal(selectedPoint.Message))
	case selectedPoint.IsTaskCheckpoint:
		// Task checkpoint - no sha
		fmt.Fprintf(w, "\nSelected: [Task] %s\n", sanitizeForTerminal(selectedPoint.Message))
	default:
		// Shadow checkpoint - no sha
		fmt.Fprintf(w, "\nSelected: %s\n", sanitizeForTerminal(selectedPoint.Message))
	}

	// Handle logs-only points with a sub-choice menu
	if selectedPoint.IsLogsOnly {
		return handleLogsOnlyRewindInteractive(ctx, w, errW, start, *selectedPoint, shortID)
	}

	// Preview rewind to show warnings about files that will be deleted
	preview, previewErr := start.PreviewRewind(ctx, *selectedPoint)
	if previewErr != nil {
		fmt.Fprintf(errW, "Warning: could not preview rewind effects: %v\n", previewErr)
	} else if preview != nil && len(preview.FilesToDelete) > 0 {
		fmt.Fprintf(errW, "\nWarning: The following untracked files will be DELETED:\n")
		for _, f := range preview.FilesToDelete {
			fmt.Fprintf(errW, "  - %s\n", f)
		}
		fmt.Fprintf(errW, "\n")
	}

	// Confirm rewind
	var confirm bool
	description := fmt.Sprintf("This will reset to: %s\nChanges after this point may be lost!", selectedPoint.Message)
	confirmForm := NewAccessibleForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(fmt.Sprintf("Reset to %s?", shortID)).
				Description(description).
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
		fmt.Fprintln(w, "Rewind cancelled.")
		return nil
	}

	// Resolve agent once for use throughout
	agent, err := getAgent(selectedPoint.Agent)
	if err != nil {
		return fmt.Errorf("failed to get agent: %w", err)
	}

	// Initialize logging context with agent from checkpoint
	logCtx := logging.WithComponent(ctx, "rewind")
	logCtx = logging.WithAgent(logCtx, agent.Name())

	logging.Debug(
		logCtx, "rewind started",
		slog.String("checkpoint_id", selectedPoint.ID),
		slog.String("session_id", selectedPoint.SessionID),
		slog.Bool("is_task_checkpoint", selectedPoint.IsTaskCheckpoint),
	)

	// Perform the rewind using strategy
	if err := start.Rewind(ctx, w, errW, *selectedPoint); err != nil {
		logging.Error(
			logCtx, "rewind failed",
			slog.String("checkpoint_id", selectedPoint.ID),
			slog.String("error", err.Error()),
		)
		return err //nolint:wrapcheck // already present in codebase
	}

	logging.Debug(
		logCtx, "rewind completed",
		slog.String("checkpoint_id", selectedPoint.ID),
	)

	// Handle transcript restoration differently for task checkpoints
	var sessionID string
	var transcriptFile string

	if selectedPoint.IsTaskCheckpoint {
		// For task checkpoint: read checkpoint.json to get UUID and truncate transcript
		checkpoint, err := start.GetTaskCheckpoint(ctx, *selectedPoint)
		if err != nil {
			fmt.Fprintf(errW, "Warning: failed to read task checkpoint: %v\n", err)
			return nil
		}

		sessionID = checkpoint.SessionID

		if checkpoint.CheckpointUUID != "" {
			// Truncate transcript at checkpoint UUID
			if err := restoreTaskCheckpointTranscript(ctx, w, start, *selectedPoint, sessionID, checkpoint.CheckpointUUID, agent); err != nil {
				fmt.Fprintf(errW, "Warning: failed to restore truncated session transcript: %v\n", err)
			} else {
				fmt.Fprintf(w, "✓ Rewound to task checkpoint. %s\n", agent.FormatResumeCommand(sessionID))
			}
			return nil
		}
	} else {
		// For session checkpoint: restore full transcript
		// Prefer SessionID from trailer (set by GetRewindPoints from Trace-Session trailer)
		// over path-based extraction which is less reliable.
		sessionID = selectedPoint.SessionID
		if sessionID == "" {
			sessionID = filepath.Base(selectedPoint.MetadataDir)
		}
		transcriptFile = filepath.Join(selectedPoint.MetadataDir, paths.TranscriptFileNameLegacy)
	}

	// Try to restore transcript using the appropriate method:
	// 1. Checkpoint storage (committed checkpoints with valid checkpoint ID)
	// 2. Shadow branch (uncommitted checkpoints with commit hash)
	// 3. Local file (active sessions)
	var restored bool
	if !selectedPoint.CheckpointID.IsEmpty() {
		// Try checkpoint storage first for committed checkpoints
		if returnedSessionID, err := restoreSessionTranscriptFromStrategy(ctx, selectedPoint.CheckpointID, sessionID, agent); err == nil {
			sessionID = returnedSessionID
			restored = true
		}
	}

	if !restored && selectedPoint.MetadataDir != "" && len(selectedPoint.ID) == 40 {
		// Try shadow branch for uncommitted checkpoints (ID is a 40-char commit hash)
		if returnedSessionID, err := restoreSessionTranscriptFromShadow(ctx, selectedPoint.ID, selectedPoint.MetadataDir, sessionID, agent); err == nil {
			sessionID = returnedSessionID
			restored = true
		}
	}

	if !restored {
		// Fall back to local file
		if err := restoreSessionTranscript(ctx, w, transcriptFile, sessionID, agent); err != nil {
			fmt.Fprintf(errW, "Warning: failed to restore session transcript: %v\n", err)
			fmt.Fprintf(errW, "  Source: %s\n", transcriptFile)
			fmt.Fprintf(errW, "  Session ID: %s\n", sessionID)
		}
	}

	fmt.Fprintf(w, "✓ Rewound to %s. %s\n", shortID, agent.FormatResumeCommand(sessionID))
	return nil
}

func runRewindDryRun(ctx context.Context, w io.Writer, commitID string) error {
	start := GetStrategy(ctx)

	points, err := start.GetRewindPoints(ctx, 50)
	if err != nil {
		return fmt.Errorf("failed to find rewind points: %w", err)
	}

	if commitID == "" && len(points) > 0 {
		commitID = points[0].ID
	}

	var target *strategy.RewindPoint
	for i := range points {
		if points[i].ID == commitID {
			target = &points[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("rewind point %q not found", commitID)
	}

	fmt.Fprintf(w, "[dry-run] Would rewind to checkpoint:\n")
	fmt.Fprintf(w, "  ID:      %s\n", target.ID)
	fmt.Fprintf(w, "  Date:    %s\n", target.Date.Format(time.RFC3339))
	fmt.Fprintf(w, "  Message: %s\n", target.Message)
	if target.SessionPrompt != "" {
		fmt.Fprintf(w, "  Prompt:  %s\n", target.SessionPrompt)
	}
	fmt.Fprintf(w, "  Logs-only: %v\n", target.IsLogsOnly)
	fmt.Fprintf(w, "\nNo changes were made.\n")
	return nil
}

func runRewindList(ctx context.Context, w io.Writer) error {
	start := GetStrategy(ctx)

	points, err := start.GetRewindPoints(ctx, 20)
	if err != nil {
		return fmt.Errorf("failed to find rewind points: %w", err)
	}

	// Output as JSON for programmatic use
	type jsonPoint struct {
		ID               string `json:"id"`
		Message          string `json:"message"`
		MetadataDir      string `json:"metadata_dir"`
		Date             string `json:"date"`
		IsTaskCheckpoint bool   `json:"is_task_checkpoint"`
		ToolUseID        string `json:"tool_use_id,omitempty"`
		IsLogsOnly       bool   `json:"is_logs_only"`
		CondensationID   string `json:"condensation_id,omitempty"`
		SessionID        string `json:"session_id,omitempty"`
		SessionPrompt    string `json:"session_prompt,omitempty"`
	}

	output := make([]jsonPoint, len(points))
	for i, p := range points {
		output[i] = jsonPoint{
			ID:               p.ID,
			Message:          p.Message,
			MetadataDir:      p.MetadataDir,
			Date:             p.Date.Format(time.RFC3339),
			IsTaskCheckpoint: p.IsTaskCheckpoint,
			ToolUseID:        p.ToolUseID,
			IsLogsOnly:       p.IsLogsOnly,
			CondensationID:   p.CheckpointID.String(),
			SessionID:        p.SessionID,
			SessionPrompt:    p.SessionPrompt,
		}
	}

	// Print as JSON
	data, err := jsonutil.MarshalIndentWithNewline(output, "", "  ")
	if err != nil {
		return err //nolint:wrapcheck // already present in codebase
	}
	fmt.Fprintln(w, string(data))
	return nil
}

func runRewindToWithOptions(ctx context.Context, w, errW io.Writer, commitID string, logsOnly bool, reset bool) error {
	return runRewindToInternal(ctx, w, errW, commitID, logsOnly, reset)
}

func runRewindToInternal(ctx context.Context, w, errW io.Writer, commitID string, logsOnly bool, reset bool) error {
	start := GetStrategy(ctx)

	// Check for uncommitted changes (skip for reset which handles this itself)
	if !reset {
		canRewind, changeMsg, err := start.CanRewind(ctx)
		if err != nil {
			return fmt.Errorf("failed to check for uncommitted changes: %w", err)
		}
		if !canRewind {
			return fmt.Errorf("%s", changeMsg)
		}
	}

	// Get rewind points
	points, err := start.GetRewindPoints(ctx, 20)
	if err != nil {
		return fmt.Errorf("failed to find rewind points: %w", err)
	}

	// Find the matching point (support both full and short commit IDs)
	var selectedPoint *strategy.RewindPoint
	for _, p := range points {
		if p.ID == commitID || (len(commitID) >= 7 && len(p.ID) >= 7 && strings.HasPrefix(p.ID, commitID)) {
			pointCopy := p
			selectedPoint = &pointCopy
			break
		}
	}

	if selectedPoint == nil {
		return fmt.Errorf("rewind point not found: %s", commitID)
	}

	// Handle reset mode (for logs-only points)
	if reset {
		return handleLogsOnlyResetNonInteractive(ctx, w, errW, start, *selectedPoint)
	}

	// Handle logs-only restoration:
	// 1. For logs-only points, always use logs-only restoration
	// 2. If --logs-only flag is set, use logs-only restoration even for checkpoint points
	if selectedPoint.IsLogsOnly || logsOnly {
		return handleLogsOnlyRewindNonInteractive(ctx, w, errW, start, *selectedPoint)
	}

	// Preview rewind to show warnings about files that will be deleted
	preview, previewErr := start.PreviewRewind(ctx, *selectedPoint)
	if previewErr != nil {
		fmt.Fprintf(errW, "Warning: could not preview rewind effects: %v\n", previewErr)
	} else if preview != nil && len(preview.FilesToDelete) > 0 {
		fmt.Fprintf(errW, "\nWarning: The following untracked files will be DELETED:\n")
		for _, f := range preview.FilesToDelete {
			fmt.Fprintf(errW, "  - %s\n", f)
		}
		fmt.Fprintf(errW, "\n")
	}

	// Resolve agent once for use throughout
	agent, err := getAgent(selectedPoint.Agent)
	if err != nil {
		return fmt.Errorf("failed to get agent: %w", err)
	}

	// Initialize logging context with agent from checkpoint
	logCtx := logging.WithComponent(ctx, "rewind")
	logCtx = logging.WithAgent(logCtx, agent.Name())

	logging.Debug(
		logCtx, "rewind started",
		slog.String("checkpoint_id", selectedPoint.ID),
		slog.String("session_id", selectedPoint.SessionID),
		slog.Bool("is_task_checkpoint", selectedPoint.IsTaskCheckpoint),
	)

	// Perform the rewind
	if err := start.Rewind(ctx, w, errW, *selectedPoint); err != nil {
		logging.Error(
			logCtx, "rewind failed",
			slog.String("checkpoint_id", selectedPoint.ID),
			slog.String("error", err.Error()),
		)
		return err //nolint:wrapcheck // already present in codebase
	}

	logging.Debug(
		logCtx, "rewind completed",
		slog.String("checkpoint_id", selectedPoint.ID),
	)

	// Handle transcript restoration
	var sessionID string
	var transcriptFile string

	if selectedPoint.IsTaskCheckpoint {
		checkpoint, err := start.GetTaskCheckpoint(ctx, *selectedPoint)
		if err != nil {
			fmt.Fprintf(errW, "Warning: failed to read task checkpoint: %v\n", err)
			return nil
		}

		sessionID = checkpoint.SessionID

		if checkpoint.CheckpointUUID != "" {
			// Use strategy-based transcript restoration for task checkpoints
			if err := restoreTaskCheckpointTranscript(ctx, w, start, *selectedPoint, sessionID, checkpoint.CheckpointUUID, agent); err != nil {
				fmt.Fprintf(errW, "Warning: failed to restore truncated session transcript: %v\n", err)
			} else {
				fmt.Fprintf(w, "✓ Rewound to task checkpoint. %s\n", agent.FormatResumeCommand(sessionID))
			}
			return nil
		}
	} else {
		// Prefer SessionID from trailer over path-based extraction
		sessionID = selectedPoint.SessionID
		if sessionID == "" {
			sessionID = filepath.Base(selectedPoint.MetadataDir)
		}
		transcriptFile = filepath.Join(selectedPoint.MetadataDir, paths.TranscriptFileNameLegacy)
	}

	// Try to restore transcript using the appropriate method:
	// 1. Checkpoint storage (committed checkpoints with valid checkpoint ID)
	// 2. Shadow branch (uncommitted checkpoints with commit hash)
	// 3. Local file (active sessions)
	var restored bool
	if !selectedPoint.CheckpointID.IsEmpty() {
		// Try checkpoint storage first for committed checkpoints
		if returnedSessionID, err := restoreSessionTranscriptFromStrategy(ctx, selectedPoint.CheckpointID, sessionID, agent); err == nil {
			sessionID = returnedSessionID
			restored = true
		}
	}

	if !restored && selectedPoint.MetadataDir != "" && len(selectedPoint.ID) == 40 {
		// Try shadow branch for uncommitted checkpoints (ID is a 40-char commit hash)
		if returnedSessionID, err := restoreSessionTranscriptFromShadow(ctx, selectedPoint.ID, selectedPoint.MetadataDir, sessionID, agent); err == nil {
			sessionID = returnedSessionID
			restored = true
		}
	}

	if !restored {
		// Fall back to local file
		if err := restoreSessionTranscript(ctx, w, transcriptFile, sessionID, agent); err != nil {
			fmt.Fprintf(errW, "Warning: failed to restore session transcript: %v\n", err)
		}
	}

	fmt.Fprintf(w, "✓ Rewound to %s. %s\n", selectedPoint.ID[:7], agent.FormatResumeCommand(sessionID))
	return nil
}

// handleLogsOnlyRewindNonInteractive handles logs-only rewind in non-interactive mode.
// Defaults to restoring logs only (no checkout) for safety.
func handleLogsOnlyRewindNonInteractive(ctx context.Context, w, errW io.Writer, start *strategy.ManualCommitStrategy, point strategy.RewindPoint) error {
	// Resolve agent once for use throughout
	agent, err := getAgent(point.Agent)
	if err != nil {
		return fmt.Errorf("failed to get agent: %w", err)
	}

	// Initialize logging context with agent from checkpoint
	logCtx := logging.WithComponent(ctx, "rewind")
	logCtx = logging.WithAgent(logCtx, agent.Name())

	logging.Debug(
		logCtx, "logs-only rewind started",
		slog.String("checkpoint_id", point.ID),
		slog.String("session_id", point.SessionID),
	)

	sessions, err := start.RestoreLogsOnly(ctx, w, errW, point, true) // force=true for explicit rewind
	if err != nil {
		logging.Error(
			logCtx, "logs-only rewind failed",
			slog.String("checkpoint_id", point.ID),
			slog.String("error", err.Error()),
		)
		return fmt.Errorf("failed to restore logs: %w", err)
	}

	logging.Debug(
		logCtx, "logs-only rewind completed",
		slog.String("checkpoint_id", point.ID),
	)

	// Show resume commands for all sessions
	printMultiSessionResumeCommands(w, errW, sessions)

	fmt.Fprintln(w, "Note: Working directory unchanged. Use interactive mode for full checkout.")
	return nil
}

// handleLogsOnlyResetNonInteractive handles reset in non-interactive mode.
// This performs a git reset --hard to the target commit.
func handleLogsOnlyResetNonInteractive(ctx context.Context, w, errW io.Writer, start *strategy.ManualCommitStrategy, point strategy.RewindPoint) error {
	// Resolve agent once for use throughout
	agent, err := getAgent(point.Agent)
	if err != nil {
		return fmt.Errorf("failed to get agent: %w", err)
	}

	// Initialize logging context with agent from checkpoint
	logCtx := logging.WithComponent(ctx, "rewind")
	logCtx = logging.WithAgent(logCtx, agent.Name())

	logging.Debug(
		logCtx, "logs-only reset started",
		slog.String("checkpoint_id", point.ID),
		slog.String("session_id", point.SessionID),
	)

	// Get current HEAD before reset (for recovery message)
	currentHead, headErr := getCurrentHeadHash(ctx)
	if headErr != nil {
		currentHead = ""
	}

	// Restore logs first
	sessions, err := start.RestoreLogsOnly(ctx, w, errW, point, true) // force=true for explicit rewind
	if err != nil {
		logging.Error(
			logCtx, "logs-only reset failed during log restoration",
			slog.String("checkpoint_id", point.ID),
			slog.String("error", err.Error()),
		)
		return fmt.Errorf("failed to restore logs: %w", err)
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

	logging.Debug(
		logCtx, "logs-only reset completed",
		slog.String("checkpoint_id", point.ID),
	)

	shortID := point.ID
	if len(shortID) > 7 {
		shortID = shortID[:7]
	}

	fmt.Fprintf(w, "✓ Reset branch to %s.\n", shortID)

	// Show resume commands for all sessions
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

func restoreSessionTranscript(ctx context.Context, w io.Writer, transcriptFile, sessionID string, agent agentpkg.Agent) error {
	sessionFile, err := resolveTranscriptPath(ctx, sessionID, agent)
	if err != nil {
		return err
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o750); err != nil {
		return fmt.Errorf("failed to create agent session directory: %w", err)
	}

	fmt.Fprintf(w, "Copying transcript:\n  From: %s\n  To: %s\n", transcriptFile, sessionFile)
	if err := copyFile(transcriptFile, sessionFile); err != nil {
		return fmt.Errorf("failed to copy transcript: %w", err)
	}

	return nil
}

// restoreSessionTranscriptFromStrategy restores a session transcript from checkpoint storage.
// This is used for strategies that store transcripts in git branches rather than local files.
// Returns the session ID that was actually used (may differ from input if checkpoint provides one).
func restoreSessionTranscriptFromStrategy(ctx context.Context, cpID id.CheckpointID, sessionID string, agent agentpkg.Agent) (string, error) {
	// Get transcript content from checkpoint storage
	content, returnedSessionID, err := checkpoint.LookupSessionLog(ctx, cpID)
	if err != nil {
		return "", fmt.Errorf("failed to get session log: %w", err)
	}

	// Use session ID returned from checkpoint if available
	// Otherwise fall back to the passed-in sessionID
	if returnedSessionID != "" {
		sessionID = returnedSessionID
	}

	sessionFile, err := resolveTranscriptPath(ctx, sessionID, agent)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o750); err != nil {
		return "", fmt.Errorf("failed to create agent session directory: %w", err)
	}
	agentSession := &agentpkg.AgentSession{
		SessionID:  sessionID,
		AgentName:  agent.Name(),
		SessionRef: sessionFile,
		NativeData: content,
	}
	if err := agent.WriteSession(ctx, agentSession); err != nil {
		return "", fmt.Errorf("failed to write session: %w", err)
	}
	return sessionID, nil
}

// restoreSessionTranscriptFromShadow restores a session transcript from a shadow branch commit.
// This is used for uncommitted checkpoints where the transcript is stored in the shadow branch tree.
func restoreSessionTranscriptFromShadow(ctx context.Context, commitHash, metadataDir, sessionID string, agent agentpkg.Agent) (string, error) {
	// Open repository
	repo, err := git.PlainOpenWithOptions(".", &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}

	// Parse commit hash
	hash := plumbing.NewHash(commitHash)
	if hash.IsZero() {
		return "", fmt.Errorf("invalid commit hash: %s", commitHash)
	}

	// Get transcript from shadow branch commit tree
	store := checkpoint.NewGitStore(repo)
	content, err := store.GetTranscriptFromCommit(ctx, hash, metadataDir, agent.Type())
	if err != nil {
		return "", fmt.Errorf("failed to get transcript from shadow branch: %w", err)
	}

	sessionFile, err := resolveTranscriptPath(ctx, sessionID, agent)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o750); err != nil {
		return "", fmt.Errorf("failed to create agent session directory: %w", err)
	}
	agentSession := &agentpkg.AgentSession{
		SessionID:  sessionID,
		AgentName:  agent.Name(),
		SessionRef: sessionFile,
		NativeData: content,
	}
	if err := agent.WriteSession(ctx, agentSession); err != nil {
		return "", fmt.Errorf("failed to write session: %w", err)
	}
	return sessionID, nil
}
