package strategy

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/GrayCodeAI/trace/redact"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFetchAndRebase_URLTarget_ReconcilesFetchedTempRef verifies that URL
// targets reconcile against the temporary fetched ref instead of any origin
// tracking state.
//
// Not parallel: uses t.Chdir() (required for OpenRepository).
func TestFetchAndRebase_URLTarget_ReconcilesFetchedTempRef(t *testing.T) {
	ctx := context.Background()
	branchName := paths.MetadataBranchName

	bareDir := t.TempDir()
	setupDir := t.TempDir()
	gitRun := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = testutil.GitIsolatedEnv()
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v in %s failed: %s", args, dir, out)
	}

	gitRun(bareDir, "init", "--bare", "-b", "main")
	gitRun(setupDir, "clone", bareDir, ".")
	gitRun(setupDir, "config", "user.email", "test@test.com")
	gitRun(setupDir, "config", "user.name", "Test User")
	gitRun(setupDir, "config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(setupDir, "README.md"), []byte("# Test"), 0o644))
	gitRun(setupDir, "add", ".")
	gitRun(setupDir, "commit", "-m", "init")
	gitRun(setupDir, "push", "origin", "main")

	gitRun(setupDir, "checkout", "--orphan", branchName)
	gitRun(setupDir, "rm", "-rf", ".")
	baseDir := filepath.Join(setupDir, "aa", "aaaaaaaaaa")
	require.NoError(t, os.MkdirAll(baseDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "metadata.json"),
		[]byte(`{"checkpoint_id":"aaaaaaaaaaaa"}`), 0o644))
	gitRun(setupDir, "add", ".")
	gitRun(setupDir, "commit", "-m", "Checkpoint: aaaaaaaaaaaa")
	gitRun(setupDir, "push", "origin", branchName)
	gitRun(setupDir, "checkout", "main")

	cloneDir := filepath.Join(t.TempDir(), "clone")
	require.NoError(t, os.MkdirAll(cloneDir, 0o755))
	gitRun(cloneDir, "clone", bareDir, ".")
	gitRun(cloneDir, "config", "user.email", "test@test.com")
	gitRun(cloneDir, "config", "user.name", "Test User")
	gitRun(cloneDir, "config", "commit.gpgsign", "false")
	gitRun(cloneDir, "branch", branchName, "origin/"+branchName)

	gitRun(cloneDir, "checkout", "--orphan", "temp-orphan")
	gitRun(cloneDir, "rm", "-rf", ".")
	localDir := filepath.Join(cloneDir, "cc", "cccccccccc")
	require.NoError(t, os.MkdirAll(localDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(localDir, "metadata.json"),
		[]byte(`{"checkpoint_id":"cccccccccccc"}`), 0o644))
	gitRun(cloneDir, "add", ".")
	gitRun(cloneDir, "commit", "-m", "Checkpoint: cccccccccccc")
	gitRun(cloneDir, "branch", "-f", branchName, "temp-orphan")
	gitRun(cloneDir, "checkout", "main")

	repo, err := git.PlainOpen(cloneDir)
	require.NoError(t, err)
	localRefBeforeFetch, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	require.NoError(t, err)
	staleOriginRef := plumbing.NewHashReference(
		plumbing.NewRemoteReferenceName("origin", branchName),
		localRefBeforeFetch.Hash(),
	)
	require.NoError(t, repo.Storer.SetReference(staleOriginRef))

	t.Chdir(cloneDir)

	err = fetchAndRebaseSessionsCommon(ctx, "file://"+bareDir, branchName)
	require.NoError(t, err)

	repo, err = git.PlainOpen(cloneDir)
	require.NoError(t, err)

	localRef, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	require.NoError(t, err)

	tipCommit, err := repo.CommitObject(localRef.Hash())
	require.NoError(t, err)
	require.Len(t, tipCommit.ParentHashes, 1)

	tree, err := tipCommit.Tree()
	require.NoError(t, err)

	entries := make(map[string]object.TreeEntry)
	require.NoError(t, checkpoint.FlattenTree(repo, tree, "", entries))
	assert.Contains(t, entries, "aa/aaaaaaaaaa/metadata.json", "remote checkpoint should be preserved")
	assert.Contains(t, entries, "cc/cccccccccc/metadata.json", "local checkpoint should be preserved")

	_, err = repo.Reference(plumbing.ReferenceName("refs/trace-fetch-tmp/"+branchName), true)
	assert.ErrorIs(t, err, plumbing.ErrReferenceNotFound, "temporary fetched ref should be cleaned up")
}

