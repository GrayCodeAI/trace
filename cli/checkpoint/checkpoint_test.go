package checkpoint

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/trailers"
	"github.com/GrayCodeAI/trace/cli/vercelconfig"
	"github.com/GrayCodeAI/trace/redact"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

func TestCheckpointType_Values(t *testing.T) {
	// Verify the enum values are distinct
	if Temporary == Committed {
		t.Error("Temporary and Committed should have different values")
	}

	// Verify Temporary is the zero value (default for Type)
	var defaultType Type
	if defaultType != Temporary {
		t.Errorf("expected zero value of Type to be Temporary, got %d", defaultType)
	}
}

func TestCopyMetadataDir_SkipsSymlinks(t *testing.T) {
	// Create a temp directory for the test
	tempDir := t.TempDir()

	// Initialize a git repository
	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create metadata directory structure
	metadataDir := filepath.Join(tempDir, "metadata")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}

	// Create a regular file that should be included
	regularFile := filepath.Join(metadataDir, "regular.txt")
	if err := os.WriteFile(regularFile, []byte("regular content"), 0o644); err != nil {
		t.Fatalf("failed to create regular file: %v", err)
	}

	// Create a sensitive file outside the metadata directory
	sensitiveFile := filepath.Join(tempDir, "sensitive.txt")
	if err := os.WriteFile(sensitiveFile, []byte("SECRET DATA"), 0o644); err != nil {
		t.Fatalf("failed to create sensitive file: %v", err)
	}

	// Create a symlink inside metadata directory pointing to the sensitive file
	symlinkPath := filepath.Join(metadataDir, "sneaky-link")
	if err := os.Symlink(sensitiveFile, symlinkPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	// Create GitStore and call copyMetadataDir
	store := NewGitStore(repo)
	entries := make(map[string]object.TreeEntry)

	err = store.copyMetadataDir(metadataDir, "checkpoint/", entries)
	if err != nil {
		t.Fatalf("copyMetadataDir failed: %v", err)
	}

	// Verify regular file was included
	if _, ok := entries["checkpoint/regular.txt"]; !ok {
		t.Error("regular.txt should be included in entries")
	}

	// Verify symlink was NOT included (security fix)
	if _, ok := entries["checkpoint/sneaky-link"]; ok {
		t.Error("symlink should NOT be included in entries - this would allow reading files outside the metadata directory")
	}

	// Verify the correct number of entries
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}

