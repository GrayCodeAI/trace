package checkpoint

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// TestWriteTemporary_SubsequentCheckpoint_ExcludesGitIgnoredNewFiles verifies that
// subsequent checkpoints filter out gitignored files from NewFiles.
func TestWriteTemporary_SubsequentCheckpoint_ExcludesGitIgnoredNewFiles(t *testing.T) {
	tempDir := t.TempDir()

	testutil.InitRepo(t, tempDir)
	repo, err := git.PlainOpen(tempDir)
	require.NoError(t, err)

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create .gitignore
	if err := os.WriteFile(filepath.Join(tempDir, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
		t.Fatalf("failed to write .gitignore: %v", err)
	}
	if _, err := worktree.Add(".gitignore"); err != nil {
		t.Fatalf("failed to add .gitignore: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tempDir, "README.md"), []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("failed to write README: %v", err)
	}
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatalf("failed to add README: %v", err)
	}
	testutil.GitCommit(t, tempDir, "Initial commit")
	headRef, err := repo.Head()
	require.NoError(t, err)
	initialCommit := headRef.Hash()

	// Create the gitignored file and a legitimate new file on disk
	if err := os.WriteFile(filepath.Join(tempDir, ".env"), []byte("SECRET=abc123\n"), 0o644); err != nil {
		t.Fatalf("failed to write .env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "config.go"), []byte("package config\n"), 0o644); err != nil {
		t.Fatalf("failed to write config.go: %v", err)
	}

	t.Chdir(tempDir)

	metadataDir := filepath.Join(tempDir, ".trace", "metadata", "test-session")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "full.jsonl"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	store := NewGitStore(repo)
	baseCommit := initialCommit.String()

	// First checkpoint
	firstResult, err := store.WriteTemporary(context.Background(), WriteTemporaryOptions{
		SessionID:         "test-session",
		BaseCommit:        baseCommit,
		MetadataDir:       ".trace/metadata/test-session",
		MetadataDirAbs:    metadataDir,
		CommitMessage:     "First checkpoint",
		AuthorName:        "Test",
		AuthorEmail:       "test@test.com",
		IsFirstCheckpoint: true,
	})
	if err != nil {
		t.Fatalf("first WriteTemporary() error = %v", err)
	}
	require.False(t, firstResult.Skipped)

	// Subsequent checkpoint with .env reported as a new file
	result, err := store.WriteTemporary(context.Background(), WriteTemporaryOptions{
		SessionID:         "test-session",
		BaseCommit:        baseCommit,
		ModifiedFiles:     []string{},
		NewFiles:          []string{"config.go", ".env"}, // Agent created both
		DeletedFiles:      []string{},
		MetadataDir:       ".trace/metadata/test-session",
		MetadataDirAbs:    metadataDir,
		CommitMessage:     "Second checkpoint",
		AuthorName:        "Test",
		AuthorEmail:       "test@test.com",
		IsFirstCheckpoint: false,
	})
	if err != nil {
		t.Fatalf("second WriteTemporary() error = %v", err)
	}

	commit, err := repo.CommitObject(result.CommitHash)
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	// config.go SHOULD be in the tree
	_, err = tree.File("config.go")
	if err != nil {
		t.Errorf("config.go should be in checkpoint tree: %v", err)
	}

	// .env MUST NOT be in the tree
	_, err = tree.File(".env")
	if err == nil {
		t.Error("SECURITY: gitignored file .env leaked into checkpoint tree via NewFiles")
	}
}

// TestWriteTemporary_SubsequentCheckpoint_ExcludesNestedGitIgnoredFiles verifies that
// gitignore patterns with directory wildcards (e.g., node_modules/) work for
// subsequent checkpoints, not just the first checkpoint.
func TestWriteTemporary_SubsequentCheckpoint_ExcludesNestedGitIgnoredFiles(t *testing.T) {
	tempDir := t.TempDir()

	testutil.InitRepo(t, tempDir)
	repo, err := git.PlainOpen(tempDir)
	require.NoError(t, err)

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tempDir, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatalf("failed to write .gitignore: %v", err)
	}
	if _, err := worktree.Add(".gitignore"); err != nil {
		t.Fatalf("failed to add .gitignore: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tempDir, "index.js"), []byte("console.log('hello')\n"), 0o644); err != nil {
		t.Fatalf("failed to write index.js: %v", err)
	}
	if _, err := worktree.Add("index.js"); err != nil {
		t.Fatalf("failed to add index.js: %v", err)
	}
	testutil.GitCommit(t, tempDir, "Initial commit")
	headRef, err := repo.Head()
	require.NoError(t, err)
	initialCommit := headRef.Hash()

	// Create node_modules file on disk
	if err := os.MkdirAll(filepath.Join(tempDir, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatalf("failed to create node_modules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "node_modules", "pkg", "index.js"), []byte("module.exports = {}"), 0o644); err != nil {
		t.Fatalf("failed to write node_modules file: %v", err)
	}

	t.Chdir(tempDir)

	metadataDir := filepath.Join(tempDir, ".trace", "metadata", "test-session")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "full.jsonl"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	store := NewGitStore(repo)
	baseCommit := initialCommit.String()

	// First checkpoint
	firstResult, err := store.WriteTemporary(context.Background(), WriteTemporaryOptions{
		SessionID:         "test-session",
		BaseCommit:        baseCommit,
		MetadataDir:       ".trace/metadata/test-session",
		MetadataDirAbs:    metadataDir,
		CommitMessage:     "First checkpoint",
		AuthorName:        "Test",
		AuthorEmail:       "test@test.com",
		IsFirstCheckpoint: true,
	})
	if err != nil {
		t.Fatalf("first WriteTemporary() error = %v", err)
	}
	require.False(t, firstResult.Skipped)

	// Subsequent checkpoint with node_modules file reported as modified
	result, err := store.WriteTemporary(context.Background(), WriteTemporaryOptions{
		SessionID:         "test-session",
		BaseCommit:        baseCommit,
		ModifiedFiles:     []string{"index.js", "node_modules/pkg/index.js"},
		NewFiles:          []string{},
		DeletedFiles:      []string{},
		MetadataDir:       ".trace/metadata/test-session",
		MetadataDirAbs:    metadataDir,
		CommitMessage:     "Second checkpoint",
		AuthorName:        "Test",
		AuthorEmail:       "test@test.com",
		IsFirstCheckpoint: false,
	})
	if err != nil {
		t.Fatalf("second WriteTemporary() error = %v", err)
	}

	commit, err := repo.CommitObject(result.CommitHash)
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	// index.js SHOULD be in the tree
	_, err = tree.File("index.js")
	if err != nil {
		t.Errorf("index.js should be in checkpoint tree: %v", err)
	}

	// node_modules/pkg/index.js MUST NOT be in the tree
	_, err = tree.File("node_modules/pkg/index.js")
	if err == nil {
		t.Error("SECURITY: gitignored file node_modules/pkg/index.js leaked into checkpoint tree")
	}
}

// TestWriteTemporary_FirstCheckpoint_UserAndAgentChanges verifies that
// the first checkpoint captures both user's pre-existing changes and agent changes.
func TestWriteTemporary_FirstCheckpoint_UserAndAgentChanges(t *testing.T) {
	tempDir := t.TempDir()

	// Initialize a git repository with an initial commit
	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create and commit README.md and main.go
	readmeFile := filepath.Join(tempDir, "README.md")
	if err := os.WriteFile(readmeFile, []byte("# Original\n"), 0o644); err != nil {
		t.Fatalf("failed to write README: %v", err)
	}
	mainFile := filepath.Join(tempDir, "main.go")
	if err := os.WriteFile(mainFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("failed to write main.go: %v", err)
	}
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatalf("failed to add README: %v", err)
	}
	if _, err := worktree.Add("main.go"); err != nil {
		t.Fatalf("failed to add main.go: %v", err)
	}
	initialCommit, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// User modifies README.md BEFORE agent starts
	userModifiedContent := "# Modified by User\n"
	if err := os.WriteFile(readmeFile, []byte(userModifiedContent), 0o644); err != nil {
		t.Fatalf("failed to modify README: %v", err)
	}

	// Agent modifies main.go
	agentModifiedContent := "package main\n\nfunc main() {\n\tprintln(\"Hello\")\n}\n"
	if err := os.WriteFile(mainFile, []byte(agentModifiedContent), 0o644); err != nil {
		t.Fatalf("failed to modify main.go: %v", err)
	}

	// Change to temp dir so paths.WorktreeRoot() works correctly
	t.Chdir(tempDir)

	// Create metadata directory
	metadataDir := filepath.Join(tempDir, ".trace", "metadata", "test-session")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "full.jsonl"), []byte(`{"test": true}`), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Create checkpoint - agent reports main.go as modified (from transcript)
	store := NewGitStore(repo)
	baseCommit := initialCommit.String()

	result, err := store.WriteTemporary(context.Background(), WriteTemporaryOptions{
		SessionID:         "test-session",
		BaseCommit:        baseCommit,
		ModifiedFiles:     []string{"main.go"}, // Only agent-modified file in list
		MetadataDir:       ".trace/metadata/test-session",
		MetadataDirAbs:    metadataDir,
		CommitMessage:     "First checkpoint",
		AuthorName:        "Test",
		AuthorEmail:       "test@test.com",
		IsFirstCheckpoint: true,
	})
	if err != nil {
		t.Fatalf("WriteTemporary() error = %v", err)
	}

	// Verify the checkpoint contains BOTH changes
	commit, err := repo.CommitObject(result.CommitHash)
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	// Check README.md has user's modification
	readmeTreeFile, err := tree.File("README.md")
	if err != nil {
		t.Fatalf("README.md not found in tree: %v", err)
	}
	readmeContent, err := readmeTreeFile.Contents()
	if err != nil {
		t.Fatalf("failed to read README.md content: %v", err)
	}
	if readmeContent != userModifiedContent {
		t.Errorf("README.md should have user's modification\ngot:\n%s\nwant:\n%s", readmeContent, userModifiedContent)
	}

	// Check main.go has agent's modification
	mainTreeFile, err := tree.File("main.go")
	if err != nil {
		t.Fatalf("main.go not found in tree: %v", err)
	}
	mainContent, err := mainTreeFile.Contents()
	if err != nil {
		t.Fatalf("failed to read main.go content: %v", err)
	}
	if mainContent != agentModifiedContent {
		t.Errorf("main.go should have agent's modification\ngot:\n%s\nwant:\n%s", mainContent, agentModifiedContent)
	}
}

// TestWriteTemporary_FirstCheckpoint_CapturesUserDeletedFiles verifies that
// the first checkpoint excludes files that the user deleted before the session started.
func TestWriteTemporary_FirstCheckpoint_CapturesUserDeletedFiles(t *testing.T) {
	tempDir := t.TempDir()

	// Initialize a git repository with an initial commit
	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create and commit two files
	keepFile := filepath.Join(tempDir, "keep.txt")
	if err := os.WriteFile(keepFile, []byte("keep this"), 0o644); err != nil {
		t.Fatalf("failed to write keep.txt: %v", err)
	}
	deleteFile := filepath.Join(tempDir, "delete-me.txt")
	if err := os.WriteFile(deleteFile, []byte("delete this"), 0o644); err != nil {
		t.Fatalf("failed to write delete-me.txt: %v", err)
	}

	if _, err := worktree.Add("keep.txt"); err != nil {
		t.Fatalf("failed to add keep.txt: %v", err)
	}
	if _, err := worktree.Add("delete-me.txt"); err != nil {
		t.Fatalf("failed to add delete-me.txt: %v", err)
	}

	initialCommit, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// User deletes delete-me.txt BEFORE the session starts
	if err := os.Remove(deleteFile); err != nil {
		t.Fatalf("failed to delete file: %v", err)
	}

	// Change to temp dir so paths.WorktreeRoot() works correctly
	t.Chdir(tempDir)

	// Create metadata directory
	metadataDir := filepath.Join(tempDir, ".trace", "metadata", "test-session")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "full.jsonl"), []byte(`{"test": true}`), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Create checkpoint store and write first checkpoint
	store := NewGitStore(repo)
	baseCommit := initialCommit.String()

	result, err := store.WriteTemporary(context.Background(), WriteTemporaryOptions{
		SessionID:         "test-session",
		BaseCommit:        baseCommit,
		ModifiedFiles:     []string{},
		DeletedFiles:      []string{}, // No agent deletions
		MetadataDir:       ".trace/metadata/test-session",
		MetadataDirAbs:    metadataDir,
		CommitMessage:     "First checkpoint",
		AuthorName:        "Test",
		AuthorEmail:       "test@test.com",
		IsFirstCheckpoint: true,
	})
	if err != nil {
		t.Fatalf("WriteTemporary() error = %v", err)
	}

	// Verify the checkpoint tree
	commit, err := repo.CommitObject(result.CommitHash)
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	// keep.txt should be in the tree (unchanged from HEAD)
	if _, err := tree.File("keep.txt"); err != nil {
		t.Errorf("keep.txt should be in checkpoint tree: %v", err)
	}

	// delete-me.txt should NOT be in the tree (user deleted it)
	_, err = tree.File("delete-me.txt")
	if err == nil {
		t.Error("delete-me.txt should NOT be in checkpoint tree (user deleted it before session)")
	} else if !errors.Is(err, object.ErrFileNotFound) && !errors.Is(err, object.ErrEntryNotFound) {
		t.Fatalf("expected delete-me.txt to be absent (ErrFileNotFound/ErrEntryNotFound), got: %v", err)
	}
}

// TestWriteTemporary_FirstCheckpoint_CapturesRenamedFiles verifies that
// the first checkpoint captures renamed files correctly.
func TestWriteTemporary_FirstCheckpoint_CapturesRenamedFiles(t *testing.T) {
	tempDir := t.TempDir()

	// Initialize a git repository with an initial commit
	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create and commit a file
	oldFile := filepath.Join(tempDir, "old-name.txt")
	if err := os.WriteFile(oldFile, []byte("content"), 0o644); err != nil {
		t.Fatalf("failed to write old-name.txt: %v", err)
	}

	if _, err := worktree.Add("old-name.txt"); err != nil {
		t.Fatalf("failed to add old-name.txt: %v", err)
	}

	initialCommit, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// User renames the file using git mv BEFORE the session starts
	// Using git mv ensures git reports this as R (rename) status, not separate D+A
	cmd := exec.CommandContext(context.Background(), "git", "mv", "old-name.txt", "new-name.txt")
	cmd.Dir = tempDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to git mv: %v", err)
	}

	// Change to temp dir so paths.WorktreeRoot() works correctly
	t.Chdir(tempDir)

	// Create metadata directory
	metadataDir := filepath.Join(tempDir, ".trace", "metadata", "test-session")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "full.jsonl"), []byte(`{"test": true}`), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Create checkpoint store and write first checkpoint
	store := NewGitStore(repo)
	baseCommit := initialCommit.String()

	result, err := store.WriteTemporary(context.Background(), WriteTemporaryOptions{
		SessionID:         "test-session",
		BaseCommit:        baseCommit,
		ModifiedFiles:     []string{},
		DeletedFiles:      []string{},
		MetadataDir:       ".trace/metadata/test-session",
		MetadataDirAbs:    metadataDir,
		CommitMessage:     "First checkpoint",
		AuthorName:        "Test",
		AuthorEmail:       "test@test.com",
		IsFirstCheckpoint: true,
	})
	if err != nil {
		t.Fatalf("WriteTemporary() error = %v", err)
	}

	// Verify the checkpoint tree
	commit, err := repo.CommitObject(result.CommitHash)
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	// new-name.txt should be in the tree
	if _, err := tree.File("new-name.txt"); err != nil {
		t.Errorf("new-name.txt should be in checkpoint tree: %v", err)
	}

	// old-name.txt should NOT be in the tree (renamed away)
	_, err = tree.File("old-name.txt")
	if err == nil {
		t.Error("old-name.txt should NOT be in checkpoint tree (file was renamed)")
	} else if !errors.Is(err, object.ErrFileNotFound) && !errors.Is(err, object.ErrEntryNotFound) {
		t.Fatalf("expected old-name.txt to be absent (ErrFileNotFound/ErrEntryNotFound), got: %v", err)
	}
}

