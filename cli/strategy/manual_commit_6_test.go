package strategy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// TestCondenseSession_GeminiMultiCheckpoint verifies that multi-checkpoint Gemini sessions
// correctly scope token usage to only the checkpoint portion (not the trace transcript).
// This is the core bug fix - ensuring CheckpointTranscriptStart is properly used.
//
//nolint:maintidx // Integration test with comprehensive verification steps
func TestCondenseSession_GeminiMultiCheckpoint(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create initial commit
	testFile := filepath.Join(dir, "code.go")
	if err := os.WriteFile(testFile, []byte("package main"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if _, err := worktree.Add("code.go"); err != nil {
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
	sessionID := "2026-02-09-multi-checkpoint"

	// Create metadata directory
	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	if err := os.MkdirAll(metadataDirAbs, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}

	transcriptPath := filepath.Join(metadataDirAbs, paths.TranscriptFileName)

	// CHECKPOINT 1: Initial work with 2 messages (1 gemini message with tokens)
	checkpoint1Transcript := `{
		"sessionId": "multi-test",
		"messages": [
			{
				"type": "user",
				"content": "Add a main function"
			},
			{
				"type": "gemini",
				"content": "I'll add a main function",
				"tokens": {
					"input": 100,
					"output": 50,
					"cached": 20
				}
			}
		]
	}`

	if err := os.WriteFile(transcriptPath, []byte(checkpoint1Transcript), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Write prompt.txt for checkpoint 1 (simulating what lifecycle does)
	if err := os.WriteFile(filepath.Join(metadataDirAbs, paths.PromptFileName), []byte("Add a main function"), 0o644); err != nil {
		t.Fatalf("failed to write prompt file: %v", err)
	}

	// Modify file for checkpoint 1
	if err := os.WriteFile(testFile, []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("failed to modify file: %v", err)
	}

	// Save checkpoint 1
	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{"code.go"},
		NewFiles:       []string{},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint 1",
		AuthorName:     "Gemini CLI",
		AuthorEmail:    "gemini@test.com",
		AgentType:      agent.AgentTypeGemini,
	})
	if err != nil {
		t.Fatalf("SaveStep() checkpoint 1 error = %v", err)
	}

	// Load and verify state after checkpoint 1
	state, err := s.loadSessionState(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("loadSessionState() error = %v", err)
	}
	if state.CheckpointTranscriptStart != 0 {
		t.Errorf("CheckpointTranscriptStart after checkpoint 1 = %d, want 0", state.CheckpointTranscriptStart)
	}

	// CHECKPOINT 2: Add more messages to transcript (simulating continued session)
	// This adds 2 more messages (indices 2 and 3), with new token counts
	checkpoint2Transcript := `{
		"sessionId": "multi-test",
		"messages": [
			{
				"type": "user",
				"content": "Add a main function"
			},
			{
				"type": "gemini",
				"content": "I'll add a main function",
				"tokens": {
					"input": 100,
					"output": 50,
					"cached": 20
				}
			},
			{
				"type": "user",
				"content": "Now add error handling"
			},
			{
				"type": "gemini",
				"content": "I'll add error handling",
				"tokens": {
					"input": 200,
					"output": 75,
					"cached": 30
				}
			}
		]
	}`

	if err := os.WriteFile(transcriptPath, []byte(checkpoint2Transcript), 0o644); err != nil {
		t.Fatalf("failed to update transcript: %v", err)
	}

	// Simulate condensation clearing prompt.txt (condenseAndUpdateState does this),
	// then lifecycle appending the new prompt at turn start.
	if err := os.WriteFile(filepath.Join(metadataDirAbs, paths.PromptFileName), []byte("Now add error handling"), 0o644); err != nil {
		t.Fatalf("failed to write prompt file: %v", err)
	}

	// Modify file for checkpoint 2
	if err := os.WriteFile(testFile, []byte("package main\n\nfunc main() {\n\tif err := run(); err != nil {\n\t\tpanic(err)\n\t}\n}\n"), 0o644); err != nil {
		t.Fatalf("failed to modify file: %v", err)
	}

	// Before checkpoint 2, manually update CheckpointTranscriptStart to simulate
	// what would happen after condensing checkpoint 1
	state.CheckpointTranscriptStart = 2 // Start from message index 2 (the second user prompt)
	state.StepCount = 1                 // Set to 1 (will be incremented to 2 by SaveStep)
	if err := s.saveSessionState(context.Background(), state); err != nil {
		t.Fatalf("failed to update session state: %v", err)
	}

	// Save checkpoint 2
	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{"code.go"},
		NewFiles:       []string{},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint 2",
		AuthorName:     "Gemini CLI",
		AuthorEmail:    "gemini@test.com",
		AgentType:      agent.AgentTypeGemini,
	})
	if err != nil {
		t.Fatalf("SaveStep() checkpoint 2 error = %v", err)
	}

	// Reload state to get updated values
	state, err = s.loadSessionState(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("loadSessionState() error = %v", err)
	}

	// Condense the session - this should calculate token usage ONLY from message index 2 onwards
	checkpointID := id.MustCheckpointID("ddeeff998877")
	result, err := s.CondenseSession(context.Background(), repo, checkpointID, state, nil)
	if err != nil {
		t.Fatalf("CondenseSession() error = %v", err)
	}

	// Verify result
	if result.CheckpointsCount != 2 {
		t.Errorf("CheckpointsCount = %d, want 2", result.CheckpointsCount)
	}
	if result.TotalTranscriptLines != 4 {
		t.Errorf("TotalTranscriptLines = %d, want 4 (4 messages in Gemini format)", result.TotalTranscriptLines)
	}

	// Read condensed metadata
	store := checkpoint.NewGitStore(repo)
	content, err := store.ReadLatestSessionContent(t.Context(), checkpointID)
	if err != nil {
		t.Fatalf("ReadLatestSessionContent() error = %v", err)
	}

	// CRITICAL VERIFICATION: Token usage should ONLY count from message index 2 onwards
	// This means ONLY the second gemini message (indices 2-3), NOT the first one (indices 0-1)
	if content.Metadata.TokenUsage == nil {
		t.Fatal("TokenUsage should not be nil")
	}

	// Expected: Only the second gemini message tokens (input=200, output=75, cached=30)
	// NOT the first gemini message tokens (input=100, output=50, cached=20)
	if content.Metadata.TokenUsage.InputTokens != 200 {
		t.Errorf("InputTokens = %d, want 200 (should only count from checkpoint start, not trace transcript)",
			content.Metadata.TokenUsage.InputTokens)
	}
	if content.Metadata.TokenUsage.OutputTokens != 75 {
		t.Errorf("OutputTokens = %d, want 75 (should only count from checkpoint start, not trace transcript)",
			content.Metadata.TokenUsage.OutputTokens)
	}
	if content.Metadata.TokenUsage.CacheReadTokens != 30 {
		t.Errorf("CacheReadTokens = %d, want 30 (should only count from checkpoint start, not trace transcript)",
			content.Metadata.TokenUsage.CacheReadTokens)
	}
	if content.Metadata.TokenUsage.APICallCount != 1 {
		t.Errorf("APICallCount = %d, want 1 (only one gemini message after checkpoint start)",
			content.Metadata.TokenUsage.APICallCount)
	}

	// Verify the full transcript is stored (all 4 messages)
	if len(content.Transcript) == 0 {
		t.Error("Full transcript should be stored")
	}

	// Verify only checkpoint-scoped prompts are present (from CheckpointTranscriptStart onwards)
	if strings.Contains(content.Prompts, "Add a main function") {
		t.Error("Prompts should NOT contain first prompt (before checkpoint start)")
	}
	if !strings.Contains(content.Prompts, "Now add error handling") {
		t.Error("Prompts should contain second prompt (checkpoint-scoped)")
	}
}

