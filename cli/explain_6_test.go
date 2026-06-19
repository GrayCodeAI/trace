package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cli/summarize"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/GrayCodeAI/trace/cli/transcript"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"
)

func TestHasCodeChanges_OnlyMetadataChanges(t *testing.T) {
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

	// Only .trace/ changes should return false
	if hasCodeChanges(commit) {
		t.Error("hasCodeChanges() should return false when only .trace/ files changed")
	}
}

func TestHasCodeChanges_WithCodeChanges(t *testing.T) {
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

	// Create second commit with code changes
	if err := os.WriteFile(testFile, []byte("modified"), 0o644); err != nil {
		t.Fatalf("failed to modify test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add modified file: %v", err)
	}
	commitHash, err := w.Commit("code change commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to create second commit: %v", err)
	}

	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	// Code changes should return true
	if !hasCodeChanges(commit) {
		t.Error("hasCodeChanges() should return true when code files changed")
	}
}

func TestHasCodeChanges_MixedChanges(t *testing.T) {
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

	// Create second commit with BOTH code and metadata changes
	if err := os.WriteFile(testFile, []byte("modified"), 0o644); err != nil {
		t.Fatalf("failed to modify test file: %v", err)
	}
	metadataDir := filepath.Join(tmpDir, ".trace", "metadata", "session-123")
	if err := os.MkdirAll(metadataDir, 0o755); err != nil {
		t.Fatalf("failed to create metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metadataDir, "full.jsonl"), []byte(`{"test": true}`), 0o644); err != nil {
		t.Fatalf("failed to write metadata file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	if _, err := w.Add(".trace"); err != nil {
		t.Fatalf("failed to add .trace: %v", err)
	}
	commitHash, err := w.Commit("mixed changes commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to create second commit: %v", err)
	}

	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		t.Fatalf("failed to get commit object: %v", err)
	}

	// Mixed changes should return true (code changes present)
	if !hasCodeChanges(commit) {
		t.Error("hasCodeChanges() should return true when commit has both code and metadata changes")
	}
}

