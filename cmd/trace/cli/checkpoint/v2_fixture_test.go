package checkpoint

import (
	"testing"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/testutil"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// initTestRepo creates a bare-minimum git repo with one commit (needed for HEAD).
func initTestRepo(t *testing.T) *git.Repository {
	t.Helper()
	dir := t.TempDir()

	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, "README.md", "init")
	testutil.GitAdd(t, dir, "README.md")
	testutil.GitCommit(t, dir, "initial")

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	return repo
}

// v2MainTree returns the root tree from the /main ref for test assertions.
func v2MainTree(t *testing.T, repo *git.Repository) *object.Tree {
	t.Helper()
	ref, err := repo.Reference(plumbing.ReferenceName(paths.V2MainRefName), true)
	require.NoError(t, err)
	commit, err := repo.CommitObject(ref.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)
	return tree
}

// v2ReadFile reads a file from a git tree by path.
func v2ReadFile(t *testing.T, tree *object.Tree, path string) string {
	t.Helper()
	file, err := tree.File(path)
	require.NoError(t, err, "expected file at %s", path)
	content, err := file.Contents()
	require.NoError(t, err)
	return content
}
