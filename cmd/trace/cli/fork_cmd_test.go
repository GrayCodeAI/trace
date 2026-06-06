package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/session"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/testutil"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/trailers"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// setupForkRepo creates a git repo with an initial commit and chdirs into it.
// It mirrors setupExportRepo but seeds v1 committed checkpoints (the default
// store path) and clears the git-common-dir cache so session state lands in
// this repo's .git.
func setupForkRepo(t *testing.T) (string, *git.Repository) {
	t.Helper()
	tmpDir := t.TempDir()
	testutil.InitRepo(t, tmpDir)
	t.Chdir(tmpDir)
	session.ClearGitCommonDirCache()
	t.Cleanup(session.ClearGitCommonDirCache)

	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte("init"), 0o600))
	_, err = wt.Add("f.txt")
	require.NoError(t, err)
	_, err = wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@trace.local", When: time.Now()},
	})
	require.NoError(t, err)

	return tmpDir, repo
}

// seedForkCheckpoint writes a v1 committed checkpoint and a code commit that
// carries its Trace-Checkpoint trailer, so the fork can resolve a base commit
// to branch. Returns the checkpoint ID.
func seedForkCheckpoint(t *testing.T, repo *git.Repository, cpID id.CheckpointID, sessionID string) {
	t.Helper()

	store := checkpoint.NewGitStore(repo)
	require.NoError(t, store.WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    sessionID,
		Strategy:     "manual-commit",
		Agent:        agent.AgentTypeClaudeCode,
		Model:        "claude-sonnet-4-20250514",
		Transcript:   redact.AlreadyRedacted([]byte(`{"type":"user","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n")),
		TokenUsage:   &agent.TokenUsage{InputTokens: 100, OutputTokens: 50, APICallCount: 3},
		AuthorName:   "Test",
		AuthorEmail:  "test@trace.local",
	}))

	// Code commit on the current branch carrying the checkpoint trailer.
	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile("work.txt", []byte("agent work"), 0o600))
	_, err = wt.Add("work.txt")
	require.NoError(t, err)
	msg := fmt.Sprintf("feat: agent work\n\n%s: %s\n", trailers.CheckpointTrailerKey, cpID)
	_, err = wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@trace.local", When: time.Now()},
	})
	require.NoError(t, err)
}

// TestRunFork_DistinctSessionWithCopiedMetadata is the core acceptance test:
// forking a committed checkpoint yields a new, distinct session ID whose state
// derives from the source (agent, model, token-usage baseline, provenance).
func TestRunFork_DistinctSessionWithCopiedMetadata(t *testing.T) {
	_, repo := setupForkRepo(t)

	cpID := id.MustCheckpointID("aaaa11112222")
	const srcSession = "source-session-1"
	seedForkCheckpoint(t, repo, cpID, srcSession)

	var out bytes.Buffer
	require.NoError(t, runFork(context.Background(), &out, cpID.String()))

	// The freshly-allocated session must be distinct from the source.
	stateStore, err := session.NewStateStore(context.Background())
	require.NoError(t, err)
	states, err := stateStore.List(context.Background())
	require.NoError(t, err)

	var forkState *session.State
	for _, s := range states {
		if s.SessionID != srcSession {
			forkState = s
		}
	}
	require.NotNil(t, forkState, "expected a new fork session state to be written")
	require.NotEqual(t, srcSession, forkState.SessionID)
	require.Contains(t, forkState.SessionID, "fork-")

	// Metadata is copied from the source checkpoint.
	require.Equal(t, agent.AgentTypeClaudeCode, forkState.AgentType)
	require.Equal(t, "claude-sonnet-4-20250514", forkState.ModelName)
	require.NotNil(t, forkState.TokenUsage)
	require.Equal(t, 100, forkState.TokenUsage.InputTokens)
	require.Equal(t, 50, forkState.TokenUsage.OutputTokens)

	// Provenance links the fork back to its origin.
	require.Equal(t, cpID.String(), forkState.Metadata["forked_from_checkpoint"])
	require.Equal(t, srcSession, forkState.Metadata["forked_from_session"])

	// A fork branch was created pointing at the resolved code commit.
	require.NotEmpty(t, forkState.BaseCommit)
	require.True(t, testutil.BranchExists(t, ".", "trace/fork/"+shortForkID(forkState.SessionID)))

	// Output tells the user how to resume.
	require.Contains(t, out.String(), forkState.SessionID)
	require.Contains(t, out.String(), "trace session resume")
}

// TestRunFork_PrefixResolution verifies a hex prefix resolves to the single
// matching checkpoint.
func TestRunFork_PrefixResolution(t *testing.T) {
	_, repo := setupForkRepo(t)

	cpID := id.MustCheckpointID("bbbb33334444")
	seedForkCheckpoint(t, repo, cpID, "source-session-2")

	var out bytes.Buffer
	require.NoError(t, runFork(context.Background(), &out, "bbbb3333"))
	require.Contains(t, out.String(), cpID.String())
}

// TestRunFork_UnknownCheckpoint returns a clear not-found error.
func TestRunFork_UnknownCheckpoint(t *testing.T) {
	setupForkRepo(t)

	var out bytes.Buffer
	err := runFork(context.Background(), &out, "ffffffffffff")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

// TestForkResultDistinctIDs guards against the fork session ID generator
// returning duplicates.
func TestForkResultDistinctIDs(t *testing.T) {
	a := generateForkSessionID()
	b := generateForkSessionID()
	require.NotEqual(t, a, b)
	require.Contains(t, a, "fork-")
}
