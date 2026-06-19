package strategy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/GrayCodeAI/trace/cli/trailers"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"
)

func TestShadowStrategy_PrepareCommitMsg_SkipSources(t *testing.T) {
	// Tests that merge, squash, and commit sources are skipped
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	commitMsgFile := filepath.Join(dir, "COMMIT_MSG")
	originalMsg := "Merge branch 'feature'\n"

	s := NewManualCommitStrategy()

	skipSources := []string{"merge", "squash", "commit"}
	for _, source := range skipSources {
		t.Run(source, func(t *testing.T) {
			if err := os.WriteFile(commitMsgFile, []byte(originalMsg), 0o644); err != nil {
				t.Fatalf("failed to write commit message file: %v", err)
			}

			prepErr := s.PrepareCommitMsg(context.Background(), commitMsgFile, source)
			if prepErr != nil {
				t.Errorf("PrepareCommitMsg() error = %v", prepErr)
			}

			// Message should be unchanged for these sources
			content, readErr := os.ReadFile(commitMsgFile)
			if readErr != nil {
				t.Fatalf("failed to read commit message file: %v", readErr)
			}
			if string(content) != originalMsg {
				t.Errorf("PrepareCommitMsg(source=%q) modified message: got %q, want %q",
					source, content, originalMsg)
			}
		})
	}
}

func TestShadowStrategy_PrepareCommitMsg_SkipsSessionWhenContentCheckFails(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)
	t.Setenv("TRACE_TEST_TTY", "1")

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	s := &ManualCommitStrategy{}

	err = s.InitializeSession(context.Background(), "test-session-corrupt-shadow", agent.AgentTypeClaudeCode, "", "", "")
	require.NoError(t, err)

	state, err := s.loadSessionState(context.Background(), "test-session-corrupt-shadow")
	require.NoError(t, err)
	require.NotNil(t, state)

	shadowBranch := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)
	corruptRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(shadowBranch), plumbing.ZeroHash)
	require.NoError(t, repo.Storer.SetReference(corruptRef))

	commitMsgFile := filepath.Join(t.TempDir(), "COMMIT_EDITMSG")
	originalMsg := "Test commit\n"
	require.NoError(t, os.WriteFile(commitMsgFile, []byte(originalMsg), 0o644))

	err = s.PrepareCommitMsg(context.Background(), commitMsgFile, "")
	require.NoError(t, err)

	content, err := os.ReadFile(commitMsgFile)
	require.NoError(t, err)

	_, found := trailers.ParseCheckpoint(string(content))
	require.False(t, found, "corrupt session state should not add a dangling checkpoint trailer")
	require.Equal(t, originalMsg, string(content))
}

func TestAddCheckpointTrailer_NoComment(t *testing.T) {
	// Test that addCheckpointTrailer adds trailer without any comment lines
	message := "Test commit message\n" //nolint:goconst // already present in codebase

	result := addCheckpointTrailer(message, testTrailerCheckpointID)

	// Should contain the trailer
	if !strings.Contains(result, trailers.CheckpointTrailerKey+": "+testTrailerCheckpointID.String()) {
		t.Errorf("addCheckpointTrailer() missing trailer, got: %q", result)
	}

	// Should NOT contain comment lines
	if strings.Contains(result, "# Remove the Trace-Checkpoint") {
		t.Errorf("addCheckpointTrailer() should not contain comment, got: %q", result)
	}
}

func TestAddCheckpointTrailerWithComment_HasComment(t *testing.T) {
	// Test that addCheckpointTrailerWithComment includes the explanatory comment
	message := "Test commit message\n"

	result := addCheckpointTrailerWithComment(message, testTrailerCheckpointID, "Claude Code", "add password hashing")

	// Should contain the trailer
	if !strings.Contains(result, trailers.CheckpointTrailerKey+": "+testTrailerCheckpointID.String()) {
		t.Errorf("addCheckpointTrailerWithComment() missing trailer, got: %q", result)
	}

	// Should contain comment lines with agent name (before prompt)
	if !strings.Contains(result, "# Remove the Trace-Checkpoint") {
		t.Errorf("addCheckpointTrailerWithComment() should contain comment, got: %q", result)
	}
	if !strings.Contains(result, "Claude Code session context") {
		t.Errorf("addCheckpointTrailerWithComment() should contain agent name in comment, got: %q", result)
	}

	// Should contain prompt line (after removal comment)
	if !strings.Contains(result, "# Last Prompt: add password hashing") {
		t.Errorf("addCheckpointTrailerWithComment() should contain prompt, got: %q", result)
	}

	// Verify order: Remove comment should come before Last Prompt
	removeIdx := strings.Index(result, "# Remove the Trace-Checkpoint")
	promptIdx := strings.Index(result, "# Last Prompt:")
	if promptIdx < removeIdx {
		t.Errorf("addCheckpointTrailerWithComment() prompt should come after remove comment, got: %q", result)
	}
}

