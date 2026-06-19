package strategy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/session"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testNewActiveSessionID = "new-active-session"

// TestPostCommit_ActiveSession_CondensesImmediately verifies that PostCommit on
// an ACTIVE session condenses immediately and stays ACTIVE.
// With the 1:1 checkpoint model, each commit gets its own checkpoint right away.
func TestPostCommit_ActiveSession_CondensesImmediately(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-postcommit-active"

	// Initialize session and save a checkpoint so there is shadow branch content
	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	// Set phase to ACTIVE (simulating agent mid-turn)
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseActive
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Create a commit WITH the Trace-Checkpoint trailer on the main branch
	commitWithCheckpointTrailer(t, repo, dir, "a1b2c3d4e5f6")

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify phase stays ACTIVE (immediate condensation, no deferred phase)
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, session.PhaseActive, state.Phase,
		"ACTIVE session should stay ACTIVE after immediate condensation on GitCommit")

	// Verify condensation happened: the trace/checkpoints/v1 branch should exist
	sessionsRef, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.NoError(t, err, "trace/checkpoints/v1 branch should exist after immediate condensation")
	assert.NotNil(t, sessionsRef)

	// Verify StepCount was reset to 0 by condensation
	assert.Equal(t, 0, state.StepCount,
		"StepCount should be reset after immediate condensation")
}

// TestPostCommit_IdleSession_Condenses verifies that PostCommit on an IDLE
// session condenses session data and cleans up the shadow branch.
func TestPostCommit_IdleSession_Condenses(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-postcommit-idle"

	// Initialize session and save a checkpoint so there is shadow branch content
	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	// Set phase to IDLE (agent turn finished, waiting for user)
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseIdle
	state.LastInteractionTime = nil
	state.FilesTouched = []string{"test.txt"}
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Record shadow branch name before PostCommit
	shadowBranch := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)

	// Create a commit WITH the Trace-Checkpoint trailer
	commitWithCheckpointTrailer(t, repo, dir, "b2c3d4e5f6a1")

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify condensation happened: the trace/checkpoints/v1 branch should exist
	sessionsRef, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.NoError(t, err, "trace/checkpoints/v1 branch should exist after condensation")
	assert.NotNil(t, sessionsRef)

	// Verify shadow branch IS deleted after condensation
	refName := plumbing.NewBranchReferenceName(shadowBranch)
	_, err = repo.Reference(refName, true)
	assert.Error(t, err,
		"shadow branch should be deleted after condensation for IDLE session")
}

// TestPostCommit_RebaseDuringActive_SkipsTransition verifies that PostCommit
// is a no-op during rebase operations, leaving the session phase unchanged.
func TestPostCommit_RebaseDuringActive_SkipsTransition(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-postcommit-rebase"

	// Initialize session and save a checkpoint
	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	// Set phase to ACTIVE
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseActive
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Capture shadow branch name BEFORE any state changes
	shadowBranch := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)
	originalStepCount := state.StepCount

	// Simulate rebase in progress by creating .git/rebase-merge/ directory
	gitDir := filepath.Join(dir, ".git")
	rebaseMergeDir := filepath.Join(gitDir, "rebase-merge")
	require.NoError(t, os.MkdirAll(rebaseMergeDir, 0o755))
	defer os.RemoveAll(rebaseMergeDir)

	// Create a commit WITH the checkpoint trailer
	commitWithCheckpointTrailer(t, repo, dir, "c3d4e5f6a1b2")

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify phase stayed ACTIVE (no transition during rebase)
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Equal(t, session.PhaseActive, state.Phase,
		"session should stay ACTIVE during rebase (no transition)")

	// Verify StepCount was NOT reset (no condensation happened)
	assert.Equal(t, originalStepCount, state.StepCount,
		"StepCount should be unchanged - no condensation during rebase")

	// Verify NO condensation happened (trace/checkpoints/v1 branch should not exist)
	_, err = repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.Error(t, err,
		"trace/checkpoints/v1 branch should NOT exist - no condensation during rebase")

	// Verify shadow branch still exists (not cleaned up during rebase)
	refName := plumbing.NewBranchReferenceName(shadowBranch)
	_, err = repo.Reference(refName, true)
	assert.NoError(t, err,
		"shadow branch should be preserved during rebase")
}

