//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/execx"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/GrayCodeAI/trace/cli/trailers"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// RewindReset performs a reset rewind using the CLI.
// This resets the branch to the specified commit (destructive).
func (env *TestEnv) RewindReset(commitID string) error {
	env.T.Helper()

	// Run rewind --to <commitID> --reset using the shared binary
	cmd := exec.Command(getTestBinary(), "checkpoint", "rewind", "--to", commitID, "--reset")
	cmd.Dir = env.RepoDir
	cmd.Env = env.cliEnv()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New("rewind reset failed: " + string(output))
	}

	env.T.Logf("Rewind reset output: %s", output)
	return nil
}

// BranchExists checks if a branch exists in the repository.
func (env *TestEnv) BranchExists(branchName string) bool {
	env.T.Helper()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	_, err = repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	return err == nil
}

// GetCommitMessage returns the commit message for the given commit hash.
func (env *TestEnv) GetCommitMessage(hash string) string {
	env.T.Helper()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	commitHash := plumbing.NewHash(hash)
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		env.T.Fatalf("failed to get commit %s: %v", hash, err)
	}

	return commit.Message
}

// FileExistsInBranch checks if a file exists in a specific branch's tree.
func (env *TestEnv) FileExistsInBranch(branchName, filePath string) bool {
	env.T.Helper()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	// Get the branch reference
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	if err != nil {
		// Try as a remote-style ref
		ref, err = repo.Reference(plumbing.ReferenceName("refs/heads/"+branchName), true)
		if err != nil {
			return false
		}
	}

	// Get the commit
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return false
	}

	// Get the tree
	tree, err := commit.Tree()
	if err != nil {
		return false
	}

	// Check if file exists
	_, err = tree.File(filePath)
	return err == nil
}

// ReadFileFromBranch reads a file's content from a specific branch's tree.
// Returns the content and true if found, empty string and false if not found.
func (env *TestEnv) ReadFileFromBranch(branchName, filePath string) (string, bool) {
	env.T.Helper()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	// Get the branch reference
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	if err != nil {
		// Try as a remote-style ref
		ref, err = repo.Reference(plumbing.ReferenceName("refs/heads/"+branchName), true)
		if err != nil {
			return "", false
		}
	}

	// Get the commit
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return "", false
	}

	// Get the tree
	tree, err := commit.Tree()
	if err != nil {
		return "", false
	}

	// Get the file
	file, err := tree.File(filePath)
	if err != nil {
		return "", false
	}

	// Get the content
	content, err := file.Contents()
	if err != nil {
		return "", false
	}

	return content, true
}

// ReadFileFromRef reads a file's content from a specific ref's tree.
// Unlike ReadFileFromBranch, this takes a full ref name (e.g., "refs/trace/checkpoints/v2/main")
// and does not prepend "refs/heads/".
// Returns the content and true if found, empty string and false if not found.
func (env *TestEnv) ReadFileFromRef(refName, filePath string) (string, bool) {
	env.T.Helper()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	ref, err := repo.Reference(plumbing.ReferenceName(refName), true)
	if err != nil {
		return "", false
	}

	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return "", false
	}

	tree, err := commit.Tree()
	if err != nil {
		return "", false
	}

	file, err := tree.File(filePath)
	if err != nil {
		return "", false
	}

	content, err := file.Contents()
	if err != nil {
		return "", false
	}

	return content, true
}

// AssertCheckpointContainsSession verifies that the checkpoint summary includes
// a session with the given session ID by reading per-session metadata from the
// metadata branch.
func (env *TestEnv) AssertCheckpointContainsSession(t *testing.T, summary checkpoint.CheckpointSummary, sessionID string) {
	t.Helper()
	for _, s := range summary.Sessions {
		if env.sessionMetadataMatchesID(s.Metadata, sessionID) {
			return
		}
	}
	t.Errorf("Checkpoint did not include session %q", sessionID)
}

