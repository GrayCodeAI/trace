package strategy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/session"
	"github.com/GrayCodeAI/trace/cli/trailers"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostCommit_OldIdleSession_BaseCommitNotUpdated(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}

	// --- Create an old IDLE session from a previous commit ---
	oldSessionID := "old-idle-session"
	setupSessionWithCheckpoint(t, s, repo, dir, oldSessionID)

	oldState, err := s.loadSessionState(context.Background(), oldSessionID)
	require.NoError(t, err)
	oldState.Phase = session.PhaseIdle
	oldState.FilesTouched = []string{"old-file.txt"} // Has files touched (important for bug)
	require.NoError(t, s.saveSessionState(context.Background(), oldState))

	// Record the old session's BaseCommit BEFORE the new commit
	oldSessionOriginalBaseCommit := oldState.BaseCommit

	// Create a commit to move HEAD forward (simulating old session was condensed)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("unrelated"), 0o644))
	_, err = wt.Add("unrelated.txt")
	require.NoError(t, err)
	_, err = wt.Commit("unrelated commit without trailer", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	// --- Create a NEW ACTIVE session at the new HEAD ---
	newSessionID := testNewActiveSessionID
	setupSessionWithCheckpoint(t, s, repo, dir, newSessionID)

	newState, err := s.loadSessionState(context.Background(), newSessionID)
	require.NoError(t, err)
	newState.Phase = session.PhaseActive
	require.NoError(t, s.saveSessionState(context.Background(), newState))

	// --- Commit from the new session ---
	commitWithCheckpointTrailer(t, repo, dir, "a1b2c3d4e5f6")

	// Get new HEAD for comparison
	head, err := repo.Head()
	require.NoError(t, err)
	newHead := head.Hash().String()

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// --- Verify: old IDLE session's BaseCommit should NOT be updated ---
	oldState, err = s.loadSessionState(context.Background(), oldSessionID)
	require.NoError(t, err)
	assert.Equal(t, oldSessionOriginalBaseCommit, oldState.BaseCommit,
		"OLD IDLE session's BaseCommit should NOT be updated when a different session commits")
	assert.NotEqual(t, newHead, oldState.BaseCommit,
		"OLD IDLE session's BaseCommit should NOT match new HEAD")

	// New ACTIVE session's BaseCommit SHOULD be updated (it was condensed)
	newState, err = s.loadSessionState(context.Background(), newSessionID)
	require.NoError(t, err)
	assert.Equal(t, newHead, newState.BaseCommit,
		"NEW ACTIVE session's BaseCommit should be updated after condensation")
}

// TestPostCommit_OldEndedSession_BaseCommitNotUpdated verifies that when an ENDED
// session from a previous commit exists (with no new content to condense), and a
// NEW session makes a commit, the old ENDED session's BaseCommit is NOT updated.
//
// This simulates the scenario where:
// 1. Old session ran and was already condensed (no new transcript content)
// 2. Old session is now ENDED
// 3. New session commits
// 4. Old ENDED session should NOT have BaseCommit updated
func TestPostCommit_OldEndedSession_BaseCommitNotUpdated(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}

	// --- Create an old ENDED session that has NO new content to condense ---
	oldSessionID := "old-ended-session"
	setupSessionWithCheckpoint(t, s, repo, dir, oldSessionID)

	oldState, err := s.loadSessionState(context.Background(), oldSessionID)
	require.NoError(t, err)
	now := time.Now()
	oldState.Phase = session.PhaseEnded
	oldState.EndedAt = &now
	oldState.FilesTouched = []string{"old-file.txt"} // Has files touched
	// Mark transcript as fully condensed (no new content since last checkpoint)
	// The transcript has 2 lines, so CheckpointTranscriptStart=2 means no new content
	oldState.CheckpointTranscriptStart = 2
	require.NoError(t, s.saveSessionState(context.Background(), oldState))

	// Record the old session's BaseCommit BEFORE the new commit
	oldSessionOriginalBaseCommit := oldState.BaseCommit

	// Create a commit to move HEAD forward
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("unrelated"), 0o644))
	_, err = wt.Add("unrelated.txt")
	require.NoError(t, err)
	_, err = wt.Commit("unrelated commit without trailer", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	// --- Create a NEW ACTIVE session at the new HEAD ---
	newSessionID := testNewActiveSessionID
	setupSessionWithCheckpoint(t, s, repo, dir, newSessionID)

	newState, err := s.loadSessionState(context.Background(), newSessionID)
	require.NoError(t, err)
	newState.Phase = session.PhaseActive
	require.NoError(t, s.saveSessionState(context.Background(), newState))

	// --- Commit from the new session ---
	commitWithCheckpointTrailer(t, repo, dir, "b1c2d3e4f5a6")

	// Get new HEAD for comparison
	head, err := repo.Head()
	require.NoError(t, err)
	newHead := head.Hash().String()

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// --- Verify: old ENDED session's BaseCommit should NOT be updated ---
	oldState, err = s.loadSessionState(context.Background(), oldSessionID)
	require.NoError(t, err)
	assert.Equal(t, oldSessionOriginalBaseCommit, oldState.BaseCommit,
		"OLD ENDED session's BaseCommit should NOT be updated when a different session commits")
	assert.NotEqual(t, newHead, oldState.BaseCommit,
		"OLD ENDED session's BaseCommit should NOT match new HEAD")

	// New ACTIVE session's BaseCommit SHOULD be updated
	newState, err = s.loadSessionState(context.Background(), newSessionID)
	require.NoError(t, err)
	assert.Equal(t, newHead, newState.BaseCommit,
		"NEW ACTIVE session's BaseCommit should be updated after condensation")
}

