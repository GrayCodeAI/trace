package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/transcript/compact"
	"github.com/GrayCodeAI/trace/cli/versioninfo"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateCheckpointsV2_TaskMetadataKeepsFirstConflictingTaskTree(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("8899aabbccdd")
	toolUseID := "toolu_conflict"
	err := v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-conflict",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte("{\"type\":\"assistant\",\"message\":\"conflict\"}\n")),
		Prompts:      []string{"conflict prompt"},
		IsTask:       true,
		ToolUseID:    toolUseID,
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)
	addV1RootTasksTreeWithContent(t, repo, cpID, toolUseID, `{"source":"root"}`)
	addV1SessionTasksTreeWithContent(t, repo, cpID, 0, toolUseID, `{"source":"session"}`)

	var stdout bytes.Buffer
	result, migrateErr := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, migrateErr)
	assert.Equal(t, 1, result.migrated)

	rootTree := v2FullTreeForCheckpoint(t, repo, v2Store, cpID)
	file, err := rootTree.File(cpID.Path() + "/0/tasks/" + toolUseID + "/checkpoint.json")
	require.NoError(t, err)
	content, err := file.Contents()
	require.NoError(t, err)
	assert.JSONEq(t, `{"source":"root"}`, content)
}

func TestMigrateCheckpointsV2_PartialRepairDoesNotMoveRootTaskMetadataToMissingSession(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("99aabbccddee")
	rootToolUseID := "toolu_root_partial"
	err := v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-old",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte("{\"type\":\"assistant\",\"message\":\"old\"}\n")),
		Prompts:      []string{"old prompt"},
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)
	err = v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-latest",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte("{\"type\":\"assistant\",\"message\":\"latest\"}\n")),
		Prompts:      []string{"latest prompt"},
		IsTask:       true,
		ToolUseID:    rootToolUseID,
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)
	addV1RootTasksTreeWithContent(t, repo, cpID, rootToolUseID, `{"source":"root"}`)

	var initialRun bytes.Buffer
	result1, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &initialRun, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result1.migrated)
	assert.True(t, v2FullFileExistsForCheckpoint(t, repo, v2Store, cpID, "1/tasks/"+rootToolUseID+"/checkpoint.json"))

	removeV2SessionTranscriptFiles(t, repo, v2Store, cpID, 0)

	var rerun bytes.Buffer
	result2, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &rerun, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result2.migrated)
	assert.Equal(t, 1, result2.repaired)
	assert.False(t, v2FullFileExistsForCheckpoint(t, repo, v2Store, cpID, "0/tasks/"+rootToolUseID+"/checkpoint.json"),
		"partial repair must not attach root task metadata to the older missing session")
	assert.True(t, v2FullFileExistsForCheckpoint(t, repo, v2Store, cpID, "1/tasks/"+rootToolUseID+"/checkpoint.json"),
		"root task metadata should stay attached to the latest v2 session")
}

func TestMigrateCheckpointsV2_SkipsCheckpointWhenAllV1SessionsMissingTranscript(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("5566778899bb")
	err := v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "metadata-only-session",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted(nil),
		Prompts:      []string{"metadata-only prompt"},
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	var stdout bytes.Buffer
	result, migrateErr := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, migrateErr)
	assert.Equal(t, 0, result.migrated)
	assert.Equal(t, 1, result.skipped)
	assert.Equal(t, 0, result.failed)
	assert.Equal(t, 1, result.missingSessions)

	output := stdout.String()
	assert.NotContains(t, output, "warning: skipping v1 session 0")
	assert.NotContains(t, output, "skipped (no migratable v1 sessions")

	summary, readErr := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, readErr)
	assert.Nil(t, summary)
}

