//go:build integration

package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/testutil"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

// CheckpointValidation contains expected values for checkpoint validation.
type CheckpointValidation struct {
	// CheckpointID is the expected checkpoint ID
	CheckpointID string

	// SessionID is the expected session ID
	SessionID string

	// Strategy is the expected strategy name
	Strategy string

	// FilesTouched are the expected files in files_touched
	FilesTouched []string

	// ExpectedPrompts are strings that should appear in prompt.txt
	ExpectedPrompts []string

	// ExpectedTranscriptContent are strings that should appear in full.jsonl
	ExpectedTranscriptContent []string

	// CheckpointsCount is the expected checkpoint count (0 means don't validate)
	CheckpointsCount int
}

// ValidateCheckpoint performs comprehensive validation of a checkpoint on the metadata branch.
// It validates:
// - Root metadata.json (CheckpointSummary) structure and expected fields
// - Session metadata.json (CommittedMetadata) structure and expected fields
// - Transcript file (full.jsonl) is valid JSONL and contains expected content
// - Content hash file (content_hash.txt) matches SHA256 of transcript
// - Prompt file (prompt.txt) contains expected prompts
func (env *TestEnv) ValidateCheckpoint(v CheckpointValidation) {
	env.T.Helper()

	// Validate root metadata.json (CheckpointSummary)
	env.validateCheckpointSummary(v)

	// Validate session metadata.json (CommittedMetadata)
	env.validateSessionMetadata(v)

	// Validate transcript is valid JSONL
	env.validateTranscriptJSONL(v.CheckpointID, v.ExpectedTranscriptContent)

	// Validate content hash matches transcript
	env.validateContentHash(v.CheckpointID)

	// Validate prompt.txt contains expected prompts
	if len(v.ExpectedPrompts) > 0 {
		env.validatePromptContent(v.CheckpointID, v.ExpectedPrompts)
	}
}

// validateCheckpointSummary validates the root metadata.json (CheckpointSummary).
func (env *TestEnv) validateCheckpointSummary(v CheckpointValidation) {
	env.T.Helper()

	summaryPath := CheckpointSummaryPath(v.CheckpointID)
	content, found := env.ReadFileFromBranch(paths.MetadataBranchName, summaryPath)
	if !found {
		env.T.Fatalf("CheckpointSummary not found at %s", summaryPath)
	}

	var summary checkpoint.CheckpointSummary
	if err := json.Unmarshal([]byte(content), &summary); err != nil {
		env.T.Fatalf("Failed to parse CheckpointSummary: %v\nContent: %s", err, content)
	}

	// Validate checkpoint_id
	if summary.CheckpointID.String() != v.CheckpointID {
		env.T.Errorf("CheckpointSummary.CheckpointID = %q, want %q", summary.CheckpointID, v.CheckpointID)
	}

	// Validate strategy
	if v.Strategy != "" && summary.Strategy != v.Strategy {
		env.T.Errorf("CheckpointSummary.Strategy = %q, want %q", summary.Strategy, v.Strategy)
	}

	// Validate sessions array is populated
	if len(summary.Sessions) == 0 {
		env.T.Error("CheckpointSummary.Sessions should have at least one entry")
	}

	// Validate files_touched
	if len(v.FilesTouched) > 0 {
		touchedSet := make(map[string]bool)
		for _, f := range summary.FilesTouched {
			touchedSet[f] = true
		}
		for _, expected := range v.FilesTouched {
			if !touchedSet[expected] {
				env.T.Errorf("CheckpointSummary.FilesTouched missing %q, got %v", expected, summary.FilesTouched)
			}
		}
	}

	// Validate checkpoints_count
	if v.CheckpointsCount > 0 && summary.CheckpointsCount != v.CheckpointsCount {
		env.T.Errorf("CheckpointSummary.CheckpointsCount = %d, want %d", summary.CheckpointsCount, v.CheckpointsCount)
	}
}