// TestPostCommit_ReadOnlyActiveSessionNotCondensed verifies that an ACTIVE session
// with no tracked files is NOT condensed when another session claims the committed
// files. This prevents read-only sessions (e.g., codex exec from summarize) from
// being condensed into checkpoints they didn't contribute to.
func TestPostCommit_ReadOnlyActiveSessionNotCondensed(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	idleSessionID := "test-postcommit-idle-multi"
	activeSessionID := "test-postcommit-active-multi"

	// Initialize the idle session with a checkpoint
	setupSessionWithCheckpoint(t, s, repo, dir, idleSessionID)

	// Get worktree path and base commit from the idle session
	idleState, err := s.loadSessionState(context.Background(), idleSessionID)
	require.NoError(t, err)
	worktreePath := idleState.WorktreePath
	baseCommit := idleState.BaseCommit
	worktreeID := idleState.WorktreeID

	// Set idle session to IDLE phase
	idleState.Phase = session.PhaseIdle
	idleState.LastInteractionTime = nil
	idleState.FilesTouched = []string{"test.txt"}
	require.NoError(t, s.saveSessionState(context.Background(), idleState))

	// Create a second session with the SAME base commit and worktree (concurrent session).
	// This session is ACTIVE but has NO checkpoints (StepCount=0, no shadow branch content)
	// and NO files touched. With multiple sessions present, the read-only gate prevents
	// this session from being condensed — it never modified files, so there's nothing
	// meaningful to attach to the checkpoint.
	now := time.Now()
	activeState := &SessionState{
		SessionID:           activeSessionID,
		BaseCommit:          baseCommit,
		WorktreePath:        worktreePath,
		WorktreeID:          worktreeID,
		StartedAt:           now,
		Phase:               session.PhaseActive,
		LastInteractionTime: &now,
		StepCount:           0,
	}
	require.NoError(t, s.saveSessionState(context.Background(), activeState))

	// Record shadow branch name before PostCommit
	shadowBranch := getShadowBranchNameForCommit(baseCommit, worktreeID)

	// Create a commit WITH the checkpoint trailer
	commitWithCheckpointTrailer(t, repo, dir, "d4e5f6a1b2c3")

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify the ACTIVE session stays ACTIVE (immediate condensation model)
	activeState, err = s.loadSessionState(context.Background(), activeSessionID)
	require.NoError(t, err)
	assert.Equal(t, session.PhaseActive, activeState.Phase,
		"ACTIVE session should stay ACTIVE after GitCommit")

	// Only the IDLE session should be condensed (trace/checkpoints/v1 branch should exist)
	idleState, err = s.loadSessionState(context.Background(), idleSessionID)
	require.NoError(t, err)
	sessionsRef, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.NoError(t, err, "trace/checkpoints/v1 branch should exist after condensation")
	require.NotNil(t, sessionsRef)

	// Verify IDLE session's StepCount was reset by condensation
	assert.Equal(t, 0, idleState.StepCount,
		"IDLE session StepCount should be reset after condensation")

	// Shadow branch should be preserved because the ACTIVE session was NOT condensed
	// (it had no files touched, and totalSessionCount > 1 triggered the gate)
	refName := plumbing.NewBranchReferenceName(shadowBranch)
	_, err = repo.Reference(refName, true)
	assert.NoError(t, err,
		"shadow branch should be preserved — uncondensed ACTIVE session still references it")
}

