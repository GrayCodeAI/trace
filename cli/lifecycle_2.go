package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/session"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cli/transcript"
)

// handleLifecycleSubagentEnd handles subagent end: detects changes, saves task checkpoint.
func handleLifecycleSubagentEnd(ctx context.Context, ag agent.Agent, event *agent.Event) error {
	logCtx := logging.WithAgent(logging.WithComponent(ctx, "lifecycle"), ag.Name())
	if event.SubagentType == "" && event.TaskDescription == "" {
		// Extract subagent type and description from tool input
		event.SubagentType, event.TaskDescription = ParseSubagentTypeAndDescription(event.ToolInput)
	}

	// Determine subagent transcript path
	transcriptDir := filepath.Dir(event.SessionRef)
	var subagentTranscriptPath string
	if event.SubagentID != "" {
		subagentTranscriptPath = AgentTranscriptPath(transcriptDir, event.SubagentID)
		if !fileExists(subagentTranscriptPath) {
			subagentTranscriptPath = ""
		}
	}

	// Log context
	subagentEndAttrs := []any{
		slog.String("event", event.Type.String()),
		slog.String("session_id", event.SessionID),
		slog.String("tool_use_id", event.ToolUseID),
	}
	if event.SubagentID != "" {
		subagentEndAttrs = append(subagentEndAttrs, slog.String("agent_id", event.SubagentID))
	}
	if subagentTranscriptPath != "" {
		subagentEndAttrs = append(subagentEndAttrs, slog.String("subagent_transcript", subagentTranscriptPath))
	}
	logging.Info(logCtx, "subagent completed", subagentEndAttrs...)

	// Extract modified files from hook payload and/or subagent transcript
	var modifiedFiles []string
	modifiedFiles = append(modifiedFiles, event.ModifiedFiles...)
	if analyzer, ok := agent.AsTranscriptAnalyzer(ag); ok {
		transcriptToScan := event.SessionRef
		if subagentTranscriptPath != "" {
			transcriptToScan = subagentTranscriptPath
		}
		if files, _, fileErr := analyzer.ExtractModifiedFilesFromOffset(transcriptToScan, 0); fileErr != nil {
			logging.Warn(logCtx, "failed to extract modified files from subagent",
				slog.String("error", fileErr.Error()))
		} else {
			modifiedFiles = mergeUnique(modifiedFiles, files)
		}
	}

	// Load pre-task state and detect file changes.
	// If no pre-task state exists (agent doesn't support pre-task hook), fall back
	// to the session's pre-prompt state. Without either, DetectFileChanges receives
	// nil and treats ALL untracked files as new — which would create spurious task
	// checkpoints for pre-existing untracked files (e.g., .github/hooks/trace.json).
	preState, err := LoadPreTaskState(ctx, event.ToolUseID)
	if err != nil {
		logging.Warn(logCtx, "failed to load pre-task state",
			slog.String("error", err.Error()))
	}
	var preUntrackedFiles []string
	if preState != nil {
		preUntrackedFiles = preState.PreUntrackedFiles()
	}
	changes, err := DetectFileChanges(ctx, preUntrackedFiles)
	if err != nil {
		logging.Warn(logCtx, "failed to compute file changes",
			slog.String("error", err.Error()))
	}

	// Get worktree root and normalize paths
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return fmt.Errorf("failed to get worktree root: %w", err)
	}

	relModifiedFiles := FilterAndNormalizePaths(modifiedFiles, repoRoot)
	var relNewFiles, relDeletedFiles []string
	if changes != nil {
		relNewFiles = FilterAndNormalizePaths(changes.New, repoRoot)
		relDeletedFiles = FilterAndNormalizePaths(changes.Deleted, repoRoot)
		relModifiedFiles = mergeUnique(relModifiedFiles, FilterAndNormalizePaths(changes.Modified, repoRoot))
	}

	// If no changes, skip
	if len(relModifiedFiles) == 0 && len(relNewFiles) == 0 && len(relDeletedFiles) == 0 {
		logging.Info(logCtx, "no file changes detected, skipping task checkpoint")
		_ = CleanupPreTaskState(ctx, event.ToolUseID) //nolint:errcheck // best-effort cleanup
		return nil
	}

	// Find checkpoint UUID from main transcript (best-effort)
	var checkpointUUID string
	// Use the existing CLI-level checkpoint UUID finder
	mainLines, _ := parseTranscriptForCheckpointUUID(event.SessionRef) //nolint:errcheck // best-effort
	if mainLines != nil {
		checkpointUUID, _ = FindCheckpointUUID(mainLines, event.ToolUseID)
	}

	// Get git author
	author, err := GetGitAuthor(ctx)
	if err != nil {
		return fmt.Errorf("failed to get git author: %w", err)
	}

	// Build task checkpoint context
	start := GetStrategy(ctx)
	agentType := ag.Type()

	taskStepCtx := strategy.TaskStepContext{
		SessionID:              event.SessionID,
		ToolUseID:              event.ToolUseID,
		AgentID:                event.SubagentID,
		ModifiedFiles:          relModifiedFiles,
		NewFiles:               relNewFiles,
		DeletedFiles:           relDeletedFiles,
		TranscriptPath:         event.SessionRef,
		SubagentTranscriptPath: subagentTranscriptPath,
		CheckpointUUID:         checkpointUUID,
		AuthorName:             author.Name,
		AuthorEmail:            author.Email,
		SubagentType:           event.SubagentType,
		TaskDescription:        event.TaskDescription,
		AgentType:              agentType,
	}

	if err := start.SaveTaskStep(ctx, taskStepCtx); err != nil {
		return fmt.Errorf("failed to save task step: %w", err)
	}

	_ = CleanupPreTaskState(ctx, event.ToolUseID) //nolint:errcheck // best-effort cleanup
	return nil
}