// validateSessionMetadata validates the session-level metadata.json (CommittedMetadata).
func (env *TestEnv) validateSessionMetadata(v CheckpointValidation) {
	env.T.Helper()

	metadataPath := SessionMetadataPath(v.CheckpointID)
	content, found := env.ReadFileFromBranch(paths.MetadataBranchName, metadataPath)
	if !found {
		env.T.Fatalf("Session metadata not found at %s", metadataPath)
	}

	var metadata checkpoint.CommittedMetadata
	if err := json.Unmarshal([]byte(content), &metadata); err != nil {
		env.T.Fatalf("Failed to parse CommittedMetadata: %v\nContent: %s", err, content)
	}

	// Validate checkpoint_id
	if metadata.CheckpointID.String() != v.CheckpointID {
		env.T.Errorf("CommittedMetadata.CheckpointID = %q, want %q", metadata.CheckpointID, v.CheckpointID)
	}

	// Validate session_id
	if v.SessionID != "" && metadata.SessionID != v.SessionID {
		env.T.Errorf("CommittedMetadata.SessionID = %q, want %q", metadata.SessionID, v.SessionID)
	}

	// Validate strategy
	if v.Strategy != "" && metadata.Strategy != v.Strategy {
		env.T.Errorf("CommittedMetadata.Strategy = %q, want %q", metadata.Strategy, v.Strategy)
	}

	// Validate created_at is not zero
	if metadata.CreatedAt.IsZero() {
		env.T.Error("CommittedMetadata.CreatedAt should not be zero")
	}

	// Validate files_touched
	if len(v.FilesTouched) > 0 {
		touchedSet := make(map[string]bool)
		for _, f := range metadata.FilesTouched {
			touchedSet[f] = true
		}
		for _, expected := range v.FilesTouched {
			if !touchedSet[expected] {
				env.T.Errorf("CommittedMetadata.FilesTouched missing %q, got %v", expected, metadata.FilesTouched)
			}
		}
	}

	// Validate checkpoints_count
	if v.CheckpointsCount > 0 && metadata.CheckpointsCount != v.CheckpointsCount {
		env.T.Errorf("CommittedMetadata.CheckpointsCount = %d, want %d", metadata.CheckpointsCount, v.CheckpointsCount)
	}
}

// validateTranscriptJSONL validates that full.jsonl exists and is valid JSON or JSONL.
// It supports both:
// - JSON format (single document, used by OpenCode and Gemini CLI)
// - JSONL format (one JSON object per line, used by Claude Code)
func (env *TestEnv) validateTranscriptJSONL(checkpointID string, expectedContent []string) {
	env.T.Helper()

	transcriptPath := SessionFilePath(checkpointID, paths.TranscriptFileName)
	content, found := env.ReadFileFromBranch(paths.MetadataBranchName, transcriptPath)
	if !found {
		env.T.Fatalf("Transcript not found at %s", transcriptPath)
	}

	// First try to parse as a single JSON document (OpenCode/Gemini format)
	var jsonDoc any
	if err := json.Unmarshal([]byte(content), &jsonDoc); err == nil {
		// Valid JSON document - validation passed
	} else {
		// Fall back to JSONL validation (Claude Code format)
		lines := strings.Split(content, "\n")
		validLines := 0
		for i, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			validLines++
			var obj map[string]any
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				env.T.Errorf("Transcript line %d is not valid JSON: %v\nLine: %s", i+1, err, line)
			}
		}

		if validLines == 0 {
			env.T.Error("Transcript is empty (no valid JSON content)")
		}
	}

	// Validate expected content appears in transcript
	for _, expected := range expectedContent {
		if !strings.Contains(content, expected) {
			env.T.Errorf("Transcript should contain %q", expected)
		}
	}
}

// validateContentHash validates that content_hash.txt matches the SHA256 of the transcript.
func (env *TestEnv) validateContentHash(checkpointID string) {
	env.T.Helper()

	// Read transcript
	transcriptPath := SessionFilePath(checkpointID, paths.TranscriptFileName)
	transcript, found := env.ReadFileFromBranch(paths.MetadataBranchName, transcriptPath)
	if !found {
		env.T.Fatalf("Transcript not found at %s", transcriptPath)
	}

	// Read content hash
	hashPath := SessionFilePath(checkpointID, "content_hash.txt")
	storedHash, found := env.ReadFileFromBranch(paths.MetadataBranchName, hashPath)
	if !found {
		env.T.Fatalf("Content hash not found at %s", hashPath)
	}
	storedHash = strings.TrimSpace(storedHash)

	// Calculate expected hash with sha256: prefix (matches format in committed.go)
	hash := sha256.Sum256([]byte(transcript))
	expectedHash := "sha256:" + hex.EncodeToString(hash[:])

	if storedHash != expectedHash {
		env.T.Errorf("Content hash mismatch:\n  stored:   %s\n  expected: %s", storedHash, expectedHash)
	}
}

// validatePromptContent validates that prompt.txt contains the expected prompts.
func (env *TestEnv) validatePromptContent(checkpointID string, expectedPrompts []string) {
	env.T.Helper()

	promptPath := SessionFilePath(checkpointID, paths.PromptFileName)
	content, found := env.ReadFileFromBranch(paths.MetadataBranchName, promptPath)
	if !found {
		env.T.Fatalf("Prompt file not found at %s", promptPath)
	}

	for _, expected := range expectedPrompts {
		if !strings.Contains(content, expected) {
			env.T.Errorf("Prompt file should contain %q\nContent: %s", expected, content)
		}
	}
}

