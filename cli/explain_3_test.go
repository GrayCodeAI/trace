package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"
)

func TestFormatSessionInfo_ShowsMessageAndFilesWhenNoInteractions(t *testing.T) {
	// Test that checkpoints without transcript but with files show both message and files
	now := time.Now()
	session := &strategy.Session{
		ID:          "2025-12-12-incremental-with-files",
		Strategy:    "manual-commit",
		StartTime:   now,
		Checkpoints: []strategy.Checkpoint{},
	}

	checkpointDetails := []checkpointDetail{
		{
			Index:            1,
			ShortID:          "def5678",
			Timestamp:        now,
			IsTaskCheckpoint: true,
			Message:          "Running tests for API endpoint (toolu_02DEF)",
			Interactions:     []interaction{}, // Empty - no transcript
			Files:            []string{"api/endpoint.go", "api/endpoint_test.go"},
		},
	}

	output := formatSessionInfo(session, "", checkpointDetails)

	// Should show the commit message
	if !strings.Contains(output, "Running tests for API endpoint (toolu_02DEF)") {
		t.Errorf("expected output to contain commit message, got:\n%s", output)
	}

	// Should also show the files
	if !strings.Contains(output, "Files Modified") {
		t.Errorf("expected output to contain 'Files Modified', got:\n%s", output)
	}
	if !strings.Contains(output, "api/endpoint.go") {
		t.Errorf("expected output to contain modified file, got:\n%s", output)
	}
}

func TestFormatSessionInfo_DoesNotShowMessageWhenHasInteractions(t *testing.T) {
	// Test that checkpoints WITH interactions don't show the message separately
	// (the interactions already contain the content)
	now := time.Now()
	session := &strategy.Session{
		ID:          "2025-12-12-full-checkpoint",
		Strategy:    "manual-commit",
		StartTime:   now,
		Checkpoints: []strategy.Checkpoint{},
	}

	checkpointDetails := []checkpointDetail{
		{
			Index:            1,
			ShortID:          "ghi9012",
			Timestamp:        now,
			IsTaskCheckpoint: true,
			Message:          "Completed 'dev' agent: Implement feature (toolu_03GHI)",
			Interactions: []interaction{
				{
					Prompt:    "Implement the feature",
					Responses: []string{"I've implemented the feature by..."},
					Files:     []string{"feature.go"},
				},
			},
		},
	}

	output := formatSessionInfo(session, "", checkpointDetails)

	// Should show the interaction content
	if !strings.Contains(output, "Implement the feature") {
		t.Errorf("expected output to contain prompt, got:\n%s", output)
	}
	if !strings.Contains(output, "I've implemented the feature by") {
		t.Errorf("expected output to contain response, got:\n%s", output)
	}

	// The message should NOT appear as a separate line (it's redundant when we have interactions)
	// The output should contain ## Prompt and ## Responses for the interaction
	if !strings.Contains(output, "## Prompt") {
		t.Errorf("expected output to contain '## Prompt' when has interactions, got:\n%s", output)
	}
}

func TestExplainCmd_HasCheckpointFlag(t *testing.T) {
	cmd := newExplainCmd()

	flag := cmd.Flags().Lookup("checkpoint")
	if flag == nil {
		t.Error("expected --checkpoint flag to exist")
	}
}

func TestExplainCmd_HasShortFlag(t *testing.T) {
	cmd := newExplainCmd()

	flag := cmd.Flags().Lookup("short")
	if flag == nil {
		t.Fatal("expected --short flag to exist")
		return // unreachable but satisfies staticcheck
	}

	// Should have -s shorthand
	if flag.Shorthand != "s" {
		t.Errorf("expected -s shorthand, got %q", flag.Shorthand)
	}
}

func TestExplainCmd_HasFullFlag(t *testing.T) {
	cmd := newExplainCmd()

	flag := cmd.Flags().Lookup("full")
	if flag == nil {
		t.Error("expected --full flag to exist")
	}
}

func TestExplainCmd_HasRawTranscriptFlag(t *testing.T) {
	cmd := newExplainCmd()

	flag := cmd.Flags().Lookup("raw-transcript")
	if flag == nil {
		t.Error("expected --raw-transcript flag to exist")
	}
}

func TestRunExplain_MutualExclusivityError(t *testing.T) {
	var buf, errBuf bytes.Buffer

	// Providing both --session and --checkpoint should error
	err := runExplain(context.Background(), &buf, &errBuf, "session-id", "", "checkpoint-id", "", false, false, false, false, false, false, false)

	if err == nil {
		t.Error("expected error when multiple flags provided")
	}
	if !strings.Contains(err.Error(), "cannot specify multiple") {
		t.Errorf("expected 'cannot specify multiple' error, got: %v", err)
	}
}

