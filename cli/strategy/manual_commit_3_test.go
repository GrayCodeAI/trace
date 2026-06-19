package strategy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

func TestShadowStrategy_PostRewrite_MigratesExistingShadowBranch(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, "tracked.txt", "one\n")
	testutil.GitAdd(t, dir, "tracked.txt")
	testutil.GitCommit(t, dir, "initial")
	t.Chdir(dir)

	repo, err := OpenRepository(context.Background())
	if err != nil {
		t.Fatalf("OpenRepository() error = %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("Head() error = %v", err)
	}
	oldBaseCommit := head.Hash().String()

	testutil.WriteFile(t, dir, "tracked.txt", "two\n")
	testutil.GitAdd(t, dir, "tracked.txt")
	testutil.GitCommit(t, dir, "second")
	head, err = repo.Head()
	if err != nil {
		t.Fatalf("Head() after second commit error = %v", err)
	}
	newBaseCommit := head.Hash().String()

	worktreePath, err := paths.WorktreeRoot(context.Background())
	if err != nil {
		t.Fatalf("WorktreeRoot() error = %v", err)
	}
	worktreeID, err := paths.GetWorktreeID(worktreePath)
	if err != nil {
		t.Fatalf("GetWorktreeID() error = %v", err)
	}

	oldShadowBranch := checkpoint.ShadowBranchNameForCommit(oldBaseCommit, worktreeID)
	newShadowBranch := checkpoint.ShadowBranchNameForCommit(newBaseCommit, worktreeID)
	oldShadowRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(oldShadowBranch), plumbing.NewHash(oldBaseCommit))
	if err := repo.Storer.SetReference(oldShadowRef); err != nil {
		t.Fatalf("SetReference(old shadow) error = %v", err)
	}

	s := &ManualCommitStrategy{}
	state := &SessionState{
		SessionID:             "session-1",
		BaseCommit:            oldBaseCommit,
		AttributionBaseCommit: oldBaseCommit,
		WorktreePath:          worktreePath,
		WorktreeID:            worktreeID,
		StartedAt:             time.Now(),
		LastCheckpointID:      testTrailerCheckpointID,
	}
	if err := s.saveSessionState(context.Background(), state); err != nil {
		t.Fatalf("saveSessionState() error = %v", err)
	}

	if err := s.PostRewrite(context.Background(), "amend", strings.NewReader(oldBaseCommit+" "+newBaseCommit+" extra\n")); err != nil {
		t.Fatalf("PostRewrite() error = %v", err)
	}

	loaded, err := s.loadSessionState(context.Background(), state.SessionID)
	if err != nil {
		t.Fatalf("loadSessionState() error = %v", err)
	}
	if loaded.BaseCommit != newBaseCommit {
		t.Fatalf("BaseCommit = %q, want %q", loaded.BaseCommit, newBaseCommit)
	}
	if loaded.AttributionBaseCommit != oldBaseCommit {
		t.Fatalf("AttributionBaseCommit = %q, want original %q when shadow branch migrates", loaded.AttributionBaseCommit, oldBaseCommit)
	}
	if !referenceExists(t, repo, plumbing.NewBranchReferenceName(newShadowBranch)) {
		t.Fatalf("expected migrated shadow branch %q to exist", newShadowBranch)
	}
	if referenceExists(t, repo, plumbing.NewBranchReferenceName(oldShadowBranch)) {
		t.Fatalf("expected old shadow branch %q to be removed", oldShadowBranch)
	}
}

