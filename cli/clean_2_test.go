package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/go-git/go-git/v6/plumbing"
)

func TestCleanCmd_All_SessionsBranchPreserved(t *testing.T) {
	repo, commitHash := setupCleanTestRepo(t)

	shadowRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName("trace/abc1234"), commitHash)
	if err := repo.Storer.SetReference(shadowRef); err != nil {
		t.Fatalf("failed to create shadow branch: %v", err)
	}

	sessionsRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), commitHash)
	if err := repo.Storer.SetReference(sessionsRef); err != nil {
		t.Fatalf("failed to create trace/checkpoints/v1: %v", err)
	}

	cmd := newCleanCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--all", "--force"})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("clean --all --force error = %v", err)
	}

	// Shadow branch should be deleted
	refName := plumbing.NewBranchReferenceName("trace/abc1234")
	if _, err := repo.Reference(refName, true); err == nil {
		t.Error("Shadow branch should be deleted")
	}

	// Sessions branch should still exist
	sessionsRefName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	if _, err := repo.Reference(sessionsRefName, true); err != nil {
		t.Error("trace/checkpoints/v1 branch should be preserved")
	}
}

func TestCleanCmd_All_NotGitRepository(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	paths.ClearWorktreeRootCache()

	cmd := newCleanCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--all"})

	err := cmd.Execute()
	// Should return error for non-git directory
	if err == nil {
		t.Error("clean --all should return error for non-git directory")
	}
}

func TestCleanCmd_All_InvalidSettingsWarnsAndContinues(t *testing.T) {
	repo, _ := setupCleanTestRepo(t)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	repoRoot := wt.Filesystem().Root()

	writeCleanSettingsFile(t, repoRoot, `{"enabled": true,`)

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--all", "--dry-run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("clean --all --dry-run error = %v", err)
	}

	if !strings.Contains(stderr.String(), "Warning: failed to load settings") {
		t.Fatalf("expected settings warning, got stderr=%q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "No items to clean up.") {
		t.Fatalf("expected command to continue cleanup flow, got stdout=%q", stdout.String())
	}
}