func TestRunExplainCheckpoint_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Initialize git repo with an initial commit (required for checkpoint lookup)
	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

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
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	var buf, errBuf bytes.Buffer
	err = runExplainCheckpoint(context.Background(), &buf, &errBuf, "nonexistent123", false, false, false, false, false, false, false)

	if err == nil {
		t.Error("expected error for nonexistent checkpoint")
	}
	if !strings.Contains(err.Error(), "checkpoint not found") {
		t.Errorf("expected 'checkpoint not found' error, got: %v", err)
	}
}

func TestRunExplainCheckpoint_V2OnlyCheckpoint(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	if err != nil {
		t.Fatalf("failed to open git repo: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := wt.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	_, err = wt.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(tmpDir, ".trace"), 0o755); err != nil {
		t.Fatalf("failed to create .trace directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".trace", "settings.json"), []byte(`{"enabled": true, "strategy_options": {"checkpoints_v2": true}}`), 0o644); err != nil {
		t.Fatalf("failed to write settings: %v", err)
	}

	v2Store := checkpoint.NewV2GitStore(repo, "origin")
	cpID := id.MustCheckpointID("777777777777")
	ctx := context.Background()

	if err := v2Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-v2",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte(`{"type":"user","message":{"content":[{"type":"text","text":"hello from v2"}]}}` + "\n")),
		AuthorName:   "Test",
		AuthorEmail:  "test@example.com",
	}); err != nil {
		t.Fatalf("failed to write v2 checkpoint: %v", err)
	}

	var buf, errBuf bytes.Buffer
	err = runExplainCheckpoint(context.Background(), &buf, &errBuf, "777777", false, false, false, false, false, false, false)
	if err != nil {
		t.Fatalf("expected success for v2-only checkpoint, got error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "● Checkpoint 777777777777") {
		t.Fatalf("expected checkpoint header in output, got: %s", output)
	}
	if !strings.Contains(output, "session-v2") {
		t.Fatalf("expected v2 session ID in output, got: %s", output)
	}
}

func TestRunExplainCheckpoint_V2OnlyRawTranscript(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	if err != nil {
		t.Fatalf("failed to open git repo: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := wt.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	_, err = wt.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(tmpDir, ".trace"), 0o755); err != nil {
		t.Fatalf("failed to create .trace directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".trace", "settings.json"), []byte(`{"enabled": true, "strategy_options": {"checkpoints_v2": true}}`), 0o644); err != nil {
		t.Fatalf("failed to write settings: %v", err)
	}

	v2Store := checkpoint.NewV2GitStore(repo, "origin")
	cpID := id.MustCheckpointID("888888888888")
	ctx := context.Background()

	if err := v2Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-v2",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte(`{"type":"user","message":{"content":[{"type":"text","text":"raw from v2"}]}}` + "\n")),
		AuthorName:   "Test",
		AuthorEmail:  "test@example.com",
	}); err != nil {
		t.Fatalf("failed to write v2 checkpoint: %v", err)
	}

	var buf, errBuf bytes.Buffer
	err = runExplainCheckpoint(context.Background(), &buf, &errBuf, "888888", false, false, false, true, false, false, false)
	if err != nil {
		t.Fatalf("expected success for v2-only raw transcript, got error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "raw from v2") {
		t.Fatalf("expected v2 raw transcript in output, got: %s", output)
	}
}

func TestRunExplainCheckpoint_V2CheckpointRemoteFallbackResolvesRawTranscript(t *testing.T) {
	ctx := context.Background()

	emptyConfig := filepath.Join(t.TempDir(), "empty-git-config")
	require.NoError(t, os.WriteFile(emptyConfig, []byte(""), 0o644))
	t.Setenv("GIT_CONFIG_GLOBAL", emptyConfig)
	t.Setenv("GIT_CONFIG_SYSTEM", emptyConfig)

	checkpointDir := t.TempDir()
	testutil.InitRepo(t, checkpointDir)
	testutil.WriteFile(t, checkpointDir, "checkpoint.txt", "checkpoint")
	testutil.GitAdd(t, checkpointDir, "checkpoint.txt")
	testutil.GitCommit(t, checkpointDir, "checkpoint init")

	checkpointRepo, err := git.PlainOpen(checkpointDir)
	require.NoError(t, err)
	t.Cleanup(func() {
		// Close the underlying storage to release file descriptors before
		// t.TempDir() attempts to remove the directory.
		if storer, ok := checkpointRepo.Storer.(interface{ Close() error }); ok {
			_ = storer.Close()
		}
	})

	cpID := id.MustCheckpointID("121212121212")
	rawTranscript := []byte(`{"type":"user","message":{"content":[{"type":"text","text":"raw from checkpoint_remote"}]}}` + "\n")
	checkpointStore := checkpoint.NewV2GitStore(checkpointRepo, "origin")
	require.NoError(t, checkpointStore.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-checkpoint-remote",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted(rawTranscript),
		AuthorName:   "Test",
		AuthorEmail:  "test@example.com",
	}))

	localDir := t.TempDir()
	t.Chdir(localDir)

	testutil.InitRepo(t, localDir)
	testutil.WriteFile(t, localDir, "local.txt", "local")
	testutil.GitAdd(t, localDir, "local.txt")
	testutil.GitCommit(t, localDir, "local init")

	cmd := exec.CommandContext(ctx, "git", "remote", "add", "origin", "git@github.com:user/source.git")
	cmd.Dir = localDir
	cmd.Env = testutil.GitIsolatedEnv()
	require.NoError(t, cmd.Run())

	sshScript := filepath.Join(t.TempDir(), "fake-ssh")
	require.NoError(t, os.WriteFile(sshScript, []byte(`#!/bin/bash
set -euo pipefail
cmd="${@: -1}"
case "$cmd" in
  *"user/source.git"*)
    echo "origin intentionally unavailable" >&2
    exit 1
    ;;
  *"org/checkpoints.git"*) repo="$CHECKPOINT_REPO" ;;
  *)
    echo "unexpected ssh command: $cmd" >&2
    exit 1
    ;;
esac
exec git-upload-pack "$repo"
`), 0o755))
	t.Setenv("GIT_SSH", sshScript)
	t.Setenv("GIT_SSH_COMMAND", sshScript) // GIT_SSH_COMMAND takes priority over GIT_SSH on systems where it's set globally.
	t.Setenv("CHECKPOINT_REPO", checkpointDir)

	require.NoError(t, os.MkdirAll(filepath.Join(localDir, ".trace"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(localDir, ".trace", "settings.json"),
		[]byte(`{"enabled": true, "strategy_options": {"checkpoints_v2": true, "checkpoint_remote": {"provider": "github", "repo": "org/checkpoints"}}}`),
		0o644,
	))

	var buf, errBuf bytes.Buffer
	err = runExplainCheckpoint(ctx, &buf, &errBuf, "121212", false, false, false, true, false, false, false)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "raw from checkpoint_remote")
}

