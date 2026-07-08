package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/spf13/cobra"
)

// newTestCleanCmd creates a cobra.Command with captured stdout/stderr for testing.
func newTestCleanCmd(t *testing.T) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	return cmd, &stdout, &stderr
}

func setupCleanTestRepo(t *testing.T) (*git.Repository, plumbing.Hash) {
	t.Helper()

	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	t.Chdir(dir)
	paths.ClearWorktreeRootCache()

	// Create initial commit
	emptyTree := &object.Tree{Entries: []object.TreeEntry{}}
	obj := repo.Storer.NewEncodedObject()
	if err := emptyTree.Encode(obj); err != nil {
		t.Fatalf("failed to encode empty tree: %v", err)
	}
	emptyTreeHash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("failed to store empty tree: %v", err)
	}

	sig := object.Signature{Name: "test", Email: "test@test.com"}
	commit := &object.Commit{
		TreeHash:  emptyTreeHash,
		Author:    sig,
		Committer: sig,
		Message:   "initial commit",
	}
	commitObj := repo.Storer.NewEncodedObject()
	if err := commit.Encode(commitObj); err != nil {
		t.Fatalf("failed to encode commit: %v", err)
	}
	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	if err != nil {
		t.Fatalf("failed to store commit: %v", err)
	}

	// Create HEAD and master references
	headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("master"))
	if err := repo.Storer.SetReference(headRef); err != nil {
		t.Fatalf("failed to set HEAD: %v", err)
	}
	masterRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName("master"), commitHash)
	if err := repo.Storer.SetReference(masterRef); err != nil {
		t.Fatalf("failed to set master: %v", err)
	}

	return repo, commitHash
}

// createSessionStateFile creates a session state JSON file in .git/trace-sessions/.
func createSessionStateFile(t *testing.T, repoRoot string, sessionID string, commitHash plumbing.Hash) string {
	t.Helper()

	sessionStateDir := filepath.Join(repoRoot, ".git", "trace-sessions")
	if err := os.MkdirAll(sessionStateDir, 0o755); err != nil {
		t.Fatalf("failed to create session state dir: %v", err)
	}

	sessionFile := filepath.Join(sessionStateDir, sessionID+".json")
	sessionState := map[string]any{
		"session_id":       sessionID,
		"base_commit":      commitHash.String(),
		"checkpoint_count": 1,
		"started_at":       time.Now().Format(time.RFC3339),
	}
	sessionData, err := json.Marshal(sessionState)
	if err != nil {
		t.Fatalf("failed to marshal session state: %v", err)
	}
	if err := os.WriteFile(sessionFile, sessionData, 0o600); err != nil {
		t.Fatalf("failed to write session state file: %v", err)
	}
	return sessionFile
}

func writeCleanSettingsFile(t *testing.T, repoRoot, content string) {
	t.Helper()

	traceDir := filepath.Join(repoRoot, ".trace")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatalf("failed to create .trace directory: %v", err)
	}

	settingsFile := filepath.Join(traceDir, "settings.json")
	if err := os.WriteFile(settingsFile, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write settings file: %v", err)
	}
}

func runCleanGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %s: %v", strings.Join(args, " "), strings.TrimSpace(string(output)), err)
	}
	return string(output)
}

func addCleanBareOrigin(t *testing.T, repoRoot string) {
	t.Helper()

	remoteDir := filepath.Join(t.TempDir(), "origin.git")
	runCleanGit(t, "", "init", "--bare", remoteDir)
	runCleanGit(t, repoRoot, "remote", "add", "origin", remoteDir)
}

func TestCleanLongDescription_DefaultIsGeneric(t *testing.T) {
	repo, _ := setupCleanTestRepo(t)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	repoRoot := wt.Filesystem().Root()

	writeCleanSettingsFile(t, repoRoot, `{"enabled": true, "strategy_options": {}}`)

	description := cleanLongDescription(context.Background())
	if strings.Contains(description, "checkpoints v2") {
		t.Fatalf("did not expect v2-specific help text by default, got: %s", description)
	}
	if strings.Contains(description, "trace/checkpoints/v1") {
		t.Fatalf("did not expect stale v1 preservation text, got: %s", description)
	}
}