// TestWriteCommitted_AgentField verifies that the Agent field is written
// to both metadata.json and the commit message trailer.
func TestWriteCommitted_AgentField(t *testing.T) {
	tempDir := t.TempDir()

	// Initialize a git repository with an initial commit
	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create worktree and make initial commit
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	readmeFile := filepath.Join(tempDir, "README.md")
	if err := os.WriteFile(readmeFile, []byte("# Test"), 0o644); err != nil {
		t.Fatalf("failed to write README: %v", err)
	}
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatalf("failed to add README: %v", err)
	}
	if _, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	}); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Create checkpoint store
	store := NewGitStore(repo)

	// Write a committed checkpoint with Agent field
	checkpointID := id.MustCheckpointID("a1b2c3d4e5f6")
	sessionID := "test-session-123"
	agentType := agent.AgentTypeClaudeCode

	err = store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID: checkpointID,
		SessionID:    sessionID,
		Strategy:     "manual-commit",
		Agent:        agentType,
		Transcript:   redact.AlreadyRedacted([]byte("test transcript content")),
		AuthorName:   "Test Author",
		AuthorEmail:  "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() error = %v", err)
	}

	// Verify root metadata.json contains agents in the Agents array
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err != nil {
		t.Fatalf("failed to get metadata branch reference: %v", err)
	}

	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	// Read root metadata.json from the sharded path
	shardedPath := checkpointID.Path()
	checkpointTree, err := tree.Tree(shardedPath)
	if err != nil {
		t.Fatalf("failed to find checkpoint tree at %s: %v", shardedPath, err)
	}

	metadataFile, err := checkpointTree.File(paths.MetadataFileName)
	if err != nil {
		t.Fatalf("failed to find metadata.json: %v", err)
	}

	content, err := metadataFile.Contents()
	if err != nil {
		t.Fatalf("failed to read metadata.json: %v", err)
	}

	// Root metadata is now CheckpointSummary (without Agents array)
	var summary CheckpointSummary
	if err := json.Unmarshal([]byte(content), &summary); err != nil {
		t.Fatalf("failed to parse metadata.json as CheckpointSummary: %v", err)
	}

	// Agent should be in the session-level metadata, not in the summary
	// Read first session's metadata to verify agent (0-based indexing)
	if len(summary.Sessions) > 0 {
		sessionTree, err := checkpointTree.Tree("0")
		if err != nil {
			t.Fatalf("failed to get session tree: %v", err)
		}
		sessionMetadataFile, err := sessionTree.File(paths.MetadataFileName)
		if err != nil {
			t.Fatalf("failed to find session metadata.json: %v", err)
		}
		sessionContent, err := sessionMetadataFile.Contents()
		if err != nil {
			t.Fatalf("failed to read session metadata.json: %v", err)
		}
		var sessionMetadata CommittedMetadata
		if err := json.Unmarshal([]byte(sessionContent), &sessionMetadata); err != nil {
			t.Fatalf("failed to parse session metadata.json: %v", err)
		}
		if sessionMetadata.Agent != agentType {
			t.Errorf("sessionMetadata.Agent = %q, want %q", sessionMetadata.Agent, agentType)
		}
	}

	// Verify commit message contains Trace-Agent trailer
	if !strings.Contains(commit.Message, trailers.AgentTrailerKey+": "+string(agentType)) {
		t.Errorf("commit message should contain %s trailer with value %q, got:\n%s",
			trailers.AgentTrailerKey, agentType, commit.Message)
	}
}

// readLatestSessionMetadata reads the session-specific metadata from the latest session subdirectory.
// This is where session-specific fields like Summary are stored.
func readLatestSessionMetadata(t *testing.T, repo *git.Repository, checkpointID id.CheckpointID) CommittedMetadata {
	t.Helper()

	ref, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err != nil {
		t.Fatalf("failed to get metadata branch reference: %v", err)
	}

	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	checkpointTree, err := tree.Tree(checkpointID.Path())
	if err != nil {
		t.Fatalf("failed to get checkpoint tree: %v", err)
	}

	// Read root metadata.json to get session count
	rootFile, err := checkpointTree.File(paths.MetadataFileName)
	if err != nil {
		t.Fatalf("failed to find root metadata.json: %v", err)
	}

	rootContent, err := rootFile.Contents()
	if err != nil {
		t.Fatalf("failed to read root metadata.json: %v", err)
	}

	var summary CheckpointSummary
	if err := json.Unmarshal([]byte(rootContent), &summary); err != nil {
		t.Fatalf("failed to parse root metadata.json: %v", err)
	}

	// Read session-level metadata from latest session subdirectory (0-based indexing)
	latestIndex := len(summary.Sessions) - 1
	sessionDir := strconv.Itoa(latestIndex)
	sessionTree, err := checkpointTree.Tree(sessionDir)
	if err != nil {
		t.Fatalf("failed to get session tree at %s: %v", sessionDir, err)
	}

	sessionFile, err := sessionTree.File(paths.MetadataFileName)
	if err != nil {
		t.Fatalf("failed to find session metadata.json: %v", err)
	}

	content, err := sessionFile.Contents()
	if err != nil {
		t.Fatalf("failed to read session metadata.json: %v", err)
	}

	var metadata CommittedMetadata
	if err := json.Unmarshal([]byte(content), &metadata); err != nil {
		t.Fatalf("failed to parse session metadata.json: %v", err)
	}

	return metadata
}

// Note: Tests for Agents array and SessionCount fields have been removed
// as those fields were removed from CommittedMetadata in the simplification.

