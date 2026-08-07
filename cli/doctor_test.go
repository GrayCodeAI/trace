package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/session"
	"github.com/GrayCodeAI/trace/cli/strategy"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createV2Ref creates a v2 custom ref with an empty tree commit.
// Works for refs/trace/checkpoints/v2/main, refs/trace/checkpoints/v2/full/current, etc.
func createV2Ref(t *testing.T, repo *git.Repository, refName string) {
	t.Helper()

	treeHash, err := checkpoint.BuildTreeFromEntries(context.Background(), repo, make(map[string]object.TreeEntry))
	require.NoError(t, err)

	commitHash, err := checkpoint.CreateCommit(context.Background(), repo, treeHash, plumbing.ZeroHash, "init v2 ref", "test", "test@test.com")
	require.NoError(t, err)

	ref := plumbing.NewHashReference(plumbing.ReferenceName(refName), commitHash)
	require.NoError(t, repo.Storer.SetReference(ref))
}

// createBlob stores a string as a git blob and returns its hash.
func createBlob(t *testing.T, repo *git.Repository, content string) plumbing.Hash {
	t.Helper()
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	w, err := obj.Writer()
	require.NoError(t, err)
	_, err = w.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	hash, err := repo.Storer.SetEncodedObject(obj)
	require.NoError(t, err)
	return hash
}

// createV2RefWithCheckpoints creates a v2 custom ref with N checkpoint shard directories.
// Each shard has a minimal metadata.json file.
func createV2RefWithCheckpoints(t *testing.T, repo *git.Repository, refName string, count int) {
	t.Helper()

	entries := make(map[string]object.TreeEntry)
	for i := range count {
		cpID := fmt.Sprintf("%02x%010x", i%256, i)
		path := cpID[:2] + "/" + cpID[2:] + "/" + paths.MetadataFileName
		blobHash := createBlob(t, repo, fmt.Sprintf(`{"checkpoint_id":"%s"}`, cpID))
		entries[path] = object.TreeEntry{
			Name: paths.MetadataFileName,
			Mode: filemode.Regular,
			Hash: blobHash,
		}
	}

	treeHash, err := checkpoint.BuildTreeFromEntries(context.Background(), repo, entries)
	require.NoError(t, err)

	commitHash, err := checkpoint.CreateCommit(context.Background(), repo, treeHash, plumbing.ZeroHash, "v2 ref with checkpoints", "test", "test@test.com")
	require.NoError(t, err)

	ref := plumbing.NewHashReference(plumbing.ReferenceName(refName), commitHash)
	require.NoError(t, repo.Storer.SetReference(ref))
}

// newTestCmd creates a minimal cobra.Command with captured stdout/stderr for testing.
func newTestCmd(t *testing.T) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	return cmd, &stdout, &stderr
}

// testBaseCommit is a fake commit hash used across classifySession tests.
const testBaseCommit = "abcdef1234567890abcdef1234567890abcdef12"

// createShadowBranchRef creates a shadow branch reference in the repo for
// the given base commit and worktree ID. Uses an empty tree commit.
func createShadowBranchRef(t *testing.T, repo *git.Repository, baseCommit, worktreeID string) {
	t.Helper()

	// Create empty tree
	emptyTree := &object.Tree{Entries: []object.TreeEntry{}}
	treeObj := repo.Storer.NewEncodedObject()
	require.NoError(t, emptyTree.Encode(treeObj))
	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	// Create commit
	commitObj := &object.Commit{
		Author:    object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
		Committer: object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
		Message:   "shadow checkpoint",
		TreeHash:  treeHash,
	}
	enc := repo.Storer.NewEncodedObject()
	require.NoError(t, commitObj.Encode(enc))
	commitHash, err := repo.Storer.SetEncodedObject(enc)
	require.NoError(t, err)

	// Create branch reference
	branchName := checkpoint.ShadowBranchNameForCommit(baseCommit, worktreeID)
	refName := plumbing.NewBranchReferenceName(branchName)
	ref := plumbing.NewHashReference(refName, commitHash)
	require.NoError(t, repo.Storer.SetReference(ref))
}

func TestClassifySession_ActiveStale_NilInteractionTime(t *testing.T) {
	dir := setupGitRepoForPhaseTest(t)
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	state := &strategy.SessionState{
		SessionID:           "test-active-nil-time",
		BaseCommit:          testBaseCommit,
		Phase:               session.PhaseActive,
		StepCount:           3,
		LastInteractionTime: nil,
	}

	result := classifySession(state, repo, time.Now())

	require.NotNil(t, result, "active session with nil LastInteractionTime should be stuck")
	assert.Contains(t, result.Reason, "active, started")
	assert.Equal(t, 3, result.CheckpointCount)
	assert.False(t, result.HasShadowBranch)
}