// AssertCheckpointExcludesSession verifies that the checkpoint summary does NOT
// include a session with the given session ID.
func (env *TestEnv) AssertCheckpointExcludesSession(t *testing.T, summary checkpoint.CheckpointSummary, sessionID string) {
	t.Helper()
	for _, s := range summary.Sessions {
		if env.sessionMetadataMatchesID(s.Metadata, sessionID) {
			t.Errorf("Checkpoint incorrectly included session %q", sessionID)
			return
		}
	}
}

// sessionMetadataMatchesID reads session metadata from the metadata branch and
// checks if it belongs to the given session ID.
func (env *TestEnv) sessionMetadataMatchesID(metadataPath, sessionID string) bool {
	// Strip leading slash — git tree paths are relative
	cleanPath := strings.TrimPrefix(metadataPath, "/")
	content, found := env.ReadFileFromBranch(paths.MetadataBranchName, cleanPath)
	if !found {
		return false
	}
	var meta checkpoint.CommittedMetadata
	if err := json.Unmarshal([]byte(content), &meta); err != nil {
		return false
	}
	return meta.SessionID == sessionID
}

// RefExists checks if a ref exists in the repository.
func (env *TestEnv) RefExists(refName string) bool {
	env.T.Helper()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	_, err = repo.Reference(plumbing.ReferenceName(refName), true)
	return err == nil
}

// GetLatestCommitMessageOnBranch returns the commit message of the latest commit on the given branch.
func (env *TestEnv) GetLatestCommitMessageOnBranch(branchName string) string {
	env.T.Helper()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	// Get the branch reference
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
	if err != nil {
		env.T.Fatalf("failed to get branch %s reference: %v", branchName, err)
	}

	// Get the commit
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		env.T.Fatalf("failed to get commit object: %v", err)
	}

	return commit.Message
}

// GitCommitWithShadowHooks stages and commits files, simulating the prepare-commit-msg
// and post-commit hooks as a human (with TTY). This is the default for tests.
func (env *TestEnv) GitCommitWithShadowHooks(message string, files ...string) {
	env.T.Helper()
	env.gitCommitWithShadowHooks(message, true, files...)
}

// GitCommitWithShadowHooksAsAgent is like GitCommitWithShadowHooks but simulates
// an agent commit (no TTY). This triggers the fast path in PrepareCommitMsg that
// skips content detection and interactive prompts for ACTIVE sessions.
func (env *TestEnv) GitCommitWithShadowHooksAsAgent(message string, files ...string) {
	env.T.Helper()
	env.gitCommitWithShadowHooks(message, false, files...)
}

// prepareCommitMsgCmd builds the prepare-commit-msg hook command. When
// simulateTTY is true, TRACE_TEST_TTY=1 forces interactive=true (an in-test
// stand-in for a real terminal — Setsid can't synthesize a TTY). When false,
// the child runs in a new session without a controlling terminal so its
// /dev/tty probe fails and CanPromptInteractively() returns false.
func (env *TestEnv) prepareCommitMsgCmd(simulateTTY bool, hookArgs ...string) *exec.Cmd {
	args := append([]string{"hooks", "git", "prepare-commit-msg"}, hookArgs...)
	var cmd *exec.Cmd
	if simulateTTY {
		cmd = exec.Command(getTestBinary(), args...)
		cmd.Env = env.gitHookEnv("TRACE_TEST_TTY=1")
	} else {
		cmd = execx.NonInteractive(context.Background(), getTestBinary(), args...)
		cmd.Env = env.gitHookEnv()
	}
	cmd.Dir = env.RepoDir
	return cmd
}

