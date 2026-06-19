package strategy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/session"
	"github.com/GrayCodeAI/trace/cli/trailers"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPostCommit_FilesTouched_ResetsAfterCondensation verifies that FilesTouched
// is reset after condensation, so subsequent condensations only contain the files
// touched since the last commit — not the accumulated history.
func TestPostCommit_FilesTouched_ResetsAfterCondensation(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-filestouched-reset"

	// --- Round 1: Save checkpoint touching files A.txt and B.txt ---

	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	require.NoError(t, os.MkdirAll(metadataDirAbs, 0o755))

	transcript := `{"type":"human","message":{"content":"round 1 prompt"}}
{"type":"assistant","message":{"content":"round 1 response"}}
`
	require.NoError(t, os.WriteFile(
		filepath.Join(metadataDirAbs, paths.TranscriptFileName),
		[]byte(transcript), 0o644,
	))

	// Create files A.txt and B.txt
	require.NoError(t, os.WriteFile(filepath.Join(dir, "A.txt"), []byte("file A"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "B.txt"), []byte("file B"), 0o644))

	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{},
		NewFiles:       []string{"A.txt", "B.txt"},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint 1: files A and B",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
	})
	require.NoError(t, err)

	// Set phase to IDLE so PostCommit triggers immediate condensation
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseIdle
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Verify FilesTouched has A.txt and B.txt before condensation
	assert.ElementsMatch(t, []string{"A.txt", "B.txt"}, state.FilesTouched,
		"FilesTouched should contain A.txt and B.txt before first condensation")

	// --- Commit A.txt, B.txt and condense (round 1) ---
	checkpointID1 := "a1a2a3a4a5a6"
	commitFilesWithTrailer(t, repo, dir, checkpointID1, "A.txt", "B.txt")

	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify condensation happened
	_, err = repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.NoError(t, err, "trace/checkpoints/v1 should exist after first condensation")

	// Verify first condensation contains A.txt and B.txt
	store := checkpoint.NewGitStore(repo)
	cpID1 := id.MustCheckpointID(checkpointID1)
	summary1, err := store.ReadCommitted(context.Background(), cpID1)
	require.NoError(t, err)
	require.NotNil(t, summary1)
	assert.ElementsMatch(t, []string{"A.txt", "B.txt"}, summary1.FilesTouched,
		"First condensation should contain A.txt and B.txt")

	// Verify FilesTouched was reset after condensation
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Nil(t, state.FilesTouched,
		"FilesTouched should be nil after condensation (all files were committed)")

	// --- Round 2: Save checkpoint touching files C.txt and D.txt ---

	// Append to transcript for round 2
	transcript2 := `{"type":"human","message":{"content":"round 2 prompt"}}
{"type":"assistant","message":{"content":"round 2 response"}}
`
	f, err := os.OpenFile(
		filepath.Join(metadataDirAbs, paths.TranscriptFileName),
		os.O_APPEND|os.O_WRONLY, 0o644,
	)
	require.NoError(t, err)
	_, err = f.WriteString(transcript2)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// Create files C.txt and D.txt
	require.NoError(t, os.WriteFile(filepath.Join(dir, "C.txt"), []byte("file C"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "D.txt"), []byte("file D"), 0o644))

	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{},
		NewFiles:       []string{"C.txt", "D.txt"},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint 2: files C and D",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
	})
	require.NoError(t, err)

	// Set phase to IDLE for immediate condensation
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseIdle
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Verify FilesTouched only has C.txt and D.txt (NOT A.txt, B.txt)
	assert.ElementsMatch(t, []string{"C.txt", "D.txt"}, state.FilesTouched,
		"FilesTouched should only contain C.txt and D.txt after reset")

	// --- Commit C.txt, D.txt and condense (round 2) ---
	checkpointID2 := "b1b2b3b4b5b6"
	commitFilesWithTrailer(t, repo, dir, checkpointID2, "C.txt", "D.txt")

	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify second condensation contains ONLY C.txt and D.txt
	cpID2 := id.MustCheckpointID(checkpointID2)
	summary2, err := store.ReadCommitted(context.Background(), cpID2)
	require.NoError(t, err)
	require.NotNil(t, summary2, "Second condensation should exist")
	assert.ElementsMatch(t, []string{"C.txt", "D.txt"}, summary2.FilesTouched,
		"Second condensation should only contain C.txt and D.txt, not accumulated files from first condensation")
}