func TestClassifySession_ActiveStale_OldInteractionTime(t *testing.T) {
	dir := setupGitRepoForPhaseTest(t)
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	state := &strategy.SessionState{
		SessionID:           "test-active-stale",
		BaseCommit:          testBaseCommit,
		Phase:               session.PhaseActive,
		StepCount:           2,
		LastInteractionTime: &twoHoursAgo,
		FilesTouched:        []string{"file1.go", "file2.go"},
	}

	now := time.Now()
	result := classifySession(state, repo, now)

	require.NotNil(t, result, "active session with old interaction time should be stuck")
	assert.Contains(t, result.Reason, "active, last interaction")
	assert.Equal(t, 2, result.CheckpointCount)
	assert.Equal(t, 2, result.FilesTouchedCount)
}

func TestClassifySession_ActiveRecent_Healthy(t *testing.T) {
	dir := setupGitRepoForPhaseTest(t)
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	fiveMinutesAgo := time.Now().Add(-5 * time.Minute)
	state := &strategy.SessionState{
		SessionID:           "test-active-healthy",
		BaseCommit:          testBaseCommit,
		Phase:               session.PhaseActive,
		StepCount:           1,
		LastInteractionTime: &fiveMinutesAgo,
	}

	result := classifySession(state, repo, time.Now())
	assert.Nil(t, result, "active session with recent interaction should be healthy")
}

func TestClassifySession_EndedWithUncondensedData(t *testing.T) {
	dir := setupGitRepoForPhaseTest(t)
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	baseCommit := testBaseCommit
	createShadowBranchRef(t, repo, baseCommit, "")

	state := &strategy.SessionState{
		SessionID:    "test-ended-uncondensed",
		BaseCommit:   baseCommit,
		Phase:        session.PhaseEnded,
		StepCount:    3,
		FilesTouched: []string{"main.go"},
	}

	result := classifySession(state, repo, time.Now())

	require.NotNil(t, result, "ended session with checkpoints and shadow branch should be stuck")
	assert.Equal(t, "ended with uncondensed checkpoint data", result.Reason)
	assert.True(t, result.HasShadowBranch)
	assert.Equal(t, 3, result.CheckpointCount)
	assert.Equal(t, 1, result.FilesTouchedCount)
}

func TestClassifySession_EndedNoShadowBranch_Healthy(t *testing.T) {
	dir := setupGitRepoForPhaseTest(t)
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	state := &strategy.SessionState{
		SessionID:  "test-ended-no-shadow",
		BaseCommit: testBaseCommit,
		Phase:      session.PhaseEnded,
		StepCount:  3,
	}

	result := classifySession(state, repo, time.Now())
	assert.Nil(t, result, "ended session without shadow branch should be healthy")
}

func TestClassifySession_EndedZeroStepCount_Healthy(t *testing.T) {
	dir := setupGitRepoForPhaseTest(t)
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	baseCommit := "1234567890abcdef1234567890abcdef12345678"
	createShadowBranchRef(t, repo, baseCommit, "")

	state := &strategy.SessionState{
		SessionID:  "test-ended-zero-steps",
		BaseCommit: baseCommit,
		Phase:      session.PhaseEnded,
		StepCount:  0,
	}

	result := classifySession(state, repo, time.Now())
	assert.Nil(t, result, "ended session with zero steps should be healthy even with shadow branch")
}

func TestClassifySession_IdlePhase_Healthy(t *testing.T) {
	dir := setupGitRepoForPhaseTest(t)
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	state := &strategy.SessionState{
		SessionID:  "test-idle",
		BaseCommit: testBaseCommit,
		Phase:      session.PhaseIdle,
		StepCount:  1,
	}

	result := classifySession(state, repo, time.Now())
	assert.Nil(t, result, "IDLE session should be healthy")
}

func TestClassifySession_EmptyPhase_Healthy(t *testing.T) {
	dir := setupGitRepoForPhaseTest(t)
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	state := &strategy.SessionState{
		SessionID:  "test-empty-phase",
		BaseCommit: testBaseCommit,
		Phase:      "",
		StepCount:  1,
	}

	result := classifySession(state, repo, time.Now())
	assert.Nil(t, result, "empty phase (backward compat) should be healthy")
}

func TestClassifySession_StalenessThresholdBoundary(t *testing.T) {
	dir := setupGitRepoForPhaseTest(t)
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	now := time.Now()

	// Exactly at the threshold — should be stuck (> check, not >=, but let's verify)
	justOverThreshold := now.Add(-session.StuckActiveThreshold - time.Second)
	state := &strategy.SessionState{
		SessionID:           "test-boundary-over",
		BaseCommit:          testBaseCommit,
		Phase:               session.PhaseActive,
		StepCount:           1,
		LastInteractionTime: &justOverThreshold,
	}

	result := classifySession(state, repo, now)
	require.NotNil(t, result, "session just over staleness threshold should be stuck")

	// Just under the threshold — should be healthy
	justUnderThreshold := now.Add(-session.StuckActiveThreshold + time.Minute)
	state2 := &strategy.SessionState{
		SessionID:           "test-boundary-under",
		BaseCommit:          testBaseCommit,
		Phase:               session.PhaseActive,
		StepCount:           1,
		LastInteractionTime: &justUnderThreshold,
	}

	result2 := classifySession(state2, repo, now)
	assert.Nil(t, result2, "session just under staleness threshold should be healthy")
}

