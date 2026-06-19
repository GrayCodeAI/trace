package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"
)

func TestBuildPagerCmd_LessRSkippedWhenLessEnvSet(t *testing.T) {
	oldEnv := pagerLookupEnv
	t.Cleanup(func() { pagerLookupEnv = oldEnv })

	pagerLookupEnv = func(key string) string {
		switch key {
		case pagerEnvVar:
			return ""
		case lessEnvVar:
			return "-FRX"
		default:
			return os.Getenv(key)
		}
	}

	cmd, _ := buildPagerCmd(context.Background())

	for _, e := range cmd.Env {
		if e == lessRawControlEnv {
			t.Error("did not expect LESS=-R when user set LESS=-FRX")
		}
	}
}

func TestBuildPagerCmd_HonorsCustomPager(t *testing.T) {
	oldEnv := pagerLookupEnv
	t.Cleanup(func() { pagerLookupEnv = oldEnv })

	pagerLookupEnv = func(key string) string {
		if key == pagerEnvVar {
			return "bat"
		}
		return os.Getenv(key)
	}

	cmd, pager := buildPagerCmd(context.Background())

	if pager != "bat" {
		t.Errorf("expected resolved pager 'bat', got %q", pager)
	}
	for _, e := range cmd.Env {
		if e == lessRawControlEnv {
			t.Error("did not expect LESS=-R when user picked a custom pager")
		}
	}
}

func TestFormatBranchCheckpoints_BasicOutput(t *testing.T) {
	now := time.Now()
	points := []strategy.RewindPoint{
		{
			ID:            "abc123def456",
			Message:       "Add feature X",
			Date:          now,
			CheckpointID:  "chk123456789",
			SessionID:     "2026-01-22-session-1",
			SessionPrompt: "Implement feature X",
		},
		{
			ID:            "def456ghi789",
			Message:       "Fix bug in Y",
			Date:          now.Add(-time.Hour),
			CheckpointID:  "chk987654321",
			SessionID:     "2026-01-22-session-2",
			SessionPrompt: "Fix the bug",
		},
	}

	output := formatBranchCheckpoints(io.Discard, "feature/my-branch", points, "")

	// Should show branch name
	if !strings.Contains(output, "feature/my-branch") {
		t.Errorf("expected branch name in output, got:\n%s", output)
	}

	// Should show checkpoint count (new metadata-row shape)
	if !strings.Contains(output, "checkpoints  2") {
		t.Errorf("expected 'checkpoints  2' in output, got:\n%s", output)
	}

	// Should show checkpoint messages
	if !strings.Contains(output, "Add feature X") {
		t.Errorf("expected first checkpoint message in output, got:\n%s", output)
	}
	if !strings.Contains(output, "Fix bug in Y") {
		t.Errorf("expected second checkpoint message in output, got:\n%s", output)
	}
}

func TestFormatBranchCheckpoints_GroupedByCheckpointID(t *testing.T) {
	// Create checkpoints spanning multiple days
	today := time.Date(2026, 1, 22, 10, 0, 0, 0, time.UTC)
	yesterday := time.Date(2026, 1, 21, 14, 0, 0, 0, time.UTC)

	points := []strategy.RewindPoint{
		{
			ID:            "abc123def456",
			Message:       "Today checkpoint 1",
			Date:          today,
			CheckpointID:  "chk111111111",
			SessionID:     "2026-01-22-session-1",
			SessionPrompt: "First task today",
		},
		{
			ID:            "def456ghi789",
			Message:       "Today checkpoint 2",
			Date:          today.Add(-30 * time.Minute),
			CheckpointID:  "chk222222222",
			SessionID:     "2026-01-22-session-1",
			SessionPrompt: "First task today",
		},
		{
			ID:            "ghi789jkl012",
			Message:       "Yesterday checkpoint",
			Date:          yesterday,
			CheckpointID:  "chk333333333",
			SessionID:     "2026-01-21-session-2",
			SessionPrompt: "Task from yesterday",
		},
	}

	output := formatBranchCheckpoints(io.Discard, "main", points, "")

	// Should group by checkpoint ID - check for checkpoint headers (identity bullet)
	if !strings.Contains(output, "● chk111111111") {
		t.Errorf("expected checkpoint ID header in output, got:\n%s", output)
	}
	if !strings.Contains(output, "● chk333333333") {
		t.Errorf("expected checkpoint ID header in output, got:\n%s", output)
	}

	// Dates should appear inline with commits (format MM-DD)
	if !strings.Contains(output, "01-22") {
		t.Errorf("expected today's date inline with commits, got:\n%s", output)
	}
	if !strings.Contains(output, "01-21") {
		t.Errorf("expected yesterday's date inline with commits, got:\n%s", output)
	}

	// Today's checkpoints should appear before yesterday's (sorted by latest timestamp)
	todayIdx := strings.Index(output, "chk111111111")
	yesterdayIdx := strings.Index(output, "chk333333333")
	if todayIdx == -1 || yesterdayIdx == -1 || todayIdx > yesterdayIdx {
		t.Errorf("expected today's checkpoints before yesterday's, got:\n%s", output)
	}
}