// TestSubtractFiles verifies that subtractFiles correctly removes files present
// in the exclude set and preserves files not in it.
func TestSubtractFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		files    []string
		exclude  map[string]struct{}
		expected []string
	}{
		{
			name:     "no overlap",
			files:    []string{"a.txt", "b.txt"},
			exclude:  map[string]struct{}{"c.txt": {}},
			expected: []string{"a.txt", "b.txt"},
		},
		{
			name:     "full overlap",
			files:    []string{"a.txt", "b.txt"},
			exclude:  map[string]struct{}{"a.txt": {}, "b.txt": {}},
			expected: nil,
		},
		{
			name:     "partial overlap",
			files:    []string{"a.txt", "b.txt", "c.txt"},
			exclude:  map[string]struct{}{"b.txt": {}},
			expected: []string{"a.txt", "c.txt"},
		},
		{
			name:     "empty files",
			files:    []string{},
			exclude:  map[string]struct{}{"a.txt": {}},
			expected: nil,
		},
		{
			name:     "empty exclude",
			files:    []string{"a.txt", "b.txt"},
			exclude:  map[string]struct{}{},
			expected: []string{"a.txt", "b.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := subtractFiles(tt.files, tt.exclude)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestFilesChangedInCommit verifies that filesChangedInCommit correctly extracts
// the set of files changed in a commit by diffing against its parent.
func TestFilesChangedInCommit(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	// Create files and commit them
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("content1"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file2.txt"), []byte("content2"), 0o644))
	_, err = wt.Add("file1.txt")
	require.NoError(t, err)
	_, err = wt.Add("file2.txt")
	require.NoError(t, err)

	commitHash, err := wt.Commit("add files", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	commit, err := repo.CommitObject(commitHash)
	require.NoError(t, err)

	headTree, err := commit.Tree()
	require.NoError(t, err)
	var parentTree *object.Tree
	if commit.NumParents() > 0 {
		parent, pErr := commit.Parent(0)
		require.NoError(t, pErr)
		parentTree, err = parent.Tree()
		require.NoError(t, err)
	}

	changed := filesChangedInCommit(context.Background(), dir, commit, headTree, parentTree)
	assert.Contains(t, changed, "file1.txt")
	assert.Contains(t, changed, "file2.txt")
	// test.txt was in the initial commit, not this one
	assert.NotContains(t, changed, "test.txt")
}

// TestFilesChangedInCommit_InitialCommit verifies that filesChangedInCommit
// handles the initial commit (no parent) by listing all files.
func TestFilesChangedInCommit_InitialCommit(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)

	cfg, err := repo.Config()
	require.NoError(t, err)
	cfg.User.Name = "Test"
	cfg.User.Email = "test@test.com"
	require.NoError(t, repo.SetConfig(cfg))

	wt, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "init.txt"), []byte("initial"), 0o644))
	_, err = wt.Add("init.txt")
	require.NoError(t, err)

	commitHash, err := wt.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	commit, err := repo.CommitObject(commitHash)
	require.NoError(t, err)

	headTree, err := commit.Tree()
	require.NoError(t, err)

	changed := filesChangedInCommit(context.Background(), dir, commit, headTree, nil)
	assert.Contains(t, changed, "init.txt")
	assert.Len(t, changed, 1)
}

// TestFilesChangedInCommit_FallbackOnBadRepoDir verifies that when git diff-tree fails
// (e.g. invalid repoDir), filesChangedInCommit falls back to go-git tree walk and still
// returns correct results instead of an empty map.
func TestFilesChangedInCommit_FallbackOnBadRepoDir(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0o644))
	_, err = wt.Add("new.txt")
	require.NoError(t, err)

	commitHash, err := wt.Commit("add new file", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	commit, err := repo.CommitObject(commitHash)
	require.NoError(t, err)

	headTree, err := commit.Tree()
	require.NoError(t, err)
	var parentTree *object.Tree
	if commit.NumParents() > 0 {
		parent, pErr := commit.Parent(0)
		require.NoError(t, pErr)
		parentTree, err = parent.Tree()
		require.NoError(t, err)
	}

	// Pass a bogus repoDir to force git diff-tree to fail, triggering the fallback
	changed := filesChangedInCommit(context.Background(), "/nonexistent/repo", commit, headTree, parentTree)

	// Fallback should still detect the changed file via go-git tree walk
	assert.Contains(t, changed, "new.txt")
	assert.NotEmpty(t, changed, "fallback should return files, not empty map")
}

