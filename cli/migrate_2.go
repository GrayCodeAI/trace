package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/transcript/compact"
	"github.com/GrayCodeAI/trace/cli/versioninfo"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

func pruneCheckpointFromRoot(repo *git.Repository, rootTreeHash plumbing.Hash, shardPrefix, shardSuffix string) (plumbing.Hash, error) {
	newRoot, err := checkpoint.UpdateSubtree(
		repo, rootTreeHash,
		[]string{shardPrefix},
		nil,
		checkpoint.UpdateSubtreeOptions{
			MergeMode:   checkpoint.MergeKeepExisting,
			DeleteNames: []string{shardSuffix},
		},
	)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to prune checkpoint from shard: %w", err)
	}
	if newRoot == rootTreeHash {
		return newRoot, nil
	}

	newRootTree, err := repo.TreeObject(newRoot)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to read pruned root tree: %w", err)
	}
	shardTree, err := newRootTree.Tree(shardPrefix)
	if err != nil {
		return newRoot, nil //nolint:nilerr // The shard prefix was already absent after pruning.
	}
	if len(shardTree.Entries) > 0 {
		return newRoot, nil
	}

	prunedRoot, err := checkpoint.UpdateSubtree(
		repo, rootTreeHash,
		nil,
		nil,
		checkpoint.UpdateSubtreeOptions{
			MergeMode:   checkpoint.MergeKeepExisting,
			DeleteNames: []string{shardPrefix},
		},
	)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to prune empty shard prefix: %w", err)
	}
	return prunedRoot, nil
}

func collectMissingFullCheckpointForPacking(
	ctx context.Context,
	repo *git.Repository,
	v1Store *checkpoint.GitStore,
	v2Store *checkpoint.V2GitStore,
	info checkpoint.CommittedInfo,
	v2Summary *checkpoint.CheckpointSummary,
) (*migratedFullCheckpoint, bool, error) {
	missingSessions, err := collectMissingFullSessionsForPacking(ctx, v2Store, info.CheckpointID, v2Summary)
	if err != nil {
		return nil, false, err
	}
	if len(missingSessions) == 0 {
		return nil, false, nil
	}

	v1Summary, err := v1Store.ReadCommitted(ctx, info.CheckpointID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to read v1 summary while checking v2 raw artifacts: %w", err)
	}
	if v1Summary == nil {
		return nil, false, fmt.Errorf("v1 checkpoint %s has no summary", info.CheckpointID)
	}

	v1BySessionID, err := collectV1SessionIndexesForPacking(ctx, v1Store, info.CheckpointID, v1Summary, missingSessions)
	if err != nil {
		return nil, false, err
	}

	fullCheckpoint := &migratedFullCheckpoint{
		checkpointID: info.CheckpointID,
	}
	v1ToV2SessionIdx := make(map[int]int)

	for _, missingSession := range missingSessions {
		v1Session, ok, readErr := readV1SessionForMissingFullArtifact(ctx, v1Store, info.CheckpointID, v1Summary, v1BySessionID, missingSession)
		if readErr != nil {
			return nil, false, readErr
		}
		if !ok {
			return nil, false, fmt.Errorf("failed to find v1 session for v2 session %d while checking raw artifacts", missingSession.sessionIndex)
		}

		fullCheckpoint.sessions = append(fullCheckpoint.sessions, migratedFullSession{
			sessionIndex: missingSession.sessionIndex,
			content:      v1Session.content,
		})
		v1ToV2SessionIdx[v1Session.sessionIndex] = missingSession.sessionIndex
	}

	latestV2SessionIdx := len(v2Summary.Sessions) - 1
	taskTrees, taskErr := collectTaskMetadataForMigratedFullGenerationWithRootSession(
		repo,
		info.CheckpointID,
		v1Summary,
		v1ToV2SessionIdx,
		latestV2SessionIdx,
		latestV2SessionIdx >= 0,
	)
	if taskErr != nil {
		return nil, false, fmt.Errorf("failed to collect task metadata while checking raw artifacts: %w", taskErr)
	}
	fullCheckpoint.taskTrees = taskTrees

	return fullCheckpoint, true, nil
}

type missingFullSessionForPacking struct {
	sessionIndex int
	sessionID    string
}

