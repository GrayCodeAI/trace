package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/paths"
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

	got := maybeCompactExternalTranscript(ctx, []byte("not-json"), kind)
	if strings.Contains(string(got), secret) {
		t.Fatalf("external compact transcript was not redacted: %s", got)
	}
	if !strings.Contains(string(got), redact.RedactedPlaceholder) {
		t.Fatalf("expected redacted compact transcript, got: %s", got)
	}
}

func TestExplainCommit_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Initialize git repo
	testutil.InitRepo(t, tmpDir)

	var stdout bytes.Buffer
	err := runExplainCommit(context.Background(), &stdout, &stdout, "nonexistent", false, false, false, false, false, false, false, 0)

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
	err = runExplainCommit(context.Background(), &stdout, &stdout, commitHash.String(), false, false, false, false, false, false, false, 0)
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
	err = runExplainCommit(context.Background(), &stdout, &stdout, commitHash.String(), false, false, false, false, false, false, false, 0)
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
	err = runExplainBranchWithFilter(context.Background(), &stdout, &stdout, true, "") // noPager=true for test
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
	err = runExplainBranchWithFilter(context.Background(), &stdout, &stdout, true, "") // noPager=true for test
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
	err := runExplain(context.Background(), &stdout, &stderr, "session-id", "commit-sha", "", "", false, false, false, false, false, false, false, 0)

	if err == nil {
		t.Error("expected error when both flags provided, got nil")
	}
	// Case-insensitive check for "cannot specify multiple"
	errLower := strings.ToLower(err.Error())
	if !strings.Contains(errLower, "cannot specify multiple") {
		t.Errorf("expected 'cannot specify multiple' in error, got: %v", err)
	}
}