func TestCleanLongDescription_IncludesV2CleanupWhenEnabled(t *testing.T) {
	repo, _ := setupCleanTestRepo(t)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	repoRoot := wt.Filesystem().Root()

	writeCleanSettingsFile(t, repoRoot, `{"enabled": true, "strategy_options": {"checkpoints_v2": true, "full_transcript_generation_retention_days": 14}}`)

	description := cleanLongDescription(context.Background())
	if !strings.Contains(description, "Archived v2 full transcripts older than the configured 14-day retention window") {
		t.Fatalf("expected v2 cleanup help text when enabled, got: %s", description)
	}
}

func createCleanV2Ref(t *testing.T, repo *git.Repository, refName plumbing.ReferenceName) {
	t.Helper()

	treeHash, err := checkpoint.BuildTreeFromEntries(context.Background(), repo, map[string]object.TreeEntry{})
	if err != nil {
		t.Fatalf("failed to build empty tree for %s: %v", refName, err)
	}

	commitHash, err := checkpoint.CreateCommit(context.Background(), repo, treeHash, plumbing.ZeroHash, "init v2 ref", "test", "test@test.com")
	if err != nil {
		t.Fatalf("failed to create commit for %s: %v", refName, err)
	}

	ref := plumbing.NewHashReference(refName, commitHash)
	if err := repo.Storer.SetReference(ref); err != nil {
		t.Fatalf("failed to create %s: %v", refName, err)
	}
}

func createArchivedGenerationRef(t *testing.T, repo *git.Repository, generation string, oldest, newest time.Time) {
	t.Helper()

	gen := checkpoint.GenerationMetadata{
		OldestCheckpointAt: oldest.UTC(),
		NewestCheckpointAt: newest.UTC(),
	}

	genJSON, err := json.Marshal(gen)
	if err != nil {
		t.Fatalf("failed to marshal generation metadata: %v", err)
	}

	genBlobHash, err := checkpoint.CreateBlobFromContent(repo, genJSON)
	if err != nil {
		t.Fatalf("failed to create generation blob: %v", err)
	}

	transcriptBlobHash, err := checkpoint.CreateBlobFromContent(repo, []byte(`{"transcript":"data"}`))
	if err != nil {
		t.Fatalf("failed to create transcript blob: %v", err)
	}

	entries := map[string]object.TreeEntry{
		paths.GenerationFileName: {
			Name: paths.GenerationFileName,
			Mode: filemode.Regular,
			Hash: genBlobHash,
		},
		"aa/bbccddeeff/0/" + paths.TranscriptFileName: {
			Name: paths.TranscriptFileName,
			Mode: filemode.Regular,
			Hash: transcriptBlobHash,
		},
	}

	treeHash, err := checkpoint.BuildTreeFromEntries(context.Background(), repo, entries)
	if err != nil {
		t.Fatalf("failed to build archived generation tree: %v", err)
	}

	commitHash, err := checkpoint.CreateCommit(context.Background(), repo, treeHash, plumbing.ZeroHash, "archived generation", "test", "test@test.com")
	if err != nil {
		t.Fatalf("failed to create archived generation commit: %v", err)
	}

	refName := plumbing.ReferenceName(paths.V2FullRefPrefix + generation)
	ref := plumbing.NewHashReference(refName, commitHash)
	if err := repo.Storer.SetReference(ref); err != nil {
		t.Fatalf("failed to create archived generation ref %s: %v", refName, err)
	}
}

