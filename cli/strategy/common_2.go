package strategy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/trailers"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// isOnlySeparators checks if a string contains only dashes, spaces, and newlines.
func isOnlySeparators(s string) bool {
	for _, r := range s {
		if r != '-' && r != ' ' && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

// ReadLatestSessionPromptFromCommittedTree reads the first prompt from a committed checkpoint's
// latest session on the metadata branch tree. This navigates the sharded directory layout:
// <cpID.Path()>/<latestSessionIndex>/prompt.txt
//
// Falls back through earlier sessions when the latest has no prompt.
// Avoids reading full transcripts — only reads prompt.txt files.
// sessionCount is the number of sessions in the checkpoint (from CommittedInfo.SessionCount).
func ReadLatestSessionPromptFromCommittedTree(tree *object.Tree, cpID id.CheckpointID, sessionCount int) string {
	cpPath := cpID.Path()
	cpTree, err := tree.Tree(cpPath)
	if err != nil {
		return ""
	}

	// Find the latest session subdirectory with a prompt.
	// Sessions use 0-based indexing: 0/, 1/, 2/, etc.
	// Start from the latest and fall back through earlier sessions
	// when the latest has no prompt (e.g. a test or empty session was
	// condensed alongside a real one).
	latestIndex := max(sessionCount-1, 0)

	for i := latestIndex; i >= 0; i-- {
		sessionPath := strconv.Itoa(i)
		sessionTree, err := cpTree.Tree(sessionPath)
		if err != nil {
			continue
		}

		file, err := sessionTree.File(paths.PromptFileName)
		if err != nil {
			continue
		}

		content, err := file.Contents()
		if err != nil {
			continue
		}

		if prompt := ExtractFirstPrompt(content); prompt != "" {
			return prompt
		}
	}

	return ""
}

// ReadAllSessionPromptsFromTree reads the first prompt for all sessions in a multi-session checkpoint.
// Returns a slice of prompts parallel to sessionIDs (oldest to newest).
// For single-session checkpoints, returns a slice with just the root prompt.
func ReadAllSessionPromptsFromTree(tree *object.Tree, checkpointPath string, sessionCount int, sessionIDs []string) []string {
	if sessionCount <= 1 || len(sessionIDs) <= 1 {
		// Single session - just return the root prompt
		prompt := ReadSessionPromptFromTree(tree, checkpointPath)
		if prompt != "" {
			return []string{prompt}
		}
		return nil
	}

	// Multi-session: read prompts from archived folders (0/, 1/, etc.) and root
	prompts := make([]string, len(sessionIDs))

	// Read archived session prompts (folders 0, 1, ... N-2)
	for i := range sessionCount - 1 {
		archivedPath := fmt.Sprintf("%s/%d", checkpointPath, i)
		prompts[i] = ReadSessionPromptFromTree(tree, archivedPath)
	}

	// Read the most recent session prompt (at root level)
	prompts[len(prompts)-1] = ReadSessionPromptFromTree(tree, checkpointPath)

	return prompts
}

// GetRemoteMetadataBranchTree returns the tree object for origin/trace/checkpoints/v1.
func GetRemoteMetadataBranchTree(repo *git.Repository) (*object.Tree, error) {
	refName := plumbing.NewRemoteReferenceName("origin", paths.MetadataBranchName)
	ref, err := repo.Reference(refName, true)
	if err != nil {
		return nil, fmt.Errorf("failed to get remote metadata branch reference: %w", err)
	}

	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get remote metadata branch commit: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get remote metadata branch tree: %w", err)
	}
	return tree, nil
}

// OpenRepository opens the git repository from the repo root.
// Each call returns a fresh instance to avoid storer contention between
// concurrent goroutines — go-git's filesystem storer is not safe for
// concurrent read+write even across separate Repository instances that
// share the same .git directory.
func OpenRepository(ctx context.Context) (*git.Repository, error) {
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		// Fallback to current directory if git command fails
		// (e.g., if git is not installed or we're not in a repo)
		repoRoot = "."
	}

	repo, err := git.PlainOpen(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}
	return repo, nil
}

