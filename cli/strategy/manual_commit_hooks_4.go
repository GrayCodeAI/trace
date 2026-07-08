package strategy

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/types"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/checkpoint/remote"
	"github.com/GrayCodeAI/trace/cli/gitops"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/settings"
	"github.com/GrayCodeAI/trace/perf"
	"github.com/GrayCodeAI/trace/redact"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/utils/binary"
)

// calculatePromptAttributionAtStart calculates attribution at prompt start (before agent runs).
// This captures user changes since the last checkpoint - no filtering needed since
// the agent hasn't made any changes yet.
//
// IMPORTANT: This reads from the worktree (not staging area) to match what WriteTemporary
// captures in checkpoints. If we read staged content but checkpoints capture worktree content,
// unstaged changes would be in the checkpoint but not counted in PromptAttribution, causing
// them to be incorrectly attributed to the agent later.
func (s *ManualCommitStrategy) calculatePromptAttributionAtStart(
	ctx context.Context,
	repo *git.Repository,
	state *SessionState,
) PromptAttribution {
	logCtx := logging.WithComponent(ctx, "attribution")
	nextCheckpointNum := state.StepCount + 1
	result := PromptAttribution{CheckpointNumber: nextCheckpointNum}

	// Get last checkpoint tree from shadow branch (if it exists).
	// For a new session (StepCount == 0), always use baseTree as the reference.
	// The shadow branch may contain checkpoints from OTHER concurrent sessions,
	// and using that tree would miss pre-session worktree dirt (e.g., .claude/settings.json)
	// because it appears unchanged when compared to another session's snapshot.
	var lastCheckpointTree *object.Tree
	if state.StepCount > 0 {
		// Existing session with prior checkpoints — use shadow branch as reference.
		shadowBranchName := checkpoint.ShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)
		refName := plumbing.NewBranchReferenceName(shadowBranchName)
		if ref, err := repo.Reference(refName, true); err != nil {
			logging.Debug(logCtx, "prompt attribution: no shadow branch",
				slog.String("shadow_branch", shadowBranchName))
		} else if shadowCommit, err := repo.CommitObject(ref.Hash()); err != nil {
			logging.Debug(logCtx, "prompt attribution: failed to get shadow commit",
				slog.String("shadow_ref", ref.Hash().String()),
				slog.String("error", err.Error()))
		} else if tree, err := shadowCommit.Tree(); err != nil {
			logging.Debug(logCtx, "prompt attribution: failed to get shadow tree",
				slog.String("error", err.Error()))
		} else {
			lastCheckpointTree = tree
		}
	}
	// For new sessions (StepCount == 0), lastCheckpointTree stays nil.
	// CalculatePromptAttribution falls back to baseTree, ensuring pre-session
	// worktree dirt is captured even when the shadow branch has other sessions' data.

	// Get base tree for agent lines calculation
	var baseTree *object.Tree
	if baseCommit, err := repo.CommitObject(plumbing.NewHash(state.BaseCommit)); err == nil {
		if tree, treeErr := baseCommit.Tree(); treeErr == nil {
			baseTree = tree
		} else {
			logging.Debug(logCtx, "prompt attribution: base tree unavailable",
				slog.String("error", treeErr.Error()))
		}
	} else {
		logging.Debug(logCtx, "prompt attribution: base commit unavailable",
			slog.String("base_commit", state.BaseCommit),
			slog.String("error", err.Error()))
	}

	worktree, err := repo.Worktree()
	if err != nil {
		logging.Debug(logCtx, "prompt attribution skipped: failed to get worktree",
			slog.String("error", err.Error()))
		return result
	}

	// Get worktree status to find ALL changed files
	status, err := worktree.Status()
	if err != nil {
		logging.Debug(logCtx, "prompt attribution skipped: failed to get worktree status",
			slog.String("error", err.Error()))
		return result
	}

	worktreeRoot := worktree.Filesystem().Root()

	// Build map of changed files with their worktree content
	// IMPORTANT: We read from worktree (not staging area) to match what WriteTemporary
	// captures in checkpoints. This ensures attribution is consistent.
	changedFiles := make(map[string]string)
	for filePath, fileStatus := range status {
		// Skip unmodified files
		if fileStatus.Worktree == git.Unmodified && fileStatus.Staging == git.Unmodified {
			continue
		}
		// Skip .trace metadata directory (session data, not user code)
		if strings.HasPrefix(filePath, paths.TraceMetadataDir+"/") || strings.HasPrefix(filePath, ".trace/") {
			continue
		}

		// Always read from worktree to match checkpoint behavior
		fullPath := filepath.Join(worktreeRoot, filePath)
		var content string
		if data, err := os.ReadFile(fullPath); err == nil { //nolint:gosec // filePath is from git worktree status
			// Use git's binary detection algorithm (matches getFileContent behavior).
			// Binary files are excluded from line-based attribution calculations.
			isBinary, binErr := binary.IsBinary(bytes.NewReader(data))
			if binErr == nil && !isBinary {
				content = string(data)
			}
		}
		// else: file deleted, unreadable, or binary - content remains empty string

		changedFiles[filePath] = content
	}

	// Use CalculatePromptAttribution from manual_commit_attribution.go
	result = CalculatePromptAttribution(baseTree, lastCheckpointTree, changedFiles, nextCheckpointNum)

	return result
}