func createArchivedGenerationRefWithRawTranscript(
	t *testing.T,
	repo *git.Repository,
	generation string,
	cpID id.CheckpointID,
	generationOldest time.Time,
	generationNewest time.Time,
	rawOldest time.Time,
	rawNewest time.Time,
) {
	t.Helper()

	gen := checkpoint.GenerationMetadata{
		OldestCheckpointAt: generationOldest.UTC(),
		NewestCheckpointAt: generationNewest.UTC(),
	}
	genJSON, err := json.Marshal(gen)
	if err != nil {
		t.Fatalf("failed to marshal generation metadata: %v", err)
	}
	genBlobHash, err := checkpoint.CreateBlobFromContent(repo, genJSON)
	if err != nil {
		t.Fatalf("failed to create generation blob: %v", err)
	}

	transcript := `{"type":"user","timestamp":` + strconv.Quote(rawOldest.UTC().Format(time.RFC3339Nano)) + "}\n" +
		`{"type":"assistant","timestamp":` + strconv.Quote(rawNewest.UTC().Format(time.RFC3339Nano)) + "}\n"
	transcriptBlobHash, err := checkpoint.CreateBlobFromContent(repo, []byte(transcript))
	if err != nil {
		t.Fatalf("failed to create transcript blob: %v", err)
	}

	entries := map[string]object.TreeEntry{
		paths.GenerationFileName: {
			Name: paths.GenerationFileName,
			Mode: filemode.Regular,
			Hash: genBlobHash,
		},
		cpID.Path() + "/0/" + paths.V2RawTranscriptFileName: {
			Name: paths.V2RawTranscriptFileName,
			Mode: filemode.Regular,
			Hash: transcriptBlobHash,
		},
	}

	treeHash, err := checkpoint.BuildTreeFromEntries(context.Background(), repo, entries)
	if err != nil {
		t.Fatalf("failed to build archived generation tree: %v", err)
	}

	commitHash, err := checkpoint.CreateCommit(context.Background(), repo, treeHash, plumbing.ZeroHash, "archived generation", "test", "test@test.com")
	if err != nil {
		t.Fatalf("failed to create archived generation commit: %v", err)
	}

	refName := plumbing.ReferenceName(paths.V2FullRefPrefix + generation)
	ref := plumbing.NewHashReference(refName, commitHash)
	if err := repo.Storer.SetReference(ref); err != nil {
		t.Fatalf("failed to create archived generation ref %s: %v", refName, err)
	}
}

func createRemoteOnlyArchivedGenerationRef(
	t *testing.T,
	repo *git.Repository,
	repoRoot string,
	generation string,
	oldest time.Time,
	newest time.Time,
) string {
	t.Helper()

	createArchivedGenerationRef(t, repo, generation, oldest, newest)
	refName := paths.V2FullRefPrefix + generation
	ref, err := repo.Reference(plumbing.ReferenceName(refName), true)
	if err != nil {
		t.Fatalf("failed to read archived generation ref %s: %v", refName, err)
	}
	runCleanGit(t, repoRoot, "push", "origin", refName+":"+refName)
	if err := strategy.DeleteRefCLI(context.Background(), refName, ref.Hash().String()); err != nil {
		t.Fatalf("failed to remove local archived generation ref %s: %v", refName, err)
	}
	return ref.Hash().String()
}

