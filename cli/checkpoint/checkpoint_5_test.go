package checkpoint

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/versioninfo"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// TestWriteCommitted_DuplicateSessionIDSingleSession verifies that writing
// the same session ID twice when it's the only session updates in-place.
func TestWriteCommitted_DuplicateSessionIDSingleSession(t *testing.T) {
	t.Parallel()
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("dedb07654321")

	// Write session "X" with initial data
	err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "session-X",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"message": "v1"}`)),
		FilesTouched:     []string{"old.go"},
		CheckpointsCount: 1,
		AuthorName:       "Test Author",
		AuthorEmail:      "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() v1 error = %v", err)
	}

	// Write session "X" again with updated data
	err = store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "session-X",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"message": "v2"}`)),
		FilesTouched:     []string{"new.go"},
		CheckpointsCount: 5,
		AuthorName:       "Test Author",
		AuthorEmail:      "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() v2 error = %v", err)
	}

	// Read the checkpoint summary
	summary, err := store.ReadCommitted(context.Background(), checkpointID)
	if err != nil {
		t.Fatalf("ReadCommitted() error = %v", err)
	}
	require.NotNil(t, summary, "ReadCommitted() returned nil summary")

	// Should have 1 session, not 2
	if len(summary.Sessions) != 1 {
		t.Errorf("len(summary.Sessions) = %d, want 1 (duplicate should be replaced)", len(summary.Sessions))
	}

	// Verify session has updated data
	content, err := store.ReadSessionContent(context.Background(), checkpointID, 0)
	if err != nil {
		t.Fatalf("ReadSessionContent(0) error = %v", err)
	}
	if content.Metadata.SessionID != "session-X" {
		t.Errorf("session 0 SessionID = %q, want %q", content.Metadata.SessionID, "session-X")
	}
	if content.Metadata.CheckpointsCount != 5 {
		t.Errorf("session 0 CheckpointsCount = %d, want 5 (updated value)", content.Metadata.CheckpointsCount)
	}
	if !strings.Contains(string(content.Transcript), "v2") {
		t.Errorf("session 0 transcript should contain 'v2', got %s", string(content.Transcript))
	}

	// Verify aggregated stats match the single session
	if summary.CheckpointsCount != 5 {
		t.Errorf("summary.CheckpointsCount = %d, want 5", summary.CheckpointsCount)
	}
	expectedFiles := []string{"new.go"}
	if len(summary.FilesTouched) != 1 || summary.FilesTouched[0] != "new.go" {
		t.Errorf("summary.FilesTouched = %v, want %v", summary.FilesTouched, expectedFiles)
	}
}