func TestShadowStrategy_MigrateAndPersistIfNeeded_PersistsBaseCommitWithoutShadowBranch(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, "tracked.txt", "one\n")
	testutil.GitAdd(t, dir, "tracked.txt")
	testutil.GitCommit(t, dir, "initial")
	t.Chdir(dir)

	repo, err := OpenRepository(context.Background())
	if err != nil {
		t.Fatalf("OpenRepository() error = %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("Head() error = %v", err)
	}
	oldBaseCommit := head.Hash().String()

	testutil.WriteFile(t, dir, "tracked.txt", "two\n")
	testutil.GitAdd(t, dir, "tracked.txt")
	testutil.GitCommit(t, dir, "second")
	head, err = repo.Head()
	if err != nil {
		t.Fatalf("Head() after second commit error = %v", err)
	}
	newBaseCommit := head.Hash().String()

	worktreePath, err := paths.WorktreeRoot(context.Background())
	if err != nil {
		t.Fatalf("WorktreeRoot() error = %v", err)
	}

	s := &ManualCommitStrategy{}
	state := &SessionState{
		SessionID:             "session-1",
		BaseCommit:            oldBaseCommit,
		AttributionBaseCommit: oldBaseCommit,
		WorktreePath:          worktreePath,
		StartedAt:             time.Now(),
		LastCheckpointID:      testTrailerCheckpointID,
	}
	if err := s.saveSessionState(context.Background(), state); err != nil {
		t.Fatalf("saveSessionState() error = %v", err)
	}

	if err := s.migrateAndPersistIfNeeded(context.Background(), repo, state); err != nil {
		t.Fatalf("migrateAndPersistIfNeeded() error = %v", err)
	}

	loaded, err := s.loadSessionState(context.Background(), state.SessionID)
	if err != nil {
		t.Fatalf("loadSessionState() error = %v", err)
	}
	if loaded.BaseCommit != newBaseCommit {
		t.Fatalf("BaseCommit = %q, want %q", loaded.BaseCommit, newBaseCommit)
	}
}

func TestShadowStrategy_PostRewrite_DoesNotTouchOtherWorktrees(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)
	oldSHA := strings.Repeat("a", 40)
	newSHA := strings.Repeat("b", 40)

	s := &ManualCommitStrategy{}
	other := &SessionState{
		SessionID:             "other-worktree",
		BaseCommit:            oldSHA,
		AttributionBaseCommit: oldSHA,
		WorktreePath:          filepath.Join(dir, "other"),
		StartedAt:             time.Now(),
		LastCheckpointID:      testTrailerCheckpointID,
	}
	if err := s.saveSessionState(context.Background(), other); err != nil {
		t.Fatalf("saveSessionState() error = %v", err)
	}

	if err := s.PostRewrite(context.Background(), "amend", strings.NewReader(oldSHA+" "+newSHA+"\n")); err != nil {
		t.Fatalf("PostRewrite() error = %v", err)
	}

	loaded, err := s.loadSessionState(context.Background(), other.SessionID)
	if err != nil {
		t.Fatalf("loadSessionState() error = %v", err)
	}
	if loaded.BaseCommit != oldSHA {
		t.Fatalf("BaseCommit = %q, want %q", loaded.BaseCommit, oldSHA)
	}
	if loaded.AttributionBaseCommit != oldSHA {
		t.Fatalf("AttributionBaseCommit = %q, want %q", loaded.AttributionBaseCommit, oldSHA)
	}
	if loaded.LastCheckpointID != testTrailerCheckpointID {
		t.Fatalf("LastCheckpointID = %q, want %q", loaded.LastCheckpointID, testTrailerCheckpointID)
	}
}

func referenceExists(t *testing.T, repo *git.Repository, refName plumbing.ReferenceName) bool {
	t.Helper()

	_, err := repo.Reference(refName, true)
	return err == nil
}