// TestFetchAndRebase_FlaggedOriginTarget_UsesTempRef verifies that enabling
// filtered_fetches for a normal remote-name target follows the temp-ref
// path and still cleans up after rebasing.
//
// Not parallel: uses t.Chdir() (required for OpenRepository).
func TestFetchAndRebase_FlaggedOriginTarget_UsesTempRef(t *testing.T) {
	ctx := context.Background()
	branchName := paths.MetadataBranchName

	bareDir := t.TempDir()
	setupDir := t.TempDir()
	gitRun := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = testutil.GitIsolatedEnv()
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v in %s failed: %s", args, dir, out)
	}

	gitRun(bareDir, "init", "--bare", "-b", "main")
	gitRun(setupDir, "clone", bareDir, ".")
	gitRun(setupDir, "config", "user.email", "test@test.com")
	gitRun(setupDir, "config", "user.name", "Test User")
	gitRun(setupDir, "config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(setupDir, "README.md"), []byte("# Test"), 0o644))
	gitRun(setupDir, "add", ".")
	gitRun(setupDir, "commit", "-m", "init")
	gitRun(setupDir, "push", "origin", "main")

	gitRun(setupDir, "checkout", "--orphan", branchName)
	gitRun(setupDir, "rm", "-rf", ".")
	baseDir := filepath.Join(setupDir, "aa", "aaaaaaaaaa")
	require.NoError(t, os.MkdirAll(baseDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, "metadata.json"),
		[]byte(`{"checkpoint_id":"aaaaaaaaaaaa"}`), 0o644))
	gitRun(setupDir, "add", ".")
	gitRun(setupDir, "commit", "-m", "Checkpoint: aaaaaaaaaaaa")
	gitRun(setupDir, "push", "origin", branchName)
	gitRun(setupDir, "checkout", "main")

	cloneDir := filepath.Join(t.TempDir(), "clone")
	require.NoError(t, os.MkdirAll(cloneDir, 0o755))
	gitRun(cloneDir, "clone", bareDir, ".")
	gitRun(cloneDir, "config", "user.email", "test@test.com")
	gitRun(cloneDir, "config", "user.name", "Test User")
	gitRun(cloneDir, "config", "commit.gpgsign", "false")
	gitRun(cloneDir, "branch", branchName, "origin/"+branchName)
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, ".trace"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(cloneDir, ".trace", "settings.json"),
		[]byte(`{"enabled": true, "strategy_options": {"filtered_fetches": true}}`),
		0o644,
	))

	gitRun(cloneDir, "checkout", "--orphan", "temp-orphan")
	gitRun(cloneDir, "rm", "-rf", ".")
	localDir := filepath.Join(cloneDir, "cc", "cccccccccc")
	require.NoError(t, os.MkdirAll(localDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(localDir, "metadata.json"),
		[]byte(`{"checkpoint_id":"cccccccccccc"}`), 0o644))
	gitRun(cloneDir, "add", ".")
	gitRun(cloneDir, "commit", "-m", "Checkpoint: cccccccccccc")
	gitRun(cloneDir, "branch", "-f", branchName, "temp-orphan")
	gitRun(cloneDir, "checkout", "main")

	repo, err := git.PlainOpen(cloneDir)
	require.NoError(t, err)
	localRefBeforeFetch, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	require.NoError(t, err)
	staleOriginRef := plumbing.NewHashReference(
		plumbing.NewRemoteReferenceName("origin", branchName),
		localRefBeforeFetch.Hash(),
	)
	require.NoError(t, repo.Storer.SetReference(staleOriginRef))

	t.Chdir(cloneDir)

	err = fetchAndRebaseSessionsCommon(ctx, "origin", branchName)
	require.NoError(t, err)

	repo, err = git.PlainOpen(cloneDir)
	require.NoError(t, err)

	localRef, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	require.NoError(t, err)

	tipCommit, err := repo.CommitObject(localRef.Hash())
	require.NoError(t, err)
	require.Len(t, tipCommit.ParentHashes, 1)

	tree, err := tipCommit.Tree()
	require.NoError(t, err)

	entries := make(map[string]object.TreeEntry)
	require.NoError(t, checkpoint.FlattenTree(repo, tree, "", entries))
	assert.Contains(t, entries, "aa/aaaaaaaaaa/metadata.json", "remote checkpoint should be preserved")
	assert.Contains(t, entries, "cc/cccccccccc/metadata.json", "local checkpoint should be preserved")

	_, err = repo.Reference(plumbing.ReferenceName("refs/trace-fetch-tmp/"+branchName), true)
	assert.ErrorIs(t, err, plumbing.ErrReferenceNotFound, "temporary fetched ref should be cleaned up")
}

