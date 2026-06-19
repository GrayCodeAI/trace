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
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/GrayCodeAI/trace/cli/trailers"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"
)

func TestGetAssociatedCommits_SearchAllFindsMergedBranchCommits(t *testing.T) {
	// Regression test: --search-all should find checkpoint commits that live on
	// a feature branch merged into main via a true merge commit. These commits
	// are on the second parent of the merge, so first-parent-only traversal
	// won't find them — but --search-all should use full DAG walk.
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

	checkpointID := id.MustCheckpointID("aabb11223344")

	// Create initial commit on main
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}
	mainBase, err := w.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now().Add(-4 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create a "feature branch" commit with checkpoint trailer (will become second parent)
	if err := os.WriteFile(testFile, []byte("feature work"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}
	featureMsg := trailers.FormatCheckpoint("feat: add feature", checkpointID)
	featureCommit, err := w.Commit(featureMsg, &git.CommitOptions{
		Author: &object.Signature{Name: "Feature Dev", Email: "dev@example.com", When: time.Now().Add(-3 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("failed to create feature commit: %v", err)
	}

	// Move HEAD back to mainBase to simulate being on main
	// Create a new commit on "main" that diverges
	if err := os.WriteFile(testFile, []byte("main work"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}
	mainCommitObj, err := repo.CommitObject(mainBase)
	if err != nil {
		t.Fatalf("failed to get main base commit: %v", err)
	}
	mainTree, err := mainCommitObj.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	// Create a second main commit (to diverge from feature)
	mainTip := createCommitWithTree(t, repo, mainTree.Hash, []plumbing.Hash{mainBase}, "main: parallel work")

	// Create merge commit: first parent = mainTip, second parent = featureCommit
	featureCommitObj, err := repo.CommitObject(featureCommit)
	if err != nil {
		t.Fatalf("failed to get feature commit: %v", err)
	}
	featureTree, err := featureCommitObj.Tree()
	if err != nil {
		t.Fatalf("failed to get feature tree: %v", err)
	}
	mergeHash := createMergeCommit(t, repo, mainTip, featureCommit, featureTree.Hash, "Merge feature into main")

	// Point HEAD at merge commit
	ref := plumbing.NewHashReference("refs/heads/main", mergeHash)
	if err := repo.Storer.SetReference(ref); err != nil {
		t.Fatalf("failed to set HEAD: %v", err)
	}
	headRef := plumbing.NewSymbolicReference("HEAD", "refs/heads/main")
	if err := repo.Storer.SetReference(headRef); err != nil {
		t.Fatalf("failed to set HEAD: %v", err)
	}

	// Without --search-all (first-parent only): should NOT find the feature commit
	// because it's on the second parent of the merge
	commits, err := getAssociatedCommits(context.Background(), repo, checkpointID, false)
	if err != nil {
		t.Fatalf("getAssociatedCommits error: %v", err)
	}
	if len(commits) != 0 {
		t.Errorf("expected 0 commits without --search-all (first-parent only), got %d", len(commits))
	}

	// With --search-all (full DAG walk): SHOULD find the feature commit
	commits, err = getAssociatedCommits(context.Background(), repo, checkpointID, true)
	if err != nil {
		t.Fatalf("getAssociatedCommits --search-all error: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("expected 1 commit with --search-all, got %d", len(commits))
	}
	if commits[0].Author != "Feature Dev" {
		t.Errorf("expected author 'Feature Dev', got %q", commits[0].Author)
	}
}

func TestGetBranchCheckpoints_DefaultBranchFindsMergedCheckpoints(t *testing.T) {
	// Regression test: on the default branch, getBranchCheckpoints should find
	// checkpoint commits that came in via merge commits (second parents).
	// First-parent-only traversal would miss these.
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

	// Create initial commit on master (this is the default branch)
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}
	masterBase, err := w.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now().Add(-4 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create a feature branch commit with checkpoint trailer
	cpID := id.MustCheckpointID("fea112233344")
	if err := os.WriteFile(testFile, []byte("feature work"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}
	featureCommit, err := w.Commit(trailers.FormatCheckpoint("feat: add feature", cpID), &git.CommitOptions{
		Author: &object.Signature{Name: "Feature Dev", Email: "dev@example.com", When: time.Now().Add(-3 * time.Hour)},
	})
	if err != nil {
		t.Fatalf("failed to create feature commit: %v", err)
	}

	// Get tree hashes for creating commits via plumbing
	masterBaseObj, err := repo.CommitObject(masterBase)
	if err != nil {
		t.Fatalf("failed to get master base: %v", err)
	}
	masterTree, err := masterBaseObj.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}
	featureObj, err := repo.CommitObject(featureCommit)
	if err != nil {
		t.Fatalf("failed to get feature commit: %v", err)
	}
	featureTree, err := featureObj.Tree()
	if err != nil {
		t.Fatalf("failed to get feature tree: %v", err)
	}

	// Create a second commit on master (diverge from feature)
	masterTip := createCommitWithTree(t, repo, masterTree.Hash, []plumbing.Hash{masterBase}, "main: parallel work")

	// Create merge commit on master: first parent = masterTip, second parent = featureCommit
	mergeHash := createMergeCommit(t, repo, masterTip, featureCommit, featureTree.Hash, "Merge feature into master")

	// Point master at merge commit
	ref := plumbing.NewHashReference("refs/heads/master", mergeHash)
	if err := repo.Storer.SetReference(ref); err != nil {
		t.Fatalf("failed to set ref: %v", err)
	}
	headRef := plumbing.NewSymbolicReference("HEAD", "refs/heads/master")
	if err := repo.Storer.SetReference(headRef); err != nil {
		t.Fatalf("failed to set HEAD: %v", err)
	}

	// Write committed checkpoint metadata so getBranchCheckpoints can find it
	store := checkpoint.NewGitStore(repo)
	if err := store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "test-session",
		Strategy:     "manual-commit",
		FilesTouched: []string{"test.txt"},
		Prompts:      []string{"add feature"},
	}); err != nil {
		t.Fatalf("failed to write committed checkpoint: %v", err)
	}

	// getBranchCheckpoints on master should find the checkpoint from the merged feature branch
	points, err := getBranchCheckpoints(context.Background(), repo, 100)
	if err != nil {
		t.Fatalf("getBranchCheckpoints error: %v", err)
	}

	// Should find at least the checkpoint from the merged feature branch
	var found bool
	for _, p := range points {
		if p.CheckpointID == cpID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find checkpoint %s from merged feature branch on default branch, got %d points: %v", cpID, len(points), points)
	}
}

func TestGetBranchCheckpoints_ReadsPromptFromCommittedCheckpoint(t *testing.T) {
	// Verifies that getBranchCheckpoints populates RewindPoint.SessionPrompt
	// from prompt.txt on trace/checkpoints/v1 (committed checkpoint) without
	// needing to read/parse the full transcript.
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
	_, err = w.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create a checkpoint ID and write committed checkpoint with prompt data
	cpID, err := id.NewCheckpointID("aabb11223344")
	if err != nil {
		t.Fatalf("failed to create checkpoint ID: %v", err)
	}

	expectedPrompt := "Refactor the authentication module to use JWT tokens"
	store := checkpoint.NewGitStore(repo)
	if err := store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "2026-02-27-test-session",
		Strategy:     "manual-commit",
		FilesTouched: []string{"auth.go"},
		Prompts:      []string{expectedPrompt},
	}); err != nil {
		t.Fatalf("WriteCommitted() error = %v", err)
	}

	// Create a user commit with the Trace-Checkpoint trailer
	if err := os.WriteFile(testFile, []byte("updated with auth changes"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	commitMsg := trailers.FormatCheckpoint("Refactor auth module", cpID)
	_, err = w.Commit(commitMsg, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to create commit with checkpoint trailer: %v", err)
	}

	// Call getBranchCheckpoints and verify prompt is populated
	points, err := getBranchCheckpoints(context.Background(), repo, 10)
	if err != nil {
		t.Fatalf("getBranchCheckpoints() error = %v", err)
	}

	var foundCommitted bool
	for _, p := range points {
		if p.CheckpointID == cpID {
			foundCommitted = true
			if !p.IsLogsOnly {
				t.Error("expected committed checkpoint to have IsLogsOnly=true")
			}
			if p.SessionPrompt != expectedPrompt {
				t.Errorf("expected SessionPrompt = %q, got %q", expectedPrompt, p.SessionPrompt)
			}
			break
		}
	}

	if !foundCommitted {
		t.Errorf("expected to find committed checkpoint %s, got %d points", cpID, len(points))
	}
}

func TestGetBranchCheckpoints_V2OnlyCheckpointDiscoverable(t *testing.T) {
	// When the v1 metadata branch doesn't exist but v2 has the checkpoint,
	// getBranchCheckpoints should still find committed checkpoints.
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("initial"), 0o644))
	_, err = wt.Add("test.txt")
	require.NoError(t, err)
	_, err = wt.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	require.NoError(t, err)

	// Enable v2 via settings.
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".trace"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".trace", "settings.json"),
		[]byte(`{"enabled": true, "strategy_options": {"checkpoints_v2": true}}`),
		0o644,
	))

	cpID := id.MustCheckpointID("dd11ee22ff33")
	expectedPrompt := "Create the v2-only checkpoint test file"

	// Write checkpoint ONLY to v2 store.
	v2Store := checkpoint.NewV2GitStore(repo, "origin")
	require.NoError(t, v2Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-v2-only",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte(`{"type":"user","message":{"content":[{"type":"text","text":"hello"}]}}` + "\n")),
		Prompts:      []string{expectedPrompt},
		AuthorName:   "Test",
		AuthorEmail:  "test@example.com",
	}))

	// Create a user commit with the Trace-Checkpoint trailer.
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("updated"), 0o644))
	_, err = wt.Add("test.txt")
	require.NoError(t, err)
	commitMsg := trailers.FormatCheckpoint("Create v2 test file", cpID)
	_, err = wt.Commit(commitMsg, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	require.NoError(t, err)

	// Verify no v1 metadata branch exists.
	_, v1Err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	require.Error(t, v1Err, "v1 metadata branch should not exist")

	// getBranchCheckpoints should find the v2-only checkpoint.
	points, err := getBranchCheckpoints(context.Background(), repo, 10)
	require.NoError(t, err)

	var found bool
	for _, p := range points {
		if p.CheckpointID == cpID {
			found = true
			require.Equal(t, expectedPrompt, p.SessionPrompt,
				"prompt should be read from v2 /main when v1 is absent")
			break
		}
	}
	require.True(t, found, "v2-only checkpoint should be discoverable in branch listing")
}

