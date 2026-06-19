package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// TestUpdateSummary_NotFound verifies that UpdateSummary returns an error
// when the checkpoint doesn't exist.
func TestUpdateSummary_NotFound(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)

	// Ensure sessions branch exists
	err := store.ensureSessionsBranch(context.Background())
	if err != nil {
		t.Fatalf("ensureSessionsBranch() error = %v", err)
	}

	// Try to update a non-existent checkpoint (ID must be 12 hex chars)
	checkpointID := id.MustCheckpointID("000000000000")
	summary := &Summary{Intent: "Test", Outcome: "Test"}

	err = store.UpdateSummary(context.Background(), checkpointID, summary)
	if err == nil {
		t.Error("UpdateSummary() should return error for non-existent checkpoint")
	}
	if !errors.Is(err, ErrCheckpointNotFound) {
		t.Errorf("UpdateSummary() error = %v, want ErrCheckpointNotFound", err)
	}
}

// TestListCommitted_FallsBackToRemote verifies that ListCommitted can find
// checkpoints when only origin/trace/checkpoints/v1 exists (simulating post-clone state).
func TestListCommitted_FallsBackToRemote(t *testing.T) {
	// Create "remote" repo (non-bare, so we can make commits)
	remoteDir := t.TempDir()
	remoteRepo, err := git.PlainInit(remoteDir, false)
	if err != nil {
		t.Fatalf("failed to init remote repo: %v", err)
	}

	// Create an initial commit on main branch (required for cloning)
	remoteWorktree, err := remoteRepo.Worktree()
	if err != nil {
		t.Fatalf("failed to get remote worktree: %v", err)
	}
	readmeFile := filepath.Join(remoteDir, "README.md")
	if err := os.WriteFile(readmeFile, []byte("# Test"), 0o644); err != nil {
		t.Fatalf("failed to write README: %v", err)
	}
	if _, err := remoteWorktree.Add("README.md"); err != nil {
		t.Fatalf("failed to add README: %v", err)
	}
	if _, err := remoteWorktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	}); err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create trace/checkpoints/v1 branch on the remote with a checkpoint
	remoteStore := NewGitStore(remoteRepo)
	cpID := id.MustCheckpointID("abcdef123456")
	err = remoteStore.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "test-session-id",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte(`{"test": true}`)),
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	if err != nil {
		t.Fatalf("failed to write checkpoint to remote: %v", err)
	}

	// Clone the repo (this clones main, but not trace/checkpoints/v1 by default)
	localDir := t.TempDir()
	localRepo, err := git.PlainClone(localDir, &git.CloneOptions{
		URL: remoteDir,
	})
	if err != nil {
		t.Fatalf("failed to clone repo: %v", err)
	}

	// Fetch the trace/checkpoints/v1 branch to origin/trace/checkpoints/v1
	// (but don't create local branch - simulating post-clone state)
	refSpec := fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", paths.MetadataBranchName, paths.MetadataBranchName)
	err = localRepo.Fetch(&git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{config.RefSpec(refSpec)},
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		t.Fatalf("failed to fetch trace/checkpoints/v1: %v", err)
	}

	// Verify local branch doesn't exist
	_, err = localRepo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err == nil {
		t.Fatal("local trace/checkpoints/v1 branch should not exist")
	}

	// Verify remote-tracking branch exists
	_, err = localRepo.Reference(plumbing.NewRemoteReferenceName("origin", paths.MetadataBranchName), true)
	if err != nil {
		t.Fatalf("origin/trace/checkpoints/v1 should exist: %v", err)
	}

	// ListCommitted should find the checkpoint by falling back to remote
	localStore := NewGitStore(localRepo)
	checkpoints, err := localStore.ListCommitted(context.Background())
	if err != nil {
		t.Fatalf("ListCommitted() error = %v", err)
	}
	if len(checkpoints) != 1 {
		t.Errorf("ListCommitted() returned %d checkpoints, want 1", len(checkpoints))
	}
	if len(checkpoints) > 0 && checkpoints[0].CheckpointID.String() != cpID.String() {
		t.Errorf("ListCommitted() checkpoint ID = %q, want %q", checkpoints[0].CheckpointID, cpID)
	}
}

