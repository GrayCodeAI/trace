package strategy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	_ "github.com/GrayCodeAI/trace/cli/agent/claudecode"
	"github.com/GrayCodeAI/trace/cli/paths"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

func TestWorktreeRoot_Cache(t *testing.T) {
	// Uses t.Chdir + t.Setenv so cannot be parallel.
	tmpDir := t.TempDir()
	initTestRepo(t, tmpDir)
	t.Chdir(tmpDir)
	paths.ClearWorktreeRootCache()

	// First call populates the cache via git.
	got, err := paths.WorktreeRoot(context.Background())
	if err != nil {
		t.Fatalf("paths.WorktreeRoot(context.Background()) first call error: %v", err)
	}

	// Resolve symlinks for comparison (macOS /var -> /private/var).
	want, err := filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatalf("EvalSymlinks error: %v", err)
	}
	if got != want {
		t.Fatalf("paths.WorktreeRoot(context.Background()) = %q, want %q", got, want)
	}

	// Break git by pointing PATH at an empty directory.
	// If the second call hits git it will fail.
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	got2, err := paths.WorktreeRoot(context.Background())
	if err != nil {
		t.Fatalf("paths.WorktreeRoot(context.Background()) cached call should succeed, got error: %v", err)
	}
	if got2 != want {
		t.Fatalf("paths.WorktreeRoot(context.Background()) cached = %q, want %q", got2, want)
	}

	// After clearing the cache the broken PATH should cause a failure.
	paths.ClearWorktreeRootCache()
	_, err = paths.WorktreeRoot(context.Background())
	if err == nil {
		t.Fatal("paths.WorktreeRoot(context.Background()) should fail after cache clear with broken PATH")
	}
}

