package strategy

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/paths"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

func TestIsShadowBranch(t *testing.T) {
	tests := []struct {
		name       string
		branchName string
		want       bool
	}{
		// Valid shadow branches - old format (7+ hex chars)
		{"old format: 7 hex chars", "trace/abc1234", true},
		{"old format: 7 hex chars numeric", "trace/1234567", true},
		{"old format: full commit hash", "trace/abcdef0123456789abcdef0123456789abcdef01", true},
		{"old format: mixed case hex", "trace/AbCdEf1", true},

		// Valid shadow branches - new format with worktree hash (12 hex + dash + 10 hex)
		{"new format: standard", "trace/abc1234-e3b0c4", true},
		{"new format: numeric worktree hash", "trace/1234567-123456", true},
		{"new format: full commit with worktree", "trace/abcdef0123456789-fedcba", true},
		{"new format: mixed case", "trace/AbCdEf1-AbCdEf", true},

		// Invalid patterns
		{"empty after prefix", "trace/", false},
		{"too short commit (6 chars)", "trace/abc123", false},
		{"too short commit (1 char)", "trace/a", false},
		{"non-hex chars in commit", "trace/ghijklm", false},
		{"sessions branch", paths.MetadataBranchName, false},
		{"no prefix", "abc1234", false},
		{"wrong prefix", "feature/abc1234", false},
		{"main branch", "main", false},
		{"master branch", "master", false},
		{"empty string", "", false},
		{"just trace", "trace", false},
		{"trace with slash only", "trace/", false},
		{"worktree hash too short (5 chars)", "trace/abc1234-e3b0c", false},
		{"worktree hash too long (11 chars)", "trace/abc1234-e3b0c442987", false},
		{"non-hex in worktree hash", "trace/abc1234-ghijkl", false},
		{"missing commit hash", "trace/-e3b0c4", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsShadowBranch(tt.branchName)
			if got != tt.want {
				t.Errorf("IsShadowBranch(%q) = %v, want %v", tt.branchName, got, tt.want)
			}
		})
	}
}

func TestListShadowBranches(t *testing.T) {
	// Setup: create a temp git repo with various branches
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	// Create initial commit so we have something to branch from
	emptyTreeHash := plumbing.NewHash("4b825dc642cb6eb9a060e54bf8d69288fbee4904")
	commitHash, err := checkpoint.CreateCommit(context.Background(), repo, emptyTreeHash, plumbing.ZeroHash, "initial commit", "test", "test@test.com")
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create HEAD reference pointing to master
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("master"))
	if err := repo.Storer.SetReference(headRef); err != nil {
		t.Fatalf("failed to set HEAD: %v", err)
	}
	masterRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName("master"), commitHash)
	if err := repo.Storer.SetReference(masterRef); err != nil {
		t.Fatalf("failed to set master: %v", err)
	}

	// Create various branches
	branches := []struct {
		name     string
		isShadow bool
	}{
		{"trace/abc1234", true},
		{"trace/def5678", true},
		{paths.MetadataBranchName, false}, // Should NOT be listed
		{"feature/foo", false},
		{"main", false},
	}

	for _, b := range branches {
		ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(b.name), commitHash)
		if err := repo.Storer.SetReference(ref); err != nil {
			t.Fatalf("failed to create branch %s: %v", b.name, err)
		}
	}

	// Test ListShadowBranches
	shadowBranches, err := ListShadowBranches(context.Background())
	if err != nil {
		t.Fatalf("ListShadowBranches(context.Background()) error = %v", err)
	}

	// Should have exactly 2 shadow branches
	if len(shadowBranches) != 2 {
		t.Errorf("ListShadowBranches(context.Background()) returned %d branches, want 2: %v", len(shadowBranches), shadowBranches)
	}

	// Check that the expected branches are present
	shadowSet := make(map[string]bool)
	for _, b := range shadowBranches {
		shadowSet[b] = true
	}

	if !shadowSet["trace/abc1234"] {
		t.Error("ListShadowBranches(context.Background()) missing 'trace/abc1234'")
	}
	if !shadowSet["trace/def5678"] {
		t.Error("ListShadowBranches(context.Background()) missing 'trace/def5678'")
	}
	if shadowSet[paths.MetadataBranchName] {
		t.Errorf("ListShadowBranches(context.Background()) should not include '%s'", paths.MetadataBranchName)
	}
}

func TestListShadowBranches_Empty(t *testing.T) {
	// Setup: create a temp git repo with no shadow branches
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	// Create initial commit
	emptyTreeHash := plumbing.NewHash("4b825dc642cb6eb9a060e54bf8d69288fbee4904")
	commitHash, err := checkpoint.CreateCommit(context.Background(), repo, emptyTreeHash, plumbing.ZeroHash, "initial commit", "test", "test@test.com")
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create HEAD reference
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("master"))
	if err := repo.Storer.SetReference(headRef); err != nil {
		t.Fatalf("failed to set HEAD: %v", err)
	}
	masterRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName("master"), commitHash)
	if err := repo.Storer.SetReference(masterRef); err != nil {
		t.Fatalf("failed to set master: %v", err)
	}

	// Test ListShadowBranches returns empty slice (not nil)
	shadowBranches, err := ListShadowBranches(context.Background())
	if err != nil {
		t.Fatalf("ListShadowBranches(context.Background()) error = %v", err)
	}

	if shadowBranches == nil {
		t.Error("ListShadowBranches(context.Background()) returned nil, want empty slice")
	}

	if len(shadowBranches) != 0 {
		t.Errorf("ListShadowBranches(context.Background()) returned %d branches, want 0", len(shadowBranches))
	}
}