func TestRunExplainCheckpoint_V2UsesCompactTranscriptForIntent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	if err != nil {
		t.Fatalf("failed to open git repo: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	if _, err := wt.Add("test.txt"); err != nil {
		t.Fatalf("failed to add test file: %v", err)
	}
	_, err = wt.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(tmpDir, ".trace"), 0o755); err != nil {
		t.Fatalf("failed to create .trace directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".trace", "settings.json"), []byte(`{"enabled": true, "strategy_options": {"checkpoints_v2": true}}`), 0o644); err != nil {
		t.Fatalf("failed to write settings: %v", err)
	}

	v2Store := checkpoint.NewV2GitStore(repo, "origin")
	cpID := id.MustCheckpointID("999999999999")
	ctx := context.Background()

	compactTranscript := []byte(
		`{"v":1,"agent":"claude-code","cli_version":"0.5.1","type":"user","ts":"2026-01-01T00:00:00Z","content":[{"text":"compact prompt text"}]}` + "\n" +
			`{"v":1,"agent":"claude-code","cli_version":"0.5.1","type":"assistant","ts":"2026-01-01T00:00:01Z","id":"m1","content":[{"type":"text","text":"assistant reply"}]}` + "\n",
	)

	if err := v2Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID:              cpID,
		SessionID:                 "session-v2",
		Strategy:                  "manual-commit",
		Transcript:                redact.AlreadyRedacted([]byte(`{"type":"user","message":{"content":[{"type":"text","text":"raw prompt text"}]}}` + "\n")),
		CompactTranscript:         compactTranscript,
		AuthorName:                "Test",
		AuthorEmail:               "test@example.com",
		CheckpointTranscriptStart: 0,
	}); err != nil {
		t.Fatalf("failed to write v2 checkpoint: %v", err)
	}

	var buf, errBuf bytes.Buffer
	err = runExplainCheckpoint(context.Background(), &buf, &errBuf, "999999", false, false, false, false, false, false, false)
	if err != nil {
		t.Fatalf("expected success for v2 checkpoint, got error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "## Intent") {
		t.Fatalf("expected '## Intent' heading in no-color output, got: %s", output)
	}
	if !strings.Contains(output, "compact prompt text") {
		t.Fatalf("expected compact transcript to drive intent extraction, got: %s", output)
	}
}