func TestCondenseSession_CopilotScopedCheckpointMetadataAndSessionBackfill(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	initialHash, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author:            &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
		AllowEmptyCommits: true,
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	t.Chdir(dir)

	sessionID := "2026-03-17-copilot-token-scope"
	transcriptDir := filepath.Join(dir, ".copilot", "session-state", sessionID)
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatalf("failed to create transcript dir: %v", err)
	}
	transcriptPath := filepath.Join(transcriptDir, "events.jsonl")

	transcript := strings.Join([]string{
		`{"type":"session.start","data":{"sessionId":"2026-03-17-copilot-token-scope"},"id":"1","timestamp":"2026-03-17T00:00:00Z","parentId":""}`,
		`{"type":"session.model_change","data":{"newModel":"claude-sonnet-4.6"},"id":"2","timestamp":"2026-03-17T00:00:01Z","parentId":"1"}`,
		`{"type":"user.message","data":{"content":"Create alpha.txt"},"id":"3","timestamp":"2026-03-17T00:00:02Z","parentId":""}`,
		`{"type":"assistant.message","data":{"content":"Created alpha.txt","outputTokens":10},"id":"4","timestamp":"2026-03-17T00:00:03Z","parentId":"3"}`,
		`{"type":"tool.execution_complete","data":{"toolCallId":"tool-1","model":"claude-sonnet-4.6","toolTelemetry":{"properties":{"filePaths":"[\"alpha.txt\"]"},"metrics":{"linesAdded":1,"linesRemoved":0}}},"id":"5","timestamp":"2026-03-17T00:00:04Z","parentId":"4"}`,
		`{"type":"user.message","data":{"content":"Create beta.txt"},"id":"6","timestamp":"2026-03-17T00:00:05Z","parentId":""}`,
		`{"type":"assistant.message","data":{"content":"Created beta.txt","outputTokens":25},"id":"7","timestamp":"2026-03-17T00:00:06Z","parentId":"6"}`,
		`{"type":"tool.execution_complete","data":{"toolCallId":"tool-2","model":"claude-sonnet-4.6","toolTelemetry":{"properties":{"filePaths":"[\"beta.txt\"]"},"metrics":{"linesAdded":1,"linesRemoved":0}}},"id":"8","timestamp":"2026-03-17T00:00:07Z","parentId":"7"}`,
		`{"type":"session.shutdown","data":{"modelMetrics":{"claude-sonnet-4.6":{"requests":{"count":2},"usage":{"inputTokens":0,"outputTokens":35,"cacheReadTokens":20,"cacheWriteTokens":10}}}},"id":"9","timestamp":"2026-03-17T00:00:08Z","parentId":""}`,
	}, "\n") + "\n"
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	state := &SessionState{
		SessionID:                 sessionID,
		BaseCommit:                initialHash.String(),
		StartedAt:                 time.Now(),
		FilesTouched:              []string{"beta.txt"},
		WorktreePath:              dir,
		TranscriptPath:            transcriptPath,
		AgentType:                 agent.AgentTypeCopilotCLI,
		ModelName:                 "claude-sonnet-4.6",
		CheckpointTranscriptStart: 5,
	}

	s := &ManualCommitStrategy{}
	checkpointID := id.MustCheckpointID("cc11aa22bb33")
	result, err := s.CondenseSession(context.Background(), repo, checkpointID, state, nil)
	if err != nil {
		t.Fatalf("CondenseSession() error = %v", err)
	}

	if result.CheckpointID != checkpointID {
		t.Errorf("CheckpointID = %v, want %v", result.CheckpointID, checkpointID)
	}
	if len(result.FilesTouched) != 1 || result.FilesTouched[0] != "beta.txt" {
		t.Errorf("FilesTouched = %v, want [beta.txt]", result.FilesTouched)
	}

	store := checkpoint.NewGitStore(repo)
	content, err := store.ReadLatestSessionContent(t.Context(), checkpointID)
	if err != nil {
		t.Fatalf("ReadLatestSessionContent() error = %v", err)
	}

	if content.Metadata.TokenUsage == nil {
		t.Fatal("TokenUsage should not be nil")
	}
	if content.Metadata.TokenUsage.InputTokens != 0 {
		t.Errorf("metadata InputTokens = %d, want 0 for scoped Copilot checkpoint usage", content.Metadata.TokenUsage.InputTokens)
	}
	if content.Metadata.TokenUsage.OutputTokens != 25 {
		t.Errorf("metadata OutputTokens = %d, want 25 for second checkpoint assistant output", content.Metadata.TokenUsage.OutputTokens)
	}
	if content.Metadata.TokenUsage.CacheReadTokens != 0 {
		t.Errorf("metadata CacheReadTokens = %d, want 0 for scoped fallback path", content.Metadata.TokenUsage.CacheReadTokens)
	}
	if content.Metadata.TokenUsage.CacheCreationTokens != 0 {
		t.Errorf("metadata CacheCreationTokens = %d, want 0 for scoped fallback path", content.Metadata.TokenUsage.CacheCreationTokens)
	}
	if content.Metadata.TokenUsage.APICallCount != 1 {
		t.Errorf("metadata APICallCount = %d, want 1", content.Metadata.TokenUsage.APICallCount)
	}

	if state.TokenUsage == nil {
		t.Fatal("state.TokenUsage should not be nil after Copilot session backfill")
	}
	if state.TokenUsage.InputTokens != 0 {
		t.Errorf("state InputTokens = %d, want 0 from session.shutdown", state.TokenUsage.InputTokens)
	}
	if state.TokenUsage.OutputTokens != 35 {
		t.Errorf("state OutputTokens = %d, want 35 from session.shutdown", state.TokenUsage.OutputTokens)
	}
	if state.TokenUsage.CacheReadTokens != 20 {
		t.Errorf("state CacheReadTokens = %d, want 20 from session.shutdown", state.TokenUsage.CacheReadTokens)
	}
	if state.TokenUsage.CacheCreationTokens != 10 {
		t.Errorf("state CacheCreationTokens = %d, want 10 from session.shutdown", state.TokenUsage.CacheCreationTokens)
	}
	if state.TokenUsage.APICallCount != 2 {
		t.Errorf("state APICallCount = %d, want 2 from session.shutdown", state.TokenUsage.APICallCount)
	}
}