// getStagedFiles returns a list of files staged for commit using native git CLI.
// This is much faster than go-git's worktree.Status() which scans the entire
// working tree. `git diff --cached --name-only` uses native git's optimized index
// and filesystem monitors.
//
// Returns (non-nil empty slice, nil) when no files are staged — callers can
// distinguish "no staged files" from "error resolving staged files" (nil, err).
func getStagedFiles(ctx context.Context) ([]string, error) {
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree root: %w", err)
	}

	cmd := exec.CommandContext(ctx, "git", "diff", "--cached", "--name-only")
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff --cached: %w", err)
	}

	staged := []string{}
	trimmed := strings.TrimSpace(string(output))
	// Normalize Windows line endings (\r\n) to Unix (\n) for cross-platform git output
	trimmed = strings.ReplaceAll(trimmed, "\r\n", "\n")
	for _, line := range strings.Split(trimmed, "\n") {
		if line != "" {
			staged = append(staged, filepath.ToSlash(line))
		}
	}
	return staged, nil
}

// getLastPrompt retrieves the most recent user prompt from a session's shadow branch.
// Reads prompt.txt directly from the shadow branch tree instead of parsing the full
// transcript (which involves token counting, context generation, etc.).
// Returns empty string if no prompt can be retrieved.
func (s *ManualCommitStrategy) getLastPrompt(ctx context.Context, repo *git.Repository, state *SessionState) string {
	prompts := readPromptsFromShadowBranch(ctx, repo, state)
	if len(prompts) == 0 {
		return ""
	}
	// Iterate backwards to find the last non-empty prompt.
	for i := len(prompts) - 1; i >= 0; i-- {
		cleaned := strings.TrimSpace(prompts[i])
		if cleaned != "" && !isOnlySeparators(cleaned) {
			return cleaned
		}
	}
	return ""
}

// readPromptsFromShadowBranch reads prompt.txt from the shadow branch tree.
// Returns all prompts split on "\n\n---\n\n", or nil if prompt.txt is not available.
func readPromptsFromShadowBranch(_ context.Context, repo *git.Repository, state *SessionState) []string {
	shadowBranchName := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)
	refName := plumbing.NewBranchReferenceName(shadowBranchName)
	ref, err := repo.Reference(refName, true)
	if err != nil {
		return nil
	}

	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil
	}

	metadataDir := paths.TraceMetadataDir + "/" + state.SessionID
	promptPath := metadataDir + "/" + paths.PromptFileName
	file, err := tree.File(promptPath)
	if err != nil {
		return nil
	}

	content, err := file.Contents()
	if err != nil {
		return nil
	}

	return splitPromptContent(content)
}

