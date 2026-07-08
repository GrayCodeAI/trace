package strategy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/session"
	"github.com/GrayCodeAI/trace/cli/trailers"
	"github.com/GrayCodeAI/trace/perf"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

func warnStaleEndedSessionsTo(ctx context.Context, count int, w io.Writer) {
	commonDir, err := GetGitCommonDir(ctx)
	if err != nil {
		return // fail-open
	}
	warnDir := filepath.Join(commonDir, session.SessionStateDirName)
	warnFile := filepath.Join(warnDir, staleEndedSessionWarnFile)
	if info, statErr := os.Stat(warnFile); statErr == nil {
		if time.Since(info.ModTime()) < staleEndedSessionWarnInterval {
			return // rate-limited
		}
	}
	//nolint:errcheck,gosec // G104: Best-effort warning — fail-open if file ops fail
	// #nosec G104 -- best-effort directory creation; fails open if file ops fail
	os.MkdirAll(warnDir, 0o750)
	//nolint:errcheck,gosec // G104: Best-effort sentinel file write
	// #nosec G104 -- best-effort sentinel file write; failure is non-fatal (rate-limit warning skipped)
	os.WriteFile(warnFile, []byte{}, 0o600)
	fmt.Fprintf(
		w,
		"\ntrace: %d ended session(s) are accumulating and slowing down commits.\n"+
			"Run 'trace doctor' to condense them and restore commit performance.\n\n",
		count,
	)
}

// activeSessionInteractionThreshold is the maximum age of LastInteractionTime
// for an ACTIVE session to be considered genuinely active. 24h is generous
// because LastInteractionTime only updates at TurnStart, not per-tool-call.
const activeSessionInteractionThreshold = 24 * time.Hour

// isRecentInteraction returns true if lastInteraction is non-nil and within
// activeSessionInteractionThreshold of now.
func isRecentInteraction(lastInteraction *time.Time) bool {
	return lastInteraction != nil && time.Since(*lastInteraction) < activeSessionInteractionThreshold
}

func (h *postCommitActionHandler) HandleDiscardIfNoFiles(state *session.State) error {
	if len(state.FilesTouched) == 0 {
		logging.Debug(
			logging.WithComponent(h.ctx, "checkpoint"), "post-commit: skipping empty ended session (no files to condense)",
			slog.String("session_id", state.SessionID),
		)
	}
	h.s.updateBaseCommitIfChanged(h.ctx, state, h.newHead)
	return nil
}

func (h *postCommitActionHandler) HandleWarnStaleSession(_ *session.State) error {
	// Not produced by EventGitCommit; no-op for exhaustiveness.
	return nil
}

// During rebase/cherry-pick/revert operations, phase transitions are skipped entirely.
//