func TestCleanCmd_All_Subdirectory(t *testing.T) {
	repo, commitHash := setupCleanTestRepo(t)

	shadowRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName("trace/abc1234"), commitHash)
	if err := repo.Storer.SetReference(shadowRef); err != nil {
		t.Fatalf("failed to create shadow branch: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	repoRoot := wt.Filesystem().Root()
	subDir := filepath.Join(repoRoot, "subdir")
	if err := wt.Filesystem().MkdirAll("subdir", 0o755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	t.Chdir(subDir)
	paths.ClearWorktreeRootCache()

	cmd := newCleanCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--all", "--dry-run"})

	err = cmd.Execute()
	if err != nil {
		t.Fatalf("clean --all --dry-run from subdirectory error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "trace/abc1234") {
		t.Errorf("Should find shadow branches from subdirectory, got: %s", output)
	}
}

// Regression test: --all should find sessions that have a shadow branch.
// Previously, --all only cleaned orphaned sessions (no shadow branch AND no checkpoints),
// so active sessions with a shadow branch were invisible to --all.
func TestCleanCmd_All_FindsSessionWithShadowBranch(t *testing.T) {
	repo, commitHash := setupCleanTestRepo(t)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	worktreePath := wt.Filesystem().Root()
	worktreeID, err := paths.GetWorktreeID(worktreePath)
	if err != nil {
		t.Fatalf("failed to get worktree ID: %v", err)
	}

	// Create shadow branch for the session's base commit
	shadowBranch := checkpoint.ShadowBranchNameForCommit(commitHash.String(), worktreeID)
	shadowRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(shadowBranch), commitHash)
	if err := repo.Storer.SetReference(shadowRef); err != nil {
		t.Fatalf("failed to create shadow branch: %v", err)
	}

	// Create session state file — this session HAS a shadow branch,
	// so it was NOT considered orphaned by the old --all behavior
	sessionFile := createSessionStateFile(t, worktreePath, "2026-02-02-active-session", commitHash)

	cmd := newCleanCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--all", "--force"})

	err = cmd.Execute()
	if err != nil {
		t.Fatalf("clean --all --force error = %v", err)
	}

	output := stdout.String()

	// Session should be cleaned
	if _, err := os.Stat(sessionFile); !os.IsNotExist(err) {
		t.Error("session state file should be deleted by --all")
	}

	// Shadow branch should be cleaned
	refName := plumbing.NewBranchReferenceName(shadowBranch)
	if _, err := repo.Reference(refName, true); err == nil {
		t.Error("shadow branch should be deleted by --all")
	}

	if !strings.Contains(output, "Deleted") {
		t.Errorf("Expected 'Deleted' in output, got: %s", output)
	}
}

func TestCleanCmd_All_DryRunListsEligibleV2Generations(t *testing.T) {
	repo, _ := setupCleanTestRepo(t)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	repoRoot := wt.Filesystem().Root()

	writeCleanSettingsFile(t, repoRoot, `{"enabled": true, "strategy_options": {"checkpoints_v2": true, "full_transcript_generation_retention_days": 14}}`)
	createArchivedGenerationRef(t, repo, "0000000000001", time.Now().AddDate(0, 0, -20), time.Now().AddDate(0, 0, -15))

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--all", "--dry-run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("clean --all --dry-run error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Archived v2 generations (1):") {
		t.Fatalf("expected archived v2 generation section, got: %s", output)
	}
	if !strings.Contains(output, "0000000000001") {
		t.Fatalf("expected archived generation ref in output, got: %s", output)
	}
}

func TestCleanCmd_All_DryRunListsRemoteOnlyEligibleV2Generations(t *testing.T) {
	repo, _ := setupCleanTestRepo(t)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	repoRoot := wt.Filesystem().Root()

	writeCleanSettingsFile(t, repoRoot, `{"enabled": true, "strategy_options": {"checkpoints_v2": true, "full_transcript_generation_retention_days": 14}}`)
	addCleanBareOrigin(t, repoRoot)
	createRemoteOnlyArchivedGenerationRef(t, repo, repoRoot, "0000000000001", time.Now().AddDate(0, 0, -20), time.Now().AddDate(0, 0, -15))

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--all", "--dry-run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("clean --all --dry-run error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Archived v2 generations (1):") {
		t.Fatalf("expected archived v2 generation section, got: %s", output)
	}
	if !strings.Contains(output, "0000000000001") {
		t.Fatalf("expected remote-only archived generation ref in output, got: %s", output)
	}
	if _, err := repo.Reference(plumbing.ReferenceName(paths.V2FullRefPrefix+"0000000000001"), true); err == nil {
		t.Fatal("dry-run should not leave remote-only archived generation as a local ref")
	}
	if _, err := repo.Reference(plumbing.ReferenceName("refs/trace-clean-tmp/v2/full/0000000000001"), true); err == nil {
		t.Fatal("dry-run should remove temporary fetched generation ref")
	}
}

func TestCleanCmd_All_UsesRawTranscriptTimeForV2GenerationRetention(t *testing.T) {
	repo, _ := setupCleanTestRepo(t)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	repoRoot := wt.Filesystem().Root()

	writeCleanSettingsFile(t, repoRoot, `{"enabled": true, "strategy_options": {"checkpoints_v2": true, "full_transcript_generation_retention_days": 14}}`)

	cpID := id.MustCheckpointID("aabbccddeeff")
	createV2MainMetadataRef(t, repo, cpID, time.Now())
	createArchivedGenerationRefWithRawTranscript(t, repo, "0000000000005", cpID,
		time.Now(), time.Now(),
		time.Now().AddDate(0, 0, -20), time.Now().AddDate(0, 0, -15))

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--all", "--dry-run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("clean --all --dry-run error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Archived v2 generations (1):") {
		t.Fatalf("expected archived v2 generation section, got: %s", output)
	}
	if !strings.Contains(output, "0000000000005") {
		t.Fatalf("expected generation to be eligible by raw transcript timestamps, got: %s", output)
	}
}

func TestCleanCmd_All_ForceDeletesRemoteOnlyEligibleV2Generations(t *testing.T) {
	repo, _ := setupCleanTestRepo(t)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	repoRoot := wt.Filesystem().Root()

	writeCleanSettingsFile(t, repoRoot, `{"enabled": true, "strategy_options": {"checkpoints_v2": true, "full_transcript_generation_retention_days": 14}}`)
	addCleanBareOrigin(t, repoRoot)
	refOID := createRemoteOnlyArchivedGenerationRef(t, repo, repoRoot, "0000000000006", time.Now().AddDate(0, 0, -20), time.Now().AddDate(0, 0, -15))

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--all", "--force"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("clean --all --force error = %v", err)
	}

	if _, err := repo.Reference(plumbing.ReferenceName(paths.V2FullRefPrefix+"0000000000006"), true); err == nil {
		t.Fatal("remote-only archived generation should not be left locally")
	}
	remoteOutput := runCleanGit(t, repoRoot, "ls-remote", "origin", paths.V2FullRefPrefix+"0000000000006")
	if strings.Contains(remoteOutput, refOID) {
		t.Fatalf("expected remote archived generation to be deleted, got: %s", remoteOutput)
	}
	if !strings.Contains(stdout.String(), "Archived v2 generations") {
		t.Fatalf("expected deletion output to include archived v2 generations, got: %s", stdout.String())
	}
}

func TestCleanCmd_All_ForceDeletesEligibleV2Generations(t *testing.T) {
	repo, _ := setupCleanTestRepo(t)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	repoRoot := wt.Filesystem().Root()

	writeCleanSettingsFile(t, repoRoot, `{"enabled": true, "strategy_options": {"checkpoints_v2": true, "full_transcript_generation_retention_days": 14}}`)
	createCleanV2Ref(t, repo, plumbing.ReferenceName(paths.V2MainRefName))
	createCleanV2Ref(t, repo, plumbing.ReferenceName(paths.V2FullCurrentRefName))
	createArchivedGenerationRef(t, repo, "0000000000002", time.Now().AddDate(0, 0, -20), time.Now().AddDate(0, 0, -15))

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--all", "--force"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("clean --all --force error = %v", err)
	}

	if _, err := repo.Reference(plumbing.ReferenceName(paths.V2FullRefPrefix+"0000000000002"), true); err == nil {
		t.Fatal("archived v2 generation ref should be deleted")
	}
	if _, err := repo.Reference(plumbing.ReferenceName(paths.V2MainRefName), true); err != nil {
		t.Fatalf("v2 main ref should remain: %v", err)
	}
	if _, err := repo.Reference(plumbing.ReferenceName(paths.V2FullCurrentRefName), true); err != nil {
		t.Fatalf("v2 full current ref should remain: %v", err)
	}
}