// TestWriteTemporary_FirstCheckpoint_FilenamesWithSpaces verifies that
// filenames with spaces are handled correctly.
func TestWriteTemporary_FirstCheckpoint_FilenamesWithSpaces(t *testing.T) {
	tempDir := t.TempDir()

	// Initialize a git repository with an initial commit
	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create and commit a simple file first
	simpleFile := filepath.Join(tempDir, "simple.txt")
	if err := os.WriteFile(simpleFile, []byte("simple"), 0o644); err != nil {
		t.Fatalf("failed to write simple.txt: %v", err)
	}

	if _, err := worktree.Add("simple.txt"); err != nil {
		t.Fatalf("failed to add simple.txt: %v", err)
	}

	initialCommit, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// User creates a file with spaces in the name
	spacesFile := filepath.Join(tempDir, "file with spaces.txt")
	if err := os.WriteFile(spacesFile, []byte("content with spaces"), 0o644); err != nil {
		t.Fatalf("failed to write file with spaces: %v", err)
	}

	// Change to temp dir so paths.WorktreeRoot() works correctly
	t.Chdir(tempDir)

	// Create metadata directory
	metadataDir := filepath.Join(tempDir, ".trace", "metadata", "test-session")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "full.jsonl"), []byte(`{"test": true}`), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Create checkpoint store and write first checkpoint
	store := NewGitStore(repo)
	baseCommit := initialCommit.String()

	result, err := store.WriteTemporary(context.Background(), WriteTemporaryOptions{
		SessionID:         "test-session",
		BaseCommit:        baseCommit,
		ModifiedFiles:     []string{},
		DeletedFiles:      []string{},
		MetadataDir:       ".trace/metadata/test-session",
		MetadataDirAbs:    metadataDir,
		CommitMessage:     "First checkpoint",
		AuthorName:        "Test",
		AuthorEmail:       "test@test.com",
		IsFirstCheckpoint: true,
	})
	if err != nil {
		t.Fatalf("WriteTemporary() error = %v", err)
	}

	// Verify the checkpoint tree
	commit, err := repo.CommitObject(result.CommitHash)
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	// "file with spaces.txt" should be in the tree with correct name
	if _, err := tree.File("file with spaces.txt"); err != nil {
		t.Errorf("'file with spaces.txt' should be in checkpoint tree: %v", err)
	}
}