func TestAddCheckpointTrailerWithComment_NoPrompt(t *testing.T) {
	// Test that addCheckpointTrailerWithComment works without a prompt
	message := "Test commit message\n"

	result := addCheckpointTrailerWithComment(message, testTrailerCheckpointID, "Claude Code", "")

	// Should contain the trailer
	if !strings.Contains(result, trailers.CheckpointTrailerKey+": "+testTrailerCheckpointID.String()) {
		t.Errorf("addCheckpointTrailerWithComment() missing trailer, got: %q", result)
	}

	// Should NOT contain prompt line when prompt is empty
	if strings.Contains(result, "# Last Prompt:") {
		t.Errorf("addCheckpointTrailerWithComment() should not contain prompt line when empty, got: %q", result)
	}

	// Should still contain the removal comment
	if !strings.Contains(result, "# Remove the Trace-Checkpoint") {
		t.Errorf("addCheckpointTrailerWithComment() should contain comment, got: %q", result)
	}
}

func TestAddCheckpointTrailer_ConventionalCommitSubject(t *testing.T) {
	t.Parallel()

	// Regression: single-line conventional commit subjects like "docs: Add foo"
	// contain ": " which falsely triggered the "already has trailers" detection,
	// causing the trailer to be appended without a blank line separator.
	tests := []struct {
		name    string
		message string
	}{
		{
			name:    "conventional commit docs",
			message: "docs: Add red.md with information about the color red\n",
		},
		{
			name:    "conventional commit feat",
			message: "feat: Add new login flow\n",
		},
		{
			name:    "conventional commit fix with scope",
			message: "fix(auth): Resolve token expiry issue\n",
		},
		{
			name:    "single line no newline",
			message: "docs: Add something",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := addCheckpointTrailer(tt.message, testTrailerCheckpointID)

			// The trailer must be separated from the subject by a blank line
			if !strings.Contains(result, "\n\n"+trailers.CheckpointTrailerKey+":") {
				t.Errorf("addCheckpointTrailer() trailer not separated by blank line from subject.\ngot: %q", result)
			}
		})
	}
}

func TestAddCheckpointTrailer_ExistingTrailers(t *testing.T) {
	t.Parallel()

	// When a message already has trailers (in a separate paragraph), the
	// new trailer should be appended directly (no extra blank line).
	message := "feat: Add login\n\nSigned-off-by: Test User <test@example.com>\n"
	result := addCheckpointTrailer(message, testTrailerCheckpointID)

	// Should NOT add a double blank line before our trailer
	if strings.Contains(result, "\n\n"+trailers.CheckpointTrailerKey) {
		t.Errorf("addCheckpointTrailer() added extra blank line before existing trailer block.\ngot: %q", result)
	}

	// Should contain both trailers
	if !strings.Contains(result, "Signed-off-by:") {
		t.Errorf("addCheckpointTrailer() lost existing trailer.\ngot: %q", result)
	}
	if !strings.Contains(result, trailers.CheckpointTrailerKey+":") {
		t.Errorf("addCheckpointTrailer() missing our trailer.\ngot: %q", result)
	}
}