func TestCleanCmd_All_DryRunSkipsV2GenerationsWithinRetention(t *testing.T) {
	repo, _ := setupCleanTestRepo(t)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	repoRoot := wt.Filesystem().Root()

	writeCleanSettingsFile(t, repoRoot, `{"enabled": true, "strategy_options": {"checkpoints_v2": true, "full_transcript_generation_retention_days": 14}}`)
	createArchivedGenerationRef(t, repo, "0000000000003", time.Now().AddDate(0, 0, -5), time.Now().AddDate(0, 0, -1))

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--all", "--dry-run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("clean --all --dry-run error = %v", err)
	}

	output := stdout.String()
	if strings.Contains(output, "Archived v2 generations") {
		t.Fatalf("did not expect archived v2 generation section for retained generation, got: %s", output)
	}
	if strings.Contains(output, "0000000000003") {
		t.Fatalf("did not expect retained generation ref in output, got: %s", output)
	}
}

func TestCleanCmd_All_ForceSkipsV2GenerationMissingMetadata(t *testing.T) {
	repo, _ := setupCleanTestRepo(t)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	repoRoot := wt.Filesystem().Root()

	writeCleanSettingsFile(t, repoRoot, `{"enabled": true, "strategy_options": {"checkpoints_v2": true, "full_transcript_generation_retention_days": 14}}`)
	createArchivedGenerationRefWithoutMetadata(t, repo, "0000000000001")

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--all", "--force"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("clean --all --force error = %v", err)
	}

	if _, err := repo.Reference(plumbing.ReferenceName(paths.V2FullRefPrefix+"0000000000001"), true); err != nil {
		t.Fatalf("archived generation ref with missing metadata should remain: %v", err)
	}
	if !strings.Contains(stderr.String(), "missing generation.json") {
		t.Fatalf("expected missing generation warning, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestCleanCmd_All_ForceSkipsV2GenerationWithInvalidTimestamps(t *testing.T) {
	repo, _ := setupCleanTestRepo(t)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	repoRoot := wt.Filesystem().Root()

	writeCleanSettingsFile(t, repoRoot, `{"enabled": true, "strategy_options": {"checkpoints_v2": true, "full_transcript_generation_retention_days": 14}}`)
	createArchivedGenerationRef(t, repo, "0000000000004", time.Now().AddDate(0, 0, -1), time.Now().AddDate(0, 0, -20))

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--all", "--force"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("clean --all --force error = %v", err)
	}

	if _, err := repo.Reference(plumbing.ReferenceName(paths.V2FullRefPrefix+"0000000000004"), true); err != nil {
		t.Fatalf("archived generation ref with invalid timestamps should remain: %v", err)
	}
	if !strings.Contains(stderr.String(), "invalid timestamps") {
		t.Fatalf("expected invalid timestamp warning, got stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestCleanCmd_All_ForceWarnsWithErrorDetailsForUnreadableV2Ref(t *testing.T) {
	repo, _ := setupCleanTestRepo(t)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	repoRoot := wt.Filesystem().Root()

	writeCleanSettingsFile(t, repoRoot, `{"enabled": true, "strategy_options": {"checkpoints_v2": true, "full_transcript_generation_retention_days": 14}}`)

	genName := "0000000000010"
	refName := plumbing.ReferenceName(paths.V2FullRefPrefix + genName)
	brokenHash := plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, brokenHash)); err != nil {
		t.Fatalf("failed to create broken archived generation ref: %v", err)
	}

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--all", "--force"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("clean --all --force error = %v", err)
	}

	warningText := stderr.String()
	if !strings.Contains(warningText, "generation "+genName+": cannot read ref:") {
		t.Fatalf("expected warning with ref error details, got stdout=%q stderr=%q", stdout.String(), warningText)
	}
}