// =============================================================================
// Duplicate Session ID Tests - Tests for ENT-252 where the same session ID
// written twice to the same checkpoint should update in-place, not append.
// =============================================================================

// TestWriteCommitted_DuplicateSessionIDUpdatesInPlace verifies that writing
// the same session ID twice to the same checkpoint updates the existing slot
// rather than creating a duplicate subdirectory.
func TestWriteCommitted_DuplicateSessionIDUpdatesInPlace(t *testing.T) {
	t.Parallel()
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("deda01234567")

	// Write session "X" with initial data
	err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "session-X",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"message": "session X v1"}`)),
		FilesTouched:     []string{"a.go"},
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
		t.Fatalf("WriteCommitted() session X v1 error = %v", err)
	}

	// Write session "Y"
	err = store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "session-Y",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"message": "session Y"}`)),
		FilesTouched:     []string{"b.go"},
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
		t.Fatalf("WriteCommitted() session Y error = %v", err)
	}

	// Write session "X" again with updated data (should replace, not append)
	err = store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "session-X",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"message": "session X v2"}`)),
		FilesTouched:     []string{"a.go", "c.go"},
		CheckpointsCount: 5,
		TokenUsage: &agent.TokenUsage{
			InputTokens:  200,
			OutputTokens: 100,
			APICallCount: 10,
		},
		AuthorName:  "Test Author",
		AuthorEmail: "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() session X v2 error = %v", err)
	}

	// Read the checkpoint summary
	summary, err := store.ReadCommitted(context.Background(), checkpointID)
	if err != nil {
		t.Fatalf("ReadCommitted() error = %v", err)
	}
	require.NotNil(t, summary, "ReadCommitted() returned nil summary")

	// Should have 2 sessions, not 3
	if len(summary.Sessions) != 2 {
		t.Errorf("len(summary.Sessions) = %d, want 2 (not 3 - duplicate should be replaced)", len(summary.Sessions))
	}

	// Verify session 0 has updated data (session X v2)
	content0, err := store.ReadSessionContent(context.Background(), checkpointID, 0)
	if err != nil {
		t.Fatalf("ReadSessionContent(0) error = %v", err)
	}
	if content0.Metadata.SessionID != "session-X" {
		t.Errorf("session 0 SessionID = %q, want %q", content0.Metadata.SessionID, "session-X")
	}
	if content0.Metadata.CheckpointsCount != 5 {
		t.Errorf("session 0 CheckpointsCount = %d, want 5", content0.Metadata.CheckpointsCount)
	}
	if !strings.Contains(string(content0.Transcript), "session X v2") {
		t.Errorf("session 0 transcript should contain 'session X v2', got %s", string(content0.Transcript))
	}

	// Verify session 1 is still "Y" (unchanged)
	content1, err := store.ReadSessionContent(context.Background(), checkpointID, 1)
	if err != nil {
		t.Fatalf("ReadSessionContent(1) error = %v", err)
	}
	if content1.Metadata.SessionID != "session-Y" {
		t.Errorf("session 1 SessionID = %q, want %q", content1.Metadata.SessionID, "session-Y")
	}

	// Verify aggregated stats: count = 5 (X v2) + 2 (Y) = 7
	if summary.CheckpointsCount != 7 {
		t.Errorf("summary.CheckpointsCount = %d, want 7", summary.CheckpointsCount)
	}

	// Verify merged files: [a.go, b.go, c.go]
	expectedFiles := []string{"a.go", "b.go", "c.go"}
	if len(summary.FilesTouched) != len(expectedFiles) {
		t.Errorf("len(summary.FilesTouched) = %d, want %d", len(summary.FilesTouched), len(expectedFiles))
	}
	for i, want := range expectedFiles {
		if i < len(summary.FilesTouched) && summary.FilesTouched[i] != want {
			t.Errorf("summary.FilesTouched[%d] = %q, want %q", i, summary.FilesTouched[i], want)
		}
	}

	// Verify aggregated tokens: 200 (X v2) + 50 (Y) = 250
	if summary.TokenUsage == nil {
		t.Fatal("summary.TokenUsage should not be nil")
	}
	if summary.TokenUsage.InputTokens != 250 {
		t.Errorf("summary.TokenUsage.InputTokens = %d, want 250", summary.TokenUsage.InputTokens)
	}
	if summary.TokenUsage.OutputTokens != 125 {
		t.Errorf("summary.TokenUsage.OutputTokens = %d, want 125", summary.TokenUsage.OutputTokens)
	}
	if summary.TokenUsage.APICallCount != 13 {
		t.Errorf("summary.TokenUsage.APICallCount = %d, want 13", summary.TokenUsage.APICallCount)
	}
}