func TestShadowStrategy_GetCheckpointLog_WithCheckpointID(t *testing.T) {
	// This test verifies that GetCheckpointLog correctly uses the checkpoint ID
	// to look up the log. Since getCheckpointLog requires a full git setup
	// with trace/checkpoints/v1 branch, we test the lookup logic by checking error behavior.

	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	s := NewManualCommitStrategy()

	// Checkpoint with checkpoint ID (12 hex chars)
	checkpoint := Checkpoint{
		CheckpointID: "a1b2c3d4e5f6",
		Message:      "Checkpoint: a1b2c3d4e5f6",
		Timestamp:    time.Now(),
	}

	// This should attempt to call getCheckpointLog (which will fail because
	// there's no trace/checkpoints/v1 branch), but the important thing is it uses
	// the checkpoint ID to look up metadata
	_, err = s.GetCheckpointLog(context.Background(), checkpoint)
	if err == nil {
		t.Error("GetCheckpointLog() expected error (no sessions branch), got nil")
	}
	// The error should be about sessions branch, not about parsing
	if err != nil && err.Error() != "sessions branch not found" {
		t.Logf("GetCheckpointLog() error = %v (expected sessions branch error)", err)
	}
}

func TestShadowStrategy_GetCheckpointLog_NoCheckpointID(t *testing.T) {
	// Test that checkpoints without checkpoint ID return ErrNoMetadata
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	s := NewManualCommitStrategy()

	// Checkpoint without checkpoint ID
	checkpoint := Checkpoint{
		CheckpointID: "",
		Message:      "Some other message",
		Timestamp:    time.Now(),
	}

	// This should return ErrNoMetadata since there's no checkpoint ID
	_, err = s.GetCheckpointLog(context.Background(), checkpoint)
	if err == nil {
		t.Error("GetCheckpointLog() expected error for missing checkpoint ID, got nil")
	}
	if !errors.Is(err, ErrNoMetadata) {
		t.Errorf("GetCheckpointLog() expected ErrNoMetadata, got %v", err)
	}
}

func TestShadowStrategy_FilesTouched_OnlyModifiedFiles(t *testing.T) {
	// This test verifies that files_touched only contains files that were actually
	// modified during the session, not ALL files in the repository.
	//
	// The fix tracks files in SessionState.FilesTouched as they are modified,
	// rather than collecting all files from the shadow branch tree.

	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create initial commit with multiple pre-existing files
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create 3 pre-existing files that should NOT be in files_touched
	preExistingFiles := []string{"existing1.txt", "existing2.txt", "existing3.txt"}
	for _, f := range preExistingFiles {
		filePath := filepath.Join(dir, f)
		if err := os.WriteFile(filePath, []byte("original content of "+f), 0o644); err != nil {
			t.Fatalf("failed to write file %s: %v", f, err)
		}
		if _, err := worktree.Add(f); err != nil {
			t.Fatalf("failed to add file %s: %v", f, err)
		}
	}

	_, err = worktree.Commit("Initial commit with pre-existing files", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Chdir(dir)

	s := &ManualCommitStrategy{}
	sessionID := "2025-01-15-test-session-123"

	// Create metadata directory with a transcript
	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	if err := os.MkdirAll(metadataDirAbs, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}

	// Write transcript file (minimal valid JSONL)
	transcript := `{"type":"human","message":{"content":"modify existing1.txt"}}
{"type":"assistant","message":{"content":"I'll modify existing1.txt for you."}}
`
	if err := os.WriteFile(filepath.Join(metadataDirAbs, paths.TranscriptFileName), []byte(transcript), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// First checkpoint using SaveStep - captures ALL working directory files
	// (for rewind purposes), but tracks only modified files in FilesTouched
	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{}, // No files modified yet
		NewFiles:       []string{},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint 1",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
	})
	if err != nil {
		t.Fatalf("SaveStep() error = %v", err)
	}

	// Now simulate a second checkpoint where ONLY existing1.txt is modified
	// (but NOT existing2.txt or existing3.txt)
	modifiedContent := []byte("MODIFIED content of existing1.txt")
	if err := os.WriteFile(filepath.Join(dir, "existing1.txt"), modifiedContent, 0o644); err != nil {
		t.Fatalf("failed to modify existing1.txt: %v", err)
	}

	// Second checkpoint using SaveStep - only modified file should be tracked
	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{"existing1.txt"}, // Only this file was modified
		NewFiles:       []string{},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint 2",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
	})
	if err != nil {
		t.Fatalf("SaveStep() error = %v", err)
	}

	// Load session state to verify FilesTouched
	state, err := s.loadSessionState(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("loadSessionState() error = %v", err)
	}

	// Now condense the session
	checkpointID := id.MustCheckpointID("a1b2c3d4e5f6")
	result, err := s.CondenseSession(context.Background(), repo, checkpointID, state, nil)
	if err != nil {
		t.Fatalf("CondenseSession() error = %v", err)
	}

	// Verify that files_touched only contains the file that was actually modified
	expectedFilesTouched := []string{"existing1.txt"}

	// Check what we actually got
	if len(result.FilesTouched) != len(expectedFilesTouched) {
		t.Errorf("FilesTouched contains %d files, want %d.\nGot: %v\nWant: %v",
			len(result.FilesTouched), len(expectedFilesTouched),
			result.FilesTouched, expectedFilesTouched)
	}

	// Verify the exact content
	filesTouchedMap := make(map[string]bool)
	for _, f := range result.FilesTouched {
		filesTouchedMap[f] = true
	}

	// Check that ONLY the modified file is in files_touched
	for _, expected := range expectedFilesTouched {
		if !filesTouchedMap[expected] {
			t.Errorf("Expected file %q to be in files_touched, but it was not. Got: %v", expected, result.FilesTouched)
		}
	}

	// Check that pre-existing unmodified files are NOT in files_touched
	unmodifiedFiles := []string{"existing2.txt", "existing3.txt"}
	for _, unmodified := range unmodifiedFiles {
		if filesTouchedMap[unmodified] {
			t.Errorf("File %q should NOT be in files_touched (it was not modified during the session), but it was included. Got: %v",
				unmodified, result.FilesTouched)
		}
	}
}