// TestPostCommit_ActiveSession_CarryForward_PartialCommit verifies that when an
// ACTIVE session has touched files A, B, C but only A and B are committed, the
// remaining file C is carried forward to a new shadow branch.
func TestPostCommit_ActiveSession_CarryForward_PartialCommit(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-carry-forward-partial"

	// Create metadata directory with transcript
	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	require.NoError(t, os.MkdirAll(metadataDirAbs, 0o755))

	transcript := `{"type":"human","message":{"content":"create files A B C"}}
{"type":"assistant","message":{"content":"creating files"}}
`
	require.NoError(t, os.WriteFile(
		filepath.Join(metadataDirAbs, paths.TranscriptFileName),
		[]byte(transcript), 0o644,
	))

	// Create all three files
	require.NoError(t, os.WriteFile(filepath.Join(dir, "A.txt"), []byte("file A"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "B.txt"), []byte("file B"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "C.txt"), []byte("file C"), 0o644))

	// Save checkpoint with all three files
	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{},
		NewFiles:       []string{"A.txt", "B.txt", "C.txt"},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint: files A, B, C",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
	})
	require.NoError(t, err)

	// Set phase to ACTIVE (agent mid-turn)
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseActive
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Verify FilesTouched contains all three files
	assert.ElementsMatch(t, []string{"A.txt", "B.txt", "C.txt"}, state.FilesTouched)

	// Commit ONLY A.txt and B.txt (not C.txt) with checkpoint trailer
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add("A.txt")
	require.NoError(t, err)
	_, err = wt.Add("B.txt")
	require.NoError(t, err)

	cpID := "cf1cf2cf3cf4"
	commitMsg := "commit A and B\n\n" + trailers.CheckpointTrailerKey + ": " + cpID + "\n"
	_, err = wt.Commit(commitMsg, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify session stayed ACTIVE
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, session.PhaseActive, state.Phase)

	// Verify carry-forward: FilesTouched should now only contain C.txt
	assert.Equal(t, []string{"C.txt"}, state.FilesTouched,
		"carry-forward should preserve only the uncommitted file C.txt")

	// Verify StepCount was set to 1 (carry-forward creates a new checkpoint)
	assert.Equal(t, 1, state.StepCount,
		"carry-forward should set StepCount to 1")

	// Verify CheckpointTranscriptStart was reset to 0 (prompt-level carry-forward)
	assert.Equal(t, 0, state.CheckpointTranscriptStart,
		"carry-forward should reset CheckpointTranscriptStart to 0 for full transcript reprocessing")

	// Verify LastCheckpointID was cleared (next commit generates fresh ID)
	assert.Empty(t, state.LastCheckpointID,
		"carry-forward should clear LastCheckpointID")

	// Verify a new shadow branch exists at the new HEAD
	newShadowBranch := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)
	_, err = repo.Reference(plumbing.NewBranchReferenceName(newShadowBranch), true)
	assert.NoError(t, err,
		"carry-forward should create a new shadow branch at the new HEAD")
}

// TestPostCommit_ActiveSession_CarryForward_AllCommitted verifies that when an
// ACTIVE session's files are ALL included in the commit, no carry-forward occurs.
func TestPostCommit_ActiveSession_CarryForward_AllCommitted(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-carry-forward-all"

	// Initialize session and save a checkpoint with files A and B
	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	require.NoError(t, os.MkdirAll(metadataDirAbs, 0o755))

	transcript := `{"type":"human","message":{"content":"create files A B"}}
{"type":"assistant","message":{"content":"creating files"}}
`
	require.NoError(t, os.WriteFile(
		filepath.Join(metadataDirAbs, paths.TranscriptFileName),
		[]byte(transcript), 0o644,
	))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "A.txt"), []byte("file A"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "B.txt"), []byte("file B"), 0o644))

	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{},
		NewFiles:       []string{"A.txt", "B.txt"},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint: files A, B",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
	})
	require.NoError(t, err)

	// Set phase to ACTIVE
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseActive
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Commit ALL files (A.txt and B.txt) with checkpoint trailer
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add("A.txt")
	require.NoError(t, err)
	_, err = wt.Add("B.txt")
	require.NoError(t, err)

	cpID := "cf5cf6cf7cf8"
	commitMsg := "commit A and B\n\n" + trailers.CheckpointTrailerKey + ": " + cpID + "\n"
	_, err = wt.Commit(commitMsg, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify session stayed ACTIVE
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, session.PhaseActive, state.Phase)

	// Verify NO carry-forward: FilesTouched should be nil (all condensed, nothing remaining)
	assert.Nil(t, state.FilesTouched,
		"when all files are committed, no carry-forward should occur (FilesTouched cleared by condensation)")

	// Verify StepCount was reset to 0 by condensation (not 1 from carry-forward)
	assert.Equal(t, 0, state.StepCount,
		"without carry-forward, StepCount should be reset to 0 by condensation")
}