// TestIsCheckpointRemoteCommitted verifies that the discoverability check reads
// the committed content of .trace/settings.json at HEAD, not just tracking status.
// Not parallel: uses t.Chdir().
func TestIsCheckpointRemoteCommitted(t *testing.T) {
	checkpointRemoteSettings := `{"strategy_options":{"checkpoint_remote":{"provider":"github","repo":"org/checkpoints"}}}`

	t.Run("false when settings.json not committed", func(t *testing.T) {
		tmpDir := t.TempDir()
		testutil.InitRepo(t, tmpDir)
		testutil.WriteFile(t, tmpDir, "f.txt", "init")
		testutil.GitAdd(t, tmpDir, "f.txt")
		testutil.GitCommit(t, tmpDir, "init")

		// Create .trace/settings.json with checkpoint_remote but don't commit it
		traceDir := filepath.Join(tmpDir, ".trace")
		require.NoError(t, os.MkdirAll(traceDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"),
			[]byte(checkpointRemoteSettings), 0o644))

		t.Chdir(tmpDir)
		assert.False(t, isCheckpointRemoteCommitted(context.Background()))
	})

	t.Run("false when committed settings.json has no checkpoint_remote", func(t *testing.T) {
		tmpDir := t.TempDir()
		testutil.InitRepo(t, tmpDir)
		testutil.WriteFile(t, tmpDir, "f.txt", "init")
		testutil.GitAdd(t, tmpDir, "f.txt")
		testutil.GitCommit(t, tmpDir, "init")

		// Commit settings.json without checkpoint_remote
		traceDir := filepath.Join(tmpDir, ".trace")
		require.NoError(t, os.MkdirAll(traceDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"), []byte(`{}`), 0o644))
		testutil.GitAdd(t, tmpDir, ".trace/settings.json")
		testutil.GitCommit(t, tmpDir, "add settings")

		t.Chdir(tmpDir)
		assert.False(t, isCheckpointRemoteCommitted(context.Background()))
	})

	t.Run("true when committed settings.json has checkpoint_remote", func(t *testing.T) {
		tmpDir := t.TempDir()
		testutil.InitRepo(t, tmpDir)
		testutil.WriteFile(t, tmpDir, "f.txt", "init")
		testutil.GitAdd(t, tmpDir, "f.txt")
		testutil.GitCommit(t, tmpDir, "init")

		// Commit settings.json with checkpoint_remote
		traceDir := filepath.Join(tmpDir, ".trace")
		require.NoError(t, os.MkdirAll(traceDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"),
			[]byte(checkpointRemoteSettings), 0o644))
		testutil.GitAdd(t, tmpDir, ".trace/settings.json")
		testutil.GitCommit(t, tmpDir, "add settings")

		t.Chdir(tmpDir)
		assert.True(t, isCheckpointRemoteCommitted(context.Background()))
	})

	t.Run("false when checkpoint_remote only in local changes", func(t *testing.T) {
		tmpDir := t.TempDir()
		testutil.InitRepo(t, tmpDir)
		testutil.WriteFile(t, tmpDir, "f.txt", "init")
		testutil.GitAdd(t, tmpDir, "f.txt")
		testutil.GitCommit(t, tmpDir, "init")

		// Commit settings.json without checkpoint_remote
		traceDir := filepath.Join(tmpDir, ".trace")
		require.NoError(t, os.MkdirAll(traceDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"), []byte(`{}`), 0o644))
		testutil.GitAdd(t, tmpDir, ".trace/settings.json")
		testutil.GitCommit(t, tmpDir, "add settings without remote")

		// Now add checkpoint_remote locally but don't commit
		require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"),
			[]byte(checkpointRemoteSettings), 0o644))

		t.Chdir(tmpDir)
		assert.False(t, isCheckpointRemoteCommitted(context.Background()),
			"uncommitted checkpoint_remote should not count as discoverable")
	})

	t.Run("works from subdirectory", func(t *testing.T) {
		tmpDir := t.TempDir()
		testutil.InitRepo(t, tmpDir)
		testutil.WriteFile(t, tmpDir, "f.txt", "init")
		testutil.GitAdd(t, tmpDir, "f.txt")
		testutil.GitCommit(t, tmpDir, "init")

		traceDir := filepath.Join(tmpDir, ".trace")
		require.NoError(t, os.MkdirAll(traceDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"),
			[]byte(checkpointRemoteSettings), 0o644))
		testutil.GitAdd(t, tmpDir, ".trace/settings.json")
		testutil.GitCommit(t, tmpDir, "add settings")

		subDir := filepath.Join(tmpDir, "subdir")
		require.NoError(t, os.MkdirAll(subDir, 0o755))
		t.Chdir(subDir)
		assert.True(t, isCheckpointRemoteCommitted(context.Background()),
			"should detect committed checkpoint_remote from subdirectory")
	})
}

// TestPrintSettingsCommitHint verifies the hint only prints for URL targets
// when checkpoint_remote is not discoverable from committed settings, and only
// once per process via sync.Once.
// Not parallel: uses t.Chdir() and resets package-level settingsHintOnce.
func TestPrintSettingsCommitHint(t *testing.T) {
	checkpointRemoteSettings := `{"strategy_options":{"checkpoint_remote":{"provider":"github","repo":"org/checkpoints"}}}`

	t.Run("no hint for non-URL target", func(t *testing.T) {
		settingsHintOnce = sync.Once{}

		tmpDir := t.TempDir()
		testutil.InitRepo(t, tmpDir)
		testutil.WriteFile(t, tmpDir, "f.txt", "init")
		testutil.GitAdd(t, tmpDir, "f.txt")
		testutil.GitCommit(t, tmpDir, "init")
		t.Chdir(tmpDir)

		old := os.Stderr
		r, w, err := os.Pipe()
		require.NoError(t, err)
		os.Stderr = w

		printSettingsCommitHint(context.Background(), "origin")

		w.Close()
		var buf bytes.Buffer
		if _, readErr := buf.ReadFrom(r); readErr != nil {
			t.Fatalf("read pipe: %v", readErr)
		}
		os.Stderr = old

		assert.Empty(t, buf.String(), "should not print hint for non-URL target")
	})

	t.Run("hint when checkpoint_remote not in committed settings", func(t *testing.T) {
		settingsHintOnce = sync.Once{}

		tmpDir := t.TempDir()
		testutil.InitRepo(t, tmpDir)
		testutil.WriteFile(t, tmpDir, "f.txt", "init")
		testutil.GitAdd(t, tmpDir, "f.txt")
		testutil.GitCommit(t, tmpDir, "init")

		// Create .trace/settings.json but don't commit it
		traceDir := filepath.Join(tmpDir, ".trace")
		require.NoError(t, os.MkdirAll(traceDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"),
			[]byte(checkpointRemoteSettings), 0o644))
		t.Chdir(tmpDir)

		old := os.Stderr
		r, w, err := os.Pipe()
		require.NoError(t, err)
		os.Stderr = w

		printSettingsCommitHint(context.Background(), "git@github.com:org/repo.git")

		w.Close()
		var buf bytes.Buffer
		if _, readErr := buf.ReadFrom(r); readErr != nil {
			t.Fatalf("read pipe: %v", readErr)
		}
		os.Stderr = old

		assert.Contains(t, buf.String(), "does not contain checkpoint_remote")
		assert.Contains(t, buf.String(), "trace.io will not be able to discover")
	})

	t.Run("hint when committed settings lacks checkpoint_remote", func(t *testing.T) {
		settingsHintOnce = sync.Once{}

		tmpDir := t.TempDir()
		testutil.InitRepo(t, tmpDir)
		testutil.WriteFile(t, tmpDir, "f.txt", "init")
		testutil.GitAdd(t, tmpDir, "f.txt")
		testutil.GitCommit(t, tmpDir, "init")

		// Commit settings.json without checkpoint_remote
		traceDir := filepath.Join(tmpDir, ".trace")
		require.NoError(t, os.MkdirAll(traceDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"), []byte(`{}`), 0o644))
		testutil.GitAdd(t, tmpDir, ".trace/settings.json")
		testutil.GitCommit(t, tmpDir, "add settings")
		t.Chdir(tmpDir)

		old := os.Stderr
		r, w, err := os.Pipe()
		require.NoError(t, err)
		os.Stderr = w

		printSettingsCommitHint(context.Background(), "git@github.com:org/repo.git")

		w.Close()
		var buf bytes.Buffer
		if _, readErr := buf.ReadFrom(r); readErr != nil {
			t.Fatalf("read pipe: %v", readErr)
		}
		os.Stderr = old

		assert.Contains(t, buf.String(), "does not contain checkpoint_remote",
			"should warn when committed settings.json exists but lacks checkpoint_remote")
	})

	t.Run("no hint when checkpoint_remote is committed", func(t *testing.T) {
		settingsHintOnce = sync.Once{}

		tmpDir := t.TempDir()
		testutil.InitRepo(t, tmpDir)
		testutil.WriteFile(t, tmpDir, "f.txt", "init")
		testutil.GitAdd(t, tmpDir, "f.txt")
		testutil.GitCommit(t, tmpDir, "init")

		// Commit settings.json with checkpoint_remote
		traceDir := filepath.Join(tmpDir, ".trace")
		require.NoError(t, os.MkdirAll(traceDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"),
			[]byte(checkpointRemoteSettings), 0o644))
		testutil.GitAdd(t, tmpDir, ".trace/settings.json")
		testutil.GitCommit(t, tmpDir, "add settings with checkpoint remote")
		t.Chdir(tmpDir)

		old := os.Stderr
		r, w, err := os.Pipe()
		require.NoError(t, err)
		os.Stderr = w

		printSettingsCommitHint(context.Background(), "git@github.com:org/repo.git")

		w.Close()
		var buf bytes.Buffer
		if _, readErr := buf.ReadFrom(r); readErr != nil {
			t.Fatalf("read pipe: %v", readErr)
		}
		os.Stderr = old

		assert.Empty(t, buf.String(), "should not print hint when checkpoint_remote is committed")
	})

	t.Run("prints only once per process", func(t *testing.T) {
		settingsHintOnce = sync.Once{}

		tmpDir := t.TempDir()
		testutil.InitRepo(t, tmpDir)
		testutil.WriteFile(t, tmpDir, "f.txt", "init")
		testutil.GitAdd(t, tmpDir, "f.txt")
		testutil.GitCommit(t, tmpDir, "init")
		t.Chdir(tmpDir)

		old := os.Stderr
		r, w, err := os.Pipe()
		require.NoError(t, err)
		os.Stderr = w

		// Call twice — should only print once
		printSettingsCommitHint(context.Background(), "git@github.com:org/repo.git")
		printSettingsCommitHint(context.Background(), "git@github.com:org/repo.git")

		w.Close()
		var buf bytes.Buffer
		if _, readErr := buf.ReadFrom(r); readErr != nil {
			t.Fatalf("read pipe: %v", readErr)
		}
		os.Stderr = old

		count := bytes.Count(buf.Bytes(), []byte("does not contain checkpoint_remote"))
		assert.Equal(t, 1, count, "hint should print exactly once, got %d", count)
	})
}

func TestIsCheckpointsVersion2Committed(t *testing.T) {
	t.Run("false when settings.json not committed", func(t *testing.T) {
		tmpDir := t.TempDir()
		testutil.InitRepo(t, tmpDir)
		testutil.WriteFile(t, tmpDir, "f.txt", "init")
		testutil.GitAdd(t, tmpDir, "f.txt")
		testutil.GitCommit(t, tmpDir, "init")

		traceDir := filepath.Join(tmpDir, ".trace")
		require.NoError(t, os.MkdirAll(traceDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"),
			[]byte(`{"strategy_options":{"checkpoints_version":2}}`), 0o644))

		t.Chdir(tmpDir)
		assert.False(t, isCheckpointsVersion2Committed(context.Background()))
	})

	t.Run("true when checkpoints_version 2 is committed", func(t *testing.T) {
		tmpDir := t.TempDir()
		testutil.InitRepo(t, tmpDir)
		testutil.WriteFile(t, tmpDir, "f.txt", "init")
		testutil.GitAdd(t, tmpDir, "f.txt")
		testutil.GitCommit(t, tmpDir, "init")

		traceDir := filepath.Join(tmpDir, ".trace")
		require.NoError(t, os.MkdirAll(traceDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"),
			[]byte(`{"strategy_options":{"checkpoints_version":2}}`), 0o644))
		testutil.GitAdd(t, tmpDir, ".trace/settings.json")
		testutil.GitCommit(t, tmpDir, "enable checkpoints_version 2")

		t.Chdir(tmpDir)
		assert.True(t, isCheckpointsVersion2Committed(context.Background()))
	})
}

// setupCheckpointsV2CommittedRepo creates a temp repo with checkpoints_version: 2
// set in the committed .trace/settings.json and chdirs into it. Returns an opened
// *git.Repository for populating checkpoints.
func setupCheckpointsV2CommittedRepo(t *testing.T) *git.Repository {
	t.Helper()

	tmpDir := t.TempDir()
	testutil.InitRepo(t, tmpDir)
	testutil.WriteFile(t, tmpDir, "f.txt", "init")
	testutil.GitAdd(t, tmpDir, "f.txt")
	testutil.GitCommit(t, tmpDir, "init")

	traceDir := filepath.Join(tmpDir, ".trace")
	require.NoError(t, os.MkdirAll(traceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"),
		[]byte(`{"strategy_options":{"checkpoints_version":2}}`), 0o644))
	testutil.GitAdd(t, tmpDir, ".trace/settings.json")
	testutil.GitCommit(t, tmpDir, "enable checkpoints_version 2")
	t.Chdir(tmpDir)

	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)
	return repo
}

// writeV1Checkpoint writes a minimal checkpoint to the v1 metadata branch.
func writeV1Checkpoint(t *testing.T, repo *git.Repository, cpID id.CheckpointID, sessionID string) {
	t.Helper()
	err := checkpoint.NewGitStore(repo).WriteCommitted(context.Background(), checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    sessionID,
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte(`{"from":"` + sessionID + `"}`)),
		AuthorName:   "Test",
		AuthorEmail:  "test@test.com",
	})
	require.NoError(t, err)
}

func writeMalformedV1CheckpointWithoutSummary(t *testing.T, repo *git.Repository, cpID id.CheckpointID) {
	t.Helper()
	ctx := context.Background()

	blobHash, err := checkpoint.CreateBlobFromContent(repo, []byte("transcript without root metadata"))
	require.NoError(t, err)

	treeHash, err := checkpoint.BuildTreeFromEntries(ctx, repo, map[string]object.TreeEntry{
		cpID.Path() + "/0/" + paths.TranscriptFileName: {
			Mode: filemode.Regular,
			Hash: blobHash,
		},
	})
	require.NoError(t, err)

	commitHash, err := checkpoint.CreateCommit(ctx, repo, treeHash, plumbing.ZeroHash, "malformed v1 checkpoint", "Test", "test@test.com")
	require.NoError(t, err)

	refName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(refName, commitHash)))
}