// TestWriteTemporary_Deduplication verifies that WriteTemporary skips creating
// a new commit when the tree hash matches the previous checkpoint.
func TestWriteTemporary_Deduplication(t *testing.T) {
	tempDir := t.TempDir()

	// Initialize a git repository with an initial commit
	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create worktree and make initial commit
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	readmeFile := filepath.Join(tempDir, "README.md")
	if err := os.WriteFile(readmeFile, []byte("# Test"), 0o644); err != nil {
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

	// Change to temp dir so paths.WorktreeRoot() works correctly
	t.Chdir(tempDir)

	// Create a test file that will be included in checkpoints
	testFile := filepath.Join(tempDir, "test.go")
	if err := os.WriteFile(testFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Create metadata directory
	metadataDir := filepath.Join(tempDir, ".trace", "metadata", "test-session")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "full.jsonl"), []byte(`{"test": true}`), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Create checkpoint store
	store := NewGitStore(repo)

	// First checkpoint should be created
	baseCommit := initialCommit.String()
	result1, err := store.WriteTemporary(context.Background(), WriteTemporaryOptions{
		SessionID:         "test-session",
		BaseCommit:        baseCommit,
		ModifiedFiles:     []string{"test.go"},
		MetadataDir:       ".trace/metadata/test-session",
		MetadataDirAbs:    metadataDir,
		CommitMessage:     "Checkpoint 1",
		AuthorName:        "Test",
		AuthorEmail:       "test@test.com",
		IsFirstCheckpoint: true,
	})
	if err != nil {
		t.Fatalf("WriteTemporary() first call error = %v", err)
	}
	if result1.Skipped {
		t.Error("first checkpoint should not be skipped")
	}
	if result1.CommitHash == plumbing.ZeroHash {
		t.Error("first checkpoint should have a commit hash")
	}

	// Second checkpoint with identical content should be skipped
	result2, err := store.WriteTemporary(context.Background(), WriteTemporaryOptions{
		SessionID:         "test-session",
		BaseCommit:        baseCommit,
		ModifiedFiles:     []string{"test.go"},
		MetadataDir:       ".trace/metadata/test-session",
		MetadataDirAbs:    metadataDir,
		CommitMessage:     "Checkpoint 2",
		AuthorName:        "Test",
		AuthorEmail:       "test@test.com",
		IsFirstCheckpoint: false,
	})
	if err != nil {
		t.Fatalf("WriteTemporary() second call error = %v", err)
	}
	if !result2.Skipped {
		t.Error("second checkpoint with identical content should be skipped")
	}
	if result2.CommitHash != result1.CommitHash {
		t.Errorf("skipped checkpoint should return previous commit hash, got %s, want %s",
			result2.CommitHash, result1.CommitHash)
	}

	// Modify the file and create another checkpoint - should NOT be skipped
	if err := os.WriteFile(testFile, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("failed to modify test file: %v", err)
	}

	result3, err := store.WriteTemporary(context.Background(), WriteTemporaryOptions{
		SessionID:         "test-session",
		BaseCommit:        baseCommit,
		ModifiedFiles:     []string{"test.go"},
		MetadataDir:       ".trace/metadata/test-session",
		MetadataDirAbs:    metadataDir,
		CommitMessage:     "Checkpoint 3",
		AuthorName:        "Test",
		AuthorEmail:       "test@test.com",
		IsFirstCheckpoint: false,
	})
	if err != nil {
		t.Fatalf("WriteTemporary() third call error = %v", err)
	}
	if result3.Skipped {
		t.Error("third checkpoint with modified content should NOT be skipped")
	}
	if result3.CommitHash == result1.CommitHash {
		t.Error("third checkpoint should have a different commit hash than first")
	}
}

// setupBranchTestRepo creates a test repository with an initial commit.
func setupBranchTestRepo(t *testing.T) (*git.Repository, plumbing.Hash) {
	t.Helper()
	tempDir := t.TempDir()

	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	readmeFile := filepath.Join(tempDir, "README.md")
	if err := os.WriteFile(readmeFile, []byte("# Test"), 0o644); err != nil {
		t.Fatalf("failed to write README: %v", err)
	}
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatalf("failed to add README: %v", err)
	}
	commitHash, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	return repo, commitHash
}

