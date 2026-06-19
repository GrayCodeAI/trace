package strategy

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createV2MainRef creates a v2 /main custom ref with a single orphan commit.
// Uses git plumbing to create the ref under refs/trace/ (not refs/heads/).
// Each call produces a distinct commit (uses a sequence counter in content).
func createV2MainRef(ctx context.Context, t *testing.T, repoDir string) {
	t.Helper()
	v2RefSeq++

	cmd := exec.CommandContext(ctx, "git", "hash-object", "-w", "--stdin")
	cmd.Dir = repoDir
	cmd.Env = testutil.GitIsolatedEnv()
	cmd.Stdin = strings.NewReader(fmt.Sprintf(`{"test": true, "seq": %d}`, v2RefSeq))
	blobOut, err := cmd.Output()
	require.NoError(t, err)
	blobHash := strings.TrimSpace(string(blobOut))

	cmd = exec.CommandContext(ctx, "git", "mktree")
	cmd.Dir = repoDir
	cmd.Env = testutil.GitIsolatedEnv()
	cmd.Stdin = strings.NewReader("100644 blob " + blobHash + "\tmetadata.json\n")
	treeOut, err := cmd.Output()
	require.NoError(t, err)
	treeHash := strings.TrimSpace(string(treeOut))

	cmd = exec.CommandContext(ctx, "git", "commit-tree", "-m", fmt.Sprintf("v2 checkpoint %d", v2RefSeq), treeHash)
	cmd.Dir = repoDir
	cmd.Env = testutil.GitIsolatedEnv()
	commitOut, err := cmd.Output()
	require.NoError(t, err)
	commitHash := strings.TrimSpace(string(commitOut))

	cmd = exec.CommandContext(ctx, "git", "update-ref", paths.V2MainRefName, commitHash)
	cmd.Dir = repoDir
	cmd.Env = testutil.GitIsolatedEnv()
	require.NoError(t, cmd.Run())
}

// refExists checks whether a custom ref exists in the repo.
func refExists(ctx context.Context, t *testing.T, repoDir, refName string) bool {
	t.Helper()
	cmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", refName)
	cmd.Dir = repoDir
	cmd.Env = testutil.GitIsolatedEnv()
	return cmd.Run() == nil
}

// Not parallel: uses t.Chdir()
func TestFetchV2MainFromURL_FetchesRef(t *testing.T) {
	ctx := context.Background()

	// Set up "remote" repo with v2 /main ref
	remoteDir := t.TempDir()
	testutil.InitRepo(t, remoteDir)
	testutil.WriteFile(t, remoteDir, "f.txt", "init")
	testutil.GitAdd(t, remoteDir, "f.txt")
	testutil.GitCommit(t, remoteDir, "init")
	createV2MainRef(ctx, t, remoteDir)

	// Set up local repo
	localDir := t.TempDir()
	testutil.InitRepo(t, localDir)
	testutil.WriteFile(t, localDir, "f.txt", "init")
	testutil.GitAdd(t, localDir, "f.txt")
	testutil.GitCommit(t, localDir, "init")

	t.Chdir(localDir)

	// Ref doesn't exist yet
	assert.False(t, refExists(ctx, t, localDir, paths.V2MainRefName))

	// Fetch from "remote"
	require.NoError(t, FetchV2MainFromURL(ctx, remoteDir))

	// Ref should now exist
	assert.True(t, refExists(ctx, t, localDir, paths.V2MainRefName))
}

// Not parallel: uses t.Chdir()
func TestFetchV2MainFromURL_UpdatesExistingRef(t *testing.T) {
	ctx := context.Background()

	// Set up "remote" repo with v2 /main ref
	remoteDir := t.TempDir()
	testutil.InitRepo(t, remoteDir)
	testutil.WriteFile(t, remoteDir, "f.txt", "init")
	testutil.GitAdd(t, remoteDir, "f.txt")
	testutil.GitCommit(t, remoteDir, "init")
	createV2MainRef(ctx, t, remoteDir)

	// Set up local repo and fetch once
	localDir := t.TempDir()
	testutil.InitRepo(t, localDir)
	testutil.WriteFile(t, localDir, "f.txt", "init")
	testutil.GitAdd(t, localDir, "f.txt")
	testutil.GitCommit(t, localDir, "init")

	t.Chdir(localDir)

	require.NoError(t, FetchV2MainFromURL(ctx, remoteDir))

	// Record initial hash
	hashCmd := exec.CommandContext(ctx, "git", "rev-parse", paths.V2MainRefName)
	hashCmd.Dir = localDir
	hashCmd.Env = testutil.GitIsolatedEnv()
	hash1Out, err := hashCmd.Output()
	require.NoError(t, err)
	hash1 := strings.TrimSpace(string(hash1Out))

	// Add a second commit on the remote's v2 ref
	createV2MainRef(ctx, t, remoteDir) // Creates a new orphan commit, updating the ref

	// Fetch again — should update
	require.NoError(t, FetchV2MainFromURL(ctx, remoteDir))

	hashCmd = exec.CommandContext(ctx, "git", "rev-parse", paths.V2MainRefName)
	hashCmd.Dir = localDir
	hashCmd.Env = testutil.GitIsolatedEnv()
	hash2Out, err := hashCmd.Output()
	require.NoError(t, err)
	hash2 := strings.TrimSpace(string(hash2Out))

	assert.NotEqual(t, hash1, hash2, "FetchV2MainFromURL should update existing ref to new remote tip")
}