// TestCondenseSession_FilesTouchedFallback_EmptyState verifies that when state.FilesTouched
// is empty (mid-session commit before SaveStep), the fallback to committedFiles works.
// This is the legitimate use case for the fallback.
func TestCondenseSession_FilesTouchedFallback_EmptyState(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create initial commit
	initialHash, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author:            &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
		AllowEmptyCommits: true,
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create a file and commit it (simulating agent mid-turn commit)
	agentFile := filepath.Join(dir, "agent.go")
	if err := os.WriteFile(agentFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if _, err := worktree.Add("agent.go"); err != nil {
		t.Fatalf("failed to stage file: %v", err)
	}
	if _, err = worktree.Commit("Add agent.go", &git.CommitOptions{
		Author: &object.Signature{Name: "Agent", Email: "agent@test.com", When: time.Now()},
	}); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Chdir(dir)

	// Create live transcript (required when no shadow branch)
	transcriptDir := filepath.Join(dir, ".claude", "projects", "test")
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatalf("failed to create transcript dir: %v", err)
	}
	transcriptFile := filepath.Join(transcriptDir, "session.jsonl")
	if err := os.WriteFile(transcriptFile, []byte(`{"type":"human","message":{"content":"create agent.go"}}
{"type":"assistant","message":{"content":"Done"}}
`), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Session state with EMPTY FilesTouched (mid-session commit scenario)
	state := &SessionState{
		SessionID:      "test-empty-files",
		BaseCommit:     initialHash.String(),
		FilesTouched:   []string{}, // Empty - no SaveStep called yet
		TranscriptPath: transcriptFile,
		AgentType:      "Claude Code",
	}

	s := &ManualCommitStrategy{}
	checkpointID := id.MustCheckpointID("fa11bac00001")

	// Condense with committedFiles - should fallback since FilesTouched is empty
	committedFiles := map[string]struct{}{"agent.go": {}}
	result, err := s.CondenseSession(context.Background(), repo, checkpointID, state, committedFiles)
	if err != nil {
		t.Fatalf("CondenseSession() error = %v", err)
	}

	// Read metadata and verify files_touched contains the committed file (fallback worked)
	sessionsRef, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err != nil {
		t.Fatalf("failed to get sessions branch: %v", err)
	}
	sessionsCommit, err := repo.CommitObject(sessionsRef.Hash())
	if err != nil {
		t.Fatalf("failed to get sessions commit: %v", err)
	}
	tree, err := sessionsCommit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	metadataPath := checkpointID.Path() + "/0/" + paths.MetadataFileName
	metadataFile, err := tree.File(metadataPath)
	if err != nil {
		t.Fatalf("failed to find metadata: %v", err)
	}
	content, err := metadataFile.Contents()
	if err != nil {
		t.Fatalf("failed to read metadata: %v", err)
	}

	var metadata struct {
		FilesTouched []string `json:"files_touched"`
	}
	if err := json.Unmarshal([]byte(content), &metadata); err != nil {
		t.Fatalf("failed to parse metadata: %v", err)
	}

	// Verify fallback worked - files_touched should contain agent.go
	if len(metadata.FilesTouched) != 1 || metadata.FilesTouched[0] != "agent.go" {
		t.Errorf("files_touched = %v, want [agent.go] (fallback should apply when FilesTouched is empty)",
			metadata.FilesTouched)
	}

	t.Logf("Fallback worked: files_touched = %v, result = %+v", metadata.FilesTouched, result)
}

// TestCondenseSession_FilesTouchedNoFallback_NoOverlap verifies that when state.FilesTouched
// has files but none overlap with committedFiles, we do NOT fallback to committedFiles.
// This prevents the bug where unrelated sessions get incorrect files_touched.
func TestCondenseSession_FilesTouchedNoFallback_NoOverlap(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create initial commit
	initialHash, err := worktree.Commit("Initial commit", &git.CommitOptions{
		Author:            &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
		AllowEmptyCommits: true,
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create files for both the session's work and the committed file
	sessionFile := filepath.Join(dir, "session_file.go")
	if err := os.WriteFile(sessionFile, []byte("package session\n"), 0o644); err != nil {
		t.Fatalf("failed to write session file: %v", err)
	}
	committedFile := filepath.Join(dir, "other_file.go")
	if err := os.WriteFile(committedFile, []byte("package other\n"), 0o644); err != nil {
		t.Fatalf("failed to write committed file: %v", err)
	}

	// Only commit the "other" file (not the session's file)
	if _, err := worktree.Add("other_file.go"); err != nil {
		t.Fatalf("failed to stage file: %v", err)
	}
	if _, err = worktree.Commit("Add other_file.go", &git.CommitOptions{
		Author: &object.Signature{Name: "Human", Email: "human@test.com", When: time.Now()},
	}); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Chdir(dir)

	// Create live transcript
	transcriptDir := filepath.Join(dir, ".claude", "projects", "test")
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatalf("failed to create transcript dir: %v", err)
	}
	transcriptFile := filepath.Join(transcriptDir, "session.jsonl")
	if err := os.WriteFile(transcriptFile, []byte(`{"type":"human","message":{"content":"work on session_file.go"}}
{"type":"assistant","message":{"content":"Done"}}
`), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Session state with FilesTouched that does NOT overlap with committedFiles
	state := &SessionState{
		SessionID:      "test-no-overlap",
		BaseCommit:     initialHash.String(),
		FilesTouched:   []string{"session_file.go"}, // Does NOT overlap with other_file.go
		TranscriptPath: transcriptFile,
		AgentType:      "Claude Code",
	}

	s := &ManualCommitStrategy{}
	checkpointID := id.MustCheckpointID("00001a000001")

	// Condense with committedFiles that don't overlap
	committedFiles := map[string]struct{}{"other_file.go": {}}
	result, err := s.CondenseSession(context.Background(), repo, checkpointID, state, committedFiles)
	if err != nil {
		t.Fatalf("CondenseSession() error = %v", err)
	}

	// Read metadata and verify files_touched is EMPTY (no fallback applied)
	sessionsRef, err := repo.Reference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), true)
	if err != nil {
		t.Fatalf("failed to get sessions branch: %v", err)
	}
	sessionsCommit, err := repo.CommitObject(sessionsRef.Hash())
	if err != nil {
		t.Fatalf("failed to get sessions commit: %v", err)
	}
	tree, err := sessionsCommit.Tree()
	if err != nil {
		t.Fatalf("failed to get tree: %v", err)
	}

	metadataPath := checkpointID.Path() + "/0/" + paths.MetadataFileName
	metadataFile, err := tree.File(metadataPath)
	if err != nil {
		t.Fatalf("failed to find metadata: %v", err)
	}
	content, err := metadataFile.Contents()
	if err != nil {
		t.Fatalf("failed to read metadata: %v", err)
	}

	var metadata struct {
		FilesTouched []string `json:"files_touched"`
	}
	if err := json.Unmarshal([]byte(content), &metadata); err != nil {
		t.Fatalf("failed to parse metadata: %v", err)
	}

	// Verify NO fallback - files_touched should be EMPTY, NOT contain other_file.go
	// This is the key fix: session had files (session_file.go) but none overlapped,
	// so we should NOT fallback to committedFiles (other_file.go)
	if len(metadata.FilesTouched) != 0 {
		t.Errorf("files_touched = %v, want [] (should NOT fallback when session had files but no overlap)",
			metadata.FilesTouched)
	}

	t.Logf("No fallback applied: files_touched = %v (correctly empty), result = %+v", metadata.FilesTouched, result)
}

// TestExtractFilesFromLiveTranscript_RespectsOffset verifies that after condensation
// sets CheckpointTranscriptStart = N, resolveFilesTouched only returns
// files from messages at index N and beyond, not from the beginning.
//
// This is a regression test for a bug where compaction events (pre-compress hooks)
// unconditionally reset CheckpointTranscriptStart to 0, causing already-condensed
// files to re-appear in carry-forward and break sequential commit scenarios.
func TestExtractFilesFromLiveTranscript_RespectsOffset(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	s := &ManualCommitStrategy{}

	// Create a Gemini-format transcript with 3 file writes at different message indices:
	//   msg 0: user prompt
	//   msg 1: gemini writes red.md      (already condensed)
	//   msg 2: user prompt
	//   msg 3: gemini writes blue.md     (already condensed)
	//   msg 4: user prompt
	//   msg 5: gemini writes green.md    (new, should be extracted)
	transcript := `{
  "messages": [
    {"type": "user", "content": [{"text": "create red.md"}]},
    {"type": "gemini", "content": "", "toolCalls": [{"name": "write_file", "args": {"file_path": "docs/red.md"}}]},
    {"type": "user", "content": [{"text": "create blue.md"}]},
    {"type": "gemini", "content": "", "toolCalls": [{"name": "write_file", "args": {"file_path": "docs/blue.md"}}]},
    {"type": "user", "content": [{"text": "create green.md"}]},
    {"type": "gemini", "content": "", "toolCalls": [{"name": "write_file", "args": {"file_path": "docs/green.md"}}]}
  ]
}`

	transcriptPath := filepath.Join(dir, "transcript.json")
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Simulate state after 2 condensations: offset points past blue.md's message
	state := &SessionState{
		SessionID:                 "test-offset-session",
		TranscriptPath:            transcriptPath,
		AgentType:                 agent.AgentTypeGemini,
		WorktreePath:              dir,
		CheckpointTranscriptStart: 4, // Past red.md (msg 1) and blue.md (msg 3)
	}

	// With correct offset (4): should only find green.md
	files := s.resolveFilesTouched(context.Background(), state)
	if len(files) != 1 || files[0] != "docs/green.md" {
		t.Errorf("resolveFilesTouched(offset=4) = %v, want [docs/green.md]", files)
	}

	// With reset offset (0): would incorrectly find all 3 files (the bug)
	state.CheckpointTranscriptStart = 0
	allFiles := s.resolveFilesTouched(context.Background(), state)
	if len(allFiles) != 3 {
		t.Errorf("resolveFilesTouched(offset=0) got %d files, want 3: %v", len(allFiles), allFiles)
	}
}

// TestResolveFilesTouched_PrefersStateFallsBackToTranscript verifies the two-tier
// resolution in resolveFilesTouched: state.FilesTouched is preferred (returns a copy),
// and transcript extraction is only used as a fallback when FilesTouched is empty.
func TestResolveFilesTouched_PrefersStateFallsBackToTranscript(t *testing.T) {
	dir := setupGitRepo(t)
	t.Chdir(dir)

	s := &ManualCommitStrategy{}

	// Gemini transcript containing a file write
	transcript := `{
  "messages": [
    {"type": "user", "content": [{"text": "create file"}]},
    {"type": "gemini", "content": "", "toolCalls": [{"name": "write_file", "args": {"file_path": "from-transcript.txt"}}]}
  ]
}`
	transcriptPath := filepath.Join(dir, "transcript.json")
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	t.Run("prefers FilesTouched over transcript", func(t *testing.T) {
		state := &SessionState{
			SessionID:      "test-prefers-state",
			TranscriptPath: transcriptPath,
			AgentType:      agent.AgentTypeGemini,
			WorktreePath:   dir,
			FilesTouched:   []string{"from-hook.txt"},
		}
		files := s.resolveFilesTouched(context.Background(), state)
		if len(files) != 1 || files[0] != "from-hook.txt" {
			t.Errorf("resolveFilesTouched with FilesTouched = %v, want [from-hook.txt]", files)
		}
	})

	t.Run("returns copy of FilesTouched", func(t *testing.T) {
		state := &SessionState{
			SessionID:    "test-copy",
			FilesTouched: []string{"a.txt", "b.txt"},
		}
		files := s.resolveFilesTouched(context.Background(), state)
		// Mutating returned slice should not affect state
		files[0] = "mutated.txt"
		if state.FilesTouched[0] != "a.txt" {
			t.Errorf("resolveFilesTouched did not return a copy; state.FilesTouched[0] = %q", state.FilesTouched[0])
		}
	})

	t.Run("falls back to transcript when FilesTouched is empty", func(t *testing.T) {
		state := &SessionState{
			SessionID:      "test-fallback",
			TranscriptPath: transcriptPath,
			AgentType:      agent.AgentTypeGemini,
			WorktreePath:   dir,
			FilesTouched:   nil,
		}
		files := s.resolveFilesTouched(context.Background(), state)
		if len(files) != 1 || files[0] != "from-transcript.txt" {
			t.Errorf("resolveFilesTouched with empty FilesTouched = %v, want [from-transcript.txt]", files)
		}
	})

	t.Run("returns nil when both sources are empty", func(t *testing.T) {
		state := &SessionState{
			SessionID:    "test-empty",
			FilesTouched: nil,
			// No transcript path — extraction will return nil
		}
		files := s.resolveFilesTouched(context.Background(), state)
		if files != nil {
			t.Errorf("resolveFilesTouched with no sources = %v, want nil", files)
		}
	})
}