// TestDeleteShadowBranch verifies that deleteShadowBranch correctly deletes a shadow branch.
func TestDeleteShadowBranch(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init repo: %v", err)
	}

	t.Chdir(dir)

	// Create a dummy commit to use as branch target
	emptyTreeHash := plumbing.NewHash("4b825dc642cb6eb9a060e54bf8d69288fbee4904")
	dummyCommitHash, err := checkpoint.CreateCommit(context.Background(), repo, emptyTreeHash, plumbing.ZeroHash, "dummy commit", "test", "test@test.com")
	if err != nil {
		t.Fatalf("failed to create dummy commit: %v", err)
	}

	// Create a shadow branch
	shadowBranchName := "trace/abc1234"
	refName := plumbing.NewBranchReferenceName(shadowBranchName)
	ref := plumbing.NewHashReference(refName, dummyCommitHash)
	if err := repo.Storer.SetReference(ref); err != nil {
		t.Fatalf("failed to create shadow branch: %v", err)
	}

	// Verify branch exists
	_, err = repo.Reference(refName, true)
	if err != nil {
		t.Fatalf("shadow branch should exist: %v", err)
	}

	// Delete the shadow branch
	err = deleteShadowBranch(context.Background(), repo, shadowBranchName)
	if err != nil {
		t.Fatalf("deleteShadowBranch() error = %v", err)
	}

	// Verify branch is deleted
	_, err = repo.Reference(refName, true)
	if err == nil {
		t.Error("shadow branch should be deleted, but still exists")
	}
}

// TestDeleteShadowBranch_NonExistent verifies that deleting a non-existent branch is idempotent.
func TestDeleteShadowBranch_NonExistent(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init repo: %v", err)
	}

	t.Chdir(dir)

	// Try to delete a branch that doesn't exist - should not error
	err = deleteShadowBranch(context.Background(), repo, "trace/nonexistent")
	if err != nil {
		t.Errorf("deleteShadowBranch() for non-existent branch should not error, got: %v", err)
	}
}

// TestSessionState_LastCheckpointID verifies that LastCheckpointID is persisted correctly.
func TestSessionState_LastCheckpointID(t *testing.T) {
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	s := &ManualCommitStrategy{}

	// Create session state with LastCheckpointID
	state := &SessionState{
		SessionID:        "test-session-123",
		BaseCommit:       "abc123def456",
		StartedAt:        time.Now(),
		StepCount:        5,
		LastCheckpointID: "a1b2c3d4e5f6",
	}

	// Save state
	err = s.saveSessionState(context.Background(), state)
	if err != nil {
		t.Fatalf("saveSessionState() error = %v", err)
	}

	// Load state and verify LastCheckpointID
	loaded, err := s.loadSessionState(context.Background(), "test-session-123")
	if err != nil {
		t.Fatalf("loadSessionState() error = %v", err)
	}
	require.NotNil(t, loaded, "loadSessionState() returned nil")

	if loaded.LastCheckpointID != state.LastCheckpointID {
		t.Errorf("LastCheckpointID = %q, want %q", loaded.LastCheckpointID, state.LastCheckpointID)
	}
}