// TestShadowStrategy_CondenseSession_EphemeralBranchTrailer verifies that checkpoint commits
// on the trace/checkpoints/v1 branch include the Ephemeral-branch trailer indicating which shadow
// branch the checkpoint originated from.
func TestShadowStrategy_CondenseSession_EphemeralBranchTrailer(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init repo: %v", err)
	}

	// Create initial commit with a file
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	initialFile := filepath.Join(dir, "initial.txt")
	if err := os.WriteFile(initialFile, []byte("initial content"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if _, err := worktree.Add("initial.txt"); err != nil {
		t.Fatalf("failed to stage file: %v", err)
	}

	_, err = worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Chdir(dir)

	s := &ManualCommitStrategy{}
	sessionID := "2025-01-15-test-session-ephemeral"

	// Create metadata directory with transcript
	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	if err := os.MkdirAll(metadataDirAbs, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(metadataDirAbs, paths.TranscriptFileName), []byte(testTranscriptPromptResponse), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Use SaveStep to create a checkpoint (this creates the shadow branch)
	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{},
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

	// Load session state
	state, err := s.loadSessionState(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("loadSessionState() error = %v", err)
	}

	// Condense the session
	checkpointID := id.MustCheckpointID("a1b2c3d4e5f6")
	_, err = s.CondenseSession(context.Background(), repo, checkpointID, state, nil)
	if err != nil {
		t.Fatalf("CondenseSession() error = %v", err)
	}

	// Get the sessions branch commit and verify the Ephemeral-branch trailer
	sessionsRef, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err != nil {
		t.Fatalf("failed to get sessions branch reference: %v", err)
	}

	sessionsCommit, err := repo.CommitObject(sessionsRef.Hash())
	if err != nil {
		t.Fatalf("failed to get sessions commit: %v", err)
	}

	// Verify the commit message contains the Ephemeral-branch trailer
	shadowBranchName := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)
	expectedTrailer := "Ephemeral-branch: " + shadowBranchName
	if !strings.Contains(sessionsCommit.Message, expectedTrailer) {
		t.Errorf("sessions branch commit should contain %q trailer, got message:\n%s", expectedTrailer, sessionsCommit.Message)
	}
}

// TestSaveStep_EmptyBaseCommit_Recovery verifies that SaveStep recovers gracefully
// when a session state exists with empty BaseCommit (can happen from concurrent warning state).
func TestSaveStep_EmptyBaseCommit_Recovery(t *testing.T) {
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
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if _, err := worktree.Add("test.txt"); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}
	_, err = worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Chdir(dir)

	s := &ManualCommitStrategy{}
	sessionID := "2025-01-15-empty-basecommit-test"

	// Create a partial session state with empty BaseCommit
	// (simulates a partial session state with empty BaseCommit)
	partialState := &SessionState{
		SessionID:  sessionID,
		BaseCommit: "", // Empty! This is the bug scenario
		StartedAt:  time.Now(),
	}
	if err := s.saveSessionState(context.Background(), partialState); err != nil {
		t.Fatalf("failed to save partial state: %v", err)
	}

	// Create metadata directory
	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	if err := os.MkdirAll(metadataDirAbs, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}
	transcript := `{"type":"human","message":{"content":"test"}}` + "\n"
	if err := os.WriteFile(filepath.Join(metadataDirAbs, paths.TranscriptFileName), []byte(transcript), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// SaveStep should recover by re-initializing the session state
	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{},
		NewFiles:       []string{},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Test checkpoint",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
	})
	if err != nil {
		t.Fatalf("SaveStep() should recover from empty BaseCommit, got error: %v", err)
	}

	// Verify session state now has a valid BaseCommit
	loaded, err := s.loadSessionState(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("failed to load session state: %v", err)
	}
	if loaded.BaseCommit == "" {
		t.Error("BaseCommit should be populated after recovery")
	}
	if loaded.StepCount != 1 {
		t.Errorf("StepCount = %d, want 1", loaded.StepCount)
	}
}