func TestWorktreeRoot_MainRepo(t *testing.T) {
	// In a normal (non-worktree) repo, WorktreeRoot returns the repo root.
	// Uses t.Chdir so cannot be parallel.
	tmpDir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(tmpDir)
	if err != nil {
		t.Fatalf("EvalSymlinks error: %v", err)
	}
	tmpDir = resolved

	initTestRepo(t, tmpDir)
	t.Chdir(tmpDir)
	paths.ClearWorktreeRootCache()

	got, err := paths.WorktreeRoot(context.Background())
	if err != nil {
		t.Fatalf("paths.WorktreeRoot(context.Background()) error: %v", err)
	}
	if got != tmpDir {
		t.Errorf("paths.WorktreeRoot(context.Background()) = %q, want repo root %q", got, tmpDir)
	}

	// Also works from a subdirectory.
	subDir := filepath.Join(tmpDir, "sub", "dir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}
	t.Chdir(subDir)
	paths.ClearWorktreeRootCache()

	got, err = paths.WorktreeRoot(context.Background())
	if err != nil {
		t.Fatalf("paths.WorktreeRoot(context.Background()) from subdir error: %v", err)
	}
	if got != tmpDir {
		t.Errorf("paths.WorktreeRoot(context.Background()) from subdir = %q, want repo root %q", got, tmpDir)
	}
}

func TestWorktreeRoot_Worktree(t *testing.T) {
	// In a git worktree, WorktreeRoot must return the worktree's own root,
	// NOT the main repository root. The worktree is placed in a separate
	// temp directory (sibling, not child) so the two paths share no prefix.
	// Uses t.Chdir so cannot be parallel.
	mainDir := t.TempDir()
	mainResolved, err := filepath.EvalSymlinks(mainDir)
	if err != nil {
		t.Fatalf("EvalSymlinks error: %v", err)
	}
	mainDir = mainResolved

	initTestRepo(t, mainDir)

	worktreeDir := t.TempDir()
	wtResolved, err := filepath.EvalSymlinks(worktreeDir)
	if err != nil {
		t.Fatalf("EvalSymlinks error: %v", err)
	}
	worktreeDir = wtResolved

	// t.TempDir() creates the directory; git worktree add needs it to not exist.
	if err := os.Remove(worktreeDir); err != nil {
		t.Fatalf("failed to remove temp dir for worktree: %v", err)
	}
	if err := createWorktree(mainDir, worktreeDir, "wt-branch"); err != nil {
		t.Fatalf("failed to create worktree: %v", err)
	}
	t.Cleanup(func() { removeWorktree(mainDir, worktreeDir) })

	t.Chdir(worktreeDir)
	paths.ClearWorktreeRootCache()

	got, err := paths.WorktreeRoot(context.Background())
	if err != nil {
		t.Fatalf("paths.WorktreeRoot(context.Background()) error: %v", err)
	}
	if got != worktreeDir {
		t.Errorf("paths.WorktreeRoot(context.Background()) = %q, want worktree root %q", got, worktreeDir)
	}
	if got == mainDir {
		t.Errorf("paths.WorktreeRoot(context.Background()) returned main repo root %q, must return worktree root", mainDir)
	}

	// Also works from a subdirectory within the worktree.
	subDir := filepath.Join(worktreeDir, "deep", "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}
	t.Chdir(subDir)
	paths.ClearWorktreeRootCache()

	got, err = paths.WorktreeRoot(context.Background())
	if err != nil {
		t.Fatalf("paths.WorktreeRoot(context.Background()) from worktree subdir error: %v", err)
	}
	if got != worktreeDir {
		t.Errorf("paths.WorktreeRoot(context.Background()) from worktree subdir = %q, want %q", got, worktreeDir)
	}
	if got == mainDir {
		t.Errorf("paths.WorktreeRoot(context.Background()) from worktree subdir returned main repo root %q", mainDir)
	}
}

func TestIsInsideWorktree(t *testing.T) {
	t.Run("main repo", func(t *testing.T) {
		tmpDir := t.TempDir()
		initTestRepo(t, tmpDir)
		t.Chdir(tmpDir)

		if IsInsideWorktree(context.Background()) {
			t.Error("IsInsideWorktree(context.Background()) should return false in main repo")
		}
	})

	t.Run("worktree", func(t *testing.T) {
		tmpDir := t.TempDir()
		initTestRepo(t, tmpDir)

		// Create a worktree
		worktreeDir := filepath.Join(tmpDir, "worktree")
		if err := createWorktree(tmpDir, worktreeDir, "test-branch"); err != nil {
			t.Fatalf("failed to create worktree: %v", err)
		}
		t.Cleanup(func() {
			removeWorktree(tmpDir, worktreeDir)
		})

		t.Chdir(worktreeDir)

		if !IsInsideWorktree(context.Background()) {
			t.Error("IsInsideWorktree(context.Background()) should return true in worktree")
		}
	})

	t.Run("non-repo", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		if IsInsideWorktree(context.Background()) {
			t.Error("IsInsideWorktree(context.Background()) should return false in non-repo")
		}
	})
}

func TestGetMainRepoRoot(t *testing.T) {
	t.Run("main repo", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Resolve symlinks (macOS /var -> /private/var)
		// git rev-parse --show-toplevel returns the resolved path
		resolved, err := filepath.EvalSymlinks(tmpDir)
		if err != nil {
			t.Fatalf("filepath.EvalSymlinks() failed: %v", err)
		}
		tmpDir = resolved

		initTestRepo(t, tmpDir)
		t.Chdir(tmpDir)

		root, err := GetMainRepoRoot(context.Background())
		if err != nil {
			t.Fatalf("GetMainRepoRoot(context.Background()) failed: %v", err)
		}

		if root != tmpDir {
			t.Errorf("GetMainRepoRoot(context.Background()) = %q, want %q", root, tmpDir)
		}
	})

	t.Run("worktree", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Resolve symlinks (macOS /var -> /private/var)
		resolved, err := filepath.EvalSymlinks(tmpDir)
		if err != nil {
			t.Fatalf("filepath.EvalSymlinks() failed: %v", err)
		}
		tmpDir = resolved

		initTestRepo(t, tmpDir)

		worktreeDir := filepath.Join(tmpDir, "worktree")
		if err := createWorktree(tmpDir, worktreeDir, "test-branch"); err != nil {
			t.Fatalf("failed to create worktree: %v", err)
		}
		t.Cleanup(func() {
			removeWorktree(tmpDir, worktreeDir)
		})

		t.Chdir(worktreeDir)

		root, err := GetMainRepoRoot(context.Background())
		if err != nil {
			t.Fatalf("GetMainRepoRoot(context.Background()) failed: %v", err)
		}

		if root != tmpDir {
			t.Errorf("GetMainRepoRoot(context.Background()) = %q, want %q", root, tmpDir)
		}
	})
}