func TestFormatBranchCheckpoints_NoCheckpoints(t *testing.T) {
	output := formatBranchCheckpoints(io.Discard, "feature/empty-branch", nil, "")

	// Should show branch name
	if !strings.Contains(output, "feature/empty-branch") {
		t.Errorf("expected branch name in output, got:\n%s", output)
	}

	// Should indicate no checkpoints (new metadata-row shape: "checkpoints  0")
	if !strings.Contains(output, "checkpoints  0") && !strings.Contains(output, "No checkpoints") {
		t.Errorf("expected indication of no checkpoints, got:\n%s", output)
	}
}

func TestFormatBranchCheckpoints_ShowsSessionInfo(t *testing.T) {
	now := time.Now()
	points := []strategy.RewindPoint{
		{
			ID:            "abc123def456",
			Message:       "Test checkpoint",
			Date:          now,
			CheckpointID:  "chk123456789",
			SessionID:     "2026-01-22-test-session",
			SessionPrompt: "This is my test prompt",
		},
	}

	output := formatBranchCheckpoints(io.Discard, "main", points, "")

	// Should show session prompt
	if !strings.Contains(output, "This is my test prompt") {
		t.Errorf("expected session prompt in output, got:\n%s", output)
	}
}

func TestFormatBranchCheckpoints_ShowsTemporaryIndicator(t *testing.T) {
	now := time.Now()
	points := []strategy.RewindPoint{
		{
			ID:           "abc123def456",
			Message:      "Committed checkpoint",
			Date:         now,
			CheckpointID: "chk123456789",
			IsLogsOnly:   true, // Committed = logs only, no indicator shown
			SessionID:    "2026-01-22-session-1",
		},
		{
			ID:           "def456ghi789",
			Message:      "Active checkpoint",
			Date:         now.Add(-time.Hour),
			CheckpointID: "chk987654321",
			IsLogsOnly:   false, // Temporary = can be rewound, shows [temporary]
			SessionID:    "2026-01-22-session-1",
		},
	}

	output := formatBranchCheckpoints(io.Discard, "main", points, "")

	// Should indicate temporary (non-committed) checkpoints with [temporary]
	if !strings.Contains(output, "[temporary]") {
		t.Errorf("expected [temporary] indicator for non-committed checkpoint, got:\n%s", output)
	}

	// Committed checkpoints should NOT have [temporary] indicator
	// Find the line with the committed checkpoint and verify it doesn't have [temporary]
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "chk123456789") && strings.Contains(line, "[temporary]") {
			t.Errorf("committed checkpoint should not have [temporary] indicator, got:\n%s", output)
		}
	}
}

func TestFormatBranchCheckpoints_ShowsTaskCheckpoints(t *testing.T) {
	now := time.Now()
	points := []strategy.RewindPoint{
		{
			ID:               "abc123def456",
			Message:          "Running tests (toolu_01ABC)",
			Date:             now,
			CheckpointID:     "chk123456789",
			IsTaskCheckpoint: true,
			ToolUseID:        "toolu_01ABC",
			SessionID:        "2026-01-22-session-1",
		},
	}

	output := formatBranchCheckpoints(io.Discard, "main", points, "")

	// Should indicate task checkpoint
	if !strings.Contains(output, "[Task]") && !strings.Contains(output, "task") {
		t.Errorf("expected task checkpoint indicator, got:\n%s", output)
	}
}

// TestFormatCheckpointGroup_NoPromptNoCommitShowsPlaceholder verifies the
// (no prompt recorded) placeholder appears only when neither a session prompt
// nor a commit message is available.
func TestFormatCheckpointGroup_NoPromptNoCommitShowsPlaceholder(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	styles := newStatusStyles(io.Discard)
	formatCheckpointGroup(&sb, checkpointGroup{
		checkpointID: "temporary",
		prompt:       "",
		isTemporary:  true,
		commits:      []commitEntry{{date: time.Now(), gitSHA: "deadbee", message: ""}},
	}, styles)
	out := sb.String()
	if !strings.Contains(out, "(no prompt recorded)") {
		t.Errorf("expected '(no prompt recorded)' placeholder:\n%s", out)
	}
}

