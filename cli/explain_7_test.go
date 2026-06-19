package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/GrayCodeAI/trace/cli/trailers"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"
)

func TestGetAssociatedCommits(t *testing.T) {
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

	checkpointID := id.MustCheckpointID("abc123def456")

	// Create first commit without checkpoint trailer
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	_, err = w.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Now().Add(-2 * time.Hour),
		},
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create commit with matching checkpoint trailer
	if err := os.WriteFile(testFile, []byte("with checkpoint"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	commitMsg := trailers.FormatCheckpoint("feat: add feature", checkpointID)
	_, err = w.Commit(commitMsg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Alice Developer",
			Email: "alice@example.com",
			When:  time.Now().Add(-1 * time.Hour),
		},
	})
	if err != nil {
		t.Fatalf("failed to create checkpoint commit: %v", err)
	}

	// Create another commit without checkpoint trailer
	if err := os.WriteFile(testFile, []byte("after checkpoint"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	_, err = w.Commit("unrelated commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("failed to create unrelated commit: %v", err)
	}

	// Test: should find the one commit with matching checkpoint
	commits, err := getAssociatedCommits(context.Background(), repo, checkpointID, false)
	if err != nil {
		t.Fatalf("getAssociatedCommits error: %v", err)
	}

	if len(commits) != 1 {
		t.Fatalf("expected 1 associated commit, got %d", len(commits))
	}

	commit := commits[0]
	if commit.Author != "Alice Developer" {
		t.Errorf("expected author 'Alice Developer', got %q", commit.Author)
	}
	if !strings.Contains(commit.Message, "feat: add feature") {
		t.Errorf("expected message to contain 'feat: add feature', got %q", commit.Message)
	}
	if len(commit.ShortSHA) != 7 {
		t.Errorf("expected 7-char short SHA, got %d chars: %q", len(commit.ShortSHA), commit.ShortSHA)
	}
	if len(commit.SHA) != 40 {
		t.Errorf("expected 40-char full SHA, got %d chars", len(commit.SHA))
	}
}

func TestGetAssociatedCommits_NoMatches(t *testing.T) {
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

	// Create commit without checkpoint trailer
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	_, err = w.Commit("regular commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("failed to create commit: %v", err)
	}

	// Search for a checkpoint ID that doesn't exist (valid format: 12 hex chars)
	checkpointID := id.MustCheckpointID("aaaa11112222")
	commits, err := getAssociatedCommits(context.Background(), repo, checkpointID, false)
	if err != nil {
		t.Fatalf("getAssociatedCommits error: %v", err)
	}

	if len(commits) != 0 {
		t.Errorf("expected 0 associated commits, got %d", len(commits))
	}
}

func TestGetAssociatedCommits_MultipleMatches(t *testing.T) {
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

	checkpointID := id.MustCheckpointID("abc123def456")

	// Create initial commit
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	_, err = w.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Now().Add(-3 * time.Hour),
		},
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create first commit with checkpoint trailer
	if err := os.WriteFile(testFile, []byte("first"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	commitMsg := trailers.FormatCheckpoint("first checkpoint commit", checkpointID)
	_, err = w.Commit(commitMsg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Now().Add(-2 * time.Hour),
		},
	})
	if err != nil {
		t.Fatalf("failed to create first checkpoint commit: %v", err)
	}

	// Create second commit with same checkpoint trailer (e.g., amend scenario)
	if err := os.WriteFile(testFile, []byte("second"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	commitMsg = trailers.FormatCheckpoint("second checkpoint commit", checkpointID)
	_, err = w.Commit(commitMsg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
			When:  time.Now().Add(-1 * time.Hour),
		},
	})
	if err != nil {
		t.Fatalf("failed to create second checkpoint commit: %v", err)
	}

	// Test: should find both commits with matching checkpoint
	commits, err := getAssociatedCommits(context.Background(), repo, checkpointID, false)
	if err != nil {
		t.Fatalf("getAssociatedCommits error: %v", err)
	}

	if len(commits) != 2 {
		t.Fatalf("expected 2 associated commits, got %d", len(commits))
	}

	// Should be in reverse chronological order (newest first)
	if !strings.Contains(commits[0].Message, "second") {
		t.Errorf("expected newest commit first, got %q", commits[0].Message)
	}
	if !strings.Contains(commits[1].Message, "first") {
		t.Errorf("expected older commit second, got %q", commits[1].Message)
	}
}