func createV2MainMetadataRef(t *testing.T, repo *git.Repository, cpID id.CheckpointID, createdAt time.Time) {
	t.Helper()

	sessionMetadata := checkpoint.CommittedMetadata{
		CheckpointID: cpID,
		SessionID:    "session-" + cpID.String(),
		Strategy:     "manual-commit",
		CreatedAt:    createdAt.UTC(),
	}
	sessionMetadataJSON, err := json.Marshal(sessionMetadata)
	if err != nil {
		t.Fatalf("failed to marshal session metadata: %v", err)
	}
	sessionMetadataHash, err := checkpoint.CreateBlobFromContent(repo, sessionMetadataJSON)
	if err != nil {
		t.Fatalf("failed to create session metadata blob: %v", err)
	}

	summary := checkpoint.CheckpointSummary{
		CheckpointID: cpID,
		Strategy:     "manual-commit",
		Sessions: []checkpoint.SessionFilePaths{
			{Metadata: "/" + cpID.Path() + "/0/" + paths.MetadataFileName},
		},
	}
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("failed to marshal checkpoint summary: %v", err)
	}
	summaryHash, err := checkpoint.CreateBlobFromContent(repo, summaryJSON)
	if err != nil {
		t.Fatalf("failed to create checkpoint summary blob: %v", err)
	}

	entries := map[string]object.TreeEntry{
		cpID.Path() + "/" + paths.MetadataFileName: {
			Name: paths.MetadataFileName,
			Mode: filemode.Regular,
			Hash: summaryHash,
		},
		cpID.Path() + "/0/" + paths.MetadataFileName: {
			Name: paths.MetadataFileName,
			Mode: filemode.Regular,
			Hash: sessionMetadataHash,
		},
	}

	treeHash, err := checkpoint.BuildTreeFromEntries(context.Background(), repo, entries)
	if err != nil {
		t.Fatalf("failed to build v2 main tree: %v", err)
	}
	commitHash, err := checkpoint.CreateCommit(context.Background(), repo, treeHash, plumbing.ZeroHash, "v2 main", "test", "test@test.com")
	if err != nil {
		t.Fatalf("failed to create v2 main commit: %v", err)
	}
	ref := plumbing.NewHashReference(plumbing.ReferenceName(paths.V2MainRefName), commitHash)
	if err := repo.Storer.SetReference(ref); err != nil {
		t.Fatalf("failed to create v2 main ref: %v", err)
	}
}

func createArchivedGenerationRefWithoutMetadata(t *testing.T, repo *git.Repository, generation string) {
	t.Helper()

	transcriptBlobHash, err := checkpoint.CreateBlobFromContent(repo, []byte(`{"transcript":"data"}`))
	if err != nil {
		t.Fatalf("failed to create transcript blob: %v", err)
	}

	entries := map[string]object.TreeEntry{
		"aa/bbccddeeff/0/" + paths.TranscriptFileName: {
			Name: paths.TranscriptFileName,
			Mode: filemode.Regular,
			Hash: transcriptBlobHash,
		},
	}

	treeHash, err := checkpoint.BuildTreeFromEntries(context.Background(), repo, entries)
	if err != nil {
		t.Fatalf("failed to build archived generation tree: %v", err)
	}

	commitHash, err := checkpoint.CreateCommit(context.Background(), repo, treeHash, plumbing.ZeroHash, "archived generation without metadata", "test", "test@test.com")
	if err != nil {
		t.Fatalf("failed to create archived generation commit: %v", err)
	}

	refName := plumbing.ReferenceName(paths.V2FullRefPrefix + generation)
	ref := plumbing.NewHashReference(refName, commitHash)
	if err := repo.Storer.SetReference(ref); err != nil {
		t.Fatalf("failed to create archived generation ref %s: %v", refName, err)
	}
}

// --- Default mode tests (current HEAD cleanup) ---

func TestCleanCmd_DefaultMode_NothingToClean(t *testing.T) {
	setupCleanTestRepo(t)

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--force"})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("clean command error = %v", err)
	}
}

func TestCleanCmd_DefaultMode_WithForce(t *testing.T) {
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

	// Create shadow branch
	shadowBranch := checkpoint.ShadowBranchNameForCommit(commitHash.String(), worktreeID)
	shadowRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(shadowBranch), commitHash)
	if err := repo.Storer.SetReference(shadowRef); err != nil {
		t.Fatalf("failed to create shadow branch: %v", err)
	}

	// Create session state file
	sessionFile := createSessionStateFile(t, worktreePath, "2026-02-02-test123", commitHash)

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--force"})

	err = cmd.Execute()
	if err != nil {
		t.Fatalf("clean command error = %v", err)
	}

	// Verify shadow branch deleted
	refName := plumbing.NewBranchReferenceName(shadowBranch)
	if _, err := repo.Reference(refName, true); err == nil {
		t.Error("shadow branch should be deleted")
	}

	// Verify session state file deleted
	if _, err := os.Stat(sessionFile); !os.IsNotExist(err) {
		t.Error("session state file should be deleted")
	}
}

