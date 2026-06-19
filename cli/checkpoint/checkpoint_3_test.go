package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// TestWriteCommitted_SessionWithSummary verifies that a non-nil Summary
// in WriteCommittedOptions is persisted in the session-level metadata.json.
// Regression test for ENT-243 where Summary was omitted from the struct literal.
func TestWriteCommitted_SessionWithSummary(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("aabbccddeeff")

	summary := &Summary{
		Intent:  "User wanted to fix a bug",
		Outcome: "Bug was fixed",
	}

	err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "summary-session",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"test": true}`)),
		CheckpointsCount: 1,
		AuthorName:       "Test Author",
		AuthorEmail:      "test@example.com",
		Summary:          summary,
	})
	if err != nil {
		t.Fatalf("WriteCommitted() error = %v", err)
	}

	content, err := store.ReadSessionContent(context.Background(), checkpointID, 0)
	if err != nil {
		t.Fatalf("ReadSessionContent() error = %v", err)
	}

	if content.Metadata.Summary == nil {
		t.Fatal("Summary should not be nil")
	}
	if content.Metadata.Summary.Intent != "User wanted to fix a bug" {
		t.Errorf("Summary.Intent = %q, want %q", content.Metadata.Summary.Intent, "User wanted to fix a bug")
	}
	if content.Metadata.Summary.Outcome != "Bug was fixed" {
		t.Errorf("Summary.Outcome = %q, want %q", content.Metadata.Summary.Outcome, "Bug was fixed")
	}
}

// TestWriteCommitted_ThreeSessions verifies the structure with three sessions
// to ensure the 0-based indexing works correctly throughout.
func TestWriteCommitted_ThreeSessions(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("515253545556")

	// Write three sessions
	for i := range 3 {
		err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
			CheckpointID:     checkpointID,
			SessionID:        fmt.Sprintf("three-session-%d", i),
			Strategy:         "manual-commit",
			Transcript:       redact.AlreadyRedacted([]byte(fmt.Sprintf(`{"session_number": %d}`, i))),
			FilesTouched:     []string{fmt.Sprintf("s%d.go", i)},
			CheckpointsCount: i + 1,
			TokenUsage: &agent.TokenUsage{
				InputTokens: 100 * (i + 1),
			},
			AuthorName:  "Test Author",
			AuthorEmail: "test@example.com",
		})
		if err != nil {
			t.Fatalf("WriteCommitted() session %d error = %v", i, err)
		}
	}

	// Read summary
	summary, err := store.ReadCommitted(context.Background(), checkpointID)
	if err != nil {
		t.Fatalf("ReadCommitted() error = %v", err)
	}

	// Verify 3 sessions
	if len(summary.Sessions) != 3 {
		t.Errorf("len(summary.Sessions) = %d, want 3", len(summary.Sessions))
	}

	// Verify aggregated stats
	// CheckpointsCount = 1 + 2 + 3 = 6
	if summary.CheckpointsCount != 6 {
		t.Errorf("summary.CheckpointsCount = %d, want 6", summary.CheckpointsCount)
	}

	// FilesTouched = [s0.go, s1.go, s2.go]
	if len(summary.FilesTouched) != 3 {
		t.Errorf("len(summary.FilesTouched) = %d, want 3", len(summary.FilesTouched))
	}

	// TokenUsage.InputTokens = 100 + 200 + 300 = 600
	if summary.TokenUsage == nil {
		t.Fatal("summary.TokenUsage should not be nil")
	}
	if summary.TokenUsage.InputTokens != 600 {
		t.Errorf("summary.TokenUsage.InputTokens = %d, want 600", summary.TokenUsage.InputTokens)
	}

	// Verify each session can be read by index
	for i := range 3 {
		content, err := store.ReadSessionContent(context.Background(), checkpointID, i)
		if err != nil {
			t.Errorf("ReadSessionContent(%d) error = %v", i, err)
			continue
		}
		expectedID := fmt.Sprintf("three-session-%d", i)
		if content.Metadata.SessionID != expectedID {
			t.Errorf("session %d SessionID = %q, want %q", i, content.Metadata.SessionID, expectedID)
		}
	}
}

// TestReadCommitted_NonexistentCheckpoint verifies that ReadCommitted returns
// nil (not an error) when the checkpoint doesn't exist.
func TestReadCommitted_NonexistentCheckpoint(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)

	// Ensure sessions branch exists
	err := store.ensureSessionsBranch(context.Background())
	if err != nil {
		t.Fatalf("ensureSessionsBranch() error = %v", err)
	}

	// Try to read non-existent checkpoint
	checkpointID := id.MustCheckpointID("ffffffffffff")
	summary, err := store.ReadCommitted(context.Background(), checkpointID)
	if err != nil {
		t.Errorf("ReadCommitted() error = %v, want nil", err)
	}
	if summary != nil {
		t.Errorf("ReadCommitted() = %v, want nil for non-existent checkpoint", summary)
	}
}

// TestReadSessionContent_NonexistentCheckpoint verifies that ReadSessionContent
// returns ErrCheckpointNotFound when the checkpoint doesn't exist.
func TestReadSessionContent_NonexistentCheckpoint(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)

	// Ensure sessions branch exists
	err := store.ensureSessionsBranch(context.Background())
	if err != nil {
		t.Fatalf("ensureSessionsBranch() error = %v", err)
	}

	// Try to read from non-existent checkpoint
	checkpointID := id.MustCheckpointID("eeeeeeeeeeee")
	_, err = store.ReadSessionContent(context.Background(), checkpointID, 0)
	if !errors.Is(err, ErrCheckpointNotFound) {
		t.Errorf("ReadSessionContent() error = %v, want ErrCheckpointNotFound", err)
	}
}

// TestWriteTemporary_FirstCheckpoint_CapturesModifiedTrackedFiles verifies that
// the first checkpoint captures modifications to tracked files that existed before
// the agent made any changes (user's uncommitted work).
func TestWriteTemporary_FirstCheckpoint_CapturesModifiedTrackedFiles(t *testing.T) {
	tempDir := t.TempDir()

	// Initialize a git repository with an initial commit containing README.md
	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create and commit README.md with original content
	readmeFile := filepath.Join(tempDir, "README.md")
	originalContent := "# Original Content\n"
	if err := os.WriteFile(readmeFile, []byte(originalContent), 0o644); err != nil {
		t.Fatalf("failed to write README: %v", err)
	}
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatalf("failed to add README: %v", err)
	}
	initialCommit, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Simulate user modifying README.md BEFORE agent starts (user's uncommitted work)
	modifiedContent := "# Modified by User\n\nThis change was made before the agent started.\n"
	if err := os.WriteFile(readmeFile, []byte(modifiedContent), 0o644); err != nil {
		t.Fatalf("failed to modify README: %v", err)
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
	// Note: ModifiedFiles is empty because agent hasn't touched anything yet
	// The first checkpoint should still capture README.md because it's modified in working dir
	store := NewGitStore(repo)
	baseCommit := initialCommit.String()

	result, err := store.WriteTemporary(context.Background(), WriteTemporaryOptions{
		SessionID:         "test-session",
		BaseCommit:        baseCommit,
		ModifiedFiles:     []string{}, // Agent hasn't modified anything
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
	if result.Skipped {
		t.Error("first checkpoint should not be skipped")
	}

	// Verify the shadow branch commit contains the MODIFIED README.md content
	commit, err := repo.CommitObject(result.CommitHash)
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	// Find README.md in the tree
	file, err := tree.File("README.md")
	if err != nil {
		t.Fatalf("README.md not found in checkpoint tree: %v", err)
	}

	content, err := file.Contents()
	if err != nil {
		t.Fatalf("failed to read README.md content: %v", err)
	}

	if content != modifiedContent {
		t.Errorf("checkpoint should contain modified content\ngot:\n%s\nwant:\n%s", content, modifiedContent)
	}
}

// TestWriteTemporary_PathNormalizationAndSkipping verifies that shadow branch writes
// normalize absolute in-repo paths back to repo-relative tree entries and skip invalid
// paths rather than encoding them into git trees.
func TestWriteTemporary_PathNormalizationAndSkipping(t *testing.T) {
	tests := []struct {
		name          string
		modifiedFiles func(repoRoot, mainFile string) []string
		wantUpdated   bool
	}{
		{
			name: "absolute in repo path is normalized",
			modifiedFiles: func(_, mainFile string) []string {
				return []string{mainFile}
			},
			wantUpdated: true,
		},
		{
			name: "absolute outside repo path is skipped",
			modifiedFiles: func(_, _ string) []string {
				return []string{"C:/Users/rober/Vaults/Flowsign/main.go"}
			},
			wantUpdated: false,
		},
		{
			name: "empty segment path is skipped",
			modifiedFiles: func(_, _ string) []string {
				return []string{"dir//main.go"}
			},
			wantUpdated: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			// Resolve symlinks so absolute paths match git's resolved repo root.
			// On macOS, t.TempDir() returns /var/... but git resolves to /private/var/...
			tempDir, err := filepath.EvalSymlinks(tempDir)
			if err != nil {
				t.Fatalf("failed to resolve symlinks: %v", err)
			}

			repo, err := git.PlainInit(tempDir, false)
			if err != nil {
				t.Fatalf("failed to init git repo: %v", err)
			}

			worktree, err := repo.Worktree()
			if err != nil {
				t.Fatalf("failed to get worktree: %v", err)
			}

			mainFile := filepath.Join(tempDir, "main.go")
			if err := os.WriteFile(mainFile, []byte("package main\n"), 0o644); err != nil {
				t.Fatalf("failed to write main.go: %v", err)
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

			updatedContent := "package main\n\nfunc main() {}\n"
			if err := os.WriteFile(mainFile, []byte(updatedContent), 0o644); err != nil {
				t.Fatalf("failed to update main.go: %v", err)
			}

			t.Chdir(tempDir)

			metadataDir := filepath.Join(tempDir, ".trace", "metadata", "test-session")
			if err := os.MkdirAll(metadataDir, 0o755); err != nil {
				t.Fatalf("failed to create metadata dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(metadataDir, "full.jsonl"), []byte(`{"test": true}`), 0o644); err != nil {
				t.Fatalf("failed to write transcript: %v", err)
			}

			store := NewGitStore(repo)
			result, err := store.WriteTemporary(context.Background(), WriteTemporaryOptions{
				SessionID:      "test-session",
				BaseCommit:     initialCommit.String(),
				ModifiedFiles:  tt.modifiedFiles(tempDir, mainFile),
				MetadataDir:    ".trace/metadata/test-session",
				MetadataDirAbs: metadataDir,
				CommitMessage:  "Checkpoint with path normalization",
				AuthorName:     "Test",
				AuthorEmail:    "test@test.com",
			})
			if err != nil {
				t.Fatalf("WriteTemporary() error = %v", err)
			}

			commit, err := repo.CommitObject(result.CommitHash)
			if err != nil {
				t.Fatalf("failed to get commit object: %v", err)
			}

			tree, err := commit.Tree()
			if err != nil {
				t.Fatalf("failed to get tree: %v", err)
			}

			assertNoEmptyEntryNames(t, repo, commit.TreeHash, "")

			file, err := tree.File("main.go")
			if err != nil {
				t.Fatalf("main.go not found in checkpoint tree: %v", err)
			}

			content, err := file.Contents()
			if err != nil {
				t.Fatalf("failed to read main.go content: %v", err)
			}

			wantContent := "package main\n"
			if tt.wantUpdated {
				wantContent = updatedContent
			}
			if content != wantContent {
				t.Errorf("unexpected main.go content\ngot:\n%s\nwant:\n%s", content, wantContent)
			}
		})
	}
}

// TestWriteTemporary_FirstCheckpoint_CapturesUntrackedFiles verifies that
// the first checkpoint captures untracked files that exist in the working directory.
func TestWriteTemporary_FirstCheckpoint_CapturesUntrackedFiles(t *testing.T) {
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

	// Create and commit README.md
	readmeFile := filepath.Join(tempDir, "README.md")
	if err := os.WriteFile(readmeFile, []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("failed to write README: %v", err)
	}
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatalf("failed to add README: %v", err)
	}
	initialCommit, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Create an untracked file (simulating user creating a file before agent starts)
	untrackedFile := filepath.Join(tempDir, "config.local.json")
	untrackedContent := `{"key": "secret_value"}`
	if err := os.WriteFile(untrackedFile, []byte(untrackedContent), 0o644); err != nil {
		t.Fatalf("failed to write untracked file: %v", err)
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
		NewFiles:          []string{}, // NewFiles might be empty if this is truly "at session start"
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

	// Verify the shadow branch commit contains the untracked file
	commit, err := repo.CommitObject(result.CommitHash)
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	// Find config.local.json in the tree
	file, err := tree.File("config.local.json")
	if err != nil {
		t.Fatalf("untracked file config.local.json not found in checkpoint tree: %v", err)
	}

	content, err := file.Contents()
	if err != nil {
		t.Fatalf("failed to read config.local.json content: %v", err)
	}

	if content != untrackedContent {
		t.Errorf("checkpoint should contain untracked file content\ngot:\n%s\nwant:\n%s", content, untrackedContent)
	}
}

// TestWriteTemporary_FirstCheckpoint_ExcludesGitIgnoredFiles verifies that
// the first checkpoint does NOT capture files that are in .gitignore.
func TestWriteTemporary_FirstCheckpoint_ExcludesGitIgnoredFiles(t *testing.T) {
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

	// Create .gitignore that ignores node_modules/
	gitignoreFile := filepath.Join(tempDir, ".gitignore")
	if err := os.WriteFile(gitignoreFile, []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatalf("failed to write .gitignore: %v", err)
	}
	if _, err := worktree.Add(".gitignore"); err != nil {
		t.Fatalf("failed to add .gitignore: %v", err)
	}

	// Create and commit README.md
	readmeFile := filepath.Join(tempDir, "README.md")
	if err := os.WriteFile(readmeFile, []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("failed to write README: %v", err)
	}
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatalf("failed to add README: %v", err)
	}
	initialCommit, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Create node_modules/ directory with a file (should be ignored)
	nodeModulesDir := filepath.Join(tempDir, "node_modules")
	if err := os.MkdirAll(nodeModulesDir, 0o755); err != nil {
		t.Fatalf("failed to create node_modules: %v", err)
	}
	ignoredFile := filepath.Join(nodeModulesDir, "some-package.js")
	if err := os.WriteFile(ignoredFile, []byte("module.exports = {}"), 0o644); err != nil {
		t.Fatalf("failed to write ignored file: %v", err)
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

	// Verify the shadow branch commit does NOT contain node_modules/
	commit, err := repo.CommitObject(result.CommitHash)
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	// node_modules/some-package.js should NOT be in the tree
	_, err = tree.File("node_modules/some-package.js")
	if err == nil {
		t.Error("gitignored file node_modules/some-package.js should NOT be in checkpoint tree")
	} else if !errors.Is(err, object.ErrFileNotFound) && !errors.Is(err, object.ErrEntryNotFound) {
		t.Fatalf("expected node_modules/some-package.js to be absent (ErrFileNotFound/ErrEntryNotFound), got: %v", err)
	}
}

// TestWriteTemporary_SubsequentCheckpoint_ExcludesGitIgnoredModifiedFiles verifies that
// subsequent checkpoints (IsFirstCheckpoint=false) filter out gitignored files from
// ModifiedFiles. This is a security-critical test: if an agent modifies a .env file
// and reports it in its transcript, the .env file must NOT leak into the shadow branch.
// See: https://techstackups.com/guides/trace-io-hands-on-what-it-actually-captures/#what-leaks-into-checkpoints
func TestWriteTemporary_SubsequentCheckpoint_ExcludesGitIgnoredModifiedFiles(t *testing.T) {
	tempDir := t.TempDir()

	testutil.InitRepo(t, tempDir)
	repo, err := git.PlainOpen(tempDir)
	require.NoError(t, err)

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create .gitignore that ignores .env files
	gitignoreContent := ".env\n*.secret\nnode_modules/\n"
	if err := os.WriteFile(filepath.Join(tempDir, ".gitignore"), []byte(gitignoreContent), 0o644); err != nil {
		t.Fatalf("failed to write .gitignore: %v", err)
	}
	if _, err := worktree.Add(".gitignore"); err != nil {
		t.Fatalf("failed to add .gitignore: %v", err)
	}

	// Create and commit a tracked file
	if err := os.WriteFile(filepath.Join(tempDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("failed to write main.go: %v", err)
	}
	if _, err := worktree.Add("main.go"); err != nil {
		t.Fatalf("failed to add main.go: %v", err)
	}
	testutil.GitCommit(t, tempDir, "Initial commit")
	headRef, err := repo.Head()
	require.NoError(t, err)
	initialCommit := headRef.Hash()

	// Create gitignored files on disk (simulating an agent creating/modifying them)
	if err := os.WriteFile(filepath.Join(tempDir, ".env"), []byte("API_KEY=sk-secret-1234\n"), 0o644); err != nil {
		t.Fatalf("failed to write .env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "db.secret"), []byte("password=hunter2\n"), 0o644); err != nil {
		t.Fatalf("failed to write db.secret: %v", err)
	}

	// Also modify a tracked file (this SHOULD be captured)
	if err := os.WriteFile(filepath.Join(tempDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("failed to modify main.go: %v", err)
	}

	t.Chdir(tempDir)

	// Create metadata directory
	metadataDir := filepath.Join(tempDir, ".trace", "metadata", "test-session")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "full.jsonl"), []byte(`{"test": true}`), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	store := NewGitStore(repo)
	baseCommit := initialCommit.String()

	// Write first checkpoint to establish the shadow branch
	firstResult, err := store.WriteTemporary(context.Background(), WriteTemporaryOptions{
		SessionID:         "test-session",
		BaseCommit:        baseCommit,
		ModifiedFiles:     []string{},
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

	// Now write a subsequent checkpoint where the agent reports .env and db.secret
	// as modified files (e.g., agent touched them during its turn).
	// These gitignored files must NOT appear in the checkpoint tree.
	result, err := store.WriteTemporary(context.Background(), WriteTemporaryOptions{
		SessionID:         "test-session",
		BaseCommit:        baseCommit,
		ModifiedFiles:     []string{"main.go", ".env", "db.secret"}, // Agent reports these
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

	// Verify the checkpoint tree (use returned commit hash — works whether skipped or not)
	commit, err := repo.CommitObject(result.CommitHash)
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	// main.go SHOULD be in the tree (tracked file, legitimately modified)
	_, err = tree.File("main.go")
	if err != nil {
		t.Errorf("main.go should be in checkpoint tree: %v", err)
	}

	// .env MUST NOT be in the tree (gitignored — contains API key)
	_, err = tree.File(".env")
	if err == nil {
		t.Error("SECURITY: gitignored file .env leaked into checkpoint tree — API keys exposed on shadow branch")
	}

	// db.secret MUST NOT be in the tree (gitignored)
	_, err = tree.File("db.secret")
	if err == nil {
		t.Error("SECURITY: gitignored file db.secret leaked into checkpoint tree — secrets exposed on shadow branch")
	}
}