type v1SessionForPacking struct {
	sessionIndex int
	content      *checkpoint.SessionContent
}

func collectMissingFullSessionsForPacking(
	ctx context.Context,
	v2Store *checkpoint.V2GitStore,
	checkpointID id.CheckpointID,
	summary *checkpoint.CheckpointSummary,
) ([]missingFullSessionForPacking, error) {
	missingSessions := make([]missingFullSessionForPacking, 0)
	for sessionIdx := range len(summary.Sessions) {
		ok, checkErr := hasFullSessionArtifacts(v2Store, checkpointID, sessionIdx)
		if checkErr != nil {
			return nil, fmt.Errorf("failed to check v2 session %d artifacts: %w", sessionIdx, checkErr)
		}
		if ok {
			continue
		}

		v2Content, readErr := v2Store.ReadSessionMetadataAndPrompts(ctx, checkpointID, sessionIdx)
		if readErr != nil {
			return nil, fmt.Errorf("failed to read v2 session %d metadata while checking raw artifacts: %w", sessionIdx, readErr)
		}

		missingSessions = append(missingSessions, missingFullSessionForPacking{
			sessionIndex: sessionIdx,
			sessionID:    v2Content.Metadata.SessionID,
		})
	}

	return missingSessions, nil
}

func collectV1SessionIndexesForPacking(
	ctx context.Context,
	v1Store *checkpoint.GitStore,
	checkpointID id.CheckpointID,
	summary *checkpoint.CheckpointSummary,
	missingSessions []missingFullSessionForPacking,
) (map[string][]int, error) {
	neededSessionIDs := make(map[string]struct{})
	for _, session := range missingSessions {
		if session.sessionID != "" {
			neededSessionIDs[session.sessionID] = struct{}{}
		}
	}

	bySessionID := make(map[string][]int)
	if len(neededSessionIDs) == 0 {
		return bySessionID, nil
	}

	for sessionIdx := range len(summary.Sessions) {
		metadata, err := v1Store.ReadSessionMetadata(ctx, checkpointID, sessionIdx)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("context canceled while reading v1 session metadata: %w", ctxErr)
			}
			continue
		}
		if _, ok := neededSessionIDs[metadata.SessionID]; ok {
			bySessionID[metadata.SessionID] = append(bySessionID[metadata.SessionID], sessionIdx)
		}
	}

	return bySessionID, nil
}

func readV1SessionForMissingFullArtifact(
	ctx context.Context,
	v1Store *checkpoint.GitStore,
	checkpointID id.CheckpointID,
	summary *checkpoint.CheckpointSummary,
	bySessionID map[string][]int,
	missingSession missingFullSessionForPacking,
) (v1SessionForPacking, bool, error) {
	var triedSessionIndexes map[int]struct{}
	if missingSession.sessionID != "" {
		indexes := bySessionID[missingSession.sessionID]
		triedSessionIndexes = make(map[int]struct{}, len(indexes))
		for i := len(indexes) - 1; i >= 0; i-- {
			sessionIdx := indexes[i]
			triedSessionIndexes[sessionIdx] = struct{}{}
			session, found, err := readV1SessionForPacking(ctx, v1Store, checkpointID, sessionIdx)
			if err != nil || found {
				return session, found, err
			}
		}
	}

	if missingSession.sessionIndex >= len(summary.Sessions) {
		return v1SessionForPacking{}, false, nil
	}
	if _, tried := triedSessionIndexes[missingSession.sessionIndex]; tried {
		return v1SessionForPacking{}, false, nil
	}
	return readV1SessionForPacking(ctx, v1Store, checkpointID, missingSession.sessionIndex)
}

func readV1SessionForPacking(
	ctx context.Context,
	v1Store *checkpoint.GitStore,
	checkpointID id.CheckpointID,
	sessionIdx int,
) (v1SessionForPacking, bool, error) {
	content, err := v1Store.ReadSessionContent(ctx, checkpointID, sessionIdx)
	if err != nil {
		if errors.Is(err, checkpoint.ErrNoTranscript) || errors.Is(err, checkpoint.ErrCheckpointNotFound) {
			return v1SessionForPacking{}, false, nil
		}
		return v1SessionForPacking{}, false, fmt.Errorf("failed to read v1 session %d while checking raw artifacts: %w", sessionIdx, err)
	}

	return v1SessionForPacking{
		sessionIndex: sessionIdx,
		content:      content,
	}, true, nil
}