func TestRunExplainCheckpoint_V2PreferredGenerateWritesBothStores(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("test"), 0o644))
	_, err = wt.Add("test.txt")
	require.NoError(t, err)
	_, err = wt.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".trace"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".trace", "settings.json"),
		[]byte(`{"enabled": true, "strategy_options": {"checkpoints_v2": true}}`),
		0o644,
	))

	v1Store := checkpoint.NewGitStore(repo)
	v2Store := checkpoint.NewV2GitStore(repo, "origin")
	cpID := id.MustCheckpointID("aabbccddeeff")
	ctx := context.Background()

	transcript := []byte(`{"type":"user","message":{"content":[{"type":"text","text":"generate test"}]}}` + "\n" +
		`{"type":"assistant","message":{"content":"done"}}` + "\n")

	// Dual-write: checkpoint exists in both v1 and v2.
	require.NoError(t, v1Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-dual",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted(transcript),
		AuthorName:   "Test",
		AuthorEmail:  "test@example.com",
	}))
	require.NoError(t, v2Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-dual",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted(transcript),
		AuthorName:   "Test",
		AuthorEmail:  "test@example.com",
	}))

	// generate=true, force=true — should succeed by writing to both v1 and v2 stores.
	var buf, errBuf bytes.Buffer
	err = runExplainCheckpoint(ctx, &buf, &errBuf, "aabbcc", false, false, false, false, true, true, false)
	// Generation requires an AI summarizer which isn't available in unit tests,
	// but the important thing is we don't get the old "only v1 checkpoints supported" error.
	if err != nil && strings.Contains(err.Error(), "summary updates are currently supported only for v1 checkpoints") {
		t.Fatalf("should not reject v2-resolved checkpoints for generation when v1 has the data: %v", err)
	}
}

func TestRunExplainCheckpoint_V2OnlyGenerateSucceedsViaV2Store(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("test"), 0o644))
	_, err = wt.Add("test.txt")
	require.NoError(t, err)
	_, err = wt.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".trace"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".trace", "settings.json"),
		[]byte(`{"enabled": true, "strategy_options": {"checkpoints_v2": true}}`),
		0o644,
	))

	v2Store := checkpoint.NewV2GitStore(repo, "origin")
	cpID := id.MustCheckpointID("f1f2f3f4f5f6")
	ctx := context.Background()

	transcript := []byte(`{"type":"user","message":{"content":[{"type":"text","text":"v2-only generate"}]}}` + "\n" +
		`{"type":"assistant","message":{"content":"done"}}` + "\n")

	// Write to v2 only — no v1 checkpoint exists.
	require.NoError(t, v2Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-v2-only",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted(transcript),
		AuthorName:   "Test",
		AuthorEmail:  "test@example.com",
	}))

	// generate=true, force=true — should not fail with "failed to save summary"
	// because v2 store can persist even when v1 doesn't have the checkpoint.
	var buf, errBuf bytes.Buffer
	err = runExplainCheckpoint(ctx, &buf, &errBuf, "f1f2f3", false, false, false, false, true, true, false)
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "claude") || strings.Contains(errMsg, "executable file not found") {
			t.Skipf("skipping: summarizer unavailable in CI: %v", err)
		}
		require.NotContains(t, errMsg, "failed to save summary",
			"v2-only checkpoint should persist summary via v2 store")
	}
}

func TestRunExplainCheckpoint_V2FallsBackToFullWhenCompactMissing(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("test"), 0o644))
	_, err = wt.Add("test.txt")
	require.NoError(t, err)
	_, err = wt.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".trace"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".trace", "settings.json"),
		[]byte(`{"enabled": true, "strategy_options": {"checkpoints_v2": true}}`),
		0o644,
	))

	v2Store := checkpoint.NewV2GitStore(repo, "origin")
	cpID := id.MustCheckpointID("e1e2e3e4e5e6")
	ctx := context.Background()

	rawTranscript := []byte(
		`{"type":"user","message":{"content":[{"type":"text","text":"raw fallback prompt"}]}}` + "\n" +
			`{"type":"assistant","message":{"content":"raw reply"}}` + "\n",
	)

	// Write checkpoint with raw transcript but NO compact transcript.
	require.NoError(t, v2Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-no-compact",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted(rawTranscript),
		AuthorName:   "Test",
		AuthorEmail:  "test@example.com",
	}))

	// Default explain (not --full) should fall back to /full/current transcript
	// when compact transcript is missing on /main.
	var buf, errBuf bytes.Buffer
	err = runExplainCheckpoint(ctx, &buf, &errBuf, "e1e2e3", false, false, false, false, false, false, false)
	require.NoError(t, err)

	output := buf.String()
	require.Contains(t, output, "raw fallback prompt",
		"should use raw transcript from /full/current when compact is missing")
}

