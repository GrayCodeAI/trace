package cli

import (
	"bytes"
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initMigrateTestRepo creates a repo with an initial commit.
func initMigrateTestRepo(t *testing.T) *git.Repository {
	t.Helper()
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, "README.md", "init")
	testutil.GitAdd(t, dir, "README.md")
	testutil.GitCommit(t, dir, "initial")

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	return repo
}

// writeV1Checkpoint writes a checkpoint to the v1 branch for testing.
func writeV1Checkpoint(t *testing.T, store *checkpoint.GitStore, cpID id.CheckpointID, sessionID string, transcript []byte, prompts []string) {
	t.Helper()
	err := store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    sessionID,
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted(transcript),
		Prompts:      prompts,
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)
}

func newMigrateStores(repo *git.Repository) (*checkpoint.GitStore, *checkpoint.V2GitStore) {
	return checkpoint.NewGitStore(repo), checkpoint.NewV2GitStore(repo, migrateRemoteName)
}

func buildTasksTreeHashWithContent(t *testing.T, repo *git.Repository, toolUseID string, content string) plumbing.Hash {
	t.Helper()

	blobHash, err := checkpoint.CreateBlobFromContent(repo, []byte(content))
	require.NoError(t, err)

	treeHash, err := checkpoint.BuildTreeFromEntries(context.Background(), repo, map[string]object.TreeEntry{
		toolUseID + "/checkpoint.json": {Mode: filemode.Regular, Hash: blobHash},
	})
	require.NoError(t, err)

	return treeHash
}

func addV1SessionTasksTree(t *testing.T, repo *git.Repository, cpID id.CheckpointID, sessionIdx int, toolUseID string) {
	t.Helper()
	addV1SessionTasksTreeWithContent(t, repo, cpID, sessionIdx, toolUseID, `{"tool_use_id":"`+toolUseID+`"}`)
}

func addV1SessionTasksTreeWithContent(t *testing.T, repo *git.Repository, cpID id.CheckpointID, sessionIdx int, toolUseID string, content string) {
	t.Helper()

	tasksTreeHash := buildTasksTreeHashWithContent(t, repo, toolUseID, content)
	tasksTree, err := repo.TreeObject(tasksTreeHash)
	require.NoError(t, err)

	refName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	ref, err := repo.Reference(refName, true)
	require.NoError(t, err)

	commit, err := repo.CommitObject(ref.Hash())
	require.NoError(t, err)

	newRoot, err := checkpoint.UpdateSubtree(
		repo, commit.TreeHash,
		[]string{string(cpID[:2]), string(cpID[2:]), strconv.Itoa(sessionIdx), "tasks"},
		tasksTree.Entries,
		checkpoint.UpdateSubtreeOptions{MergeMode: checkpoint.MergeKeepExisting},
	)
	require.NoError(t, err)

	commitHash, err := checkpoint.CreateCommit(context.Background(), repo, newRoot, ref.Hash(),
		"Add test session task metadata\n",
		"Test", "test@test.com")
	require.NoError(t, err)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(refName, commitHash)))
}

func addV1RootTasksTreeWithContent(t *testing.T, repo *git.Repository, cpID id.CheckpointID, toolUseID string, content string) {
	t.Helper()

	tasksTreeHash := buildTasksTreeHashWithContent(t, repo, toolUseID, content)
	tasksTree, err := repo.TreeObject(tasksTreeHash)
	require.NoError(t, err)

	refName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	ref, err := repo.Reference(refName, true)
	require.NoError(t, err)

	commit, err := repo.CommitObject(ref.Hash())
	require.NoError(t, err)

	newRoot, err := checkpoint.UpdateSubtree(
		repo, commit.TreeHash,
		[]string{string(cpID[:2]), string(cpID[2:]), "tasks"},
		tasksTree.Entries,
		checkpoint.UpdateSubtreeOptions{MergeMode: checkpoint.MergeKeepExisting},
	)
	require.NoError(t, err)

	commitHash, err := checkpoint.CreateCommit(context.Background(), repo, newRoot, ref.Hash(),
		"Add test root task metadata\n",
		"Test", "test@test.com")
	require.NoError(t, err)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(refName, commitHash)))
}

