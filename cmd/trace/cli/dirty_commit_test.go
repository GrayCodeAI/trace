package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// setupDirtyRepo creates a temp git repo with an initial commit, chdirs into
// it, and returns the directory. The repo has a committed baseline so that
// subsequent edits register as "dirty". A .trace dir is created with the given
// settings JSON (empty string skips writing the file).
func setupDirtyRepo(t *testing.T, settingsJSON string) string {
	t.Helper()
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...) //nolint:noctx // test setup
		cmd.Dir = tmpDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init")
	run("config", "user.name", "Test User")
	run("config", "user.email", "test@example.com")
	run("config", "commit.gpgsign", "false")

	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "base.txt"), []byte("base"), 0o644))

	traceDir := filepath.Join(tmpDir, ".trace")
	require.NoError(t, os.MkdirAll(traceDir, 0o755))
	if settingsJSON != "" {
		require.NoError(t, os.WriteFile(filepath.Join(traceDir, "settings.json"), []byte(settingsJSON), 0o644))
	}

	// Commit the baseline (including .trace/settings.json) so the starting
	// working tree is clean — tests then introduce their own dirtiness.
	run("add", "-A")
	run("commit", "-q", "-m", "initial")

	return tmpDir
}

func gitStatusPorcelain(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "status", "--porcelain") //nolint:noctx // test
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func headSubject(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "log", "-1", "--pretty=%s") //nolint:noctx // test
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func TestAutoCommitDirtyWorkingTree_CreatesWIPCommit(t *testing.T) {
	SetDirtyCommitsDisabled(false)
	t.Cleanup(func() { SetDirtyCommitsDisabled(false) })

	dir := setupDirtyRepo(t, `{"enabled": true}`) // dirty_commits unset -> default on

	// Make the tree dirty: modify a tracked file and add an untracked one.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "base.txt"), []byte("changed"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0o644))

	hash, err := AutoCommitDirtyWorkingTree(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, hash, "expected a WIP commit hash")

	if subj := headSubject(t, dir); subj != dirtyCommitMessage {
		t.Errorf("HEAD subject = %q, want %q", subj, dirtyCommitMessage)
	}
	if status := gitStatusPorcelain(t, dir); status != "" {
		t.Errorf("working tree should be clean after WIP commit, got:\n%s", status)
	}
}

func TestAutoCommitDirtyWorkingTree_DisabledByConfig(t *testing.T) {
	SetDirtyCommitsDisabled(false)
	t.Cleanup(func() { SetDirtyCommitsDisabled(false) })

	dir := setupDirtyRepo(t, `{"enabled": true, "dirty_commits": false}`)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "base.txt"), []byte("changed"), 0o644))

	hash, err := AutoCommitDirtyWorkingTree(context.Background())
	require.NoError(t, err)
	require.Empty(t, hash, "no WIP commit should be created when disabled")

	if subj := headSubject(t, dir); subj != "initial" {
		t.Errorf("HEAD should still be the initial commit, got %q", subj)
	}
	// The dirty change must remain uncommitted.
	if status := gitStatusPorcelain(t, dir); status == "" {
		t.Errorf("expected working tree to remain dirty when disabled")
	}
}

func TestAutoCommitDirtyWorkingTree_DisabledByFlag(t *testing.T) {
	dir := setupDirtyRepo(t, `{"enabled": true}`) // config default on

	SetDirtyCommitsDisabled(true) // simulate --no-dirty-commits
	t.Cleanup(func() { SetDirtyCommitsDisabled(false) })

	require.NoError(t, os.WriteFile(filepath.Join(dir, "base.txt"), []byte("changed"), 0o644))

	hash, err := AutoCommitDirtyWorkingTree(context.Background())
	require.NoError(t, err)
	require.Empty(t, hash, "--no-dirty-commits must override config")

	if subj := headSubject(t, dir); subj != "initial" {
		t.Errorf("HEAD should still be the initial commit, got %q", subj)
	}
}

func TestAutoCommitDirtyWorkingTree_CleanTreeNoOp(t *testing.T) {
	SetDirtyCommitsDisabled(false)
	t.Cleanup(func() { SetDirtyCommitsDisabled(false) })

	dir := setupDirtyRepo(t, `{"enabled": true}`)

	hash, err := AutoCommitDirtyWorkingTree(context.Background())
	require.NoError(t, err)
	require.Empty(t, hash, "clean tree should produce no WIP commit")

	if subj := headSubject(t, dir); subj != "initial" {
		t.Errorf("HEAD should still be the initial commit, got %q", subj)
	}
}
