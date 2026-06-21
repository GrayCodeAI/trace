package strategy

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/types"

	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/interactive"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/provenance"
	"github.com/GrayCodeAI/trace/cli/review"
	"github.com/GrayCodeAI/trace/cli/session"
	"github.com/GrayCodeAI/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cli/trailers"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// sessionHasNewContent checks if a session has new transcript content
// beyond what was already condensed.
// The opts parameter provides pre-computed values to avoid redundant work.
func (s *ManualCommitStrategy) sessionHasNewContent(ctx context.Context, repo *git.Repository, state *SessionState, opts contentCheckOpts) (bool, error) {
	logCtx := logging.WithComponent(ctx, "manual-commit")

	// Use cached shadow tree if provided
	var tree *object.Tree
	if opts.shadowTree != nil {
		tree = opts.shadowTree
	} else {
		// Resolve shadow branch from repo
		shadowBranchName := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)
		refName := plumbing.NewBranchReferenceName(shadowBranchName)
		ref, err := repo.Reference(refName, true)
		if err != nil {
			logging.Debug(
				logCtx, "sessionHasNewContent: no shadow branch, checking live transcript",
				slog.String("session_id", state.SessionID),
				slog.String("shadow_branch", shadowBranchName),
			)
			return s.sessionHasNewContentFromLiveTranscript(ctx, state, opts.stagedFiles)
		}

		commit, err := repo.CommitObject(ref.Hash())
		if err != nil {
			return false, fmt.Errorf("failed to get commit object: %w", err)
		}

		tree, err = commit.Tree()
		if err != nil {
			return false, fmt.Errorf("failed to get commit tree: %w", err)
		}
	}

	// Look for transcript file — use blob size for fast growth check when possible.
	// This avoids reading the full transcript content (potentially tens of MB) just
	// to count lines, which was the main source of PostCommit latency with many sessions.
	metadataDir := paths.TraceMetadataDir + "/" + state.SessionID
	var hasTranscriptFile bool
	var transcriptBlobSize int64

	if size, sizeErr := tree.Size(metadataDir + "/" + paths.TranscriptFileName); sizeErr == nil {
		hasTranscriptFile = true
		transcriptBlobSize = size
	} else if size, sizeErr := tree.Size(metadataDir + "/" + paths.TranscriptFileNameLegacy); sizeErr == nil {
		hasTranscriptFile = true
		transcriptBlobSize = size
	}

	// If shadow branch exists but has no transcript (e.g., carry-forward from mid-session commit),
	// check if the session has FilesTouched. Carry-forward sets FilesTouched with remaining files.
	if !hasTranscriptFile {
		if len(state.FilesTouched) > 0 {
			// Shadow branch has files from carry-forward - check if staged files overlap
			// AND have matching content (content-aware check).
			if len(opts.stagedFiles) > 0 {
				// PrepareCommitMsg context: check staged files overlap with content
				result := stagedFilesOverlapWithContent(ctx, repo, tree, opts.stagedFiles, state.FilesTouched)
				logging.Debug(
					logCtx, "sessionHasNewContent: no transcript, carry-forward with staged files",
					slog.String("session_id", state.SessionID),
					slog.Int("files_touched", len(state.FilesTouched)),
					slog.Int("staged_files", len(opts.stagedFiles)),
					slog.Bool("result", result),
				)
				return result, nil
			}
			// PostCommit context: no staged files, but we have carry-forward files.
			// Return true and let the caller do the overlap check with committed files.
			logging.Debug(
				logCtx, "sessionHasNewContent: no transcript, carry-forward without staged files (post-commit context)",
				slog.String("session_id", state.SessionID),
				slog.Int("files_touched", len(state.FilesTouched)),
			)
			return true, nil
		}
		// No transcript and no FilesTouched - fall back to live transcript check
		logging.Debug(
			logCtx, "sessionHasNewContent: no transcript and no files touched, checking live transcript",
			slog.String("session_id", state.SessionID),
		)
		return s.sessionHasNewContentFromLiveTranscript(ctx, state, opts.stagedFiles)
	}

	// Check if there's new content to condense. Two cases:
	// 1. Transcript has grown since last condensation (new prompts/responses)
	// 2. FilesTouched has files not yet committed (carry-forward scenario)
	//
	// For PrepareCommitMsg context, we verify staged files overlap with session's files
	// using content-aware matching to detect reverted files.
	// For PostCommit context, stagedFiles is nil/empty (files already committed),
	// so we return true and let the caller do the overlap check via filesOverlapWithContent.

	// Fast path: compare blob size against stored size from last condensation.
	// This avoids reading the full transcript content just to count items.
	var hasTranscriptGrowth bool
	switch {
	case state.CheckpointTranscriptSize > 0:
		hasTranscriptGrowth = transcriptBlobSize > state.CheckpointTranscriptSize
	case state.CheckpointTranscriptStart > 0:
		// Legacy session: condensed at least once (has line count) but no size tracking.
		// Cannot safely compare sizes — conservatively assume growth so condensation
		// can do the full content check. After one condensation with the new CLI,
		// CheckpointTranscriptSize will be populated and this path won't be hit again.
		hasTranscriptGrowth = true
	default:
		// Never condensed (CheckpointTranscriptStart == 0): any content means growth.
		hasTranscriptGrowth = transcriptBlobSize > 0
	}
	hasUncommittedFiles := len(state.FilesTouched) > 0

	logging.Debug(
		logCtx, "sessionHasNewContent: transcript size check",
		slog.String("session_id", state.SessionID),
		slog.Int64("transcript_blob_size", transcriptBlobSize),
		slog.Int64("checkpoint_transcript_size", state.CheckpointTranscriptSize),
		slog.Bool("has_transcript_growth", hasTranscriptGrowth),
		slog.Bool("has_uncommitted_files", hasUncommittedFiles),
	)

	if !hasTranscriptGrowth && !hasUncommittedFiles {
		return false, nil // No new content and no carry-forward files
	}

	// Check if staged files overlap with session's files with content-aware matching.
	// This is primarily for PrepareCommitMsg; in PostCommit, stagedFiles is nil/empty.
	if len(opts.stagedFiles) > 0 {
		result := stagedFilesOverlapWithContent(ctx, repo, tree, opts.stagedFiles, state.FilesTouched)
		logging.Debug(
			logCtx, "sessionHasNewContent: staged files overlap check",
			slog.String("session_id", state.SessionID),
			slog.Int("staged_files", len(opts.stagedFiles)),
			slog.Bool("result", result),
		)
		return result, nil
	}

	// No staged files - either PostCommit context or edge case.
	//
	// For recently-active IDLE sessions, the staged set is already gone by
	// PostCommit, but carry-forward files from the just-ended turn must still
	// count as "new content". The caller performs the stricter committed-file
	// overlap check before actually condensing, which prevents false positives
	// from unrelated commits.
	//
	// Stale IDLE sessions and ENDED sessions should NOT take this path. Those
	// states may retain FilesTouched from older work, and treating that alone as
	// fresh content would incorrectly condense old sessions into unrelated commits.
	if hasUncommittedFiles && state.Phase == session.PhaseIdle && isRecentInteraction(state.LastInteractionTime) {
		logging.Debug(
			logCtx, "sessionHasNewContent: no staged files, returning true due to recent idle carry-forward files",
			slog.String("session_id", state.SessionID),
			slog.Bool("has_transcript_growth", hasTranscriptGrowth),
			slog.Bool("has_uncommitted_files", hasUncommittedFiles),
			slog.String("phase", string(state.Phase)),
		)
		return true, nil
	}

	// No staged files and no carry-forward files: transcript growth is the only
	// remaining signal that there is new session content to condense.
	logging.Debug(
		logCtx, "sessionHasNewContent: no staged files, returning transcript growth",
		slog.String("session_id", state.SessionID),
		slog.Bool("has_transcript_growth", hasTranscriptGrowth),
		slog.Bool("has_uncommitted_files", hasUncommittedFiles),
	)
	return hasTranscriptGrowth, nil
}