// HandleTurnEnd dispatches strategy-specific actions emitted when an agent turn ends.
// The primary job is to finalize all checkpoints from this turn with the full transcript.
//
// During a turn, PostCommit writes provisional transcript data (whatever was available
// at commit time). HandleTurnEnd replaces that with the complete session transcript
// (from prompt to stop event), ensuring every checkpoint has the full context.
//

func (s *ManualCommitStrategy) HandleTurnEnd(ctx context.Context, state *SessionState) error { //nolint:unparam // error return is part of the hook contract; callers check it
	hadMidTurnCommits := len(state.TurnCheckpointIDs) > 0

	// Finalize all checkpoints from this turn with the full transcript.
	//
	// IMPORTANT: This is best-effort - errors are logged but don't fail the hook.
	// Failing here would prevent session cleanup and could leave state inconsistent.
	// The provisional transcript from PostCommit is already persisted, so the
	// checkpoint isn't lost - it just won't have the complete transcript.
	errCount := s.finalizeAllTurnCheckpoints(ctx, state)
	if errCount > 0 {
		logCtx := logging.WithComponent(ctx, "checkpoint")
		logging.Warn(
			logCtx, "HandleTurnEnd completed with errors (best-effort)",
			slog.String("session_id", state.SessionID),
			slog.Int("error_count", errCount),
		)
	}

	// Advance CheckpointTranscriptStart to the actual transcript end after
	// mid-turn commits. When an agent commits mid-turn (e.g., Codex "commit/push"),
	// condensation records TotalTranscriptLines at commit time, but the agent
	// continues writing to the transcript (tool results, token counts, task_complete).
	// Without this fix, the next checkpoint's scoped transcript starts mid-turn,
	// including a tail of already-condensed content.
	//
	// Skip this when carry-forward is active. carryForwardToNewShadowBranch
	// intentionally resets CheckpointTranscriptStart to 0 so the next checkpoint
	// remains self-contained with the full transcript.
	if hadMidTurnCommits && state.TranscriptPath != "" && len(state.FilesTouched) == 0 {
		transcriptPath, resolveErr := resolveTranscriptPath(state)
		if resolveErr == nil {
			if ag, agErr := agent.GetByAgentType(state.AgentType); agErr == nil {
				if analyzer, ok := agent.AsTranscriptAnalyzer(ag); ok {
					if pos, posErr := analyzer.GetTranscriptPosition(transcriptPath); posErr == nil && pos > state.CheckpointTranscriptStart {
						logging.Debug(
							logging.WithComponent(ctx, "hooks"),
							"advancing CheckpointTranscriptStart to turn end after mid-turn commit",
							slog.String("session_id", state.SessionID),
							slog.Int("old_offset", state.CheckpointTranscriptStart),
							slog.Int("new_offset", pos),
						)
						state.CheckpointTranscriptStart = pos
					}
				}
			}
		}
	}

	return nil
}

// precomputeTranscriptBlobsForFinalize chunks + zlib-compresses the redacted
// transcript once for reuse across every checkpoint in the turn. Returns nil
// (without error) when the transcript is empty — downstream stores skip
// transcript updates in that case, so precompute would only write a wasted
// empty-chunk blob to the object store. On failure, logs a warning and
// returns nil so the loop falls back to per-checkpoint chunking.
func precomputeTranscriptBlobsForFinalize(ctx context.Context, repo *git.Repository, transcript redact.RedactedBytes, state *SessionState) *checkpoint.PrecomputedTranscriptBlobs {
	if transcript.Len() == 0 {
		return nil
	}
	_, span := perf.Start(ctx, "precompute_transcript_blobs")
	defer span.End()
	precomputed, err := checkpoint.PrecomputeTranscriptBlobs(ctx, repo, transcript, state.AgentType)
	if err != nil {
		logging.Warn(
			ctx, "finalize: precompute transcript blobs failed, falling back to per-checkpoint work",
			slog.String("session_id", state.SessionID),
			slog.String("error", err.Error()),
		)
		return nil
	}
	return precomputed
}