// --- Helper functions ---

// resolveTranscriptOffset determines the transcript offset to use for parsing.
// Prefers pre-prompt state, falls back to session state.
func resolveTranscriptOffset(ctx context.Context, preState *PrePromptState, sessionID string) int {
	logCtx := logging.WithComponent(ctx, "lifecycle")
	if preState != nil && preState.TranscriptOffset > 0 {
		logging.Debug(logCtx, "pre-prompt state found, parsing transcript from offset",
			slog.Int("offset", preState.TranscriptOffset))
		return preState.TranscriptOffset
	}

	// Fall back to session state
	sessionState, loadErr := strategy.LoadSessionState(ctx, sessionID)
	if loadErr != nil {
		logging.Warn(logCtx, "failed to load session state",
			slog.String("error", loadErr.Error()))
		return 0
	}
	if sessionState != nil && sessionState.CheckpointTranscriptStart > 0 {
		logging.Debug(logCtx, "session state found, parsing transcript from offset",
			slog.Int("offset", sessionState.CheckpointTranscriptStart))
		return sessionState.CheckpointTranscriptStart
	}

	return 0
}

// parseTranscriptForCheckpointUUID is a thin wrapper around transcript parsing for checkpoint UUID lookup.
// Returns parsed transcript lines for use with FindCheckpointUUID.
func parseTranscriptForCheckpointUUID(transcriptPath string) ([]transcriptLine, error) {
	lines, err := transcript.ParseFromFileAtLine(transcriptPath, 0)
	if err != nil {
		return nil, fmt.Errorf("parsing transcript for checkpoint UUID: %w", err)
	}
	return lines, nil
}