func hasFullSessionArtifacts(v2Store *checkpoint.V2GitStore, cpID id.CheckpointID, sessionIdx int) (bool, error) {
	ok, err := v2Store.HasFullSessionArtifacts(cpID, sessionIdx)
	if err != nil {
		return false, fmt.Errorf("failed to check v2 full artifacts for session %d: %w", sessionIdx, err)
	}
	return ok, nil
}

// backfillCompactTranscripts checks sessions in an already-migrated v2 checkpoint
// for missing transcript.jsonl and attempts to generate + write them from v1 data.
// Returns errAlreadyMigrated if all sessions already have compact transcripts.
func backfillCompactTranscripts(ctx context.Context, v1Store *checkpoint.GitStore, v2Store *checkpoint.V2GitStore, info checkpoint.CommittedInfo, v2Summary *checkpoint.CheckpointSummary) (int, error) {
	// Find sessions missing transcript.jsonl
	var needsBackfill []int
	for i, session := range v2Summary.Sessions {
		if session.Transcript == "" {
			needsBackfill = append(needsBackfill, i)
		}
	}

	if len(needsBackfill) == 0 {
		return 0, errAlreadyMigrated
	}

	backfilled := 0
	var lastAgent string

	for _, sessionIdx := range needsBackfill {
		content, readErr := v1Store.ReadSessionContent(ctx, info.CheckpointID, sessionIdx)
		if readErr != nil {
			logging.Warn(
				ctx, "transcript.jsonl backfill: could not read v1 session",
				slog.String("checkpoint_id", string(info.CheckpointID)),
				slog.Int("session_index", sessionIdx),
				slog.String("error", readErr.Error()),
			)
			continue
		}

		if content.Metadata.Agent != "" {
			lastAgent = string(content.Metadata.Agent)
		}

		compacted := tryCompactTranscript(ctx, content.Transcript, content.Metadata)
		if compacted == nil {
			// tryCompactTranscript already logs for no-agent and compact-error cases;
			// log the empty-transcript case here.
			if len(content.Transcript) == 0 {
				logging.Warn(
					ctx, "transcript.jsonl backfill: empty transcript in v1",
					slog.String("checkpoint_id", string(info.CheckpointID)),
					slog.Int("session_index", sessionIdx),
				)
			}
			continue
		}

		updateErr := v2Store.UpdateCommitted(ctx, checkpoint.UpdateCommittedOptions{
			CheckpointID:      info.CheckpointID,
			SessionID:         content.Metadata.SessionID,
			CompactTranscript: compacted,
		})
		if updateErr != nil {
			logging.Warn(
				ctx, "transcript.jsonl backfill: failed to write to v2",
				slog.String("checkpoint_id", string(info.CheckpointID)),
				slog.Int("session_index", sessionIdx),
				slog.String("error", updateErr.Error()),
			)
			continue
		}

		backfilled++
	}

	if backfilled == 0 {
		if lastAgent != "" {
			return 0, fmt.Errorf("%w: agent %q", errTranscriptNotGeneratable, lastAgent)
		}
		return 0, fmt.Errorf("%w: no agent type in metadata", errTranscriptNotGeneratable)
	}

	return backfilled, nil
}