func TestCleanCmd_DefaultMode_DryRun(t *testing.T) {
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

	// Create shadow branch
	shadowBranch := checkpoint.ShadowBranchNameForCommit(commitHash.String(), worktreeID)
	shadowRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(shadowBranch), commitHash)
	if err := repo.Storer.SetReference(shadowRef); err != nil {
		t.Fatalf("failed to create shadow branch: %v", err)
	}

	// Create session state file
	sessionFile := createSessionStateFile(t, worktreePath, "2026-02-02-test123", commitHash)

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--dry-run"})

	err = cmd.Execute()
	if err != nil {
		t.Fatalf("clean command error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Would clean") {
		t.Errorf("Expected 'Would clean' in output, got: %s", output)
	}
	if !strings.Contains(output, shadowBranch) {
		t.Errorf("Expected shadow branch name in output, got: %s", output)
	}
	if !strings.Contains(output, "2026-02-02-test123") {
		t.Errorf("Expected session ID in output, got: %s", output)
	}

	// Verify nothing was deleted
	refName := plumbing.NewBranchReferenceName(shadowBranch)
	if _, err := repo.Reference(refName, true); err != nil {
		t.Error("shadow branch should still exist after dry-run")
	}
	if _, err := os.Stat(sessionFile); os.IsNotExist(err) {
		t.Error("session state file should still exist after dry-run")
	}
}

func TestCleanCmd_DefaultMode_DryRun_NothingToClean(t *testing.T) {
	setupCleanTestRepo(t)

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--dry-run"})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("clean command error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Nothing to clean") {
		t.Errorf("Expected 'Nothing to clean' message, got: %s", output)
	}
}

func TestCleanCmd_DefaultMode_SessionsWithoutShadowBranch(t *testing.T) {
	repo, commitHash := setupCleanTestRepo(t)

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("failed to get worktree: %v", err)
	}
	worktreePath := wt.Filesystem().Root()

	// Create session state files WITHOUT a shadow branch
	sessionFile := createSessionStateFile(t, worktreePath, "2026-02-02-orphaned", commitHash)

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--force"})

	err = cmd.Execute()
	if err != nil {
		t.Fatalf("clean command error = %v", err)
	}

	// Verify session state file deleted
	if _, err := os.Stat(sessionFile); !os.IsNotExist(err) {
		t.Error("session state file should be deleted even without shadow branch")
	}
}

func TestCleanCmd_DefaultMode_MultipleSessions(t *testing.T) {
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

	// Create shadow branch
	shadowBranch := checkpoint.ShadowBranchNameForCommit(commitHash.String(), worktreeID)
	shadowRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(shadowBranch), commitHash)
	if err := repo.Storer.SetReference(shadowRef); err != nil {
		t.Fatalf("failed to create shadow branch: %v", err)
	}

	// Create multiple session state files
	session1File := createSessionStateFile(t, worktreePath, "2026-02-02-session1", commitHash)
	session2File := createSessionStateFile(t, worktreePath, "2026-02-02-session2", commitHash)

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--force"})

	err = cmd.Execute()
	if err != nil {
		t.Fatalf("clean command error = %v", err)
	}

	// Verify both session files deleted
	if _, err := os.Stat(session1File); !os.IsNotExist(err) {
		t.Error("session1 file should be deleted")
	}
	if _, err := os.Stat(session2File); !os.IsNotExist(err) {
		t.Error("session2 file should be deleted")
	}

	// Verify shadow branch deleted
	refName := plumbing.NewBranchReferenceName(shadowBranch)
	if _, err := repo.Reference(refName, true); err == nil {
		t.Error("shadow branch should be deleted")
	}
}

func TestCleanCmd_DefaultMode_NotGitRepo(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	paths.ClearWorktreeRootCache()

	cmd := newCleanCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("clean command should return error for non-git directory")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("Expected 'not a git repository' error, got: %v", err)
	}
}

// --- --all mode tests (repo-wide orphan cleanup) ---