func TestFormatCheckpointOutput_WithAssociatedCommits(t *testing.T) {
	summary := &checkpoint.CheckpointSummary{
		CheckpointID: id.MustCheckpointID("abc123def456"),
		FilesTouched: []string{"main.go"},
	}
	content := &checkpoint.SessionContent{
		Metadata: checkpoint.CommittedMetadata{
			CheckpointID:              "abc123def456",
			SessionID:                 "2026-02-04-test-session",
			CreatedAt:                 time.Date(2026, 2, 4, 10, 30, 0, 0, time.UTC),
			FilesTouched:              []string{"main.go"},
			CheckpointTranscriptStart: 0,
		},
		Prompts:    "Add a new feature",
		Transcript: nil, // No transcript available
	}

	associatedCommits := []associatedCommit{
		{
			SHA:      "abc123def4567890abc123def4567890abc12345",
			ShortSHA: "abc123d",
			Message:  "feat: add feature",
			Author:   "Alice Developer",
			Date:     time.Date(2026, 2, 4, 11, 0, 0, 0, time.UTC),
		},
		{
			SHA:      "def456abc7890123def456abc7890123def45678",
			ShortSHA: "def456a",
			Message:  "fix: update feature",
			Author:   "Bob Developer",
			Date:     time.Date(2026, 2, 4, 12, 0, 0, 0, time.UTC),
		},
	}

	output := formatCheckpointOutput(summary, content, id.MustCheckpointID("abc123def456"), associatedCommits, checkpoint.Author{}, true, false, &bytes.Buffer{})

	// Should show commits section with count
	if !strings.Contains(output, "  commits  (2)") {
		t.Errorf("expected 'Commits: (2)' in output, got:\n%s", output)
	}
	// Should show commit details
	if !strings.Contains(output, "abc123d") {
		t.Errorf("expected short SHA 'abc123d' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "def456a") {
		t.Errorf("expected short SHA 'def456a' in output, got:\n%s", output)
	}
	if !strings.Contains(output, "feat: add feature") {
		t.Errorf("expected commit message in output, got:\n%s", output)
	}
	if !strings.Contains(output, "fix: update feature") {
		t.Errorf("expected commit message in output, got:\n%s", output)
	}
	// Should show date in format YYYY-MM-DD
	if !strings.Contains(output, "2026-02-04") {
		t.Errorf("expected date in output, got:\n%s", output)
	}
}

// createMergeCommit creates a merge commit with two parents using go-git plumbing APIs.
// Returns the merge commit hash.
func createMergeCommit(t *testing.T, repo *git.Repository, parent1, parent2 plumbing.Hash, treeHash plumbing.Hash, message string) plumbing.Hash {
	t.Helper()

	sig := object.Signature{
		Name:  "Test",
		Email: "test@example.com",
		When:  time.Now(),
	}
	commit := object.Commit{
		Author:       sig,
		Committer:    sig,
		Message:      message,
		TreeHash:     treeHash,
		ParentHashes: []plumbing.Hash{parent1, parent2},
	}
	obj := repo.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		t.Fatalf("failed to encode merge commit: %v", err)
	}
	hash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("failed to store merge commit: %v", err)
	}
	return hash
}