// --- runCleanAllWithItems unit tests ---

func TestRunCleanAllWithItems_PartialFailure(t *testing.T) {
	repo, commitHash := setupCleanTestRepo(t)

	shadowRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName("trace/abc1234"), commitHash)
	if err := repo.Storer.SetReference(shadowRef); err != nil {
		t.Fatalf("failed to create shadow branch: %v", err)
	}

	items := []strategy.CleanupItem{
		{Type: strategy.CleanupTypeShadowBranch, ID: "trace/abc1234", Reason: "test"},
		{Type: strategy.CleanupTypeShadowBranch, ID: "trace/nonexistent1234567", Reason: "test"},
	}

	cmd, stdout, stderr := newTestCleanCmd(t)
	err := runCleanAllWithItems(cmd.Context(), cmd, true, false, items, nil)

	if err == nil {
		t.Fatal("runCleanAllWithItems() should return error when items fail to delete")
	}
	if !strings.Contains(err.Error(), "failed to delete 1 item") {
		t.Errorf("Error should mention 'failed to delete 1 item', got: %v", err)
	}
	// Verify singular (not "1 items")
	if strings.Contains(err.Error(), "1 items") {
		t.Errorf("Error should use singular 'item' for count 1, got: %v", err)
	}

	// Output should show the successful deletion with singular grammar
	output := stdout.String()
	if !strings.Contains(output, "✓ Deleted 1 item:") {
		t.Errorf("Output should show '✓ Deleted 1 item:', got: %s", output)
	}
	// Stderr should show the failure with singular grammar
	errOutput := stderr.String()
	if !strings.Contains(errOutput, "Failed to delete 1 item:") {
		t.Errorf("Stderr should show 'Failed to delete 1 item:', got: %s", errOutput)
	}
}