func (s *ManualCommitStrategy) PostCommit(ctx context.Context) error { //nolint:unparam // error return is part of the hook contract; callers check it
	logCtx := logging.WithComponent(ctx, "checkpoint")

	_, openRepoSpan := perf.Start(ctx, "open_repository_and_head")
	repo, err := OpenRepository(ctx)
	if err != nil {
		openRepoSpan.RecordError(err)
		openRepoSpan.End()
		return nil
	}

	// Get HEAD commit to check for trailer
	head, err := repo.Head()
	if err != nil {
		openRepoSpan.RecordError(err)
		openRepoSpan.End()
		return nil
	}

	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		openRepoSpan.RecordError(err)
		openRepoSpan.End()
		return nil
	}

	// Check if commit has checkpoint trailer (ParseCheckpoint validates format)
	checkpointID, found := trailers.ParseCheckpoint(commit.Message)
	openRepoSpan.End()

	if !found {
		// No trailer — user removed it or it was never added (mid-turn commit).
		// Still update BaseCommit for active sessions so future commits can match.
		s.postCommitUpdateBaseCommitOnly(ctx, head)
		return nil
	}

	_, findSessionsSpan := perf.Start(ctx, "find_sessions_for_worktree")
	worktreePath, err := paths.WorktreeRoot(ctx)
	if err != nil {
		findSessionsSpan.RecordError(err)
		findSessionsSpan.End()
		return nil
	}

	// Find all active sessions for this worktree
	sessions, err := s.findSessionsForWorktree(ctx, worktreePath)
	findSessionsSpan.RecordError(err)
	findSessionsSpan.End()

	if err != nil || len(sessions) == 0 {
		logging.Warn(
			logCtx, "post-commit: no active sessions despite trailer",
			slog.String("strategy", "manual-commit"),
			slog.String("checkpoint_id", checkpointID.String()),
		)
		return nil //nolint:nilerr // Intentional: hooks must be silent on failure
	}

	// Build transition context
	isRebase := isGitSequenceOperation(ctx)
	transitionCtx := session.TransitionContext{
		IsRebaseInProgress: isRebase,
	}

	if isRebase {
		logging.Debug(
			logCtx, "post-commit: rebase/sequence in progress, skipping phase transitions",
			slog.String("strategy", "manual-commit"),
		)
	}

	// Track shadow branch names and whether they can be deleted
	shadowBranchesToDelete := make(map[string]struct{})
	// Track active sessions that were NOT condensed — their shadow branches must be preserved
	uncondensedActiveOnBranch := make(map[string]bool)

	newHead := head.Hash().String()

	// Pre-resolve HEAD tree and parent tree once for the trace PostCommit.
	// These are immutable within this hook invocation and used by multiple
	// per-session functions (filesOverlapWithContent, filesWithRemainingAgentChanges,
	// calculateSessionAttributions).
	_, resolveTreesSpan := perf.Start(ctx, "resolve_commit_trees")
	var headTree *object.Tree
	if t, err := commit.Tree(); err == nil {
		headTree = t
	}
	var parentTree *object.Tree
	if commit.NumParents() > 0 {
		if parent, err := commit.Parent(0); err == nil {
			if t, err := parent.Tree(); err == nil {
				parentTree = t
			}
		}
	}

	committedFileSet := filesChangedInCommit(ctx, worktreePath, commit, headTree, parentTree)
	resolveTreesSpan.End()

	// Compute union of all sessions' FilesTouched for cross-session attribution,
	// and count sessions whose tracked files overlap with committed files.
	allAgentFiles := make(map[string]struct{})
	sessionsWithCommittedFiles := 0
	for _, state := range sessions {
		if state.FullyCondensed && state.Phase == session.PhaseEnded {
			continue
		}
		for _, f := range state.FilesTouched {
			allAgentFiles[f] = struct{}{}
			if _, ok := committedFileSet[f]; ok {
				sessionsWithCommittedFiles++
				break // count each session at most once
			}
		}
	}

	loopCtx, processSessionsLoop := perf.StartLoop(ctx, "process_sessions")
	for _, state := range sessions {
		// Skip fully-condensed ended sessions — no work remains.
		// These sessions only persist for LastCheckpointID (amend trailer reuse).
		if state.FullyCondensed && state.Phase == session.PhaseEnded {
			continue
		}
		iterCtx, iterSpan := processSessionsLoop.Iteration(loopCtx)
		s.postCommitProcessSession(iterCtx, repo, state, &transitionCtx, checkpointID,
			head, commit, newHead, worktreePath, headTree, parentTree,
			committedFileSet, shadowBranchesToDelete, uncondensedActiveOnBranch, allAgentFiles,
			sessionsWithCommittedFiles)
		iterSpan.End()
	}
	processSessionsLoop.End()

	if err := s.updateCombinedAttributionForCheckpoint(ctx, repo, checkpointID, headTree, parentTree, worktreePath); err != nil {
		logging.Warn(logCtx, "failed to update combined checkpoint attribution",
			slog.String("checkpoint_id", checkpointID.String()),
			slog.String("error", err.Error()))
	}

	// Clean up shadow branches — only delete when ALL sessions on the branch are non-active
	// or were condensed during this PostCommit.
	_, cleanupBranchesSpan := perf.Start(ctx, "cleanup_shadow_branches")
	for shadowBranchName := range shadowBranchesToDelete {
		if uncondensedActiveOnBranch[shadowBranchName] {
			logging.Debug(
				logCtx, "post-commit: preserving shadow branch (active session exists)",
				slog.String("shadow_branch", shadowBranchName),
			)
			continue
		}
		if err := deleteShadowBranch(ctx, repo, shadowBranchName); err != nil {
			logging.Warn(logCtx, "failed to clean up shadow branch",
				slog.String("shadow_branch", shadowBranchName),
				slog.String("error", err.Error()))
		} else {
			logging.Info(
				logCtx, "shadow branch deleted",
				slog.String("strategy", "manual-commit"),
				slog.String("shadow_branch", shadowBranchName),
			)
		}
	}
	cleanupBranchesSpan.End()

	if stale := countWarnableStaleEndedSessions(repo, sessions); stale >= staleEndedSessionWarnThreshold {
		warnStaleEndedSessions(ctx, stale)
	}

	return nil
}