// gitCommitWithShadowHooks is the shared implementation for committing with shadow hooks.
func (env *TestEnv) gitCommitWithShadowHooks(message string, simulateTTY bool, files ...string) {
	env.T.Helper()

	// Stage files using go-git
	for _, file := range files {
		env.GitAdd(file)
	}

	// Create a temp file for the commit message (prepare-commit-msg hook modifies this)
	msgFile := filepath.Join(env.RepoDir, ".git", "COMMIT_EDITMSG")
	if err := os.WriteFile(msgFile, []byte(message), 0o644); err != nil {
		env.T.Fatalf("failed to write commit message file: %v", err)
	}

	// Run prepare-commit-msg hook using the shared binary.
	// Pass source="message" to match real `git commit -m` behavior.
	prepCmd := env.prepareCommitMsgCmd(simulateTTY, msgFile, "message")
	if output, err := prepCmd.CombinedOutput(); err != nil {
		env.T.Logf("prepare-commit-msg output: %s", output)
		// Don't fail - hook may silently succeed
	}

	// Read the modified message
	modifiedMsg, err := os.ReadFile(msgFile)
	if err != nil {
		env.T.Fatalf("failed to read modified commit message: %v", err)
	}

	// Create the commit using go-git with the modified message
	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		env.T.Fatalf("failed to get worktree: %v", err)
	}

	_, err = worktree.Commit(string(modifiedMsg), &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		env.T.Fatalf("failed to commit: %v", err)
	}

	// Run post-commit hook using the shared binary
	// This triggers condensation if the commit has an Trace-Checkpoint trailer
	postCmd := exec.Command(getTestBinary(), "hooks", "git", "post-commit")
	postCmd.Dir = env.RepoDir
	postCmd.Env = env.gitHookEnv()
	if output, err := postCmd.CombinedOutput(); err != nil {
		env.T.Logf("post-commit output: %s", output)
		// Don't fail - hook may silently succeed
	}
}

func (env *TestEnv) gitHookEnv(extra ...string) []string {
	envVars := append(
		testutil.GitIsolatedEnv(),
		"TRACE_TEST_OPENCODE_PROJECT_DIR="+env.OpenCodeProjectDir,
		"TRACE_TEST_OPENCODE_MOCK_EXPORT=1",
	)
	return append(envVars, extra...)
}

// GitCommitAmendWithShadowHooks amends the last commit with shadow hooks.
// This simulates `git commit --amend` with the prepare-commit-msg and post-commit hooks.
// The prepare-commit-msg hook is called with "commit" source to indicate an amend.
func (env *TestEnv) GitCommitAmendWithShadowHooks(message string, files ...string) {
	env.T.Helper()

	// Stage any additional files
	for _, file := range files {
		env.GitAdd(file)
	}

	// Write commit message to temp file
	msgFile := filepath.Join(env.RepoDir, ".git", "COMMIT_EDITMSG")
	if err := os.WriteFile(msgFile, []byte(message), 0o644); err != nil {
		env.T.Fatalf("failed to write commit message file: %v", err)
	}

	// Run prepare-commit-msg hook with "commit" source (indicates amend).
	// Set TRACE_TEST_TTY=1 to simulate human (amend is always a human operation).
	prepCmd := exec.Command(getTestBinary(), "hooks", "git", "prepare-commit-msg", msgFile, "commit")
	prepCmd.Dir = env.RepoDir
	prepCmd.Env = env.gitHookEnv("TRACE_TEST_TTY=1")
	if output, err := prepCmd.CombinedOutput(); err != nil {
		env.T.Logf("prepare-commit-msg (amend) output: %s", output)
	}

	// Read the modified message
	modifiedMsg, err := os.ReadFile(msgFile)
	if err != nil {
		env.T.Fatalf("failed to read modified commit message: %v", err)
	}

	// Amend the commit using go-git
	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		env.T.Fatalf("failed to get worktree: %v", err)
	}

	_, err = worktree.Commit(string(modifiedMsg), &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
		Amend: true,
	})
	if err != nil {
		env.T.Fatalf("failed to amend commit: %v", err)
	}

	// Run post-commit hook
	postCmd := exec.Command(getTestBinary(), "hooks", "git", "post-commit")
	postCmd.Dir = env.RepoDir
	postCmd.Env = env.gitHookEnv()
	if output, err := postCmd.CombinedOutput(); err != nil {
		env.T.Logf("post-commit (amend) output: %s", output)
	}
}