func TestGetCurrentBranchName(t *testing.T) {
	t.Run("on branch", func(t *testing.T) {
		tmpDir := t.TempDir()
		repo, err := git.PlainInit(tmpDir, false)
		if err != nil {
			t.Fatalf("failed to init repo: %v", err)
		}

		// Create initial commit
		testFile := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}

		wt, err := repo.Worktree()
		if err != nil {
			t.Fatalf("failed to get worktree: %v", err)
		}

		if _, err := wt.Add("test.txt"); err != nil {
			t.Fatalf("failed to add file: %v", err)
		}

		if _, err := wt.Commit("Initial commit", &git.CommitOptions{
			Author: &object.Signature{Name: "Test", Email: "test@test.com"},
		}); err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		// Should be on default branch (master or main)
		branchName := GetCurrentBranchName(repo)
		if branchName == "" {
			t.Error("GetCurrentBranchName() returned empty string, expected branch name")
		}

		// Create and checkout a new branch
		head, err := repo.Head()
		if err != nil {
			t.Fatalf("failed to get HEAD: %v", err)
		}

		newBranch := "feature/test-branch"
		if err := wt.Checkout(&git.CheckoutOptions{
			Branch: plumbing.NewBranchReferenceName(newBranch),
			Create: true,
			Hash:   head.Hash(),
		}); err != nil {
			t.Fatalf("failed to checkout branch: %v", err)
		}

		// Should return the new branch name
		branchName = GetCurrentBranchName(repo)
		if branchName != newBranch {
			t.Errorf("GetCurrentBranchName() = %q, want %q", branchName, newBranch)
		}
	})

	t.Run("detached HEAD", func(t *testing.T) {
		tmpDir := t.TempDir()
		repo, err := git.PlainInit(tmpDir, false)
		if err != nil {
			t.Fatalf("failed to init repo: %v", err)
		}

		// Create initial commit
		testFile := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to write file: %v", err)
		}

		wt, err := repo.Worktree()
		if err != nil {
			t.Fatalf("failed to get worktree: %v", err)
		}

		if _, err := wt.Add("test.txt"); err != nil {
			t.Fatalf("failed to add file: %v", err)
		}

		commitHash, err := wt.Commit("Initial commit", &git.CommitOptions{
			Author: &object.Signature{Name: "Test", Email: "test@test.com"},
		})
		if err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		// Checkout the commit directly (detached HEAD)
		if err := wt.Checkout(&git.CheckoutOptions{Hash: commitHash}); err != nil {
			t.Fatalf("failed to checkout commit: %v", err)
		}

		// Should return empty string for detached HEAD
		branchName := GetCurrentBranchName(repo)
		if branchName != "" {
			t.Errorf("GetCurrentBranchName() = %q, want empty string for detached HEAD", branchName)
		}
	})
}

// initTestRepo creates a git repo with an initial commit
func initTestRepo(t *testing.T, dir string) {
	t.Helper()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init repo: %v", err)
	}

	testFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test"), 0o600); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}

	if _, err := wt.Add("README.md"); err != nil {
		t.Fatalf("failed to add: %v", err)
	}

	if _, err := wt.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test.com"},
	}); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}
}

// createWorktree creates a git worktree using native git command
func createWorktree(repoDir, worktreeDir, branch string) error {
	cmd := exec.CommandContext(context.Background(), "git", "worktree", "add", worktreeDir, "-b", branch)
	cmd.Dir = repoDir
	return cmd.Run()
}

// removeWorktree removes a git worktree using native git command
func removeWorktree(repoDir, worktreeDir string) {
	cmd := exec.CommandContext(context.Background(), "git", "worktree", "remove", worktreeDir, "--force")
	cmd.Dir = repoDir
	_ = cmd.Run() //nolint:errcheck // Best effort cleanup, ignore errors
}

