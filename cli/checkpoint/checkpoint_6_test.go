package checkpoint

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/redact"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

func TestUpdateSummary_RedactsSecrets(t *testing.T) {
	t.Parallel()
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("aabbccddeef8")

	// First write a checkpoint without a summary
	err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "update-summary-session",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"msg":"safe"}` + "\n")),
		CheckpointsCount: 1,
		AuthorName:       "Test Author",
		AuthorEmail:      "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() error = %v", err)
	}

	// Now update the summary with a secret
	err = store.UpdateSummary(context.Background(), checkpointID, &Summary{
		Intent:  "Rotated key " + highEntropySecret,
		Outcome: "Done",
	})
	if err != nil {
		t.Fatalf("UpdateSummary() error = %v", err)
	}

	content, err := store.ReadSessionContent(context.Background(), checkpointID, 0)
	if err != nil {
		t.Fatalf("ReadSessionContent() error = %v", err)
	}

	if content.Metadata.Summary == nil {
		t.Fatal("Summary should not be nil after update")
	}
	if strings.Contains(content.Metadata.Summary.Intent, highEntropySecret) {
		t.Error("Updated Summary.Intent should not contain the secret")
	}
	if !strings.Contains(content.Metadata.Summary.Intent, "REDACTED") {
		t.Error("Updated Summary.Intent should contain REDACTED placeholder")
	}
}

func TestWriteCommitted_SubagentTranscript_JSONLFallback(t *testing.T) {
	t.Parallel()
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("aabbccddeef9")

	// Create a temp file with invalid JSONL containing a secret
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "agent.jsonl")
	invalidJSONL := "this is not valid JSON but has a secret " + highEntropySecret + " in it"
	if err := os.WriteFile(transcriptPath, []byte(invalidJSONL), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:           checkpointID,
		SessionID:              "jsonl-fallback-session",
		Strategy:               "manual-commit",
		Transcript:             redact.AlreadyRedacted([]byte(`{"msg":"safe"}` + "\n")),
		CheckpointsCount:       1,
		AuthorName:             "Test Author",
		AuthorEmail:            "test@example.com",
		IsTask:                 true,
		ToolUseID:              "toolu_test123",
		AgentID:                "agent1",
		SubagentTranscriptPath: transcriptPath,
	})
	if err != nil {
		t.Fatalf("WriteCommitted() error = %v", err)
	}

	// Read back the subagent transcript from the tree
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err != nil {
		t.Fatalf("failed to get branch ref: %v", err)
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("failed to get commit: %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	agentPath := checkpointID.Path() + "/tasks/toolu_test123/agent-agent1.jsonl"
	file, err := tree.File(agentPath)
	if err != nil {
		t.Fatalf("subagent transcript should exist at %s (JSONL fallback should not drop it): %v", agentPath, err)
	}

	content, err := file.Contents()
	if err != nil {
		t.Fatalf("failed to read subagent transcript: %v", err)
	}

	// Verify the transcript was stored (not dropped) and secret was redacted
	if content == "" {
		t.Error("subagent transcript should not be empty")
	}
	if strings.Contains(content, highEntropySecret) {
		t.Error("subagent transcript should not contain the secret after fallback redaction")
	}
	if !strings.Contains(content, "REDACTED") {
		t.Error("subagent transcript should contain REDACTED from fallback redaction")
	}
}

func TestWriteTemporaryTask_SubagentTranscript_RedactsSecrets(t *testing.T) {
	// Cannot use t.Parallel() because t.Chdir is required for paths.WorktreeRoot()
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

	t.Chdir(tempDir)

	// Create a temp file with invalid JSONL containing a secret
	transcriptPath := filepath.Join(tempDir, "agent-transcript.jsonl")
	invalidJSONL := "this is not valid JSON but has a secret " + highEntropySecret + " in it"
	if err := os.WriteFile(transcriptPath, []byte(invalidJSONL), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	store := NewGitStore(repo)
	baseCommit := initialCommit.String()

	_, err = store.WriteTemporaryTask(context.Background(), WriteTemporaryTaskOptions{
		SessionID:              "test-session",
		BaseCommit:             baseCommit,
		ToolUseID:              "toolu_test456",
		AgentID:                "agent1",
		SubagentTranscriptPath: transcriptPath,
		CheckpointUUID:         "test-uuid",
		CommitMessage:          "Task checkpoint",
		AuthorName:             "Test",
		AuthorEmail:            "test@test.com",
	})
	if err != nil {
		t.Fatalf("WriteTemporaryTask() error = %v", err)
	}

	// Find the shadow branch and read the subagent transcript
	shadowBranch := ShadowBranchNameForCommit(baseCommit, "")
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(shadowBranch), true)
	if err != nil {
		t.Fatalf("failed to get shadow branch ref: %v", err)
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("failed to get commit: %v", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	agentPath := paths.TraceMetadataDir + "/test-session/tasks/toolu_test456/agent-agent1.jsonl"
	file, err := tree.File(agentPath)
	if err != nil {
		t.Fatalf("subagent transcript should exist at %s: %v", agentPath, err)
	}

	content, err := file.Contents()
	if err != nil {
		t.Fatalf("failed to read subagent transcript: %v", err)
	}

	// Verify the transcript was stored (not dropped) and secret was redacted
	if content == "" {
		t.Error("subagent transcript should not be empty")
	}
	if strings.Contains(content, highEntropySecret) {
		t.Error("subagent transcript on shadow branch should not contain the secret after redaction")
	}
	if !strings.Contains(content, "REDACTED") {
		t.Error("subagent transcript on shadow branch should contain REDACTED")
	}
}

func TestAddDirectoryToEntries_PathTraversal(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create a directory structure where the relative path could escape
	metadataDir := filepath.Join(tempDir, "metadata")
	subDir := filepath.Join(metadataDir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("failed to create dirs: %v", err)
	}

	// Create a regular file — should be included
	regularFile := filepath.Join(subDir, "data.txt")
	if err := os.WriteFile(regularFile, []byte("safe content"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	entries := make(map[string]object.TreeEntry)
	err = addDirectoryToEntriesWithAbsPath(repo, metadataDir, ".trace/metadata/session", entries)
	if err != nil {
		t.Fatalf("addDirectoryToEntriesWithAbsPath failed: %v", err)
	}

	// Verify the regular file was included with correct path
	expectedPath := filepath.ToSlash(filepath.Join(".trace/metadata/session", "sub", "data.txt"))
	if _, ok := entries[expectedPath]; !ok {
		t.Errorf("expected entry at %q, got entries: %v", expectedPath, entries)
	}
}

func TestAddDirectoryToEntries_SkipsSymlinks(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create metadata directory
	metadataDir := filepath.Join(tempDir, "metadata")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}

	// Create a regular file
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

	entries := make(map[string]object.TreeEntry)
	err = addDirectoryToEntriesWithAbsPath(repo, metadataDir, "checkpoint/", entries)
	if err != nil {
		t.Fatalf("addDirectoryToEntriesWithAbsPath failed: %v", err)
	}

	// Verify regular file was included
	if _, ok := entries["checkpoint/regular.txt"]; !ok {
		t.Error("regular.txt should be included in entries")
	}

	// Verify symlink was NOT included
	if _, ok := entries["checkpoint/sneaky-link"]; ok {
		t.Error("symlink should NOT be included in entries — this would allow reading files outside the metadata directory")
	}

	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}

func TestAddDirectoryToEntries_SkipsSymlinkedDirectories(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create metadata directory with a regular file
	metadataDir := filepath.Join(tempDir, "metadata")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}
	regularFile := filepath.Join(metadataDir, "regular.txt")
	if err := os.WriteFile(regularFile, []byte("regular content"), 0o644); err != nil {
		t.Fatalf("failed to create regular file: %v", err)
	}

	// Create an external directory with sensitive files
	externalDir := filepath.Join(tempDir, "external-secrets")
	if err := os.MkdirAll(externalDir, 0o755); err != nil {
		t.Fatalf("failed to create external dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(externalDir, "secret.txt"), []byte("SECRET DATA"), 0o644); err != nil {
		t.Fatalf("failed to create secret file: %v", err)
	}

	// Create a symlink to the external directory inside metadata
	symlinkDir := filepath.Join(metadataDir, "evil-dir-link")
	if err := os.Symlink(externalDir, symlinkDir); err != nil {
		t.Fatalf("failed to create directory symlink: %v", err)
	}

	entries := make(map[string]object.TreeEntry)
	err = addDirectoryToEntriesWithAbsPath(repo, metadataDir, "checkpoint/", entries)
	if err != nil {
		t.Fatalf("addDirectoryToEntriesWithAbsPath failed: %v", err)
	}

	// Verify regular file was included
	if _, ok := entries["checkpoint/regular.txt"]; !ok {
		t.Error("regular.txt should be included in entries")
	}

	// Verify files from the symlinked directory were NOT included
	if _, ok := entries["checkpoint/evil-dir-link/secret.txt"]; ok {
		t.Error("files inside symlinked directory should NOT be included — this would allow reading files outside the metadata directory")
	}

	if len(entries) != 1 {
		t.Errorf("expected 1 entry (regular.txt only), got %d: %v", len(entries), entries)
	}
}

// TestWriteTemporaryTask_ExcludesGitIgnoredFiles verifies that task (subagent)
// checkpoints also filter out gitignored files. This is the same vulnerability as
// the WriteTemporary path — a subagent that touches .env must not leak it into the
// shadow branch.
func TestWriteTemporaryTask_ExcludesGitIgnoredFiles(t *testing.T) {
	tempDir := t.TempDir()

	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create .gitignore that ignores .env
	if err := os.WriteFile(filepath.Join(tempDir, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
		t.Fatalf("failed to write .gitignore: %v", err)
	}
	if _, err := worktree.Add(".gitignore"); err != nil {
		t.Fatalf("failed to add .gitignore: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tempDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
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

	// Create gitignored .env file and a legitimate file on disk
	if err := os.WriteFile(filepath.Join(tempDir, ".env"), []byte("API_KEY=sk-secret-1234\n"), 0o644); err != nil {
		t.Fatalf("failed to write .env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "handler.go"), []byte("package main\n\nfunc handler() {}\n"), 0o644); err != nil {
		t.Fatalf("failed to write handler.go: %v", err)
	}

	t.Chdir(tempDir)

	// Create subagent transcript file
	transcriptPath := filepath.Join(tempDir, "agent-transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"role":"assistant","content":"done"}`+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	store := NewGitStore(repo)
	baseCommit := initialCommit.String()

	// Write task checkpoint where subagent reports .env as modified
	commitHash, err := store.WriteTemporaryTask(context.Background(), WriteTemporaryTaskOptions{
		SessionID:              "test-session",
		BaseCommit:             baseCommit,
		ToolUseID:              "toolu_test789",
		AgentID:                "agent1",
		ModifiedFiles:          []string{"handler.go", ".env"}, // Subagent reports both
		NewFiles:               []string{},
		DeletedFiles:           []string{},
		SubagentTranscriptPath: transcriptPath,
		CheckpointUUID:         "test-uuid",
		CommitMessage:          "Task checkpoint",
		AuthorName:             "Test",
		AuthorEmail:            "test@test.com",
	})
	if err != nil {
		t.Fatalf("WriteTemporaryTask() error = %v", err)
	}

	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	// handler.go SHOULD be in the tree
	_, err = tree.File("handler.go")
	if err != nil {
		t.Errorf("handler.go should be in task checkpoint tree: %v", err)
	}

	// .env MUST NOT be in the tree
	_, err = tree.File(".env")
	if err == nil {
		t.Error("SECURITY: gitignored file .env leaked into task checkpoint tree — secrets exposed on shadow branch via subagent")
	}
}
