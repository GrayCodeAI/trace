package strategy

import (
	"bytes"
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

// TestPostCommit_EndedSession_SkipsSentinelWait is the same regression test as
// TestPostCommit_IdleSession_SkipsSentinelWait but for ENDED phase sessions.
// Both IDLE and ENDED sessions should skip the sentinel wait since their
// transcripts are already fully flushed.
func TestPostCommit_EndedSession_SkipsSentinelWait(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-ended-skip-sentinel"

	// Initialize session and save a checkpoint
	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	// Set phase to ENDED, set AgentType to Claude Code, and set TranscriptPath
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	now := time.Now()
	state.Phase = session.PhaseEnded
	state.EndedAt = &now
	state.FilesTouched = []string{"test.txt"}
	state.AgentType = agent.AgentTypeClaudeCode

	// Create a transcript file so PrepareTranscript would be triggered if not guarded
	transcriptFile := filepath.Join(dir, ".trace", "transcript-"+sessionID+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(transcriptFile), 0o755))
	require.NoError(t, os.WriteFile(transcriptFile, []byte(`{"type":"human"}`+"\n"), 0o644))
	state.TranscriptPath = transcriptFile

	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Create a commit WITH the Trace-Checkpoint trailer
	commitWithCheckpointTrailer(t, repo, dir, "e1e2e3e4e5e6")

	// Time PostCommit — before the fix this would take ~3s+ due to sentinel timeout
	start := time.Now()
	err = s.PostCommit(context.Background())
	elapsed := time.Since(start)
	require.NoError(t, err)

	// Assert it completes well under the 3s sentinel timeout
	assert.Less(t, elapsed, 2*time.Second,
		"ENDED session PostCommit should skip sentinel wait and complete in <2s, took %v", elapsed)

	// Verify condensation still happened correctly
	sessionsRef, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.NoError(t, err, "trace/checkpoints/v1 branch should exist after condensation")
	assert.NotNil(t, sessionsRef)
}

// TestPostCommit_EndedSession_SetsFullyCondensed verifies that an ENDED session
// is marked FullyCondensed after condensation when no carry-forward files remain.
func TestPostCommit_EndedSession_SetsFullyCondensed(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-postcommit-ended-fully-condensed"

	// Initialize session and save a checkpoint
	setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

	// Set phase to ENDED with files touched (the committed file matches shadow branch)
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	now := time.Now()
	state.Phase = session.PhaseEnded
	state.EndedAt = &now
	state.FilesTouched = []string{"test.txt"}
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// Create a commit that includes test.txt — this commits the only touched file,
	// so carry-forward will be empty afterward.
	commitWithCheckpointTrailer(t, repo, dir, "fc01fc01fc01")

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify FullyCondensed is set
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	assert.True(t, state.FullyCondensed,
		"ENDED session with no carry-forward should be marked FullyCondensed")
	assert.Equal(t, session.PhaseEnded, state.Phase)
	assert.Empty(t, state.FilesTouched,
		"FilesTouched should be empty after all files were committed")
}