// updateCombinedAttributionForCheckpoint computes holistic attribution across all sessions.
// Instead of summing per-session numbers (which inflates totals because each session
// independently counts the full commit), this diffs parent→HEAD once and classifies
// lines as agent or human based on the union of all sessions' files_touched.
func (s *ManualCommitStrategy) updateCombinedAttributionForCheckpoint(
	ctx context.Context,
	repo *git.Repository,
	checkpointID id.CheckpointID,
	headTree, parentTree *object.Tree,
	repoDir string,
) error {
	logCtx := logging.WithComponent(ctx, "attribution")
	store := checkpoint.NewGitStore(repo)

	summary, err := store.ReadCommitted(ctx, checkpointID)
	if err != nil {
		return fmt.Errorf("reading checkpoint summary: %w", err)
	}
	if summary == nil || len(summary.Sessions) <= 1 {
		return nil
	}

	// Collect union of files_touched from sessions that had real checkpoints (SaveStep ran).
	// Sessions with checkpoints_count == 0 (e.g., commit-only sessions) use a fallback that
	// includes ALL committed files, which would incorrectly classify human-created files as agent work.
	agentFiles := make(map[string]struct{})
	for i := range len(summary.Sessions) {
		metadata, readErr := store.ReadSessionMetadata(ctx, checkpointID, i)
		if readErr != nil || metadata == nil {
			continue
		}
		if metadata.CheckpointsCount == 0 {
			continue // Skip sessions that used the filesTouched fallback
		}
		for _, f := range metadata.FilesTouched {
			agentFiles[f] = struct{}{}
		}
	}

	if len(agentFiles) == 0 {
		return nil
	}

	// Get all files changed in this commit (parent → HEAD)
	allChangedFiles, err := getAllChangedFiles(ctx, parentTree, headTree, repoDir, "", "")
	if err != nil {
		logging.Warn(logCtx, "combined attribution: failed to enumerate changed files",
			slog.String("error", err.Error()))
		return nil
	}

	// Classify each changed file as agent or human and count lines
	var agentAdded, agentRemoved, humanAdded, humanRemoved int
	for _, filePath := range allChangedFiles {
		// Skip CLI/agent config metadata — not human or agent code work
		if strings.HasPrefix(filePath, ".trace/") || strings.HasPrefix(filePath, paths.TraceMetadataDir+"/") ||
			strings.HasPrefix(filePath, ".claude/") {
			continue
		}

		parentContent := getFileContent(parentTree, filePath)
		headContent := getFileContent(headTree, filePath)
		_, added, removed := diffLines(parentContent, headContent)

		if _, isAgent := agentFiles[filePath]; isAgent {
			agentAdded += added
			agentRemoved += removed
		} else {
			humanAdded += added
			humanRemoved += removed
		}
	}

	totalLinesChanged := agentAdded + agentRemoved + humanAdded + humanRemoved
	totalCommitted := agentAdded + humanAdded

	var agentPercentage float64
	if totalLinesChanged > 0 {
		agentPercentage = float64(agentAdded+agentRemoved) / float64(totalLinesChanged) * 100
	}

	combined := &checkpoint.InitialAttribution{
		CalculatedAt:      time.Now().UTC(),
		AgentLines:        agentAdded,
		AgentRemoved:      agentRemoved,
		HumanAdded:        humanAdded,
		HumanRemoved:      humanRemoved,
		TotalCommitted:    totalCommitted,
		TotalLinesChanged: totalLinesChanged,
		AgentPercentage:   agentPercentage,
		MetricVersion:     2,
	}

	logging.Info(
		logCtx, "combined attribution calculated",
		slog.String("checkpoint_id", checkpointID.String()),
		slog.Int("sessions", len(summary.Sessions)),
		slog.Int("agent_files", len(agentFiles)),
		slog.Int("agent_lines", agentAdded),
		slog.Int("human_added", humanAdded),
		slog.Float64("agent_percentage", agentPercentage),
	)

	if err := store.UpdateCheckpointSummary(ctx, checkpointID, combined); err != nil {
		return fmt.Errorf("persisting combined attribution: %w", err)
	}

	return nil
}

