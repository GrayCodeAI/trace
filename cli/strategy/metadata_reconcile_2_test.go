package strategy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// createTestBlob stores a string as a blob and returns its hash.
func createTestBlob(t *testing.T, repo *git.Repository, content string) plumbing.Hash {
	t.Helper()
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	w, err := obj.Writer()
	require.NoError(t, err)
	_, err = w.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	hash, err := repo.Storer.SetEncodedObject(obj)
	require.NoError(t, err)
	return hash
}

func TestIsV2MainDisconnected_NoLocalRef(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	disconnected, err := IsV2MainDisconnected(context.Background(), repo, dir)
	require.NoError(t, err)
	assert.False(t, disconnected)
}

func TestIsV2MainDisconnected_NoRemoteRef(t *testing.T) {
	t.Parallel()

	bareDir := t.TempDir()
	workDir := t.TempDir()
	run := func(dir string, args ...string) {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		cmd.Env = testutil.GitIsolatedEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	run(bareDir, "init", "--bare", "-b", "main")
	run(workDir, "clone", bareDir, ".")
	run(workDir, "config", "user.email", "test@test.com")
	run(workDir, "config", "user.name", "Test User")
	run(workDir, "config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# Test"), 0o644))
	run(workDir, "add", ".")
	run(workDir, "commit", "-m", "init")
	run(workDir, "push", "origin", "main")

	// Create local v2 /main ref but don't push it
	repo, err := git.PlainOpen(workDir)
	require.NoError(t, err)
	emptyTree := &object.Tree{Entries: []object.TreeEntry{}}
	treeObj := repo.Storer.NewEncodedObject()
	require.NoError(t, emptyTree.Encode(treeObj))
	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	require.NoError(t, err)
	commitHash, err := checkpoint.CreateCommit(context.Background(), repo, treeHash, plumbing.ZeroHash, "init v2", "test", "test@test.com")
	require.NoError(t, err)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(paths.V2MainRefName), commitHash)))

	disconnected, err := IsV2MainDisconnected(context.Background(), repo, bareDir)
	require.NoError(t, err)
	assert.False(t, disconnected, "should not be disconnected when remote doesn't have the ref")
}

func TestIsV2MainDisconnected_Disconnected(t *testing.T) {
	t.Parallel()

	bareDir := initBareWithV2MainRef(t)
	cloneDir, _ := cloneWithConfig(t, bareDir)

	// Create a disconnected local v2 /main ref (independent orphan)
	repo, err := git.PlainOpen(cloneDir)
	require.NoError(t, err)

	localEntries := map[string]object.TreeEntry{
		"cd/ef01234567/" + paths.MetadataFileName: {
			Name: paths.MetadataFileName,
			Mode: 0o100644,
			Hash: createTestBlob(t, repo, `{"checkpoint_id":"cdef01234567"}`),
		},
	}
	localTreeHash, err := checkpoint.BuildTreeFromEntries(context.Background(), repo, localEntries)
	require.NoError(t, err)
	localCommitHash, err := checkpoint.CreateCommit(context.Background(), repo, localTreeHash, plumbing.ZeroHash, "local checkpoint", "test", "test@test.com")
	require.NoError(t, err)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(paths.V2MainRefName), localCommitHash)))

	disconnected, err := IsV2MainDisconnected(context.Background(), repo, bareDir)
	require.NoError(t, err)
	assert.True(t, disconnected, "independent orphan commits should be disconnected")
}

