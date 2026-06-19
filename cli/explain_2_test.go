package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/claudecode"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cli/summarize"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/GrayCodeAI/trace/cli/trailers"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"
)

func TestMaybeCompactExternalTranscriptForSummary_RedactsExternalOutput(t *testing.T) {
	// Cannot use t.Parallel() because external agent discovery mutates the
	// package-level agent registry and this test changes cwd/PATH.
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	ctx := context.Background()
	tmpDir := t.TempDir()
	testutil.InitRepo(t, tmpDir)
	t.Chdir(tmpDir)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".trace"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".trace", "settings.json"),
		[]byte(`{"enabled":true,"external_agents":true}`),
		0o644,
	))

	const (
		name   = "summary-redact"
		kind   = types.AgentType("Summary Redact Agent")
		secret = "q9Xv2Lm8Rt1Yp4Kd7Wz0Hs6Nc3Bf5Jg"
	)
	externalDir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
  info)
    echo '{"protocol_version":1,"name":"` + name + `","type":"` + string(kind) + `","description":"External redaction test agent","is_preview":false,"protected_dirs":[],"hook_names":[],"capabilities":{"hooks":false,"transcript_analyzer":false,"transcript_preparer":false,"token_calculator":false,"compact_transcript":true,"text_generator":false,"hook_response_writer":false,"subagent_aware_extractor":false}}'
    ;;
  compact-transcript)
    echo '{"transcript":"eyJ2IjoxLCJhZ2VudCI6InN1bW1hcnktcmVkYWN0IiwiY2xpX3ZlcnNpb24iOiJ0ZXN0IiwidHlwZSI6InVzZXIiLCJ0cyI6IjIwMjYtMDEtMDFUMDA6MDA6MDBaIiwiY29udGVudCI6W3sidGV4dCI6ImtleT1xOVh2MkxtOFJ0MVlwNEtkN1d6MEhzNk5jM0JmNUpnIn1dfQo="}'
    ;;
  *)
    echo '{}'
    ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(externalDir, "trace-agent-"+name), []byte(script), 0o755))
	t.Setenv("PATH", externalDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got := maybeCompactExternalTranscriptForSummary(ctx, []byte("not-json"), kind)
	if strings.Contains(string(got), secret) {
		t.Fatalf("external compact transcript was not redacted: %s", got)
	}
	if !strings.Contains(string(got), redact.RedactedPlaceholder) {
		t.Fatalf("expected redacted compact transcript, got: %s", got)
	}
}