// sessionHasNewContentFromLiveTranscript checks if a session has new content
// by examining the live transcript file. This is used when no shadow branch exists
// (i.e., no Stop has happened yet) but the agent may have done work.
//
// Returns true if:
//  1. The transcript has grown since the last condensation, AND
//  2. The new transcript portion contains file modifications, AND
//  3. At least one modified file overlaps with the currently staged files
//
// The overlap check ensures we don't add checkpoint trailers to commits that are
// unrelated to the agent's recent changes.
//
// stagedFiles is the pre-computed list of staged files from the caller.
//
// This handles the scenario where the agent commits mid-session before Stop.
func (s *ManualCommitStrategy) sessionHasNewContentFromLiveTranscript(ctx context.Context, state *SessionState, stagedFiles []string) (bool, error) {
	logCtx := logging.WithComponent(ctx, "checkpoint")

	if !s.hasNewTranscriptWork(ctx, state) {
		return false, nil
	}

	// Prefer hook-populated files. If empty, extract from transcript directly —
	// hasNewTranscriptWork already called PrepareTranscript, so we bypass
	// resolveFilesTouched (which would prepare again) and extract directly.
	modifiedFiles := state.FilesTouched
	if len(modifiedFiles) == 0 {
		modifiedFiles = s.extractModifiedFilesFromLiveTranscript(ctx, state, state.CheckpointTranscriptStart)
	}
	if len(modifiedFiles) == 0 {
		return false, nil
	}

	logging.Debug(
		logCtx, "live transcript check: found file modifications",
		slog.String("session_id", state.SessionID),
		slog.Int("modified_files", len(modifiedFiles)),
	)

	logging.Debug(
		logCtx, "live transcript check: comparing staged vs modified",
		slog.String("session_id", state.SessionID),
		slog.Int("staged_files", len(stagedFiles)),
		slog.Int("modified_files", len(modifiedFiles)),
	)

	if !hasOverlappingFiles(stagedFiles, modifiedFiles) {
		logging.Debug(
			logCtx, "live transcript check: no overlap between staged and modified files",
			slog.String("session_id", state.SessionID),
		)
		return false, nil // No overlap - staged files are unrelated to agent's work
	}

	return true, nil
}