func TestGetBranchCheckpoints_V2PromptFallbackWhenV1Deleted(t *testing.T) {
	// When v2 is preferred and v1 metadata branch is deleted after dual-write,
	// prompts should still be readable from v2 /main.
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("initial"), 0o644))
	_, err = wt.Add("test.txt")
	require.NoError(t, err)
	_, err = wt.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".trace"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".trace", "settings.json"),
		[]byte(`{"enabled": true, "strategy_options": {"checkpoints_v2": true}}`),
		0o644,
	))

	cpID := id.MustCheckpointID("aa11bb22cc33")
	expectedPrompt := "Dual-write prompt visible after v1 deletion"

	// Dual-write: checkpoint in both v1 and v2.
	v1Store := checkpoint.NewGitStore(repo)
	v2Store := checkpoint.NewV2GitStore(repo, "origin")
	require.NoError(t, v1Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-dual",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte(`{"type":"user","message":{"content":[{"type":"text","text":"hello"}]}}` + "\n")),
		Prompts:      []string{expectedPrompt},
		AuthorName:   "Test",
		AuthorEmail:  "test@example.com",
	}))
	require.NoError(t, v2Store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-dual",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte(`{"type":"user","message":{"content":[{"type":"text","text":"hello"}]}}` + "\n")),
		Prompts:      []string{expectedPrompt},
		AuthorName:   "Test",
		AuthorEmail:  "test@example.com",
	}))

	// Create user commit with checkpoint trailer.
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("updated"), 0o644))
	_, err = wt.Add("test.txt")
	require.NoError(t, err)
	commitMsg := trailers.FormatCheckpoint("Dual-write commit", cpID)
	_, err = wt.Commit(commitMsg, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	require.NoError(t, err)

	// Delete the v1 metadata branch to simulate it being unavailable.
	require.NoError(t, repo.Storer.RemoveReference(plumbing.NewBranchReferenceName(paths.MetadataBranchName)))

	// getBranchCheckpoints should still find the checkpoint and read prompt from v2.
	points, err := getBranchCheckpoints(context.Background(), repo, 10)
	require.NoError(t, err)

	var found bool
	for _, p := range points {
		if p.CheckpointID == cpID {
			found = true
			require.Equal(t, expectedPrompt, p.SessionPrompt,
				"prompt should be read from v2 /main after v1 deletion")
			break
		}
	}
	require.True(t, found, "checkpoint should be discoverable after v1 branch deletion")
}