func TestGenerateCheckpointAISummary_UsesParentDeadlineAndWrapsSentinel(t *testing.T) {
	tmpTimeout := checkpointSummaryTimeout
	tmpGenerator := generateTranscriptSummary
	t.Cleanup(func() {
		checkpointSummaryTimeout = tmpTimeout
		generateTranscriptSummary = tmpGenerator
	})

	checkpointSummaryTimeout = 30 * time.Second

	parentCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	parentDeadline, _ := parentCtx.Deadline()

	var gotDeadline time.Time
	generateTranscriptSummary = func(
		ctx context.Context,
		_ redact.RedactedBytes,
		_ []string,
		_ types.AgentType,
		_ summarize.Generator,
	) (*checkpoint.Summary, error) {
		gotDeadline, _ = ctx.Deadline()
		<-ctx.Done()
		return nil, ctx.Err()
	}

	_, appliedDeadline, err := generateCheckpointAISummary(parentCtx, []byte("transcript"), nil, agent.AgentTypeClaudeCode, nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
	if gotDeadline.IsZero() {
		t.Fatal("expected deadline to be captured")
	}
	// The applied deadline must reflect the shorter parent-ctx deadline,
	// not the package-default checkpointSummaryTimeout. Otherwise
	// formatCheckpointSummaryError would report the wrong timeout to users.
	if appliedDeadline >= checkpointSummaryTimeout {
		t.Fatalf("appliedDeadline = %s; want shorter than %s (parent had tighter deadline)",
			appliedDeadline, checkpointSummaryTimeout)
	}
	if delta := gotDeadline.Sub(parentDeadline); delta < -5*time.Millisecond || delta > 5*time.Millisecond {
		t.Fatalf("deadline delta = %s, want near 0", delta)
	}
	if strings.Contains(err.Error(), "30s") {
		t.Fatalf("timeout error should not report default timeout when parent deadline fired: %v", err)
	}
}

// TestGenerateCheckpointAISummary_PreservesClaudeErrorWhenCtxIsDone guards
// against the race where the underlying summarizer returns a typed
// *ClaudeError AND the context happens to be done. Prior code checked
// timeoutCtx.Err() and unconditionally wrapped with %w context.DeadlineExceeded,
// which discarded the typed error and routed the user to the wrong
// "safety deadline" guidance instead of the auth/rate-limit message.
func TestGenerateCheckpointAISummary_PreservesClaudeErrorWhenCtxIsDone(t *testing.T) {
	tmpTimeout := checkpointSummaryTimeout
	tmpGenerator := generateTranscriptSummary
	t.Cleanup(func() {
		checkpointSummaryTimeout = tmpTimeout
		generateTranscriptSummary = tmpGenerator
	})

	checkpointSummaryTimeout = 30 * time.Second

	// Cancel the parent before we even call — ctx.Err() will be non-nil.
	parentCtx, cancel := context.WithCancel(context.Background())
	cancel()

	claudeErr := &claudecode.ClaudeError{Kind: claudecode.ClaudeErrorAuth, Message: "Invalid API key"}
	generateTranscriptSummary = func(
		context.Context,
		redact.RedactedBytes,
		[]string,
		types.AgentType,
		summarize.Generator,
	) (*checkpoint.Summary, error) {
		return nil, claudeErr
	}

	_, _, err := generateCheckpointAISummary(parentCtx, []byte("transcript"), nil, agent.AgentTypeClaudeCode, nil)
	var ce *claudecode.ClaudeError
	if !errors.As(err, &ce) {
		t.Fatalf("errors.As did not recover *ClaudeError; got %v", err)
	}
	if ce.Kind != claudecode.ClaudeErrorAuth {
		t.Errorf("Kind = %v; want auth", ce.Kind)
	}
}

func TestGenerateCheckpointAISummary_ClampsLongParentDeadlineToDefaultTimeout(t *testing.T) {
	tmpTimeout := checkpointSummaryTimeout
	tmpGenerator := generateTranscriptSummary
	t.Cleanup(func() {
		checkpointSummaryTimeout = tmpTimeout
		generateTranscriptSummary = tmpGenerator
	})

	checkpointSummaryTimeout = 50 * time.Millisecond

	parentCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var gotDeadline time.Time
	generateTranscriptSummary = func(
		ctx context.Context,
		_ redact.RedactedBytes,
		_ []string,
		_ types.AgentType,
		_ summarize.Generator,
	) (*checkpoint.Summary, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return nil, errors.New("expected deadline on summary context")
		}
		gotDeadline = deadline
		return &checkpoint.Summary{Intent: "intent", Outcome: "outcome"}, nil
	}

	start := time.Now()
	summary, _, err := generateCheckpointAISummary(parentCtx, []byte("transcript"), nil, agent.AgentTypeClaudeCode, nil)
	if err != nil {
		t.Fatalf("generateCheckpointAISummary() error = %v", err)
	}
	if summary == nil {
		t.Fatal("expected summary")
	}
	if gotDeadline.IsZero() {
		t.Fatal("expected deadline to be set")
	}
	if remaining := gotDeadline.Sub(start); remaining < 30*time.Millisecond || remaining > 200*time.Millisecond {
		t.Fatalf("deadline offset = %s, want around %s", remaining, checkpointSummaryTimeout)
	}
}

func TestGenerateCheckpointAISummary_UsesCancellationSentinel(t *testing.T) {
	tmpTimeout := checkpointSummaryTimeout
	tmpGenerator := generateTranscriptSummary
	t.Cleanup(func() {
		checkpointSummaryTimeout = tmpTimeout
		generateTranscriptSummary = tmpGenerator
	})

	parentCtx, cancel := context.WithCancel(context.Background())

	generateTranscriptSummary = func(
		ctx context.Context,
		_ redact.RedactedBytes,
		_ []string,
		_ types.AgentType,
		_ summarize.Generator,
	) (*checkpoint.Summary, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}

	_, _, err := generateCheckpointAISummary(parentCtx, []byte("transcript"), nil, agent.AgentTypeClaudeCode, nil)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected Canceled, got %v", err)
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("expected cancellation message, got %v", err)
	}
}

