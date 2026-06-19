package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/checkpoint/remote"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cli/strategy"

	"charm.land/huh/v2"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// tryReadCheckpointFromTree attempts to read checkpoint metadata from a metadata tree.
func tryReadCheckpointFromTree(ctx context.Context, tree *object.Tree, repo *git.Repository, checkpointID id.CheckpointID) (*strategy.CheckpointInfo, error) {
	cpSubtree, cpErr := tree.Tree(checkpointID.Path())
	if cpErr != nil {
		return nil, fmt.Errorf("checkpoint subtree not found: %w", cpErr)
	}
	ft := checkpoint.NewFetchingTree(ctx, cpSubtree, repo.Storer, FetchBlobsByHash)
	if _, pfErr := ft.PreFetch(); pfErr != nil {
		logging.Debug(
			ctx, "tryReadCheckpointFromTree: PreFetch failed",
			slog.String("checkpoint_id", checkpointID.String()),
			slog.String("error", pfErr.Error()),
		)
	}
	metadata, err := strategy.ReadCheckpointMetadataFromSubtree(ft, checkpointID.Path())
	if err != nil {
		return nil, fmt.Errorf("failed to read checkpoint metadata: %w", err)
	}
	return metadata, nil
}

// resumeSession restores and displays the resume command for a specific session.
// For multi-session checkpoints, restores ALL sessions and shows commands for each.
// If force is false, prompts for confirmation when local logs have newer timestamps.
// The caller must provide the already-resolved checkpoint metadata to avoid redundant lookups
// and to support both local and remote metadata trees.
func resumeSession(ctx context.Context, w, errW io.Writer, metadata *strategy.CheckpointInfo, force bool) error {
	checkpointID := metadata.CheckpointID
	sessionID := metadata.SessionID

	// Resolve agent from checkpoint metadata (same as rewind)
	ag, err := strategy.ResolveAgentForRewind(metadata.Agent)
	if err != nil {
		return fmt.Errorf("failed to resolve agent: %w", err)
	}

	// Initialize logging context with agent
	logCtx := logging.WithAgent(logging.WithComponent(ctx, "resume"), ag.Name())

	logging.Debug(
		logCtx, "resume session started",
		slog.String("checkpoint_id", checkpointID.String()),
		slog.String("session_id", sessionID),
	)

	// Get worktree root for session directory lookup
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return fmt.Errorf("failed to get worktree root: %w", err)
	}

	sessionDir, err := ag.GetSessionDir(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to determine session directory: %w", err)
	}

	// Create directory if it doesn't exist
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return fmt.Errorf("failed to create session directory: %w", err)
	}

	// Get strategy and restore sessions using full checkpoint data
	start := GetStrategy(ctx)

	// Use RestoreLogsOnly via LogsOnlyRestorer interface for multi-session support
	// Create a logs-only rewind point with Agent populated (same as rewind)
	point := strategy.RewindPoint{
		IsLogsOnly:   true,
		CheckpointID: checkpointID,
		Agent:        metadata.Agent,
	}

	sessions, restoreErr := start.RestoreLogsOnly(ctx, w, errW, point, force)
	if restoreErr != nil || len(sessions) == 0 {
		// Fall back to single-session restore (e.g., old checkpoints without agent metadata)
		return resumeSingleSession(ctx, w, errW, ag, sessionID, checkpointID, repoRoot, force)
	}

	logging.Debug(
		logCtx, "resume session completed",
		slog.String("checkpoint_id", checkpointID.String()),
		slog.Int("session_count", len(sessions)),
	)

	return displayRestoredSessions(w, sessions)
}

// displayRestoredSessions sorts sessions by CreatedAt and prints resume commands.
func displayRestoredSessions(w io.Writer, sessions []strategy.RestoredSession) error {
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].CreatedAt.Before(sessions[j].CreatedAt)
	})

	if len(sessions) > 1 {
		fmt.Fprintf(w, "\n✓ Restored %d sessions. To continue:\n", len(sessions))
	} else if len(sessions) == 1 {
		fmt.Fprintf(w, "✓ Restored session %s.\n", sessions[0].SessionID)
		fmt.Fprintf(w, "\nTo continue this session:\n")
	}

	isMulti := len(sessions) > 1
	for i, sess := range sessions {
		sessionAgent, err := strategy.ResolveAgentForRewind(sess.Agent)
		if err != nil {
			return fmt.Errorf("failed to resolve agent for session %s: %w", sess.SessionID, err)
		}
		printSessionCommand(w, sessionAgent.FormatResumeCommand(sess.SessionID), sess.Prompt, isMulti, i == len(sessions)-1)
	}

	return nil
}