func TestResolvePromptTree_PrefersV2WhenEnabled(t *testing.T) {
	t.Parallel()

	v1 := &object.Tree{}
	v2 := &object.Tree{}

	require.Same(t, v2, resolvePromptTree(v1, v2, true), "should prefer v2 when enabled")
	require.Same(t, v1, resolvePromptTree(v1, v2, false), "should prefer v1 when v2 disabled")
	require.Same(t, v1, resolvePromptTree(v1, nil, true), "should fall back to v1 when v2 is nil")
	require.Same(t, v2, resolvePromptTree(nil, v2, false), "should use v2 as last resort when v1 is nil")
	require.Nil(t, resolvePromptTree(nil, nil, true), "should return nil when both are nil")
}

func TestHasAnyChanges_FirstCommitReturnsTrue(t *testing.T) {
	// First commit (no parent) should always return true
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

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

	if !hasAnyChanges(commit) {
		t.Error("hasAnyChanges() should return true for first commit (no parent)")
	}
}

func TestHasAnyChanges_MetadataOnlyChangeReturnsTrue(t *testing.T) {
	// Unlike hasCodeChanges, hasAnyChanges uses tree hash comparison and
	// does not filter out .trace/ metadata files. A metadata-only change
	// should return true because the tree hash differs from the parent's.
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create first commit
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	_, err = w.Commit("first commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to create first commit: %v", err)
	}

	// Create second commit with only .trace/ metadata changes
	metadataDir := filepath.Join(tmpDir, ".trace", "metadata", "session-123")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "full.jsonl"), []byte(`{"test": true}`), 0o644); err != nil {
		t.Fatalf("failed to write metadata file: %v", err)
	}
	if _, err := w.Add(".trace"); err != nil {
		t.Fatalf("failed to add .trace: %v", err)
	}
	commitHash, err := w.Commit("metadata only commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to create second commit: %v", err)
	}

	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	// hasAnyChanges compares tree hashes, so metadata-only changes DO count
	// (unlike hasCodeChanges which filters .trace/ files)
	if !hasAnyChanges(commit) {
		t.Error("hasAnyChanges() should return true for metadata-only changes (tree hash differs)")
	}
}