// resolveFilesTouched returns the file list for a session.
// Prefers hook-populated state.FilesTouched, falls back to transcript extraction.
// All call sites that need "what files did the agent touch?" should use this.
//
// Handles PrepareTranscript internally before falling back to extraction,
// so callers don't need to prepare the transcript first.
func (s *ManualCommitStrategy) resolveFilesTouched(ctx context.Context, state *SessionState) []string {
	if len(state.FilesTouched) > 0 {
		result := make([]string, len(state.FilesTouched))
		copy(result, state.FilesTouched)
		return result
	}

	// Prepare transcript before extraction (e.g., OpenCode `opencode export`).
	prepareTranscriptForState(ctx, state)

	return s.extractModifiedFilesFromLiveTranscript(ctx, state, state.CheckpointTranscriptStart)
}

// hasNewTranscriptWork checks if the agent has done work since the last condensation.
// Uses agent-delegated GetTranscriptPosition() — does NOT do file extraction.
// All call sites that need "has the agent done new work?" should use this.
//
// Returns false if: no transcript path, unknown agent type, agent doesn't implement
// TranscriptAnalyzer, or GetTranscriptPosition fails. This is intentional fail-safe
// behavior: callers treat false as "no new work detected", which conservatively
// skips condensation on errors.
func (s *ManualCommitStrategy) hasNewTranscriptWork(ctx context.Context, state *SessionState) bool {
	logCtx := logging.WithComponent(ctx, "checkpoint")

	if state.TranscriptPath == "" || state.AgentType == "" {
		return false
	}

	// Re-resolve transcript path — handles agents that relocate transcripts mid-session.
	if _, resolveErr := resolveTranscriptPath(state); resolveErr != nil {
		logging.Debug(
			logCtx, "hasNewTranscriptWork: transcript path resolution failed",
			slog.String("session_id", state.SessionID),
			slog.Any("error", resolveErr),
		)
		return false
	}

	ag, err := agent.GetByAgentType(state.AgentType)
	if err != nil {
		return false
	}

	// Ensure transcript file is up-to-date (OpenCode creates/refreshes it via `opencode export`).
	// Only wait for flush when the session is active — for idle/ended sessions the
	// transcript is already fully flushed (the Stop hook completed the flush).
	if state.Phase.IsActive() {
		if preparer, ok := agent.AsTranscriptPreparer(ag); ok {
			if prepErr := preparer.PrepareTranscript(ctx, state.TranscriptPath); prepErr != nil {
				logging.Debug(
					logCtx, "prepare transcript failed",
					slog.String("session_id", state.SessionID),
					slog.String("agent_type", string(state.AgentType)),
					slog.String("transcript_path", state.TranscriptPath),
					slog.Any("error", prepErr),
				)
			}
		}
	}
	analyzer, ok := agent.AsTranscriptAnalyzer(ag)
	if !ok {
		return false
	}

	currentPos, err := analyzer.GetTranscriptPosition(state.TranscriptPath)
	if err != nil {
		logging.Debug(
			logCtx, "hasNewTranscriptWork: GetTranscriptPosition failed",
			slog.String("session_id", state.SessionID),
			slog.String("transcript_path", state.TranscriptPath),
			slog.Any("error", err),
		)
		return false
	}

	if currentPos <= state.CheckpointTranscriptStart {
		logging.Debug(
			logCtx, "hasNewTranscriptWork: no new content",
			slog.String("session_id", state.SessionID),
			slog.Int("current_pos", currentPos),
			slog.Int("start_offset", state.CheckpointTranscriptStart),
		)
		return false
	}

	return true
}

