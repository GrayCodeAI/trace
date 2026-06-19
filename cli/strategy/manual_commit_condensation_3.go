package strategy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/GrayCodeAI/trace/cli/agent"
	cpkg "github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/checkpoint/remote"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cli/transcript/compact"
	"github.com/GrayCodeAI/trace/cli/versioninfo"
	"github.com/GrayCodeAI/trace/redact"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

// computeCompactTranscriptStart chooses the compact transcript start line offset
// for v2 /main metadata.
//
// Preferred source is session state CompactTranscriptStart. For legacy sessions
// that have only full-transcript offsets persisted, this recalculates the compact
// offset from transcript bytes when possible. On any failure, returns 0 (fail-open).
func computeCompactTranscriptStart(ctx context.Context, ag agent.Agent, state *SessionState, transcript []byte, scopedCompact []byte) int {
	if state.CompactTranscriptStart > 0 {
		return state.CompactTranscriptStart
	}
	if state.CheckpointTranscriptStart == 0 || ag == nil || len(transcript) == 0 || len(scopedCompact) == 0 {
		return 0
	}

	// transcript is already redacted (passed as .Bytes() from RedactedBytes).
	fullCompacted, err := compact.Compact(redact.AlreadyRedacted(transcript), compact.MetadataFields{
		Agent:      string(ag.Name()),
		CLIVersion: versioninfo.Version,
		StartLine:  0,
	})
	if err != nil || len(fullCompacted) == 0 {
		logging.Warn(
			ctx, "failed to recalculate compact transcript start, using 0",
			slog.String("session_id", state.SessionID),
		)
		return 0
	}

	fullLines := countCompactLines(fullCompacted)
	scopedLines := countCompactLines(scopedCompact)
	offset := fullLines - scopedLines
	if offset < 0 {
		return 0
	}
	return offset
}

// writeCommittedV2 writes checkpoint data to v2 refs unconditionally.
// Callers decide whether to propagate or swallow the error (v2-only vs dual-write).
func writeCommittedV2(ctx context.Context, repo *git.Repository, opts cpkg.WriteCommittedOptions) error {
	v2URL, err := remote.FetchURL(ctx)
	if err != nil {
		logging.Debug(
			ctx, "manual-commit condensation: using origin for v2 write fetch remote",
			slog.String("error", err.Error()),
		)
		v2URL = originRemote
	}
	v2Store := cpkg.NewV2GitStore(repo, v2URL)
	if err := v2Store.WriteCommitted(ctx, opts); err != nil {
		return fmt.Errorf("v2 write committed: %w", err)
	}
	return nil
}

// writeCommittedV2IfEnabled writes checkpoint data to v2 refs when checkpoints_v2
// is enabled. Failures are logged as warnings — in dual-write mode v2 writes are
// best-effort and must not block the v1 path.
func writeCommittedV2IfEnabled(ctx context.Context, repo *git.Repository, opts cpkg.WriteCommittedOptions) {
	if !settings.IsCheckpointsV2Enabled(ctx) {
		return
	}
	if err := writeCommittedV2(ctx, repo, opts); err != nil {
		logging.Warn(
			ctx, "v2 dual-write failed",
			slog.String("checkpoint_id", opts.CheckpointID.String()),
			slog.String("error", err.Error()),
		)
	}
}

// writeTaskMetadataV2IfEnabled copies task metadata trees from the shadow branch
// to v2 /full/current when dual-write is enabled.
//
// This mirrors migrate's task backfill behavior for newly created checkpoints so
// task rewind artifacts (tasks/<tool-use-id>/...) are available in v2 immediately,
// not only after running `trace migrate --checkpoints v2`.
func writeTaskMetadataV2IfEnabled(
	ctx context.Context,
	repo *git.Repository,
	checkpointID id.CheckpointID,
	sessionID string,
	shadowRef *plumbing.Reference,
) {
	if !settings.IsCheckpointsV2Enabled(ctx) || shadowRef == nil {
		return
	}

	shadowCommit, err := repo.CommitObject(shadowRef.Hash())
	if err != nil {
		logging.Warn(
			ctx, "v2 dual-write task metadata copy skipped: failed to read shadow commit",
			slog.String("checkpoint_id", checkpointID.String()),
			slog.String("session_id", sessionID),
			slog.String("error", err.Error()),
		)
		return
	}

	shadowTree, err := shadowCommit.Tree()
	if err != nil {
		logging.Warn(
			ctx, "v2 dual-write task metadata copy skipped: failed to read shadow tree",
			slog.String("checkpoint_id", checkpointID.String()),
			slog.String("session_id", sessionID),
			slog.String("error", err.Error()),
		)
		return
	}

	tasksPath := paths.SessionMetadataDirFromSessionID(sessionID) + "/tasks"
	tasksTree, err := shadowTree.Tree(tasksPath)
	if err != nil {
		return
	}

	v2URL, err := remote.FetchURL(ctx)
	if err != nil {
		logging.Debug(
			ctx, "manual-commit condensation: using origin for v2 task metadata fetch remote",
			slog.String("error", err.Error()),
		)
		v2URL = originRemote
	}
	v2Store := cpkg.NewV2GitStore(repo, v2URL)
	sessionIndex, err := resolveV2SessionIndexForCheckpoint(repo, checkpointID, sessionID)
	if err != nil {
		logging.Warn(
			ctx, "v2 dual-write task metadata copy skipped: failed to resolve session index",
			slog.String("checkpoint_id", checkpointID.String()),
			slog.String("session_id", sessionID),
			slog.String("error", err.Error()),
		)
		return
	}

	if err := spliceTaskTreeToV2FullCurrent(ctx, repo, v2Store, checkpointID, sessionIndex, tasksTree.Hash); err != nil {
		logging.Warn(
			ctx, "v2 dual-write task metadata copy failed",
			slog.String("checkpoint_id", checkpointID.String()),
			slog.String("session_id", sessionID),
			slog.String("error", err.Error()),
		)
	}
}