func TestCleanCmd_All_NoOrphanedItems(t *testing.T) {
	setupCleanTestRepo(t)

	cmd := newCleanCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--all"})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("clean --all error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "No items to clean up") {
		t.Errorf("Expected 'No items to clean up' message, got: %s", output)
	}
}

func TestCleanCmd_All_PreviewMode(t *testing.T) {
	repo, commitHash := setupCleanTestRepo(t)

	// Create shadow branches
	shadowBranches := []string{"trace/abc1234", "trace/def5678"}
	for _, b := range shadowBranches {
		ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(b), commitHash)
		if err := repo.Storer.SetReference(ref); err != nil {
			t.Fatalf("failed to create branch %s: %v", b, err)
		}
	}

	// Also create trace/checkpoints/v1 (should NOT be listed)
	sessionsRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName(paths.MetadataBranchName), commitHash)
	if err := repo.Storer.SetReference(sessionsRef); err != nil {
		t.Fatalf("failed to create %s: %v", paths.MetadataBranchName, err)
	}

	cmd := newCleanCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--all", "--dry-run"})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("clean --all --dry-run error = %v", err)
	}

	output := stdout.String()

	if !strings.Contains(output, "to clean") {
		t.Errorf("Expected 'to clean' in output, got: %s", output)
	}
	if !strings.Contains(output, "trace/abc1234") {
		t.Errorf("Expected 'trace/abc1234' in output, got: %s", output)
	}
	if !strings.Contains(output, "trace/def5678") {
		t.Errorf("Expected 'trace/def5678' in output, got: %s", output)
	}
	if strings.Contains(output, paths.MetadataBranchName) {
		t.Errorf("Should not list '%s', got: %s", paths.MetadataBranchName, output)
	}
	if !strings.Contains(output, "without --dry-run") {
		t.Errorf("Expected '--dry-run' hint in output, got: %s", output)
	}

	// Branches should still exist (dry-run doesn't delete)
	for _, b := range shadowBranches {
		refName := plumbing.NewBranchReferenceName(b)
		if _, err := repo.Reference(refName, true); err != nil {
			t.Errorf("Branch %s should still exist after dry-run", b)
		}
	}
}

func TestCleanCmd_All_DryRun(t *testing.T) {
	repo, commitHash := setupCleanTestRepo(t)

	shadowBranches := []string{"trace/abc1234"}
	for _, b := range shadowBranches {
		ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(b), commitHash)
		if err := repo.Storer.SetReference(ref); err != nil {
			t.Fatalf("failed to create branch %s: %v", b, err)
		}
	}

	cmd := newCleanCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--all", "--dry-run"})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("clean --all --dry-run error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "to clean") {
		t.Errorf("Expected 'to clean' in output, got: %s", output)
	}
	if !strings.Contains(output, "without --dry-run") {
		t.Errorf("Expected '--dry-run' hint in output, got: %s", output)
	}

	// Branches should still exist
	for _, b := range shadowBranches {
		refName := plumbing.NewBranchReferenceName(b)
		if _, err := repo.Reference(refName, true); err != nil {
			t.Errorf("Branch %s should still exist after dry-run", b)
		}
	}
}

func TestCleanCmd_All_ForceMode(t *testing.T) {
	repo, commitHash := setupCleanTestRepo(t)

	shadowBranches := []string{"trace/abc1234", "trace/def5678"}
	for _, b := range shadowBranches {
		ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(b), commitHash)
		if err := repo.Storer.SetReference(ref); err != nil {
			t.Fatalf("failed to create branch %s: %v", b, err)
		}
	}

	cmd := newCleanCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--all", "--force"})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("clean --all --force error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Deleted") {
		t.Errorf("Expected 'Deleted' in output, got: %s", output)
	}

	// Branches should be deleted
	for _, b := range shadowBranches {
		refName := plumbing.NewBranchReferenceName(b)
		if _, err := repo.Reference(refName, true); err == nil {
			t.Errorf("Branch %s should be deleted but still exists", b)
		}
	}
}