// extractModifiedFilesFromLiveTranscript extracts modified files from the live transcript
// (including subagent transcripts) starting from the given offset, and normalizes them
// to repo-relative paths. Returns the normalized file list.
//
// Callers must ensure the transcript is prepared (e.g., via prepareTranscriptForState
// or hasNewTranscriptWork) before calling this function.
func (s *ManualCommitStrategy) extractModifiedFilesFromLiveTranscript(ctx context.Context, state *SessionState, offset int) []string {
	logCtx := logging.WithComponent(ctx, "checkpoint")

	if state.TranscriptPath == "" || state.AgentType == "" {
		return nil
	}

	// Re-resolve transcript path — handles agents that relocate transcripts mid-session.
	if _, resolveErr := resolveTranscriptPath(state); resolveErr != nil {
		logging.Debug(
			logCtx, "extractModifiedFilesFromLiveTranscript: transcript path resolution failed",
			slog.String("session_id", state.SessionID),
			slog.Any("error", resolveErr),
		)
		return nil
	}

	ag, err := agent.GetByAgentType(state.AgentType)
	if err != nil {
		return nil
	}

	analyzer, ok := agent.AsTranscriptAnalyzer(ag)
	if !ok {
		return nil
	}

	var modifiedFiles []string

	// Prefer SubagentAwareExtractor for agents that support it, to include
	// subagent transcript files in a single pass. Fall back to basic extraction.
	if subagentExtractor, subOk := agent.AsSubagentAwareExtractor(ag); subOk {
		subagentsDir := filepath.Join(filepath.Dir(state.TranscriptPath), state.SessionID, "subagents")
		transcriptData, readErr := os.ReadFile(state.TranscriptPath)
		if readErr != nil {
			logging.Debug(
				logCtx, "extractModifiedFilesFromLiveTranscript: failed to read transcript",
				slog.String("session_id", state.SessionID),
				slog.String("error", readErr.Error()),
			)
		} else {
			allFiles, extractErr := subagentExtractor.ExtractAllModifiedFiles(transcriptData, offset, subagentsDir)
			if extractErr != nil {
				logging.Debug(
					logCtx, "extractModifiedFilesFromLiveTranscript: extraction failed",
					slog.String("session_id", state.SessionID),
					slog.String("error", extractErr.Error()),
				)
			} else {
				modifiedFiles = allFiles
			}
		}
	} else {
		files, _, err := analyzer.ExtractModifiedFilesFromOffset(state.TranscriptPath, offset)
		if err != nil {
			logging.Debug(
				logCtx, "extractModifiedFilesFromLiveTranscript: main transcript extraction failed",
				slog.String("transcript_path", state.TranscriptPath),
				slog.Any("error", err),
			)
		} else {
			modifiedFiles = files
		}
	}

	if len(modifiedFiles) == 0 {
		return nil
	}

	// Normalize to repo-relative paths.
	// Transcript tool_use entries contain absolute paths (e.g., /Users/alex/project/src/main.go)
	// but getStagedFiles/committedFiles use repo-relative paths (e.g., src/main.go).
	basePath := state.WorktreePath
	if basePath == "" {
		if wp, wpErr := paths.WorktreeRoot(ctx); wpErr == nil {
			basePath = wp
		}
	}
	if basePath != "" {
		normalized := make([]string, 0, len(modifiedFiles))
		for _, f := range modifiedFiles {
			if rel := paths.ToRelativePath(f, basePath); rel != "" {
				normalized = append(normalized, filepath.ToSlash(rel))
			} else if len(f) > 0 && !filepath.IsAbs(f) && f[0] != '/' {
				// Already relative — keep as-is
				normalized = append(normalized, filepath.ToSlash(f))
			}
			// else: absolute path outside repo — skip. These can't match
			// committed file paths (which are repo-relative) and would
			// create phantom carry-forward branches.
		}
		modifiedFiles = normalized
	}

	return modifiedFiles
}