func TestGetDefaultBranchName(t *testing.T) {
	t.Run("returns main when main branch exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		repo, err := git.PlainInit(tmpDir, false)
		if err != nil {
			t.Fatalf("failed to init repo: %v", err)
		}

		// Create initial commit (go-git creates master by default)
		testFile := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		wt, err := repo.Worktree()
		if err != nil {
			t.Fatalf("failed to get worktree: %v", err)
		}

		if _, err := wt.Add("test.txt"); err != nil {
			t.Fatalf("failed to add file: %v", err)
		}

		commitHash, err := wt.Commit("Initial commit", &git.CommitOptions{
			Author: &object.Signature{Name: "Test", Email: "test@example.com"},
		})
		if err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		// Create main branch
		ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), commitHash)
		if err := repo.Storer.SetReference(ref); err != nil {
			t.Fatalf("failed to create main branch: %v", err)
		}

		result := GetDefaultBranchName(repo)

		if result != "main" {
			t.Errorf("GetDefaultBranchName() = %q, want %q", result, "main")
		}
	})

	t.Run("returns master when only master exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		repo, err := git.PlainInit(tmpDir, false)
		if err != nil {
			t.Fatalf("failed to init repo: %v", err)
		}

		// Create initial commit (go-git creates master by default)
		testFile := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		wt, err := repo.Worktree()
		if err != nil {
			t.Fatalf("failed to get worktree: %v", err)
		}

		if _, err := wt.Add("test.txt"); err != nil {
			t.Fatalf("failed to add file: %v", err)
		}

		if _, err := wt.Commit("Initial commit", &git.CommitOptions{
			Author: &object.Signature{Name: "Test", Email: "test@example.com"},
		}); err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		result := GetDefaultBranchName(repo)

		if result != "master" {
			t.Errorf("GetDefaultBranchName() = %q, want %q", result, "master")
		}
	})

	t.Run("returns empty when no main or master", func(t *testing.T) {
		tmpDir := t.TempDir()
		repo, err := git.PlainInit(tmpDir, false)
		if err != nil {
			t.Fatalf("failed to init repo: %v", err)
		}

		// Create initial commit
		testFile := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		wt, err := repo.Worktree()
		if err != nil {
			t.Fatalf("failed to get worktree: %v", err)
		}

		if _, err := wt.Add("test.txt"); err != nil {
			t.Fatalf("failed to add file: %v", err)
		}

		commitHash, err := wt.Commit("Initial commit", &git.CommitOptions{
			Author: &object.Signature{Name: "Test", Email: "test@example.com"},
		})
		if err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		// Create a different branch and delete master
		if err := wt.Checkout(&git.CheckoutOptions{
			Hash:   commitHash,
			Branch: plumbing.NewBranchReferenceName("develop"),
			Create: true,
		}); err != nil {
			t.Fatalf("failed to create develop branch: %v", err)
		}

		// Delete master branch
		if err := repo.Storer.RemoveReference(plumbing.NewBranchReferenceName("master")); err != nil {
			t.Fatalf("failed to delete master branch: %v", err)
		}

		result := GetDefaultBranchName(repo)
		if result != "" {
			t.Errorf("GetDefaultBranchName() = %q, want empty string", result)
		}
	})

	t.Run("returns origin/HEAD target when set", func(t *testing.T) {
		tmpDir := t.TempDir()
		repo, err := git.PlainInit(tmpDir, false)
		if err != nil {
			t.Fatalf("failed to init repo: %v", err)
		}

		// Create initial commit
		testFile := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		wt, err := repo.Worktree()
		if err != nil {
			t.Fatalf("failed to get worktree: %v", err)
		}

		if _, err := wt.Add("test.txt"); err != nil {
			t.Fatalf("failed to add file: %v", err)
		}

		commitHash, err := wt.Commit("Initial commit", &git.CommitOptions{
			Author: &object.Signature{Name: "Test", Email: "test@example.com"},
		})
		if err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		// Create trunk branch (simulate non-standard default branch)
		trunkRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName("trunk"), commitHash)
		if err := repo.Storer.SetReference(trunkRef); err != nil {
			t.Fatalf("failed to create trunk branch: %v", err)
		}

		// Create origin/trunk remote ref
		originTrunkRef := plumbing.NewHashReference(plumbing.NewRemoteReferenceName("origin", "trunk"), commitHash)
		if err := repo.Storer.SetReference(originTrunkRef); err != nil {
			t.Fatalf("failed to create origin/trunk ref: %v", err)
		}

		// Set origin/HEAD to point to origin/trunk (symbolic ref)
		originHeadRef := plumbing.NewSymbolicReference(plumbing.ReferenceName("refs/remotes/origin/HEAD"), plumbing.ReferenceName("refs/remotes/origin/trunk"))
		if err := repo.Storer.SetReference(originHeadRef); err != nil {
			t.Fatalf("failed to set origin/HEAD: %v", err)
		}

		// Delete master branch so it doesn't take precedence
		_ = repo.Storer.RemoveReference(plumbing.NewBranchReferenceName("master")) //nolint:errcheck // best-effort cleanup for test

		result := GetDefaultBranchName(repo)

		if result != "trunk" {
			t.Errorf("GetDefaultBranchName() = %q, want %q", result, "trunk")
		}
	})
}