func TestMigrateCheckpointsV2_Basic(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("a1b2c3d4e5f6")
	writeV1Checkpoint(
		t, v1Store, cpID, "session-001",
		[]byte("{\"type\":\"assistant\",\"message\":\"hello\"}\n"),
		[]string{"test prompt"},
	)

	var stdout bytes.Buffer

	result, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result.migrated)
	assert.Equal(t, 0, result.skipped)
	assert.Equal(t, 0, result.failed)

	// Verify checkpoint exists in v2
	summary, err := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, err)
	require.NotNil(t, summary, "checkpoint should exist in v2 after migration")
	assert.Equal(t, cpID, summary.CheckpointID)
}

func TestMigrateCheckpointsV2_PreservesCreatedAt(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cpID := id.MustCheckpointID("b1c2d3e4f5a6")
	err := v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-created-at",
		CreatedAt:    createdAt,
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte("{\"type\":\"assistant\",\"message\":\"hello\"}\n")),
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	var stdout bytes.Buffer
	result, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result.migrated)

	content, err := v2Store.ReadSessionContent(context.Background(), cpID, 0)
	require.NoError(t, err)
	assert.True(t, content.Metadata.CreatedAt.Equal(createdAt))
}

func TestMigrateCheckpointsV2_PacksFullGenerationsOldestFirst(t *testing.T) {
	oldMax := migrateMaxCheckpointsPerGeneration
	migrateMaxCheckpointsPerGeneration = 2
	t.Cleanup(func() {
		migrateMaxCheckpointsPerGeneration = oldMax
	})

	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)
	ctx := context.Background()

	checkpointIDs := []id.CheckpointID{
		id.MustCheckpointID("000000000001"),
		id.MustCheckpointID("000000000002"),
		id.MustCheckpointID("000000000003"),
		id.MustCheckpointID("000000000004"),
		id.MustCheckpointID("000000000005"),
	}
	createdAt := []time.Time{
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC),
	}

	// Write in non-chronological order to prove migration repacks by checkpoint time,
	// not v1 tree traversal or v1 ListCommitted's newest-first order.
	for _, idx := range []int{3, 1, 4, 0, 2} {
		err := v1Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
			CheckpointID: checkpointIDs[idx],
			SessionID:    "session-pack-" + strconv.Itoa(idx),
			CreatedAt:    createdAt[idx],
			Strategy:     "manual-commit",
			Transcript: redact.AlreadyRedacted([]byte(
				`{"type":"assistant","message":"checkpoint ` + strconv.Itoa(idx) + `"}` + "\n",
			)),
			Prompts:     []string{"prompt " + strconv.Itoa(idx)},
			AuthorName:  "Test",
			AuthorEmail: "test@test.com",
		})
		require.NoError(t, err)
	}

	var stdout bytes.Buffer
	result, err := migrateCheckpointsV2(ctx, repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, err)
	assert.Equal(t, 5, result.migrated)
	assert.Equal(t, 0, result.skipped)
	assert.Equal(t, 0, result.failed)

	archived, err := v2Store.ListArchivedGenerations()
	require.NoError(t, err)
	require.Equal(t, []string{"0000000000001", "0000000000002", "0000000000003"}, archived)

	expectedBatches := [][]int{
		{0, 1},
		{2, 3},
		{4},
	}
	for genIdx, batch := range expectedBatches {
		refName := plumbing.ReferenceName(paths.V2FullRefPrefix + archived[genIdx])
		gen, genErr := v2Store.ReadGenerationFromRef(refName)
		require.NoError(t, genErr)
		assert.True(t, gen.OldestCheckpointAt.Equal(createdAt[batch[0]]), "generation %s oldest", archived[genIdx])
		assert.True(t, gen.NewestCheckpointAt.Equal(createdAt[batch[len(batch)-1]]), "generation %s newest", archived[genIdx])

		_, treeHash, refErr := v2Store.GetRefState(refName)
		require.NoError(t, refErr)
		count, countErr := v2Store.CountCheckpointsInTree(treeHash)
		require.NoError(t, countErr)
		assert.Equal(t, len(batch), count)

		tree, treeErr := repo.TreeObject(treeHash)
		require.NoError(t, treeErr)
		for _, idx := range batch {
			_, treeErr = tree.Tree(checkpointIDs[idx].Path())
			require.NoError(t, treeErr, "generation %s should contain checkpoint %s", archived[genIdx], checkpointIDs[idx])
		}
	}

	_, currentTreeHash, err := v2Store.GetRefState(plumbing.ReferenceName(paths.V2FullCurrentRefName))
	require.NoError(t, err)
	currentCount, err := v2Store.CountCheckpointsInTree(currentTreeHash)
	require.NoError(t, err)
	assert.Equal(t, 0, currentCount, "fresh migration should leave /full/current empty for post-migration writes")
}