func resolveV2SessionIndexForCheckpoint(repo *git.Repository, checkpointID id.CheckpointID, sessionID string) (int, error) {
	v2MainRef, err := repo.Reference(plumbing.ReferenceName(paths.V2MainRefName), true)
	if err != nil {
		return 0, fmt.Errorf("read v2 /main ref: %w", err)
	}
	v2MainCommit, err := repo.CommitObject(v2MainRef.Hash())
	if err != nil {
		return 0, fmt.Errorf("read v2 /main commit: %w", err)
	}
	v2MainTree, err := v2MainCommit.Tree()
	if err != nil {
		return 0, fmt.Errorf("read v2 /main tree: %w", err)
	}

	checkpointTree, err := v2MainTree.Tree(checkpointID.Path())
	if err != nil {
		return 0, fmt.Errorf("read checkpoint subtree on v2 /main: %w", err)
	}

	metadataFile, err := checkpointTree.File(paths.MetadataFileName)
	if err != nil {
		return 0, fmt.Errorf("read checkpoint summary metadata: %w", err)
	}
	metadataContent, err := metadataFile.Contents()
	if err != nil {
		return 0, fmt.Errorf("read checkpoint summary contents: %w", err)
	}

	var summary cpkg.CheckpointSummary
	if err := json.Unmarshal([]byte(metadataContent), &summary); err != nil {
		return 0, fmt.Errorf("parse checkpoint summary metadata: %w", err)
	}

	for i := range len(summary.Sessions) {
		sessionTree, err := checkpointTree.Tree(strconv.Itoa(i))
		if err != nil {
			continue
		}
		sessionMetadataFile, err := sessionTree.File(paths.MetadataFileName)
		if err != nil {
			continue
		}
		sessionMetadataContent, err := sessionMetadataFile.Contents()
		if err != nil {
			continue
		}

		var sessionMeta cpkg.CommittedMetadata
		if err := json.Unmarshal([]byte(sessionMetadataContent), &sessionMeta); err != nil {
			continue
		}
		if sessionMeta.SessionID == sessionID {
			return i, nil
		}
	}

	return 0, fmt.Errorf("session %q not found in v2 checkpoint %s", sessionID, checkpointID)
}

func spliceTaskTreeToV2FullCurrent(
	ctx context.Context,
	repo *git.Repository,
	v2Store *cpkg.V2GitStore,
	checkpointID id.CheckpointID,
	sessionIndex int,
	tasksTreeHash plumbing.Hash,
) error {
	refName := plumbing.ReferenceName(paths.V2FullCurrentRefName)
	parentHash, rootTreeHash, err := v2Store.GetRefState(refName)
	if err != nil {
		return fmt.Errorf("get v2 /full/current ref state: %w", err)
	}
	incomingTasksTree, err := repo.TreeObject(tasksTreeHash)
	if err != nil {
		return fmt.Errorf("read task tree: %w", err)
	}

	shardPrefix := string(checkpointID[:2])
	shardSuffix := string(checkpointID[2:])
	sessionDir := strconv.Itoa(sessionIndex)

	newRootHash, err := cpkg.UpdateSubtree(
		repo, rootTreeHash,
		[]string{shardPrefix, shardSuffix, sessionDir, "tasks"},
		incomingTasksTree.Entries,
		cpkg.UpdateSubtreeOptions{MergeMode: cpkg.MergeKeepExisting},
	)
	if err != nil {
		return fmt.Errorf("splice task tree into v2 /full/current: %w", err)
	}

	authorName, authorEmail := cpkg.GetGitAuthorFromRepo(repo)
	commitHash, err := cpkg.CreateCommit(ctx, repo, newRootHash, parentHash,
		fmt.Sprintf("Checkpoint: %s (task metadata)\n", checkpointID),
		authorName, authorEmail)
	if err != nil {
		return fmt.Errorf("create v2 task metadata commit: %w", err)
	}

	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, commitHash)); err != nil {
		return fmt.Errorf("update v2 /full/current ref: %w", err)
	}

	return nil
}