// finalizeAllTurnCheckpoints replaces the provisional transcript in each checkpoint
// created during this turn with the full session transcript.
//
// This is called at turn end (stop hook). During the turn, PostCommit wrote whatever
// transcript was available at commit time. Now we have the complete transcript and
// replace it so every checkpoint has the full prompt-to-stop context.
//
// Returns the number of errors encountered (best-effort: continues processing on error).
func (s *ManualCommitStrategy) finalizeAllTurnCheckpoints(ctx context.Context, state *SessionState) int {
	if len(state.TurnCheckpointIDs) == 0 {
		return 0 // No mid-turn commits to finalize
	}

	logCtx := logging.WithComponent(ctx, "checkpoint")

	logging.Info(
		logCtx, "finalizing turn checkpoints with full transcript",
		slog.String("session_id", state.SessionID),
		slog.Int("checkpoint_count", len(state.TurnCheckpointIDs)),
	)

	errCount := 0

	// Read full transcript from live transcript file, re-resolving the path if the
	// agent relocated it mid-session (e.g., Cursor CLI flat → nested layout change).
	if state.TranscriptPath == "" {
		logging.Warn(
			logCtx, "finalize: no transcript path, skipping",
			slog.String("session_id", state.SessionID),
		)
		state.TurnCheckpointIDs = nil
		return 1 // Count as error - all checkpoints will be skipped
	}

	transcriptPath, resolveErr := resolveTranscriptPath(state)
	if resolveErr != nil {
		logging.Warn(
			logCtx, "finalize: transcript path resolution failed, skipping",
			slog.String("session_id", state.SessionID),
			slog.Any("error", resolveErr),
		)
		state.TurnCheckpointIDs = nil
		return 1 // Count as error - all checkpoints will be skipped
	}

	fullTranscript, err := os.ReadFile(transcriptPath) //nolint:gosec // path validated by resolveTranscriptPath
	if err != nil || len(fullTranscript) == 0 {
		msg := "finalize: empty transcript, skipping"
		if err != nil {
			msg = "finalize: failed to read transcript, skipping"
		}
		logging.Warn(
			logCtx, msg,
			slog.String("session_id", state.SessionID),
			slog.String("transcript_path", state.TranscriptPath),
			slog.Any("error", err),
		)
		state.TurnCheckpointIDs = nil
		return 1 // Count as error - all checkpoints will be skipped
	}

	// Open repository (needed for shadow branch prompt reading and checkpoint store)
	repo, err := OpenRepository(ctx)
	if err != nil {
		logging.Warn(
			logCtx, "finalize: failed to open repository",
			slog.String("error", err.Error()),
		)
		state.TurnCheckpointIDs = nil
		return 1 // Count as error - all checkpoints will be skipped
	}

	prompts := readPromptsFromShadowBranch(ctx, repo, state)
	if len(prompts) == 0 {
		prompts = readPromptsFromFilesystem(ctx, state.SessionID)
	}

	// Redact secrets before writing. Checkpoint store methods require
	// pre-redacted in-memory transcript content from callers. The live
	// transcript on disk is still treated as raw/untrusted input, so redact it
	// here before anything is persisted to the metadata branch.
	//
	// On failure: drop the transcript but continue writing checkpoint metadata
	// (attribution, files touched, prompts). Hooks run without user interaction
	// so there is no retry path — preserving partial metadata is better than
	// losing everything. Persisting an unredacted transcript would be worse.
	_, redactSpan := perf.Start(logCtx, "redact_transcript")
	redactedTranscript, redactErr := redact.JSONLBytes(fullTranscript)
	redactSpan.End()
	if redactErr != nil {
		logging.Warn(
			logCtx, "finalize: transcript redaction failed, dropping transcript",
			slog.String("session_id", state.SessionID),
			slog.String("error", redactErr.Error()),
		)
		redactedTranscript = redact.RedactedBytes{}
	}
	for i, p := range prompts {
		prompts[i] = redact.String(p)
	}

	store := checkpoint.NewGitStore(repo)
	v2 := settings.CheckpointsVersion(logCtx) == 2

	// Evaluate v2 flag once before the loop to avoid re-reading settings per checkpoint
	var v2Store *checkpoint.V2GitStore
	if settings.IsCheckpointsV2Enabled(logCtx) {
		v2URL, err := remote.FetchURL(logCtx)
		if err != nil {
			logging.Debug(
				logCtx, "finalize: using origin for v2 store fetch remote",
				slog.String("error", err.Error()),
			)
			v2URL = originRemote
		}
		v2Store = checkpoint.NewV2GitStore(repo, v2URL)
	}

	precomputed := precomputeTranscriptBlobsForFinalize(logCtx, repo, redactedTranscript, state)

	// Resolve the agent and try external compaction once before the loop —
	// external compaction is invariant across checkpoints (same session/transcript).
	// Internal compaction must remain per-checkpoint due to per-checkpoint startLine.
	finalAg, _ := agent.GetByAgentType(state.AgentType) //nolint:errcheck // ag may be nil; compactTranscriptForV2 handles nil
	var externalCompact []byte
	var isExternalAgent bool
	if v2Store != nil {
		externalCompact, isExternalAgent = compactAndRedactExternalTranscript(logCtx, finalAg, state)
	}

	// Update each checkpoint with the full transcript
	for _, cpIDStr := range state.TurnCheckpointIDs {
		cpID, parseErr := id.NewCheckpointID(cpIDStr)
		if parseErr != nil {
			logging.Warn(
				logCtx, "finalize: invalid checkpoint ID, skipping",
				slog.String("checkpoint_id", cpIDStr),
				slog.String("error", parseErr.Error()),
			)
			errCount++
			continue
		}

		updateOpts := checkpoint.UpdateCommittedOptions{
			CheckpointID:     cpID,
			SessionID:        state.SessionID,
			Transcript:       redactedTranscript,
			Prompts:          prompts,
			Agent:            state.AgentType,
			PrecomputedBlobs: precomputed,
		}

		// Generate compact transcript for v2 /main
		if v2Store != nil {
			if isExternalAgent {
				updateOpts.CompactTranscript = externalCompact
			} else if redactedTranscript.Len() > 0 {
				updateOpts.CompactTranscript = finalizeInternalCompactTranscript(logCtx, finalAg, cpID, state, redactedTranscript, store, v2Store, v2)
			}
		}

		if !v2 {
			updateErr := store.UpdateCommitted(ctx, updateOpts)
			if updateErr != nil {
				logging.Warn(
					logCtx, "finalize: failed to update checkpoint",
					slog.String("checkpoint_id", cpIDStr),
					slog.String("error", updateErr.Error()),
				)
				errCount++
				continue
			}
		}

		if v2Store != nil {
			if v2Err := v2Store.UpdateCommitted(logCtx, updateOpts); v2Err != nil {
				attrs := []any{
					slog.String("checkpoint_id", cpIDStr),
					slog.String("error", v2Err.Error()),
				}
				if v2 {
					logging.Warn(logCtx, "finalize: failed to update checkpoint in v2", attrs...)
					errCount++
					continue
				}
				logging.Warn(logCtx, "v2 dual-write update failed", attrs...)
			}
		}

		logging.Info(
			logCtx, "finalize: checkpoint updated with full transcript",
			slog.String("checkpoint_id", cpIDStr),
			slog.String("session_id", state.SessionID),
		)
	}

	// Clear turn checkpoint IDs. Do NOT update CheckpointTranscriptStart here — it was
	// already set correctly by PostCommit: condenseAndUpdateState sets it to the total
	// transcript lines when condensing, and carryForwardToNewShadowBranch resets it to 0
	// when carry-forward is active. Overwriting here would break carry-forward by making
	// sessionHasNewContent think the transcript is fully consumed (no growth).
	state.TurnCheckpointIDs = nil

	return errCount
}