// IsInsideWorktree returns true if the current directory is inside a git worktree
// (as opposed to the main repository). Worktrees have .git as a file pointing
// to the main repo, while the main repo has .git as a directory.
// This function works correctly from any subdirectory within the repository.
func IsInsideWorktree(ctx context.Context) bool {
	// First find the repository root
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return false
	}

	gitPath := filepath.Join(repoRoot, gitDir)
	gitInfo, err := os.Stat(gitPath)
	if err != nil {
		return false
	}
	return !gitInfo.IsDir()
}

// GetMainRepoRoot returns the root directory of the main repository.
// In the main repo, this is the worktree path (repo root).
// In a worktree, this parses the .git file to find the main repo.
// This function works correctly from any subdirectory within the repository.
//
// Per gitrepository-layout(5), a worktree's .git file is a "gitfile" containing
// "gitdir: <path>" pointing to $GIT_DIR/worktrees/<id> in the main repository.
// See: https://git-scm.com/docs/gitrepository-layout
func GetMainRepoRoot(ctx context.Context) (string, error) {
	// First find the worktree/repo root
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get worktree path: %w", err)
	}

	if !IsInsideWorktree(ctx) {
		return repoRoot, nil
	}

	// Worktree .git file contains: "gitdir: /path/to/main/.git/worktrees/<id>"
	gitFilePath := filepath.Join(repoRoot, gitDir)
	content, err := os.ReadFile(gitFilePath) //nolint:gosec // G304: gitFilePath is constructed from repo root, not user input
	if err != nil {
		return "", fmt.Errorf("failed to read .git file: %w", err)
	}

	gitdir := strings.TrimSpace(string(content))
	gitdir = strings.TrimPrefix(gitdir, "gitdir: ")

	// Extract main repo root: everything before "/.git/"
	idx := strings.LastIndex(gitdir, "/.git/")
	if idx < 0 {
		return "", fmt.Errorf("unexpected gitdir format: %s", gitdir)
	}
	return gitdir[:idx], nil
}

// GetGitCommonDir returns the path to the shared git directory.
// In a regular checkout, this is .git/
// In a worktree, this is the main repo's .git/ (not .git/worktrees/<name>/)
// Uses git rev-parse --git-common-dir for reliable handling of worktrees.
func GetGitCommonDir(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--git-common-dir")
	cmd.Dir = "."
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get git common dir: %w", err)
	}

	commonDir := strings.TrimSpace(string(output))

	// git rev-parse --git-common-dir returns relative paths from the working directory,
	// so we need to make it absolute if it isn't already
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(".", commonDir)
	}

	return filepath.Clean(commonDir), nil
}

// EnsureTraceGitignore ensures all required entries are in .trace/.gitignore
// Works correctly from any subdirectory within the repository.
func EnsureTraceGitignore(ctx context.Context) error {
	// Get absolute path for the gitignore file
	gitignoreAbs, err := paths.AbsPath(ctx, traceGitignore)
	if err != nil {
		gitignoreAbs = traceGitignore // Fallback to relative
	}

	// Read existing content
	var content string
	if data, err := os.ReadFile(gitignoreAbs); err == nil { //nolint:gosec // path is from AbsPath or constant
		content = string(data)
	}

	// All entries that should be in .trace/.gitignore
	requiredEntries := []string{
		"tmp/",
		"settings.local.json",
		"metadata/",
		"logs/",
	}

	// Track what needs to be added
	var toAdd []string
	for _, entry := range requiredEntries {
		if !strings.Contains(content, entry) {
			toAdd = append(toAdd, entry)
		}
	}

	// Nothing to add
	if len(toAdd) == 0 {
		return nil
	}

	// Ensure .trace directory exists
	if err := os.MkdirAll(filepath.Dir(gitignoreAbs), 0o750); err != nil {
		return fmt.Errorf("failed to create .trace directory: %w", err)
	}

	// Append missing entries to gitignore
	var sb strings.Builder
	for _, entry := range toAdd {
		sb.WriteString(entry + "\n")
	}
	content += sb.String()

	if err := os.WriteFile(gitignoreAbs, []byte(content), 0o644); err != nil { //nolint:gosec // path is from AbsPath or constant
		return fmt.Errorf("failed to write gitignore: %w", err)
	}
	return nil
}