func TestMigrateCheckpointsV2_PacksFullGenerationMetadataFromRawTranscriptTimestamps(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("101112131415")
	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rawOldest := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	rawNewest := time.Date(2026, 3, 10, 9, 5, 0, 0, time.UTC)
	transcript := []byte(
		`{"type":"user","timestamp":"` + rawOldest.Format(time.RFC3339Nano) + `"}` + "\n" +
			`{"type":"assistant","timestamp":"` + rawNewest.Format(time.RFC3339Nano) + `"}` + "\n",
	)

	err := v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-raw-timestamps",
		CreatedAt:    createdAt,
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted(transcript),
		Prompts:      []string{"raw timestamp prompt"},
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	var stdout bytes.Buffer
	result, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result.migrated)

	archived, err := v2Store.ListArchivedGenerations()
	require.NoError(t, err)
	require.Equal(t, []string{"0000000000001"}, archived)

	gen, err := v2Store.ReadGenerationFromRef(plumbing.ReferenceName(paths.V2FullRefPrefix + archived[0]))
	require.NoError(t, err)
	assert.True(t, gen.OldestCheckpointAt.Equal(rawOldest))
	assert.True(t, gen.NewestCheckpointAt.Equal(rawNewest))
	assert.False(t, gen.OldestCheckpointAt.Equal(createdAt), "raw transcript timestamps should take precedence over checkpoint metadata")
}

