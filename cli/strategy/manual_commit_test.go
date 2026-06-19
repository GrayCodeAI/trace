package strategy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"
)

const testTrailerCheckpointID id.CheckpointID = "a1b2c3d4e5f6"

const testCheckpointsV2SettingsJSON = `{"enabled": true, "strategy": "manual-commit", "strategy_options": {"checkpoints_v2": true}}`

// testTranscriptPromptResponse is a minimal transcript used across strategy tests.
const testTranscriptPromptResponse = "{\"type\":\"human\",\"message\":{\"content\":\"test prompt\"}}\n{\"type\":\"assistant\",\"message\":{\"content\":\"test response\"}}\n"

func TestShadowStrategy_ValidateRepository(t *testing.T) {
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	s := NewManualCommitStrategy()
	err = s.ValidateRepository()
	if err != nil {
		t.Errorf("ValidateRepository() error = %v, want nil", err)
	}
}

func TestShadowStrategy_ValidateRepository_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	s := NewManualCommitStrategy()
	err := s.ValidateRepository()
	if err == nil {
		t.Error("ValidateRepository() error = nil, want error for non-git directory")
	}
}

func TestShadowStrategy_SessionState_SaveLoad(t *testing.T) {
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	s := &ManualCommitStrategy{}

	state := &SessionState{
		SessionID:  "test-session-123",
		BaseCommit: "abc123def456",
		StartedAt:  time.Now(),
		StepCount:  5,
	}

	// Save state
	err = s.saveSessionState(context.Background(), state)
	if err != nil {
		t.Fatalf("saveSessionState() error = %v", err)
	}

	// Verify file exists
	stateFile := filepath.Join(".git", "trace-sessions", "test-session-123.json")
	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		t.Error("session state file not created")
	}

	// Load state
	loaded, err := s.loadSessionState(context.Background(), "test-session-123")
	if err != nil {
		t.Fatalf("loadSessionState() error = %v", err)
	}
	require.NotNil(t, loaded, "loadSessionState() returned nil")

	if loaded.SessionID != state.SessionID {
		t.Errorf("SessionID = %q, want %q", loaded.SessionID, state.SessionID)
	}
	if loaded.BaseCommit != state.BaseCommit {
		t.Errorf("BaseCommit = %q, want %q", loaded.BaseCommit, state.BaseCommit)
	}
	if loaded.StepCount != state.StepCount {
		t.Errorf("StepCount = %d, want %d", loaded.StepCount, state.StepCount)
	}
}

func TestShadowStrategy_SessionState_LoadNonExistent(t *testing.T) {
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	s := &ManualCommitStrategy{}

	loaded, err := s.loadSessionState(context.Background(), "nonexistent-session")
	if err != nil {
		t.Errorf("loadSessionState() error = %v, want nil for nonexistent session", err)
	}
	if loaded != nil {
		t.Error("loadSessionState() returned non-nil for nonexistent session")
	}
}

func TestShadowStrategy_ListAllSessionStates(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	// Create a dummy commit to use as a base for the shadow branch
	emptyTreeHash := plumbing.NewHash("4b825dc642cb6eb9a060e54bf8d69288fbee4904")
	dummyCommitHash, err := checkpoint.CreateCommit(context.Background(), repo, emptyTreeHash, plumbing.ZeroHash, "dummy commit", "test", "test@test.com")
	if err != nil {
		t.Fatalf("failed to create dummy commit: %v", err)
	}

	// Create shadow branch for base commit "abc1234" (needs 7 chars for prefix)
	// Use empty worktreeID since this is simulating the main worktree
	shadowBranch := getShadowBranchNameForCommit("abc1234", "")
	refName := plumbing.NewBranchReferenceName(shadowBranch)
	ref := plumbing.NewHashReference(refName, dummyCommitHash)
	if err := repo.Storer.SetReference(ref); err != nil {
		t.Fatalf("failed to create shadow branch: %v", err)
	}

	s := &ManualCommitStrategy{}

	// Save multiple session states (both with same base commit)
	state1 := &SessionState{
		SessionID:  "session-1",
		BaseCommit: "abc1234",
		StartedAt:  time.Now(),
		StepCount:  1,
	}
	state2 := &SessionState{
		SessionID:  "session-2",
		BaseCommit: "abc1234",
		StartedAt:  time.Now(),
		StepCount:  2,
	}

	if err := s.saveSessionState(context.Background(), state1); err != nil {
		t.Fatalf("saveSessionState() error = %v", err)
	}
	if err := s.saveSessionState(context.Background(), state2); err != nil {
		t.Fatalf("saveSessionState() error = %v", err)
	}

	// List all states
	states, err := s.listAllSessionStates(context.Background())
	if err != nil {
		t.Fatalf("listAllSessionStates() error = %v", err)
	}

	if len(states) != 2 {
		t.Errorf("listAllSessionStates() returned %d states, want 2", len(states))
	}
}