// finalizeInternalCompactTranscript resolves the per-checkpoint startLine and
// produces the compact transcript for built-in agents during finalization.
func finalizeInternalCompactTranscript(
	ctx context.Context,
	ag agent.Agent,
	cpID id.CheckpointID,
	state *SessionState,
	redactedTranscript redact.RedactedBytes,
	store *checkpoint.GitStore,
	v2Store *checkpoint.V2GitStore,
	v2 bool,
) []byte {
	var (
		content *checkpoint.SessionContent
		readErr error
	)
	if v2 {
		content, readErr = v2Store.ReadSessionContentByID(ctx, cpID, state.SessionID)
	} else {
		content, readErr = store.ReadSessionContentByID(ctx, cpID, state.SessionID)
	}
	startLine := 0
	if readErr == nil && content != nil {
		startLine = content.Metadata.GetTranscriptStart()
	} else {
		errMsg := "unknown"
		if readErr != nil {
			errMsg = readErr.Error()
		}
		logging.Debug(
			ctx, "finalize: failed to read checkpoint metadata, using full transcript for compact output",
			slog.String("checkpoint_id", cpID.String()),
			slog.String("session_id", state.SessionID),
			slog.String("error", errMsg),
		)
	}
	return compactTranscriptForV2(ctx, ag, redactedTranscript, startLine)
}

