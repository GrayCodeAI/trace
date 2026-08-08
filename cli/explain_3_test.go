package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
)

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
	err := runExplain(context.Background(), &buf, &errBuf, "session-id", "", "checkpoint-id", "", false, false, false, false, false, false, false, 0)

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
	err = runExplainCheckpoint(context.Background(), &buf, &errBuf, "nonexistent123", false, false, false, false, false, false, false, 0)

	if err == nil {
		t.Error("expected error for nonexistent checkpoint")
	}
	if !strings.Contains(err.Error(), "checkpoint not found") {
		t.Errorf("expected 'checkpoint not found' error, got: %v", err)
	}
}