func TestEnsureSessionsBranch_WritesVercelConfigWhenEnabled(t *testing.T) {
	vercelconfig.ResetSettingsCache()
	t.Cleanup(vercelconfig.ResetSettingsCache)

	repo, _ := setupBranchTestRepo(t)
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("repo.Worktree() error = %v", err)
	}
	t.Chdir(worktree.Filesystem().Root())

	traceDir := filepath.Join(worktree.Filesystem().Root(), ".trace")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatalf("mkdir .trace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(traceDir, "settings.json"), []byte(`{"enabled":true,"vercel":true}`), 0o644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}

	store := NewGitStore(repo)
	if err := store.ensureSessionsBranch(context.Background()); err != nil {
		t.Fatalf("ensureSessionsBranch() error = %v", err)
	}

	ref, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err != nil {
		t.Fatalf("metadata branch ref: %v", err)
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("metadata commit: %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("metadata tree: %v", err)
	}
	file, err := tree.File(vercelconfig.FileName)
	if err != nil {
		t.Fatalf("expected %s on metadata branch: %v", vercelconfig.FileName, err)
	}
	content, err := file.Contents()
	if err != nil {
		t.Fatalf("read %s: %v", vercelconfig.FileName, err)
	}

	var config map[string]any
	if err := json.Unmarshal([]byte(content), &config); err != nil {
		t.Fatalf("parse %s: %v", vercelconfig.FileName, err)
	}
	if !vercelconfig.DeploymentDisabled(config) {
		t.Fatalf("expected %s to disable %s, got %s", vercelconfig.FileName, vercelconfig.BranchPattern, content)
	}
}

func TestWriteCommitted_MergesVercelConfigOnMetadataBranch(t *testing.T) {
	vercelconfig.ResetSettingsCache()
	t.Cleanup(vercelconfig.ResetSettingsCache)

	repo, _ := setupBranchTestRepo(t)
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("repo.Worktree() error = %v", err)
	}
	repoRoot := worktree.Filesystem().Root()
	t.Chdir(repoRoot)

	traceDir := filepath.Join(repoRoot, ".trace")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatalf("mkdir .trace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(traceDir, "settings.json"), []byte(`{"enabled":true,"vercel":true}`), 0o644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}

	initialConfig := []byte(`{
  "cleanUrls": true,
  "git": {
    "deploymentEnabled": {
      "main": true
    }
  }
}
`)
	blobHash, err := CreateBlobFromContent(repo, initialConfig)
	if err != nil {
		t.Fatalf("CreateBlobFromContent() error = %v", err)
	}
	treeHash, err := BuildTreeFromEntries(context.Background(), repo, map[string]object.TreeEntry{
		vercelconfig.FileName: {Name: vercelconfig.FileName, Mode: filemode.Regular, Hash: blobHash},
	})
	if err != nil {
		t.Fatalf("BuildTreeFromEntries() error = %v", err)
	}

	store := NewGitStore(repo)
	commitHash, err := store.createCommit(context.Background(), treeHash, plumbing.ZeroHash, "Initialize metadata branch", "Test", "test@test.com")
	if err != nil {
		t.Fatalf("createCommit() error = %v", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), commitHash)); err != nil {
		t.Fatalf("set metadata branch ref: %v", err)
	}

	cpID := id.MustCheckpointID("abcdef123456")
	err = store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "test-session-id",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte(`{"test": true}`)),
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() error = %v", err)
	}

	ref, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err != nil {
		t.Fatalf("metadata branch ref: %v", err)
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("metadata commit: %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("metadata tree: %v", err)
	}
	file, err := tree.File(vercelconfig.FileName)
	if err != nil {
		t.Fatalf("expected %s on metadata branch: %v", vercelconfig.FileName, err)
	}
	content, err := file.Contents()
	if err != nil {
		t.Fatalf("read %s: %v", vercelconfig.FileName, err)
	}

	var config map[string]any
	if err := json.Unmarshal([]byte(content), &config); err != nil {
		t.Fatalf("parse %s: %v", vercelconfig.FileName, err)
	}
	if config["cleanUrls"] != true {
		t.Fatalf("expected cleanUrls to be preserved, got %#v", config["cleanUrls"])
	}
	gitConfig, ok := config["git"].(map[string]any)
	if !ok {
		t.Fatalf("expected git object, got %#v", config["git"])
	}
	deploymentEnabled, ok := gitConfig["deploymentEnabled"].(map[string]any)
	if !ok {
		t.Fatalf("expected deploymentEnabled object, got %#v", gitConfig["deploymentEnabled"])
	}
	if deploymentEnabled["main"] != true {
		t.Fatalf("expected main rule to be preserved, got %#v", deploymentEnabled["main"])
	}
	if deploymentEnabled[vercelconfig.BranchPattern] != false {
		t.Fatalf("expected %s to be disabled, got %#v", vercelconfig.BranchPattern, deploymentEnabled[vercelconfig.BranchPattern])
	}
}