// warnIfAttributionDiverged prints at most one stderr warning per call and
// marks every divergent session as notified so subsequent invocations stay
// silent until the next successful condensation (or reconcile) realigns
// attribution and clears the flag via State.RealignAttributionBase.
//
// Divergence arises when the migrate path advances BaseCommit to a new HEAD
// but intentionally leaves AttributionBaseCommit pinned (e.g., after a pull
// or git reset to an unrelated commit). Writing to stderrWriter surfaces the
// message in the user's terminal during prepare-commit-msg, not the agent's
// transcript — stderr from the hook is TTY-bound to the invoking process.
func (s *ManualCommitStrategy) warnIfAttributionDiverged(ctx context.Context, sessions []*SessionState) {
	logCtx := logging.WithComponent(ctx, "checkpoint")
	printed := false
	for _, sess := range sessions {
		if sess.AttributionBaseCommit == "" ||
			sess.AttributionBaseCommit == sess.BaseCommit ||
			sess.DivergenceNoticeShown {
			continue
		}
		if !printed {
			fmt.Fprintln(stderrWriter, "trace: session attribution diverged after recent history movement; figures may be off until next checkpoint")
			printed = true
		}
		sess.DivergenceNoticeShown = true
		if err := s.saveSessionState(ctx, sess); err != nil {
			logging.Warn(logCtx, "failed to save divergence notice flag",
				slog.String("session_id", sess.SessionID),
				slog.String("error", err.Error()))
		}
	}
}

// tryAgentCommitFastPath skips content detection for mid-turn agent commits.
// Returns true if the fast path was taken (trailer added or attempt made),
// false if the caller should continue with normal content detection.
//
// The fast path activates when an ACTIVE session exists and either:
//   - No TTY is available (agent subprocess, CI), or
//   - commit_linking="always" (user opted into auto-linking — needed because
//     some agents like Gemini subagents commit mid-turn from processes that
//     have /dev/tty but can't respond to prompts, and content detection fails
//     since the shadow branch doesn't exist yet).
func (s *ManualCommitStrategy) tryAgentCommitFastPath(ctx context.Context, commitMsgFile string, sessions []*SessionState, source string) bool {
	noTTY := !interactive.CanPromptInteractively()
	skipContentDetection := noTTY
	if !skipContentDetection {
		if stngs, err := settings.Load(ctx); err == nil {
			skipContentDetection = stngs.GetCommitLinking() == settings.CommitLinkingAlways
		}
	}
	if !skipContentDetection {
		return false
	}
	logCtx := logging.WithComponent(ctx, "checkpoint")
	activeSessions := 0
	emptyActiveSessions := 0
	for _, state := range sessions {
		if !state.Phase.IsActive() {
			continue
		}
		activeSessions++
		// Skip sessions that have no condensable content: no transcript path,
		// no tracked files, and no shadow branch data (StepCount == 0). These
		// would produce a Skipped result in CondenseSession, leaving the
		// Trace-Checkpoint trailer pointing to nothing on the metadata branch.
		// NOTE: conservative approximation of the skip gate in CondenseSession
		// (which checks extracted data, not raw state). Keep aligned.
		if state.TranscriptPath == "" && len(state.FilesTouched) == 0 && state.StepCount == 0 {
			emptyActiveSessions++
			logging.Debug(
				logCtx, "prepare-commit-msg: fast path skipping empty session",
				slog.String("session_id", state.SessionID),
				slog.String("agent_type", string(state.AgentType)),
			)
			continue
		}
		_ = s.addTrailerForAgentCommit(logCtx, commitMsgFile, state, source) //nolint:errcheck // always returns nil; kept for signature stability
		return true
	}
	// Log why fast path didn't fire — collect session phases for diagnostics.
	phases := make([]string, 0, len(sessions))
	for _, state := range sessions {
		phases = append(phases, string(state.Phase))
	}
	message := "prepare-commit-msg: fast path found no ACTIVE sessions"
	if activeSessions > 0 && emptyActiveSessions == activeSessions {
		message = "prepare-commit-msg: fast path skipped all ACTIVE sessions as empty"
	}
	logging.Debug(
		logCtx, message,
		slog.Bool("no_tty", noTTY),
		slog.Int("sessions", len(sessions)),
		slog.Int("active_sessions", activeSessions),
		slog.Int("empty_active_sessions", emptyActiveSessions),
		slog.Any("session_phases", phases),
	)
	return false
}