func TestExplainCommit_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Initialize git repo
	testutil.InitRepo(t, tmpDir)

	var stdout bytes.Buffer
	err := runExplainCommit(context.Background(), &stdout, &stdout, "nonexistent", false, false, false, false, false, false, false)

	if err == nil {
		t.Error("expected error for nonexistent commit, got nil")
	}
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "resolve") {
		t.Errorf("expected 'not found' or 'resolve' in error, got: %v", err)
	}
}

func TestExplainCommit_NoTraceData(t *testing.T) {
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

	// Create a commit without Trace metadata
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	commitHash, err := w.Commit("regular commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("failed to create commit: %v", err)
	}

	var stdout bytes.Buffer
	err = runExplainCommit(context.Background(), &stdout, &stdout, commitHash.String(), false, false, false, false, false, false, false)
	if err != nil {
		t.Fatalf("runExplainCommit() should not error for non-Trace commits, got: %v", err)
	}

	output := stdout.String()

	// Should show message indicating no Trace checkpoint (new failure-block shape)
	if !strings.Contains(output, "✗ No associated Trace checkpoint") {
		t.Errorf("expected styled failure block on output, got: %s", output)
	}
	if !strings.Contains(output, "  reason") {
		t.Errorf("expected reason row, got: %s", output)
	}
	// Should mention the commit hash
	if !strings.Contains(output, commitHash.String()[:7]) {
		t.Errorf("expected output to contain short commit hash, got: %s", output)
	}
}

func TestExplainCommit_WithMetadataTrailerButNoCheckpoint(t *testing.T) {
	// Test that commits with Trace-Metadata trailer (but no Trace-Checkpoint)
	// now show "no checkpoint" message (new behavior)
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

	// Create session metadata directory first
	sessionID := "2025-12-09-test-session-xyz789"
	sessionDir := filepath.Join(tmpDir, ".trace", "metadata", sessionID)
	if err := os.MkdirAll(sessionDir, 0o750); err != nil {
		t.Fatalf("failed to create session dir: %v", err)
	}

	// Create prompt file
	promptContent := "Add new feature"
	if err := os.WriteFile(filepath.Join(sessionDir, paths.PromptFileName), []byte(promptContent), 0o644); err != nil {
		t.Fatalf("failed to create prompt file: %v", err)
	}

	// Create a commit with Trace-Metadata trailer (but NO Trace-Checkpoint)
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("feature content"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}

	// Commit with Trace-Metadata trailer (no Trace-Checkpoint)
	metadataDir := ".trace/metadata/" + sessionID
	commitMessage := trailers.FormatMetadata("Add new feature", metadataDir)
	commitHash, err := w.Commit(commitMessage, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("failed to create commit: %v", err)
	}

	var stdout bytes.Buffer
	err = runExplainCommit(context.Background(), &stdout, &stdout, commitHash.String(), false, false, false, false, false, false, false)
	if err != nil {
		t.Fatalf("runExplainCommit() error = %v", err)
	}

	output := stdout.String()

	// New behavior: should show "no checkpoint" failure block since there's no Trace-Checkpoint trailer
	if !strings.Contains(output, "✗ No associated Trace checkpoint") {
		t.Errorf("expected styled failure block, got: %s", output)
	}
	if !strings.Contains(output, "  reason") {
		t.Errorf("expected reason row, got: %s", output)
	}
	// Should mention the commit hash
	if !strings.Contains(output, commitHash.String()[:7]) {
		t.Errorf("expected output to contain short commit hash, got: %s", output)
	}
}