// checkCanRewindWithWarning checks working directory and returns a warning with diff stats.
// Always returns canRewind=true but includes a warning message with +/- line stats for
// uncommitted changes. Used by manual-commit strategy.
func checkCanRewindWithWarning(ctx context.Context) (bool, string, error) {
	repo, err := OpenRepository(ctx)
	if err != nil {
		// Can't open repo - still allow rewind but without stats
		return true, "", nil //nolint:nilerr // Rewind allowed even if repo can't be opened
	}

	worktree, err := repo.Worktree()
	if err != nil {
		return true, "", nil //nolint:nilerr // Rewind allowed even if worktree can't be accessed
	}

	status, err := worktree.Status()
	if err != nil {
		return true, "", nil //nolint:nilerr // Rewind allowed even if status can't be retrieved
	}

	if status.IsClean() {
		return true, "", nil
	}

	// Get HEAD commit tree for comparison - if we can't get it, just return without stats
	head, err := repo.Head()
	if err != nil {
		return true, "", nil //nolint:nilerr // Rewind allowed even without HEAD (e.g., empty repo)
	}

	headCommit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return true, "", nil //nolint:nilerr // Rewind allowed even if commit lookup fails
	}

	headTree, err := headCommit.Tree()
	if err != nil {
		return true, "", nil //nolint:nilerr // Rewind allowed even if tree lookup fails
	}

	type fileChange struct {
		status   string // "modified", "added", "deleted"
		added    int
		removed  int
		filename string
	}

	var changes []fileChange
	// Use repo root, not cwd - git status returns paths relative to repo root
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return true, "", nil //nolint:nilerr // Rewind allowed even if worktree root lookup fails
	}

	for file, st := range status {
		// Skip .trace directory
		if paths.IsInfrastructurePath(file) {
			continue
		}

		// Skip untracked files
		if st.Worktree == git.Untracked {
			continue
		}

		var change fileChange
		change.filename = file

		switch {
		case st.Staging == git.Added || st.Worktree == git.Added:
			change.status = "added"
			// New file - count all lines as added
			absPath := filepath.Join(repoRoot, file)
			if content, err := os.ReadFile(absPath); err == nil { //nolint:gosec // absPath is repo root + relative path from git status
				change.added = countLines(content)
			}
		case st.Staging == git.Deleted || st.Worktree == git.Deleted:
			change.status = "deleted"
			// Deleted file - count lines from HEAD as removed
			if entry, err := headTree.File(file); err == nil {
				if content, err := entry.Contents(); err == nil {
					change.removed = countLines([]byte(content))
				}
			}
		case st.Staging == git.Modified || st.Worktree == git.Modified:
			change.status = "modified"
			// Modified file - compute diff stats
			var headContent, workContent []byte
			if entry, err := headTree.File(file); err == nil {
				if content, err := entry.Contents(); err == nil {
					headContent = []byte(content)
				}
			}
			absPath := filepath.Join(repoRoot, file)
			if content, err := os.ReadFile(absPath); err == nil { //nolint:gosec // absPath is repo root + relative path from git status
				workContent = content
			}
			if headContent != nil && workContent != nil {
				change.added, change.removed = computeDiffStats(headContent, workContent)
			}
		default:
			continue
		}

		changes = append(changes, change)
	}

	if len(changes) == 0 {
		return true, "", nil
	}

	// Sort changes by filename for consistent output
	sort.Slice(changes, func(i, j int) bool {
		return changes[i].filename < changes[j].filename
	})

	var msg strings.Builder
	msg.WriteString("The following uncommitted changes will be reverted:\n")

	totalAdded, totalRemoved := 0, 0
	for _, c := range changes {
		totalAdded += c.added
		totalRemoved += c.removed

		var stats string
		switch {
		case c.added > 0 && c.removed > 0:
			stats = fmt.Sprintf("+%d/-%d", c.added, c.removed)
		case c.added > 0:
			stats = fmt.Sprintf("+%d", c.added)
		case c.removed > 0:
			stats = fmt.Sprintf("-%d", c.removed)
		}

		fmt.Fprintf(&msg, "  %-10s %s", c.status+":", c.filename)
		if stats != "" {
			fmt.Fprintf(&msg, " (%s)", stats)
		}
		msg.WriteString("\n")
	}

	if totalAdded > 0 || totalRemoved > 0 {
		fmt.Fprintf(&msg, "\nTotal: +%d/-%d lines\n", totalAdded, totalRemoved)
	}

	return true, msg.String(), nil
}