// TestPostCommit_ActiveSession_RecordsTurnCheckpointIDs verifies that PostCommit
// records the checkpoint ID in TurnCheckpointIDs for ACTIVE sessions.
// This enables HandleTurnEnd to finalize all checkpoints with the full transcript.
func TestPostCommit_ActiveSession_RecordsTurnCheckpointIDs(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-turn-checkpoint-ids"

	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	// Set phase to ACTIVE (simulating agent mid-turn)
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseActive
	state.TurnCheckpointIDs = nil // Start clean
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Create first commit with checkpoint trailer
	commitWithCheckpointTrailer(t, repo, dir, "a1b2c3d4e5f6")

	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify TurnCheckpointIDs was populated
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, []string{"a1b2c3d4e5f6"}, state.TurnCheckpointIDs,
		"TurnCheckpointIDs should contain the checkpoint ID after condensation")
}

// TestPostCommit_IdleSession_DoesNotRecordTurnCheckpointIDs verifies that PostCommit
// does NOT record TurnCheckpointIDs for IDLE sessions.
func TestPostCommit_IdleSession_DoesNotRecordTurnCheckpointIDs(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-idle-no-turn-ids"

	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	// Set phase to IDLE with files touched so overlap check passes
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseIdle
	state.FilesTouched = []string{"test.txt"}
	require.NoError(t, s.saveSessionState(context.Background(), state))

	commitWithCheckpointTrailer(t, repo, dir, "c3d4e5f6a1b2")

	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify TurnCheckpointIDs was NOT set (IDLE sessions don't need finalization)
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Empty(t, state.TurnCheckpointIDs,
		"TurnCheckpointIDs should not be populated for IDLE sessions")
}

// TestHandleTurnEnd_PartialFailure verifies that HandleTurnEnd continues
// processing remaining checkpoints when one UpdateCommitted call fails.
// This locks the best-effort behavior: valid checkpoints get finalized even
// when one checkpoint ID is invalid or missing from trace/checkpoints/v1.
func TestHandleTurnEnd_PartialFailure(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-partial-failure"

	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	// Set phase to ACTIVE and create a transcript file with updated content
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseActive
	state.TurnCheckpointIDs = nil
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// First commit → creates real checkpoint on trace/checkpoints/v1
	commitWithCheckpointTrailer(t, repo, dir, "a1b2c3d4e5f6")
	require.NoError(t, s.PostCommit(context.Background()))

	// Write new content and create a second checkpoint on the shadow branch.
	// Use SaveStep directly (instead of setupSessionWithCheckpoint) so that
	// second.txt is included in FilesTouched — the overlap check needs it.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "second.txt"), []byte("second file"), 0o644))
	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{"test.txt"},
		NewFiles:       []string{"second.txt"},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint 2",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
	})
	require.NoError(t, err, "SaveStep should succeed for second checkpoint")
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseActive
	// Preserve TurnCheckpointIDs from the first commit
	state.TurnCheckpointIDs = []string{"a1b2c3d4e5f6"}
	require.NoError(t, s.saveSessionState(context.Background(), state))

	commitFilesWithTrailer(t, repo, dir, "b2c3d4e5f6a1", "second.txt")
	require.NoError(t, s.PostCommit(context.Background()))

	// Verify we now have 2 real checkpoint IDs
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	require.Len(t, state.TurnCheckpointIDs, 2,
		"Should have 2 real checkpoint IDs after 2 mid-turn commits")

	// Inject a fake 3rd checkpoint ID that doesn't exist on trace/checkpoints/v1
	state.TurnCheckpointIDs = append(state.TurnCheckpointIDs, "ffffffffffff")

	// Write a full transcript file for HandleTurnEnd to read
	fullTranscript := `{"type":"human","message":{"content":"build something"}}
{"type":"assistant","message":{"content":"done building"}}
{"type":"human","message":{"content":"now test it"}}
{"type":"assistant","message":{"content":"tests pass"}}
`
	transcriptPath := filepath.Join(dir, ".trace", "metadata", sessionID, "full_transcript.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(transcriptPath), 0o755))
	require.NoError(t, os.WriteFile(transcriptPath, []byte(fullTranscript), 0o644))
	state.TranscriptPath = transcriptPath
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Call HandleTurnEnd — should NOT return error (best-effort)
	err = s.HandleTurnEnd(context.Background(), state)
	require.NoError(t, err,
		"HandleTurnEnd should return nil even with partial failures (best-effort)")

	// TurnCheckpointIDs should be cleared regardless of partial failure
	assert.Empty(t, state.TurnCheckpointIDs,
		"TurnCheckpointIDs should be cleared after HandleTurnEnd, even with errors")

	// Verify the 2 valid checkpoints were finalized with the full transcript
	store := checkpoint.NewGitStore(repo)
	for _, cpIDStr := range []string{"a1b2c3d4e5f6", "b2c3d4e5f6a1"} {
		cpID := id.MustCheckpointID(cpIDStr)
		content, readErr := store.ReadSessionContent(context.Background(), cpID, 0)
		require.NoError(t, readErr,
			"Should be able to read finalized checkpoint %s", cpIDStr)
		assert.Contains(t, string(content.Transcript), "now test it",
			"Checkpoint %s should contain the full transcript (including later messages)", cpIDStr)
	}
}