func buildMigrateWriteOpts(content *checkpoint.SessionContent, info checkpoint.CommittedInfo, combinedAttribution *checkpoint.InitialAttribution) checkpoint.WriteCommittedOptions {
	m := content.Metadata

	prompts := checkpoint.SplitPromptContent(content.Prompts)

	return checkpoint.WriteCommittedOptions{
		CheckpointID: info.CheckpointID,
		SessionID:    m.SessionID,
		CreatedAt:    m.CreatedAt,
		Strategy:     m.Strategy,
		Branch:       m.Branch,
		// content.Transcript comes from persisted checkpoint storage and is
		// already redacted.
		Transcript:                  redact.AlreadyRedacted(content.Transcript),
		Prompts:                     prompts,
		FilesTouched:                m.FilesTouched,
		CheckpointsCount:            m.CheckpointsCount,
		Agent:                       m.Agent,
		Model:                       m.Model,
		TurnID:                      m.TurnID,
		TokenUsage:                  m.TokenUsage,
		SessionMetrics:              m.SessionMetrics,
		InitialAttribution:          m.InitialAttribution,
		PromptAttributionsJSON:      m.PromptAttributions,
		CombinedAttribution:         combinedAttribution,
		Summary:                     m.Summary,
		CheckpointTranscriptStart:   m.GetTranscriptStart(),
		TranscriptIdentifierAtStart: m.TranscriptIdentifierAtStart,
		IsTask:                      m.IsTask,
		ToolUseID:                   m.ToolUseID,
		AuthorName:                  migrateAuthorName,
		AuthorEmail:                 migrateAuthorEmail,
	}
}

func tryCompactTranscript(ctx context.Context, transcript []byte, m checkpoint.CommittedMetadata) []byte {
	return compactTranscriptForStartLine(ctx, transcript, m, 0)
}

func compactTranscriptForStartLine(ctx context.Context, transcript []byte, m checkpoint.CommittedMetadata, startLine int) []byte {
	if len(transcript) == 0 {
		return nil
	}
	if m.Agent == "" {
		logging.Warn(
			ctx, "compact transcript skipped: no agent type in checkpoint metadata",
			slog.String("checkpoint_id", string(m.CheckpointID)),
		)
		return nil
	}

	// transcript is read from persisted checkpoint storage and already redacted.
	compacted, err := compact.Compact(redact.AlreadyRedacted(transcript), compact.MetadataFields{
		Agent:      string(m.Agent),
		CLIVersion: versioninfo.Version,
		StartLine:  startLine,
	})
	if err != nil {
		logging.Warn(
			ctx, "compact transcript generation failed during migration",
			slog.String("checkpoint_id", string(m.CheckpointID)),
			slog.String("agent", string(m.Agent)),
			slog.String("error", err.Error()),
		)
		return nil
	}
	if len(compacted) == 0 {
		logging.Warn(
			ctx, "transcript.jsonl generation produced no output",
			slog.String("checkpoint_id", string(m.CheckpointID)),
			slog.String("agent", string(m.Agent)),
			slog.Int("input_bytes", len(transcript)),
		)
		return nil
	}
	return compacted
}

// computeCompactOffset determines the transcript.jsonl line offset for a checkpoint
// by comparing a full compact (startLine=0) against the scoped compact. The difference
// is the number of compact lines before this checkpoint's data.
func computeCompactOffset(ctx context.Context, fullTranscript, fullCompact []byte, m checkpoint.CommittedMetadata) int {
	startLine := m.GetTranscriptStart()
	if startLine == 0 || len(fullTranscript) == 0 || m.Agent == "" {
		return 0
	}

	if len(fullCompact) == 0 {
		return 0
	}

	// fullTranscript is read from persisted checkpoint storage and already redacted.
	scopedCompact, err := compact.Compact(redact.AlreadyRedacted(fullTranscript), compact.MetadataFields{
		Agent:      string(m.Agent),
		CLIVersion: versioninfo.Version,
		StartLine:  startLine,
	})
	if err != nil {
		logging.Warn(
			ctx, "compact transcript offset calculation failed during migration",
			slog.String("checkpoint_id", string(m.CheckpointID)),
			slog.String("agent", string(m.Agent)),
			slog.String("error", err.Error()),
		)
		return 0
	}
	if len(scopedCompact) == 0 {
		return 0
	}

	fullLines := bytes.Count(fullCompact, []byte{'\n'})
	scopedLines := bytes.Count(scopedCompact, []byte{'\n'})
	offset := fullLines - scopedLines
	if offset < 0 {
		logging.Warn(
			ctx, "compact transcript offset was negative during migration, defaulting to 0",
			slog.String("checkpoint_id", string(m.CheckpointID)),
			slog.Int("full_lines", fullLines),
			slog.Int("scoped_lines", scopedLines),
		)
		return 0
	}
	return offset
}