func TestMigrateCheckpointsV2_ForcePrunesSkippedV2Sessions(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("778899aabbcc")
	writeV1Checkpoint(
		t, v1Store, cpID, "session-keep",
		[]byte("{\"type\":\"assistant\",\"message\":\"keep\"}\n"),
		[]string{"keep prompt"},
	)
	writeV1Checkpoint(
		t, v1Store, cpID, "session-stale",
		[]byte("{\"type\":\"assistant\",\"message\":\"stale\"}\n"),
		[]string{"stale prompt"},
	)

	var initialRun bytes.Buffer
	result1, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &initialRun, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result1.migrated)

	initialSummary, readErr := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, readErr)
	require.NotNil(t, initialSummary)
	require.Len(t, initialSummary.Sessions, 2)

	err = v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-stale",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted(nil),
		Prompts:      []string{"metadata-only stale prompt"},
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	var stdout bytes.Buffer
	result2, rerunErr := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, true)
	require.NoError(t, rerunErr)
	assert.Equal(t, 1, result2.migrated)
	assert.Equal(t, 0, result2.skipped)
	assert.Equal(t, 1, result2.missingSessions)
	assert.NotContains(t, stdout.String(), "warning: skipping v1 session 1")

	summary, readErr := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, readErr)
	require.NotNil(t, summary)
	require.Len(t, summary.Sessions, 1)
	assert.Equal(t, "/"+cpID.Path()+"/0/metadata.json", summary.Sessions[0].Metadata)

	_, rootTreeHash, refErr := v2Store.GetRefState(plumbing.ReferenceName(paths.V2FullCurrentRefName))
	require.NoError(t, refErr)
	rootTree, treeErr := repo.TreeObject(rootTreeHash)
	require.NoError(t, treeErr)
	_, err = rootTree.File(cpID.Path() + "/1/" + paths.V2RawTranscriptHashFileName)
	require.Error(t, err, "force migration should remove stale full transcript data for skipped sessions")
}

func TestMigrateCheckpointsV2_ForcePruneRemovesEmptyShardWhenAllSessionsSkipped(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("8899aabbccdd")
	writeV1Checkpoint(
		t, v1Store, cpID, "session-stale-only",
		[]byte("{\"type\":\"assistant\",\"message\":\"stale only\"}\n"),
		[]string{"stale prompt"},
	)

	var initialRun bytes.Buffer
	result1, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &initialRun, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result1.migrated)

	err = v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-stale-only",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted(nil),
		Prompts:      []string{"metadata-only stale prompt"},
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	var stdout bytes.Buffer
	result2, rerunErr := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, true)
	require.NoError(t, rerunErr)
	assert.Equal(t, 0, result2.migrated)
	assert.Equal(t, 1, result2.skipped)
	assert.Equal(t, 1, result2.missingSessions)
	assert.NotContains(t, stdout.String(), "no migratable v1 sessions")

	summary, readErr := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, readErr)
	assert.Nil(t, summary)

	assertNoV2ShardPrefix(t, repo, v2Store, plumbing.ReferenceName(paths.V2MainRefName), cpID)
	assertNoV2ShardPrefix(t, repo, v2Store, plumbing.ReferenceName(paths.V2FullCurrentRefName), cpID)
}

func assertNoV2ShardPrefix(t *testing.T, repo *git.Repository, v2Store *checkpoint.V2GitStore, refName plumbing.ReferenceName, cpID id.CheckpointID) {
	t.Helper()

	_, rootTreeHash, err := v2Store.GetRefState(refName)
	require.NoError(t, err)

	rootTree, err := repo.TreeObject(rootTreeHash)
	require.NoError(t, err)

	_, err = rootTree.Tree(string(cpID[:2]))
	require.Error(t, err, "force prune should remove an empty shard prefix from %s", refName)
}

func appendMissingV1SessionReference(t *testing.T, repo *git.Repository, v1Store *checkpoint.GitStore, cpID id.CheckpointID) {
	t.Helper()

	ctx := context.Background()
	summary, err := v1Store.ReadCommitted(ctx, cpID)
	require.NoError(t, err)
	require.NotNil(t, summary)

	missingIndex := len(summary.Sessions)
	missingBase := "/" + cpID.Path() + "/" + strconv.Itoa(missingIndex) + "/"
	summary.Sessions = append(summary.Sessions, checkpoint.SessionFilePaths{
		Metadata:    missingBase + paths.MetadataFileName,
		Transcript:  missingBase + paths.TranscriptFileName,
		ContentHash: missingBase + paths.ContentHashFileName,
		Prompt:      missingBase + paths.PromptFileName,
	})

	metadataJSON, err := json.MarshalIndent(summary, "", "  ")
	require.NoError(t, err)
	metadataJSON = append(metadataJSON, '\n')

	metadataHash, err := checkpoint.CreateBlobFromContent(repo, metadataJSON)
	require.NoError(t, err)

	refName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	ref, err := repo.Reference(refName, true)
	require.NoError(t, err)
	commit, err := repo.CommitObject(ref.Hash())
	require.NoError(t, err)

	newTreeHash, err := checkpoint.UpdateSubtree(
		repo,
		commit.TreeHash,
		[]string{string(cpID[:2]), string(cpID[2:])},
		[]object.TreeEntry{{
			Name: paths.MetadataFileName,
			Mode: filemode.Regular,
			Hash: metadataHash,
		}},
		checkpoint.UpdateSubtreeOptions{MergeMode: checkpoint.MergeKeepExisting},
	)
	require.NoError(t, err)

	newCommitHash, err := checkpoint.CreateCommit(ctx, repo, newTreeHash, ref.Hash(), "test: stale v1 session reference\n", "Test", "test@test.com")
	require.NoError(t, err)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(refName, newCommitHash)))
}