// SetupBareRemote creates a bare git repository, adds it as "origin" remote to the
// test repo, and pushes the current HEAD. Returns the bare repo path.
// This mirrors the E2E helper in e2e/testutil/repo.go but adapted for TestEnv.
func (env *TestEnv) SetupBareRemote() string {
	env.T.Helper()
	return env.SetupNamedBareRemote("origin")
}

// SetupNamedBareRemote creates a bare git repository with a custom remote name.
// Returns the bare repo path. Use this for checkpoint_remote scenarios that need
// multiple remotes.
func (env *TestEnv) SetupNamedBareRemote(remoteName string) string {
	env.T.Helper()

	ctx := env.T.Context()

	bareDir := env.T.TempDir()
	if resolved, err := filepath.EvalSymlinks(bareDir); err == nil {
		bareDir = resolved
	}

	// Initialize bare repo
	cmd := exec.CommandContext(ctx, "git", "init", "--bare")
	cmd.Dir = bareDir
	cmd.Env = testutil.GitIsolatedEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		env.T.Fatalf("failed to init bare repo: %v\n%s", err, output)
	}

	// Add as remote
	cmd = exec.CommandContext(ctx, "git", "remote", "add", remoteName, bareDir)
	cmd.Dir = env.RepoDir
	cmd.Env = testutil.GitIsolatedEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		env.T.Fatalf("failed to add remote %s: %v\n%s", remoteName, err, output)
	}

	// Push HEAD to the remote
	cmd = exec.CommandContext(ctx, "git", "push", "--no-verify", "-u", remoteName, "HEAD")
	cmd.Dir = env.RepoDir
	cmd.Env = testutil.GitIsolatedEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		env.T.Fatalf("failed to push to %s: %v\n%s", remoteName, err, output)
	}

	env.setGitConfigBaseline()

	return bareDir
}

// CloneFrom clones from a bare repo into a new temp directory and returns a new TestEnv
// pointing at the clone. The clone has its own .trace directory initialized.
// The clone checks out the same branch as the current env's HEAD.
func (env *TestEnv) CloneFrom(bareDir string) *TestEnv {
	env.T.Helper()

	ctx := env.T.Context()

	cloneDir := env.T.TempDir()
	if resolved, err := filepath.EvalSymlinks(cloneDir); err == nil {
		cloneDir = resolved
	}

	// Get the current branch name to clone the right branch
	currentBranch := env.GetCurrentBranch()

	// Clone the bare repo, explicitly checking out the right branch.
	// Bare repos may have HEAD pointing to a non-existent default branch
	// when the original was on a feature branch.
	cloneArgs := []string{"clone"}
	if currentBranch != "" {
		cloneArgs = append(cloneArgs, "--branch", currentBranch)
	}
	cloneArgs = append(cloneArgs, bareDir, cloneDir)
	cmd := exec.CommandContext(ctx, "git", cloneArgs...)
	cmd.Env = testutil.GitIsolatedEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		env.T.Fatalf("failed to clone from %s: %v\n%s", bareDir, err, output)
	}

	// Configure git user (clone doesn't inherit local config from the bare repo)
	for _, kv := range [][2]string{
		{"user.name", "Test User"},
		{"user.email", "test@example.com"},
		{"commit.gpgsign", "false"},
	} {
		cmd = exec.CommandContext(ctx, "git", "config", kv[0], kv[1])
		cmd.Dir = cloneDir
		cmd.Env = testutil.GitIsolatedEnv()
		if output, err := cmd.CombinedOutput(); err != nil {
			env.T.Fatalf("failed to set git config %s: %v\n%s", kv[0], err, output)
		}
	}

	claudeProjectDir := env.T.TempDir()
	if resolved, err := filepath.EvalSymlinks(claudeProjectDir); err == nil {
		claudeProjectDir = resolved
	}
	geminiProjectDir := env.T.TempDir()
	if resolved, err := filepath.EvalSymlinks(geminiProjectDir); err == nil {
		geminiProjectDir = resolved
	}
	openCodeProjectDir := env.T.TempDir()
	if resolved, err := filepath.EvalSymlinks(openCodeProjectDir); err == nil {
		openCodeProjectDir = resolved
	}

	cloneEnv := &TestEnv{
		T:                  env.T,
		RepoDir:            cloneDir,
		ClaudeProjectDir:   claudeProjectDir,
		GeminiProjectDir:   geminiProjectDir,
		OpenCodeProjectDir: openCodeProjectDir,
	}

	// Initialize Trace in the clone
	cloneEnv.InitTrace()
	cloneEnv.setGitConfigBaseline()

	return cloneEnv
}