// GitPostRewriteWithShadowHooks runs the git post-rewrite hook with the provided
// old->new commit mappings. Each mapping is a pair of commit SHAs.
func (env *TestEnv) GitPostRewriteWithShadowHooks(rewriteType string, mappings ...[2]string) {
	env.T.Helper()

	var input strings.Builder
	for _, mapping := range mappings {
		input.WriteString(mapping[0])
		input.WriteByte(' ')
		input.WriteString(mapping[1])
		input.WriteByte('\n')
	}

	cmd := exec.Command(getTestBinary(), "hooks", "git", "post-rewrite", rewriteType)
	cmd.Dir = env.RepoDir
	cmd.Env = env.gitHookEnv()
	cmd.Stdin = strings.NewReader(input.String())
	if output, err := cmd.CombinedOutput(); err != nil {
		env.T.Fatalf("post-rewrite hook failed: %v\nOutput: %s", err, output)
	}
}

// GitCommitWithTrailerRemoved stages and commits files, simulating what happens when
// a user removes the Trace-Checkpoint trailer during commit message editing.
// This tests the opt-out behavior where removing the trailer skips condensation.
func (env *TestEnv) GitCommitWithTrailerRemoved(message string, files ...string) {
	env.T.Helper()

	// Stage files using go-git
	for _, file := range files {
		env.GitAdd(file)
	}

	// Create a temp file for the commit message (prepare-commit-msg hook modifies this)
	msgFile := filepath.Join(env.RepoDir, ".git", "COMMIT_EDITMSG")
	if err := os.WriteFile(msgFile, []byte(message), 0o644); err != nil {
		env.T.Fatalf("failed to write commit message file: %v", err)
	}

	// Run prepare-commit-msg hook using the shared binary.
	// Set TRACE_TEST_TTY=1 to simulate human (this tests the editor flow where
	// the user removes the trailer before committing).
	prepCmd := exec.Command(getTestBinary(), "hooks", "git", "prepare-commit-msg", msgFile)
	prepCmd.Dir = env.RepoDir
	prepCmd.Env = env.gitHookEnv("TRACE_TEST_TTY=1")
	if output, err := prepCmd.CombinedOutput(); err != nil {
		env.T.Logf("prepare-commit-msg output: %s", output)
	}

	// Read the modified message (with trailer added by hook)
	modifiedMsg, err := os.ReadFile(msgFile)
	if err != nil {
		env.T.Fatalf("failed to read modified commit message: %v", err)
	}

	// REMOVE the Trace-Checkpoint trailer (simulating user editing the message)
	lines := strings.Split(string(modifiedMsg), "\n")
	var cleanedLines []string
	for _, line := range lines {
		// Skip the trailer and the comments about it
		if strings.HasPrefix(line, "Trace-Checkpoint:") {
			continue
		}
		if strings.Contains(line, "Remove the Trace-Checkpoint trailer") {
			continue
		}
		if strings.Contains(line, "trailer will be added to your next commit") {
			continue
		}
		cleanedLines = append(cleanedLines, line)
	}
	cleanedMsg := strings.TrimRight(strings.Join(cleanedLines, "\n"), "\n") + "\n"

	// Create the commit using go-git with the cleaned message (no trailer)
	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		env.T.Fatalf("failed to get worktree: %v", err)
	}

	_, err = worktree.Commit(cleanedMsg, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		env.T.Fatalf("failed to commit: %v", err)
	}

	// Run post-commit hook - since trailer was removed, no condensation should happen
	postCmd := exec.Command(getTestBinary(), "hooks", "git", "post-commit")
	postCmd.Dir = env.RepoDir
	postCmd.Env = env.gitHookEnv()
	if output, err := postCmd.CombinedOutput(); err != nil {
		env.T.Logf("post-commit output: %s", output)
	}
}