// TestPostCommit_FullyCondensedEndedSession_SkippedOnNextCommit verifies that
// a FullyCondensed ENDED session is skipped entirely on subsequent commits,
// avoiding redundant shadow branch resolution and condensation attempts.
func TestPostCommit_FullyCondensedEndedSession_SkippedOnNextCommit(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-postcommit-skip-fully-condensed"

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

	// First commit — condenses the ENDED session and marks it FullyCondensed
	commitWithCheckpointTrailer(t, repo, dir, "fc02fc02fc02")
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify it's now fully condensed
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	require.True(t, state.FullyCondensed)

	// Record the LastCheckpointID — this should persist (the reason the session exists)
	lastCPID := state.LastCheckpointID

	// Second commit — the fully-condensed session should be skipped entirely.
	// Create a new file so there's something to commit.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other.txt"), []byte("other"), 0o644))
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add("other.txt")
	require.NoError(t, err)
	commitMsg := "second commit\n\n" + trailers.CheckpointTrailerKey + ": fc03fc03fc03\n"
	_, err = wt.Commit(commitMsg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// Run PostCommit again
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify state is unchanged — the session was skipped, not re-processed
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	assert.True(t, state.FullyCondensed,
		"FullyCondensed should still be true after being skipped")
	assert.Equal(t, session.PhaseEnded, state.Phase)
	assert.Equal(t, lastCPID, state.LastCheckpointID,
		"LastCheckpointID should be preserved across skipped commits")
}

// TestPostCommit_NonEndedSession_NotMarkedFullyCondensed verifies that ACTIVE
// and IDLE sessions are never marked FullyCondensed, even when condensed with
// no carry-forward. Only ENDED sessions get the flag.
func TestPostCommit_NonEndedSession_NotMarkedFullyCondensed(t *testing.T) {
	for _, phase := range []session.Phase{session.PhaseActive, session.PhaseIdle} {
		t.Run(string(phase), func(t *testing.T) {
			dir := setupGitRepo(t)
			t.Chdir(dir)

			repo, err := git.PlainOpen(dir)
			require.NoError(t, err)

			s := &ManualCommitStrategy{}
			sessionID := "test-postcommit-" + string(phase) + "-not-fully-condensed"

			// Initialize session and save a checkpoint
			setupSessionWithCheckpoint(t, s, repo, dir, sessionID)

			state, err := s.loadSessionState(context.Background(), sessionID)
			require.NoError(t, err)
			state.Phase = phase
			state.FilesTouched = []string{"test.txt"}
			require.NoError(t, s.saveSessionState(context.Background(), state))

			// Commit the file
			commitWithCheckpointTrailer(t, repo, dir, "fc04fc04fc04")

			// Run PostCommit
			err = s.PostCommit(context.Background())
			require.NoError(t, err)

			// Verify FullyCondensed is NOT set
			state, err = s.loadSessionState(context.Background(), sessionID)
			require.NoError(t, err)
			assert.False(t, state.FullyCondensed,
				"%s sessions must never be marked FullyCondensed", phase)
		})
	}
}

// TestPostCommit_ActiveSession_DifferentFilesThanCommit_ShouldCondense verifies
// that when an ACTIVE session's Turn 1 touched file A (e.g., a cache file) but
// Turn 2 commits different files B and C, condensation still happens.
//
// This is a regression test for the bug where shouldCondenseWithOverlapCheck
// incorrectly skipped condensation because filesTouchedBefore (from Turn 1)
// didn't overlap with the committed files (from Turn 2). ACTIVE sessions with a
// recent LastInteractionTime should condense when hasNew is true — the overlap
// check is only meaningful for IDLE/ENDED sessions and stale ACTIVE sessions.
func TestPostCommit_ActiveSession_DifferentFilesThanCommit_ShouldCondense(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	sessionID := "test-active-different-files"

	// --- Turn 1: Save checkpoint touching a cache file (not what will be committed) ---
	// Write the cache file so SaveStep can snapshot it
	cacheFile := filepath.Join(dir, ".gitstats_cache.sqlite3")
	require.NoError(t, os.WriteFile(cacheFile, []byte("cache data"), 0o644))

	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	require.NoError(t, os.MkdirAll(metadataDirAbs, 0o755))

	transcript := `{"type":"human","message":{"content":"analyze git stats"}}
{"type":"assistant","message":{"content":"analyzing stats, creating cache"}}
`
	require.NoError(t, os.WriteFile(
		filepath.Join(metadataDirAbs, paths.TranscriptFileName),
		[]byte(transcript), 0o644,
	))

	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{},
		NewFiles:       []string{".gitstats_cache.sqlite3"},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint: cache created",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
	})
	require.NoError(t, err)

	// Set phase to ACTIVE (agent mid-turn) with recent interaction
	state, err := s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)
	state.Phase = session.PhaseActive
	// FilesTouched reflects Turn 1's cache file — NOT the files about to be committed
	state.FilesTouched = []string{".gitstats_cache.sqlite3"}
	now := time.Now()
	state.LastInteractionTime = &now
	require.NoError(t, s.saveSessionState(context.Background(), state))

	// --- Turn 2: Agent commits DIFFERENT files (README.md, org_commit_activity.py) ---
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Git Stats"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "org_commit_activity.py"), []byte("print('hello')"), 0o644))

	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add("README.md")
	require.NoError(t, err)
	_, err = wt.Add("org_commit_activity.py")
	require.NoError(t, err)

	cpID := "d1d2d3d4d5d6"
	commitMsg := "Add git stats tools\n\n" + trailers.CheckpointTrailerKey + ": " + cpID + "\n"
	_, err = wt.Commit(commitMsg, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)

	// --- Run PostCommit ---
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// --- Verify condensation happened ---
	state, err = s.loadSessionState(context.Background(), sessionID)
	require.NoError(t, err)

	// StepCount should be 1 because carry-forward created a new checkpoint for
	// .gitstats_cache.sqlite3 which was NOT committed (remaining agent work)
	assert.Equal(t, 1, state.StepCount,
		"ACTIVE session StepCount should be 1 (carry-forward for uncommitted cache file)")

	// Phase stays ACTIVE
	assert.Equal(t, session.PhaseActive, state.Phase,
		"ACTIVE session should stay ACTIVE after condensation")

	// trace/checkpoints/v1 branch should exist
	_, err = repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.NoError(t, err,
		"trace/checkpoints/v1 should exist — ACTIVE session with different files must still condense")
}