// TestFormatCheckpointGroup_FallsBackToCommitMessage verifies the cascade:
// when SessionPrompt is empty but a commit message is present, the headline
// renders the commit message bare (not the placeholder).
func TestFormatCheckpointGroup_FallsBackToCommitMessage(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	styles := newStatusStyles(io.Discard)
	formatCheckpointGroup(&sb, checkpointGroup{
		checkpointID: "abc123def456",
		prompt:       "",
		commits:      []commitEntry{{date: time.Now(), gitSHA: "deadbee", message: "feat(cli): wire up paging"}},
	}, styles)
	out := sb.String()
	if !strings.Contains(out, "● abc123def456") {
		t.Errorf("expected identity bullet headline:\n%s", out)
	}
	if !strings.Contains(out, "feat(cli): wire up paging") {
		t.Errorf("expected commit-message fallback in headline:\n%s", out)
	}
	if strings.Contains(out, "(no prompt recorded)") {
		t.Errorf("did not expect dimmed placeholder when commit message available:\n%s", out)
	}
}

func TestFormatBranchCheckpoints_TruncatesLongMessages(t *testing.T) {
	now := time.Now()
	longMessage := strings.Repeat("a", 200) // 200 character message
	points := []strategy.RewindPoint{
		{
			ID:           "abc123def456",
			Message:      longMessage,
			Date:         now,
			CheckpointID: "chk123456789",
			SessionID:    "2026-01-22-session-1",
		},
	}

	output := formatBranchCheckpoints(io.Discard, "main", points, "")

	// Output should not contain the full 200 character message
	if strings.Contains(output, longMessage) {
		t.Errorf("expected long message to be truncated, got full message in output")
	}

	// Should contain truncation indicator (usually "...")
	if !strings.Contains(output, "...") {
		t.Errorf("expected truncation indicator '...' for long message, got:\n%s", output)
	}
}

func TestGetBranchCheckpoints_ReadsPromptFromShadowBranch(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Initialize git repo with an initial commit
	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create and commit initial file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial content"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	initialCommit, err := w.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create .trace directory
	if err := os.MkdirAll(filepath.Join(tmpDir, ".trace"), 0o750); err != nil {
		t.Fatalf("failed to create .trace dir: %v", err)
	}

	// Create metadata directory with prompt.txt
	sessionID := "2026-01-27-test-session"
	metadataDir := filepath.Join(tmpDir, ".trace", "metadata", sessionID)
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}

	expectedPrompt := "This is my test prompt for the checkpoint"
	if err := os.WriteFile(filepath.Join(metadataDir, paths.PromptFileName), []byte(expectedPrompt), 0o644); err != nil {
		t.Fatalf("failed to write prompt file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "full.jsonl"), []byte(`{"test": true}`), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Create first checkpoint (baseline copy) - this one gets filtered out
	store := checkpoint.NewGitStore(repo)
	baseCommit := initialCommit.String()[:7]
	_, err = store.WriteTemporary(context.Background(), checkpoint.WriteTemporaryOptions{
		SessionID:         sessionID,
		BaseCommit:        baseCommit,
		ModifiedFiles:     []string{"test.txt"},
		MetadataDir:       ".trace/metadata/" + sessionID,
		MetadataDirAbs:    metadataDir,
		CommitMessage:     "First checkpoint (baseline)",
		AuthorName:        "Test",
		AuthorEmail:       "test@test.com",
		IsFirstCheckpoint: true,
	})
	if err != nil {
		t.Fatalf("WriteTemporary() first checkpoint error = %v", err)
	}

	// Modify test file again for a second checkpoint with actual code changes
	if err := os.WriteFile(testFile, []byte("second modification"), 0o644); err != nil {
		t.Fatalf("failed to modify test file: %v", err)
	}

	// Create second checkpoint (has code changes, won't be filtered)
	_, err = store.WriteTemporary(context.Background(), checkpoint.WriteTemporaryOptions{
		SessionID:         sessionID,
		BaseCommit:        baseCommit,
		ModifiedFiles:     []string{"test.txt"},
		MetadataDir:       ".trace/metadata/" + sessionID,
		MetadataDirAbs:    metadataDir,
		CommitMessage:     "Second checkpoint with code changes",
		AuthorName:        "Test",
		AuthorEmail:       "test@test.com",
		IsFirstCheckpoint: false, // Not first, has parent
	})
	if err != nil {
		t.Fatalf("WriteTemporary() second checkpoint error = %v", err)
	}

	// Now call getBranchCheckpoints and verify the prompt is read
	points, err := getBranchCheckpoints(context.Background(), repo, 10)
	if err != nil {
		t.Fatalf("getBranchCheckpoints() error = %v", err)
	}

	// Should have at least one temporary checkpoint (the second one with code changes)
	var foundTempCheckpoint bool
	for _, point := range points {
		if !point.IsLogsOnly && point.SessionID == sessionID {
			foundTempCheckpoint = true
			// Verify the prompt was read correctly from the shadow branch tree
			if point.SessionPrompt != expectedPrompt {
				t.Errorf("expected prompt %q, got %q", expectedPrompt, point.SessionPrompt)
			}
			break
		}
	}

	if !foundTempCheckpoint {
		t.Errorf("expected to find temporary checkpoint with session ID %s, got points: %+v", sessionID, points)
	}
}

func TestGetCurrentWorktreeHash_MainWorktree(t *testing.T) {
	// In a temp dir with a real .git directory (main worktree), getCurrentWorktreeHash
	// should return the hash of empty string (main worktree ID is "").
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)

	hash := getCurrentWorktreeHash(context.Background())
	expected := checkpoint.HashWorktreeID("") // Main worktree has empty ID
	if hash != expected {
		t.Errorf("getCurrentWorktreeHash(context.Background()) = %q, want %q (hash of empty worktree ID)", hash, expected)
	}
}