// TestPostCommit_CondensationFailure_PreservesShadowBranch verifies that when
// condensation fails (corrupted shadow branch), BaseCommit is NOT updated.
func TestPostCommit_CondensationFailure_PreservesShadowBranch(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-postcommit-condense-fail-idle"

	// Initialize session and save a checkpoint
	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	// Set phase to IDLE
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseIdle
	state.LastInteractionTime = nil
	state.FilesTouched = []string{"test.txt"}
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Record original BaseCommit and StepCount before corruption
	originalBaseCommit := state.BaseCommit
	originalStepCount := state.StepCount

	// Corrupt shadow branch by pointing it at ZeroHash
	shadowBranch := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)
	corruptRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(shadowBranch), plumbing.ZeroHash)
	require.NoError(t, repo.Storer.SetReference(corruptRef))

	// Create a commit with the checkpoint trailer
	commitWithCheckpointTrailer(t, repo, dir, "e5f6a1b2c3d4")

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err, "PostCommit should not return error even when condensation fails")

	// Verify BaseCommit was NOT updated (condensation failed)
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, originalBaseCommit, state.BaseCommit,
		"BaseCommit should NOT be updated when condensation fails")
	assert.Equal(t, originalStepCount, state.StepCount,
		"StepCount should NOT be reset when condensation fails")

	// Verify trace/checkpoints/v1 branch does NOT exist (condensation failed)
	_, err = repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.Error(t, err,
		"trace/checkpoints/v1 branch should NOT exist when condensation fails")

	// Phase transition still applies even when condensation fails
	assert.Equal(t, session.PhaseIdle, state.Phase,
		"phase should remain IDLE when condensation fails")
}

// TestPostCommit_IdleSession_NoNewContent_PreservesBaseCommit verifies that when
// an IDLE session has no new transcript content since last condensation,
// PostCommit skips condensation and does NOT update BaseCommit.
//
// This prevents the bug where old IDLE sessions would have their BaseCommit
// incorrectly updated, causing them to be condensed on future commits.
func TestPostCommit_IdleSession_NoNewContent_PreservesBaseCommit(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-postcommit-idle-no-content"

	// Initialize session and save a checkpoint
	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	// Set phase to IDLE with CheckpointTranscriptStart matching transcript length (2 lines)
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseIdle
	state.LastInteractionTime = nil
	state.CheckpointTranscriptStart = 2                                   // Transcript has exactly 2 lines
	state.CheckpointTranscriptSize = shadowTranscriptSize(t, repo, state) // Match blob size on shadow branch
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Record shadow branch name and original BaseCommit
	shadowBranch := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)
	originalBaseCommit := state.BaseCommit
	originalStepCount := state.StepCount

	// Create a commit with the checkpoint trailer
	commitWithCheckpointTrailer(t, repo, dir, "f6a1b2c3d4e5")

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify BaseCommit was NOT updated (IDLE sessions don't get BaseCommit updated)
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, originalBaseCommit, state.BaseCommit,
		"BaseCommit should NOT be updated for IDLE session with no new content")

	// Shadow branch should still exist (not deleted, no condensation)
	refName := plumbing.NewBranchReferenceName(shadowBranch)
	_, err = repo.Reference(refName, true)
	require.NoError(t, err,
		"shadow branch should still exist when no condensation happened")

	// trace/checkpoints/v1 branch should NOT exist (no condensation)
	_, err = repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.Error(t, err,
		"trace/checkpoints/v1 branch should NOT exist when no condensation happened")

	// StepCount should be unchanged
	assert.Equal(t, originalStepCount, state.StepCount,
		"StepCount should be unchanged when no condensation happened")
}

// TestPostCommit_LegacySession_NoTranscriptSize_Condenses verifies that a session
// created by an older CLI (has CheckpointTranscriptStart but no CheckpointTranscriptSize)
// conservatively condenses rather than silently skipping. After condensation,
// CheckpointTranscriptSize is populated so future commits use the fast path.
func TestPostCommit_LegacySession_NoTranscriptSize_Condenses(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-postcommit-legacy-no-size"

	// Initialize session and save a checkpoint
	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	// Simulate a legacy session: has line count but no byte size (old CLI version).
	// The transcript has 2 lines and hasn't changed, but without CheckpointTranscriptSize
	// the new code can't verify that — it should conservatively condense.
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseIdle
	state.LastInteractionTime = nil
	state.CheckpointTranscriptStart = 2 // Set by old CLI after condensation
	state.CheckpointTranscriptSize = 0  // Not set by old CLI (field didn't exist)
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Create a commit with the checkpoint trailer
	commitWithCheckpointTrailer(t, repo, dir, "a1b2c3d4e5f6")

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Legacy session should have been condensed (conservative assumption)
	_, err = repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.NoError(t, err,
		"trace/checkpoints/v1 should exist — legacy session should condense conservatively")

	// After condensation, CheckpointTranscriptSize should now be populated
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Positive(t, state.CheckpointTranscriptSize,
		"CheckpointTranscriptSize should be populated after condensation (self-healing)")
}

