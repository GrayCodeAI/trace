package checkpoint

import (
	"context"
	"fmt"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

func TestV2GitStore_UpdateCommitted_CheckpointNotFound(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewV2GitStore(repo, "origin")
	ctx := context.Background()

	cpID := id.MustCheckpointID("bb44cc55dd66")

	// Update without prior write should return error
	err := store.UpdateCommitted(ctx, UpdateCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "nonexistent",
		Transcript:   redact.AlreadyRedacted([]byte(`{"type":"assistant","message":"hello"}`)),
		Agent:        agent.AgentTypeClaudeCode,
	})
	require.Error(t, err)
}

func TestV2GitStore_UpdateCommitted_PreservesExistingTaskMetadataInFullCurrent(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewV2GitStore(repo, "origin")
	ctx := context.Background()

	cpID := id.MustCheckpointID("cc55dd66ee77")

	// Initial write creates checkpoint/session on both /main and /full/current.
	err := store.WriteCommitted(ctx, WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "test-session-task-preserve",
		Strategy:     "manual-commit",
		Agent:        agent.AgentTypeClaudeCode,
		Transcript:   redact.AlreadyRedacted([]byte(`{"type":"assistant","message":"initial"}`)),
		Prompts:      []string{"first prompt"},
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	// Inject task metadata into /full/current to emulate condensation-time task copy.
	refName := plumbing.ReferenceName(paths.V2FullCurrentRefName)
	parentHash, rootTreeHash, err := store.GetRefState(refName)
	require.NoError(t, err)

	taskPath := []string{string(cpID[:2]), string(cpID[2:]), "0", "tasks", "toolu_01TASK"}
	checkpointJSON := []byte(`{"session_id":"test-session-task-preserve","tool_use_id":"toolu_01TASK"}`)
	blobHash, err := CreateBlobFromContent(repo, checkpointJSON)
	require.NoError(t, err)

	newRootHash, err := UpdateSubtree(
		repo, rootTreeHash,
		taskPath,
		[]object.TreeEntry{{Name: "checkpoint.json", Mode: filemode.Regular, Hash: blobHash}},
		UpdateSubtreeOptions{MergeMode: MergeKeepExisting},
	)
	require.NoError(t, err)

	authorName, authorEmail := GetGitAuthorFromRepo(repo)
	commitHash, err := CreateCommit(ctx, repo, newRootHash, parentHash,
		fmt.Sprintf("Checkpoint: %s (task metadata)\n", cpID), authorName, authorEmail)
	require.NoError(t, err)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(refName, commitHash)))

	// Finalize checkpoint with full transcript (the stop-time path).
	err = store.UpdateCommitted(ctx, UpdateCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "test-session-task-preserve",
		Transcript:   redact.AlreadyRedacted([]byte(`{"type":"assistant","message":"finalized"}`)),
		Prompts:      []string{"first prompt", "second prompt"},
		Agent:        agent.AgentTypeClaudeCode,
	})
	require.NoError(t, err)

	// Task metadata should still exist after UpdateCommitted.
	fullTree := v2FullTree(t, repo)
	_, err = fullTree.File(cpID.Path() + "/0/tasks/toolu_01TASK/checkpoint.json")
	require.NoError(t, err, "task metadata should be preserved on /full/current during UpdateCommitted")
}