func TestGetBranchCheckpoints_FiltersMainCommits(t *testing.T) {
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

	// Create initial commit on master (go-git default)
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("initial"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	mainCommit, err := w.Commit("main commit with Trace-Checkpoint: abc123def456", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com"},
	})
	if err != nil {
		t.Fatalf("failed to create main commit: %v", err)
	}

	// Create feature branch
	featureBranch := "feature/test"
	if err := w.Checkout(&git.CheckoutOptions{
		Hash:   mainCommit,
		Branch: plumbing.NewBranchReferenceName(featureBranch),
		Create: true,
	}); err != nil {
		t.Fatalf("failed to create feature branch: %v", err)
	}

	// Create commit on feature branch
	if err := os.WriteFile(testFile, []byte("feature work"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	_, err = w.Commit("feature commit with Trace-Checkpoint: def456ghi789", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com"},
	})
	if err != nil {
		t.Fatalf("failed to create feature commit: %v", err)
	}

	// Create .trace directory
	if err := os.MkdirAll(".trace", 0o750); err != nil {
		t.Fatalf("failed to create .trace dir: %v", err)
	}

	// Get checkpoints - should only include feature branch commits, not main
	// Note: Without actual checkpoint data in trace/checkpoints/v1, this returns empty
	// but the important thing is it doesn't error and the filtering logic runs
	points, err := getBranchCheckpoints(context.Background(), repo, 20)
	if err != nil {
		t.Fatalf("getBranchCheckpoints() error = %v", err)
	}

	// Without checkpoint data (no trace/checkpoints/v1 branch), should return 0 checkpoints
	// This validates the filtering code path runs without error
	if len(points) != 0 {
		t.Errorf("expected 0 checkpoints without checkpoint data, got %d", len(points))
	}
}

func TestScopeTranscriptForCheckpoint_SlicesTranscript(t *testing.T) {
	// Transcript with 5 lines - prompts 1, 2, 3 with their responses
	fullTranscript := []byte(`{"type":"user","uuid":"u1","message":{"content":"prompt 1"}}
{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"response 1"}]}}
{"type":"user","uuid":"u2","message":{"content":"prompt 2"}}
{"type":"assistant","uuid":"a2","message":{"content":[{"type":"text","text":"response 2"}]}}
{"type":"user","uuid":"u3","message":{"content":"prompt 3"}}
`)

	// Checkpoint starts at line 2 (after prompt 1 and response 1)
	// Should only include lines 2-4 (prompt 2, response 2, prompt 3)
	scoped := scopeTranscriptForCheckpoint(fullTranscript, 2, agent.AgentTypeClaudeCode)

	// Parse the scoped transcript to verify content
	lines, err := transcript.ParseFromBytes(scoped)
	if err != nil {
		t.Fatalf("failed to parse scoped transcript: %v", err)
	}

	if len(lines) != 3 {
		t.Fatalf("expected 3 lines in scoped transcript, got %d", len(lines))
	}

	// First line should be prompt 2 (u2), not prompt 1
	if lines[0].UUID != "u2" {
		t.Errorf("expected first line to be u2 (prompt 2), got %s", lines[0].UUID)
	}

	// Last line should be prompt 3 (u3)
	if lines[2].UUID != "u3" {
		t.Errorf("expected last line to be u3 (prompt 3), got %s", lines[2].UUID)
	}
}

func TestScopeTranscriptForCheckpoint_ZeroLinesReturnsAll(t *testing.T) {
	transcriptData := []byte(`{"type":"user","uuid":"u1","message":{"content":"prompt 1"}}
{"type":"user","uuid":"u2","message":{"content":"prompt 2"}}
`)

	// With linesAtStart=0, should return full transcript
	scoped := scopeTranscriptForCheckpoint(transcriptData, 0, agent.AgentTypeClaudeCode)

	lines, err := transcript.ParseFromBytes(scoped)
	if err != nil {
		t.Fatalf("failed to parse scoped transcript: %v", err)
	}

	if len(lines) != 2 {
		t.Fatalf("expected 2 lines with linesAtStart=0, got %d", len(lines))
	}
}

func TestScopeTranscriptForCheckpoint_CodexUsesStoredLineOffsets(t *testing.T) {
	t.Parallel()

	fullTranscript := []byte(`{"timestamp":"t1","type":"session_meta","payload":{"id":"s1"}}
{"timestamp":"t2","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"developer instructions"}]}}
{"timestamp":"t3","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md\ninstructions"}]}}
{"timestamp":"t4","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"first prompt"}]}}
{"timestamp":"t5","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"response to first"}]}}
{"timestamp":"t6","type":"event_msg","payload":{"type":"token_count","input_tokens":10,"output_tokens":1}}
{"timestamp":"t7","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"second prompt"}]}}
{"timestamp":"t8","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"response to second"}]}}
`)

	scoped := scopeTranscriptForCheckpoint(fullTranscript, 6, agent.AgentTypeCodex)
	entries, err := summarize.BuildCondensedTranscriptFromBytes(redact.AlreadyRedacted(scoped), agent.AgentTypeCodex)
	if err != nil {
		t.Fatalf("failed to build condensed transcript: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 scoped entries, got %d", len(entries))
	}

	if entries[0].Type != summarize.EntryTypeUser || entries[0].Content != "second prompt" {
		t.Fatalf("expected first entry to be second prompt, got %#v", entries[0])
	}

	if entries[1].Type != summarize.EntryTypeAssistant || entries[1].Content != "response to second" {
		t.Fatalf("expected second entry to be second response, got %#v", entries[1])
	}
}

func TestExtractPromptsFromScopedTranscript(t *testing.T) {
	// Transcript with 4 lines - 2 user prompts, 2 assistant responses
	transcript := []byte(`{"type":"user","uuid":"u1","message":{"content":"First prompt"}}
{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"First response"}]}}
{"type":"user","uuid":"u2","message":{"content":"Second prompt"}}
{"type":"assistant","uuid":"a2","message":{"content":[{"type":"text","text":"Second response"}]}}
`)

	prompts := extractPromptsFromTranscript(transcript, "")

	if len(prompts) != 2 {
		t.Fatalf("expected 2 prompts, got %d", len(prompts))
	}

	if prompts[0] != "First prompt" {
		t.Errorf("expected first prompt 'First prompt', got %q", prompts[0])
	}

	if prompts[1] != "Second prompt" {
		t.Errorf("expected second prompt 'Second prompt', got %q", prompts[1])
	}
}

func TestFormatCheckpointOutput_UsesScopedPrompts(t *testing.T) {
	// Full transcript with 4 lines (2 prompts + 2 responses)
	// Checkpoint starts at line 2 (should only show second prompt)
	fullTranscript := []byte(`{"type":"user","uuid":"u1","message":{"content":"First prompt - should NOT appear"}}
{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"First response"}]}}
{"type":"user","uuid":"u2","message":{"content":"Second prompt - SHOULD appear"}}
{"type":"assistant","uuid":"a2","message":{"content":[{"type":"text","text":"Second response"}]}}
`)

	summary := &checkpoint.CheckpointSummary{
		CheckpointID: id.MustCheckpointID("abc123def456"),
		FilesTouched: []string{"main.go"},
	}
	content := &checkpoint.SessionContent{
		Metadata: checkpoint.CommittedMetadata{
			CheckpointID:              "abc123def456",
			SessionID:                 "2026-01-30-test-session",
			CreatedAt:                 time.Date(2026, 1, 30, 10, 30, 0, 0, time.UTC),
			FilesTouched:              []string{"main.go"},
			CheckpointTranscriptStart: 2, // Checkpoint starts at line 2
		},
		Prompts:    "First prompt - should NOT appear\nSecond prompt - SHOULD appear", // Full prompts (not scoped yet)
		Transcript: fullTranscript,
	}

	// Verbose output should use scoped prompts
	output := formatCheckpointOutput(summary, content, id.MustCheckpointID("abc123def456"), nil, checkpoint.Author{}, true, false, &bytes.Buffer{})

	// Should show ONLY the second prompt (scoped)
	if !strings.Contains(output, "Second prompt - SHOULD appear") {
		t.Errorf("expected scoped prompt in output, got:\n%s", output)
	}

	// Should NOT show the first prompt (it's before this checkpoint's scope)
	if strings.Contains(output, "First prompt - should NOT appear") {
		t.Errorf("expected first prompt to be excluded from scoped output, got:\n%s", output)
	}
}

func TestFormatCheckpointOutput_FallsBackToStoredPrompts(t *testing.T) {
	// Test backwards compatibility: when no transcript exists, use stored prompts
	summary := &checkpoint.CheckpointSummary{
		CheckpointID: id.MustCheckpointID("abc123def456"),
		FilesTouched: []string{"main.go"},
	}
	content := &checkpoint.SessionContent{
		Metadata: checkpoint.CommittedMetadata{
			CheckpointID:              "abc123def456",
			SessionID:                 "2026-01-30-test-session",
			CreatedAt:                 time.Date(2026, 1, 30, 10, 30, 0, 0, time.UTC),
			FilesTouched:              []string{"main.go"},
			CheckpointTranscriptStart: 0,
		},
		Prompts:    "Stored prompt from older checkpoint",
		Transcript: nil, // No transcript available
	}

	// Verbose output should fall back to stored prompts
	output := formatCheckpointOutput(summary, content, id.MustCheckpointID("abc123def456"), nil, checkpoint.Author{}, true, false, &bytes.Buffer{})

	// Intent should use stored prompt
	if !strings.Contains(output, "Stored prompt from older checkpoint") {
		t.Errorf("expected fallback to stored prompts, got:\n%s", output)
	}
}

func TestFormatCheckpointOutput_FullShowsTraceTranscript(t *testing.T) {
	// Test that --full mode shows the trace transcript, not scoped
	fullTranscript := []byte(`{"type":"user","uuid":"u1","message":{"content":"First prompt"}}
{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"First response"}]}}
{"type":"user","uuid":"u2","message":{"content":"Second prompt"}}
{"type":"assistant","uuid":"a2","message":{"content":[{"type":"text","text":"Second response"}]}}
`)

	summary := &checkpoint.CheckpointSummary{
		CheckpointID: id.MustCheckpointID("abc123def456"),
		FilesTouched: []string{"main.go"},
	}
	content := &checkpoint.SessionContent{
		Metadata: checkpoint.CommittedMetadata{
			CheckpointID:              "abc123def456",
			SessionID:                 "2026-01-30-test-session",
			CreatedAt:                 time.Date(2026, 1, 30, 10, 30, 0, 0, time.UTC),
			FilesTouched:              []string{"main.go"},
			CheckpointTranscriptStart: 2, // Checkpoint starts at line 2
		},
		Transcript: fullTranscript,
	}

	// Full mode should show the ENTIRE transcript (not scoped)
	output := formatCheckpointOutput(summary, content, id.MustCheckpointID("abc123def456"), nil, checkpoint.Author{}, false, true, &bytes.Buffer{})

	// Should show the full transcript including first prompt (even though scoped prompts exclude it)
	if !strings.Contains(output, "First prompt") {
		t.Errorf("expected --full to show trace transcript including first prompt, got:\n%s", output)
	}
	if !strings.Contains(output, "Second prompt") {
		t.Errorf("expected --full to show trace transcript including second prompt, got:\n%s", output)
	}
}

func TestRunExplainCommit_NoCheckpointTrailer(t *testing.T) {
	// Create test repo with a commit that has no Trace-Checkpoint trailer
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	// Create a commit without checkpoint trailer
	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	hash, err := w.Commit("Regular commit without trailer", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to create commit: %v", err)
	}

	var buf bytes.Buffer
	err = runExplainCommit(context.Background(), &buf, &buf, hash.String()[:7], false, false, false, false, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "✗ No associated Trace checkpoint") {
		t.Errorf("expected styled failure block, got: %s", output)
	}
	if !strings.Contains(output, "  reason") {
		t.Errorf("expected reason row, got: %s", output)
	}
}

func TestRunExplainCommit_WithCheckpointTrailer(t *testing.T) {
	// Create test repo with a commit that has an Trace-Checkpoint trailer
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	// Create a commit with checkpoint trailer
	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("content"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}

	// Create commit with checkpoint trailer
	checkpointID := "abc123def456"
	commitMsg := "Feature commit\n\nTrace-Checkpoint: " + checkpointID + "\n"
	hash, err := w.Commit(commitMsg, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to create commit: %v", err)
	}

	var buf bytes.Buffer
	// This should try to look up the checkpoint and fail (checkpoint doesn't exist in store)
	// but it should still attempt the lookup rather than showing commit details
	err = runExplainCommit(context.Background(), &buf, &buf, hash.String()[:7], false, false, false, false, false, false, false)

	// Should error because the checkpoint doesn't exist in the store
	if err == nil {
		t.Fatalf("expected error for missing checkpoint in store, got nil")
	}

	// Error should mention checkpoint not found
	if !strings.Contains(err.Error(), "checkpoint not found") && !strings.Contains(err.Error(), "abc123def456") {
		t.Errorf("expected error about checkpoint not found, got: %v", err)
	}
}

func TestFormatBranchCheckpoints_SessionFilter(t *testing.T) {
	now := time.Now()
	points := []strategy.RewindPoint{
		{
			ID:            "abc123def456",
			Message:       "Checkpoint from session 1",
			Date:          now,
			CheckpointID:  "chk111111111",
			SessionID:     "2026-01-22-session-alpha",
			SessionPrompt: "Task for session alpha",
		},
		{
			ID:            "def456ghi789",
			Message:       "Checkpoint from session 2",
			Date:          now.Add(-time.Hour),
			CheckpointID:  "chk222222222",
			SessionID:     "2026-01-22-session-beta",
			SessionPrompt: "Task for session beta",
		},
		{
			ID:            "ghi789jkl012",
			Message:       "Another checkpoint from session 1",
			Date:          now.Add(-2 * time.Hour),
			CheckpointID:  "chk333333333",
			SessionID:     "2026-01-22-session-alpha",
			SessionPrompt: "Another task for session alpha",
		},
	}

	t.Run("no filter shows all checkpoints", func(t *testing.T) {
		output := formatBranchCheckpoints(io.Discard, "main", points, "")

		// Should show all checkpoints (new metadata-row shape)
		if !strings.Contains(output, "checkpoints  3") {
			t.Errorf("expected 'checkpoints  3' in output, got:\n%s", output)
		}
		// Should show prompts from both sessions
		if !strings.Contains(output, "Task for session alpha") {
			t.Errorf("expected alpha session prompt in output, got:\n%s", output)
		}
		if !strings.Contains(output, "Task for session beta") {
			t.Errorf("expected beta session prompt in output, got:\n%s", output)
		}
	})

	t.Run("filter by exact session ID", func(t *testing.T) {
		output := formatBranchCheckpoints(io.Discard, "main", points, "2026-01-22-session-alpha")

		// Should show only alpha checkpoints (2 of them)
		if !strings.Contains(output, "checkpoints  2") {
			t.Errorf("expected 'checkpoints  2' in output, got:\n%s", output)
		}
		if !strings.Contains(output, "Task for session alpha") {
			t.Errorf("expected alpha session prompt in output, got:\n%s", output)
		}
		// Should NOT contain beta session prompt
		if strings.Contains(output, "Task for session beta") {
			t.Errorf("expected output to NOT contain beta session prompt, got:\n%s", output)
		}
		// Should show filter info as a metadata row (label aligned to widest "checkpoints")
		if !strings.Contains(output, "session      2026-01-22-session-alpha") {
			t.Errorf("expected 'session ... 2026-01-22-session-alpha' in output, got:\n%s", output)
		}
	})

	t.Run("filter by session ID prefix", func(t *testing.T) {
		output := formatBranchCheckpoints(io.Discard, "main", points, "2026-01-22-session-b")

		// Should show only beta checkpoint (1)
		if !strings.Contains(output, "checkpoints  1") {
			t.Errorf("expected 'checkpoints  1' in output, got:\n%s", output)
		}
		if !strings.Contains(output, "Task for session beta") {
			t.Errorf("expected beta session prompt in output, got:\n%s", output)
		}
	})

	t.Run("filter with no matches", func(t *testing.T) {
		output := formatBranchCheckpoints(io.Discard, "main", points, "nonexistent-session")

		// Should show 0 checkpoints
		if !strings.Contains(output, "checkpoints  0") {
			t.Errorf("expected 'checkpoints  0' in output, got:\n%s", output)
		}
		// Should show filter info even with no matches (label aligned to widest "checkpoints")
		if !strings.Contains(output, "session      nonexistent-session") {
			t.Errorf("expected 'session ... nonexistent-session' in output, got:\n%s", output)
		}
	})
}

func TestRunExplain_SessionFlagFiltersListView(t *testing.T) {
	// Test that --session alone (without --checkpoint or --commit) filters the list view.
	// This is a unit test for the routing logic.
	// Use a fresh git repo so we don't walk the real repo's shadow branches (which is slow).
	tmp := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test User"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = tmp
		cmd.Env = testutil.GitIsolatedEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(tmp)

	var buf, errBuf bytes.Buffer

	// When session is specified alone, it should NOT error for mutual exclusivity
	// It should route to the list view with a filter (which may fail for other reasons
	// like not being in a git repo, but not for mutual exclusivity)
	err := runExplain(context.Background(), &buf, &errBuf, "some-session", "", "", "", false, false, false, false, false, false, false)

	// Should NOT be a mutual exclusivity error
	if err != nil && strings.Contains(err.Error(), "cannot specify multiple") {
		t.Errorf("--session alone should not trigger mutual exclusivity error, got: %v", err)
	}
}

func TestRunExplain_SessionWithCheckpointStillMutuallyExclusive(t *testing.T) {
	// Test that --session with --checkpoint is still an error
	var buf, errBuf bytes.Buffer

	err := runExplain(context.Background(), &buf, &errBuf, "some-session", "", "some-checkpoint", "", false, false, false, false, false, false, false)

	if err == nil {
		t.Error("expected error when --session and --checkpoint both specified")
	}
	if !strings.Contains(err.Error(), "cannot specify multiple") {
		t.Errorf("expected 'cannot specify multiple' error, got: %v", err)
	}
}

func TestRunExplain_SessionWithCommitStillMutuallyExclusive(t *testing.T) {
	// Test that --session with --commit is still an error
	var buf, errBuf bytes.Buffer

	err := runExplain(context.Background(), &buf, &errBuf, "some-session", "some-commit", "", "", false, false, false, false, false, false, false)

	if err == nil {
		t.Error("expected error when --session and --commit both specified")
	}
	if !strings.Contains(err.Error(), "cannot specify multiple") {
		t.Errorf("expected 'cannot specify multiple' error, got: %v", err)
	}
}

func TestFormatCheckpointOutput_WithAuthor(t *testing.T) {
	summary := &checkpoint.CheckpointSummary{
		CheckpointID: id.MustCheckpointID("abc123def456"),
		FilesTouched: []string{"main.go"},
	}
	content := &checkpoint.SessionContent{
		Metadata: checkpoint.CommittedMetadata{
			CheckpointID:              "abc123def456",
			SessionID:                 "2026-01-30-test-session",
			CreatedAt:                 time.Date(2026, 1, 30, 10, 30, 0, 0, time.UTC),
			FilesTouched:              []string{"main.go"},
			CheckpointTranscriptStart: 0,
		},
		Prompts:    "Add a new feature",
		Transcript: nil, // No transcript available
	}

	author := checkpoint.Author{
		Name:  "Alice Developer",
		Email: "alice@example.com",
	}

	// With author, should show author line
	output := formatCheckpointOutput(summary, content, id.MustCheckpointID("abc123def456"), nil, author, true, false, &bytes.Buffer{})

	if !strings.Contains(output, "  author   Alice Developer <alice@example.com>") {
		t.Errorf("expected author line in output, got:\n%s", output)
	}
}

func TestFormatCheckpointOutput_EmptyAuthor(t *testing.T) {
	// Test backwards compatibility: when no transcript exists, use stored prompts
	summary := &checkpoint.CheckpointSummary{
		CheckpointID: id.MustCheckpointID("abc123def456"),
		FilesTouched: []string{"main.go"},
	}
	content := &checkpoint.SessionContent{
		Metadata: checkpoint.CommittedMetadata{
			CheckpointID:              "abc123def456",
			SessionID:                 "2026-01-30-test-session",
			CreatedAt:                 time.Date(2026, 1, 30, 10, 30, 0, 0, time.UTC),
			FilesTouched:              []string{"main.go"},
			CheckpointTranscriptStart: 0,
		},
		Prompts:    "Add a new feature",
		Transcript: nil, // No transcript available
	}

	// Empty author - should not show author line
	author := checkpoint.Author{}

	output := formatCheckpointOutput(summary, content, id.MustCheckpointID("abc123def456"), nil, author, true, false, &bytes.Buffer{})

	if strings.Contains(output, "  author") {
		t.Errorf("expected no author line for empty author, got:\n%s", output)
	}
}