func TestMigrateCheckpointsV2_RerunPacksCheckpointsMissingFullArtifacts(t *testing.T) {
	oldMax := migrateMaxCheckpointsPerGeneration
	migrateMaxCheckpointsPerGeneration = 2
	t.Cleanup(func() {
		migrateMaxCheckpointsPerGeneration = oldMax
	})

	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)
	ctx := context.Background()

	checkpointIDs := []id.CheckpointID{
		id.MustCheckpointID("000000000011"),
		id.MustCheckpointID("000000000012"),
		id.MustCheckpointID("000000000013"),
	}
	createdAt := []time.Time{
		time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC),
	}

	for i, cpID := range checkpointIDs {
		err := v1Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
			CheckpointID: cpID,
			SessionID:    "session-interrupt-" + strconv.Itoa(i),
			CreatedAt:    createdAt[i],
			Strategy:     "manual-commit",
			Transcript: redact.AlreadyRedacted([]byte(
				`{"type":"assistant","message":"checkpoint ` + strconv.Itoa(i) + `"}` + "\n",
			)),
			Prompts:     []string{"prompt " + strconv.Itoa(i)},
			AuthorName:  "Test",
			AuthorEmail: "test@test.com",
		})
		require.NoError(t, err)
	}

	v1List, err := v1Store.ListCommitted(ctx)
	require.NoError(t, err)
	sortMigratableCheckpoints(v1List)
	for _, info := range v1List {
		fullCheckpoint, _, migrateErr := migrateOneCheckpoint(ctx, repo, v1Store, v2Store, info, false)
		require.NoError(t, migrateErr)
		require.NotNil(t, fullCheckpoint)
		require.NotEmpty(t, fullCheckpoint.sessions)
	}

	_, _, err = v2Store.GetRefState(plumbing.ReferenceName(paths.V2FullCurrentRefName))
	require.Error(t, err, "interrupted migration should not have written /full/current")

	var rerun bytes.Buffer
	result, err := migrateCheckpointsV2(ctx, repo, v1Store, v2Store, &rerun, false)
	require.NoError(t, err)
	assert.Equal(t, 3, result.migrated)
	assert.Equal(t, 0, result.skipped)
	assert.Equal(t, 0, result.failed)
	assert.Empty(t, rerun.String())

	archived, err := v2Store.ListArchivedGenerations()
	require.NoError(t, err)
	require.Equal(t, []string{"0000000000001", "0000000000002"}, archived)

	expectedBatches := [][]int{{0, 1}, {2}}
	for genIdx, batch := range expectedBatches {
		refName := plumbing.ReferenceName(paths.V2FullRefPrefix + archived[genIdx])
		gen, genErr := v2Store.ReadGenerationFromRef(refName)
		require.NoError(t, genErr)
		assert.True(t, gen.OldestCheckpointAt.Equal(createdAt[batch[0]]), "generation %s oldest", archived[genIdx])
		assert.True(t, gen.NewestCheckpointAt.Equal(createdAt[batch[len(batch)-1]]), "generation %s newest", archived[genIdx])

		_, treeHash, refErr := v2Store.GetRefState(refName)
		require.NoError(t, refErr)
		tree, treeErr := repo.TreeObject(treeHash)
		require.NoError(t, treeErr)
		for _, idx := range batch {
			_, treeErr = tree.Tree(checkpointIDs[idx].Path())
			require.NoError(t, treeErr, "generation %s should contain checkpoint %s", archived[genIdx], checkpointIDs[idx])
		}
	}

	_, currentTreeHash, err := v2Store.GetRefState(plumbing.ReferenceName(paths.V2FullCurrentRefName))
	require.NoError(t, err)
	currentCount, err := v2Store.CountCheckpointsInTree(currentTreeHash)
	require.NoError(t, err)
	assert.Equal(t, 0, currentCount, "rerun packing should leave /full/current empty for post-migration writes")
}

func TestMigrateCheckpointsV2_Idempotent(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("c3d4e5f6a1b2")
	writeV1Checkpoint(
		t, v1Store, cpID, "session-idem",
		[]byte("{\"type\":\"assistant\",\"message\":\"idempotent test\"}\n"),
		[]string{"idem prompt"},
	)

	var stdout bytes.Buffer

	// First run: should migrate
	result1, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result1.migrated)
	assert.Equal(t, 0, result1.skipped)

	// Second run: should skip (no agent type means backfill also can't produce compact transcript)
	stdout.Reset()
	result2, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, err)
	assert.Equal(t, 0, result2.migrated)
	assert.Equal(t, 1, result2.skipped)
}