func TestRunCleanAllWithItems_AllFailures(t *testing.T) {
	setupCleanTestRepo(t)

	items := []strategy.CleanupItem{
		{Type: strategy.CleanupTypeShadowBranch, ID: "trace/nonexistent1234567", Reason: "test"},
		{Type: strategy.CleanupTypeShadowBranch, ID: "trace/alsononexistent", Reason: "test"},
	}

	cmd, stdout, stderr := newTestCleanCmd(t)
	err := runCleanAllWithItems(cmd.Context(), cmd, true, false, items, nil)

	if err == nil {
		t.Fatal("runCleanAllWithItems() should return error when items fail to delete")
	}
	if !strings.Contains(err.Error(), "failed to delete 2 items") {
		t.Errorf("Error should mention 'failed to delete 2 items', got: %v", err)
	}

	output := stdout.String()
	if strings.Contains(output, "✓ Deleted") {
		t.Errorf("Output should not show successful deletions, got: %s", output)
	}
	// Failures are written to stderr
	errOutput := stderr.String()
	if !strings.Contains(errOutput, "Failed to delete 2 items:") {
		t.Errorf("Stderr should show 'Failed to delete 2 items:', got: %s", errOutput)
	}
}

func TestRunCleanAllWithItems_NoItems(t *testing.T) {
	setupCleanTestRepo(t)

	cmd, stdout, _ := newTestCleanCmd(t)
	err := runCleanAllWithItems(cmd.Context(), cmd, false, false, []strategy.CleanupItem{}, nil)
	if err != nil {
		t.Fatalf("runCleanAllWithItems() error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "No items to clean up") {
		t.Errorf("Expected 'No items to clean up' message, got: %s", output)
	}
}

func TestRunCleanAllWithItems_MixedTypes_Preview(t *testing.T) {
	setupCleanTestRepo(t)

	items := []strategy.CleanupItem{
		{Type: strategy.CleanupTypeShadowBranch, ID: "trace/abc1234", Reason: "test"},
		{Type: strategy.CleanupTypeSessionState, ID: "session-123", Reason: "no checkpoints"},
		{Type: strategy.CleanupTypeCheckpoint, ID: "checkpoint-abc", Reason: "orphaned"},
	}

	cmd, stdout, _ := newTestCleanCmd(t)
	err := runCleanAllWithItems(cmd.Context(), cmd, false, true, items, nil)
	if err != nil {
		t.Fatalf("runCleanAllWithItems() error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Shadow branches") {
		t.Errorf("Expected 'Shadow branches' section, got: %s", output)
	}
	if !strings.Contains(output, "Session states") {
		t.Errorf("Expected 'Session states' section, got: %s", output)
	}
	if !strings.Contains(output, "Checkpoint metadata") {
		t.Errorf("Expected 'Checkpoint metadata' section, got: %s", output)
	}
	if !strings.Contains(output, "Found 3 items to clean") {
		t.Errorf("Expected 'Found 3 items to clean', got: %s", output)
	}
}

// --- Flag validation tests ---

func TestCleanCmd_MutuallyExclusiveFlags(t *testing.T) {
	setupCleanTestRepo(t)

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--all", "--session", "test-session"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("--all and --session should be mutually exclusive")
	}
	if !strings.Contains(err.Error(), "cannot be used together") {
		t.Errorf("Expected mutual exclusion error, got: %v", err)
	}
}
