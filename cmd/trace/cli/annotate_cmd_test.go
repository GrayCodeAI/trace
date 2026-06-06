package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/session"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/testutil"
	"github.com/spf13/cobra"
)

// Compile-time guard against an accidental return-type change.
var _ *cobra.Command = newAnnotateCmd()

func runAnnotateCmd(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newAnnotateCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SilenceErrors = true
	cmd.SetContext(context.Background())
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func TestAnnotate_WriteAndListRoundTrip(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)

	const sessionID = "annotate-roundtrip"
	state := &strategy.SessionState{
		SessionID:    sessionID,
		WorktreePath: dir,
		StartedAt:    time.Now().UTC().Add(-time.Hour),
		Phase:        session.PhaseIdle,
	}
	if err := strategy.SaveSessionState(context.Background(), state); err != nil {
		t.Fatalf("SaveSessionState: %v", err)
	}

	// Write a session-level annotation.
	if _, _, err := runAnnotateCmd(t, sessionID, "--comment", "first note"); err != nil {
		t.Fatalf("annotate write: %v", err)
	}
	// Write a checkpoint-scoped annotation.
	if _, _, err := runAnnotateCmd(t, sessionID, "--checkpoint", "cp-123", "--comment", "broke build"); err != nil {
		t.Fatalf("annotate checkpoint write: %v", err)
	}

	// Verify persistence in session state.
	reloaded, err := strategy.LoadSessionState(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("LoadSessionState: %v", err)
	}
	if reloaded == nil {
		t.Fatal("session state nil after annotate")
	}
	if len(reloaded.Annotations) != 2 {
		t.Fatalf("annotations = %d, want 2", len(reloaded.Annotations))
	}
	if reloaded.Annotations[0].Comment != "first note" {
		t.Errorf("first comment = %q, want %q", reloaded.Annotations[0].Comment, "first note")
	}
	if reloaded.Annotations[1].CheckpointID != "cp-123" {
		t.Errorf("checkpoint id = %q, want %q", reloaded.Annotations[1].CheckpointID, "cp-123")
	}
	if reloaded.Annotations[0].Author == "" {
		t.Errorf("expected author to be populated from git config")
	}

	// List and verify both annotations are rendered.
	stdout, _, err := runAnnotateCmd(t, sessionID, "--list")
	if err != nil {
		t.Fatalf("annotate list: %v", err)
	}
	if !strings.Contains(stdout, "first note") {
		t.Errorf("list output missing 'first note': %q", stdout)
	}
	if !strings.Contains(stdout, "broke build") {
		t.Errorf("list output missing 'broke build': %q", stdout)
	}
	if !strings.Contains(stdout, "cp-123") {
		t.Errorf("list output missing checkpoint id: %q", stdout)
	}
}

func TestAnnotate_RequiresCommentOrList(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)

	state := &strategy.SessionState{SessionID: "annotate-no-comment", WorktreePath: dir, StartedAt: time.Now().UTC()}
	if err := strategy.SaveSessionState(context.Background(), state); err != nil {
		t.Fatalf("SaveSessionState: %v", err)
	}

	if _, _, err := runAnnotateCmd(t, "annotate-no-comment"); err == nil {
		t.Fatal("expected error when neither --comment nor --list is given")
	}
}

func TestAnnotate_SessionNotFound(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)

	if _, _, err := runAnnotateCmd(t, "does-not-exist", "--comment", "x"); err == nil {
		t.Fatal("expected error for unknown session")
	}
}

func TestAnnotate_ListEmpty(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)

	state := &strategy.SessionState{SessionID: "annotate-empty", WorktreePath: dir, StartedAt: time.Now().UTC()}
	if err := strategy.SaveSessionState(context.Background(), state); err != nil {
		t.Fatalf("SaveSessionState: %v", err)
	}

	stdout, _, err := runAnnotateCmd(t, "annotate-empty", "--list")
	if err != nil {
		t.Fatalf("annotate list: %v", err)
	}
	if !strings.Contains(stdout, "No annotations") {
		t.Errorf("expected 'No annotations' message, got: %q", stdout)
	}
}