// countLines counts the number of lines in content.
func countLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	count := 1
	for _, b := range content {
		if b == '\n' {
			count++
		}
	}
	// Don't count trailing newline as extra line
	if len(content) > 0 && content[len(content)-1] == '\n' {
		count--
	}
	return count
}

// computeDiffStats computes added and removed line counts between old and new content.
// Uses a simple line-based diff algorithm.
func computeDiffStats(oldContent, newContent []byte) (added, removed int) {
	oldLines := splitLines(oldContent)
	newLines := splitLines(newContent)

	// Build a set of old lines with counts
	oldSet := make(map[string]int)
	for _, line := range oldLines {
		oldSet[line]++
	}

	// Check which new lines are truly new
	for _, line := range newLines {
		if oldSet[line] > 0 {
			oldSet[line]--
		} else {
			added++
		}
	}

	// Remaining old lines are removed
	for _, count := range oldSet {
		removed += count
	}

	return added, removed
}

// splitLines splits content into lines, preserving empty lines.
// Handles both Unix (\n) and Windows (\r\n) line endings.
func splitLines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	s := string(content)
	// Normalize Windows line endings to Unix
	s = strings.ReplaceAll(s, "\r\n", "\n")
	// Remove trailing newline to avoid empty last element
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// fileExists checks if a file exists at the given path.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// getTaskCheckpointFromTree retrieves a task checkpoint from a commit tree.
// Shared implementation for shadow and linear-shadow strategies.
func getTaskCheckpointFromTree(ctx context.Context, point RewindPoint) (*TaskCheckpoint, error) {
	if !point.IsTaskCheckpoint {
		return nil, ErrNotTaskCheckpoint
	}

	repo, err := OpenRepository(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	commitHash := plumbing.NewHash(point.ID)
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	// Read checkpoint.json from the tree
	checkpointPath := point.MetadataDir + "/checkpoint.json"
	file, err := tree.File(checkpointPath)
	if err != nil {
		return nil, fmt.Errorf("failed to find checkpoint at %s: %w", checkpointPath, err)
	}

	content, err := file.Contents()
	if err != nil {
		return nil, fmt.Errorf("failed to read checkpoint: %w", err)
	}

	var checkpoint TaskCheckpoint
	if err := json.Unmarshal([]byte(content), &checkpoint); err != nil {
		return nil, fmt.Errorf("failed to parse checkpoint: %w", err)
	}

	return &checkpoint, nil
}

// getTaskTranscriptFromTree retrieves a task transcript from a commit tree.
// Shared implementation for shadow and linear-shadow strategies.
func getTaskTranscriptFromTree(ctx context.Context, point RewindPoint) ([]byte, error) {
	if !point.IsTaskCheckpoint {
		return nil, ErrNotTaskCheckpoint
	}

	repo, err := OpenRepository(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open repository: %w", err)
	}

	commitHash := plumbing.NewHash(point.ID)
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	// MetadataDir format: .trace/metadata/<session>/tasks/<toolUseID>
	// Session transcript is at: .trace/metadata/<session>/<TranscriptFileName>
	sessionDir := filepath.Dir(filepath.Dir(point.MetadataDir))

	// Try current format first, then legacy
	transcriptPath := sessionDir + "/" + paths.TranscriptFileName
	file, err := tree.File(transcriptPath)
	if err != nil {
		transcriptPath = sessionDir + "/" + paths.TranscriptFileNameLegacy
		file, err = tree.File(transcriptPath)
		if err != nil {
			return nil, fmt.Errorf("failed to find transcript: %w", err)
		}
	}

	content, err := file.Contents()
	if err != nil {
		return nil, fmt.Errorf("failed to read transcript: %w", err)
	}

	return []byte(content), nil
}

// ErrBranchNotFound is returned by DeleteBranchCLI when the branch does not exist.
var ErrBranchNotFound = errors.New("branch not found")

// ErrRefNotFound is returned by DeleteRefCLI when the ref does not exist.
var ErrRefNotFound = errors.New("ref not found")

// ErrRefChanged is returned by DeleteRefCLI when the ref no longer points to the expected OID.
var ErrRefChanged = errors.New("ref changed since inspection")

// DeleteBranchCLI deletes a git branch using the git CLI.
// Uses `git branch -D` instead of go-git's RemoveReference because go-git v5
// doesn't properly persist deletions when refs are packed (.git/packed-refs)
// or in a worktree context. This is the same class of go-git v5 bug that
// affects checkout and reset --hard (see HardResetWithProtection).
//
// Returns ErrBranchNotFound if the branch does not exist, allowing callers
// to use errors.Is for idempotent deletion patterns.
func DeleteBranchCLI(ctx context.Context, branchName string) error {
	// Pre-check: verify the branch exists so callers get a structured error
	// instead of parsing git's output string (which varies across locales).
	// git show-ref exits 1 for "not found" and 128+ for fatal errors (corrupt
	// repo, permissions, not a git directory). Only map exit code 1 to
	// ErrBranchNotFound; propagate other failures as-is.
	check := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
	if err := check.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return fmt.Errorf("%w: %s", ErrBranchNotFound, branchName)
		}
		return fmt.Errorf("failed to check branch %s: %w", branchName, err)
	}

	cmd := exec.CommandContext(ctx, "git", "branch", "-D", "--", branchName)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to delete branch %s: %s: %w", branchName, strings.TrimSpace(string(output)), err)
	}
	return nil
}