func TestGetReachableTemporaryCheckpoints_FiltersByWorktree(t *testing.T) {
	// Shadow branches are namespaced by worktree hash (trace/<commit>-<worktreeHash>).
	// Only shadow branches matching the current worktree should be included.
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create initial commit
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	initialCommit, err := w.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Setup metadata for both sessions
	sessionIDLocal := "2026-02-10-local-session"
	sessionIDOther := "2026-02-10-other-session"
	for _, sid := range []string{sessionIDLocal, sessionIDOther} {
		metaDir := filepath.Join(tmpDir, ".trace", "metadata", sid)
		if err := os.MkdirAll(metaDir, 0o755); err != nil {
			t.Fatalf("failed to create metadata dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(metaDir, paths.PromptFileName), []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to write prompt: %v", err)
		}
		if err := os.WriteFile(filepath.Join(metaDir, "full.jsonl"), []byte(`{"test":true}`), 0o644); err != nil {
			t.Fatalf("failed to write transcript: %v", err)
		}
	}

	store := checkpoint.NewGitStore(repo)
	baseCommit := initialCommit.String()[:7]

	writeCheckpoints := func(sessionID, worktreeID string) {
		t.Helper()
		metaDirAbs := filepath.Join(tmpDir, ".trace", "metadata", sessionID)
		// Baseline
		if _, err := store.WriteTemporary(context.Background(), checkpoint.WriteTemporaryOptions{
			SessionID: sessionID, BaseCommit: baseCommit, WorktreeID: worktreeID,
			ModifiedFiles: []string{"test.txt"}, MetadataDir: ".trace/metadata/" + sessionID,
			MetadataDirAbs: metaDirAbs, CommitMessage: "baseline", AuthorName: "Test",
			AuthorEmail: "test@test.com", IsFirstCheckpoint: true,
		}); err != nil {
			t.Fatalf("WriteTemporary baseline error: %v", err)
		}
		// Code change checkpoint
		if err := os.WriteFile(testFile, []byte(sessionID+" changes"), 0o644); err != nil {
			t.Fatalf("failed to modify test file: %v", err)
		}
		if _, err := store.WriteTemporary(context.Background(), checkpoint.WriteTemporaryOptions{
			SessionID: sessionID, BaseCommit: baseCommit, WorktreeID: worktreeID,
			ModifiedFiles: []string{"test.txt"}, MetadataDir: ".trace/metadata/" + sessionID,
			MetadataDirAbs: metaDirAbs, CommitMessage: "code changes", AuthorName: "Test",
			AuthorEmail: "test@test.com", IsFirstCheckpoint: false,
		}); err != nil {
			t.Fatalf("WriteTemporary code changes error: %v", err)
		}
	}

	writeCheckpoints(sessionIDLocal, "")               // Main worktree (matches test env)
	writeCheckpoints(sessionIDOther, "other-worktree") // Different worktree

	// getBranchCheckpoints should only include local worktree's checkpoints
	points, err := getBranchCheckpoints(context.Background(), repo, 20)
	if err != nil {
		t.Fatalf("getBranchCheckpoints error: %v", err)
	}

	for _, p := range points {
		if p.SessionID == sessionIDOther {
			t.Errorf("found checkpoint from other worktree (session %s) - should be filtered out", sessionIDOther)
		}
	}
	var foundLocal bool
	for _, p := range points {
		if p.SessionID == sessionIDLocal {
			foundLocal = true
		}
	}
	if !foundLocal {
		t.Errorf("expected local worktree checkpoint (session %s), got: %+v", sessionIDLocal, points)
	}
}