// TestPostCommit_EmptyEndedSession_MarkedFullyCondensed verifies that an ENDED
// session with no FilesTouched and no new content (hasNew=false) is marked
// FullyCondensed on the next PostCommit. Without this, empty ENDED sessions
// go through HandleDiscardIfNoFiles (which is a no-op for ENDED) and are
// iterated on every future PostCommit forever.
func TestPostCommit_EmptyEndedSession_MarkedFullyCondensed(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}

	// We need a real session with BaseCommit/WorktreeID to pass PostCommit's
	// session iteration. Use setupSessionWithCheckpoint to create the plumbing,
	// then create a separate empty ENDED session sharing the same base commit.
	helperSessionID := "helper-session"
	setupSessionWithCheckpoint(t, s, repo, dir, helperSessionID)

	helperState, err := s.loadSessionState(context.Background(), helperSessionID)
	require.NoError(t, err)

	// Create the empty ENDED session — no files, no steps, no shadow branch content
	emptySessionID := "empty-ended-session"
	endedAt := time.Now().Add(-2 * time.Hour)
	emptyState := &SessionState{
		SessionID:    emptySessionID,
		BaseCommit:   helperState.BaseCommit,
		WorktreePath: helperState.WorktreePath,
		WorktreeID:   helperState.WorktreeID,
		StartedAt:    time.Now().Add(-3 * time.Hour),
		Phase:        session.PhaseEnded,
		EndedAt:      &endedAt,
		FilesTouched: nil,
		StepCount:    0,
	}
	require.NoError(t, s.saveSessionState(context.Background(), emptyState))

	// Create a commit with checkpoint trailer
	commitWithCheckpointTrailer(t, repo, dir, "e1e2e3e4e5e6")

	// Run PostCommit
	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	// Verify: empty ENDED session should be marked FullyCondensed
	state, err := s.loadSessionState(context.Background(), emptySessionID)
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.True(t, state.FullyCondensed,
		"ENDED session with no files and no new content should be marked FullyCondensed")
	assert.Equal(t, session.PhaseEnded, state.Phase,
		"Phase should stay ENDED")
}