func collectTaskMetadataForMigratedFullGeneration(repo *git.Repository, cpID id.CheckpointID, summary *checkpoint.CheckpointSummary, v1ToV2SessionIdx map[int]int) (map[int][]plumbing.Hash, error) {
	rootTaskV2SessionIdx, attachRootTasks := latestMigratedV2SessionIndex(v1ToV2SessionIdx)
	return collectTaskMetadataForMigratedFullGenerationWithRootSession(repo, cpID, summary, v1ToV2SessionIdx, rootTaskV2SessionIdx, attachRootTasks)
}

func collectTaskMetadataForMigratedFullGenerationWithRootSession(
	repo *git.Repository,
	cpID id.CheckpointID,
	summary *checkpoint.CheckpointSummary,
	v1ToV2SessionIdx map[int]int,
	rootTaskV2SessionIdx int,
	attachRootTasks bool,
) (map[int][]plumbing.Hash, error) {
	v1Tree, err := resolveV1CheckpointTree(repo, cpID)
	if err != nil {
		return nil, err
	}

	taskTrees := make(map[int][]plumbing.Hash)

	// Legacy v1 layout stores task metadata at checkpoint root: <cp>/tasks/<tool-use-id>/...
	// Prefer attaching this tree to the latest session in v2.
	if rootTasksTree, rootTasksErr := v1Tree.Tree("tasks"); rootTasksErr == nil {
		if attachRootTasks {
			taskTrees[rootTaskV2SessionIdx] = append(taskTrees[rootTaskV2SessionIdx], rootTasksTree.Hash)
		}
	}

	for sessionIdx := range len(summary.Sessions) {
		sessionDir := strconv.Itoa(sessionIdx)
		sessionTree, sessionErr := v1Tree.Tree(sessionDir)
		if sessionErr != nil {
			continue
		}

		tasksTree, tasksErr := sessionTree.Tree("tasks")
		if tasksErr != nil {
			continue
		}

		v2SessionIdx, ok := v1ToV2SessionIdx[sessionIdx]
		if !ok {
			continue
		}
		taskTrees[v2SessionIdx] = append(taskTrees[v2SessionIdx], tasksTree.Hash)
	}

	return taskTrees, nil
}

func latestMigratedV2SessionIndex(v1ToV2SessionIdx map[int]int) (int, bool) {
	latest := -1
	for _, v2SessionIdx := range v1ToV2SessionIdx {
		if v2SessionIdx > latest {
			latest = v2SessionIdx
		}
	}
	if latest < 0 {
		return -1, false
	}
	return latest, true
}

// resolveV1CheckpointTree reads the checkpoint subtree from the v1 branch.
func resolveV1CheckpointTree(repo *git.Repository, cpID id.CheckpointID) (*object.Tree, error) {
	refName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	ref, err := repo.Reference(refName, true)
	if err != nil {
		// Try remote tracking branch
		remoteRefName := plumbing.NewRemoteReferenceName(migrateRemoteName, paths.MetadataBranchName)
		ref, err = repo.Reference(remoteRefName, true)
		if err != nil {
			return nil, fmt.Errorf("v1 branch not found: %w", err)
		}
	}

	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get v1 commit: %w", err)
	}

	rootTree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get v1 tree: %w", err)
	}

	cpTree, err := rootTree.Tree(cpID.Path())
	if err != nil {
		return nil, fmt.Errorf("checkpoint %s not found in v1 tree: %w", cpID, err)
	}

	return cpTree, nil
}

// cleanupV1TranscriptFiles removes legacy v1-named transcript files (full.jsonl,
// full.jsonl.*, content_hash.txt) from /full/current. Older CLI versions wrote
// these before the rename to raw_transcript; they are inert but waste space.
// Best-effort: failures are logged and do not block migration.
func cleanupV1TranscriptFiles(ctx context.Context, _ *git.Repository, v2Store *checkpoint.V2GitStore, cpID id.CheckpointID, sessionCount int) {
	if err := v2Store.CleanupV1TranscriptFiles(ctx, cpID, sessionCount); err != nil {
		logging.Warn(
			ctx, "v1 transcript cleanup failed",
			slog.String("checkpoint_id", string(cpID)),
			slog.String("error", err.Error()),
		)
	}
}