func TestGetBranchCheckpoints_WithMergeFromMain(t *testing.T) {
	// Regression test: when main is merged into a feature branch, getBranchCheckpoints
	// should still find feature branch checkpoints from before the merge.
	// The old repo.Log() approach did a full DAG walk, entering main's history through
	// merge commits and eventually hitting consecutiveMainLimit, silently dropping
	// older feature branch checkpoints.
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	repo, err := git.PlainInit(tmpDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create initial commit on master
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	initialCommit, err := w.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now().Add(-5 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create feature branch from initial commit
	featureBranch := plumbing.NewBranchReferenceName("feature/test")
	if err := w.Checkout(&git.CheckoutOptions{
		Hash:   initialCommit,
		Branch: featureBranch,
		Create: true,
	}); err != nil {
		t.Fatalf("failed to create feature branch: %v", err)
	}

	// Create first feature checkpoint commit (BEFORE the merge)
	cpID1 := id.MustCheckpointID("aaa111bbb222")
	if err := os.WriteFile(testFile, []byte("feature work 1"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	featureCommit1, err := w.Commit(trailers.FormatCheckpoint("feat: first feature", cpID1), &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now().Add(-4 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("failed to create first feature commit: %v", err)
	}

	// Switch to master and add commits (simulating work on main)
	if err := w.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("master"),
	}); err != nil {
		t.Fatalf("failed to checkout master: %v", err)
	}
	if err := os.WriteFile(testFile, []byte("main work"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	mainCommit, err := w.Commit("main: add work", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now().Add(-3 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("failed to create main commit: %v", err)
	}

	// Switch back to feature branch
	if err := w.Checkout(&git.CheckoutOptions{
		Branch: featureBranch,
	}); err != nil {
		t.Fatalf("failed to checkout feature branch: %v", err)
	}

	// Create merge commit: merge main into feature (feature is first parent, main is second parent)
	featureCommitObj, commitObjErr := repo.CommitObject(featureCommit1)
	if commitObjErr != nil {
		t.Fatalf("failed to get feature commit object: %v", commitObjErr)
	}
	featureTree, treeErr := featureCommitObj.Tree()
	if treeErr != nil {
		t.Fatalf("failed to get feature commit tree: %v", treeErr)
	}
	mergeHash := createMergeCommit(t, repo, featureCommit1, mainCommit, featureTree.Hash, "Merge branch 'master' into feature/test")

	// Update feature branch ref to point to merge commit
	ref := plumbing.NewHashReference(featureBranch, mergeHash)
	if err := repo.Storer.SetReference(ref); err != nil {
		t.Fatalf("failed to update feature branch ref: %v", err)
	}

	// Reset worktree to merge commit
	if err := w.Reset(&git.ResetOptions{Commit: mergeHash, Mode: git.HardReset}); err != nil {
		t.Fatalf("failed to reset to merge: %v", err)
	}

	// Create second feature checkpoint commit (AFTER the merge)
	cpID2 := id.MustCheckpointID("ccc333ddd444")
	if err := os.WriteFile(testFile, []byte("feature work 2"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	_, err = w.Commit(trailers.FormatCheckpoint("feat: second feature", cpID2), &git.CommitOptions{
		Author:    &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now().Add(-1 * time.Hour)},
		Parents:   []plumbing.Hash{mergeHash},
		Committer: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now().Add(-1 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("failed to create second feature commit: %v", err)
	}

	// Create .trace directory
	if err := os.MkdirAll(".trace", 0o750); err != nil {
		t.Fatalf("failed to create .trace dir: %v", err)
	}

	// Test getAssociatedCommits - should find BOTH feature checkpoint commits
	// by walking first-parent chain (skipping the merge's second parent into main)
	commits1, err := getAssociatedCommits(context.Background(), repo, cpID1, false)
	if err != nil {
		t.Fatalf("getAssociatedCommits for cpID1 error: %v", err)
	}
	if len(commits1) != 1 {
		t.Errorf("expected 1 commit for cpID1 (first feature checkpoint), got %d", len(commits1))
	}

	commits2, err := getAssociatedCommits(context.Background(), repo, cpID2, false)
	if err != nil {
		t.Fatalf("getAssociatedCommits for cpID2 error: %v", err)
	}
	if len(commits2) != 1 {
		t.Errorf("expected 1 commit for cpID2 (second feature checkpoint), got %d", len(commits2))
	}
}

func TestGetBranchCheckpoints_MergeCommitAtHEAD(t *testing.T) {
	// Test that when HEAD itself is a merge commit, walkFirstParentCommits
	// correctly follows the first parent (feature branch history) and
	// doesn't walk into the second parent (main branch history).
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	repo, err := git.PlainInit(tmpDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create initial commit on master
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	initialCommit, err := w.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now().Add(-5 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create feature branch
	featureBranch := plumbing.NewBranchReferenceName("feature/merge-at-head")
	if err := w.Checkout(&git.CheckoutOptions{
		Hash:   initialCommit,
		Branch: featureBranch,
		Create: true,
	}); err != nil {
		t.Fatalf("failed to create feature branch: %v", err)
	}

	// Create feature checkpoint commit
	cpID := id.MustCheckpointID("eee555fff666")
	if err := os.WriteFile(testFile, []byte("feature work"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	featureCommit, err := w.Commit(trailers.FormatCheckpoint("feat: feature work", cpID), &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now().Add(-3 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("failed to create feature commit: %v", err)
	}

	// Switch to master and add a commit
	if err := w.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("master"),
	}); err != nil {
		t.Fatalf("failed to checkout master: %v", err)
	}
	mainFile := filepath.Join(tmpDir, "main.txt")
	if err := os.WriteFile(mainFile, []byte("main work"), 0o644); err != nil {
		t.Fatalf("failed to write main file: %v", err)
	}
	if _, err := w.Add("main.txt"); err != nil {
		t.Fatalf("failed to add main file: %v", err)
	}
	mainCommit, err := w.Commit("main: add work", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now().Add(-2 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("failed to create main commit: %v", err)
	}

	// Switch back to feature and create merge commit AT HEAD
	if err := w.Checkout(&git.CheckoutOptions{
		Branch: featureBranch,
	}); err != nil {
		t.Fatalf("failed to checkout feature branch: %v", err)
	}

	featureCommitObj, commitObjErr := repo.CommitObject(featureCommit)
	if commitObjErr != nil {
		t.Fatalf("failed to get feature commit object: %v", commitObjErr)
	}
	featureTree, treeErr := featureCommitObj.Tree()
	if treeErr != nil {
		t.Fatalf("failed to get feature commit tree: %v", treeErr)
	}
	mergeHash := createMergeCommit(t, repo, featureCommit, mainCommit, featureTree.Hash, "Merge branch 'master' into feature/merge-at-head")

	// Update feature branch ref to merge commit (HEAD IS the merge)
	ref := plumbing.NewHashReference(featureBranch, mergeHash)
	if err := repo.Storer.SetReference(ref); err != nil {
		t.Fatalf("failed to update feature branch ref: %v", err)
	}

	// Create .trace directory
	if err := os.MkdirAll(".trace", 0o750); err != nil {
		t.Fatalf("failed to create .trace dir: %v", err)
	}

	// HEAD is the merge commit itself.
	// getAssociatedCommits should walk: merge -> featureCommit -> initial
	// and find the checkpoint on featureCommit.
	commits, err := getAssociatedCommits(context.Background(), repo, cpID, false)
	if err != nil {
		t.Fatalf("getAssociatedCommits error: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("expected 1 associated commit when HEAD is merge commit, got %d", len(commits))
	}
	if !strings.Contains(commits[0].Message, "feat: feature work") {
		t.Errorf("expected feature commit message, got %q", commits[0].Message)
	}
}

func TestWalkFirstParentCommits_SkipsMergeParents(t *testing.T) {
	// Verify that walkFirstParentCommits follows only first parents and doesn't
	// enter the second parent (merge source) of merge commits.
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	repo, err := git.PlainInit(tmpDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create initial commit (shared ancestor)
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	initialCommit, err := w.Commit("A: initial", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now().Add(-5 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create feature branch with one commit
	featureBranch := plumbing.NewBranchReferenceName("feature/walk-test")
	if err := w.Checkout(&git.CheckoutOptions{
		Hash:   initialCommit,
		Branch: featureBranch,
		Create: true,
	}); err != nil {
		t.Fatalf("failed to create feature branch: %v", err)
	}
	if err := os.WriteFile(testFile, []byte("feature"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	featureCommit, err := w.Commit("B: feature work", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now().Add(-4 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("failed to create feature commit: %v", err)
	}

	// Create main branch commit (will be merge source)
	if err := w.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("master"),
	}); err != nil {
		t.Fatalf("failed to checkout master: %v", err)
	}
	mainFile := filepath.Join(tmpDir, "main.txt")
	if err := os.WriteFile(mainFile, []byte("main"), 0o644); err != nil {
		t.Fatalf("failed to write main file: %v", err)
	}
	if _, err := w.Add("main.txt"); err != nil {
		t.Fatalf("failed to add main file: %v", err)
	}
	mainCommit, err := w.Commit("C: main work", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now().Add(-3 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("failed to create main commit: %v", err)
	}

	// Switch to feature and create merge commit
	if err := w.Checkout(&git.CheckoutOptions{
		Branch: featureBranch,
	}); err != nil {
		t.Fatalf("failed to checkout feature: %v", err)
	}
	featureCommitObj, commitObjErr := repo.CommitObject(featureCommit)
	if commitObjErr != nil {
		t.Fatalf("failed to get feature commit object: %v", commitObjErr)
	}
	featureTree, treeErr := featureCommitObj.Tree()
	if treeErr != nil {
		t.Fatalf("failed to get feature commit tree: %v", treeErr)
	}
	mergeHash := createMergeCommit(t, repo, featureCommit, mainCommit, featureTree.Hash, "M: merge main into feature")

	// Walk should visit: M (merge) -> B (feature) -> A (initial)
	// It should NOT visit C (main work), because that's the second parent of the merge.
	var visited []string
	err = walkFirstParentCommits(context.Background(), repo, mergeHash, 0, func(c *object.Commit) error {
		visited = append(visited, strings.Split(c.Message, "\n")[0])
		return nil
	})
	if err != nil {
		t.Fatalf("walkFirstParentCommits error: %v", err)
	}

	expected := []string{"M: merge main into feature", "B: feature work", "A: initial"}
	if len(visited) != len(expected) {
		t.Fatalf("expected %d commits visited, got %d: %v", len(expected), len(visited), visited)
	}
	for i, msg := range expected {
		if visited[i] != msg {
			t.Errorf("commit %d: expected %q, got %q", i, msg, visited[i])
		}
	}

	// Verify C was NOT visited
	for _, msg := range visited {
		if strings.Contains(msg, "C: main work") {
			t.Error("walkFirstParentCommits visited main branch commit (second parent of merge) - should only follow first parents")
		}
	}
}

func TestFormatCheckpointOutput_NoCommitsOnBranch(t *testing.T) {
	summary := &checkpoint.CheckpointSummary{
		CheckpointID: id.MustCheckpointID("abc123def456"),
		FilesTouched: []string{"main.go"},
	}
	content := &checkpoint.SessionContent{
		Metadata: checkpoint.CommittedMetadata{
			CheckpointID:              "abc123def456",
			SessionID:                 "2026-02-04-test-session",
			CreatedAt:                 time.Date(2026, 2, 4, 10, 30, 0, 0, time.UTC),
			FilesTouched:              []string{"main.go"},
			CheckpointTranscriptStart: 0,
		},
		Prompts:    "Add a new feature",
		Transcript: nil, // No transcript available
	}

	// No associated commits - use empty slice (not nil) to indicate "searched but found none"
	associatedCommits := []associatedCommit{}

	output := formatCheckpointOutput(summary, content, id.MustCheckpointID("abc123def456"), associatedCommits, checkpoint.Author{}, true, false, &bytes.Buffer{})

	// Should show message indicating no commits found
	if !strings.Contains(output, "  commits  (none on this branch)") {
		t.Errorf("expected 'Commits: No commits found on this branch' in output, got:\n%s", output)
	}
}