// addTrailerForAgentCommit handles the fast path when an agent is committing
// (ACTIVE session + no TTY). Generates a checkpoint ID and adds the trailer
// directly, bypassing content detection and interactive prompts.
func (s *ManualCommitStrategy) addTrailerForAgentCommit(logCtx context.Context, commitMsgFile string, state *SessionState, source string) error { //nolint:unparam // kept for signature stability
	cpID, err := id.Generate()
	if err != nil {
		return nil //nolint:nilerr // Hook must be silent on failure
	}

	content, err := os.ReadFile(commitMsgFile) //nolint:gosec // commitMsgFile is provided by git hook
	if err != nil {
		return nil //nolint:nilerr // Hook must be silent on failure
	}

	message := string(content)

	// Don't add if trailer already exists
	if _, found := trailers.ParseCheckpoint(message); found {
		return nil
	}

	message = addCheckpointTrailer(message, cpID)

	logging.Info(
		logCtx, "prepare-commit-msg: agent commit trailer added",
		slog.String("strategy", "manual-commit"),
		slog.String("source", source),
		slog.String("checkpoint_id", cpID.String()),
		slog.String("session_id", state.SessionID),
	)

	if err := os.WriteFile(commitMsgFile, []byte(message), 0o600); err != nil { //nolint:gosec // path from git hook arg
		return nil //nolint:nilerr // Hook must be silent on failure
	}
	return nil
}

// addCheckpointTrailer adds the Trace-Checkpoint trailer to a commit message.
// Delegates to trailers.AppendCheckpointTrailer for trailer-aware formatting.
func addCheckpointTrailer(message string, checkpointID id.CheckpointID) string {
	return trailers.AppendCheckpointTrailer(message, checkpointID.String())
}

// addCheckpointTrailerWithComment adds the Trace-Checkpoint trailer with an explanatory comment.
// The trailer is placed above the git comment block but below the user's message area,
// with a comment explaining that the user can remove it if they don't want to link the commit
// to the agent session. If prompt is non-empty, it's shown as context.
func addCheckpointTrailerWithComment(message string, checkpointID id.CheckpointID, agentName, prompt string) string {
	trailer := trailers.CheckpointTrailerKey + ": " + checkpointID.String()
	commentLines := []string{
		"# Remove the Trace-Checkpoint trailer above if you don't want to link this commit to " + agentName + " session context.",
	}
	if prompt != "" {
		commentLines = append(commentLines, "# Last Prompt: "+prompt)
	}
	commentLines = append(commentLines, "# The trailer will be added to your next commit based on this branch.")
	comment := strings.Join(commentLines, "\n")

	lines := strings.Split(message, "\n")

	// Find where the git comment block starts (first # line)
	commentStart := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "#") {
			commentStart = i
			break
		}
	}

	if commentStart == -1 {
		// No git comments, append trailer at the end
		return strings.TrimRight(message, "\n") + "\n\n" + trailer + "\n" + comment + "\n"
	}

	// Split into user content and git comments
	userContent := strings.Join(lines[:commentStart], "\n")
	gitComments := strings.Join(lines[commentStart:], "\n")

	// Build result: user content, blank line, trailer, comment, blank line, git comments
	userContent = strings.TrimRight(userContent, "\n")
	if userContent == "" {
		// No user content yet - leave space for them to type, then trailer
		// Two newlines: first for user's message line, second for blank separator
		return "\n\n" + trailer + "\n" + comment + "\n\n" + gitComments
	}
	return userContent + "\n\n" + trailer + "\n" + comment + "\n\n" + gitComments
}