func TestHandleTurnEnd_V2FullCurrent_PreservesTaskMetadata(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	// Enable checkpoints_v2 dual-write so PostCommit/HandleTurnEnd update v2 refs.
	traceDir := filepath.Join(dir, ".trace")
	require.NoError(t, os.MkdirAll(traceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"), []byte(testCheckpointsV2SettingsJSON), 0o644))

	s := &ManualCommitStrategy{}
	sessionID := "test-turn-end-v2-task-preserve"

	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	require.NoError(t, os.MkdirAll(metadataDirAbs, 0o755))
	transcriptPath := filepath.Join(metadataDirAbs, paths.TranscriptFileName)
	require.NoError(t, os.WriteFile(transcriptPath, []byte(testTranscriptPromptResponse), 0o644))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.txt"), []byte("agent modified content"), 0o644))
	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{"test.txt"},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint 1",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
	})
	require.NoError(t, err)

	subagentTranscriptPath := filepath.Join(metadataDirAbs, "subagent.jsonl")
	require.NoError(t, os.WriteFile(subagentTranscriptPath, []byte("{\"type\":\"event\",\"message\":\"done\"}\n"), 0o644))
	err = s.SaveTaskStep(context.Background(), TaskStepContext{
		SessionID:              sessionID,
		ToolUseID:              "toolu_01TASK",
		AgentID:                "agent-01",
		ModifiedFiles:          []string{"test.txt"},
		TranscriptPath:         transcriptPath,
		SubagentTranscriptPath: subagentTranscriptPath,
		CheckpointUUID:         "uuid-task-001",
		AuthorName:             "Test",
		AuthorEmail:            "test@test.com",
		SubagentType:           "general",
		TaskDescription:        "Implement task",
		AgentType:              agent.AgentTypeClaudeCode,
	})
	require.NoError(t, err)

	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseActive
	state.TurnCheckpointIDs = nil
	require.NoError(t, s.saveSessionState(context.Background(), state))

	cpID := "a1b2c3d4e5f6"
	commitWithCheckpointTrailer(t, repo, dir, cpID)
	require.NoError(t, s.PostCommit(context.Background()))

	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	require.Equal(t, []string{cpID}, state.TurnCheckpointIDs)

	fullTranscript := `{"type":"human","message":{"content":"final user prompt"}}
{"type":"assistant","message":{"content":"final assistant response"}}
`
	require.NoError(t, os.WriteFile(transcriptPath, []byte(fullTranscript), 0o644))
	state.TranscriptPath = transcriptPath
	require.NoError(t, s.saveSessionState(context.Background(), state))

	require.NoError(t, s.HandleTurnEnd(context.Background(), state))

	v2FullRef, err := repo.Reference(plumbing.ReferenceName(paths.V2FullCurrentRefName), true)
	require.NoError(t, err)
	v2FullCommit, err := repo.CommitObject(v2FullRef.Hash())
	require.NoError(t, err)
	v2FullTree, err := v2FullCommit.Tree()
	require.NoError(t, err)

	checkpointID := id.MustCheckpointID(cpID)
	_, err = v2FullTree.File(checkpointID.Path() + "/0/tasks/toolu_01TASK/checkpoint.json")
	require.NoError(t, err, "task metadata should be preserved after HandleTurnEnd finalization")
}