// TestShadowStrategy_ListAllSessionStates_CleansUpStaleSessions tests that
// listAllSessionStates cleans up stale sessions whose shadow branch no longer exists.
// Stale sessions include: pre-state-machine sessions (empty phase), IDLE/ENDED sessions
// that were never condensed. Active sessions and sessions with LastCheckpointID are kept.
func TestShadowStrategy_ListAllSessionStates_CleansUpStaleSessions(t *testing.T) {
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	s := &ManualCommitStrategy{}
	now := time.Now()

	// None of these sessions have shadow branches → cleanup logic applies.

	// Session 1: Pre-state-machine session (empty phase, no checkpoint ID)
	// Should be cleaned up.
	staleEmpty := &SessionState{
		SessionID:  "stale-empty-phase",
		BaseCommit: "aaa1111",
		StartedAt:  now.Add(-24 * time.Hour),
		StepCount:  0,
	}

	// Session 2: IDLE session with no checkpoint ID
	// Should be cleaned up.
	staleIdle := &SessionState{
		SessionID:  "stale-idle",
		BaseCommit: "bbb2222",
		StartedAt:  now.Add(-12 * time.Hour),
		StepCount:  3,
		Phase:      "idle",
	}

	// Session 3: ENDED session with no checkpoint ID
	// Should be cleaned up.
	staleEnded := &SessionState{
		SessionID:  "stale-ended",
		BaseCommit: "ccc3333",
		StartedAt:  now.Add(-6 * time.Hour),
		StepCount:  1,
		Phase:      "ended",
	}

	// Session 4: ACTIVE session with no shadow branch (branch not yet created)
	// Should be KEPT (session is still running).
	activeNoShadow := &SessionState{
		SessionID:  "active-no-shadow",
		BaseCommit: "ddd4444",
		StartedAt:  now,
		StepCount:  0,
		Phase:      "active",
	}

	// Session 5: IDLE session with LastCheckpointID set (already condensed)
	// Should be KEPT (for checkpoint ID reuse).
	condensedIdle := &SessionState{
		SessionID:        "condensed-idle",
		BaseCommit:       "eee5555",
		StartedAt:        now.Add(-1 * time.Hour),
		StepCount:        0,
		Phase:            "idle",
		LastCheckpointID: "a1b2c3d4e5f6",
	}

	for _, state := range []*SessionState{staleEmpty, staleIdle, staleEnded, activeNoShadow, condensedIdle} {
		if err := s.saveSessionState(context.Background(), state); err != nil {
			t.Fatalf("saveSessionState(%s) error = %v", state.SessionID, err)
		}
	}

	states, err := s.listAllSessionStates(context.Background())
	if err != nil {
		t.Fatalf("listAllSessionStates() error = %v", err)
	}

	// Only active-no-shadow and condensed-idle should survive
	if len(states) != 2 {
		var ids []string
		for _, st := range states {
			ids = append(ids, st.SessionID)
		}
		t.Fatalf("listAllSessionStates() returned %d states %v, want 2 [active-no-shadow, condensed-idle]", len(states), ids)
	}

	kept := make(map[string]bool)
	for _, st := range states {
		kept[st.SessionID] = true
	}
	if !kept["active-no-shadow"] {
		t.Error("active session without shadow branch should be kept")
	}
	if !kept["condensed-idle"] {
		t.Error("session with LastCheckpointID should be kept")
	}

	// Verify stale sessions were actually cleared from disk
	for _, staleID := range []string{"stale-empty-phase", "stale-idle", "stale-ended"} {
		loaded, err := LoadSessionState(context.Background(), staleID)
		if err != nil {
			t.Errorf("LoadSessionState(%s) error = %v", staleID, err)
		}
		if loaded != nil {
			t.Errorf("stale session %s should have been cleared from disk", staleID)
		}
	}
}