// TestPostCommit_StaleActiveSession_NotCondensed verifies that a stale ACTIVE
// session (agent killed without Stop hook) is NOT condensed into an unrelated
// commit from a different session.
//
// Root cause: when an agent is killed without the Stop hook firing, its session
// remains in ACTIVE phase permanently. The overlap check prevents stale sessions
// with unrelated files from being condensed. The isRecentInteraction guard
// ensures that genuinely-active sessions (recent LastInteractionTime) skip the
// overlap check, while stale sessions (old/nil LastInteractionTime) must pass it.
func TestPostCommit_StaleActiveSession_NotCondensed(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}

	// --- Create a stale ACTIVE session from an old commit ---
	// This simulates an agent that was killed without the Stop hook firing.
	staleSessionID := "stale-active-session"
	setupSessionWithCheckpoint(t, s, repo, dir, staleSessionID)

	staleState, err := s.loadSessionState(context.Background(), staleSessionID)
	require.NoError(t, err)
	staleState.Phase = session.PhaseActive
	// The stale session touched "test.txt" (set by setupSessionWithCheckpoint)
	// but the new commit will modify a different file.
	staleState.FilesTouched = []string{"test.txt"}
	// Stale session: LastInteractionTime is old (agent was killed days ago)
	staleTime := time.Now().Add(-48 * time.Hour)
	staleState.LastInteractionTime = &staleTime
	require.NoError(t, s.saveSessionState(context.Background(), staleState))

	staleOriginalBaseCommit := staleState.BaseCommit
	staleOriginalStepCount := staleState.StepCount

	// Move HEAD forward with an unrelated commit (no trailer)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("unrelated work"), 0o644))
	_, err = wt.Add("unrelated.txt")
	require.NoError(t, err)
	_, err = wt.Commit("unrelated commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	// --- Create a NEW ACTIVE session at the new HEAD ---
	newSessionID := testNewActiveSessionID

	// Create a new file for the new session (different from stale session's test.txt)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new-feature.txt"), []byte("new feature content"), 0o644))

	metadataDir := ".trace/metadata/" + newSessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	require.NoError(t, os.MkdirAll(metadataDirAbs, 0o755))

	transcript := `{"type":"human","message":{"content":"add new feature"}}
{"type":"assistant","message":{"content":"adding new feature"}}
`
	require.NoError(t, os.WriteFile(
		filepath.Join(metadataDirAbs, paths.TranscriptFileName),
		[]byte(transcript), 0o644,
	))

	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      newSessionID,
		ModifiedFiles:  []string{},
		NewFiles:       []string{"new-feature.txt"},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint: new feature",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
	})
	require.NoError(t, err)

	newState, err := s.loadSessionState(context.Background(), newSessionID)
	require.NoError(t, err)
	newState.Phase = session.PhaseActive
	// New session has recent interaction (agent is genuinely running)
	now := time.Now()
	newState.LastInteractionTime = &now
	require.NoError(t, s.saveSessionState(context.Background(), newState))

	// --- Commit ONLY new-feature.txt (not test.txt) with checkpoint trailer ---
	wt, err = repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add("new-feature.txt")
	require.NoError(t, err)

	cpID := "de1de2de3de4"
	commitMsg := "add new feature\n\n" + trailers.CheckpointTrailerKey + ": " + cpID + "\n"
	_, err = wt.Commit(commitMsg, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	head, err := repo.Head()
	require.NoError(t, err)
	newHead := head.Hash().String()

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// --- Verify: stale ACTIVE session was NOT condensed ---
	staleState, err = s.loadSessionState(context.Background(), staleSessionID)
	require.NoError(t, err)

	// StepCount should be unchanged (not reset by condensation)
	assert.Equal(t, staleOriginalStepCount, staleState.StepCount,
		"Stale ACTIVE session StepCount should NOT be reset (no condensation)")

	// BaseCommit IS updated for ACTIVE sessions (updateBaseCommitIfChanged)
	assert.Equal(t, newHead, staleState.BaseCommit,
		"Stale ACTIVE session BaseCommit should be updated (ACTIVE sessions always get BaseCommit updated)")
	assert.NotEqual(t, staleOriginalBaseCommit, staleState.BaseCommit,
		"Stale ACTIVE session BaseCommit should have changed")

	// Phase stays ACTIVE
	assert.Equal(t, session.PhaseActive, staleState.Phase,
		"Stale ACTIVE session should remain ACTIVE")

	// --- Verify: new ACTIVE session WAS condensed ---
	newState, err = s.loadSessionState(context.Background(), newSessionID)
	require.NoError(t, err)

	// StepCount reset to 0 by condensation
	assert.Equal(t, 0, newState.StepCount,
		"New ACTIVE session StepCount should be reset by condensation")

	// BaseCommit updated to new HEAD
	assert.Equal(t, newHead, newState.BaseCommit,
		"New ACTIVE session BaseCommit should be updated after condensation")

	// Verify trace/checkpoints/v1 exists (new session was condensed)
	_, err = repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.NoError(t, err,
		"trace/checkpoints/v1 should exist (new session was condensed)")
}