// TestGetCheckpointAuthor verifies that GetCheckpointAuthor retrieves the
// author of the commit that created the checkpoint on the trace/checkpoints/v1 branch.
func TestGetCheckpointAuthor(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("a1b2c3d4e5f6")

	// Create a checkpoint with specific author info
	authorName := "Alice Developer"
	authorEmail := "alice@example.com"

	err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID: checkpointID,
		SessionID:    "test-session-author",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte("test transcript")),
		FilesTouched: []string{"main.go"},
		AuthorName:   authorName,
		AuthorEmail:  authorEmail,
	})
	if err != nil {
		t.Fatalf("WriteCommitted() error = %v", err)
	}

	// Retrieve the author
	author, err := store.GetCheckpointAuthor(context.Background(), checkpointID)
	if err != nil {
		t.Fatalf("GetCheckpointAuthor() error = %v", err)
	}

	if author.Name != authorName {
		t.Errorf("author.Name = %q, want %q", author.Name, authorName)
	}
	if author.Email != authorEmail {
		t.Errorf("author.Email = %q, want %q", author.Email, authorEmail)
	}
}

// TestGetCheckpointAuthor_NotFound verifies that GetCheckpointAuthor returns
// empty author when the checkpoint doesn't exist.
func TestGetCheckpointAuthor_NotFound(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)

	// Query for a non-existent checkpoint (must be valid hex)
	checkpointID := id.MustCheckpointID("ffffffffffff")

	author, err := store.GetCheckpointAuthor(context.Background(), checkpointID)
	if err != nil {
		t.Fatalf("GetCheckpointAuthor() error = %v", err)
	}

	// Should return empty author (no error)
	if author.Name != "" || author.Email != "" {
		t.Errorf("expected empty author for non-existent checkpoint, got Name=%q, Email=%q", author.Name, author.Email)
	}
}

// TestGetCheckpointAuthor_NoSessionsBranch verifies that GetCheckpointAuthor
// returns empty author when the trace/checkpoints/v1 branch doesn't exist.
func TestGetCheckpointAuthor_NoSessionsBranch(t *testing.T) {
	// Create a fresh repo without sessions branch
	tempDir := t.TempDir()
	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("aabbccddeeff")

	author, err := store.GetCheckpointAuthor(context.Background(), checkpointID)
	if err != nil {
		t.Fatalf("GetCheckpointAuthor() error = %v", err)
	}

	// Should return empty author (no error)
	if author.Name != "" || author.Email != "" {
		t.Errorf("expected empty author when sessions branch doesn't exist, got Name=%q, Email=%q", author.Name, author.Email)
	}
}

// =============================================================================
// Multi-Session Tests - Tests for checkpoint structure with CheckpointSummary
// at root level and sessions stored in numbered subfolders (0-based: 0/, 1/, 2/)
// =============================================================================

// TestWriteCommitted_MultipleSessionsSameCheckpoint verifies that writing multiple
// sessions to the same checkpoint ID creates separate numbered subdirectories.
func TestWriteCommitted_MultipleSessionsSameCheckpoint(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("a1a2a3a4a5a6")

	// Write first session
	err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "session-one",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"message": "first session"}`)),
		Prompts:          []string{"First prompt"},
		FilesTouched:     []string{"file1.go"},
		CheckpointsCount: 3,
		AuthorName:       "Test Author",
		AuthorEmail:      "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() first session error = %v", err)
	}

	// Write second session to the same checkpoint ID
	err = store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "session-two",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"message": "second session"}`)),
		Prompts:          []string{"Second prompt"},
		FilesTouched:     []string{"file2.go"},
		CheckpointsCount: 2,
		AuthorName:       "Test Author",
		AuthorEmail:      "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() second session error = %v", err)
	}

	// Read the checkpoint summary
	summary, err := store.ReadCommitted(context.Background(), checkpointID)
	if err != nil {
		t.Fatalf("ReadCommitted() error = %v", err)
	}
	if summary == nil {
		t.Fatal("ReadCommitted() returned nil summary")
		return
	}

	// Verify Sessions array has 2 entries
	if len(summary.Sessions) != 2 {
		t.Errorf("len(summary.Sessions) = %d, want 2", len(summary.Sessions))
	}

	// Verify both sessions have correct file paths (0-based indexing)
	if !strings.Contains(summary.Sessions[0].Transcript, "/0/") {
		t.Errorf("session 0 transcript path should contain '/0/', got %s", summary.Sessions[0].Transcript)
	}
	if !strings.Contains(summary.Sessions[1].Transcript, "/1/") {
		t.Errorf("session 1 transcript path should contain '/1/', got %s", summary.Sessions[1].Transcript)
	}

	// Verify session content can be read from each subdirectory
	content0, err := store.ReadSessionContent(context.Background(), checkpointID, 0)
	if err != nil {
		t.Fatalf("ReadSessionContent(0) error = %v", err)
	}
	if content0.Metadata.SessionID != "session-one" {
		t.Errorf("session 0 SessionID = %q, want %q", content0.Metadata.SessionID, "session-one")
	}

	content1, err := store.ReadSessionContent(context.Background(), checkpointID, 1)
	if err != nil {
		t.Fatalf("ReadSessionContent(1) error = %v", err)
	}
	if content1.Metadata.SessionID != "session-two" {
		t.Errorf("session 1 SessionID = %q, want %q", content1.Metadata.SessionID, "session-two")
	}
}