// TestSessionState_TokenUsagePersistence verifies that token usage fields are persisted correctly
// across session state save/load cycles. This is critical for tracking token usage in the
// manual-commit strategy where session state is persisted to disk between checkpoints.
func TestSessionState_TokenUsagePersistence(t *testing.T) {
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	s := &ManualCommitStrategy{}

	// Create session state with token usage fields
	state := &SessionState{
		SessionID:                   "test-session-token-usage",
		BaseCommit:                  "abc123def456",
		StartedAt:                   time.Now(),
		StepCount:                   5,
		CheckpointTranscriptStart:   42,
		TranscriptIdentifierAtStart: "test-uuid-abc123",
		TokenUsage: &agent.TokenUsage{
			InputTokens:         1000,
			CacheCreationTokens: 200,
			CacheReadTokens:     300,
			OutputTokens:        500,
			APICallCount:        5,
		},
	}

	// Save state
	err = s.saveSessionState(context.Background(), state)
	if err != nil {
		t.Fatalf("saveSessionState() error = %v", err)
	}

	// Load state and verify token usage fields are persisted
	loaded, err := s.loadSessionState(context.Background(), "test-session-token-usage")
	if err != nil {
		t.Fatalf("loadSessionState() error = %v", err)
	}
	require.NotNil(t, loaded, "loadSessionState() returned nil")

	// Verify CheckpointTranscriptStart
	if loaded.CheckpointTranscriptStart != state.CheckpointTranscriptStart {
		t.Errorf("CheckpointTranscriptStart = %d, want %d", loaded.CheckpointTranscriptStart, state.CheckpointTranscriptStart)
	}

	// Verify TranscriptIdentifierAtStart
	if loaded.TranscriptIdentifierAtStart != state.TranscriptIdentifierAtStart {
		t.Errorf("TranscriptIdentifierAtStart = %q, want %q", loaded.TranscriptIdentifierAtStart, state.TranscriptIdentifierAtStart)
	}

	// Verify TokenUsage
	if loaded.TokenUsage == nil {
		t.Fatal("TokenUsage should be persisted, got nil")
	}
	if loaded.TokenUsage.InputTokens != state.TokenUsage.InputTokens {
		t.Errorf("TokenUsage.InputTokens = %d, want %d", loaded.TokenUsage.InputTokens, state.TokenUsage.InputTokens)
	}
	if loaded.TokenUsage.CacheCreationTokens != state.TokenUsage.CacheCreationTokens {
		t.Errorf("TokenUsage.CacheCreationTokens = %d, want %d", loaded.TokenUsage.CacheCreationTokens, state.TokenUsage.CacheCreationTokens)
	}
	if loaded.TokenUsage.CacheReadTokens != state.TokenUsage.CacheReadTokens {
		t.Errorf("TokenUsage.CacheReadTokens = %d, want %d", loaded.TokenUsage.CacheReadTokens, state.TokenUsage.CacheReadTokens)
	}
	if loaded.TokenUsage.OutputTokens != state.TokenUsage.OutputTokens {
		t.Errorf("TokenUsage.OutputTokens = %d, want %d", loaded.TokenUsage.OutputTokens, state.TokenUsage.OutputTokens)
	}
	if loaded.TokenUsage.APICallCount != state.TokenUsage.APICallCount {
		t.Errorf("TokenUsage.APICallCount = %d, want %d", loaded.TokenUsage.APICallCount, state.TokenUsage.APICallCount)
	}
}

