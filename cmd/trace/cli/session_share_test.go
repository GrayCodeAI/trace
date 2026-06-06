package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/session"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/testutil"
)

// writeSampleSession persists a session whose transcript contains one user and
// one assistant message, returning the session ID.
func writeSampleSession(t *testing.T, dir string) string {
	t.Helper()
	transcriptPath := filepath.Join(dir, "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(sampleTranscript), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	const sessionID = "export-sample"
	state := &strategy.SessionState{
		SessionID:      sessionID,
		WorktreePath:   dir,
		StartedAt:      time.Now().UTC().Add(-time.Hour),
		Phase:          session.PhaseIdle,
		TranscriptPath: transcriptPath,
	}
	if err := strategy.SaveSessionState(context.Background(), state); err != nil {
		t.Fatalf("SaveSessionState: %v", err)
	}
	return sessionID
}

func runExportCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newSessionExportCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SilenceErrors = true
	cmd.SetContext(context.Background())
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), err
}

func TestSessionExport_AsciinemaWritesValidCast(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)
	sessionID := writeSampleSession(t, dir)

	out := filepath.Join(dir, "out.cast")
	if _, err := runExportCmd(t, sessionID, "--format", "asciinema", "-o", out); err != nil {
		t.Fatalf("export asciinema: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read cast: %v", err)
	}
	// First line must be a valid v2 header with at least one event following.
	nl := bytes.IndexByte(data, '\n')
	if nl < 0 {
		t.Fatal("cast has no newline; missing header/events")
	}
	var header asciinemaHeader
	if err := json.Unmarshal(data[:nl], &header); err != nil {
		t.Fatalf("invalid header: %v", err)
	}
	if header.Version != 2 {
		t.Errorf("version = %d, want 2", header.Version)
	}
	if len(bytes.TrimSpace(data[nl+1:])) == 0 {
		t.Error("expected at least one event line after header")
	}
}

func TestSessionExport_DefaultJSONStillWorks(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)
	sessionID := writeSampleSession(t, dir)

	out := filepath.Join(dir, "out.json")
	if _, err := runExportCmd(t, sessionID, "-o", out); err != nil {
		t.Fatalf("export json: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	var export sessionExport
	if err := json.Unmarshal(data, &export); err != nil {
		t.Fatalf("invalid json export: %v", err)
	}
	if export.SessionID != sessionID {
		t.Errorf("session_id = %q, want %q", export.SessionID, sessionID)
	}
	if export.Version != 1 {
		t.Errorf("version = %d, want 1", export.Version)
	}
}

func TestSessionExport_UnknownFormatErrors(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)
	sessionID := writeSampleSession(t, dir)

	if _, err := runExportCmd(t, sessionID, "--format", "bogus"); err == nil {
		t.Fatal("expected error for unsupported format")
	}
}