// TestRunExplainBranchDefault_ShowsBranchCheckpoints is covered by TestExplainDefault_ShowsBranchView
// since runExplainDefault now calls runExplainBranchDefault directly.

func TestRunExplainBranchDefault_DetachedHead(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Initialize git repo with a commit
	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	commitHash, err := w.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Checkout to detached HEAD state
	if err := w.Checkout(&git.CheckoutOptions{Hash: commitHash}); err != nil {
		t.Fatalf("failed to checkout detached HEAD: %v", err)
	}

	// Create .trace directory
	if err := os.MkdirAll(".trace", 0o750); err != nil {
		t.Fatalf("failed to create .trace dir: %v", err)
	}

	var stdout bytes.Buffer
	err = runExplainBranchDefault(context.Background(), &stdout, true)
	// Should NOT error
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	output := stdout.String()

	// Should indicate detached HEAD state in branch name
	if !strings.Contains(output, "HEAD") && !strings.Contains(output, "detached") {
		t.Errorf("expected output to indicate detached HEAD state, got: %s", output)
	}
}

func TestIsAncestorOf(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Initialize git repo
	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create first commit
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("v1"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	commit1, err := w.Commit("first commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com"},
	})
	if err != nil {
		t.Fatalf("failed to create first commit: %v", err)
	}

	// Create second commit
	if err := os.WriteFile(testFile, []byte("v2"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	commit2, err := w.Commit("second commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com"},
	})
	if err != nil {
		t.Fatalf("failed to create second commit: %v", err)
	}

	t.Run("commit is ancestor of later commit", func(t *testing.T) {
		// commit1 should be an ancestor of commit2
		if !strategy.IsAncestorOf(context.Background(), repo, commit1, commit2) {
			t.Error("expected commit1 to be ancestor of commit2")
		}
	})

	t.Run("commit is not ancestor of earlier commit", func(t *testing.T) {
		// commit2 should NOT be an ancestor of commit1
		if strategy.IsAncestorOf(context.Background(), repo, commit2, commit1) {
			t.Error("expected commit2 to NOT be ancestor of commit1")
		}
	})

	t.Run("commit is ancestor of itself", func(t *testing.T) {
		// A commit should be considered an ancestor of itself
		if !strategy.IsAncestorOf(context.Background(), repo, commit1, commit1) {
			t.Error("expected commit to be ancestor of itself")
		}
	})
}

func TestGetBranchCheckpoints_OnFeatureBranch(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Initialize git repo
	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create initial commit on main
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	_, err = w.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com"},
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create .trace directory
	if err := os.MkdirAll(".trace", 0o750); err != nil {
		t.Fatalf("failed to create .trace dir: %v", err)
	}

	// Get checkpoints (should be empty, but shouldn't error)
	points, err := getBranchCheckpoints(context.Background(), repo, 20)
	if err != nil {
		t.Fatalf("getBranchCheckpoints() error = %v", err)
	}

	// Should return empty list (no checkpoints yet)
	if len(points) != 0 {
		t.Errorf("expected 0 checkpoints, got %d", len(points))
	}
}

func TestHasCodeChanges_FirstCommitReturnsTrue(t *testing.T) {
	// First commit on a shadow branch (no parent) should return true
	// since it captures the working copy state - real uncommitted work
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create first commit (has no parent)
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	commitHash, err := w.Commit("first commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to create commit: %v", err)
	}

	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	// First commit (no parent) captures working copy state - should return true
	if !hasCodeChanges(commit) {
		t.Error("hasCodeChanges() should return true for first commit (captures working copy)")
	}
}