func TestMigrateCheckpointsV2_ForceOverwritesExisting(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("f0f1f2f3f4f5")
	writeV1Checkpoint(
		t, v1Store, cpID, "session-force",
		[]byte("{\"type\":\"assistant\",\"message\":\"original\"}\n"),
		[]string{"original prompt"},
	)

	var stdout bytes.Buffer

	// First run: normal migration
	result1, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result1.migrated)

	// Second run without force: should skip
	stdout.Reset()
	result2, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, err)
	assert.Equal(t, 0, result2.migrated)
	assert.Equal(t, 1, result2.skipped)

	// Third run with force: should re-migrate
	stdout.Reset()
	result3, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, true)
	require.NoError(t, err)
	assert.Equal(t, 1, result3.migrated)
	assert.Equal(t, 0, result3.skipped)
	assert.Empty(t, stdout.String())

	// Verify checkpoint still readable in v2
	summary, readErr := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, readErr)
	require.NotNil(t, summary)
	assert.Equal(t, cpID, summary.CheckpointID)

	archived, err := v2Store.ListArchivedGenerations()
	require.NoError(t, err)
	require.Equal(t, []string{"0000000000001"}, archived, "force migration should replace archived raw transcripts instead of duplicating them into a later generation")

	_, currentTreeHash, err := v2Store.GetRefState(plumbing.ReferenceName(paths.V2FullCurrentRefName))
	require.NoError(t, err)
	currentCount, err := v2Store.CountCheckpointsInTree(currentTreeHash)
	require.NoError(t, err)
	assert.Equal(t, 0, currentCount, "force migration should leave /full/current empty for post-migration writes")
}

func TestMigrateCheckpointsV2_ForceMultipleCheckpoints(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID1 := id.MustCheckpointID("a0a1a2a3a4a5")
	cpID2 := id.MustCheckpointID("b0b1b2b3b4b5")
	writeV1Checkpoint(
		t, v1Store, cpID1, "session-force-1",
		[]byte("{\"type\":\"assistant\",\"message\":\"first\"}\n"),
		[]string{"prompt 1"},
	)
	writeV1Checkpoint(
		t, v1Store, cpID2, "session-force-2",
		[]byte("{\"type\":\"assistant\",\"message\":\"second\"}\n"),
		[]string{"prompt 2"},
	)

	// First run: migrates both
	var discard bytes.Buffer
	result1, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &discard, false)
	require.NoError(t, err)
	assert.Equal(t, 2, result1.migrated)

	// Force re-migrate: should re-migrate both (0 skipped)
	var stdout bytes.Buffer
	result2, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, true)
	require.NoError(t, err)
	assert.Equal(t, 2, result2.migrated)
	assert.Equal(t, 0, result2.skipped)
}

func TestPruneV2CheckpointForForce_RecomputesPartialArchivedGeneration(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)
	ctx := context.Background()

	cpID1 := id.MustCheckpointID("101010101010")
	cpID2 := id.MustCheckpointID("202020202020")
	cp1CreatedAt := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	cp2CreatedAt := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	for _, cp := range []struct {
		id        id.CheckpointID
		sessionID string
		createdAt time.Time
	}{
		{cpID1, "session-force-prune-1", cp1CreatedAt},
		{cpID2, "session-force-prune-2", cp2CreatedAt},
	} {
		err := v1Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
			CheckpointID: cp.id,
			SessionID:    cp.sessionID,
			CreatedAt:    cp.createdAt,
			Strategy:     "manual-commit",
			Transcript:   redact.AlreadyRedacted([]byte("{\"type\":\"assistant\",\"message\":\"force prune\"}\n")),
			AuthorName:   "Test",
			AuthorEmail:  "test@test.com",
		})
		require.NoError(t, err)
	}

	var stdout bytes.Buffer
	result, err := migrateCheckpointsV2(ctx, repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, err)
	assert.Equal(t, 2, result.migrated)

	require.NoError(t, pruneV2CheckpointForForce(ctx, repo, v2Store, cpID1))

	archived, err := v2Store.ListArchivedGenerations()
	require.NoError(t, err)
	require.Equal(t, []string{"0000000000001"}, archived)

	refName := plumbing.ReferenceName(paths.V2FullRefPrefix + archived[0])
	_, treeHash, err := v2Store.GetRefState(refName)
	require.NoError(t, err)
	count, err := v2Store.CountCheckpointsInTree(treeHash)
	require.NoError(t, err)
	assert.Equal(t, 1, count)

	rootTree, err := repo.TreeObject(treeHash)
	require.NoError(t, err)
	_, err = rootTree.Tree(cpID1.Path())
	require.Error(t, err, "force prune should remove the target checkpoint from archived generations")
	_, err = rootTree.Tree(cpID2.Path())
	require.NoError(t, err, "force prune should preserve other checkpoints in the archived generation")

	gen, err := v2Store.ReadGenerationFromRef(refName)
	require.NoError(t, err)
	assert.True(t, gen.OldestCheckpointAt.Equal(cp2CreatedAt))
	assert.True(t, gen.NewestCheckpointAt.Equal(cp2CreatedAt))
}