func TestClassifySession_ActiveWithShadowBranch(t *testing.T) {
	dir := setupGitRepoForPhaseTest(t)
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	baseCommit := testBaseCommit
	createShadowBranchRef(t, repo, baseCommit, "")

	state := &strategy.SessionState{
		SessionID:           "test-active-shadow",
		BaseCommit:          baseCommit,
		Phase:               session.PhaseActive,
		StepCount:           2,
		LastInteractionTime: nil,
	}

	result := classifySession(state, repo, time.Now())

	require.NotNil(t, result)
	assert.True(t, result.HasShadowBranch, "should detect existing shadow branch")
	assert.NotEmpty(t, result.ShadowBranch)
}

func TestClassifySession_WorktreeIDInShadowBranch(t *testing.T) {
	dir := setupGitRepoForPhaseTest(t)
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	baseCommit := testBaseCommit
	worktreeID := "my-worktree"
	createShadowBranchRef(t, repo, baseCommit, worktreeID)

	state := &strategy.SessionState{
		SessionID:    "test-worktree-shadow",
		BaseCommit:   baseCommit,
		WorktreeID:   worktreeID,
		Phase:        session.PhaseEnded,
		StepCount:    1,
		FilesTouched: []string{"a.go"},
	}

	result := classifySession(state, repo, time.Now())

	require.NotNil(t, result, "ended session with worktree shadow branch should be stuck")
	assert.True(t, result.HasShadowBranch)
	expectedBranch := checkpoint.ShadowBranchNameForCommit(baseCommit, worktreeID)
	assert.Equal(t, expectedBranch, result.ShadowBranch)
}

func TestRunSessionsFix_MetadataCheckFailure_PropagatesError(t *testing.T) {
	// Cannot use t.Parallel() because t.Chdir modifies process-global state.
	dir := setupGitRepoForPhaseTest(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	// Create a real local metadata branch
	emptyTree := &object.Tree{Entries: []object.TreeEntry{}}
	treeObj := repo.Storer.NewEncodedObject()
	require.NoError(t, emptyTree.Encode(treeObj))
	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)

	commitObj := &object.Commit{
		Author:    object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
		Committer: object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
		Message:   "metadata",
		TreeHash:  treeHash,
	}
	enc := repo.Storer.NewEncodedObject()
	require.NoError(t, commitObj.Encode(enc))
	localHash, err := repo.Storer.SetEncodedObject(enc)
	require.NoError(t, err)

	localRef := plumbing.NewHashReference(
		plumbing.NewBranchReferenceName(paths.MetadataBranchName), localHash,
	)
	require.NoError(t, repo.Storer.SetReference(localRef))

	// Create a remote-tracking ref that points to a nonexistent object.
	// This makes IsMetadataDisconnected call git merge-base with a bad hash,
	// which fails with a non-0/1 exit code → treated as an error.
	bogusHash := plumbing.NewHash("0000000000000000000000000000000000000001")
	remoteRef := plumbing.NewHashReference(
		plumbing.NewRemoteReferenceName("origin", paths.MetadataBranchName), bogusHash,
	)
	require.NoError(t, repo.Storer.SetReference(remoteRef))

	// Build a minimal cobra command with captured output and context
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err = runSessionsFix(cmd, true)

	// The metadata check error should be propagated, not swallowed.
	// It should be SilentError because the user-facing message was already printed.
	require.Error(t, err, "runSessionsFix should return error when metadata check fails")
	var silentErr *SilentError
	require.ErrorAs(t, err, &silentErr)
	assert.Contains(t, err.Error(), "metadata check failed")
	assert.Contains(t, stderr.String(), "Error: metadata check failed")
}

func TestRunSessionsFix_ForceDiscardOutput_Indented(t *testing.T) {
	// Cannot use t.Parallel() because t.Chdir modifies process-global state.
	dir := setupGitRepoForPhaseTest(t)
	t.Chdir(dir)

	state := &strategy.SessionState{
		SessionID:  "2026-02-02-doctor-output",
		BaseCommit: testBaseCommit,
		Phase:      session.PhaseActive,
		StartedAt:  time.Now().Add(-2 * time.Hour),
	}
	require.NoError(t, strategy.SaveSessionState(context.Background(), state))

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	require.NoError(t, runSessionsFix(cmd, true))
	assert.Empty(t, stderr.String())

	output := stdout.String()
	assert.Contains(t, output, "✓ Metadata branches: OK")
	assert.Contains(t, output, "Found 1 stuck session(s):")
	assert.Contains(t, output, "  Session: 2026-02-02-doctor-output")
	assert.Contains(t, output, "  ✓ Discarded session 2026-02-02-doctor-output")

	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "Discarded session") {
			assert.True(t, strings.HasPrefix(line, "  ✓ "), "expected nested success line to stay indented: %q", line)
		}
	}
}