func TestExplainDefault_ShowsBranchView(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Initialize git repo
	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	// Create initial commit so HEAD exists (required for branch view)
	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	_, err = w.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create .trace directory
	if err := os.MkdirAll(".trace", 0o750); err != nil {
		t.Fatalf("failed to create .trace dir: %v", err)
	}

	var stdout bytes.Buffer
	err = runExplainDefault(context.Background(), &stdout, true) // noPager=true for test
	// Should NOT error - should show branch view
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	output := stdout.String()
	// Should show branch header (new metadata-row shape: "branch  <name>")
	if !strings.Contains(output, "branch  ") {
		t.Errorf("expected 'branch' row in output, got: %s", output)
	}
	// Should show checkpoints count (likely 0)
	if !strings.Contains(output, "checkpoints") {
		t.Errorf("expected 'checkpoints' row in output, got: %s", output)
	}
}

func TestExplainDefault_NoCheckpoints_ShowsHelpfulMessage(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Initialize git repo
	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	// Create initial commit so HEAD exists (required for branch view)
	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := w.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	_, err = w.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@example.com",
		},
	})
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create .trace directory but no checkpoints
	if err := os.MkdirAll(".trace", 0o750); err != nil {
		t.Fatalf("failed to create .trace dir: %v", err)
	}

	var stdout bytes.Buffer
	err = runExplainDefault(context.Background(), &stdout, true) // noPager=true for test
	// Should NOT error
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	output := stdout.String()
	// Should show checkpoints count as 0 (new metadata-row shape)
	if !strings.Contains(output, "checkpoints  0") {
		t.Errorf("expected 'checkpoints  0' in output, got: %s", output)
	}
	// Should show helpful message about checkpoints appearing after saves
	if !strings.Contains(output, "Checkpoints will appear") || !strings.Contains(output, "agent session") {
		t.Errorf("expected helpful message about checkpoints, got: %s", output)
	}
}

func TestExplainBothFlagsError(t *testing.T) {
	// Test that providing both --session and --commit returns an error
	var stdout, stderr bytes.Buffer
	err := runExplain(context.Background(), &stdout, &stderr, "session-id", "commit-sha", "", "", false, false, false, false, false, false, false)

	if err == nil {
		t.Error("expected error when both flags provided, got nil")
	}
	// Case-insensitive check for "cannot specify multiple"
	errLower := strings.ToLower(err.Error())
	if !strings.Contains(errLower, "cannot specify multiple") {
		t.Errorf("expected 'cannot specify multiple' in error, got: %v", err)
	}
}

func TestFormatSessionInfo(t *testing.T) {
	now := time.Now()
	session := &strategy.Session{
		ID:          "2025-12-09-test-session-abc",
		Description: "Test description",
		Strategy:    "manual-commit",
		StartTime:   now,
		Checkpoints: []strategy.Checkpoint{
			{
				CheckpointID: "abc1234567890",
				Message:      "First checkpoint",
				Timestamp:    now.Add(-time.Hour),
			},
			{
				CheckpointID: "def0987654321",
				Message:      "Second checkpoint",
				Timestamp:    now,
			},
		},
	}

	// Create checkpoint details matching the session checkpoints
	checkpointDetails := []checkpointDetail{
		{
			Index:     1,
			ShortID:   "abc1234",
			Timestamp: now.Add(-time.Hour),
			Message:   "First checkpoint",
			Interactions: []interaction{{
				Prompt:    "Fix the bug",
				Responses: []string{"Fixed the bug in auth module"},
				Files:     []string{"auth.go"},
			}},
			Files: []string{"auth.go"},
		},
		{
			Index:     2,
			ShortID:   "def0987",
			Timestamp: now,
			Message:   "Second checkpoint",
			Interactions: []interaction{{
				Prompt:    "Add tests",
				Responses: []string{"Added unit tests"},
				Files:     []string{"auth_test.go"},
			}},
			Files: []string{"auth_test.go"},
		},
	}

	output := formatSessionInfo(session, "", checkpointDetails)

	// Verify output contains expected sections
	if !strings.Contains(output, "Session:") {
		t.Error("expected output to contain 'Session:'")
	}
	if !strings.Contains(output, session.ID) {
		t.Error("expected output to contain session ID")
	}
	if !strings.Contains(output, "Strategy:") {
		t.Error("expected output to contain 'Strategy:'")
	}
	if !strings.Contains(output, "manual-commit") {
		t.Error("expected output to contain strategy name")
	}
	if !strings.Contains(output, "Checkpoints: 2") {
		t.Error("expected output to contain 'Checkpoints: 2'")
	}
	// Check checkpoint details
	if !strings.Contains(output, "Checkpoint 1") {
		t.Error("expected output to contain 'Checkpoint 1'")
	}
	if !strings.Contains(output, "## Prompt") {
		t.Error("expected output to contain '## Prompt'")
	}
	if !strings.Contains(output, "## Responses") {
		t.Error("expected output to contain '## Responses'")
	}
	if !strings.Contains(output, "Files Modified") {
		t.Error("expected output to contain 'Files Modified'")
	}
}