func TestShadowStrategy_FindSessionsForCommit(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	// Create a dummy commit to use as a base for the shadow branches
	emptyTreeHash := plumbing.NewHash("4b825dc642cb6eb9a060e54bf8d69288fbee4904")
	dummyCommitHash, err := checkpoint.CreateCommit(context.Background(), repo, emptyTreeHash, plumbing.ZeroHash, "dummy commit", "test", "test@test.com")
	if err != nil {
		t.Fatalf("failed to create dummy commit: %v", err)
	}

	// Create shadow branches for base commits "abc1234" and "xyz7890" (7 chars)
	// Use empty worktreeID since this is simulating the main worktree
	for _, baseCommit := range []string{"abc1234", "xyz7890"} {
		shadowBranch := getShadowBranchNameForCommit(baseCommit, "")
		refName := plumbing.NewBranchReferenceName(shadowBranch)
		ref := plumbing.NewHashReference(refName, dummyCommitHash)
		if err := repo.Storer.SetReference(ref); err != nil {
			t.Fatalf("failed to create shadow branch for %s: %v", baseCommit, err)
		}
	}

	s := &ManualCommitStrategy{}

	// Save session states with different base commits
	state1 := &SessionState{
		SessionID:  "session-1",
		BaseCommit: "abc1234",
		StartedAt:  time.Now(),
		StepCount:  1,
	}
	state2 := &SessionState{
		SessionID:  "session-2",
		BaseCommit: "abc1234",
		StartedAt:  time.Now(),
		StepCount:  2,
	}
	state3 := &SessionState{
		SessionID:  "session-3",
		BaseCommit: "xyz7890",
		StartedAt:  time.Now(),
		StepCount:  3,
	}

	for _, state := range []*SessionState{state1, state2, state3} {
		if err := s.saveSessionState(context.Background(), state); err != nil {
			t.Fatalf("saveSessionState() error = %v", err)
		}
	}

	// Find sessions for base commit "abc1234"
	matching, err := s.findSessionsForCommit(context.Background(), "abc1234")
	if err != nil {
		t.Fatalf("findSessionsForCommit() error = %v", err)
	}

	if len(matching) != 2 {
		t.Errorf("findSessionsForCommit() returned %d sessions, want 2", len(matching))
	}

	// Find sessions for base commit "xyz7890"
	matching, err = s.findSessionsForCommit(context.Background(), "xyz7890")
	if err != nil {
		t.Fatalf("findSessionsForCommit() error = %v", err)
	}

	if len(matching) != 1 {
		t.Errorf("findSessionsForCommit() returned %d sessions, want 1", len(matching))
	}

	// Find sessions for nonexistent base commit
	matching, err = s.findSessionsForCommit(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("findSessionsForCommit() error = %v", err)
	}

	if len(matching) != 0 {
		t.Errorf("findSessionsForCommit() returned %d sessions, want 0", len(matching))
	}
}