// TestSaveStep_UsesCtxAgentType_WhenNoSessionState tests that SaveStep uses
// ctx.AgentType when no session state exists.
func TestSaveStep_UsesCtxAgentType_WhenNoSessionState(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if _, err := worktree.Add("test.txt"); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}
	if _, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	}); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Chdir(dir)

	s := &ManualCommitStrategy{}
	sessionID := "2026-02-06-agent-type-test"

	// NO session state exists (simulates InitializeSession failure)
	// SaveStep should use ctx.AgentType

	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	if err := os.MkdirAll(metadataDirAbs, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}
	transcript := `{"type":"human","message":{"content":"test"}}` + "\n"
	if err := os.WriteFile(filepath.Join(metadataDirAbs, paths.TranscriptFileName), []byte(transcript), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{},
		NewFiles:       []string{},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Test checkpoint",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
		AgentType:      agent.AgentTypeClaudeCode,
	})
	if err != nil {
		t.Fatalf("SaveStep() error = %v", err)
	}

	loaded, err := s.loadSessionState(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("failed to load session state: %v", err)
	}
	if loaded.AgentType != agent.AgentTypeClaudeCode {
		t.Errorf("AgentType = %q, want %q", loaded.AgentType, agent.AgentTypeClaudeCode)
	}
}

// TestSaveStep_UsesCtxAgentType_WhenPartialState tests that SaveStep uses
// ctx.AgentType when a partial session state exists (empty BaseCommit and AgentType).
func TestSaveStep_UsesCtxAgentType_WhenPartialState(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if _, err := worktree.Add("test.txt"); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}
	if _, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	}); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Chdir(dir)

	s := &ManualCommitStrategy{}
	sessionID := "2026-02-06-partial-state-agent-test"

	// Create partial session state with empty BaseCommit and no AgentType
	partialState := &SessionState{
		SessionID:  sessionID,
		BaseCommit: "",
		StartedAt:  time.Now(),
	}
	if err := s.saveSessionState(context.Background(), partialState); err != nil {
		t.Fatalf("failed to save partial state: %v", err)
	}

	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	if err := os.MkdirAll(metadataDirAbs, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}
	transcript := `{"type":"human","message":{"content":"test"}}` + "\n"
	if err := os.WriteFile(filepath.Join(metadataDirAbs, paths.TranscriptFileName), []byte(transcript), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{},
		NewFiles:       []string{},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Test checkpoint",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
		AgentType:      agent.AgentTypeClaudeCode,
	})
	if err != nil {
		t.Fatalf("SaveStep() error = %v", err)
	}

	loaded, err := s.loadSessionState(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("failed to load session state: %v", err)
	}
	if loaded.AgentType != agent.AgentTypeClaudeCode {
		t.Errorf("AgentType = %q, want %q", loaded.AgentType, agent.AgentTypeClaudeCode)
	}
}