func TestFormatSessionInfo_WithSourceRef(t *testing.T) {
	now := time.Now()
	session := &strategy.Session{
		ID:          "2025-12-09-test-session-abc",
		Description: "Test description",
		Strategy:    "manual-commit",
		StartTime:   now,
		Checkpoints: []strategy.Checkpoint{
			{
				CheckpointID: "abc1234567890",
				Message:      "First checkpoint",
				Timestamp:    now,
			},
		},
	}

	checkpointDetails := []checkpointDetail{
		{
			Index:     1,
			ShortID:   "abc1234",
			Timestamp: now,
			Message:   "First checkpoint",
		},
	}

	// Test with source ref provided
	sourceRef := "trace/metadata@abc123def456"
	output := formatSessionInfo(session, sourceRef, checkpointDetails)

	// Verify source ref is displayed
	if !strings.Contains(output, "Source Ref:") {
		t.Error("expected output to contain 'Source Ref:'")
	}
	if !strings.Contains(output, sourceRef) {
		t.Errorf("expected output to contain source ref %q, got:\n%s", sourceRef, output)
	}
}

// TestManualCommitStrategyCallable verifies that the strategy's methods are callable
func TestManualCommitStrategyCallable(t *testing.T) {
	s := strategy.NewManualCommitStrategy()

	// GetAdditionalSessions should exist and be callable
	_, err := s.GetAdditionalSessions(context.Background())
	if err != nil {
		t.Logf("GetAdditionalSessions returned error: %v", err)
	}
}

func TestFormatSessionInfo_CheckpointNumberingReversed(t *testing.T) {
	now := time.Now()
	session := &strategy.Session{
		ID:          "2025-12-09-test-session",
		Strategy:    "manual-commit",
		StartTime:   now.Add(-2 * time.Hour),
		Checkpoints: []strategy.Checkpoint{}, // Not used for format test
	}

	// Simulate checkpoints coming in newest-first order from ListSessions
	// but numbered with oldest=1, newest=N
	checkpointDetails := []checkpointDetail{
		{
			Index:     3, // Newest checkpoint should have highest number
			ShortID:   "ccc3333",
			Timestamp: now,
			Message:   "Third (newest) checkpoint",
			Interactions: []interaction{{
				Prompt:    "Latest change",
				Responses: []string{},
			}},
		},
		{
			Index:     2,
			ShortID:   "bbb2222",
			Timestamp: now.Add(-time.Hour),
			Message:   "Second checkpoint",
			Interactions: []interaction{{
				Prompt:    "Middle change",
				Responses: []string{},
			}},
		},
		{
			Index:     1, // Oldest checkpoint should be #1
			ShortID:   "aaa1111",
			Timestamp: now.Add(-2 * time.Hour),
			Message:   "First (oldest) checkpoint",
			Interactions: []interaction{{
				Prompt:    "Initial change",
				Responses: []string{},
			}},
		},
	}

	output := formatSessionInfo(session, "", checkpointDetails)

	// Verify checkpoint ordering in output
	// Checkpoint 3 should appear before Checkpoint 2 which should appear before Checkpoint 1
	idx3 := strings.Index(output, "Checkpoint 3")
	idx2 := strings.Index(output, "Checkpoint 2")
	idx1 := strings.Index(output, "Checkpoint 1")

	if idx3 == -1 || idx2 == -1 || idx1 == -1 {
		t.Fatalf("expected all checkpoints to be in output, got:\n%s", output)
	}

	// In the output, they should appear in the order they're in the slice (newest first)
	if idx3 > idx2 || idx2 > idx1 {
		t.Errorf("expected checkpoints to appear in order 3, 2, 1 in output (newest first), got positions: 3=%d, 2=%d, 1=%d", idx3, idx2, idx1)
	}

	// Verify the dates appear correctly
	if !strings.Contains(output, "Latest change") {
		t.Error("expected output to contain 'Latest change' prompt")
	}
	if !strings.Contains(output, "Initial change") {
		t.Error("expected output to contain 'Initial change' prompt")
	}
}