func TestPrintCheckpointsV2MigrationHint(t *testing.T) {
	t.Run("suppressed when no v1 checkpoints exist", func(t *testing.T) {
		checkpointsV2MigrationHintOnce = sync.Once{}
		setupCheckpointsV2CommittedRepo(t)

		restore := captureStderr(t)
		printCheckpointsV2MigrationHint(context.Background())
		output := restore()

		assert.Empty(t, output, "hint should not print when there are no v1 checkpoints to migrate")
	})

	t.Run("suppressed when every v1 checkpoint is already in v2", func(t *testing.T) {
		checkpointsV2MigrationHintOnce = sync.Once{}
		repo := setupCheckpointsV2CommittedRepo(t)

		cpID := id.MustCheckpointID("aabbccddeeff")
		writeV1Checkpoint(t, repo, cpID, "session-1")
		writeV2Checkpoint(t, repo, cpID, "session-1")

		restore := captureStderr(t)
		printCheckpointsV2MigrationHint(context.Background())
		output := restore()

		assert.Empty(t, output, "hint should not print once v2 already mirrors every v1 checkpoint")
	})

	t.Run("prints when v1 has checkpoints not in v2", func(t *testing.T) {
		checkpointsV2MigrationHintOnce = sync.Once{}
		repo := setupCheckpointsV2CommittedRepo(t)

		writeV1Checkpoint(t, repo, id.MustCheckpointID("111111111111"), "session-1")

		restore := captureStderr(t)
		printCheckpointsV2MigrationHint(context.Background())
		output := restore()

		assert.Contains(t, output, "trace migrate --checkpoints v2")
		assert.Contains(t, output, "trace migrate --checkpoints v2 --force")
	})

	t.Run("prints only once per process", func(t *testing.T) {
		checkpointsV2MigrationHintOnce = sync.Once{}
		repo := setupCheckpointsV2CommittedRepo(t)

		writeV1Checkpoint(t, repo, id.MustCheckpointID("222222222222"), "session-2")

		restore := captureStderr(t)
		printCheckpointsV2MigrationHint(context.Background())
		printCheckpointsV2MigrationHint(context.Background())
		output := restore()

		// --force appears in exactly one line, so its count equals the number of
		// invocations that actually emitted output.
		forceCount := strings.Count(output, "--force")
		assert.Equal(t, 1, forceCount, "hint should print exactly once per process")
	})
}