// postCommitProcessSession handles a single session within the PostCommit loop.
// Pre-resolved git objects (headTree, parentTree) are shared across all sessions;
// per-session shadow ref/tree are resolved once here and threaded through sub-calls.
func (s *ManualCommitStrategy) postCommitProcessSession(
	ctx context.Context,
	repo *git.Repository,
	state *SessionState,
	transitionCtx *session.TransitionContext,
	checkpointID id.CheckpointID,
	head *plumbing.Reference,
	commit *object.Commit,
	newHead string,
	repoDir string,
	headTree, parentTree *object.Tree,
	committedFileSet map[string]struct{},
	shadowBranchesToDelete map[string]struct{},
	uncondensedActiveOnBranch map[string]bool,
	allAgentFiles map[string]struct{},
	sessionsWithCommittedFiles int,
) {
	logCtx := logging.WithComponent(ctx, "checkpoint")
	shadowBranchName := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)

	// Pre-resolve shadow branch ref and tree for this session.
	// These are read 4+ times across sessionHasNewContent, filesOverlapWithContent,
	// CondenseSession, filesWithRemainingAgentChanges, and calculateSessionAttributions.
	_, resolveShadowBranchSpan := perf.Start(ctx, "resolve_shadow_branch")
	var shadowRef *plumbing.Reference
	var shadowTree *object.Tree
	if ref, refErr := repo.Reference(plumbing.NewBranchReferenceName(shadowBranchName), true); refErr == nil {
		shadowRef = ref
		if sc, scErr := repo.CommitObject(ref.Hash()); scErr == nil {
			if st, stErr := sc.Tree(); stErr == nil {
				shadowTree = st
			}
		}
	}
	resolveShadowBranchSpan.End()

	// Check for new content (needed for TransitionContext and condensation).
	// Fail-open: if content check errors, assume new content exists so we
	// don't silently skip data that should have been condensed.
	//
	// For ACTIVE sessions: the commit has a checkpoint trailer (verified above),
	// meaning PrepareCommitMsg already determined this commit is session-related.
	// The trailer is only added when either:
	//   - No TTY (agent/subagent committing) — added unconditionally
	//   - TTY (human committing) — added after content detection confirmed agent work
	// In both cases, PrepareCommitMsg already validated this commit. We trust
	// that decision here. Transcript-based re-validation is unreliable because
	// subagent transcripts may not be available yet (subagent still running).
	_, checkContentSpan := perf.Start(ctx, "check_session_content")
	var hasNew bool
	if state.Phase.IsActive() {
		hasNew = true
	} else {
		var contentErr error
		hasNew, contentErr = s.sessionHasNewContent(ctx, repo, state, contentCheckOpts{shadowTree: shadowTree})
		if contentErr != nil {
			hasNew = true
			logging.Debug(
				logCtx, "post-commit: error checking session content, assuming new content",
				slog.String("session_id", state.SessionID),
				slog.String("error", contentErr.Error()),
			)
		}
	}
	transitionCtx.HasFilesTouched = len(state.FilesTouched) > 0

	// Save FilesTouched BEFORE TransitionAndLog — the handler's condensation
	// clears it, but we need the original list for carry-forward computation.
	// Only fall back to transcript extraction for ACTIVE sessions — IDLE/ENDED
	// sessions have FilesTouched already populated by SaveStep/mergeFilesTouched.
	var filesTouchedBefore []string
	if state.Phase.IsActive() {
		filesTouchedBefore = s.resolveFilesTouched(ctx, state)
	} else if len(state.FilesTouched) > 0 {
		filesTouchedBefore = make([]string, len(state.FilesTouched))
		copy(filesTouchedBefore, state.FilesTouched)
	}
	checkContentSpan.End()

	logging.Debug(
		logCtx, "post-commit: carry-forward prep",
		slog.String("session_id", state.SessionID),
		slog.Bool("is_active", state.Phase.IsActive()),
		slog.String("transcript_path", state.TranscriptPath),
		slog.Int("files_touched_before", len(filesTouchedBefore)),
		slog.Any("files", filesTouchedBefore),
	)

	// Run the state machine transition with handler for strategy-specific actions.
	_, transitionAndCondenseSpan := perf.Start(ctx, "transition_and_condense")
	handler := &postCommitActionHandler{
		s:                          s,
		ctx:                        ctx,
		repo:                       repo,
		checkpointID:               checkpointID,
		head:                       head,
		commit:                     commit,
		newHead:                    newHead,
		repoDir:                    repoDir,
		shadowBranchName:           shadowBranchName,
		shadowBranchesToDelete:     shadowBranchesToDelete,
		committedFileSet:           committedFileSet,
		hasNew:                     hasNew,
		filesTouchedBefore:         filesTouchedBefore,
		headTree:                   headTree,
		parentTree:                 parentTree,
		shadowRef:                  shadowRef,
		shadowTree:                 shadowTree,
		allAgentFiles:              allAgentFiles,
		sessionsWithCommittedFiles: sessionsWithCommittedFiles,
	}

	if err := TransitionAndLog(ctx, state, session.EventGitCommit, *transitionCtx, handler); err != nil {
		logging.Warn(logCtx, "post-commit action handler error",
			slog.String("error", err.Error()))
	}
	transitionAndCondenseSpan.End()

	// Record checkpoint ID for ACTIVE sessions so HandleTurnEnd can finalize
	// with full transcript. IDLE/ENDED sessions already have complete transcripts.
	// NOTE: This check runs AFTER TransitionAndLog updated the phase. It relies on
	// ACTIVE + GitCommit → ACTIVE (phase stays ACTIVE). If that state machine
	// transition ever changed, this guard would silently stop recording IDs.
	if handler.condensed && state.Phase.IsActive() {
		state.TurnCheckpointIDs = append(state.TurnCheckpointIDs, checkpointID.String())
	}

	// Carry forward remaining uncommitted files so the next commit gets its
	// own checkpoint ID. This applies to ALL phases — if a user splits their
	// commit across two `git commit` invocations, each gets a 1:1 checkpoint.
	// Uses content-aware comparison: if user did `git add -p` and committed
	// partial changes, the file still has remaining agent changes to carry forward.
	_, carryForwardSpan := perf.Start(ctx, "carry_forward_files")
	if handler.condensed {
		remainingFiles := filesWithRemainingAgentChanges(ctx, repo, shadowBranchName, commit, filesTouchedBefore, committedFileSet, overlapOpts{
			headTree:   headTree,
			shadowTree: shadowTree,
		})
		state.FilesTouched = remainingFiles
		logging.Debug(
			logCtx, "post-commit: carry-forward decision (content-aware)",
			slog.String("session_id", state.SessionID),
			slog.Int("files_touched_before", len(filesTouchedBefore)),
			slog.Int("committed_files", len(committedFileSet)),
			slog.Int("remaining_files", len(remainingFiles)),
			slog.Any("remaining", remainingFiles),
			slog.Any("committed_files", committedFileSet),
		)
		if len(remainingFiles) > 0 {
			s.carryForwardToNewShadowBranch(ctx, repo, state, remainingFiles)
		}

		// Clear filesystem prompt.txt only when ALL files are committed.
		// If carry-forward files remain, the prompt must persist so the next
		// condensation (triggered by the next commit) can read it.
		if len(state.FilesTouched) == 0 {
			clearFilesystemPrompt(ctx, state.SessionID)
		}
	}
	carryForwardSpan.End()

	// Mark ENDED sessions as fully condensed when there's nothing left to do.
	// Either we just condensed (no carry-forward remains) or there was never any
	// new content. PostCommit will skip these on future commits; they persist only
	// for LastCheckpointID (amend trailer restoration).
	if state.Phase == session.PhaseEnded && len(state.FilesTouched) == 0 && (handler.condensed || !hasNew) {
		state.FullyCondensed = true
	}

	// Save the updated state
	_, saveSessionStateSpan := perf.Start(ctx, "save_session_state")
	if err := s.saveSessionState(ctx, state); err != nil {
		logging.Warn(logCtx, "failed to update session state",
			slog.String("session_id", state.SessionID),
			slog.String("error", err.Error()))
	}
	saveSessionStateSpan.End()

	// Only preserve shadow branch for active sessions that were NOT condensed.
	// Condensed sessions already have their data on trace/checkpoints/v1.
	if state.Phase.IsActive() && !handler.condensed {
		uncondensedActiveOnBranch[shadowBranchName] = true
	}
}