// TestWriteCommitted_DuplicateSessionIDReusesIndex verifies that when a session ID
// already exists at index 0, writing it again reuses index 0 (not index 2).
// The session file paths in the summary must point to /0/, not /2/.
func TestWriteCommitted_DuplicateSessionIDReusesIndex(t *testing.T) {
	t.Parallel()
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("dedc0abcdef1")

	// Write session A at index 0
	err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "session-A",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"v": 1}`)),
		CheckpointsCount: 1,
		AuthorName:       "Test",
		AuthorEmail:      "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() session A error = %v", err)
	}

	// Write session B at index 1
	err = store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "session-B",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"v": 2}`)),
		CheckpointsCount: 1,
		AuthorName:       "Test",
		AuthorEmail:      "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() session B error = %v", err)
	}

	// Write session A again — should reuse index 0, not create index 2
	err = store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "session-A",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"v": 3}`)),
		CheckpointsCount: 2,
		AuthorName:       "Test",
		AuthorEmail:      "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() session A v2 error = %v", err)
	}

	summary, err := store.ReadCommitted(context.Background(), checkpointID)
	if err != nil {
		t.Fatalf("ReadCommitted() error = %v", err)
	}

	// Must still be 2 sessions
	if len(summary.Sessions) != 2 {
		t.Fatalf("len(summary.Sessions) = %d, want 2", len(summary.Sessions))
	}

	// Session A's file paths must point to subdirectory /0/, not /2/
	if !strings.Contains(summary.Sessions[0].Transcript, "/0/") {
		t.Errorf("session A should be at index 0, got transcript path %s", summary.Sessions[0].Transcript)
	}

	// Session B stays at /1/
	if !strings.Contains(summary.Sessions[1].Transcript, "/1/") {
		t.Errorf("session B should be at index 1, got transcript path %s", summary.Sessions[1].Transcript)
	}

	// Verify index 0 has the updated content
	content, err := store.ReadSessionContent(context.Background(), checkpointID, 0)
	if err != nil {
		t.Fatalf("ReadSessionContent(0) error = %v", err)
	}
	if content.Metadata.SessionID != "session-A" {
		t.Errorf("session 0 SessionID = %q, want %q", content.Metadata.SessionID, "session-A")
	}
	if !strings.Contains(string(content.Transcript), `"v": 3`) {
		t.Errorf("session 0 should have updated transcript, got %s", string(content.Transcript))
	}
}

// TestWriteCommitted_DuplicateSessionIDClearsStaleFiles verifies that when a session
// is overwritten in-place, optional files from the previous write (prompts, context)
// do not persist if the new write omits them, and sibling session data is untouched.
func TestWriteCommitted_DuplicateSessionIDClearsStaleFiles(t *testing.T) {
	t.Parallel()
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("dedd0abcdef2")

	// Write session A with prompts and context
	err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "session-A",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"v": 1}`)),
		Prompts:          []string{"original prompt"},
		CheckpointsCount: 1,
		AuthorName:       "Test",
		AuthorEmail:      "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() A v1 error = %v", err)
	}

	// Write session B with prompts
	err = store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "session-B",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"session": "B"}`)),
		Prompts:          []string{"B prompt"},
		CheckpointsCount: 1,
		AuthorName:       "Test",
		AuthorEmail:      "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() B error = %v", err)
	}

	// Overwrite session A WITHOUT prompts
	err = store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "session-A",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"v": 2}`)),
		Prompts:          nil,
		CheckpointsCount: 2,
		AuthorName:       "Test",
		AuthorEmail:      "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() A v2 error = %v", err)
	}

	// Session A: stale prompts should be cleared
	contentA, err := store.ReadSessionContent(context.Background(), checkpointID, 0)
	if err != nil {
		t.Fatalf("ReadSessionContent(0) error = %v", err)
	}
	if contentA.Prompts != "" {
		t.Errorf("session A stale prompts should be cleared, got %q", contentA.Prompts)
	}
	if !strings.Contains(string(contentA.Transcript), `"v": 2`) {
		t.Errorf("session A transcript should be updated, got %s", string(contentA.Transcript))
	}

	// Session B: data must be untouched
	contentB, err := store.ReadSessionContent(context.Background(), checkpointID, 1)
	if err != nil {
		t.Fatalf("ReadSessionContent(1) error = %v", err)
	}
	if contentB.Metadata.SessionID != "session-B" {
		t.Errorf("session B SessionID = %q, want %q", contentB.Metadata.SessionID, "session-B")
	}
	if !strings.Contains(contentB.Prompts, "B prompt") {
		t.Errorf("session B prompts should be preserved, got %q", contentB.Prompts)
	}
}

// highEntropySecret is a string with Shannon entropy > 4.5 that will trigger redaction.
const highEntropySecret = "sk-ant-api03-xK9mZ2vL8nQ5rT1wY4bC7dF0gH3jE6pA"

func TestWriteCommitted_PreservesRedactedTranscript(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("aabbccddeef1")

	// Callers redact before passing to WriteCommitted; the store persists as-is.
	rawTranscript := []byte(`{"role":"assistant","content":"Here is your key: ` + highEntropySecret + `"}` + "\n")
	redactedTranscript, err := redact.JSONLBytes(rawTranscript)
	if err != nil {
		t.Fatalf("redact.JSONLBytes() error = %v", err)
	}

	err = store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "redact-transcript-session",
		Strategy:         "manual-commit",
		Transcript:       redactedTranscript,
		CheckpointsCount: 1,
		AuthorName:       "Test Author",
		AuthorEmail:      "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() error = %v", err)
	}

	content, err := store.ReadSessionContent(context.Background(), checkpointID, 0)
	if err != nil {
		t.Fatalf("ReadSessionContent() error = %v", err)
	}

	if strings.Contains(string(content.Transcript), highEntropySecret) {
		t.Error("transcript should not contain the secret after redaction")
	}
	if !strings.Contains(string(content.Transcript), "REDACTED") {
		t.Error("transcript should contain REDACTED placeholder")
	}
}