// TestWriteCommitted_Aggregation verifies that CheckpointSummary correctly
// aggregates statistics (CheckpointsCount, FilesTouched, TokenUsage) from
// multiple sessions written to the same checkpoint.
func TestWriteCommitted_Aggregation(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("b1b2b3b4b5b6")

	// Write first session with specific stats
	err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "session-one",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"message": "first"}`)),
		FilesTouched:     []string{"a.go", "b.go"},
		CheckpointsCount: 3,
		TokenUsage: &agent.TokenUsage{
			InputTokens:  100,
			OutputTokens: 50,
			APICallCount: 5,
		},
		AuthorName:  "Test Author",
		AuthorEmail: "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() first session error = %v", err)
	}

	// Write second session with overlapping and new files
	err = store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "session-two",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"message": "second"}`)),
		FilesTouched:     []string{"b.go", "c.go"}, // b.go overlaps
		CheckpointsCount: 2,
		TokenUsage: &agent.TokenUsage{
			InputTokens:  50,
			OutputTokens: 25,
			APICallCount: 3,
		},
		AuthorName:  "Test Author",
		AuthorEmail: "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() second session error = %v", err)
	}

	// Read the checkpoint summary
	summary, err := store.ReadCommitted(context.Background(), checkpointID)
	if err != nil {
		t.Fatalf("ReadCommitted() error = %v", err)
	}
	if summary == nil {
		t.Fatal("ReadCommitted() returned nil summary")
		return
	}

	// Verify aggregated CheckpointsCount = 3 + 2 = 5
	if summary.CheckpointsCount != 5 {
		t.Errorf("summary.CheckpointsCount = %d, want 5", summary.CheckpointsCount)
	}

	// Verify merged FilesTouched = ["a.go", "b.go", "c.go"] (sorted, deduplicated)
	expectedFiles := []string{"a.go", "b.go", "c.go"}
	if len(summary.FilesTouched) != len(expectedFiles) {
		t.Errorf("len(summary.FilesTouched) = %d, want %d", len(summary.FilesTouched), len(expectedFiles))
	}
	for i, want := range expectedFiles {
		if i >= len(summary.FilesTouched) {
			break
		}
		if summary.FilesTouched[i] != want {
			t.Errorf("summary.FilesTouched[%d] = %q, want %q", i, summary.FilesTouched[i], want)
		}
	}

	// Verify aggregated TokenUsage
	if summary.TokenUsage == nil {
		t.Fatal("summary.TokenUsage should not be nil")
	}
	if summary.TokenUsage.InputTokens != 150 {
		t.Errorf("summary.TokenUsage.InputTokens = %d, want 150", summary.TokenUsage.InputTokens)
	}
	if summary.TokenUsage.OutputTokens != 75 {
		t.Errorf("summary.TokenUsage.OutputTokens = %d, want 75", summary.TokenUsage.OutputTokens)
	}
	if summary.TokenUsage.APICallCount != 8 {
		t.Errorf("summary.TokenUsage.APICallCount = %d, want 8", summary.TokenUsage.APICallCount)
	}
}