// transitionSessionTurnEnd transitions the session phase to IDLE and dispatches turn-end actions.
func transitionSessionTurnEnd(ctx context.Context, sessionID string, event *agent.Event) {
	logCtx := logging.WithComponent(ctx, "lifecycle")
	turnState, loadErr := strategy.LoadSessionState(ctx, sessionID)
	if loadErr != nil {
		logging.Warn(logCtx, "failed to load session state for turn end",
			slog.String("error", loadErr.Error()))
		return
	}
	if turnState == nil {
		return
	}

	persistEventMetadataToState(event, turnState)

	if err := strategy.TransitionAndLog(ctx, turnState, session.EventTurnEnd, session.TransitionContext{}, session.NoOpActionHandler{}); err != nil {
		logging.Warn(logCtx, "turn-end transition failed",
			slog.String("error", err.Error()))
	}

	// Always dispatch to strategy for turn-end handling. The strategy reads
	// work items from state (e.g. TurnCheckpointIDs), not the action list.
	start := GetStrategy(ctx)
	if err := start.HandleTurnEnd(ctx, turnState); err != nil {
		logging.Warn(logCtx, "turn-end action dispatch failed",
			slog.String("error", err.Error()))
	}

	if updateErr := strategy.SaveSessionState(ctx, turnState); updateErr != nil {
		logging.Warn(logCtx, "failed to update session phase on turn end",
			slog.String("error", updateErr.Error()))
	}
}

// markSessionEnded transitions the session to ENDED phase via the state machine.
// If event is non-nil, hook-provided metrics are persisted to state before saving.
func markSessionEnded(ctx context.Context, event *agent.Event, sessionID string) error {
	state, err := strategy.LoadSessionState(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to load session state: %w", err)
	}
	if state == nil {
		return nil // No state file, nothing to update
	}

	if event != nil {
		persistEventMetadataToState(event, state)
	}

	if transErr := strategy.TransitionAndLog(ctx, state, session.EventSessionStop, session.TransitionContext{}, session.NoOpActionHandler{}); transErr != nil {
		logging.Warn(logging.WithComponent(ctx, "lifecycle"), "session stop transition failed",
			slog.String("error", transErr.Error()))
	}

	now := time.Now()
	state.EndedAt = &now

	if err := strategy.SaveSessionState(ctx, state); err != nil {
		return fmt.Errorf("failed to save session state: %w", err)
	}
	return nil
}

// logFileChanges logs the files modified, created, and deleted during a session.
func logFileChanges(ctx context.Context, modified, newFiles, deleted []string) {
	logCtx := logging.WithComponent(ctx, "lifecycle")
	logging.Debug(logCtx, "files changed during session",
		slog.Int("modified", len(modified)),
		slog.Int("new", len(newFiles)),
		slog.Int("deleted", len(deleted)))
}

func persistEventMetadataToState(event *agent.Event, state *strategy.SessionState) {
	// Update ModelName if provided (model is known by turn-end even on first turn)
	if event.Model != "" {
		state.ModelName = event.Model
	}

	// Persist hook-provided session metrics (e.g., from Cursor hooks)
	if event.DurationMs > 0 {
		state.SessionDurationMs = event.DurationMs
	}
	// Use hook-reported turn count if available (take max); otherwise
	// increment on each TurnEnd event to count turns ourselves.
	if event.TurnCount > 0 {
		if event.TurnCount > state.SessionTurnCount {
			state.SessionTurnCount = event.TurnCount
		}
	} else if event.Type == agent.TurnEnd {
		state.SessionTurnCount++
	}
	if event.ContextTokens > 0 {
		state.ContextTokens = event.ContextTokens
	}
	if event.ContextWindowSize > 0 {
		state.ContextWindowSize = event.ContextWindowSize
	}
}

// xorObfuscate applies XOR obfuscation to data using a key derived from the
// session ID. Because XOR is its own inverse, calling this function twice with
// the same sessionID returns the original data.
//
// NOTE: This is obfuscation, NOT encryption. It deters casual reading of
// prompt.txt on disk but provides no real security against a determined
// attacker who can read the source code or the session ID.
func xorObfuscate(data []byte, sessionID string) []byte {
	hash := sha256.Sum256([]byte(sessionID))
	result := make([]byte, len(data))
	for i, b := range data {
		result[i] = b ^ hash[i%len(hash)]
	}
	return result
}