func TestMigrateCheckpointsV2_NoV1Branch(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)
	var stdout bytes.Buffer

	// No v1 data written — ListCommitted returns empty
	result, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, err)
	assert.Equal(t, 0, result.migrated)
	assert.Empty(t, stdout.String())
}

func TestMigrateCmd_InvalidFlag(t *testing.T) {
	t.Parallel()
	cmd := newMigrateCmd()
	cmd.SetArgs([]string{"--checkpoints", "v3"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported checkpoints version")
}

func TestMigrateCheckpointsV2_CompactionSkipped(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("e5f6a1b2c3d4")
	// Write checkpoint with no agent type — compaction will be skipped
	err := v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-noagent",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte("{\"type\":\"assistant\",\"message\":\"no agent\"}\n")),
		Prompts:      []string{"compact fail prompt"},
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	var stdout bytes.Buffer

	result, migrateErr := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, migrateErr)
	assert.Equal(t, 1, result.migrated)
	assert.Equal(t, 1, result.compactTranscriptSkipped)
	assert.Empty(t, stdout.String())
}

func TestMigrateCheckpointsV2_TaskCheckpoint(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("b2c3d4e5f6a1")
	err := v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-task-001",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte("{\"type\":\"assistant\",\"message\":\"task work\"}\n")),
		Prompts:      []string{"task prompt"},
		IsTask:       true,
		ToolUseID:    "toolu_01ABC",
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	var stdout bytes.Buffer

	result, migrateErr := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, migrateErr)
	assert.Equal(t, 1, result.migrated)

	// Verify task checkpoint exists in v2
	summary, readErr := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, readErr)
	require.NotNil(t, summary)

	// Verify task metadata tree was copied into the migrated v2 /full/* generation.
	rootTree := v2FullTreeForCheckpoint(t, repo, v2Store, cpID)
	_, taskFileErr := rootTree.File(cpID.Path() + "/0/tasks/toolu_01ABC/checkpoint.json")
	require.NoError(t, taskFileErr, "expected migrated task checkpoint metadata in /full/*")
}

func TestMigrateCheckpointsV2_AllSkippedOnRerun(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID1 := id.MustCheckpointID("f6a1b2c3d4e5")
	cpID2 := id.MustCheckpointID("a1b2c3d4e5f7")

	writeV1Checkpoint(
		t, v1Store, cpID1, "session-p1",
		[]byte("{\"type\":\"assistant\",\"message\":\"first\"}\n"),
		[]string{"prompt 1"},
	)
	writeV1Checkpoint(
		t, v1Store, cpID2, "session-p2",
		[]byte("{\"type\":\"assistant\",\"message\":\"second\"}\n"),
		[]string{"prompt 2"},
	)

	// First run: migrates both
	var discard bytes.Buffer
	result1, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &discard, false)
	require.NoError(t, err)
	assert.Equal(t, 2, result1.migrated)

	// Second run: skips both
	var stdout bytes.Buffer
	result2, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, err)
	assert.Equal(t, 0, result2.migrated)
	assert.Equal(t, 2, result2.skipped)
}