// TestReadCommitted_ReturnsCheckpointSummary verifies that ReadCommitted returns
// a CheckpointSummary with the correct structure including Sessions array.
func TestReadCommitted_ReturnsCheckpointSummary(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("c1c2c3c4c5c6")

	// Write two sessions
	for i, sessionID := range []string{"session-alpha", "session-beta"} {
		err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
			CheckpointID:     checkpointID,
			SessionID:        sessionID,
			Strategy:         "manual-commit",
			Transcript:       redact.AlreadyRedacted([]byte(fmt.Sprintf(`{"session": %d}`, i))),
			Prompts:          []string{fmt.Sprintf("Prompt %d", i)},
			FilesTouched:     []string{fmt.Sprintf("file%d.go", i)},
			CheckpointsCount: i + 1,
			AuthorName:       "Test Author",
			AuthorEmail:      "test@example.com",
		})
		if err != nil {
			t.Fatalf("WriteCommitted() session %d error = %v", i, err)
		}
	}

	// Read the checkpoint summary
	summary, err := store.ReadCommitted(context.Background(), checkpointID)
	if err != nil {
		t.Fatalf("ReadCommitted() error = %v", err)
	}
	if summary == nil {
		t.Fatal("ReadCommitted() returned nil summary")
		return
	}

	// Verify basic summary fields
	if summary.CheckpointID != checkpointID {
		t.Errorf("summary.CheckpointID = %v, want %v", summary.CheckpointID, checkpointID)
	}
	if summary.Strategy != "manual-commit" {
		t.Errorf("summary.Strategy = %q, want %q", summary.Strategy, "manual-commit")
	}

	// Verify Sessions array
	if len(summary.Sessions) != 2 {
		t.Fatalf("len(summary.Sessions) = %d, want 2", len(summary.Sessions))
	}

	// Verify file paths point to correct locations
	for i, session := range summary.Sessions {
		expectedSubdir := fmt.Sprintf("/%d/", i)
		if !strings.Contains(session.Metadata, expectedSubdir) {
			t.Errorf("session %d Metadata path should contain %q, got %q", i, expectedSubdir, session.Metadata)
		}
		if !strings.Contains(session.Transcript, expectedSubdir) {
			t.Errorf("session %d Transcript path should contain %q, got %q", i, expectedSubdir, session.Transcript)
		}
	}
}

// TestReadSessionContent_ByIndex verifies that ReadSessionContent can read
// specific sessions by their 0-based index within a checkpoint.
func TestReadSessionContent_ByIndex(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("d1d2d3d4d5d6")

	// Write two sessions with distinct content
	sessions := []struct {
		id         string
		transcript string
		prompt     string
	}{
		{"session-first", `{"order": "first"}`, "First user prompt"},
		{"session-second", `{"order": "second"}`, "Second user prompt"},
	}

	for _, s := range sessions {
		err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
			CheckpointID:     checkpointID,
			SessionID:        s.id,
			Strategy:         "manual-commit",
			Transcript:       redact.AlreadyRedacted([]byte(s.transcript)),
			Prompts:          []string{s.prompt},
			CheckpointsCount: 1,
			AuthorName:       "Test Author",
			AuthorEmail:      "test@example.com",
		})
		if err != nil {
			t.Fatalf("WriteCommitted() session %s error = %v", s.id, err)
		}
	}

	// Read session 0
	content0, err := store.ReadSessionContent(context.Background(), checkpointID, 0)
	if err != nil {
		t.Fatalf("ReadSessionContent(0) error = %v", err)
	}
	if content0.Metadata.SessionID != "session-first" {
		t.Errorf("session 0 SessionID = %q, want %q", content0.Metadata.SessionID, "session-first")
	}
	if !strings.Contains(string(content0.Transcript), "first") {
		t.Errorf("session 0 transcript should contain 'first', got %s", string(content0.Transcript))
	}
	if !strings.Contains(content0.Prompts, "First") {
		t.Errorf("session 0 prompts should contain 'First', got %s", content0.Prompts)
	}

	// Read session 1
	content1, err := store.ReadSessionContent(context.Background(), checkpointID, 1)
	if err != nil {
		t.Fatalf("ReadSessionContent(1) error = %v", err)
	}
	if content1.Metadata.SessionID != "session-second" {
		t.Errorf("session 1 SessionID = %q, want %q", content1.Metadata.SessionID, "session-second")
	}
	if !strings.Contains(string(content1.Transcript), "second") {
		t.Errorf("session 1 transcript should contain 'second', got %s", string(content1.Transcript))
	}
}

// writeSingleSession is a test helper that creates a store with a single session
// and returns the store and checkpoint ID for further testing.
func writeSingleSession(t *testing.T, cpIDStr, sessionID, transcript string) (*GitStore, id.CheckpointID) {
	t.Helper()
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID(cpIDStr)

	err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        sessionID,
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(transcript)),
		CheckpointsCount: 1,
		AuthorName:       "Test Author",
		AuthorEmail:      "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() error = %v", err)
	}
	return store, checkpointID
}