func TestWriteCommitted_RedactsPromptSecrets(t *testing.T) {
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("aabbccddeef2")

	err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "redact-prompt-session",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"msg":"safe"}`)),
		Prompts:          []string{"Set API_KEY=" + highEntropySecret},
		CheckpointsCount: 1,
		AuthorName:       "Test Author",
		AuthorEmail:      "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() error = %v", err)
	}

	content, err := store.ReadSessionContent(context.Background(), checkpointID, 0)
	if err != nil {
		t.Fatalf("ReadSessionContent() error = %v", err)
	}

	if strings.Contains(content.Prompts, highEntropySecret) {
		t.Error("prompts should not contain the secret after redaction")
	}
	if !strings.Contains(content.Prompts, "REDACTED") {
		t.Error("prompts should contain REDACTED placeholder")
	}
}

func TestCopyMetadataDir_RedactsSecrets(t *testing.T) {
	tempDir := t.TempDir()

	repo, err := git.PlainInit(tempDir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	metadataDir := filepath.Join(tempDir, "metadata")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}

	// Write a JSONL file with a secret
	jsonlFile := filepath.Join(metadataDir, "agent.jsonl")
	if err := os.WriteFile(jsonlFile, []byte(`{"content":"key=`+highEntropySecret+`"}`+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write jsonl file: %v", err)
	}

	// Write a plain text file with a secret
	txtFile := filepath.Join(metadataDir, "notes.txt")
	if err := os.WriteFile(txtFile, []byte("secret: "+highEntropySecret), 0o644); err != nil {
		t.Fatalf("failed to write txt file: %v", err)
	}

	store := NewGitStore(repo)
	entries := make(map[string]object.TreeEntry)

	if err := store.copyMetadataDir(metadataDir, "cp/", entries); err != nil {
		t.Fatalf("copyMetadataDir() error = %v", err)
	}

	// Verify both files were added
	if _, ok := entries["cp/agent.jsonl"]; !ok {
		t.Fatal("agent.jsonl should be in entries")
	}
	if _, ok := entries["cp/notes.txt"]; !ok {
		t.Fatal("notes.txt should be in entries")
	}

	// Read back the blob content and verify redaction
	for path, entry := range entries {
		blob, bErr := repo.BlobObject(entry.Hash)
		if bErr != nil {
			t.Fatalf("failed to read blob for %s: %v", path, bErr)
		}
		reader, rErr := blob.Reader()
		if rErr != nil {
			t.Fatalf("failed to get reader for %s: %v", path, rErr)
		}
		buf := make([]byte, blob.Size)
		if _, rErr = reader.Read(buf); rErr != nil && rErr.Error() != "EOF" {
			t.Fatalf("failed to read blob content for %s: %v", path, rErr)
		}
		reader.Close()

		content := string(buf)
		if strings.Contains(content, highEntropySecret) {
			t.Errorf("%s should not contain the secret after redaction", path)
		}
		if !strings.Contains(content, "REDACTED") {
			t.Errorf("%s should contain REDACTED placeholder", path)
		}
	}
}

// TestWriteCommitted_CLIVersionField verifies that versioninfo.Version is written
// to both the root CheckpointSummary and session-level CommittedMetadata.
func TestWriteCommitted_CLIVersionField(t *testing.T) {
	t.Parallel()

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
	if _, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	}); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	store := NewGitStore(repo)

	checkpointID := id.MustCheckpointID("b1c2d3e4f5a6")
	sessionID := "test-session-version"

	err = store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID: checkpointID,
		SessionID:    sessionID,
		Strategy:     "manual-commit",
		Agent:        agent.AgentTypeClaudeCode,
		Transcript:   redact.AlreadyRedacted([]byte("test transcript")),
		AuthorName:   "Test Author",
		AuthorEmail:  "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() error = %v", err)
	}

	// Read the metadata branch
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
		t.Fatalf("failed to find checkpoint tree at %s: %v", checkpointID.Path(), err)
	}

	// Verify root metadata.json (CheckpointSummary) has CLIVersion
	metadataFile, err := checkpointTree.File(paths.MetadataFileName)
	if err != nil {
		t.Fatalf("failed to find root metadata.json: %v", err)
	}

	content, err := metadataFile.Contents()
	if err != nil {
		t.Fatalf("failed to read root metadata.json: %v", err)
	}

	var summary CheckpointSummary
	if err := json.Unmarshal([]byte(content), &summary); err != nil {
		t.Fatalf("failed to parse root metadata.json: %v", err)
	}

	if summary.CLIVersion != versioninfo.Version {
		t.Errorf("CheckpointSummary.CLIVersion = %q, want %q", summary.CLIVersion, versioninfo.Version)
	}

	// Verify session-level metadata.json (CommittedMetadata) has CLIVersion
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

	if sessionMetadata.CLIVersion != versioninfo.Version {
		t.Errorf("CommittedMetadata.CLIVersion = %q, want %q", sessionMetadata.CLIVersion, versioninfo.Version)
	}
}

func TestWriteCommitted_ModelFieldAlwaysPresent(t *testing.T) {
	t.Parallel()

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
	if _, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	}); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	store := NewGitStore(repo)

	checkpointID := id.MustCheckpointID("c1d2e3f4a5b6")
	err = store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID: checkpointID,
		SessionID:    "test-session-model",
		Strategy:     "manual-commit",
		Agent:        agent.AgentTypeClaudeCode,
		Transcript:   redact.AlreadyRedacted([]byte("test transcript")),
		AuthorName:   "Test Author",
		AuthorEmail:  "test@example.com",
	})
	if err != nil {
		t.Fatalf("WriteCommitted() error = %v", err)
	}

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

	sessionMetadataPath := checkpointID.Path() + "/0/" + paths.MetadataFileName
	sessionMetadataFile, err := tree.File(sessionMetadataPath)
	if err != nil {
		t.Fatalf("failed to find session metadata.json at %s: %v", sessionMetadataPath, err)
	}

	sessionContent, err := sessionMetadataFile.Contents()
	if err != nil {
		t.Fatalf("failed to read session metadata.json: %v", err)
	}

	var sessionMetadata CommittedMetadata
	if err := json.Unmarshal([]byte(sessionContent), &sessionMetadata); err != nil {
		t.Fatalf("failed to parse session metadata.json: %v", err)
	}

	if sessionMetadata.Model != "" {
		t.Errorf("CommittedMetadata.Model = %q, want empty string", sessionMetadata.Model)
	}
	if !strings.Contains(sessionContent, `"model": ""`) {
		t.Errorf("session metadata.json should contain an explicit empty model field, got:\n%s", sessionContent)
	}
}

func TestRedactSummary_Nil(t *testing.T) {
	t.Parallel()
	result := redactSummary(nil)
	if result != nil {
		t.Error("redactSummary(nil) should return nil")
	}
}

func TestRedactSummary_WithSecrets(t *testing.T) {
	t.Parallel()
	summary := &Summary{
		Intent:  "Set API_KEY=" + highEntropySecret,
		Outcome: "Configured key " + highEntropySecret + " successfully",
		Friction: []string{
			"Had to find " + highEntropySecret + " in env",
			"No issues here",
		},
		OpenItems: []string{
			"Rotate " + highEntropySecret,
		},
		Learnings: LearningsSummary{
			Repo: []string{
				"Found secret " + highEntropySecret + " in config",
			},
			Workflow: []string{
				"Use vault for " + highEntropySecret,
			},
			Code: []CodeLearning{
				{
					Path:    "config/secrets.go",
					Line:    42,
					EndLine: 50,
					Finding: "Key " + highEntropySecret + " is hardcoded",
				},
			},
		},
	}

	result := redactSummary(summary)

	// Verify secrets are removed from all text fields
	if strings.Contains(result.Intent, highEntropySecret) {
		t.Error("Intent should not contain the secret")
	}
	if !strings.Contains(result.Intent, "REDACTED") {
		t.Error("Intent should contain REDACTED placeholder")
	}

	if strings.Contains(result.Outcome, highEntropySecret) {
		t.Error("Outcome should not contain the secret")
	}

	if strings.Contains(result.Friction[0], highEntropySecret) {
		t.Error("Friction[0] should not contain the secret")
	}
	if result.Friction[1] != "No issues here" {
		t.Errorf("Friction[1] should be unchanged, got %q", result.Friction[1])
	}

	if strings.Contains(result.OpenItems[0], highEntropySecret) {
		t.Error("OpenItems[0] should not contain the secret")
	}

	if strings.Contains(result.Learnings.Repo[0], highEntropySecret) {
		t.Error("Learnings.Repo[0] should not contain the secret")
	}

	if strings.Contains(result.Learnings.Workflow[0], highEntropySecret) {
		t.Error("Learnings.Workflow[0] should not contain the secret")
	}

	// Verify CodeLearning structural fields preserved, Finding redacted
	cl := result.Learnings.Code[0]
	if cl.Path != "config/secrets.go" {
		t.Errorf("CodeLearning.Path should be preserved, got %q", cl.Path)
	}
	if cl.Line != 42 {
		t.Errorf("CodeLearning.Line should be preserved, got %d", cl.Line)
	}
	if cl.EndLine != 50 {
		t.Errorf("CodeLearning.EndLine should be preserved, got %d", cl.EndLine)
	}
	if strings.Contains(cl.Finding, highEntropySecret) {
		t.Error("CodeLearning.Finding should not contain the secret")
	}
	if !strings.Contains(cl.Finding, "REDACTED") {
		t.Error("CodeLearning.Finding should contain REDACTED placeholder")
	}

	// Verify original is not mutated
	if !strings.Contains(summary.Intent, highEntropySecret) {
		t.Error("original Summary.Intent should not be mutated")
	}
}

func TestRedactSummary_NoSecrets(t *testing.T) {
	t.Parallel()
	summary := &Summary{
		Intent:    "Fix a bug",
		Outcome:   "Bug fixed",
		Friction:  []string{"None"},
		OpenItems: []string{},
		Learnings: LearningsSummary{
			Repo:     []string{"Found the pattern"},
			Workflow: []string{"Use TDD"},
			Code: []CodeLearning{
				{Path: "main.go", Line: 1, Finding: "Good code"},
			},
		},
	}

	result := redactSummary(summary)

	if result.Intent != "Fix a bug" {
		t.Errorf("Intent should be unchanged, got %q", result.Intent)
	}
	if result.Outcome != "Bug fixed" {
		t.Errorf("Outcome should be unchanged, got %q", result.Outcome)
	}
	if result.Learnings.Code[0].Finding != "Good code" {
		t.Errorf("Finding should be unchanged, got %q", result.Learnings.Code[0].Finding)
	}
}

func TestRedactStringSlice_NilAndEmpty(t *testing.T) {
	t.Parallel()

	// nil input should return nil (not empty slice)
	if result := redactStringSlice(nil); result != nil {
		t.Errorf("redactStringSlice(nil) should return nil, got %v", result)
	}

	// empty slice should return empty slice (not nil)
	result := redactStringSlice([]string{})
	if result == nil {
		t.Error("redactStringSlice([]string{}) should return empty slice, not nil")
	}
	if len(result) != 0 {
		t.Errorf("redactStringSlice([]string{}) should return empty slice, got len %d", len(result))
	}
}

func TestRedactCodeLearnings_NilAndEmpty(t *testing.T) {
	t.Parallel()

	// nil input should return nil
	if result := redactCodeLearnings(nil); result != nil {
		t.Errorf("redactCodeLearnings(nil) should return nil, got %v", result)
	}

	// empty slice should return empty slice
	result := redactCodeLearnings([]CodeLearning{})
	if result == nil {
		t.Error("redactCodeLearnings([]CodeLearning{}) should return empty slice, not nil")
	}
	if len(result) != 0 {
		t.Errorf("expected len 0, got %d", len(result))
	}
}

func TestWriteCommitted_RedactsSummarySecrets(t *testing.T) {
	t.Parallel()
	repo, _ := setupBranchTestRepo(t)
	store := NewGitStore(repo)
	checkpointID := id.MustCheckpointID("aabbccddeef7")

	err := store.WriteCommitted(context.Background(), WriteCommittedOptions{
		CheckpointID:     checkpointID,
		SessionID:        "redact-summary-session",
		Strategy:         "manual-commit",
		Transcript:       redact.AlreadyRedacted([]byte(`{"msg":"safe"}` + "\n")),
		CheckpointsCount: 1,
		AuthorName:       "Test Author",
		AuthorEmail:      "test@example.com",
		Summary: &Summary{
			Intent:  "Used key " + highEntropySecret + " to auth",
			Outcome: "Authenticated with " + highEntropySecret,
		},
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
	if strings.Contains(content.Metadata.Summary.Intent, highEntropySecret) {
		t.Error("Summary.Intent should not contain the secret after redaction")
	}
	if !strings.Contains(content.Metadata.Summary.Intent, "REDACTED") {
		t.Error("Summary.Intent should contain REDACTED placeholder")
	}
	if strings.Contains(content.Metadata.Summary.Outcome, highEntropySecret) {
		t.Error("Summary.Outcome should not contain the secret after redaction")
	}
}