// TestCountTranscriptItems tests counting lines/messages in different transcript formats.
func TestCountTranscriptItems(t *testing.T) {
	tests := []struct {
		name      string
		agentType types.AgentType
		content   string
		expected  int
	}{
		{
			name:      "Gemini JSON with messages",
			agentType: agent.AgentTypeGemini,
			content: `{
				"messages": [
					{"type": "user", "content": "Hello"},
					{"type": "gemini", "content": "Hi there!"}
				]
			}`,
			expected: 2,
		},
		{
			name:      "Gemini empty messages array",
			agentType: agent.AgentTypeGemini,
			content:   `{"messages": []}`,
			expected:  0,
		},
		{
			name:      "Claude Code JSONL",
			agentType: agent.AgentTypeClaudeCode,
			content: `{"type":"human","message":{"content":"Hello"}}
{"type":"assistant","message":{"content":"Hi"}}`,
			expected: 2,
		},
		{
			name:      "Claude Code JSONL with trailing newline",
			agentType: agent.AgentTypeClaudeCode,
			content: `{"type":"human","message":{"content":"Hello"}}
{"type":"assistant","message":{"content":"Hi"}}
`,
			expected: 2,
		},
		{
			name:      "empty string",
			agentType: agent.AgentTypeClaudeCode,
			content:   "",
			expected:  0,
		},
		{
			name:      "Gemini JSON with array content (real format)",
			agentType: agent.AgentTypeGemini,
			content: `{
				"messages": [
					{"type": "user", "content": [{"text": "Hello"}]},
					{"type": "gemini", "content": "Hi there!"},
					{"type": "user", "content": [{"text": "Do something"}]},
					{"type": "gemini", "content": "Done!"}
				]
			}`,
			expected: 4,
		},
		{
			name:      "OpenCode export JSON with messages",
			agentType: agent.AgentTypeOpenCode,
			content: `{
				"info": {"id": "session-1"},
				"messages": [
					{"info": {"role": "user"}, "parts": [{"type": "text", "text": "Hello"}]},
					{"info": {"role": "assistant"}, "parts": [{"type": "text", "text": "Hi there!"}]}
				]
			}`,
			expected: 2,
		},
		{
			name:      "OpenCode export JSON empty messages",
			agentType: agent.AgentTypeOpenCode,
			content:   `{"info": {"id": "session-1"}, "messages": []}`,
			expected:  0,
		},
		{
			name:      "OpenCode invalid JSON",
			agentType: agent.AgentTypeOpenCode,
			content:   `not valid json`,
			expected:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := countTranscriptItems(tt.agentType, tt.content)
			if result != tt.expected {
				t.Errorf("countTranscriptItems() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestExtractUserPrompts tests extraction of user prompts from different transcript formats.
func TestExtractUserPrompts(t *testing.T) {
	tests := []struct {
		name      string
		agentType types.AgentType
		content   string
		expected  []string
	}{
		{
			name:      "Gemini single user prompt",
			agentType: agent.AgentTypeGemini,
			content: `{
				"messages": [
					{"type": "user", "content": "Create a file called test.txt"}
				]
			}`,
			expected: []string{"Create a file called test.txt"},
		},
		{
			name:      "Gemini multiple user prompts",
			agentType: agent.AgentTypeGemini,
			content: `{
				"messages": [
					{"type": "user", "content": "First prompt"},
					{"type": "gemini", "content": "Response 1"},
					{"type": "user", "content": "Second prompt"},
					{"type": "gemini", "content": "Response 2"}
				]
			}`,
			expected: []string{"First prompt", "Second prompt"},
		},
		{
			name:      "Gemini no user messages",
			agentType: agent.AgentTypeGemini,
			content: `{
				"messages": [
					{"type": "gemini", "content": "Hello!"}
				]
			}`,
			expected: nil,
		},
		{
			name:      "Claude Code JSONL with user messages",
			agentType: agent.AgentTypeClaudeCode,
			content: `{"type":"user","message":{"content":"Hello"}}
{"type":"assistant","message":{"content":"Hi"}}
{"type":"user","message":{"content":"Goodbye"}}`,
			expected: []string{"Hello", "Goodbye"},
		},
		{
			name:      "empty string",
			agentType: agent.AgentTypeClaudeCode,
			content:   "",
			expected:  nil,
		},
		{
			name:      "Gemini array content (real format)",
			agentType: agent.AgentTypeGemini,
			content: `{
				"messages": [
					{"type": "user", "content": [{"text": "Create a file"}]},
					{"type": "gemini", "content": "Done!"},
					{"type": "user", "content": [{"text": "Edit the file"}]},
					{"type": "gemini", "content": "Updated!"}
				]
			}`,
			expected: []string{"Create a file", "Edit the file"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractUserPrompts(tt.agentType, tt.content)
			if len(result) != len(tt.expected) {
				t.Errorf("extractUserPrompts() returned %d prompts, want %d", len(result), len(tt.expected))
				return
			}
			for i, prompt := range result {
				if prompt != tt.expected[i] {
					t.Errorf("prompt[%d] = %q, want %q", i, prompt, tt.expected[i])
				}
			}
		})
	}
}