// GitRm stages file deletions using git rm.
func (env *TestEnv) GitRm(paths ...string) {
	env.T.Helper()

	args := append([]string{"rm", "--"}, paths...)
	cmd := exec.Command("git", args...)
	cmd.Dir = env.RepoDir
	cmd.Env = testutil.GitIsolatedEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		env.T.Fatalf("git rm failed: %v\nOutput: %s", err, output)
	}
}

// GitCommitStagedWithShadowHooks commits whatever is already staged (without adding files first),
// running the prepare-commit-msg and post-commit hooks like a real workflow.
// Use this after GitRm or when files are already staged.
func (env *TestEnv) GitCommitStagedWithShadowHooks(message string) {
	env.T.Helper()
	env.gitCommitStagedWithShadowHooks(message, true)
}

// gitCommitStagedWithShadowHooks is the shared implementation for committing staged changes with hooks.
func (env *TestEnv) gitCommitStagedWithShadowHooks(message string, simulateTTY bool) {
	env.T.Helper()

	// Create a temp file for the commit message (prepare-commit-msg hook modifies this)
	msgFile := filepath.Join(env.RepoDir, ".git", "COMMIT_EDITMSG")
	if err := os.WriteFile(msgFile, []byte(message), 0o644); err != nil {
		env.T.Fatalf("failed to write commit message file: %v", err)
	}

	// Run prepare-commit-msg hook using the shared binary.
	prepCmd := env.prepareCommitMsgCmd(simulateTTY, msgFile, "message")
	if output, err := prepCmd.CombinedOutput(); err != nil {
		env.T.Logf("prepare-commit-msg output: %s", output)
	}

	// Read the modified message
	modifiedMsg, err := os.ReadFile(msgFile)
	if err != nil {
		env.T.Fatalf("failed to read modified commit message: %v", err)
	}

	// Create the commit using go-git with the modified message
	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		env.T.Fatalf("failed to get worktree: %v", err)
	}

	_, err = worktree.Commit(string(modifiedMsg), &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test User",
			Email: "test@example.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		env.T.Fatalf("failed to commit: %v", err)
	}

	// Run post-commit hook
	postCmd := exec.Command(getTestBinary(), "hooks", "git", "post-commit")
	postCmd.Dir = env.RepoDir
	postCmd.Env = env.gitHookEnv()
	if output, err := postCmd.CombinedOutput(); err != nil {
		env.T.Logf("post-commit output: %s", output)
	}
}

// ListBranchesWithPrefix returns all branches that start with the given prefix.
func (env *TestEnv) ListBranchesWithPrefix(prefix string) []string {
	env.T.Helper()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	refs, err := repo.References()
	if err != nil {
		env.T.Fatalf("failed to get references: %v", err)
	}

	var branches []string
	_ = refs.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().Short()
		if len(name) >= len(prefix) && name[:len(prefix)] == prefix {
			branches = append(branches, name)
		}
		return nil
	})

	return branches
}

// GetLatestCheckpointID returns the most recent checkpoint ID from the trace/checkpoints/v1 branch.
// This is used by tests that previously extracted the checkpoint ID from commit message trailers.
// Now that active branch commits are clean (no trailers), we get the ID from the sessions branch.
// Fatals if the checkpoint ID cannot be found, with detailed context about what was found.
func (env *TestEnv) GetLatestCheckpointID() string {
	env.T.Helper()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	// Get the trace/checkpoints/v1 branch
	refName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	ref, err := repo.Reference(refName, true)
	if err != nil {
		env.T.Fatalf("failed to get %s branch: %v", paths.MetadataBranchName, err)
	}

	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		env.T.Fatalf("failed to get commit: %v", err)
	}

	// Extract checkpoint ID from commit message
	// Format: "Checkpoint: <12-hex-char-id>\n\nSession: ...\nStrategy: ..."
	for _, line := range strings.Split(commit.Message, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Checkpoint: ") {
			return strings.TrimPrefix(line, "Checkpoint: ")
		}
	}

	env.T.Fatalf("could not find checkpoint ID in %s branch commit message:\n%s",
		paths.MetadataBranchName, commit.Message)
	return ""
}