func TestHasUnmigratedV1Checkpoints(t *testing.T) {
	t.Run("false when no v1 checkpoints exist", func(t *testing.T) {
		setupCheckpointsV2CommittedRepo(t)
		assert.False(t, hasUnmigratedV1Checkpoints(context.Background()))
	})

	t.Run("false when every v1 checkpoint is in v2", func(t *testing.T) {
		repo := setupCheckpointsV2CommittedRepo(t)
		cpID := id.MustCheckpointID("333333333333")
		writeV1Checkpoint(t, repo, cpID, "session-a")
		writeV2Checkpoint(t, repo, cpID, "session-a")

		assert.False(t, hasUnmigratedV1Checkpoints(context.Background()))
	})

	t.Run("true when at least one v1 checkpoint is missing from v2", func(t *testing.T) {
		repo := setupCheckpointsV2CommittedRepo(t)
		mirrored := id.MustCheckpointID("444444444444")
		missing := id.MustCheckpointID("555555555555")
		writeV1Checkpoint(t, repo, mirrored, "session-b")
		writeV2Checkpoint(t, repo, mirrored, "session-b")
		writeV1Checkpoint(t, repo, missing, "session-c")

		assert.True(t, hasUnmigratedV1Checkpoints(context.Background()))
	})

	t.Run("false when only malformed v1 checkpoint entries are missing from v2", func(t *testing.T) {
		repo := setupCheckpointsV2CommittedRepo(t)
		writeMalformedV1CheckpointWithoutSummary(t, repo, id.MustCheckpointID("666666666666"))

		assert.False(t, hasUnmigratedV1Checkpoints(context.Background()))
	})
}