// TestShadowStrategy_PrepareCommitMsg_ReusesLastCheckpointID verifies that PrepareCommitMsg
// reuses the LastCheckpointID when there's no new content to condense.
func TestShadowStrategy_PrepareCommitMsg_ReusesLastCheckpointID(t *testing.T) {
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
	initialCommit, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Chdir(dir)

	s := &ManualCommitStrategy{}

	// Create session state with LastCheckpointID but no new content
	// (simulating state after first commit with condensation)
	state := &SessionState{
		SessionID:                 "test-session",
		BaseCommit:                initialCommit.String(),
		WorktreePath:              dir,
		StartedAt:                 time.Now(),
		StepCount:                 1,
		CheckpointTranscriptStart: 10, // Already condensed
		LastCheckpointID:          testTrailerCheckpointID,
	}
	if err := s.saveSessionState(context.Background(), state); err != nil {
		t.Fatalf("saveSessionState() error = %v", err)
	}

	// Note: We can't fully test PrepareCommitMsg without setting up a shadow branch
	// with transcript, but we can verify the session state has LastCheckpointID set
	// The actual behavior is tested through integration tests

	// Verify the state was saved correctly
	loaded, err := s.loadSessionState(context.Background(), "test-session")
	if err != nil {
		t.Fatalf("loadSessionState() error = %v", err)
	}
	if loaded.LastCheckpointID != testTrailerCheckpointID {
		t.Errorf("LastCheckpointID = %q, want %q", loaded.LastCheckpointID, testTrailerCheckpointID)
	}
}

func TestParsePostRewritePairs(t *testing.T) {
	pairs, err := parsePostRewritePairs(strings.NewReader("oldsha newsha\n\nold2 new2\n"))
	if err != nil {
		t.Fatalf("parsePostRewritePairs() error = %v", err)
	}
	if len(pairs) != 2 {
		t.Fatalf("len(pairs) = %d, want 2", len(pairs))
	}
	if pairs[0].OldSHA != "oldsha" || pairs[0].NewSHA != "newsha" {
		t.Fatalf("pairs[0] = %+v, want oldsha->newsha", pairs[0])
	}
	if pairs[1].OldSHA != "old2" || pairs[1].NewSHA != "new2" {
		t.Fatalf("pairs[1] = %+v, want old2->new2", pairs[1])
	}
}

func TestParsePostRewritePairs_AllowsOptionalExtraField(t *testing.T) {
	pairs, err := parsePostRewritePairs(strings.NewReader("oldsha newsha extra-info\n"))
	if err != nil {
		t.Fatalf("parsePostRewritePairs() error = %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("len(pairs) = %d, want 1", len(pairs))
	}
	if pairs[0].OldSHA != "oldsha" || pairs[0].NewSHA != "newsha" {
		t.Fatalf("pairs[0] = %+v, want oldsha->newsha", pairs[0])
	}
}

func TestParsePostRewritePairs_InvalidLine(t *testing.T) {
	_, err := parsePostRewritePairs(strings.NewReader("missing-second-column\n"))
	if err == nil {
		t.Fatal("parsePostRewritePairs() error = nil, want error")
	}
}

func TestShadowStrategy_PostRewrite_RemapsMatchingSessionInWorktree(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)
	oldSHA := strings.Repeat("a", 40)
	newSHA := strings.Repeat("b", 40)
	worktreePath, err := paths.WorktreeRoot(context.Background())
	if err != nil {
		t.Fatalf("WorktreeRoot() error = %v", err)
	}

	s := &ManualCommitStrategy{}
	state := &SessionState{
		SessionID:             "session-1",
		BaseCommit:            oldSHA,
		AttributionBaseCommit: oldSHA,
		WorktreePath:          worktreePath,
		StartedAt:             time.Now(),
		LastCheckpointID:      testTrailerCheckpointID,
	}
	if err := s.saveSessionState(context.Background(), state); err != nil {
		t.Fatalf("saveSessionState() error = %v", err)
	}

	if err := s.PostRewrite(context.Background(), "amend", strings.NewReader(oldSHA+" "+newSHA+"\n")); err != nil {
		t.Fatalf("PostRewrite() error = %v", err)
	}

	loaded, err := s.loadSessionState(context.Background(), state.SessionID)
	if err != nil {
		t.Fatalf("loadSessionState() error = %v", err)
	}
	if loaded.BaseCommit != newSHA {
		t.Fatalf("BaseCommit = %q, want %q", loaded.BaseCommit, newSHA)
	}
	if loaded.AttributionBaseCommit != newSHA {
		t.Fatalf("AttributionBaseCommit = %q, want %q", loaded.AttributionBaseCommit, newSHA)
	}
	if loaded.LastCheckpointID != testTrailerCheckpointID {
		t.Fatalf("LastCheckpointID = %q, want %q", loaded.LastCheckpointID, testTrailerCheckpointID)
	}
}