func TestIsV2MainDisconnected_SharedAncestry(t *testing.T) {
	t.Parallel()

	bareDir := initBareWithV2MainRef(t)
	cloneDir, run := cloneWithConfig(t, bareDir)

	// Fetch the v2 /main ref from remote
	run("fetch", "origin", paths.V2MainRefName+":"+paths.V2MainRefName)

	// Add a local commit on top (diverged but shared ancestry)
	repo, err := git.PlainOpen(cloneDir)
	require.NoError(t, err)

	ref, err := repo.Reference(plumbing.ReferenceName(paths.V2MainRefName), true)
	require.NoError(t, err)
	parentCommit, err := repo.CommitObject(ref.Hash())
	require.NoError(t, err)
	parentTree, err := parentCommit.Tree()
	require.NoError(t, err)

	existing := make(map[string]object.TreeEntry)
	require.NoError(t, checkpoint.FlattenTree(repo, parentTree, "", existing))
	existing["ef/0123456789/"+paths.MetadataFileName] = object.TreeEntry{
		Name: paths.MetadataFileName,
		Mode: 0o100644,
		Hash: createTestBlob(t, repo, `{"checkpoint_id":"ef0123456789"}`),
	}
	newTreeHash, err := checkpoint.BuildTreeFromEntries(context.Background(), repo, existing)
	require.NoError(t, err)
	newCommitHash, err := checkpoint.CreateCommit(context.Background(), repo, newTreeHash, ref.Hash(), "local checkpoint 2", "test", "test@test.com")
	require.NoError(t, err)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(paths.V2MainRefName), newCommitHash)))

	disconnected, err := IsV2MainDisconnected(context.Background(), repo, bareDir)
	require.NoError(t, err)
	assert.False(t, disconnected, "diverged with shared ancestor should not be disconnected")
}

func TestReconcileDisconnectedV2Ref_CherryPicksOntoRemote(t *testing.T) {
	t.Parallel()

	bareDir := initBareWithV2MainRef(t)
	cloneDir, _ := cloneWithConfig(t, bareDir)

	// Create disconnected local v2 /main with different checkpoint data
	repo, err := git.PlainOpen(cloneDir)
	require.NoError(t, err)

	localEntries := map[string]object.TreeEntry{
		"cd/ef01234567/" + paths.MetadataFileName: {
			Name: paths.MetadataFileName,
			Mode: 0o100644,
			Hash: createTestBlob(t, repo, `{"checkpoint_id":"cdef01234567"}`),
		},
	}
	localTreeHash, err := checkpoint.BuildTreeFromEntries(context.Background(), repo, localEntries)
	require.NoError(t, err)
	localCommitHash, err := checkpoint.CreateCommit(context.Background(), repo, localTreeHash, plumbing.ZeroHash, "local checkpoint", "test", "test@test.com")
	require.NoError(t, err)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(paths.V2MainRefName), localCommitHash)))

	// Verify disconnected
	disconnected, err := IsV2MainDisconnected(context.Background(), repo, bareDir)
	require.NoError(t, err)
	require.True(t, disconnected, "setup: should be disconnected")

	// Reconcile
	var buf strings.Builder
	err = ReconcileDisconnectedV2Ref(context.Background(), repo, bareDir, &buf)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Cherry-picking")
	assert.Contains(t, buf.String(), "Done")

	// After reconciliation, should no longer be disconnected
	disconnected, err = IsV2MainDisconnected(context.Background(), repo, bareDir)
	require.NoError(t, err)
	assert.False(t, disconnected, "should be connected after reconciliation")

	// Verify both remote and local checkpoint data exist in the tree
	ref, err := repo.Reference(plumbing.ReferenceName(paths.V2MainRefName), true)
	require.NoError(t, err)
	tipCommit, err := repo.CommitObject(ref.Hash())
	require.NoError(t, err)
	tree, err := tipCommit.Tree()
	require.NoError(t, err)
	entries := make(map[string]object.TreeEntry)
	require.NoError(t, checkpoint.FlattenTree(repo, tree, "", entries))

	assert.Contains(t, entries, "ab/cdef012345/"+paths.MetadataFileName, "remote checkpoint should be preserved")
	assert.Contains(t, entries, "cd/ef01234567/"+paths.MetadataFileName, "local checkpoint should be preserved")
}

func TestReconcileDisconnectedV2Ref_NoLocalRef(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	var buf strings.Builder
	err = ReconcileDisconnectedV2Ref(context.Background(), repo, dir, &buf)
	require.NoError(t, err)
	assert.Empty(t, buf.String())
}