func TestMigrateCmd_ForceFlag(t *testing.T) {
	t.Parallel()
	cmd := newMigrateCmd()

	// Verify --force flag exists
	flag := cmd.Flags().Lookup("force")
	require.NotNil(t, flag, "--force flag should be registered")
	assert.Equal(t, "false", flag.DefValue)
}

func TestMigrateCmd_RepairsArchivedGenerationMetadata(t *testing.T) {
	repo := initMigrateTestRepo(t)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	t.Chdir(wt.Filesystem.Root())
	paths.ClearWorktreeRootCache()

	cpID := id.MustCheckpointID("123456789abc")
	rawOldest := time.Date(2025, 12, 20, 8, 0, 0, 0, time.UTC)
	rawNewest := time.Date(2025, 12, 20, 8, 5, 0, 0, time.UTC)
	createArchivedGenerationRefWithRawTranscript(t, repo, "0000000000007", cpID,
		time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 7, 1, 0, 0, 0, time.UTC),
		rawOldest, rawNewest)

	cmd := newMigrateCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--checkpoints", "v2"})

	require.NoError(t, cmd.Execute())
	assert.Contains(t, stdout.String(), "Archived generation metadata repair: 1 repaired")
	assert.Empty(t, stderr.String())

	v2Store := checkpoint.NewV2GitStore(repo, migrateRemoteName)
	gen, genErr := v2Store.ReadGenerationFromRef(plumbing.ReferenceName(paths.V2FullRefPrefix + "0000000000007"))
	require.NoError(t, genErr)
	assert.True(t, gen.OldestCheckpointAt.Equal(rawOldest))
	assert.True(t, gen.NewestCheckpointAt.Equal(rawNewest))
}

func TestMigrateCheckpointsV2_MultiSession(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("d4e5f6a1b2c3")

	// Write first session
	writeV1Checkpoint(
		t, v1Store, cpID, "session-multi-1",
		[]byte("{\"type\":\"assistant\",\"message\":\"session 1\"}\n"),
		[]string{"prompt 1"},
	)

	// Write second session to same checkpoint
	writeV1Checkpoint(
		t, v1Store, cpID, "session-multi-2",
		[]byte("{\"type\":\"assistant\",\"message\":\"session 2\"}\n"),
		[]string{"prompt 2"},
	)

	var stdout bytes.Buffer

	result, err := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, err)
	assert.Equal(t, 1, result.migrated)

	// Verify both sessions are in v2
	summary, readErr := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, readErr)
	require.NotNil(t, summary)
	assert.GreaterOrEqual(t, len(summary.Sessions), 2, "should have at least 2 sessions")
}