func TestHasAnyChanges_NoOpTreeChangeReturnsFalse(t *testing.T) {
	// When a commit has the same tree hash as its parent (no-op commit),
	// hasAnyChanges should return false
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

	// Create first commit
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	firstHash, err := w.Commit("first commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to create first commit: %v", err)
	}

	// Create a second commit with the exact same tree (allow-empty equivalent)
	firstCommit, err := repo.CommitObject(firstHash)
	if err != nil {
		t.Fatalf("failed to get first commit: %v", err)
	}

	sig := object.Signature{
		Name:  "Test",
		Email: "test@example.com",
		When:  time.Now(),
	}
	emptyCommit := object.Commit{
		Author:       sig,
		Committer:    sig,
		Message:      "no-op commit with same tree",
		TreeHash:     firstCommit.TreeHash,
		ParentHashes: []plumbing.Hash{firstHash},
	}
	obj := repo.Storer.NewEncodedObject()
	if err := emptyCommit.Encode(obj); err != nil {
		t.Fatalf("failed to encode commit: %v", err)
	}
	secondHash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("failed to store commit: %v", err)
	}

	secondCommit, err := repo.CommitObject(secondHash)
	if err != nil {
		t.Fatalf("failed to get second commit: %v", err)
	}

	// Same tree hash as parent → no changes
	if hasAnyChanges(secondCommit) {
		t.Error("hasAnyChanges() should return false when tree hash matches parent (no-op commit)")
	}
}