// TestPostCommit_IdleSessionEmptyFilesTouched_NotCondensed verifies that an IDLE
// session with hasNew=true but empty FilesTouched is NOT condensed into a commit.
//
// This can happen for conversation-only sessions where the transcript grew but no
// files were modified. Previously, filesOverlapWithContent was called with an empty
// list and returned false. The shouldCondenseWithOverlapCheck method must also
// return false when filesTouchedBefore is empty.
func TestPostCommit_IdleSessionEmptyFilesTouched_NotCondensed(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}

	// --- Create an IDLE session with a checkpoint but no files touched ---
	idleSessionID := "idle-no-files-session"
	setupSessionWithCheckpoint(t, s, repo, dir, idleSessionID)

	idleState, err := s.loadSessionState(context.Background(), idleSessionID)
	require.NoError(t, err)
	idleState.Phase = session.PhaseIdle
	// Clear FilesTouched to simulate a conversation-only session
	idleState.FilesTouched = nil
	// CheckpointTranscriptStart=0 so sessionHasNewContent returns true
	idleState.CheckpointTranscriptStart = 0
	require.NoError(t, s.saveSessionState(context.Background(), idleState))

	idleOriginalStepCount := idleState.StepCount

	// --- Make a commit with an unrelated file ---
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other-work.txt"), []byte("other work"), 0o644))
	_, err = wt.Add("other-work.txt")
	require.NoError(t, err)

	cpID := "f1f2f3f4f5f6"
	commitMsg := "other work\n\n" + trailers.CheckpointTrailerKey + ": " + cpID + "\n"
	_, err = wt.Commit(commitMsg, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// --- Verify: IDLE session with no files was NOT condensed ---
	idleState, err = s.loadSessionState(context.Background(), idleSessionID)
	require.NoError(t, err)

	assert.Equal(t, idleOriginalStepCount, idleState.StepCount,
		"IDLE session with empty FilesTouched should NOT be condensed")
	assert.Equal(t, session.PhaseIdle, idleState.Phase,
		"IDLE session should remain IDLE")
	// BaseCommit is NOT updated for non-ACTIVE sessions (updateBaseCommitIfChanged skips them)
}