func TestWriteCommitted_CodexSanitizesPortableTranscript(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("c0de1234beef")

	transcript := `{"timestamp":"2026-03-25T11:31:11.754Z","type":"response_item","payload":{"type":"reasoning","summary":[{"text":"brief"}],"encrypted_content":"REDACTED"}}
{"timestamp":"2026-03-25T11:31:11.755Z","type":"response_item","payload":{"type":"compaction","encrypted_content":"REDACTED"}}
{"timestamp":"2026-03-25T11:31:11.756Z","type":"compacted","payload":{"message":"","replacement_history":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},{"type":"reasoning","summary":[{"text":"nested"}],"encrypted_content":"REDACTED"},{"type":"compaction","encrypted_content":"REDACTED"},{"type":"compaction_summary","encrypted_content":"REDACTED"}]}}
`

	err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "codex-session",
		Strategy:         "manual-commit",
		Agent:            agent.AgentTypeCodex,
		Transcript:       redact.AlreadyRedacted([]byte(transcript)),
		CheckpointsCount: 1,
		AuthorName:       "Test Author",
		AuthorEmail:      "test@example.com",
	})
	require.NoError(t, err)

	content, err := store.ReadLatestSessionContent(context.Background(), checkpointID)
	require.NoError(t, err)

	got := string(content.Transcript)
	require.NotContains(t, got, `"encrypted_content":"REDACTED"`)
	require.NotContains(t, got, `"type":"compaction"`)
	require.NotContains(t, got, `"type":"compaction_summary"`)
	require.Contains(t, got, `"summary":[{"text":"brief"}]`)
	require.Contains(t, got, `"summary":[{"text":"nested"}]`)
}

// TestReadSessionContent_InvalidIndex verifies that ReadSessionContent returns
// an error when requesting a session index that doesn't exist.
func TestReadSessionContent_InvalidIndex(t *testing.T) {
	store, checkpointID := writeSingleSession(t, "e1e2e3e4e5e6", "only-session", `{"single": true}`)

	// Try to read session index 1 (doesn't exist)
	_, err := store.ReadSessionContent(context.Background(), checkpointID, 1)
	if err == nil {
		t.Error("ReadSessionContent(1) should return error for non-existent session")
	}
	if !strings.Contains(err.Error(), "session 1 not found") {
		t.Errorf("error should mention session not found, got: %v", err)
	}
	if !errors.Is(err, ErrCheckpointNotFound) {
		t.Errorf("ReadSessionContent(1) error = %v, want ErrCheckpointNotFound", err)
	}
}

// TestReadLatestSessionContent verifies that ReadLatestSessionContent returns
// the content of the most recently added session (highest index).
func TestReadLatestSessionContent(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("f1f2f3f4f5f6")

	// Write three sessions
	for i := range 3 {
		err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
			CheckpointID:     checkpointID,
			SessionID:        fmt.Sprintf("session-%d", i),
			Strategy:         "manual-commit",
			Transcript:       redact.AlreadyRedacted([]byte(fmt.Sprintf(`{"index": %d}`, i))),
			CheckpointsCount: 1,
			AuthorName:       "Test Author",
			AuthorEmail:      "test@example.com",
		})
		if err != nil {
			t.Fatalf("WriteCommitted() session %d error = %v", i, err)
		}
	}

	// Read latest session content
	content, err := store.ReadLatestSessionContent(context.Background(), checkpointID)
	if err != nil {
		t.Fatalf("ReadLatestSessionContent() error = %v", err)
	}

	// Should return session 2 (0-indexed, so latest is index 2)
	if content.Metadata.SessionID != "session-2" {
		t.Errorf("latest session SessionID = %q, want %q", content.Metadata.SessionID, "session-2")
	}
	if !strings.Contains(string(content.Transcript), `"index": 2`) {
		t.Errorf("latest session transcript should contain index 2, got %s", string(content.Transcript))
	}
}