// condenseAndUpdateState runs condensation for a session and updates state afterward.
// Returns true if condensation succeeded.
func (s *ManualCommitStrategy) condenseAndUpdateState(
	ctx context.Context,
	repo *git.Repository,
	checkpointID id.CheckpointID,
	state *SessionState,
	head *plumbing.Reference,
	shadowBranchName string,
	shadowBranchesToDelete map[string]struct{},
	committedFiles map[string]struct{},
	opts ...condenseOpts,
) bool {
	logCtx := logging.WithComponent(ctx, "checkpoint")
	result, err := s.CondenseSession(ctx, repo, checkpointID, state, committedFiles, opts...)
	if err != nil {
		logging.Warn(
			logCtx, "condensation failed",
			slog.String("session_id", state.SessionID),
			slog.String("error", err.Error()),
		)
		return false
	}

	if result.Skipped {
		logging.Debug(
			logCtx, "condensation skipped, session state unchanged",
			slog.String("session_id", state.SessionID),
			slog.String("checkpoint_id", checkpointID.String()),
		)
		return false
	}

	// Track this shadow branch for cleanup
	shadowBranchesToDelete[shadowBranchName] = struct{}{}

	// Update session state for the new base commit
	newHead := head.Hash().String()
	state.BaseCommit = newHead
	state.RealignAttributionBase(newHead)
	state.StepCount = 0
	state.CheckpointTranscriptStart = result.TotalTranscriptLines
	state.CompactTranscriptStart += result.CompactTranscriptLines
	state.CheckpointTranscriptSize = int64(len(result.Transcript))

	// Clear attribution tracking — condensation already used these values
	state.PromptAttributions = nil
	state.PendingPromptAttribution = nil
	state.FilesTouched = nil

	// NOTE: filesystem prompt.txt is NOT cleared here. The caller (PostCommit handler)
	// decides whether to clear it based on carry-forward: if remaining files exist,
	// the prompt must persist so the next condensation can read it.

	// Save checkpoint ID so subsequent commits can reuse it (e.g., amend restores trailer).
	// LastCheckpointCommitHash records the exact commit SHA so the reconcile path can
	// distinguish a true reset (same SHA) from cherry-pick/rebase (same trailer, new SHA).
	state.LastCheckpointID = checkpointID
	state.LastCheckpointCommitHash = newHead

	logging.Info(
		logCtx, "session condensed",
		slog.String("strategy", "manual-commit"),
		slog.String("session_id", state.SessionID),
		slog.String("checkpoint_id", result.CheckpointID.String()),
		slog.Int("checkpoints_condensed", result.CheckpointsCount),
		slog.Int("transcript_lines", result.TotalTranscriptLines),
	)

	return true
}