func TestShadowStrategy_ClearSessionState(t *testing.T) {
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	s := &ManualCommitStrategy{}

	state := &SessionState{
		SessionID:  "test-session",
		BaseCommit: "abc123",
		StartedAt:  time.Now(),
		StepCount:  1,
	}

	// Save state
	if err := s.saveSessionState(context.Background(), state); err != nil {
		t.Fatalf("saveSessionState() error = %v", err)
	}

	// Verify it exists
	loaded, loadErr := s.loadSessionState(context.Background(), "test-session")
	if loadErr != nil {
		t.Fatalf("loadSessionState() error = %v", loadErr)
	}
	if loaded == nil {
		t.Fatal("session state not created")
	}

	// Clear state
	if err := s.clearSessionState(context.Background(), "test-session"); err != nil {
		t.Fatalf("clearSessionState() error = %v", err)
	}

	// Verify it's gone
	loaded, loadErr = s.loadSessionState(context.Background(), "test-session")
	if loadErr != nil {
		t.Fatalf("loadSessionState() error = %v", loadErr)
	}
	if loaded != nil {
		t.Error("session state not cleared")
	}
}

func TestShadowStrategy_GetRewindPoints_NoShadowBranch(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create initial commit
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := worktree.Add("test.txt"); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}
	_, err = worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Chdir(dir)

	s := NewManualCommitStrategy()
	points, err := s.GetRewindPoints(context.Background(), 10)
	if err != nil {
		t.Errorf("GetRewindPoints() error = %v", err)
	}
	if len(points) != 0 {
		t.Errorf("GetRewindPoints() returned %d points, want 0", len(points))
	}
}

func TestShadowStrategy_ListSessions_Empty(t *testing.T) {
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	sessions, err := ListSessions(context.Background())
	if err != nil {
		t.Errorf("ListSessions(context.Background()) error = %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("ListSessions(context.Background()) returned %d sessions, want 0", len(sessions))
	}
}

func TestShadowStrategy_GetSession_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	_, err = GetSession(context.Background(), "nonexistent")
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("GetSession() error = %v, want ErrNoSession", err)
	}
}

func TestShadowStrategy_GetSessionInfo_NoShadowBranch(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create initial commit
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := worktree.Add("test.txt"); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}
	_, err = worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Chdir(dir)

	s := NewManualCommitStrategy()
	_, err = s.GetSessionInfo(context.Background())
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("GetSessionInfo() error = %v, want ErrNoSession", err)
	}
}

func TestShadowStrategy_CanRewind_CleanRepo(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create initial commit
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := worktree.Add("test.txt"); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}
	_, err = worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Chdir(dir)

	s := NewManualCommitStrategy()
	can, reason, err := s.CanRewind(context.Background())
	if err != nil {
		t.Errorf("CanRewind() error = %v", err)
	}
	if !can {
		t.Errorf("CanRewind() = false, want true (clean repo)")
	}
	if reason != "" {
		t.Errorf("CanRewind() reason = %q, want empty", reason)
	}
}

func TestShadowStrategy_CanRewind_DirtyRepo(t *testing.T) {
	// For shadow, CanRewind always returns true because rewinding
	// replaces local changes with checkpoint contents - that's the expected behavior.
	// Users rewind to undo Claude's changes, which are uncommitted by definition.
	// However, it now returns a warning message with diff stats.
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create initial commit
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := worktree.Add("test.txt"); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}
	_, err = worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Make the repo dirty by modifying the file
	if err := os.WriteFile(testFile, []byte("line1\nmodified line2\nline3\nnew line4\n"), 0o644); err != nil {
		t.Fatalf("failed to modify test file: %v", err)
	}

	t.Chdir(dir)

	s := NewManualCommitStrategy()
	can, reason, err := s.CanRewind(context.Background())
	if err != nil {
		t.Errorf("CanRewind() error = %v", err)
	}
	if !can {
		t.Error("CanRewind() = false, want true (shadow always allows rewind)")
	}
	// Now we expect a warning message with diff stats
	if reason == "" {
		t.Error("CanRewind() reason is empty, want warning about uncommitted changes")
	}
	if !strings.Contains(reason, "uncommitted changes will be reverted") {
		t.Errorf("CanRewind() reason = %q, want to contain 'uncommitted changes will be reverted'", reason)
	}
	if !strings.Contains(reason, "test.txt") {
		t.Errorf("CanRewind() reason = %q, want to contain filename 'test.txt'", reason)
	}
}