// verifyBranchInMetadata reads and verifies the branch field in metadata.json.
func verifyBranchInMetadata(t *testing.T, repo *git.Repository, checkpointID id.CheckpointID, expectedBranch string, shouldOmit bool) {
	t.Helper()

	metadataRef, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err != nil {
		t.Fatalf("failed to get metadata branch reference: %v", err)
	}

	commit, err := repo.CommitObject(metadataRef.Hash())
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	shardedPath := checkpointID.Path()
	metadataPath := shardedPath + "/" + paths.MetadataFileName
	metadataFile, err := tree.File(metadataPath)
	if err != nil {
		t.Fatalf("failed to find metadata.json at %s: %v", metadataPath, err)
	}

	content, err := metadataFile.Contents()
	if err != nil {
		t.Fatalf("failed to read metadata.json: %v", err)
	}

	var metadata CommittedMetadata
	if err := json.Unmarshal([]byte(content), &metadata); err != nil {
		t.Fatalf("failed to parse metadata.json: %v", err)
	}

	if metadata.Branch != expectedBranch {
		t.Errorf("metadata.Branch = %q, want %q", metadata.Branch, expectedBranch)
	}

	if shouldOmit && strings.Contains(content, `"branch"`) {
		t.Errorf("metadata.json should not contain 'branch' field when empty (omitempty), got:\n%s", content)
	}
}

// TestWriteCommitted_BranchField verifies that the Branch field is correctly
// captured in metadata.json when on a branch, and is empty when in detached HEAD.
func TestWriteCommitted_BranchField(t *testing.T) {
	t.Run("on branch", func(t *testing.T) {
		repo, commitHash := setupBranchTestRepo(t)

		// Create a feature branch and switch to it
		branchName := "feature/test-branch"
		branchRef := plumbing.NewBranchReferenceName(branchName)
		ref := plumbing.NewHashReference(branchRef, commitHash)
		if err := repo.Storer.SetReference(ref); err != nil {
			t.Fatalf("failed to create branch: %v", err)
		}

		worktree, err := repo.Worktree()
		if err != nil {
			t.Fatalf("failed to get worktree: %v", err)
		}
		if err := worktree.Checkout(&git.CheckoutOptions{Branch: branchRef}); err != nil {
			t.Fatalf("failed to checkout branch: %v", err)
		}

		// Get current branch name
		var currentBranch string
		head, err := repo.Head()
		if err == nil && head.Name().IsBranch() {
			currentBranch = head.Name().Short()
		}

		// Write a committed checkpoint with branch information
		checkpointID := id.MustCheckpointID("a1b2c3d4e5f6")
		store := NewGitStore(repo)
		err = store.WriteCommitted(context.Background(), WriteCommittedOptions{
			CheckpointID: checkpointID,
			SessionID:    "test-session-123",
			Strategy:     "manual-commit",
			Branch:       currentBranch,
			Transcript:   redact.AlreadyRedacted([]byte("test transcript content")),
			AuthorName:   "Test Author",
			AuthorEmail:  "test@example.com",
		})
		if err != nil {
			t.Fatalf("WriteCommitted() error = %v", err)
		}

		verifyBranchInMetadata(t, repo, checkpointID, branchName, false)
	})

	t.Run("detached HEAD", func(t *testing.T) {
		repo, commitHash := setupBranchTestRepo(t)

		// Checkout the commit directly (detached HEAD)
		worktree, err := repo.Worktree()
		if err != nil {
			t.Fatalf("failed to get worktree: %v", err)
		}
		if err := worktree.Checkout(&git.CheckoutOptions{Hash: commitHash}); err != nil {
			t.Fatalf("failed to checkout commit: %v", err)
		}

		// Verify we're in detached HEAD
		head, err := repo.Head()
		if err != nil {
			t.Fatalf("failed to get HEAD: %v", err)
		}
		if head.Name().IsBranch() {
			t.Fatalf("expected detached HEAD, but on branch %s", head.Name().Short())
		}

		// Write a committed checkpoint (branch should be empty in detached HEAD)
		checkpointID := id.MustCheckpointID("b2c3d4e5f6a7")
		store := NewGitStore(repo)
		err = store.WriteCommitted(context.Background(), WriteCommittedOptions{
			CheckpointID: checkpointID,
			SessionID:    "test-session-456",
			Strategy:     "manual-commit",
			Branch:       "", // Empty when in detached HEAD
			Transcript:   redact.AlreadyRedacted([]byte("test transcript content")),
			AuthorName:   "Test Author",
			AuthorEmail:  "test@example.com",
		})
		if err != nil {
			t.Fatalf("WriteCommitted() error = %v", err)
		}

		verifyBranchInMetadata(t, repo, checkpointID, "", true)
	})
}