// DeleteRefCLI deletes an arbitrary ref using the git CLI.
// Uses `git update-ref -d` instead of go-git's RemoveReference because go-git
// ref deletion is unreliable with packed refs and worktrees.
//
// When expectedOID is non-empty, it is passed to `git update-ref -d <ref> <old-oid>`
// as a compare-and-swap guard: git will refuse the deletion if the ref no longer
// points to expectedOID, and ErrRefChanged is returned.
//
// Returns ErrRefNotFound if the ref does not exist, allowing callers to use
// errors.Is for idempotent deletion patterns.
func DeleteRefCLI(ctx context.Context, refName string, expectedOID string) error {
	exists, _, err := refStateCLI(ctx, refName)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: %s", ErrRefNotFound, refName)
	}

	args := []string{"update-ref", "-d", refName}
	if expectedOID != "" {
		args = append(args, expectedOID)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return classifyDeleteRefFailure(ctx, refName, expectedOID, output, err)
	}
	return nil
}

func classifyDeleteRefFailure(ctx context.Context, refName string, expectedOID string, output []byte, updateErr error) error {
	baseErr := fmt.Errorf("failed to delete ref %s: %s: %w", refName, strings.TrimSpace(string(output)), updateErr)

	exists, currentOID, stateErr := refStateCLI(ctx, refName)
	if stateErr != nil {
		return baseErr
	}
	if !exists {
		return fmt.Errorf("%w: %s", ErrRefNotFound, refName)
	}
	if expectedOID != "" && currentOID != expectedOID {
		return fmt.Errorf("%w: %s (expected %s)", ErrRefChanged, refName, expectedOID)
	}

	return baseErr
}