// TryGetLatestCheckpointID returns the most recent checkpoint ID from the trace/checkpoints/v1 branch.
// Returns empty string if the branch doesn't exist or has no checkpoint commits yet.
// Use this when you need to check if a checkpoint exists without failing the test.
func (env *TestEnv) TryGetLatestCheckpointID() string {
	env.T.Helper()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		return ""
	}

	// Get the trace/checkpoints/v1 branch
	refName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	ref, err := repo.Reference(refName, true)
	if err != nil {
		return ""
	}

	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return ""
	}

	// Extract checkpoint ID from commit message
	// Format: "Checkpoint: <12-hex-char-id>\n\nSession: ...\nStrategy: ..."
	for _, line := range strings.Split(commit.Message, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Checkpoint: ") {
			return strings.TrimPrefix(line, "Checkpoint: ")
		}
	}

	return ""
}

// GetLatestCondensationID is an alias for GetLatestCheckpointID for backwards compatibility.
func (env *TestEnv) GetLatestCondensationID() string {
	return env.GetLatestCheckpointID()
}

// GetCheckpointIDFromCommitMessage extracts the Trace-Checkpoint trailer from a commit message.
// Returns empty string if no trailer found.
func (env *TestEnv) GetCheckpointIDFromCommitMessage(commitSHA string) string {
	env.T.Helper()

	msg := env.GetCommitMessage(commitSHA)
	cpID, found := trailers.ParseCheckpoint(msg)
	if !found {
		return ""
	}
	return cpID.String()
}

// GetLatestCheckpointIDFromHistory walks backwards from HEAD on the active branch
// and returns the checkpoint ID from the first commit that has an Trace-Checkpoint trailer.
// This verifies that condensation actually happened (commit has trailer) without relying
// on timestamp-based matching.
func (env *TestEnv) GetLatestCheckpointIDFromHistory() string {
	env.T.Helper()

	repo, err := git.PlainOpen(env.RepoDir)
	if err != nil {
		env.T.Fatalf("failed to open git repo: %v", err)
	}

	head, err := repo.Head()
	if err != nil {
		env.T.Fatalf("failed to get HEAD: %v", err)
	}

	commitIter, err := repo.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		env.T.Fatalf("failed to iterate commits: %v", err)
	}

	var checkpointID string
	//nolint:errcheck // ForEach callback handles errors
	commitIter.ForEach(func(c *object.Commit) error {
		if cpID, found := trailers.ParseCheckpoint(c.Message); found {
			checkpointID = cpID.String()
			return errors.New("stop iteration") // Found it, stop
		}
		return nil
	})

	if checkpointID == "" {
		env.T.Fatalf("no commit with Trace-Checkpoint trailer found in history")
	}

	return checkpointID
}

// ShardedCheckpointPath returns the sharded path for a checkpoint ID.
// Format: <id[:2]>/<id[2:]>
// Delegates to id.CheckpointID.Path() for consistency.
func ShardedCheckpointPath(checkpointID string) string {
	return id.CheckpointID(checkpointID).Path()
}

// SessionFilePath returns the path to a session file within a checkpoint.
// Session files are stored in numbered subdirectories using 0-based indexing (e.g., 0/full.jsonl).
// This function constructs the path for the first (default) session.
func SessionFilePath(checkpointID string, fileName string) string {
	return id.CheckpointID(checkpointID).Path() + "/0/" + fileName
}

// CheckpointSummaryPath returns the path to the root metadata.json (CheckpointSummary) for a checkpoint.
func CheckpointSummaryPath(checkpointID string) string {
	return id.CheckpointID(checkpointID).Path() + "/" + paths.MetadataFileName
}

// SessionMetadataPath returns the path to the session-level metadata.json for a checkpoint.
func SessionMetadataPath(checkpointID string) string {
	return SessionFilePath(checkpointID, paths.MetadataFileName)
}