// filesChangedInCommit returns the set of files changed in a commit using git diff-tree.
// Uses the git CLI for faster performance vs go-git tree walks (lower constant factors).
// Falls back to go-git tree walk if git diff-tree fails, since an empty result would
// break downstream condensation and carry-forward logic.
func filesChangedInCommit(ctx context.Context, repoDir string, commit *object.Commit, headTree, parentTree *object.Tree) map[string]struct{} {
	var parentHash string
	if commit.NumParents() > 0 {
		parentHash = commit.ParentHashes[0].String()
	}
	result, err := gitops.DiffTreeFiles(ctx, repoDir, parentHash, commit.Hash.String())
	if err != nil {
		logging.Warn(
			ctx, "post-commit: git diff-tree failed, falling back to tree walk",
			slog.String("commit", commit.Hash.String()),
			slog.String("error", err.Error()),
		)
		return filesChangedInCommitFallback(ctx, headTree, parentTree)
	}
	return result
}

// filesChangedInCommitFallback uses go-git tree walks to compute changed files.
// Slower than git diff-tree but doesn't depend on an external process.
func filesChangedInCommitFallback(ctx context.Context, headTree, parentTree *object.Tree) map[string]struct{} {
	files, err := getAllChangedFilesBetweenTreesSlow(ctx, parentTree, headTree)
	if err != nil {
		logging.Warn(
			ctx, "post-commit: tree walk fallback also failed; condensation and carry-forward may be affected",
			slog.String("error", err.Error()),
		)
		return make(map[string]struct{})
	}
	result := make(map[string]struct{}, len(files))
	for _, f := range files {
		result[f] = struct{}{}
	}
	return result
}

// subtractFiles returns files that are NOT in the exclude set.
func subtractFiles(files []string, exclude map[string]struct{}) []string {
	var remaining []string
	for _, f := range files {
		if _, excluded := exclude[f]; !excluded {
			remaining = append(remaining, f)
		}
	}
	return remaining
}