// captureStderr redirects os.Stderr to a pipe and returns a function that restores
// stderr and returns the captured output. Must be called on the main goroutine
// (not parallel-safe). Uses t.Cleanup as a safety net to restore stderr and close
// pipe file descriptors if the test fails or panics before the returned function
// is called.
func captureStderr(t *testing.T) func() string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w

	// Safety net: restore stderr and close pipe ends on test failure/panic.
	// In the normal path the returned function handles cleanup first;
	// duplicate Close calls return an error that we intentionally ignore.
	t.Cleanup(func() {
		os.Stderr = old
		_ = w.Close()
		_ = r.Close()
	})

	return func() string {
		_ = w.Close()
		var buf bytes.Buffer
		_, readErr := buf.ReadFrom(r)
		require.NoError(t, readErr)
		_ = r.Close()
		os.Stderr = old
		return buf.String()
	}
}

// setupBareRemoteWithCheckpointBranch creates a work repo with a checkpoint branch
// and a bare remote that already has the branch pushed. Returns (workDir, bareDir).
// Caller must t.Chdir(workDir) before calling push functions.
func setupBareRemoteWithCheckpointBranch(t *testing.T) (string, string) {
	t.Helper()
	ctx := context.Background()

	workDir := setupRepoWithCheckpointBranch(t)

	bareDir := t.TempDir()
	initCmd := exec.CommandContext(ctx, "git", "init", "--bare")
	initCmd.Dir = bareDir
	initCmd.Env = testutil.GitIsolatedEnv()
	out, err := initCmd.CombinedOutput()
	require.NoError(t, err, "git init --bare failed: %s", out)

	// Push the checkpoint branch to the bare remote
	pushCmd := exec.CommandContext(ctx, "git", "push", bareDir, paths.MetadataBranchName)
	pushCmd.Dir = workDir
	pushCmd.Env = testutil.GitIsolatedEnv()
	out, err = pushCmd.CombinedOutput()
	require.NoError(t, err, "initial push failed: %s", out)

	return workDir, bareDir
}