func TestIsOnDefaultBranch(t *testing.T) {
	t.Run("returns true when on main", func(t *testing.T) {
		tmpDir := t.TempDir()
		repo, err := git.PlainInit(tmpDir, false)
		if err != nil {
			t.Fatalf("failed to init repo: %v", err)
		}

		// Create initial commit
		testFile := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		wt, err := repo.Worktree()
		if err != nil {
			t.Fatalf("failed to get worktree: %v", err)
		}

		if _, err := wt.Add("test.txt"); err != nil {
			t.Fatalf("failed to add file: %v", err)
		}

		commitHash, err := wt.Commit("Initial commit", &git.CommitOptions{
			Author: &object.Signature{Name: "Test", Email: "test@example.com"},
		})
		if err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		// Create and checkout main branch
		ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), commitHash)
		if err := repo.Storer.SetReference(ref); err != nil {
			t.Fatalf("failed to create main branch: %v", err)
		}
		if err := wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("main")}); err != nil {
			t.Fatalf("failed to checkout main: %v", err)
		}

		isDefault, branchName := IsOnDefaultBranch(repo)
		if !isDefault {
			t.Error("IsOnDefaultBranch() = false, want true when on main")
		}
		if branchName != "main" {
			t.Errorf("branchName = %q, want %q", branchName, "main")
		}
	})

	t.Run("returns true when on master", func(t *testing.T) {
		tmpDir := t.TempDir()
		repo, err := git.PlainInit(tmpDir, false)
		if err != nil {
			t.Fatalf("failed to init repo: %v", err)
		}

		// Create initial commit (go-git creates master by default)
		testFile := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		wt, err := repo.Worktree()
		if err != nil {
			t.Fatalf("failed to get worktree: %v", err)
		}

		if _, err := wt.Add("test.txt"); err != nil {
			t.Fatalf("failed to add file: %v", err)
		}

		if _, err := wt.Commit("Initial commit", &git.CommitOptions{
			Author: &object.Signature{Name: "Test", Email: "test@example.com"},
		}); err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		isDefault, branchName := IsOnDefaultBranch(repo)
		if !isDefault {
			t.Error("IsOnDefaultBranch() = false, want true when on master")
		}
		if branchName != "master" {
			t.Errorf("branchName = %q, want %q", branchName, "master")
		}
	})

	t.Run("returns false when on feature branch", func(t *testing.T) {
		tmpDir := t.TempDir()
		repo, err := git.PlainInit(tmpDir, false)
		if err != nil {
			t.Fatalf("failed to init repo: %v", err)
		}

		// Create initial commit
		testFile := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		wt, err := repo.Worktree()
		if err != nil {
			t.Fatalf("failed to get worktree: %v", err)
		}

		if _, err := wt.Add("test.txt"); err != nil {
			t.Fatalf("failed to add file: %v", err)
		}

		commitHash, err := wt.Commit("Initial commit", &git.CommitOptions{
			Author: &object.Signature{Name: "Test", Email: "test@example.com"},
		})
		if err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		// Create and checkout feature branch
		if err := wt.Checkout(&git.CheckoutOptions{
			Hash:   commitHash,
			Branch: plumbing.NewBranchReferenceName("feature/test"),
			Create: true,
		}); err != nil {
			t.Fatalf("failed to create feature branch: %v", err)
		}

		isDefault, branchName := IsOnDefaultBranch(repo)
		if isDefault {
			t.Error("IsOnDefaultBranch() = true, want false when on feature branch")
		}
		if branchName != "feature/test" {
			t.Errorf("branchName = %q, want %q", branchName, "feature/test")
		}
	})

	t.Run("returns false for detached HEAD", func(t *testing.T) {
		tmpDir := t.TempDir()
		repo, err := git.PlainInit(tmpDir, false)
		if err != nil {
			t.Fatalf("failed to init repo: %v", err)
		}

		// Create initial commit
		testFile := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		wt, err := repo.Worktree()
		if err != nil {
			t.Fatalf("failed to get worktree: %v", err)
		}

		if _, err := wt.Add("test.txt"); err != nil {
			t.Fatalf("failed to add file: %v", err)
		}

		commitHash, err := wt.Commit("Initial commit", &git.CommitOptions{
			Author: &object.Signature{Name: "Test", Email: "test@example.com"},
		})
		if err != nil {
			t.Fatalf("failed to commit: %v", err)
		}

		// Checkout to detached HEAD state
		if err := wt.Checkout(&git.CheckoutOptions{Hash: commitHash}); err != nil {
			t.Fatalf("failed to checkout detached HEAD: %v", err)
		}

		isDefault, branchName := IsOnDefaultBranch(repo)
		if isDefault {
			t.Error("IsOnDefaultBranch() = true, want false for detached HEAD")
		}
		if branchName != "" {
			t.Errorf("branchName = %q, want empty string for detached HEAD", branchName)
		}
	})
}

// resetProtectedDirsForTest resets the cached protected dirs so tests that
// manipulate the agent registry can get fresh results. Call this in any test
// that registers/unregisters agents and then checks isProtectedPath behavior.
//
//lint:ignore U1000 // Intentionally kept as a test utility for future tests that mutate the agent registry.
func resetProtectedDirsForTest() {
	protectedDirsOnce = sync.Once{}
	protectedDirsCache = nil
}