// TestPostCommit_IdleSession_NoTranscriptFallbackForCarryForward verifies that
// carry-forward computation for IDLE sessions does NOT fall back to transcript
// extraction. Only ACTIVE sessions (mid-session commits before Stop) should parse
// the transcript, because IDLE sessions have FilesTouched populated by SaveStep.
//
// Regression test: resolveFilesTouched unconditionally falls back to transcript
// extraction, but the PostCommit call site must gate it on IsActive().
func TestPostCommit_IdleSession_NoTranscriptFallbackForCarryForward(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}

	// Create an IDLE session with checkpoint
	sessionID := "idle-transcript-guard"
	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseIdle
	// Clear FilesTouched to simulate the edge case
	state.FilesTouched = nil
	// Set transcript info so transcript extraction WOULD find files if called
	state.AgentType = agent.AgentTypeGemini
	transcriptPath := filepath.Join(dir, "idle-transcript.json")
	transcript := `{
  "messages": [
    {"type": "user", "content": [{"text": "create file"}]},
    {"type": "gemini", "content": "", "toolCalls": [{"name": "write_file", "args": {"file_path": "` + filepath.Join(dir, "test.txt") + `"}}]}
  ]
}`
	require.NoError(t, os.WriteFile(transcriptPath, []byte(transcript), 0o644))
	state.TranscriptPath = transcriptPath
	state.CheckpointTranscriptStart = 0
	require.NoError(t, s.saveSessionState(context.Background(), state))

	originalStepCount := state.StepCount

	// Commit the file the transcript references — if transcript extraction
	// ran, it would find overlap and trigger condensation
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "test.txt"), []byte("committed"), 0o644))
	_, err = wt.Add("test.txt")
	require.NoError(t, err)

	cpID := "a1a2a3a4a5a6"
	commitMsg := "commit test.txt\n\n" + trailers.CheckpointTrailerKey + ": " + cpID + "\n"
	_, err = wt.Commit(commitMsg, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify: IDLE session was NOT condensed (transcript fallback was skipped)
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, originalStepCount, state.StepCount,
		"IDLE session should NOT be condensed via transcript fallback — only ACTIVE sessions get transcript extraction for carry-forward")
}

// TestPostCommit_IdleSession_SkipsSentinelWait is a regression test verifying that
// PostCommit for an IDLE session with AgentType=ClaudeCode and a TranscriptPath
// completes quickly without hitting the 3s sentinel timeout in PrepareTranscript.
//
// Before the fix, the transcript extraction functions called PrepareTranscript unconditionally,
// which triggered waitForTranscriptFlush (3s timeout) even for idle/ended sessions
// where the transcript was already fully flushed.
//
// After the fix, PrepareTranscript is only called when state.Phase.IsActive().
func TestPostCommit_IdleSession_SkipsSentinelWait(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-idle-skip-sentinel"

	// Initialize session and save a checkpoint
	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	// Set phase to IDLE, set AgentType to Claude Code, and set TranscriptPath
	// Without TranscriptPath, the PrepareTranscript code path is never reached.
	// Without AgentType=ClaudeCode, the sentinel wait is not triggered.
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseIdle
	state.LastInteractionTime = nil
	state.FilesTouched = []string{"test.txt"}
	state.AgentType = agent.AgentTypeClaudeCode

	// Create a transcript file so PrepareTranscript would be triggered if not guarded
	transcriptFile := filepath.Join(dir, ".trace", "transcript-"+sessionID+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(transcriptFile), 0o755))
	require.NoError(t, os.WriteFile(transcriptFile, []byte(`{"type":"human"}`+"\n"), 0o644))
	state.TranscriptPath = transcriptFile

	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Create a commit WITH the Trace-Checkpoint trailer
	commitWithCheckpointTrailer(t, repo, dir, "a1a2a3a4a5a6")

	// Time PostCommit — before the fix this would take ~3s+ due to sentinel timeout
	start := time.Now()
	err = s.PostCommit(context.Background())
	elapsed := time.Since(start)
	require.NoError(t, err)

	// Assert it completes well under the 3s sentinel timeout.
	// Normal PostCommit for these tests runs in <500ms (git operations only).
	assert.Less(t, elapsed, 2*time.Second,
		"IDLE session PostCommit should skip sentinel wait and complete in <2s, took %v", elapsed)

	// Verify condensation still happened correctly
	sessionsRef, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.NoError(t, err, "trace/checkpoints/v1 branch should exist after condensation")
	assert.NotNil(t, sessionsRef)
}