// TestFetchV2MainFromURL_DoesNotRewindLocalAhead verifies that fetching the v2
// /main ref from a remote whose tip is at A does NOT rewind a locally-ahead
// ref at B (A's descendant). The buggy version used a direct-write refspec
// `+refs/trace/v2/main:refs/trace/v2/main` which git applies before Go can
// intercept — orphaning locally-committed-but-unpushed v2 checkpoints.
//
// Not parallel: uses t.Chdir().
func TestFetchV2MainFromURL_DoesNotRewindLocalAhead(t *testing.T) {
	ctx := context.Background()

	// Set up remote with v2 /main ref at commit A.
	remoteDir := t.TempDir()
	testutil.InitRepo(t, remoteDir)
	testutil.WriteFile(t, remoteDir, "f.txt", "init")
	testutil.GitAdd(t, remoteDir, "f.txt")
	testutil.GitCommit(t, remoteDir, "init")
	createV2MainRef(ctx, t, remoteDir)

	// Set up local repo and fetch once so local v2 /main is at A.
	localDir := t.TempDir()
	testutil.InitRepo(t, localDir)
	testutil.WriteFile(t, localDir, "f.txt", "init")
	testutil.GitAdd(t, localDir, "f.txt")
	testutil.GitCommit(t, localDir, "init")
	t.Chdir(localDir)

	require.NoError(t, FetchV2MainFromURL(ctx, remoteDir))

	hashCmd := exec.CommandContext(ctx, "git", "rev-parse", paths.V2MainRefName)
	hashCmd.Dir = localDir
	hashCmd.Env = testutil.GitIsolatedEnv()
	aOut, err := hashCmd.Output()
	require.NoError(t, err)
	aHash := strings.TrimSpace(string(aOut))

	// Advance local v2 /main to B, with A as parent (descendant relationship).
	// This mirrors the real flow where condensation appends a new checkpoint
	// commit on top of the previous tip.
	advanceV2MainOnTop(ctx, t, localDir, aHash)

	hashCmd = exec.CommandContext(ctx, "git", "rev-parse", paths.V2MainRefName)
	hashCmd.Dir = localDir
	hashCmd.Env = testutil.GitIsolatedEnv()
	bOut, err := hashCmd.Output()
	require.NoError(t, err)
	bHash := strings.TrimSpace(string(bOut))
	require.NotEqual(t, aHash, bHash, "test setup: local v2 /main should have advanced beyond remote")

	// Fetch from remote — must NOT rewind local from B to A.
	require.NoError(t, FetchV2MainFromURL(ctx, remoteDir))

	hashCmd = exec.CommandContext(ctx, "git", "rev-parse", paths.V2MainRefName)
	hashCmd.Dir = localDir
	hashCmd.Env = testutil.GitIsolatedEnv()
	afterOut, err := hashCmd.Output()
	require.NoError(t, err)
	afterHash := strings.TrimSpace(string(afterOut))

	assert.Equal(t, bHash, afterHash,
		"FetchV2MainFromURL must not rewind locally-ahead v2 /main; expected %s (B), got %s (A=%s)",
		bHash, afterHash, aHash)
}

// advanceV2MainOnTop creates a new v2 /main commit whose parent is parentHash,
// and updates refs/trace/checkpoints/v2/main to point at it. Used to simulate
// a locally-ahead ref in tests.
func advanceV2MainOnTop(ctx context.Context, t *testing.T, repoDir, parentHash string) {
	t.Helper()
	v2RefSeq++

	cmd := exec.CommandContext(ctx, "git", "hash-object", "-w", "--stdin")
	cmd.Dir = repoDir
	cmd.Env = testutil.GitIsolatedEnv()
	cmd.Stdin = strings.NewReader(fmt.Sprintf(`{"advance": %d}`, v2RefSeq))
	blobOut, err := cmd.Output()
	require.NoError(t, err)
	blobHash := strings.TrimSpace(string(blobOut))

	cmd = exec.CommandContext(ctx, "git", "mktree")
	cmd.Dir = repoDir
	cmd.Env = testutil.GitIsolatedEnv()
	cmd.Stdin = strings.NewReader("100644 blob " + blobHash + "\tadvance.json\n")
	treeOut, err := cmd.Output()
	require.NoError(t, err)
	treeHash := strings.TrimSpace(string(treeOut))

	cmd = exec.CommandContext(ctx, "git", "commit-tree", "-p", parentHash, "-m", fmt.Sprintf("advance %d", v2RefSeq), treeHash)
	cmd.Dir = repoDir
	cmd.Env = testutil.GitIsolatedEnv()
	commitOut, err := cmd.Output()
	require.NoError(t, err)
	commitHash := strings.TrimSpace(string(commitOut))

	cmd = exec.CommandContext(ctx, "git", "update-ref", paths.V2MainRefName, commitHash)
	cmd.Dir = repoDir
	cmd.Env = testutil.GitIsolatedEnv()
	require.NoError(t, cmd.Run())
}