func TestMigrateCheckpointsV2_BackfillCompactTranscript(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("aabb11223344")

	// Write v1 checkpoint with agent type (so compaction can succeed)
	err := v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-backfill",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"hello\"}}\n{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"hi\"}]}}\n")),
		Prompts:      []string{"hello"},
		Agent:        "Claude Code",
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	// Write to v2 WITHOUT compact transcript (simulating earlier migration)
	err = v2Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-backfill",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"hello\"}}\n")),
		Prompts:      []string{"hello"},
		Agent:        "Claude Code",
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
		// CompactTranscript intentionally nil
	})
	require.NoError(t, err)

	// Verify no transcript.jsonl on /main yet
	summary, err := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.Empty(t, summary.Sessions[0].Transcript, "should have no compact transcript before backfill")

	// Run migration — should backfill the compact transcript
	var stdout bytes.Buffer
	result, migrateErr := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, migrateErr)
	assert.Equal(t, 1, result.migrated, "backfill should count as migrated")
	assert.Equal(t, 0, result.skipped)
	assert.Equal(t, 1, result.backfilledCompactTranscripts)
	assert.Empty(t, stdout.String())

	// Verify transcript.jsonl now exists
	summary2, err := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, err)
	require.NotNil(t, summary2)
	assert.NotEmpty(t, summary2.Sessions[0].Transcript, "should have compact transcript after backfill")
}

func TestMigrateCheckpointsV2_UsesComputedCompactTranscriptStart(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)
	ctx := context.Background()

	cpID := id.MustCheckpointID("5566778899aa")
	transcript := []byte(
		"{\"type\":\"human\",\"message\":{\"content\":\"prompt 1\"}}\n" +
			"{\"type\":\"assistant\",\"message\":{\"content\":\"reply 1\"}}\n" +
			"{\"type\":\"human\",\"message\":{\"content\":\"prompt 2\"}}\n" +
			"{\"type\":\"assistant\",\"message\":{\"content\":\"reply 2\"}}\n",
	)
	err := v1Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID:              cpID,
		SessionID:                 "session-compact-start-migrate",
		Strategy:                  "manual-commit",
		Transcript:                redact.AlreadyRedacted(transcript),
		Prompts:                   []string{"prompt 2"},
		Agent:                     agent.AgentTypeClaudeCode,
		CheckpointTranscriptStart: 2, // full transcript line domain
		AuthorName:                "Test",
		AuthorEmail:               "test@test.com",
	})
	require.NoError(t, err)

	v1Content, err := v1Store.ReadSessionContent(ctx, cpID, 0)
	require.NoError(t, err)
	fullCompacted := tryCompactTranscript(ctx, v1Content.Transcript, v1Content.Metadata)
	require.NotNil(t, fullCompacted)
	scopedCompacted, err := compact.Compact(redact.AlreadyRedacted(v1Content.Transcript), compact.MetadataFields{
		Agent:      string(v1Content.Metadata.Agent),
		CLIVersion: versioninfo.Version,
		StartLine:  v1Content.Metadata.GetTranscriptStart(),
	})
	require.NoError(t, err)
	require.NotNil(t, scopedCompacted)
	require.Greater(t, bytes.Count(fullCompacted, []byte{'\n'}), bytes.Count(scopedCompacted, []byte{'\n'}))
	expectedOffset := computeCompactOffset(ctx, v1Content.Transcript, fullCompacted, v1Content.Metadata)
	require.Positive(t, expectedOffset, "expected non-zero compact transcript start")

	var stdout bytes.Buffer
	result, migrateErr := migrateCheckpointsV2(ctx, repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, migrateErr)
	assert.Equal(t, 1, result.migrated)

	v2MainRef, err := repo.Reference(plumbing.ReferenceName(paths.V2MainRefName), true)
	require.NoError(t, err)
	v2MainCommit, err := repo.CommitObject(v2MainRef.Hash())
	require.NoError(t, err)
	v2MainTree, err := v2MainCommit.Tree()
	require.NoError(t, err)

	metadataFile, err := v2MainTree.File(cpID.Path() + "/0/" + paths.MetadataFileName)
	require.NoError(t, err)
	metadataContent, err := metadataFile.Contents()
	require.NoError(t, err)

	var metadata checkpoint.CommittedMetadata
	require.NoError(t, json.Unmarshal([]byte(metadataContent), &metadata))
	assert.Equal(t, expectedOffset, metadata.CheckpointTranscriptStart)

	storedCompact, err := v2Store.ReadSessionCompactTranscript(ctx, cpID, 0)
	require.NoError(t, err)
	assert.Equal(t, fullCompacted, storedCompact, "migration should persist cumulative compact transcript")
}