// updateBaseCommitIfChanged updates BaseCommit to newHead if it changed.
// Only updates ACTIVE sessions. IDLE/ENDED sessions should NOT have their
// BaseCommit updated, as this would cause them to be incorrectly associated
// with a new shadow branch and potentially condensed on future commits.
func (s *ManualCommitStrategy) updateBaseCommitIfChanged(ctx context.Context, state *SessionState, newHead string) {
	logCtx := logging.WithComponent(ctx, "checkpoint")
	// Only update ACTIVE sessions. IDLE/ENDED sessions are kept around for
	// LastCheckpointID reuse and should not be advanced to HEAD.
	if !state.Phase.IsActive() {
		logging.Debug(
			logCtx, "post-commit: updateBaseCommitIfChanged skipped non-active session",
			slog.String("session_id", state.SessionID),
			slog.String("phase", string(state.Phase)),
		)
		return
	}
	if state.BaseCommit != newHead {
		state.BaseCommit = newHead
		// Keep AttributionBaseCommit in sync to prevent stale base drift.
		// Without this, a subsequent condensation would diff from the old base,
		// inflating human_added with lines from unrelated prior commits.
		state.RealignAttributionBase(newHead)
		logging.Debug(
			logCtx, "post-commit: updated BaseCommit and AttributionBaseCommit",
			slog.String("session_id", state.SessionID),
			slog.String("new_head", truncateHash(newHead)),
		)
	}
}