func TestRunExplainCheckpoint_V2CompactTranscriptNotUsedForGenerate(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("test"), 0o644))
	_, err = wt.Add("test.txt")
	require.NoError(t, err)
	_, err = wt.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, ".trace"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, ".trace", "settings.json"),
		[]byte(`{"enabled": true, "strategy_options": {"checkpoints_v2": true}}`),
		0o644,
	))

	v1Store := checkpoint.NewGitStore(repo)
	v2Store := checkpoint.NewV2GitStore(repo, "origin")
	cpID := id.MustCheckpointID("c0c1c2c3c4c5")
	ctx := context.Background()

	rawTranscript := []byte(`{"type":"user","message":{"content":[{"type":"text","text":"raw prompt for summarizer"}]}}` + "\n" +
		`{"type":"assistant","message":{"content":"raw reply"}}` + "\n")
	compactTranscript := []byte(`{"v":1,"agent":"claude-code","cli_version":"0.5.1","type":"user","content":[{"text":"compact prompt"}]}` + "\n")

	// Dual-write with compact transcript.
	require.NoError(t, v1Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-compact",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted(rawTranscript),
		AuthorName:   "Test",
		AuthorEmail:  "test@example.com",
	}))
	require.NoError(t, v2Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID:      cpID,
		SessionID:         "session-compact",
		Strategy:          "manual-commit",
		Transcript:        redact.AlreadyRedacted(rawTranscript),
		CompactTranscript: compactTranscript,
		AuthorName:        "Test",
		AuthorEmail:       "test@example.com",
	}))

	// generate=true — should NOT fail with "no transcript content" which would
	// indicate the compact transcript was incorrectly fed to the summarizer.
	var buf, errBuf bytes.Buffer
	err = runExplainCheckpoint(ctx, &buf, &errBuf, "c0c1c2", false, false, false, false, true, true, false)
	if err != nil && strings.Contains(err.Error(), "no transcript content for this checkpoint") {
		t.Fatalf("compact transcript should not be used for --generate; raw transcript should be used instead: %v", err)
	}
}

func TestListCommittedForExplain_MergesV1AndV2(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte("x"), 0o644))
	_, err = wt.Add("f.txt")
	require.NoError(t, err)
	_, err = wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "T", Email: "t@t.com", When: time.Now()},
	})
	require.NoError(t, err)

	v1Store := checkpoint.NewGitStore(repo)
	v2Store := checkpoint.NewV2GitStore(repo, "origin")
	ctx := context.Background()

	transcript := []byte(`{"type":"user","message":{"content":[{"type":"text","text":"hello"}]}}` + "\n")

	// Write a v1-only checkpoint (pre-v2 era).
	v1OnlyID := id.MustCheckpointID("aaa111222333")
	require.NoError(t, v1Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID: v1OnlyID,
		SessionID:    "session-v1-only",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted(transcript),
		AuthorName:   "T",
		AuthorEmail:  "t@t.com",
	}))

	// Write a dual-write checkpoint (exists in both v1 and v2).
	dualID := id.MustCheckpointID("bbb444555666")
	require.NoError(t, v1Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID: dualID,
		SessionID:    "session-dual",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted(transcript),
		AuthorName:   "T",
		AuthorEmail:  "t@t.com",
	}))
	require.NoError(t, v2Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID: dualID,
		SessionID:    "session-dual",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted(transcript),
		AuthorName:   "T",
		AuthorEmail:  "t@t.com",
	}))

	// With v2 preferred: should return both the dual-write AND the v1-only checkpoint.
	results, err := listCommittedForExplain(ctx, v1Store, v2Store, true)
	require.NoError(t, err)

	foundIDs := make(map[id.CheckpointID]bool)
	for _, r := range results {
		foundIDs[r.CheckpointID] = true
	}
	require.True(t, foundIDs[v1OnlyID], "v1-only checkpoint should be visible when v2 is preferred")
	require.True(t, foundIDs[dualID], "dual-write checkpoint should be visible")

	// No duplicates: dual checkpoint should appear exactly once.
	dualCount := 0
	for _, r := range results {
		if r.CheckpointID == dualID {
			dualCount++
		}
	}
	require.Equal(t, 1, dualCount, "dual-write checkpoint should not be duplicated")
}
