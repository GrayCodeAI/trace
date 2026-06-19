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

// TestMultiCheckpoint_UserEditsBetweenCheckpoints tests that user edits made between
// agent checkpoints are correctly attributed to the user, not the agent.
//
// This tests two scenarios:
// 1. User edits a DIFFERENT file than agent - detected at checkpoint save time
// 2. User edits the SAME file as agent - detected at commit time (shadow → head diff)
//
//nolint:maintidx // Integration test with multiple steps is inherently complex
func TestMultiCheckpoint_UserEditsBetweenCheckpoints(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	// Create initial commit with two files
	agentFile := filepath.Join(dir, "agent.go")
	userFile := filepath.Join(dir, "user.go")
	if err := os.WriteFile(agentFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("failed to write agent file: %v", err)
	}
	if err := os.WriteFile(userFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("failed to write user file: %v", err)
	}
	if _, err := worktree.Add("agent.go"); err != nil {
		t.Fatalf("failed to stage file: %v", err)
	}
	if _, err := worktree.Add("user.go"); err != nil {
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
	sessionID := "2025-01-15-multi-checkpoint-test"

	// Create metadata directory
	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	if err := os.MkdirAll(metadataDirAbs, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}

	transcript := `{"type":"human","message":{"content":"add function"}}
{"type":"assistant","message":{"content":"adding function"}}
`
	if err := os.WriteFile(filepath.Join(metadataDirAbs, paths.TranscriptFileName), []byte(transcript), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// === PROMPT 1 START: Initialize session (simulates UserPromptSubmit) ===
	// This must happen BEFORE agent makes any changes
	if err := s.InitializeSession(context.Background(), sessionID, "Claude Code", "", "", ""); err != nil {
		t.Fatalf("InitializeSession() prompt 1 error = %v", err)
	}

	// === CHECKPOINT 1: Agent modifies agent.go (adds 4 lines) ===
	checkpoint1Content := "package main\n\nfunc agentFunc1() {\n\tprintln(\"agent1\")\n}\n"
	if err := os.WriteFile(agentFile, []byte(checkpoint1Content), 0o644); err != nil {
		t.Fatalf("failed to write agent changes 1: %v", err)
	}

	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{"agent.go"},
		NewFiles:       []string{},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint 1",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
	})
	if err != nil {
		t.Fatalf("SaveStep() checkpoint 1 error = %v", err)
	}

	// Verify PromptAttribution was recorded for checkpoint 1
	state1, err := s.loadSessionState(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("loadSessionState() after checkpoint 1 error = %v", err)
	}
	if len(state1.PromptAttributions) != 1 {
		t.Fatalf("expected 1 PromptAttribution after checkpoint 1, got %d", len(state1.PromptAttributions))
	}
	// First checkpoint: no user edits yet (user.go hasn't changed)
	if state1.PromptAttributions[0].UserLinesAdded != 0 {
		t.Errorf("checkpoint 1: expected 0 user lines added, got %d", state1.PromptAttributions[0].UserLinesAdded)
	}

	// === USER EDITS A DIFFERENT FILE (user.go) BETWEEN CHECKPOINTS ===
	userEditContent := "package main\n\n// User added this function\nfunc userFunc() {\n\tprintln(\"user\")\n}\n"
	if err := os.WriteFile(userFile, []byte(userEditContent), 0o644); err != nil {
		t.Fatalf("failed to write user edits: %v", err)
	}

	// === PROMPT 2 START: Initialize session again (simulates UserPromptSubmit) ===
	// This captures the user's edits to user.go BEFORE the agent runs
	if err := s.InitializeSession(context.Background(), sessionID, "Claude Code", "", "", ""); err != nil {
		t.Fatalf("InitializeSession() prompt 2 error = %v", err)
	}

	// === CHECKPOINT 2: Agent modifies agent.go again (adds 4 more lines) ===
	checkpoint2Content := "package main\n\nfunc agentFunc1() {\n\tprintln(\"agent1\")\n}\n\nfunc agentFunc2() {\n\tprintln(\"agent2\")\n}\n"
	if err := os.WriteFile(agentFile, []byte(checkpoint2Content), 0o644); err != nil {
		t.Fatalf("failed to write agent changes 2: %v", err)
	}

	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{"agent.go"},
		NewFiles:       []string{},
		DeletedFiles:   []string{},
		MetadataDir:    metadataDir,
		MetadataDirAbs: metadataDirAbs,
		CommitMessage:  "Checkpoint 2",
		AuthorName:     "Test",
		AuthorEmail:    "test@test.com",
	})
	if err != nil {
		t.Fatalf("SaveStep() checkpoint 2 error = %v", err)
	}

	// Verify PromptAttribution was recorded for checkpoint 2
	state2, err := s.loadSessionState(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("loadSessionState() after checkpoint 2 error = %v", err)
	}
	if len(state2.PromptAttributions) != 2 {
		t.Fatalf("expected 2 PromptAttributions after checkpoint 2, got %d", len(state2.PromptAttributions))
	}

	t.Logf("Checkpoint 2 PromptAttribution: user_added=%d, user_removed=%d, agent_added=%d, agent_removed=%d",
		state2.PromptAttributions[1].UserLinesAdded,
		state2.PromptAttributions[1].UserLinesRemoved,
		state2.PromptAttributions[1].AgentLinesAdded,
		state2.PromptAttributions[1].AgentLinesRemoved)

	// Second checkpoint should detect user's edits to user.go (different file than agent)
	// User added 5 lines to user.go
	if state2.PromptAttributions[1].UserLinesAdded == 0 {
		t.Error("checkpoint 2: expected user lines added > 0 because user edited user.go")
	}

	// === USER COMMITS ===
	if _, err := worktree.Add("agent.go"); err != nil {
		t.Fatalf("failed to stage agent.go: %v", err)
	}
	if _, err := worktree.Add("user.go"); err != nil {
		t.Fatalf("failed to stage user.go: %v", err)
	}
	_, err = worktree.Commit("Final commit with agent and user changes", &git.CommitOptions{
		Author: &object.Signature{Name: "Human", Email: "human@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// === CONDENSE AND VERIFY ATTRIBUTION ===
	checkpointID := id.MustCheckpointID("b2c3d4e5f6a7")
	result, err := s.CondenseSession(context.Background(), repo, checkpointID, state2, nil)
	if err != nil {
		t.Fatalf("CondenseSession() error = %v", err)
	}

	if result.CheckpointID != checkpointID {
		t.Errorf("CheckpointID = %q, want %q", result.CheckpointID, checkpointID)
	}

	// Read metadata and verify attribution
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

	// InitialAttribution is stored in session-level metadata (0/metadata.json), not root (0-based indexing)
	sessionMetadataPath := checkpointID.Path() + "/0/" + paths.MetadataFileName
	metadataFile, err := tree.File(sessionMetadataPath)
	if err != nil {
		t.Fatalf("failed to find session metadata.json at %s: %v", sessionMetadataPath, err)
	}

	content, err := metadataFile.Contents()
	if err != nil {
		t.Fatalf("failed to read metadata.json: %v", err)
	}

	var metadata struct {
		InitialAttribution *struct {
			AgentLines      int     `json:"agent_lines"`
			HumanAdded      int     `json:"human_added"`
			HumanModified   int     `json:"human_modified"`
			HumanRemoved    int     `json:"human_removed"`
			TotalCommitted  int     `json:"total_committed"`
			AgentPercentage float64 `json:"agent_percentage"`
		} `json:"initial_attribution"`
	}
	if err := json.Unmarshal([]byte(content), &metadata); err != nil {
		t.Fatalf("failed to parse metadata.json: %v", err)
	}

	if metadata.InitialAttribution == nil {
		t.Fatal("InitialAttribution should be present in session metadata")
	}

	t.Logf("Final Attribution: agent=%d, human_added=%d, human_modified=%d, human_removed=%d, total=%d, percentage=%.1f%%",
		metadata.InitialAttribution.AgentLines,
		metadata.InitialAttribution.HumanAdded,
		metadata.InitialAttribution.HumanModified,
		metadata.InitialAttribution.HumanRemoved,
		metadata.InitialAttribution.TotalCommitted,
		metadata.InitialAttribution.AgentPercentage)

	// Verify the attribution makes sense:
	// - Agent modified agent.go: added ~8 lines total
	// - User modified user.go: added ~5 lines
	// - So agent percentage should be around 50-70%
	if metadata.InitialAttribution.AgentLines == 0 {
		t.Error("AgentLines should be > 0")
	}
	if metadata.InitialAttribution.TotalCommitted == 0 {
		t.Error("TotalCommitted should be > 0")
	}

	// The key test: user's lines should be captured in HumanAdded
	if metadata.InitialAttribution.HumanAdded == 0 {
		t.Error("HumanAdded should be > 0 because user added lines to user.go")
	}

	// Agent percentage should not be 100% since user contributed
	if metadata.InitialAttribution.AgentPercentage >= 100 {
		t.Errorf("AgentPercentage should be < 100%% since user contributed, got %.1f%%",
			metadata.InitialAttribution.AgentPercentage)
	}
}

// TestCondenseSession_PrefersLiveTranscript verifies that CondenseSession reads the
// live transcript file when available, rather than the potentially stale shadow branch copy.
// This reproduces the bug where SaveStep was skipped (no code changes) but the
// transcript continued growing — deferred condensation would read stale data.
func TestCondenseSession_PrefersLiveTranscript(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init repo: %v", err)
	}

	// Create initial commit
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if _, err := wt.Add("file.txt"); err != nil {
		t.Fatalf("failed to stage: %v", err)
	}
	_, err = wt.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Chdir(dir)

	s := &ManualCommitStrategy{}
	sessionID := "2025-01-15-test-live-transcript"

	// Create metadata dir with an initial (short) transcript
	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	if err := os.MkdirAll(metadataDirAbs, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}

	staleTranscript := `{"type":"human","message":{"content":"first prompt"}}
{"type":"assistant","message":{"content":"first response"}}
`
	if err := os.WriteFile(filepath.Join(metadataDirAbs, paths.TranscriptFileName), []byte(staleTranscript), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// SaveStep to create shadow branch with the stale transcript
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

	// Now simulate the conversation continuing: write a LONGER live transcript file.
	// In the real bug, SaveStep would be skipped because totalChanges == 0,
	// so the shadow branch still has the stale version.
	liveTranscriptFile := filepath.Join(dir, "live-transcript.jsonl")
	liveTranscript := `{"type":"human","message":{"content":"first prompt"}}
{"type":"assistant","message":{"content":"first response"}}
{"type":"human","message":{"content":"second prompt"}}
{"type":"assistant","message":{"content":"second response"}}
`
	if err := os.WriteFile(liveTranscriptFile, []byte(liveTranscript), 0o644); err != nil {
		t.Fatalf("failed to write live transcript: %v", err)
	}

	// Load session state and set TranscriptPath to the live file
	state, err := s.loadSessionState(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("loadSessionState() error = %v", err)
	}
	state.TranscriptPath = liveTranscriptFile
	if err := s.saveSessionState(context.Background(), state); err != nil {
		t.Fatalf("saveSessionState() error = %v", err)
	}

	// Condense — this should read the live transcript, not the shadow branch copy
	checkpointID := id.MustCheckpointID("b2c3d4e5f6a1")
	result, err := s.CondenseSession(context.Background(), repo, checkpointID, state, nil)
	if err != nil {
		t.Fatalf("CondenseSession() error = %v", err)
	}

	// The live transcript has 4 lines; the shadow branch copy has 2.
	// If we read the stale shadow copy, we'd only see 2 lines.
	if result.TotalTranscriptLines != 4 {
		t.Errorf("TotalTranscriptLines = %d, want 4 (live transcript has 4 lines, shadow has 2)", result.TotalTranscriptLines)
	}

	// Verify the condensed content includes the second prompt
	store := checkpoint.NewGitStore(repo)
	content, err := store.ReadLatestSessionContent(t.Context(), checkpointID)
	if err != nil {
		t.Fatalf("ReadLatestSessionContent() error = %v", err)
	}
	if !strings.Contains(string(content.Transcript), "second prompt") {
		t.Error("condensed transcript should contain 'second prompt' from live file, but it doesn't")
	}
}

// TestCondenseSession_TranscriptRelocatedMidSession verifies that CondenseSession
// succeeds when the agent relocates its transcript mid-session (e.g., Cursor CLI
// switching from flat <dir>/<id>.jsonl to nested <dir>/<id>/<id>.jsonl layout).
// This is a regression test for a Cursor CLI 2026.03.11 change that broke mid-turn
// commits because the stored TranscriptPath became stale.
func TestCondenseSession_TranscriptRelocatedMidSession(t *testing.T) {
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init repo: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if _, err := wt.Add("file.txt"); err != nil {
		t.Fatalf("failed to stage: %v", err)
	}
	_, err = wt.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	t.Chdir(dir)

	s := &ManualCommitStrategy{}
	sessionID := "87874108-eff2-47a0-b260-183961dd6cb0"

	// Create the session state with a flat TranscriptPath (what before-submit-prompt reports)
	agentTranscriptsDir := filepath.Join(dir, "agent-transcripts")
	if err := os.MkdirAll(agentTranscriptsDir, 0o755); err != nil {
		t.Fatalf("failed to create agent-transcripts dir: %v", err)
	}
	flatPath := filepath.Join(agentTranscriptsDir, sessionID+".jsonl")

	// But the file actually lives at the nested path (Cursor relocated it)
	nestedDir := filepath.Join(agentTranscriptsDir, sessionID)
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("failed to create nested dir: %v", err)
	}
	nestedPath := filepath.Join(nestedDir, sessionID+".jsonl")
	transcript := `{"type":"human","message":{"content":"create a file"}}
{"type":"assistant","message":{"content":"done"}}
`
	if err := os.WriteFile(nestedPath, []byte(transcript), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Create session state pointing to the FLAT (stale) path
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("failed to get HEAD: %v", err)
	}
	state := &SessionState{
		SessionID:      sessionID,
		BaseCommit:     head.Hash().String(),
		WorktreePath:   dir,
		AgentType:      agent.AgentTypeCursor,
		TranscriptPath: flatPath, // stale: file was relocated to nested path
	}
	if err := s.saveSessionState(context.Background(), state); err != nil {
		t.Fatalf("saveSessionState() error = %v", err)
	}

	// CondenseSession should succeed by re-resolving the transcript path
	checkpointID := id.MustCheckpointID("c1d2e3f4a5b6")
	result, err := s.CondenseSession(context.Background(), repo, checkpointID, state, nil)
	if err != nil {
		t.Fatalf("CondenseSession() error = %v, want nil (should re-resolve stale transcript path)", err)
	}

	if result.TotalTranscriptLines != 2 {
		t.Errorf("TotalTranscriptLines = %d, want 2", result.TotalTranscriptLines)
	}

	// State should have been updated to the resolved path
	if state.TranscriptPath != nestedPath {
		t.Errorf("state.TranscriptPath = %q, want %q (should be updated after re-resolution)", state.TranscriptPath, nestedPath)
	}
}

// TestCondenseSession_GeminiTranscript verifies that CondenseSession works correctly
// with Gemini JSON format transcripts, including prompt extraction and format detection.
func TestCondenseSession_GeminiTranscript(t *testing.T) {
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
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial content"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if _, err := worktree.Add("test.txt"); err != nil {
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
	sessionID := "2026-02-09-gemini-test"

	// Create metadata directory with Gemini JSON transcript
	metadataDir := ".trace/metadata/" + sessionID
	metadataDirAbs := filepath.Join(dir, metadataDir)
	if err := os.MkdirAll(metadataDirAbs, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}

	// Gemini JSON format with IDE tags to test stripping
	geminiTranscript := `{
		"sessionId": "test-session",
		"messages": [
			{
				"type": "user",
				"content": "<ide_opened_file>test.txt</ide_opened_file>Create a new file"
			},
			{
				"type": "gemini",
				"content": "I'll create the file for you",
				"tokens": {
					"input": 50,
					"output": 20,
					"cached": 10
				}
			}
		]
	}`

	if err := os.WriteFile(filepath.Join(metadataDirAbs, paths.TranscriptFileName), []byte(geminiTranscript), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Write prompt.txt (simulating what lifecycle does at turn start / turn end)
	if err := os.WriteFile(filepath.Join(metadataDirAbs, paths.PromptFileName), []byte("Create a new file"), 0o644); err != nil {
		t.Fatalf("failed to write prompt file: %v", err)
	}

	// Create modified file
	if err := os.WriteFile(testFile, []byte("modified by gemini"), 0o644); err != nil {
		t.Fatalf("failed to modify file: %v", err)
	}

	// Save checkpoint (creates shadow branch)
	err = s.SaveStep(context.Background(), StepContext{
		SessionID:      sessionID,
		ModifiedFiles:  []string{"test.txt"},
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
		t.Fatalf("SaveStep() error = %v", err)
	}

	// Load session state
	state, err := s.loadSessionState(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("loadSessionState() error = %v", err)
	}
	if state.AgentType != agent.AgentTypeGemini {
		t.Errorf("AgentType = %q, want %q", state.AgentType, agent.AgentTypeGemini)
	}

	// Condense the session
	checkpointID := id.MustCheckpointID("aabbcc112233")
	result, err := s.CondenseSession(context.Background(), repo, checkpointID, state, nil)
	if err != nil {
		t.Fatalf("CondenseSession() error = %v", err)
	}

	// Verify result
	if result.CheckpointID != checkpointID {
		t.Errorf("CheckpointID = %v, want %v", result.CheckpointID, checkpointID)
	}
	if result.SessionID != sessionID {
		t.Errorf("SessionID = %q, want %q", result.SessionID, sessionID)
	}
	if len(result.FilesTouched) != 1 || result.FilesTouched[0] != "test.txt" {
		t.Errorf("FilesTouched = %v, want [test.txt]", result.FilesTouched)
	}

	// Verify condensed data on trace/checkpoints/v1 branch
	store := checkpoint.NewGitStore(repo)
	content, err := store.ReadLatestSessionContent(t.Context(), checkpointID)
	if err != nil {
		t.Fatalf("ReadLatestSessionContent() error = %v", err)
	}

	// Verify transcript was stored
	if len(content.Transcript) == 0 {
		t.Error("Transcript should not be empty")
	}

	// Verify prompts were extracted and IDE tags were stripped
	if !strings.Contains(content.Prompts, "Create a new file") {
		t.Errorf("Prompts = %q, should contain %q (IDE tags should be stripped)", content.Prompts, "Create a new file")
	}
	if strings.Contains(content.Prompts, "<ide_opened_file>") {
		t.Error("Prompts should not contain IDE tags")
	}

	// Verify token usage was calculated
	if content.Metadata.TokenUsage == nil {
		t.Fatal("TokenUsage should not be nil for Gemini transcript")
	}
	if content.Metadata.TokenUsage.InputTokens != 50 {
		t.Errorf("InputTokens = %d, want 50", content.Metadata.TokenUsage.InputTokens)
	}
	if content.Metadata.TokenUsage.OutputTokens != 20 {
		t.Errorf("OutputTokens = %d, want 20", content.Metadata.TokenUsage.OutputTokens)
	}
	if content.Metadata.TokenUsage.CacheReadTokens != 10 {
		t.Errorf("CacheReadTokens = %d, want 10", content.Metadata.TokenUsage.CacheReadTokens)
	}
}