// postCommitUpdateBaseCommitOnly updates BaseCommit for all sessions on the current
// worktree when a commit has no Trace-Checkpoint trailer. This prevents BaseCommit
// from going stale, which would cause future PrepareCommitMsg calls to skip the
// session (BaseCommit != currentHeadHash filter).
//
// Unlike the full PostCommit flow, this does NOT fire EventGitCommit or trigger
// condensation — it only keeps BaseCommit in sync with HEAD.
func (s *ManualCommitStrategy) postCommitUpdateBaseCommitOnly(ctx context.Context, head *plumbing.Reference) {
	logCtx := logging.WithComponent(ctx, "checkpoint")
	worktreePath, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return // Silent failure — hooks must be resilient
	}

	sessions, err := s.findSessionsForWorktree(ctx, worktreePath)
	if err != nil || len(sessions) == 0 {
		return
	}

	newHead := head.Hash().String()
	for _, state := range sessions {
		// Only update active sessions. Idle/ended sessions are kept around for
		// LastCheckpointID reuse and should not be advanced to HEAD.
		if !state.Phase.IsActive() {
			continue
		}
		if state.BaseCommit != newHead {
			logging.Debug(
				logCtx, "post-commit (no trailer): updating BaseCommit and AttributionBaseCommit",
				slog.String("session_id", state.SessionID),
				slog.String("old_base", truncateHash(state.BaseCommit)),
				slog.String("new_head", truncateHash(newHead)),
			)
			state.BaseCommit = newHead
			// Keep AttributionBaseCommit in sync to prevent stale base drift.
			// Without this, a subsequent condensation would diff from the old base,
			// inflating human_added with lines from unrelated prior commits.
			state.RealignAttributionBase(newHead)
			if err := s.saveSessionState(ctx, state); err != nil {
				logging.Warn(logCtx, "failed to update session state",
					slog.String("session_id", state.SessionID),
					slog.String("error", err.Error()))
			}
		}
	}
}

// truncateHash safely truncates a git hash to 7 chars for logging.
func truncateHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

// filterSessionsWithNewContent returns sessions that have new transcript content
// beyond what was already condensed.
// Computes the staged files list once and reuses it across all sessions to avoid
// redundant `git diff --cached` calls (previously called up to 3 times per session).
func (s *ManualCommitStrategy) filterSessionsWithNewContent(ctx context.Context, repo *git.Repository, sessions []*SessionState) []*SessionState {
	logCtx := logging.WithComponent(ctx, "manual-commit")
	var result []*SessionState

	// Compute staged files once for all sessions.
	// On error, pass nil — sessionHasNewContent treats nil stagedFiles as
	// "unavailable" and skips overlap checks, falling through to other heuristics.
	stagedFiles, err := getStagedFiles(ctx)
	if err != nil {
		logging.Debug(
			logCtx,
			"filterSessionsWithNewContent: getStagedFiles failed, skipping overlap checks",
			slog.String("error", err.Error()),
		)
		stagedFiles = nil
	}

	for _, state := range sessions {
		// Skip fully-condensed ended sessions — no new content possible.
		if state.FullyCondensed && state.Phase == session.PhaseEnded {
			logging.Debug(
				logCtx, "filterSessionsWithNewContent: skipping fully-condensed ended session",
				slog.String("session_id", state.SessionID),
			)
			continue
		}
		hasNew, err := s.sessionHasNewContent(ctx, repo, state, contentCheckOpts{stagedFiles: stagedFiles})
		if err != nil {
			logging.Debug(
				logCtx, "filterSessionsWithNewContent: error checking session, skipping it",
				slog.String("session_id", state.SessionID),
				slog.String("error", err.Error()),
			)
			continue
		}
		if !hasNew {
			logging.Debug(
				logCtx, "filterSessionsWithNewContent: session has no new content",
				slog.String("session_id", state.SessionID),
				slog.String("phase", string(state.Phase)),
				slog.Int("files_touched", len(state.FilesTouched)),
			)
		}
		if hasNew {
			result = append(result, state)
		}
	}

	return result
}

// contentCheckOpts holds pre-computed values for sessionHasNewContent to avoid
// redundant work across multiple sessions in a single hook invocation.
type contentCheckOpts struct {
	// stagedFiles is the pre-computed list of staged files (from getStagedFiles).
	// nil means staged files are unavailable (error or PostCommit context where
	// files are already committed) — callers skip overlap checks and fall through
	// to other heuristics (e.g., transcript growth).
	// Non-nil empty means successfully resolved but no files are staged.
	stagedFiles []string

	// shadowTree, when non-nil, is used directly to avoid redundant shadow branch
	// resolution (the shadow ref/commit/tree were already resolved by the caller).
	shadowTree *object.Tree
}