// TestCountWarnableStaleEndedSessions verifies that the warning only counts the
// same ENDED sessions that 'trace doctor' can actually condense.
// Uses t.Chdir — do NOT add t.Parallel().
func TestCountWarnableStaleEndedSessions(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	setupSessionWithCheckpoint(t, s, repo, dir, "warnable-session")

	warnableState, err := s.loadSessionState(context.Background(), "warnable-session")
	require.NoError(t, err)
	warnableState.Phase = session.PhaseEnded
	warnableState.FullyCondensed = false
	require.NoError(t, s.saveSessionState(context.Background(), warnableState))

	sessions := []*SessionState{
		warnableState,
		{
			SessionID:      "no-shadow-branch",
			BaseCommit:     "1234567890abcdef1234567890abcdef12345678",
			WorktreeID:     warnableState.WorktreeID,
			Phase:          session.PhaseEnded,
			FullyCondensed: false,
			StepCount:      3,
		},
		{
			SessionID:      "zero-steps",
			BaseCommit:     warnableState.BaseCommit,
			WorktreeID:     warnableState.WorktreeID,
			Phase:          session.PhaseEnded,
			FullyCondensed: false,
			StepCount:      0,
		},
		{
			SessionID:      "fully-condensed",
			BaseCommit:     warnableState.BaseCommit,
			WorktreeID:     warnableState.WorktreeID,
			Phase:          session.PhaseEnded,
			FullyCondensed: true,
			StepCount:      3,
		},
		{
			SessionID:      "idle-session",
			BaseCommit:     warnableState.BaseCommit,
			WorktreeID:     warnableState.WorktreeID,
			Phase:          session.PhaseIdle,
			FullyCondensed: false,
			StepCount:      3,
		},
	}

	assert.Equal(t, 1, countWarnableStaleEndedSessions(repo, sessions))
}

// TestPostCommit_WarnStaleEndedSessions_AfterProcessing verifies that the
// warning is emitted only for sessions that remain stale AFTER the current
// commit is processed.
// Uses t.Chdir — do NOT add t.Parallel().
func TestPostCommit_WarnStaleEndedSessions_AfterProcessing(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}
	type sessionFile struct {
		sessionID string
		fileName  string
	}
	sessionFiles := []sessionFile{
		{"ended-a", "stale-a.txt"},
		{"ended-b", "stale-b.txt"},
		{"ended-c", "stale-c.txt"},
	}

	filesToCommit := make([]string, 0, len(sessionFiles))
	for _, sf := range sessionFiles {
		setupSessionWithCheckpointAndFile(t, s, dir, sf.sessionID, sf.fileName)

		state, loadErr := s.loadSessionState(context.Background(), sf.sessionID)
		require.NoError(t, loadErr)
		now := time.Now()
		state.Phase = session.PhaseEnded
		state.EndedAt = &now
		state.FilesTouched = []string{sf.fileName}
		require.NoError(t, s.saveSessionState(context.Background(), state))

		filesToCommit = append(filesToCommit, sf.fileName)
	}

	commitFilesWithTrailer(t, repo, dir, "abc123def456", filesToCommit...)

	// Capture warning output via the injectable stderrWriter instead of
	// mutating the process-global os.Stderr.
	var buf bytes.Buffer
	oldWriter := stderrWriter
	stderrWriter = &buf
	defer func() { stderrWriter = oldWriter }()

	err = s.PostCommit(context.Background())
	require.NoError(t, err)

	assert.NotContains(t, buf.String(), "trace doctor",
		"warning should be suppressed when this commit already condensed the stale ended sessions")
}

// TestWarnStaleEndedSessions_RateLimit verifies the 24h sentinel file gate.
// Uses t.Chdir — do NOT add t.Parallel().
func TestWarnStaleEndedSessions_RateLimit(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)
	ctx := context.Background()

	// First call: no sentinel file → should write to stderr
	var buf bytes.Buffer
	warnStaleEndedSessionsTo(ctx, 5, &buf)
	assert.Contains(t, buf.String(), "trace doctor")

	// Sentinel file now exists with current mtime → second call suppressed
	buf.Reset()
	warnStaleEndedSessionsTo(ctx, 5, &buf)
	assert.Empty(t, buf.String(), "second call within window must be suppressed")

	// Backdate sentinel file by 25h → call should warn again
	commonDir, err := GetGitCommonDir(ctx)
	require.NoError(t, err)
	warnFile := filepath.Join(commonDir, session.SessionStateDirName, staleEndedSessionWarnFile)
	past := time.Now().Add(-25 * time.Hour)
	require.NoError(t, os.Chtimes(warnFile, past, past))

	buf.Reset()
	warnStaleEndedSessionsTo(ctx, 5, &buf)
	assert.Contains(t, buf.String(), "trace doctor")
}