// TestUpdateSummary verifies that UpdateSummary correctly updates the summary
// field in an existing checkpoint's metadata.
func TestUpdateSummary(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("f1e2d3c4b5a6")

	// First, create a checkpoint without a summary
	err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID: checkpointID,
		SessionID:    "test-session-summary",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte("test transcript content")),
		FilesTouched: []string{"file1.go", "file2.go"},
		AuthorName:   "Test Author",
		AuthorEmail:  "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() error = %v", err)
	}

	// Verify no summary initially (summary is stored in session-level metadata)
	metadata := readLatestSessionMetadata(t, repo, checkpointID)
	if metadata.Summary != nil {
		t.Error("initial checkpoint should not have a summary")
	}

	// Update with a summary
	summary := &Summary{
		Intent:  "Test intent",
		Outcome: "Test outcome",
		Learnings: LearningsSummary{
			Repo:     []string{"Repo learning 1"},
			Code:     []CodeLearning{{Path: "file1.go", Line: 10, Finding: "Code finding"}},
			Workflow: []string{"Workflow learning"},
		},
		Friction:  []string{"Some friction"},
		OpenItems: []string{"Open item 1"},
	}

	err = store.UpdateSummary(context.Background(), checkpointID, summary)
	if err != nil {
		t.Fatalf("UpdateSummary() error = %v", err)
	}

	// Verify summary was saved (in session-level metadata)
	updatedMetadata := readLatestSessionMetadata(t, repo, checkpointID)
	if updatedMetadata.Summary == nil {
		t.Fatal("updated checkpoint should have a summary")
	}
	if updatedMetadata.Summary.Intent != "Test intent" {
		t.Errorf("summary.Intent = %q, want %q", updatedMetadata.Summary.Intent, "Test intent")
	}
	if updatedMetadata.Summary.Outcome != "Test outcome" {
		t.Errorf("summary.Outcome = %q, want %q", updatedMetadata.Summary.Outcome, "Test outcome")
	}
	if len(updatedMetadata.Summary.Learnings.Repo) != 1 {
		t.Errorf("summary.Learnings.Repo length = %d, want 1", len(updatedMetadata.Summary.Learnings.Repo))
	}
	if len(updatedMetadata.Summary.Friction) != 1 {
		t.Errorf("summary.Friction length = %d, want 1", len(updatedMetadata.Summary.Friction))
	}

	// Verify other metadata fields are preserved
	if updatedMetadata.SessionID != "test-session-summary" {
		t.Errorf("metadata.SessionID = %q, want %q", updatedMetadata.SessionID, "test-session-summary")
	}
	if len(updatedMetadata.FilesTouched) != 2 {
		t.Errorf("metadata.FilesTouched length = %d, want 2", len(updatedMetadata.FilesTouched))
	}
}