// resumeSingleSession restores a single session (fallback when multi-session restore fails).
// Always overwrites existing session logs to ensure consistency with checkpoint state.
// If force is false, prompts for confirmation when local log has newer timestamps.
func resumeSingleSession(ctx context.Context, w, errW io.Writer, ag agent.Agent, sessionID string, checkpointID id.CheckpointID, repoRoot string, force bool) error {
	sessionLogPath, err := resolveTranscriptPath(ctx, sessionID, ag)
	if err != nil {
		return fmt.Errorf("failed to resolve transcript path: %w", err)
	}

	if checkpointID.IsEmpty() {
		logging.Debug(
			ctx, "resume session: empty checkpoint ID",
			slog.String("checkpoint_id", checkpointID.String()),
		)
		fmt.Fprintf(w, "Session '%s' found in commit trailer but session log not available\n", sessionID)
		fmt.Fprintf(w, "\nTo continue this session:\n")
		fmt.Fprintf(w, "  %s\n", ag.FormatResumeCommand(sessionID))
		return nil
	}

	var logContent []byte
	err = nil // Reset before v2/v1 resolution to avoid stale error from earlier code paths
	if settings.IsCheckpointsV2Enabled(ctx) {
		repo, repoErr := openRepository(ctx)
		if repoErr == nil {
			v2URL, fetchRemoteErr := remote.FetchURL(ctx)
			if fetchRemoteErr != nil {
				logging.Debug(
					ctx, "resume: using origin for v2 session log fetch remote",
					slog.String("error", fetchRemoteErr.Error()),
				)
				v2URL = ""
			}
			v2Store := checkpoint.NewV2GitStore(repo, v2URL)
			var v2Err error
			logContent, _, v2Err = v2Store.GetSessionLog(ctx, checkpointID)
			if v2Err != nil {
				logging.Debug(
					ctx, "v2 GetSessionLog failed, falling back to v1",
					slog.String("checkpoint_id", checkpointID.String()),
					slog.String("error", v2Err.Error()),
				)
			}
		}
	}
	if len(logContent) == 0 {
		logContent, _, err = checkpoint.LookupSessionLog(ctx, checkpointID)
	}
	if err != nil {
		if errors.Is(err, checkpoint.ErrCheckpointNotFound) || errors.Is(err, checkpoint.ErrNoTranscript) {
			logging.Debug(
				ctx, "resume session completed (no metadata)",
				slog.String("checkpoint_id", checkpointID.String()),
				slog.String("session_id", sessionID),
			)
			fmt.Fprintf(w, "Session '%s' found in commit trailer but session log not available\n", sessionID)
			fmt.Fprintf(w, "\nTo continue this session:\n")
			fmt.Fprintf(w, "  %s\n", ag.FormatResumeCommand(sessionID))
			return nil
		}
		logging.Error(
			ctx, "resume session failed",
			slog.String("checkpoint_id", checkpointID.String()),
			slog.String("session_id", sessionID),
			slog.String("error", err.Error()),
		)
		return fmt.Errorf("failed to get session log: %w", err)
	}

	// Check if local file has newer timestamps than checkpoint
	if !force {
		localTime := paths.GetLastTimestampFromFile(sessionLogPath)
		checkpointTime := paths.GetLastTimestampFromBytes(logContent)
		status := strategy.ClassifyTimestamps(localTime, checkpointTime)

		if status == strategy.StatusLocalNewer {
			sessions := []strategy.SessionRestoreInfo{{
				SessionID:      sessionID,
				Status:         status,
				LocalTime:      localTime,
				CheckpointTime: checkpointTime,
			}}
			shouldOverwrite, promptErr := strategy.PromptOverwriteNewerLogs(errW, sessions)
			if promptErr != nil {
				return fmt.Errorf("failed to get confirmation: %w", promptErr)
			}
			if !shouldOverwrite {
				fmt.Fprintf(w, "Resume cancelled. Local session log preserved.\n")
				return nil
			}
		}
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(sessionLogPath), 0o750); err != nil {
		return fmt.Errorf("failed to create session directory: %w", err)
	}

	agentSession := &agent.AgentSession{
		SessionID:  sessionID,
		AgentName:  ag.Name(),
		RepoPath:   repoRoot,
		SessionRef: sessionLogPath,
		NativeData: logContent,
	}

	// Write the session using the agent's WriteSession method
	if err := ag.WriteSession(ctx, agentSession); err != nil {
		logging.Error(
			ctx, "resume session failed during write",
			slog.String("checkpoint_id", checkpointID.String()),
			slog.String("session_id", sessionID),
			slog.String("error", err.Error()),
		)
		return fmt.Errorf("failed to write session: %w", err)
	}

	logging.Debug(
		ctx, "resume session completed",
		slog.String("checkpoint_id", checkpointID.String()),
		slog.String("session_id", sessionID),
	)

	fmt.Fprintf(w, "✓ Session restored to: %s\n", sessionLogPath)
	fmt.Fprintf(w, "  Session: %s\n", sessionID)
	fmt.Fprintf(w, "\nTo continue this session:\n")
	fmt.Fprintf(w, "  %s\n", ag.FormatResumeCommand(sessionID))

	return nil
}

func promptFetchFromRemote(branchName string) (bool, error) {
	var confirmed bool

	form := NewAccessibleForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(fmt.Sprintf("Branch '%s' not found locally. Fetch from origin?", branchName)).
				Value(&confirmed),
		),
	)

	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, fmt.Errorf("failed to get confirmation: %w", err)
	}

	return confirmed, nil
}

// firstLine returns the first line of a string
func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	return s
}