// BranchExistsOnRemote checks if a branch exists on a bare remote by inspecting its refs.
func (env *TestEnv) BranchExistsOnRemote(bareDir, branchName string) bool {
	env.T.Helper()

	cmd := exec.CommandContext(env.T.Context(), "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
	cmd.Dir = bareDir
	cmd.Env = testutil.GitIsolatedEnv()
	return cmd.Run() == nil
}

// PatchSettings merges extra keys into .trace/settings.json.
func (env *TestEnv) PatchSettings(extra map[string]any) {
	env.T.Helper()

	settingsPath := filepath.Join(env.RepoDir, ".trace", paths.SettingsFileName)
	data, err := os.ReadFile(settingsPath) //nolint:gosec // G304: path is constructed from test env, not user input
	if err != nil {
		env.T.Fatalf("failed to read settings: %v", err)
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		env.T.Fatalf("failed to parse settings: %v", err)
	}

	for k, v := range extra {
		settings[k] = v
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		env.T.Fatalf("failed to marshal settings: %v", err)
	}
	out = append(out, '\n')

	if err := os.WriteFile(settingsPath, out, 0o644); err != nil { //nolint:gosec // G306: consistent with other settings writes in testenv.go
		env.T.Fatalf("failed to write settings: %v", err)
	}
}

// GitPush pushes a branch to a remote. Fails the test on error.
func (env *TestEnv) GitPush(remote, refSpec string) {
	env.T.Helper()

	cmd := exec.CommandContext(env.T.Context(), "git", "push", "--no-verify", remote, refSpec)
	cmd.Dir = env.RepoDir
	cmd.Env = testutil.GitIsolatedEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		env.T.Fatalf("git push %s %s failed: %v\n%s", remote, refSpec, err, output)
	}
}

// RunPrePush runs the pre-push hook via the CLI binary, consistent with how
// other CLI invocations (GitCommitWithShadowHooks, RunCLI) use env.cliEnv().
func (env *TestEnv) RunPrePush(remote string) {
	env.T.Helper()
	if err := env.RunPrePushWithError(remote); err != nil {
		env.T.Fatalf("PrePush failed: %v", err)
	}
}

// RunPrePushWithError runs the pre-push hook and returns any error instead of failing.
func (env *TestEnv) RunPrePushWithError(remote string) error {
	env.T.Helper()

	cmd := exec.CommandContext(env.T.Context(), getTestBinary(), "hooks", "git", "pre-push", remote)
	cmd.Dir = env.RepoDir
	cmd.Env = env.cliEnv()
	cmd.Stdin = nil

	output, err := cmd.CombinedOutput()
	env.T.Logf("pre-push output: %s", output)
	if err != nil {
		return fmt.Errorf("pre-push hook failed: %w", err)
	}
	return nil
}

// FetchMetadataBranch fetches the trace/checkpoints/v1 branch from a remote URL.
// Fails the test on error. Use this for clone-and-resume tests that need metadata.
func (env *TestEnv) FetchMetadataBranch(remoteURL string) {
	env.T.Helper()

	branchName := paths.MetadataBranchName
	refSpec := "+refs/heads/" + branchName + ":refs/heads/" + branchName
	cmd := exec.CommandContext(env.T.Context(), "git", "fetch", "--no-tags", remoteURL, refSpec)
	cmd.Dir = env.RepoDir
	cmd.Env = testutil.GitIsolatedEnv()

	output, err := cmd.CombinedOutput()
	if err != nil {
		env.T.Fatalf("fetch metadata branch failed: %v\n%s", err, output)
	}
}

// GetBranchTipParentCount returns the number of parents for the tip commit of a branch.
func (env *TestEnv) GetBranchTipParentCount(branchName string) int {
	env.T.Helper()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	ref, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	if err != nil {
		env.T.Fatalf("failed to get branch %s: %v", branchName, err)
	}

	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		env.T.Fatalf("failed to get commit for branch %s: %v", branchName, err)
	}

	return len(commit.ParentHashes)
}

func findModuleRoot() string {
	// Start from this source file's location and walk up to find go.mod
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("failed to get current file path via runtime.Caller")
	}
	dir := filepath.Dir(thisFile)

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("could not find go.mod starting from " + thisFile)
		}
		dir = parent
	}
}