func TestMigrateCheckpointsV2_SkipsV1SessionWithoutTranscript(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("445566778899")

	writeV1Checkpoint(
		t, v1Store, cpID, "session-real",
		[]byte("{\"type\":\"assistant\",\"message\":\"real session\"}\n"),
		[]string{"real prompt"},
	)

	err := v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-without-transcript",
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
	assert.Equal(t, 1, result.migrated)
	assert.Equal(t, 0, result.skipped)
	assert.Equal(t, 0, result.failed)
	assert.Equal(t, 1, result.missingSessions)

	output := stdout.String()
	assert.NotContains(t, output, "warning: skipping v1 session 1")
	assert.NotContains(t, output, "skipped 1 session(s) with missing transcript/session content")

	summary, readErr := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, readErr)
	require.NotNil(t, summary)
	require.Len(t, summary.Sessions, 1)
	assert.Equal(t, "/"+cpID.Path()+"/0/metadata.json", summary.Sessions[0].Metadata)
}

func TestMigrateCheckpointsV2_SkipsV1SessionWithMissingDirectory(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("4455667788aa")
	writeV1Checkpoint(
		t, v1Store, cpID, "session-real",
		[]byte("{\"type\":\"assistant\",\"message\":\"real session\"}\n"),
		[]string{"real prompt"},
	)
	appendMissingV1SessionReference(t, repo, v1Store, cpID)

	var stdout bytes.Buffer
	result, migrateErr := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, migrateErr)
	assert.Equal(t, 1, result.migrated)
	assert.Equal(t, 0, result.skipped)
	assert.Equal(t, 0, result.failed)
	assert.Equal(t, 1, result.missingSessions)

	output := stdout.String()
	assert.NotContains(t, output, "warning: skipping v1 session 1")
	assert.NotContains(t, output, "skipped 1 session(s) with missing transcript/session content")

	summary, readErr := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, readErr)
	require.NotNil(t, summary)
	require.Len(t, summary.Sessions, 1)
	assert.Equal(t, "/"+cpID.Path()+"/0/metadata.json", summary.Sessions[0].Metadata)
}

func TestMigrateCheckpointsV2_TaskMetadataUsesMigratedSessionIndexAfterSkip(t *testing.T) {
	t.Parallel()
	repo := initMigrateTestRepo(t)
	v1Store, v2Store := newMigrateStores(repo)

	cpID := id.MustCheckpointID("66778899aabb")

	writeV1Checkpoint(
		t, v1Store, cpID, "session-real",
		[]byte("{\"type\":\"assistant\",\"message\":\"real session\"}\n"),
		[]string{"real prompt"},
	)

	err := v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-without-transcript",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted(nil),
		Prompts:      []string{"metadata-only prompt"},
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)

	err = v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-task",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte("{\"type\":\"assistant\",\"message\":\"task session\"}\n")),
		Prompts:      []string{"task prompt"},
		IsTask:       true,
		ToolUseID:    "toolu_root_shifted",
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)
	addV1SessionTasksTree(t, repo, cpID, 2, "toolu_session_shifted")

	var stdout bytes.Buffer
	result, migrateErr := migrateCheckpointsV2(context.Background(), repo, v1Store, v2Store, &stdout, false)
	require.NoError(t, migrateErr)
	assert.Equal(t, 1, result.migrated)

	summary, readErr := v2Store.ReadCommitted(context.Background(), cpID)
	require.NoError(t, readErr)
	require.NotNil(t, summary)
	require.Len(t, summary.Sessions, 2)
	assert.Equal(t, "/"+cpID.Path()+"/1/metadata.json", summary.Sessions[1].Metadata)

	rootTree := v2FullTreeForCheckpoint(t, repo, v2Store, cpID)

	_, err = rootTree.File(cpID.Path() + "/1/tasks/toolu_root_shifted/checkpoint.json")
	require.NoError(t, err, "root task metadata should follow the shifted v2 session index")
	_, err = rootTree.File(cpID.Path() + "/1/tasks/toolu_session_shifted/checkpoint.json")
	require.NoError(t, err, "session task metadata should follow the shifted v2 session index")
	_, err = rootTree.File(cpID.Path() + "/2/tasks/toolu_root_shifted/checkpoint.json")
	require.Error(t, err, "task metadata must not be written under a non-existent v2 session")
}