func TestMigrateCheckpointsV2_RepairsMissingFullTranscriptBeforeBackfill(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("112233aabbcc")
	writeV1Checkpoint(
		t, v1Store, cpID, "session-repair-001",
		[]byte("{\"type\":\"assistant\",\"message\":\"repair me\"}\n"),
		[]string{"repair prompt"},
	)

	// Initial migration to create v2 state.
	var initialRun bytes.Buffer
	result1, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &initialRun, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result1.migrated)

	// Simulate interrupted migration by removing raw transcript files from every /full/* ref.
	removeV2SessionTranscriptFiles(t, repo, v2Store, cpID, 0)

	// Re-run migration: should requeue the missing raw transcript for final
	// generation packing and count as migrated (not skipped).
	var rerun bytes.Buffer
	result2, rerunErr := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &rerun, false)
	require.NoError(t, rerunErr)
	assert.Equal(t, 1, result2.migrated)
	assert.Equal(t, 0, result2.failed)
	assert.Equal(t, 1, result2.repaired)
	assert.Empty(t, rerun.String())

	content, readErr := v2Store.ReadSessionContent(context.Background(), cpID, 0)
	require.NoError(t, readErr)
	assert.NotEmpty(t, content.Transcript, "raw full transcript should be restored in a packed /full/* generation")
	assert.False(t, hasCurrentFullSessionArtifactsForTest(t, repo, v2Store, cpID, 0),
		"rerun repair must not rehydrate migrated raw transcripts into /full/current")
}

func TestMigrateCheckpointsV2_SkipsRepairWhenArchivedFullExists(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("334455ddeeff")
	writeV1Checkpoint(
		t, v1Store, cpID, "session-repair-archive-001",
		[]byte("{\"type\":\"assistant\",\"message\":\"repair from archive fallback\"}\n"),
		[]string{"repair archive prompt"},
	)

	// Initial migration to seed v2.
	var initialRun bytes.Buffer
	result1, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &initialRun, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result1.migrated)

	// Fresh migration packs raw transcripts into an archived generation and
	// leaves /full/current empty.
	archivedRead, archivedReadErr := v2Store.ReadSessionContent(context.Background(), cpID, 0)
	require.NoError(t, archivedReadErr)
	assert.NotEmpty(t, archivedRead.Transcript)

	// Re-run migration: archived /full/* artifacts are sufficient, so it should
	// not rehydrate old raw transcripts into /full/current.
	var rerun bytes.Buffer
	result2, rerunErr := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &rerun, false)
	require.NoError(t, rerunErr)
	assert.Equal(t, 0, result2.migrated)
	assert.Equal(t, 1, result2.skipped)
	assert.NotContains(t, rerun.String(), "repaired partial v2 checkpoint state")

	ok, checkErr := hasFullSessionArtifacts(v2Store, cpID, 0)
	require.NoError(t, checkErr)
	assert.True(t, ok, "expected archived /full/* artifacts to count as present")
	assert.False(t, hasCurrentFullSessionArtifactsForTest(t, repo, v2Store, cpID, 0),
		"migration rerun must not copy archived artifacts back into /full/current")
}

func removeV2SessionTranscriptFiles(t *testing.T, repo *git.Repository, v2Store *checkpoint.V2GitStore, cpID id.CheckpointID, sessionIdx int) {
	t.Helper()

	for _, refName := range v2FullRefSearchOrderForTest(t, v2Store) {
		removeV2SessionTranscriptFilesFromRef(t, repo, v2Store, refName, cpID, sessionIdx)
	}
}

func removeV2SessionTranscriptFilesFromRef(t *testing.T, repo *git.Repository, v2Store *checkpoint.V2GitStore, refName plumbing.ReferenceName, cpID id.CheckpointID, sessionIdx int) {
	t.Helper()

	parentHash, rootTreeHash, err := v2Store.GetRefState(refName)
	if err != nil {
		return
	}

	newRootHash, updateErr := checkpoint.UpdateSubtree(
		repo,
		rootTreeHash,
		[]string{string(cpID[:2]), string(cpID[2:]), strconv.Itoa(sessionIdx)},
		nil,
		checkpoint.UpdateSubtreeOptions{
			MergeMode: checkpoint.MergeKeepExisting,
			DeleteNames: []string{
				paths.V2RawTranscriptFileName,
				paths.V2RawTranscriptFileName + ".001",
				paths.V2RawTranscriptFileName + ".002",
				paths.V2RawTranscriptHashFileName,
			},
		},
	)
	require.NoError(t, updateErr)
	if newRootHash == rootTreeHash {
		return
	}

	commitHash, commitErr := checkpoint.CreateCommit(context.Background(), repo, newRootHash, parentHash, "test: remove full transcript\n", "Test", "test@test.com")
	require.NoError(t, commitErr)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(refName, commitHash)))
}