// TestPostCommit_EndedSession_FilesTouched_Condenses verifies that an ENDED
// session with files touched and new content condenses on commit.
func TestPostCommit_EndedSession_FilesTouched_Condenses(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-postcommit-ended-condenses"

	// Initialize session and save a checkpoint
	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	// Set phase to ENDED with files touched
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	now := time.Now()
	state.Phase = session.PhaseEnded
	state.EndedAt = &now
	state.FilesTouched = []string{"test.txt"}
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Record shadow branch name
	shadowBranch := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)

	// Create a commit with the checkpoint trailer
	commitWithCheckpointTrailer(t, repo, dir, "a1b2c3d4e5f7")

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify trace/checkpoints/v1 branch exists (condensation happened)
	sessionsRef, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.NoError(t, err, "trace/checkpoints/v1 branch should exist after condensation")
	assert.NotNil(t, sessionsRef)

	// Verify old shadow branch is deleted after condensation
	refName := plumbing.NewBranchReferenceName(shadowBranch)
	_, err = repo.Reference(refName, true)
	require.Error(t, err,
		"shadow branch should be deleted after condensation for ENDED session")

	// Verify StepCount was reset by condensation
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, 0, state.StepCount,
		"StepCount should be reset after condensation")

	// Phase stays ENDED
	assert.Equal(t, session.PhaseEnded, state.Phase,
		"ENDED session should stay ENDED after condensation")
}

// TestPostCommit_EndedSession_FilesTouched_NoNewContent verifies that an ENDED
// session with files touched but no new transcript content skips condensation
// and does NOT update BaseCommit.
//
// This prevents the bug where old ENDED sessions would have their BaseCommit
// incorrectly updated, causing them to be condensed on future commits.
func TestPostCommit_EndedSession_FilesTouched_NoNewContent(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-postcommit-ended-no-content"

	// Initialize session and save a checkpoint
	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	// Set phase to ENDED with files touched but no new content
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	now := time.Now()
	state.Phase = session.PhaseEnded
	state.EndedAt = &now
	state.FilesTouched = []string{"test.txt"}
	state.CheckpointTranscriptStart = 2                                   // Transcript has exactly 2 lines
	state.CheckpointTranscriptSize = shadowTranscriptSize(t, repo, state) // Match blob size on shadow branch
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Record shadow branch name, original BaseCommit, and StepCount
	shadowBranch := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)
	originalBaseCommit := state.BaseCommit
	originalStepCount := state.StepCount

	// Create a commit with the checkpoint trailer
	commitWithCheckpointTrailer(t, repo, dir, "b2c3d4e5f6a2")

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify trace/checkpoints/v1 branch does NOT exist (no condensation)
	_, err = repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.Error(t, err,
		"trace/checkpoints/v1 branch should NOT exist when no new content")

	// Shadow branch should still exist
	refName := plumbing.NewBranchReferenceName(shadowBranch)
	_, err = repo.Reference(refName, true)
	require.NoError(t, err,
		"shadow branch should still exist when no condensation happened")

	// BaseCommit should NOT be updated (ENDED sessions don't get BaseCommit updated)
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, originalBaseCommit, state.BaseCommit,
		"BaseCommit should NOT be updated for ENDED session with no new content")

	// StepCount unchanged
	assert.Equal(t, originalStepCount, state.StepCount,
		"StepCount should be unchanged when no condensation happened")
}

// TestPostCommit_EndedSession_NoFilesTouched_Discards verifies that an ENDED
// session with no files touched takes the discard path and does NOT update BaseCommit.
//
// This prevents the bug where old ENDED sessions would have their BaseCommit
// incorrectly updated, causing them to be condensed on future commits.
func TestPostCommit_EndedSession_NoFilesTouched_Discards(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-postcommit-ended-discard"

	// Initialize session and save a checkpoint
	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	// Set phase to ENDED with no files touched
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	now := time.Now()
	state.Phase = session.PhaseEnded
	state.EndedAt = &now
	state.FilesTouched = nil // No files touched
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Record original BaseCommit and StepCount
	originalBaseCommit := state.BaseCommit
	originalStepCount := state.StepCount

	// Create a commit with the checkpoint trailer
	commitWithCheckpointTrailer(t, repo, dir, "c3d4e5f6a1b3")

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify trace/checkpoints/v1 branch does NOT exist (no condensation for discard path)
	_, err = repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.Error(t, err,
		"trace/checkpoints/v1 branch should NOT exist for discard path")

	// BaseCommit should NOT be updated (ENDED sessions don't get BaseCommit updated)
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, originalBaseCommit, state.BaseCommit,
		"BaseCommit should NOT be updated for ENDED session on discard path")

	// StepCount unchanged
	assert.Equal(t, originalStepCount, state.StepCount,
		"StepCount should be unchanged on discard path")

	// Phase stays ENDED
	assert.Equal(t, session.PhaseEnded, state.Phase,
		"ENDED session should stay ENDED on discard path")
}