func applyProvenanceEnvToState(state *SessionState) {
	if os.Getenv(provenance.ReviewSession) != "" {
		state.Kind = session.KindAgentReview
		if skills, err := review.DecodeSkills(os.Getenv(provenance.ReviewSkills)); err == nil {
			state.ReviewSkills = skills
		}
		if prompt := strings.TrimSpace(os.Getenv(provenance.ReviewPrompt)); prompt != "" {
			state.ReviewPrompt = prompt
		}
	}

	if os.Getenv(provenance.InvestigateSession) != "" {
		state.Kind = session.KindAgentInvestigate
		if runID := strings.TrimSpace(os.Getenv(provenance.InvestigateRunID)); provenance.IsValidRunID(runID) {
			state.InvestigateRunID = runID
		}
		if topic := strings.TrimSpace(os.Getenv(provenance.InvestigateTopic)); topic != "" {
			state.InvestigateTopic = topic
		}
	}
}

// InitializeSession creates session state for a new session or updates an existing one.
// This implements the optional SessionInitializer interface.
// Called during UserPromptSubmit to allow git hooks to detect active sessions.
//
// If the session already exists and HEAD has moved (e.g., user committed), updates
// BaseCommit to the new HEAD so future checkpoints go to the correct shadow branch.
//
// If there's an existing shadow branch with commits from a different session ID,
// returns a SessionIDConflictError to prevent orphaning existing session work.
//
// agentType is the human-readable name of the agent (e.g., "Claude Code").
// transcriptPath is the path to the live transcript file (for mid-session commit detection).
// userPrompt is the user's prompt text (stored truncated as LastPrompt for display).
// model is the LLM model identifier (e.g., "claude-sonnet-4-20250514"); empty if unknown.
func (s *ManualCommitStrategy) InitializeSession(ctx context.Context, sessionID string, agentType types.AgentType, transcriptPath string, userPrompt string, model string) error {
	repo, err := OpenRepository(ctx)
	if err != nil {
		return fmt.Errorf("failed to open git repository: %w", err)
	}

	// Resolve which agent actually owns this session.
	resolvedAgentType := resolveSessionAgentType(ctx, sessionID, agentType, transcriptPath)

	// Check if session already exists
	state, err := s.loadSessionState(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to check session state: %w", err)
	}

	if state != nil && state.BaseCommit != "" {
		// Session is fully initialized — apply phase transition for TurnStart.
		if transErr := TransitionAndLog(ctx, state, session.EventTurnStart, session.TransitionContext{}, session.NoOpActionHandler{}); transErr != nil {
			logging.Warn(logging.WithComponent(ctx, "hooks"), "turn start transition failed",
				slog.String("session_id", sessionID),
				slog.String("error", transErr.Error()))
		}

		// Generate a new TurnID for each turn (correlates carry-forward checkpoints)
		turnID, err := id.Generate()
		if err != nil {
			return fmt.Errorf("failed to generate turn ID: %w", err)
		}
		state.TurnID = turnID.String()

		// Update AgentType when it isn't set yet, or when the transcript path
		// proves we're a different agent than the one stored.
		if state.AgentType == "" && resolvedAgentType != "" {
			state.AgentType = resolvedAgentType
		} else if corrected, changed := correctSessionAgentType(ctx, state.AgentType, transcriptPath); changed {
			logging.Info(logging.WithComponent(ctx, "hooks"), "corrected session agent type from transcript path",
				slog.String("session_id", sessionID),
				slog.String("from", string(state.AgentType)),
				slog.String("to", string(corrected)),
				slog.String("transcript_path", transcriptPath))
			state.AgentType = corrected
		}

		// Update ModelName if provided (model can change between turns)
		if model != "" {
			state.ModelName = model
		}

		applyProvenanceEnvToState(state)

		// Update LastPrompt on every turn so condensation always has the current prompt
		if userPrompt != "" {
			state.LastPrompt = truncatePromptForStorage(userPrompt)
		}

		// Update transcript path if provided (may change on session resume)
		if transcriptPath != "" && state.TranscriptPath != transcriptPath {
			state.TranscriptPath = transcriptPath
		}

		// ORDERING: attribution runs BEFORE migrate to use the pre-migration
		// BaseCommit as the base tree (preserving correct agent-line counts when
		// HEAD moved between turns via pull/rebase). Migrate runs BEFORE the
		// LastCheckpointID clear so the reconcile guard can read the checkpoint ID.
		//
		// Sequence: attribution → migrate → clear
		//
		// 1. Attribution uses state.BaseCommit to locate the shadow branch and
		//    base tree. Running it before migrate ensures it diffs against the
		//    original base, not the post-migration HEAD.
		// 2. Migrate/reconcile reads state.LastCheckpointID — clearing it first
		//    would prevent the reconcile path from ever firing at turn start.

		// Calculate attribution at prompt start (BEFORE agent makes any changes)
		// This captures user edits since the last checkpoint (or base commit for first prompt).
		// IMPORTANT: Always calculate attribution, even for the first checkpoint, to capture
		// user edits made before the first prompt. The inner CalculatePromptAttribution handles
		// nil lastCheckpointTree by falling back to baseTree.
		promptAttr := s.calculatePromptAttributionAtStart(ctx, repo, state)
		state.PendingPromptAttribution = &promptAttr

		// Check if HEAD has moved (user pulled/rebased or committed)
		// migrateShadowBranchIfNeeded handles renaming the shadow branch and updating state.BaseCommit
		_, reconciled, err := s.migrateShadowBranchIfNeeded(ctx, repo, state)
		if err != nil {
			return fmt.Errorf("failed to check/migrate shadow branch: %w", err)
		}
		if reconciled {
			// Reconcile advanced BaseCommit + AttributionBaseCommit to HEAD (the
			// known checkpoint we reset to). The attribution just computed is
			// against the stale pre-reset base and would count discarded-history
			// edits as churn. Recompute against the new base so the next
			// checkpoint sees accurate user-delta.
			recomputed := s.calculatePromptAttributionAtStart(ctx, repo, state)
			state.PendingPromptAttribution = &recomputed
		}

		// Clear checkpoint IDs on every new prompt.
		// LastCheckpointID is set during PostCommit, cleared at new prompt.
		// TurnCheckpointIDs tracks mid-turn checkpoints for stop-time finalization.
		state.LastCheckpointID = ""
		state.TurnCheckpointIDs = nil

		if err := s.saveSessionState(ctx, state); err != nil {
			return fmt.Errorf("failed to update session state: %w", err)
		}
		return nil
	}
	// If state exists but BaseCommit is empty, it's a partial state from concurrent warning
	// Continue below to properly initialize it

	// Initialize new session
	state, err = s.initializeSession(ctx, repo, sessionID, resolvedAgentType, transcriptPath, userPrompt, model)
	if err != nil {
		return fmt.Errorf("failed to initialize session: %w", err)
	}

	applyProvenanceEnvToState(state)

	// Apply phase transition: new session starts as ACTIVE.
	if transErr := TransitionAndLog(ctx, state, session.EventTurnStart, session.TransitionContext{}, session.NoOpActionHandler{}); transErr != nil {
		logging.Warn(logging.WithComponent(ctx, "hooks"), "turn start transition failed",
			slog.String("session_id", sessionID),
			slog.String("error", transErr.Error()))
	}

	// Calculate attribution for pre-prompt edits
	// This captures any user edits made before the first prompt
	promptAttr := s.calculatePromptAttributionAtStart(ctx, repo, state)
	state.PendingPromptAttribution = &promptAttr
	if err = s.saveSessionState(ctx, state); err != nil {
		return fmt.Errorf("failed to save attribution: %w", err)
	}

	logging.Info(logging.WithComponent(ctx, "hooks"), "initialized shadow session",
		slog.String("session_id", sessionID))
	return nil
}