func v2FullTreeForCheckpoint(t *testing.T, repo *git.Repository, v2Store *checkpoint.V2GitStore, cpID id.CheckpointID) *object.Tree {
	t.Helper()

	for _, refName := range v2FullRefSearchOrderForTest(t, v2Store) {
		_, rootTreeHash, err := v2Store.GetRefState(refName)
		if err != nil {
			continue
		}
		rootTree, err := repo.TreeObject(rootTreeHash)
		require.NoError(t, err)
		if _, treeErr := rootTree.Tree(cpID.Path()); treeErr == nil {
			return rootTree
		}
	}

	t.Fatalf("checkpoint %s not found in any v2 /full/* ref", cpID)
	return nil
}

func v2FullFileExistsForCheckpoint(t *testing.T, repo *git.Repository, v2Store *checkpoint.V2GitStore, cpID id.CheckpointID, relPath string) bool {
	t.Helper()

	for _, refName := range v2FullRefSearchOrderForTest(t, v2Store) {
		_, rootTreeHash, err := v2Store.GetRefState(refName)
		if err != nil {
			continue
		}
		rootTree, err := repo.TreeObject(rootTreeHash)
		require.NoError(t, err)
		if _, err := rootTree.File(cpID.Path() + "/" + relPath); err == nil {
			return true
		}
	}

	return false
}

func v2FullRefSearchOrderForTest(t *testing.T, v2Store *checkpoint.V2GitStore) []plumbing.ReferenceName {
	t.Helper()

	refNames := []plumbing.ReferenceName{plumbing.ReferenceName(paths.V2FullCurrentRefName)}
	archived, err := v2Store.ListArchivedGenerations()
	require.NoError(t, err)
	for i := len(archived) - 1; i >= 0; i-- {
		refNames = append(refNames, plumbing.ReferenceName(paths.V2FullRefPrefix+archived[i]))
	}
	return refNames
}

func hasCurrentFullSessionArtifactsForTest(t *testing.T, repo *git.Repository, v2Store *checkpoint.V2GitStore, cpID id.CheckpointID, sessionIdx int) bool {
	t.Helper()

	_, rootTreeHash, err := v2Store.GetRefState(plumbing.ReferenceName(paths.V2FullCurrentRefName))
	require.NoError(t, err)

	rootTree, err := repo.TreeObject(rootTreeHash)
	require.NoError(t, err)

	sessionPath := cpID.Path() + "/" + strconv.Itoa(sessionIdx)
	sessionTree, err := rootTree.Tree(sessionPath)
	if err != nil {
		return false
	}

	hasTranscript := false
	for _, entry := range sessionTree.Entries {
		if entry.Name == paths.V2RawTranscriptFileName || strings.HasPrefix(entry.Name, paths.V2RawTranscriptFileName+".") {
			hasTranscript = true
			break
		}
	}
	if !hasTranscript {
		return false
	}

	_, err = sessionTree.File(paths.V2RawTranscriptHashFileName)
	return err == nil
}

func TestBuildMigrateWriteOpts_PromptSeparatorRoundTrip(t *testing.T) {
	t.Parallel()

	cpID := id.MustCheckpointID("123456abcdef")
	rawPrompts := strings.Join([]string{
		"first line\nwith newline",
		"second prompt",
	}, checkpoint.PromptSeparator)

	opts := buildMigrateWriteOpts(&checkpoint.SessionContent{
		Metadata: checkpoint.CommittedMetadata{
			SessionID: "session-prompts-001",
			Strategy:  "manual-commit",
		},
		Prompts: rawPrompts,
	}, checkpoint.CommittedInfo{
		CheckpointID: cpID,
	}, nil)

	require.Len(t, opts.Prompts, 2)
	assert.Equal(t, "first line\nwith newline", opts.Prompts[0])
	assert.Equal(t, "second prompt", opts.Prompts[1])
}

func TestLatestMigratedV2SessionIndex_Empty(t *testing.T) {
	t.Parallel()

	latest, ok := latestMigratedV2SessionIndex(nil)
	assert.Equal(t, -1, latest)
	assert.False(t, ok)
}
