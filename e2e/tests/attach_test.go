//go:build e2e

package tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/e2e/agents"
	"github.com/GrayCodeAI/trace/e2e/testutil"
	"github.com/GrayCodeAI/trace/e2e/trace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAttachSessionCreatesCheckpoint(t *testing.T) {
	testutil.ForEachNamedAgent(t, 2*time.Minute, []string{"vogon"}, func(t *testing.T, s *testutil.RepoState, ctx context.Context) {
		homeDir := t.TempDir()
		extraEnv := []string{"HOME=" + homeDir}
		vogon := requireVogonAgent(t, s)

		mainBranch := testutil.GitOutput(t, s.Dir, "branch", "--show-current")
		s.Git(t, "add", ".")
		s.Git(t, "commit", "-m", "Enable trace")
		s.Git(t, "checkout", "-b", "feature")

		sessionID := "attach-vogon-session"
		_, writeErr := vogon.WriteSessionTranscript(ctx, s.Dir, extraEnv, sessionID, "explain the feature module", "The feature module organizes related work.")
		require.NoError(t, writeErr, "prepare vogon session")

		out, err := trace.AttachWithEnv(s.Dir, extraEnv, sessionID, s.Agent.TraceAgent())
		require.NoError(t, err, "trace attach failed: %s", out)

		assert.Contains(t, out, "Attached session "+sessionID)
		assert.Contains(t, out, "Created checkpoint")
		checkpointID := testutil.AssertHasCheckpointTrailer(t, s.Dir, "HEAD")

		transcriptPath := filepath.Join(homeDir, ".vogon", "sessions", sessionID+".jsonl")
		require.NoError(t, os.Remove(transcriptPath), "remove prepared vogon session before resume")

		resumeOut, resumeErr := trace.ResumeWithEnv(s.Dir, "feature", extraEnv)
		require.NoError(t, resumeErr, "trace resume failed after attach: %s", resumeOut)
		assert.Contains(t, resumeOut, sessionID, "resume output should reference the attached session")
		assert.Contains(t, resumeOut, "To continue", "resume output should show follow-up instructions")
		_, statErr := os.Stat(transcriptPath)
		assert.NoError(t, statErr, "resume should restore the transcript into the isolated vogon HOME")

		s.Git(t, "checkout", mainBranch)
		explainOut := trace.Explain(t, s.Dir, checkpointID)
		assert.Contains(t, explainOut, "● Checkpoint "+checkpointID)
	})
}

func TestAttachSessionAddsToExistingCheckpoint(t *testing.T) {
	testutil.ForEachNamedAgent(t, 3*time.Minute, []string{"vogon"}, func(t *testing.T, s *testutil.RepoState, ctx context.Context) {
		homeDir := t.TempDir()
		extraEnv := []string{"HOME=" + homeDir}
		vogon := requireVogonAgent(t, s)

		s.Git(t, "add", ".")
		s.Git(t, "commit", "-m", "Enable trace")

		_, err := s.RunPrompt(t, ctx,
			"create a file at docs/existing.md with a short paragraph about existing checkpoints. Do not ask for confirmation or approval, just make the change.")
		require.NoError(t, err, "agent failed")

		checkpointBefore := ""
		if _, refErr := testutil.GitOutputErr(s.Dir, "rev-parse", "--verify", testutil.CheckpointVerifyRef()); refErr == nil {
			checkpointBefore = testutil.CurrentCheckpointRef(t, s.Dir)
		}

		s.Git(t, "add", ".")
		s.Git(t, "commit", "-m", "Add existing checkpoint doc")
		testutil.WaitForCheckpointAdvanceFrom(t, s.Dir, checkpointBefore, 30*time.Second)

		checkpointID := testutil.AssertHasCheckpointTrailer(t, s.Dir, "HEAD")

		sessionID := "attach-second-vogon-session"
		_, writeErr := vogon.WriteSessionTranscript(ctx, s.Dir, extraEnv, sessionID, "summarize the checkpoint flow", "The checkpoint flow stores the session on the existing checkpoint.")
		require.NoError(t, writeErr, "prepare vogon session")

		out, attachErr := trace.AttachWithEnv(s.Dir, extraEnv, sessionID, s.Agent.TraceAgent())
		require.NoError(t, attachErr, "trace attach failed: %s", out)

		assert.Contains(t, out, "Attached session "+sessionID)
		assert.Contains(t, out, "Added to existing checkpoint "+checkpointID)
		assert.Equal(t, checkpointID, testutil.AssertHasCheckpointTrailer(t, s.Dir, "HEAD"),
			"attach should reuse the existing checkpoint trailer")

		checkpointMeta := testutil.ReadCheckpointMetadata(t, s.Dir, checkpointID)
		assert.Len(t, checkpointMeta.Sessions, 2, "attach should append a second session to checkpoint metadata")
		attachedSessionMeta := testutil.ReadSessionMetadata(t, s.Dir, checkpointID, 1)
		assert.Equal(t, sessionID, attachedSessionMeta.SessionID, "attach should persist the attached session metadata")
	})
}

func requireVogonAgent(t *testing.T, s *testutil.RepoState) *agents.Vogon {
	t.Helper()

	vogon, ok := s.Agent.(*agents.Vogon)
	require.True(t, ok, "expected Vogon agent")
	return vogon
}