// createCommitWithTree creates a commit with a specific tree and parent hashes.
func createCommitWithTree(t *testing.T, repo *git.Repository, treeHash plumbing.Hash, parents []plumbing.Hash, message string) plumbing.Hash {
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
		ParentHashes: parents,
	}
	obj := repo.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		t.Fatalf("failed to encode commit: %v", err)
	}
	hash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("failed to store commit: %v", err)
	}
	return hash
}

func TestExtractIntent_PrefersScopedPrompt(t *testing.T) {
	t.Parallel()
	got := extractIntent([]string{"add explain --generate flag", "later prompt"}, "fallback prompt\nline2")
	want := "add explain --generate flag"
	if got != want {
		t.Errorf("extractIntent scoped\n got: %q\nwant: %q", got, want)
	}
}

func TestExtractIntent_FallsBackToFirstLineOfContent(t *testing.T) {
	t.Parallel()
	got := extractIntent(nil, "first content line\nsecond line")
	want := "first content line"
	if got != want {
		t.Errorf("extractIntent fallback\n got: %q\nwant: %q", got, want)
	}
}

func TestExtractIntent_EmptyReturnsEmpty(t *testing.T) {
	t.Parallel()
	if got := extractIntent(nil, ""); got != "" {
		t.Errorf("extractIntent empty: got %q want empty", got)
	}
	if got := extractIntent([]string{""}, ""); got != "" {
		t.Errorf("extractIntent empty-string-prompt: got %q want empty", got)
	}
}

func TestExtractIntent_TruncatesLongPrompts(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 500)
	got := extractIntent([]string{long}, "")
	if len(got) >= len(long) {
		t.Errorf("expected truncation; got %d chars", len(got))
	}
}

func TestBuildNoSummaryMarkdown_IntentAndAffordance(t *testing.T) {
	t.Parallel()
	got := buildNoSummaryMarkdown("add explain --generate flag", nil, "Run `trace explain --generate abc`.")
	if !strings.Contains(got, "## Intent\n\nadd explain --generate flag\n") {
		t.Fatalf("missing intent section:\n%s", got)
	}
	// escapeSummaryText replaces every backtick with U+2018 (‘), so both
	// backticks in "Run `trace explain --generate abc`." map to ‘.
	if !strings.Contains(got, "## Summary\n\n*Run ‘trace explain --generate abc‘.*\n") {
		t.Fatalf("missing italic summary affordance:\n%s", got)
	}
	if strings.Contains(got, "## Files") {
		t.Fatalf("did not expect Files when files=nil:\n%s", got)
	}
}

func TestBuildNoSummaryMarkdown_RendersFilesWhenProvided(t *testing.T) {
	t.Parallel()
	got := buildNoSummaryMarkdown("intent", []string{"a.go", "b.go"}, "hint")
	if !strings.Contains(got, "## Files (2)\n\n- `a.go`\n- `b.go`\n") {
		t.Fatalf("expected Files section with count and list:\n%s", got)
	}
}

func TestBuildNoSummaryMarkdown_EmptyIntentShowsPlaceholder(t *testing.T) {
	t.Parallel()
	got := buildNoSummaryMarkdown("", nil, "hint")
	if !strings.Contains(got, "## Intent\n\n*(no prompt recorded)*\n") {
		t.Fatalf("expected italic placeholder:\n%s", got)
	}
}

func TestRenderExplainBody_NoColorReturnsRawMarkdown(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer // not a TTY → shouldUseColor false
	got := renderExplainBody(&buf, "## Intent\n\nfoo\n")
	if got != "## Intent\n\nfoo\n" {
		t.Errorf("expected raw markdown when no color\n got: %q", got)
	}
}