func TestFormatSessionInfo_EmptyCheckpoints(t *testing.T) {
	now := time.Now()
	session := &strategy.Session{
		ID:          "2025-12-09-empty-session",
		Strategy:    "manual-commit",
		StartTime:   now,
		Checkpoints: []strategy.Checkpoint{},
	}

	output := formatSessionInfo(session, "", nil)

	if !strings.Contains(output, "Checkpoints: 0") {
		t.Errorf("expected output to contain 'Checkpoints: 0', got:\n%s", output)
	}
}

func TestFormatSessionInfo_CheckpointWithTaskMarker(t *testing.T) {
	now := time.Now()
	session := &strategy.Session{
		ID:          "2025-12-09-task-session",
		Strategy:    "manual-commit",
		StartTime:   now,
		Checkpoints: []strategy.Checkpoint{},
	}

	checkpointDetails := []checkpointDetail{
		{
			Index:            1,
			ShortID:          "abc1234",
			Timestamp:        now,
			IsTaskCheckpoint: true,
			Message:          "Task checkpoint",
			Interactions: []interaction{{
				Prompt:    "Run tests",
				Responses: []string{},
			}},
		},
	}

	output := formatSessionInfo(session, "", checkpointDetails)

	if !strings.Contains(output, "[Task]") {
		t.Errorf("expected output to contain '[Task]' marker, got:\n%s", output)
	}
}

func TestFormatSessionInfo_CheckpointWithDate(t *testing.T) {
	// Test that checkpoint headers include the full date
	timestamp := time.Date(2025, 12, 10, 14, 35, 0, 0, time.UTC)
	session := &strategy.Session{
		ID:          "2025-12-10-dated-session",
		Strategy:    "manual-commit",
		StartTime:   timestamp,
		Checkpoints: []strategy.Checkpoint{},
	}

	checkpointDetails := []checkpointDetail{
		{
			Index:     1,
			ShortID:   "abc1234",
			Timestamp: timestamp,
			Message:   "Test checkpoint",
		},
	}

	output := formatSessionInfo(session, "", checkpointDetails)

	// Should contain "2025-12-10 14:35" in the checkpoint header
	if !strings.Contains(output, "2025-12-10 14:35") {
		t.Errorf("expected output to contain date '2025-12-10 14:35', got:\n%s", output)
	}
}

func TestFormatSessionInfo_ShowsMessageWhenNoInteractions(t *testing.T) {
	// Test that checkpoints without transcript content show the commit message
	now := time.Now()
	session := &strategy.Session{
		ID:          "2025-12-12-incremental-session",
		Strategy:    "manual-commit",
		StartTime:   now,
		Checkpoints: []strategy.Checkpoint{},
	}

	// Checkpoint with message but no interactions (like incremental checkpoints)
	checkpointDetails := []checkpointDetail{
		{
			Index:            1,
			ShortID:          "abc1234",
			Timestamp:        now,
			IsTaskCheckpoint: true,
			Message:          "Starting 'dev' agent: Implement feature X (toolu_01ABC)",
			Interactions:     []interaction{}, // Empty - no transcript available
		},
	}

	output := formatSessionInfo(session, "", checkpointDetails)

	// Should show the commit message when there are no interactions
	if !strings.Contains(output, "Starting 'dev' agent: Implement feature X (toolu_01ABC)") {
		t.Errorf("expected output to contain commit message when no interactions, got:\n%s", output)
	}

	// Should NOT show "## Prompt" or "## Responses" sections since there are no interactions
	if strings.Contains(output, "## Prompt") {
		t.Errorf("expected output to NOT contain '## Prompt' when no interactions, got:\n%s", output)
	}
	if strings.Contains(output, "## Responses") {
		t.Errorf("expected output to NOT contain '## Responses' when no interactions, got:\n%s", output)
	}
}