func refStateCLI(ctx context.Context, refName string) (exists bool, oid string, err error) {
	check := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", refName)
	if err := check.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, "", nil
		}
		return false, "", fmt.Errorf("failed to check ref %s: %w", refName, err)
	}

	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", refName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, "", fmt.Errorf("failed to resolve ref %s: %s: %w", refName, strings.TrimSpace(string(output)), err)
	}

	return true, strings.TrimSpace(string(output)), nil
}

// branchExistsCLI checks if a branch exists using git CLI.
// Returns nil if the branch exists, or an error if it does not.
func branchExistsCLI(ctx context.Context, branchName string) error {
	cmd := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branchName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("branch %s not found: %w", branchName, err)
	}
	return nil
}

// HardResetWithProtection performs a git reset --hard to the specified commit.
// Uses the git CLI instead of go-git because go-git's HardReset incorrectly
// deletes untracked directories (like .trace/) even when they're in .gitignore.
// Returns the short commit ID (7 chars) on success for display purposes.
func HardResetWithProtection(ctx context.Context, commitHash plumbing.Hash) (shortID string, err error) {
	hashStr := commitHash.String()
	cmd := exec.CommandContext(ctx, "git", "reset", "--hard", hashStr)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("reset failed: %s: %w", strings.TrimSpace(string(output)), err)
	}

	// Return short commit ID for display
	shortID = hashStr
	if len(shortID) > 7 {
		shortID = shortID[:7]
	}
	return shortID, nil
}

// collectUntrackedFiles collects untracked files in the working directory that are
// NOT ignored by .gitignore. This is used to capture the initial state when starting
// a session, ensuring untracked files present at session start are preserved during rewind.
// Uses "git ls-files --others --exclude-standard -z" to respect .gitignore rules,
// avoiding bloated session state from large ignored directories like node_modules/.
// Returns paths relative to the repository root.
func collectUntrackedFiles(ctx context.Context) ([]string, error) {
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		repoRoot = "."
	}

	cmd := exec.CommandContext(ctx, "git", "ls-files", "--others", "--exclude-standard", "-z")
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("git ls-files failed: %s: %w", strings.TrimSpace(string(exitErr.Stderr)), err)
		}
		return nil, fmt.Errorf("git ls-files failed: %w", err)
	}

	raw := string(output)
	if raw == "" {
		return nil, nil
	}

	var files []string
	for _, f := range strings.Split(raw, "\x00") {
		// Defense-in-depth: filter protected paths even though --exclude-standard should already handle them
		if f != "" && !isProtectedPath(f) {
			files = append(files, f)
		}
	}
	return files, nil
}

// ExtractSessionIDFromCommit extracts the session ID from a commit's trailers.
// It checks the Trace-Session trailer first, then falls back to extracting from
// the metadata directory path in the Trace-Metadata trailer.
// Returns empty string if no session ID is found.
func ExtractSessionIDFromCommit(commit *object.Commit) string {
	// Try Trace-Session trailer first
	if sessionID, found := trailers.ParseSession(commit.Message); found {
		return sessionID
	}

	// Try extracting from metadata directory (last path component)
	if metadataDir, found := trailers.ParseMetadata(commit.Message); found {
		return filepath.Base(metadataDir)
	}

	return ""
}

// NOTE: The following git tree helper functions have been moved to checkpoint/ package:
// - FlattenTree -> checkpoint.FlattenTree
// - CreateBlobFromContent -> checkpoint.CreateBlobFromContent
// - BuildTreeFromEntries -> checkpoint.BuildTreeFromEntries
// - sortTreeEntries (internal to checkpoint package)
// - treeNode, insertIntoTree, buildTreeObject (internal to checkpoint package)
//
// See push_common.go and session_test.go for usage examples.