func TestWriteCommitted_TriggersRotationAtThreshold(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewV2GitStore(repo, "origin")
	store.maxCheckpointsPerGeneration = 3 // Low threshold for testing
	ctx := context.Background()

	// Write 3 checkpoints — the 3rd should trigger rotation
	for i := range 3 {
		cpID := id.MustCheckpointID(fmt.Sprintf("%012x", i+1))
		err := store.WriteCommitted(ctx, WriteCommittedOptions{
			CheckpointID: cpID,
			SessionID:    fmt.Sprintf("session-rot-%d", i),
			Strategy:     "manual-commit",
			Agent:        agent.AgentTypeClaudeCode,
			Transcript:   redact.AlreadyRedacted([]byte(fmt.Sprintf(`{"cp":%d}`, i))),
			AuthorName:   "Test",
			AuthorEmail:  "test@test.com",
		})
		require.NoError(t, err)
	}

	// Verify an archived generation exists
	archived, err := store.ListArchivedGenerations()
	require.NoError(t, err)
	assert.Len(t, archived, 1, "one archived generation should exist after rotation")

	// Verify /full/current is now a fresh generation (empty tree, no generation.json)
	_, freshTreeHash, err := store.GetRefState(plumbing.ReferenceName(paths.V2FullCurrentRefName))
	require.NoError(t, err)
	freshCount, err := store.CountCheckpointsInTree(freshTreeHash)
	require.NoError(t, err)
	assert.Equal(t, 0, freshCount, "fresh /full/current should have no checkpoints")

	// Verify the archived generation has 3 checkpoints
	_, archiveTreeHash, err := store.GetRefState(plumbing.ReferenceName(paths.V2FullRefPrefix + archived[0]))
	require.NoError(t, err)
	archiveCount, err := store.CountCheckpointsInTree(archiveTreeHash)
	require.NoError(t, err)
	assert.Equal(t, 3, archiveCount)

	// Write a 4th checkpoint — should land on the fresh /full/current
	cpID4 := id.MustCheckpointID("000000000004")
	err = store.WriteCommitted(ctx, WriteCommittedOptions{
		CheckpointID: cpID4,
		SessionID:    "session-rot-3",
		Strategy:     "manual-commit",
		Agent:        agent.AgentTypeClaudeCode,
		Transcript:   redact.AlreadyRedacted([]byte(`{"cp":3}`)),
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	_, newTreeHash, err := store.GetRefState(plumbing.ReferenceName(paths.V2FullCurrentRefName))
	require.NoError(t, err)
	newCount, err := store.CountCheckpointsInTree(newTreeHash)
	require.NoError(t, err)
	assert.Equal(t, 1, newCount, "new checkpoint should be on fresh generation")
}

func TestWriteCommitted_NoRotationBelowThreshold(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewV2GitStore(repo, "origin")
	store.maxCheckpointsPerGeneration = 5
	ctx := context.Background()

	// Write 3 checkpoints (below threshold of 5)
	for i := range 3 {
		cpID := id.MustCheckpointID(fmt.Sprintf("%012x", i+100))
		err := store.WriteCommitted(ctx, WriteCommittedOptions{
			CheckpointID: cpID,
			SessionID:    fmt.Sprintf("session-norot-%d", i),
			Strategy:     "manual-commit",
			Agent:        agent.AgentTypeClaudeCode,
			Transcript:   redact.AlreadyRedacted([]byte(fmt.Sprintf(`{"cp":%d}`, i))),
			AuthorName:   "Test",
			AuthorEmail:  "test@test.com",
		})
		require.NoError(t, err)
	}

	// No rotation should have occurred
	archived, err := store.ListArchivedGenerations()
	require.NoError(t, err)
	assert.Empty(t, archived, "no archived generations should exist below threshold")

	_, noRotTreeHash, err := store.GetRefState(plumbing.ReferenceName(paths.V2FullCurrentRefName))
	require.NoError(t, err)
	noRotCount, err := store.CountCheckpointsInTree(noRotTreeHash)
	require.NoError(t, err)
	assert.Equal(t, 3, noRotCount)
}

// TestV2GitStore_CleanupV1TranscriptFiles verifies that CleanupV1TranscriptFiles
// removes legacy v1-named files (full.jsonl, full.jsonl.*, content_hash.txt)
// from /full/current while preserving v2-named files.
func TestV2GitStore_CleanupV1TranscriptFiles(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewV2GitStore(repo, "origin")
	ctx := context.Background()

	cpID := id.MustCheckpointID("851fcec4a874")

	// Write initial checkpoint (sets up both /main and /full/current with v2 naming).
	err := store.WriteCommitted(ctx, WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "test-session-v1-cleanup",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte(`{"type":"human","message":"initial"}` + "\n")),
		Agent:        agent.AgentTypeClaudeCode,
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	// Inject v1-named files (full.jsonl, full.jsonl.001, content_hash.txt)
	// directly into the /full/current tree to simulate legacy data.
	refName := plumbing.ReferenceName(paths.V2FullCurrentRefName)
	parentHash, rootTreeHash, err := store.GetRefState(refName)
	require.NoError(t, err)

	basePath := cpID.Path() + "/"
	sessionPath := basePath + "0/"

	entries, err := store.gs.flattenCheckpointEntries(rootTreeHash, cpID.Path())
	require.NoError(t, err)

	v1Blob, err := CreateBlobFromContent(repo, []byte(`{"type":"human","message":"v1 data"}`+"\n"))
	require.NoError(t, err)
	v1HashBlob, err := CreateBlobFromContent(repo, []byte("sha256:v1hash"))
	require.NoError(t, err)
	v1ChunkBlob, err := CreateBlobFromContent(repo, []byte(`{"type":"assistant","message":"v1 chunk"}`+"\n"))
	require.NoError(t, err)

	entries[sessionPath+paths.TranscriptFileName] = object.TreeEntry{
		Name: sessionPath + paths.TranscriptFileName,
		Mode: filemode.Regular,
		Hash: v1Blob,
	}
	entries[sessionPath+paths.TranscriptFileName+".001"] = object.TreeEntry{
		Name: sessionPath + paths.TranscriptFileName + ".001",
		Mode: filemode.Regular,
		Hash: v1ChunkBlob,
	}
	entries[sessionPath+paths.ContentHashFileName] = object.TreeEntry{
		Name: sessionPath + paths.ContentHashFileName,
		Mode: filemode.Regular,
		Hash: v1HashBlob,
	}

	newTreeHash, err := store.gs.spliceCheckpointSubtree(ctx, rootTreeHash, cpID, basePath, entries)
	require.NoError(t, err)
	err = store.updateRef(ctx, refName, newTreeHash, parentHash, "Inject v1 files", "Test", "test@test.com")
	require.NoError(t, err)

	// Verify v1-named files exist before cleanup.
	tree := v2FullTree(t, repo)
	cpPath := cpID.Path()
	sessionTree, err := tree.Tree(cpPath + "/0")
	require.NoError(t, err)
	preCleanup := make(map[string]bool)
	for _, entry := range sessionTree.Entries {
		preCleanup[entry.Name] = true
	}
	assert.True(t, preCleanup[paths.TranscriptFileName], "full.jsonl should exist before cleanup")
	assert.True(t, preCleanup[paths.TranscriptFileName+".001"], "full.jsonl.001 should exist before cleanup")
	assert.True(t, preCleanup[paths.ContentHashFileName], "content_hash.txt should exist before cleanup")
	assert.True(t, preCleanup[paths.V2RawTranscriptFileName], "raw_transcript should exist before cleanup")

	// Run cleanup.
	err = store.CleanupV1TranscriptFiles(ctx, cpID, 1)
	require.NoError(t, err)

	// Verify v1-named files are gone, v2-named files are preserved.
	tree = v2FullTree(t, repo)
	sessionTree, err = tree.Tree(cpPath + "/0")
	require.NoError(t, err)

	postCleanup := make(map[string]bool)
	for _, entry := range sessionTree.Entries {
		postCleanup[entry.Name] = true
	}

	assert.True(t, postCleanup[paths.V2RawTranscriptFileName], "raw_transcript should exist after cleanup")
	assert.True(t, postCleanup[paths.V2RawTranscriptHashFileName], "raw_transcript_hash.txt should exist after cleanup")
	assert.False(t, postCleanup[paths.TranscriptFileName], "full.jsonl should be removed after cleanup")
	assert.False(t, postCleanup[paths.TranscriptFileName+".001"], "full.jsonl.001 should be removed after cleanup")
	assert.False(t, postCleanup[paths.ContentHashFileName], "content_hash.txt should be removed after cleanup")
}

// TestV2GitStore_CleanupV1TranscriptFiles_NoopWhenClean verifies that
// CleanupV1TranscriptFiles is a no-op when no v1 files exist.
func TestV2GitStore_CleanupV1TranscriptFiles_NoopWhenClean(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewV2GitStore(repo, "origin")
	ctx := context.Background()

	cpID := id.MustCheckpointID("962fcec4a874")

	err := store.WriteCommitted(ctx, WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "test-session-noop",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte(`{"type":"human","message":"clean"}` + "\n")),
		Agent:        agent.AgentTypeClaudeCode,
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	// Get tree hash before cleanup.
	_, treeBefore, err := store.GetRefState(plumbing.ReferenceName(paths.V2FullCurrentRefName))
	require.NoError(t, err)

	// Cleanup should be a no-op (no v1 files to remove).
	err = store.CleanupV1TranscriptFiles(ctx, cpID, 1)
	require.NoError(t, err)

	// Tree hash should be unchanged (no commit created).
	_, treeAfter, err := store.GetRefState(plumbing.ReferenceName(paths.V2FullCurrentRefName))
	require.NoError(t, err)
	assert.Equal(t, treeBefore, treeAfter, "tree should be unchanged when no v1 files exist")
}

func TestV2GitStore_CleanupV1TranscriptFiles_ReturnsCorruptRefError(t *testing.T) {
	t.Parallel()
	repo := initTestRepo(t)
	store := NewV2GitStore(repo, "origin")

	refName := plumbing.ReferenceName(paths.V2FullCurrentRefName)
	missingCommit := plumbing.NewHash("1111111111111111111111111111111111111111")
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(refName, missingCommit)))

	err := store.CleanupV1TranscriptFiles(context.Background(), id.MustCheckpointID("962fcec4a874"), 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get commit")
}