func TestDeleteShadowBranches(t *testing.T) {
	// Setup: create a temp git repo with shadow branches
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	// Create initial commit
	emptyTreeHash := plumbing.NewHash("4b825dc642cb6eb9a060e54bf8d69288fbee4904")
	commitHash, err := checkpoint.CreateCommit(context.Background(), repo, emptyTreeHash, plumbing.ZeroHash, "initial commit", "test", "test@test.com")
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create HEAD reference
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("master"))
	if err := repo.Storer.SetReference(headRef); err != nil {
		t.Fatalf("failed to set HEAD: %v", err)
	}
	masterRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName("master"), commitHash)
	if err := repo.Storer.SetReference(masterRef); err != nil {
		t.Fatalf("failed to set master: %v", err)
	}

	// Create shadow branches
	shadowBranches := []string{"trace/abc1234", "trace/def5678"}
	for _, b := range shadowBranches {
		ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(b), commitHash)
		if err := repo.Storer.SetReference(ref); err != nil {
			t.Fatalf("failed to create branch %s: %v", b, err)
		}
	}

	// Delete shadow branches
	deleted, failed, err := DeleteShadowBranches(context.Background(), shadowBranches)
	if err != nil {
		t.Fatalf("DeleteShadowBranches() error = %v", err)
	}

	// All should be deleted successfully
	if len(deleted) != 2 {
		t.Errorf("DeleteShadowBranches() deleted %d branches, want 2", len(deleted))
	}
	if len(failed) != 0 {
		t.Errorf("DeleteShadowBranches() failed %d branches, want 0: %v", len(failed), failed)
	}

	// Verify branches are actually deleted using git CLI
	// (go-git may have stale cached refs, so use the same mechanism as production code)
	for _, b := range shadowBranches {
		cmd := exec.CommandContext(context.Background(), "git", "branch", "--list", b)
		output, cmdErr := cmd.Output()
		if cmdErr != nil {
			t.Fatalf("git branch --list failed: %v", cmdErr)
		}
		if strings.TrimSpace(string(output)) != "" {
			t.Errorf("Branch %s still exists after deletion (git branch --list returned: %q)", b, strings.TrimSpace(string(output)))
		}
	}
}

func TestDeleteShadowBranches_NonExistent(t *testing.T) {
	// Setup: create a temp git repo
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	// Create initial commit
	emptyTreeHash := plumbing.NewHash("4b825dc642cb6eb9a060e54bf8d69288fbee4904")
	commitHash, err := checkpoint.CreateCommit(context.Background(), repo, emptyTreeHash, plumbing.ZeroHash, "initial commit", "test", "test@test.com")
	if err != nil {
		t.Fatalf("failed to create initial commit: %v", err)
	}

	// Create HEAD reference
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("master"))
	if err := repo.Storer.SetReference(headRef); err != nil {
		t.Fatalf("failed to set HEAD: %v", err)
	}
	masterRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName("master"), commitHash)
	if err := repo.Storer.SetReference(masterRef); err != nil {
		t.Fatalf("failed to set master: %v", err)
	}

	// Try to delete non-existent branches
	nonExistent := []string{"trace/doesnotexist"}
	deleted, failed, err := DeleteShadowBranches(context.Background(), nonExistent)
	if err != nil {
		t.Fatalf("DeleteShadowBranches() error = %v", err)
	}

	// Should have one failed branch
	if len(deleted) != 0 {
		t.Errorf("DeleteShadowBranches() deleted %d branches, want 0", len(deleted))
	}
	if len(failed) != 1 {
		t.Errorf("DeleteShadowBranches() failed %d branches, want 1", len(failed))
	}
}

func TestDeleteShadowBranches_Empty(t *testing.T) {
	// Setup: create a temp git repo
	dir := t.TempDir()
	_, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)

	// Delete empty list should return empty results
	deleted, failed, err := DeleteShadowBranches(context.Background(), []string{})
	if err != nil {
		t.Fatalf("DeleteShadowBranches() error = %v", err)
	}

	if len(deleted) != 0 || len(failed) != 0 {
		t.Errorf("DeleteShadowBranches([]) = (%v, %v), want ([], [])", deleted, failed)
	}
}

// TestListOrphanedSessionStates_RecentSessionNotOrphaned tests that recently started
// sessions are NOT marked as orphaned, even if they have no checkpoints yet.
//
// P1 Bug: A session that just started (via InitializeSession) but hasn't created
// its first checkpoint yet would be incorrectly marked as orphaned because it has:
// - A session state file
// - No checkpoints on trace/checkpoints/v1
// - No shadow branch before first checkpoint
//
// This test should FAIL with the current implementation, demonstrating the bug.