func TestShadowStrategy_CanRewind_NoRepo(t *testing.T) {
	// Test that CanRewind still returns true even when not in a git repo
	dir := t.TempDir()
	t.Chdir(dir)

	s := NewManualCommitStrategy()
	can, reason, err := s.CanRewind(context.Background())
	if err != nil {
		t.Errorf("CanRewind() error = %v", err)
	}
	if !can {
		t.Error("CanRewind() = false, want true (shadow always allows rewind)")
	}
	if reason != "" {
		t.Errorf("CanRewind() reason = %q, want empty string (no repo, no stats)", reason)
	}
}

func TestShadowStrategy_GetTaskCheckpoint_NotTaskCheckpoint(t *testing.T) {
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	s := NewManualCommitStrategy()

	point := RewindPoint{
		ID:               "abc123",
		IsTaskCheckpoint: false,
	}

	_, err = s.GetTaskCheckpoint(context.Background(), point)
	if !errors.Is(err, ErrNotTaskCheckpoint) {
		t.Errorf("GetTaskCheckpoint() error = %v, want ErrNotTaskCheckpoint", err)
	}
}

func TestShadowStrategy_GetTaskCheckpointTranscript_NotTaskCheckpoint(t *testing.T) {
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	s := NewManualCommitStrategy()

	point := RewindPoint{
		ID:               "abc123",
		IsTaskCheckpoint: false,
	}

	_, err = s.GetTaskCheckpointTranscript(context.Background(), point)
	if !errors.Is(err, ErrNotTaskCheckpoint) {
		t.Errorf("GetTaskCheckpointTranscript() error = %v, want ErrNotTaskCheckpoint", err)
	}
}

func TestGetShadowBranchNameForCommit(t *testing.T) {
	// Hash of empty worktreeID (main worktree) is "e3b0c44298"
	mainWorktreeHash := "e3b0c44298"

	tests := []struct {
		name       string
		baseCommit string
		worktreeID string
		want       string
	}{
		{
			name:       "short commit main worktree",
			baseCommit: "abc",
			worktreeID: "",
			want:       "trace/abc-" + mainWorktreeHash,
		},
		{
			name:       "7 char commit main worktree",
			baseCommit: "abc1234",
			worktreeID: "",
			want:       "trace/abc1234-" + mainWorktreeHash,
		},
		{
			name:       "long commit main worktree",
			baseCommit: "abc1234567890",
			worktreeID: "",
			want:       "trace/abc123456789-" + mainWorktreeHash,
		},
		{
			name:       "with linked worktree",
			baseCommit: "abc1234",
			worktreeID: "feature-branch",
			want:       "trace/abc1234-" + checkpoint.HashWorktreeID("feature-branch"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getShadowBranchNameForCommit(tt.baseCommit, tt.worktreeID)
			if got != tt.want {
				t.Errorf("getShadowBranchNameForCommit(%q, %q) = %q, want %q", tt.baseCommit, tt.worktreeID, got, tt.want)
			}
		})
	}
}

func TestShadowStrategy_PrepareCommitMsg_NoActiveSession(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create initial commit
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := worktree.Add("test.txt"); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}
	_, err = worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Chdir(dir)

	// Create commit message file
	commitMsgFile := filepath.Join(dir, "COMMIT_MSG")
	if err := os.WriteFile(commitMsgFile, []byte("Test commit\n"), 0o644); err != nil {
		t.Fatalf("failed to write commit message file: %v", err)
	}

	s := NewManualCommitStrategy()
	prepErr := s.PrepareCommitMsg(context.Background(), commitMsgFile, "")
	if prepErr != nil {
		t.Errorf("PrepareCommitMsg() error = %v", prepErr)
	}

	// Message should be unchanged (no session)
	content, err := os.ReadFile(commitMsgFile)
	if err != nil {
		t.Fatalf("failed to read commit message file: %v", err)
	}
	if string(content) != "Test commit\n" {
		t.Errorf("PrepareCommitMsg() modified message when no session active: %q", content)
	}
}