// TestDoPushBranch_AlreadyUpToDate verifies that when the remote already has all
// commits, the output says "already up-to-date" instead of "done".
//
// Not parallel: uses t.Chdir() and os.Stderr redirection.
func TestDoPushBranch_AlreadyUpToDate(t *testing.T) {
	workDir, bareDir := setupBareRemoteWithCheckpointBranch(t)
	t.Chdir(workDir)

	restore := captureStderr(t)
	err := doPushBranch(context.Background(), bareDir, paths.MetadataBranchName)
	output := restore()

	require.NoError(t, err)
	assert.Contains(t, output, "already up-to-date", "should indicate nothing was pushed")
	assert.NotContains(t, output, " done", "should not say 'done' when nothing was pushed")
}

// TestDoPushBranch_NewContent_SaysDone verifies that when there are new commits
// to push, the output says "done".
//
// Not parallel: uses t.Chdir() and os.Stderr redirection.
func TestDoPushBranch_NewContent_SaysDone(t *testing.T) {
	workDir := setupRepoWithCheckpointBranch(t)

	// Create a bare remote with no checkpoint branch yet
	bareDir := t.TempDir()
	initCmd := exec.CommandContext(context.Background(), "git", "init", "--bare")
	initCmd.Dir = bareDir
	initCmd.Env = testutil.GitIsolatedEnv()
	out, err := initCmd.CombinedOutput()
	require.NoError(t, err, "git init --bare failed: %s", out)

	t.Chdir(workDir)

	restore := captureStderr(t)
	err = doPushBranch(context.Background(), bareDir, paths.MetadataBranchName)
	output := restore()

	require.NoError(t, err)
	assert.Contains(t, output, " done", "should say 'done' when new content was pushed")
	assert.NotContains(t, output, "already up-to-date", "should not say 'already up-to-date' when content was pushed")
}

func TestIsProtectedRefRejection(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		output string
		want   bool
	}{
		"GH013 marker":           {"remote: error: GH013: Repository rule violations found", true},
		"cannot update phrase":   {"remote: error: Cannot update this protected ref.", true},
		"legacy hook declined":   {"! [remote rejected] main -> main (protected branch hook declined)", true},
		"plain non-fast-forward": {"! [rejected] v1 -> v1 (non-fast-forward)", false},
		"empty":                  {"", false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, isProtectedRefRejection(tc.output))
		})
	}
}