// TestPostCommit_CondensationFailure_EndedSession_PreservesShadowBranch verifies
// that when condensation fails for an ENDED session with files touched,
// BaseCommit is preserved (not updated).
func TestPostCommit_CondensationFailure_EndedSession_PreservesShadowBranch(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-postcommit-condense-fail-ended"

	// Initialize session and save a checkpoint
	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	// Set phase to ENDED with files touched
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	now := time.Now()
	state.Phase = session.PhaseEnded
	state.EndedAt = &now
	state.FilesTouched = []string{"test.txt"}
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Record original BaseCommit and StepCount
	originalBaseCommit := state.BaseCommit
	originalStepCount := state.StepCount

	// Corrupt shadow branch by pointing it at ZeroHash
	shadowBranch := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)
	corruptRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(shadowBranch), plumbing.ZeroHash)
	require.NoError(t, repo.Storer.SetReference(corruptRef))

	// Create a commit with the checkpoint trailer
	commitWithCheckpointTrailer(t, repo, dir, "e5f6a1b2c3d5")

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err, "PostCommit should not return error even when condensation fails")

	// Verify BaseCommit was NOT updated (condensation failed)
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, originalBaseCommit, state.BaseCommit,
		"BaseCommit should NOT be updated when condensation fails for ENDED session")
	assert.Equal(t, originalStepCount, state.StepCount,
		"StepCount should NOT be reset when condensation fails for ENDED session")

	// Verify trace/checkpoints/v1 branch does NOT exist (condensation failed)
	_, err = repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.Error(t, err,
		"trace/checkpoints/v1 branch should NOT exist when condensation fails")

	// Phase stays ENDED
	assert.Equal(t, session.PhaseEnded, state.Phase,
		"ENDED session should stay ENDED when condensation fails")
}

// TestTurnEnd_Active_NoActions verifies that HandleTurnEnd with no actions
// is a no-op (normal ACTIVE → IDLE transition has no strategy-specific actions).
func TestTurnEnd_Active_NoActions(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-turnend-no-actions"

	// Initialize session and save a checkpoint
	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	// Set phase to ACTIVE (normal turn, no commit during turn)
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseActive
	require.NoError(t, s.saveSessionState(context.Background(), state))

	originalBaseCommit := state.BaseCommit
	originalStepCount := state.StepCount

	// ACTIVE + TurnEnd → IDLE with no strategy-specific actions
	result := session.Transition(state.Phase, session.EventTurnEnd, session.TransitionContext{})

	// Apply transition with no-op handler (no strategy actions for ACTIVE → IDLE)
	err = session.ApplyTransition(context.Background(), state, result, session.NoOpActionHandler{})
	require.NoError(t, err)

	// Call HandleTurnEnd — should be a no-op (no TurnCheckpointIDs)
	err = s.HandleTurnEnd(context.Background(), state)
	require.NoError(t, err)

	// Verify state is unchanged
	assert.Equal(t, originalBaseCommit, state.BaseCommit,
		"BaseCommit should be unchanged for no-op turn end")
	assert.Equal(t, originalStepCount, state.StepCount,
		"StepCount should be unchanged for no-op turn end")

	// Shadow branch should still exist (not cleaned up)
	shadowBranch := getShadowBranchNameForCommit(originalBaseCommit, state.WorktreeID)
	refName := plumbing.NewBranchReferenceName(shadowBranch)
	_, err = repo.Reference(refName, true)
	assert.NoError(t, err,
		"shadow branch should still exist after no-op turn end")
}