// TestReadSessionContentByID verifies that ReadSessionContentByID can find
// a session by its session ID rather than by index.
func TestReadSessionContentByID(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("010203040506")

	// Write two sessions with distinct IDs
	sessionIDs := []string{"unique-id-alpha", "unique-id-beta"}
	for i, sid := range sessionIDs {
		err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
			CheckpointID:     checkpointID,
			SessionID:        sid,
			Strategy:         "manual-commit",
			Transcript:       redact.AlreadyRedacted([]byte(fmt.Sprintf(`{"session_name": "%s"}`, sid))),
			CheckpointsCount: 1,
			AuthorName:       "Test Author",
			AuthorEmail:      "test@example.com",
		})
		if err != nil {
			t.Fatalf("WriteCommitted() session %d error = %v", i, err)
		}
	}

	// Read by session ID
	content, err := store.ReadSessionContentByID(context.Background(), checkpointID, "unique-id-beta")
	if err != nil {
		t.Fatalf("ReadSessionContentByID() error = %v", err)
	}

	if content.Metadata.SessionID != "unique-id-beta" {
		t.Errorf("SessionID = %q, want %q", content.Metadata.SessionID, "unique-id-beta")
	}
	if !strings.Contains(string(content.Transcript), "unique-id-beta") {
		t.Errorf("transcript should contain session name, got %s", string(content.Transcript))
	}
}

// TestReadSessionContentByID_NotFound verifies that ReadSessionContentByID
// returns an error when the session ID doesn't exist in the checkpoint.
func TestReadSessionContentByID_NotFound(t *testing.T) {
	store, checkpointID := writeSingleSession(t, "111213141516", "existing-session", `{"exists": true}`)

	// Try to read non-existent session ID
	_, err := store.ReadSessionContentByID(context.Background(), checkpointID, "nonexistent-session")
	if err == nil {
		t.Error("ReadSessionContentByID() should return error for non-existent session ID")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

// TestListCommitted_MultiSessionInfo verifies that ListCommitted returns correct
// information for checkpoints with multiple sessions.
func TestListCommitted_MultiSessionInfo(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("212223242526")

	// Write two sessions to the same checkpoint
	for i, sid := range []string{"list-session-1", "list-session-2"} {
		err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
			CheckpointID:     checkpointID,
			SessionID:        sid,
			Strategy:         "manual-commit",
			Agent:            agent.AgentTypeClaudeCode,
			Transcript:       redact.AlreadyRedacted([]byte(fmt.Sprintf(`{"i": %d}`, i))),
			FilesTouched:     []string{fmt.Sprintf("file%d.go", i)},
			CheckpointsCount: i + 1,
			AuthorName:       "Test Author",
			AuthorEmail:      "test@example.com",
		})
		if err != nil {
			t.Fatalf("WriteCommitted() session %d error = %v", i, err)
		}
	}

	// List all checkpoints
	checkpoints, err := store.ListCommitted(context.Background())
	if err != nil {
		t.Fatalf("ListCommitted() error = %v", err)
	}

	// Find our checkpoint
	var found *CommittedInfo
	for i := range checkpoints {
		if checkpoints[i].CheckpointID == checkpointID {
			found = &checkpoints[i]
			break
		}
	}
	if found == nil {
		t.Fatal("checkpoint not found in ListCommitted() results")
		return
	}

	// Verify SessionCount = 2
	if found.SessionCount != 2 {
		t.Errorf("SessionCount = %d, want 2", found.SessionCount)
	}

	// Verify SessionID is from the latest session
	if found.SessionID != "list-session-2" {
		t.Errorf("SessionID = %q, want %q (latest session)", found.SessionID, "list-session-2")
	}

	// Verify Agent comes from latest session metadata
	if found.Agent != agent.AgentTypeClaudeCode {
		t.Errorf("Agent = %q, want %q", found.Agent, agent.AgentTypeClaudeCode)
	}
}

// TestWriteCommitted_SessionWithNoPrompts verifies that a session can be
// written without prompts and still be read correctly.
func TestWriteCommitted_SessionWithNoPrompts(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("313233343536")

	// Write session without prompts
	err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "no-prompts-session",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"no_prompts": true}`)),
		Prompts:          nil, // No prompts
		CheckpointsCount: 1,
		AuthorName:       "Test Author",
		AuthorEmail:      "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() error = %v", err)
	}

	// Read the session content
	content, err := store.ReadSessionContent(context.Background(), checkpointID, 0)
	if err != nil {
		t.Fatalf("ReadSessionContent() error = %v", err)
	}

	// Verify session metadata is correct
	if content.Metadata.SessionID != "no-prompts-session" {
		t.Errorf("SessionID = %q, want %q", content.Metadata.SessionID, "no-prompts-session")
	}

	// Verify transcript is present
	if len(content.Transcript) == 0 {
		t.Error("Transcript should not be empty")
	}

	// Verify prompts is empty
	if content.Prompts != "" {
		t.Errorf("Prompts should be empty, got %q", content.Prompts)
	}
}