// carryForwardToNewShadowBranch creates a new shadow branch at the current HEAD
// containing the remaining uncommitted files and all session metadata.
// This enables the next commit to get its own unique checkpoint.
func (s *ManualCommitStrategy) carryForwardToNewShadowBranch(
	ctx context.Context,
	repo *git.Repository,
	state *SessionState,
	remainingFiles []string,
) {
	logCtx := logging.WithComponent(ctx, "checkpoint")
	start := time.Now()
	store := checkpoint.NewGitStore(repo)

	// Don't include metadata directory in carry-forward. The carry-forward branch
	// only needs to preserve file content for comparison - not the transcript.
	// Including the transcript would cause sessionHasNewContent to always return true
	// because CheckpointTranscriptStart is reset to 0 for carry-forward.
	writeCtx, carryForwardWriteSpan := perf.Start(ctx, "write_carry_forward_shadow")
	result, err := store.WriteTemporary(writeCtx, checkpoint.WriteTemporaryOptions{
		SessionID:         state.SessionID,
		BaseCommit:        state.BaseCommit,
		WorktreeID:        state.WorktreeID,
		ModifiedFiles:     remainingFiles,
		MetadataDir:       "",
		MetadataDirAbs:    "",
		CommitMessage:     "carry forward: uncommitted session files",
		IsFirstCheckpoint: false,
	})
	if err != nil {
		carryForwardWriteSpan.RecordError(err)
		carryForwardWriteSpan.End()
		logging.Warn(
			logCtx, "post-commit: carry-forward failed",
			slog.String("session_id", state.SessionID),
			slog.String("error", err.Error()),
		)
		return
	}
	carryForwardWriteSpan.End()
	duration := time.Since(start)
	if result.Skipped {
		logging.Debug(
			logCtx, "post-commit: carry-forward skipped (no changes)",
			slog.String("session_id", state.SessionID),
		)
		return
	}

	// Update state for the carry-forward checkpoint.
	// CheckpointTranscriptStart = 0 is intentional: each checkpoint is self-contained with
	// the full transcript. This trades storage efficiency for simplicity:
	// - Pro: Each checkpoint is independently readable without needing to stitch together
	//   multiple checkpoints to understand the session history
	// - Con: For long sessions with multiple partial commits, each checkpoint includes
	//   the full transcript, which could be large
	// An alternative would be incremental checkpoints (only new content since last condensation),
	// but this would complicate checkpoint retrieval and require careful tracking of dependencies.
	state.StepCount = 1
	state.CheckpointTranscriptStart = 0
	state.CompactTranscriptStart = 0
	state.CheckpointTranscriptSize = 0
	state.LastCheckpointID = ""
	// NOTE: TurnCheckpointIDs is intentionally NOT cleared here. Those checkpoint
	// IDs from earlier in the turn still need finalization with the full transcript
	// when HandleTurnEnd runs at stop time.

	logging.Info(
		logCtx, "post-commit: carried forward remaining files",
		slog.String("session_id", state.SessionID),
		slog.Int("remaining_files", len(remainingFiles)),
	)
	logging.Debug(
		logCtx, "carry-forward timings",
		slog.String("session_id", state.SessionID),
		slog.Int64("write_carry_forward_shadow_ms", duration.Milliseconds()),
		slog.Int("remaining_files", len(remainingFiles)),
	)
}

// resolveSessionAgentType picks the most reliable identifier for which agent
// owns a session, given the agent whose hook is firing right now (callerAgentType),
// an optional transcript path, and the SessionStart hint stored under the
// session ID.
func resolveSessionAgentType(ctx context.Context, sessionID string, callerAgentType types.AgentType, transcriptPath string) types.AgentType {
	if repoRoot, err := paths.WorktreeRoot(ctx); err == nil && transcriptPath != "" {
		if owner, ok := agent.ForTranscriptPath(transcriptPath, repoRoot); ok {
			return owner.Type()
		}
	}
	if hint := LoadAgentTypeHint(ctx, sessionID); hint != "" && hint != agent.AgentTypeUnknown {
		return hint
	}
	return callerAgentType
}

// correctSessionAgentType returns the agent type that owns transcriptPath when
// it disagrees with currentType. Returns (currentType, false) if there's no
// disagreement or no transcript signal.
func correctSessionAgentType(ctx context.Context, currentType types.AgentType, transcriptPath string) (types.AgentType, bool) {
	if transcriptPath == "" {
		return currentType, false
	}
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return currentType, false
	}
	owner, ok := agent.ForTranscriptPath(transcriptPath, repoRoot)
	if !ok {
		return currentType, false
	}
	if owner.Type() == currentType {
		return currentType, false
	}
	return owner.Type(), true
}
