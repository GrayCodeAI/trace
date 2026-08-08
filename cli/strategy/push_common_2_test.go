package strategy

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/testutil"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
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
	acquireGitCLITest(t)

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

	cloneDir := t.TempDir()
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

	err = fetchAndRebaseRefCommon(ctx, "file://"+bareDir, plumbing.NewBranchReferenceName(branchName))
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
	acquireGitCLITest(t)

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

	cloneDir := t.TempDir()
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

	err = fetchAndRebaseRefCommon(ctx, "origin", plumbing.NewBranchReferenceName(branchName))
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

func TestDoPushBranch_AlreadyUpToDate(t *testing.T) {
	workDir, bareDir := setupBareRemoteWithCheckpointBranch(t)
	t.Chdir(workDir)

	restore := captureStderr(t)
	err := doPushRef(context.Background(), bareDir, plumbing.NewBranchReferenceName(paths.MetadataBranchName))
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
	err = doPushRef(context.Background(), bareDir, plumbing.NewBranchReferenceName(paths.MetadataBranchName))
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
