package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// buildTreeWithChanges builds a git tree with the given changes.
// metadataDir is the relative path for git tree entries, metadataDirAbs is the absolute path
// for filesystem operations (needed when CLI is run from a subdirectory).
//
// Uses ApplyTreeChanges (tree surgery) instead of FlattenTree+BuildTreeFromEntries,
// so only affected subtrees are read/rebuilt — O(changed dirs) instead of O(total files).
func (s *GitStore) buildTreeWithChanges(
	ctx context.Context,
	baseTreeHash plumbing.Hash,
	modifiedFiles, deletedFiles []string,
	metadataDir, metadataDirAbs string,
) (plumbing.Hash, error) {
	// Get worktree root for resolving file paths
	// This is critical because fileExists() and createBlobFromFile() use os.Stat()
	// which resolves relative to CWD. The modifiedFiles are repo-relative paths,
	// so we must resolve them against repo root, not CWD.
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to get worktree root: %w", err)
	}

	// Build list of tree changes
	changes := make([]TreeChange, 0, len(modifiedFiles)+len(deletedFiles))

	// Deleted files → nil Entry means deletion
	for _, file := range deletedFiles {
		relPath, relErr := normalizeRepoRelativeTreePath(repoRoot, file)
		if relErr != nil {
			logInvalidGitTreePath(ctx, "delete shadow branch entry", file, relErr)
			continue
		}
		changes = append(changes, TreeChange{Path: relPath, Entry: nil})
	}

	// Modified/new files → create blobs from disk
	for _, file := range modifiedFiles {
		relPath, relErr := normalizeRepoRelativeTreePath(repoRoot, file)
		if relErr != nil {
			logInvalidGitTreePath(ctx, "add shadow branch entry", file, relErr)
			continue
		}

		absPath := filepath.Join(repoRoot, filepath.FromSlash(relPath))
		if !fileExists(absPath) {
			// File disappeared since detection — treat as deletion
			changes = append(changes, TreeChange{Path: relPath, Entry: nil})
			continue
		}

		blobHash, mode, blobErr := createBlobFromFile(s.repo, absPath)
		if blobErr != nil {
			// Skip files that can't be staged (may have been deleted since detection)
			continue
		}

		changes = append(changes, TreeChange{
			Path: relPath,
			Entry: &object.TreeEntry{
				Mode: mode,
				Hash: blobHash,
			},
		})
	}

	// Metadata directory files
	if metadataDir != "" && metadataDirAbs != "" {
		metadataRel, relErr := normalizeRepoRelativeTreePath(repoRoot, metadataDir)
		if relErr != nil {
			logInvalidGitTreePath(ctx, "add metadata directory", metadataDir, relErr)
		} else {
			metaChanges, metaErr := addDirectoryToChanges(s.repo, metadataDirAbs, metadataRel)
			if metaErr != nil {
				return plumbing.ZeroHash, fmt.Errorf("failed to add metadata directory: %w", metaErr)
			}
			changes = append(changes, metaChanges...)
		}
	}

	return ApplyTreeChanges(ctx, s.repo, baseTreeHash, changes)
}

// createCommit creates a commit object.
func (s *GitStore) createCommit(ctx context.Context, treeHash, parentHash plumbing.Hash, message, authorName, authorEmail string) (plumbing.Hash, error) {
	return CreateCommit(ctx, s.repo, treeHash, parentHash, message, authorName, authorEmail)
}

// Helper functions extracted from strategy/common.go
// These are exported for use by strategy package (push_common.go, session_test.go)

// FlattenTree recursively flattens a tree into a map of full paths to entries.
func FlattenTree(repo *git.Repository, tree *object.Tree, prefix string, entries map[string]object.TreeEntry) error {
	for _, entry := range tree.Entries {
		fullPath := entry.Name
		if prefix != "" {
			fullPath = prefix + "/" + entry.Name
		}

		if entry.Mode == filemode.Dir {
			// Recurse into subtree
			subtree, err := repo.TreeObject(entry.Hash)
			if err != nil {
				return fmt.Errorf("failed to get subtree %s: %w", fullPath, err)
			}
			if err := FlattenTree(repo, subtree, fullPath, entries); err != nil {
				return err
			}
		} else {
			entries[fullPath] = object.TreeEntry{
				Name: fullPath,
				Mode: entry.Mode,
				Hash: entry.Hash,
			}
		}
	}
	return nil
}

// fileExists checks if a file exists at the given path.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// createBlobFromFile creates a blob object from a file in the working directory.
func createBlobFromFile(repo *git.Repository, filePath string) (plumbing.Hash, filemode.FileMode, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return plumbing.ZeroHash, 0, fmt.Errorf("failed to stat file: %w", err)
	}

	// Determine file mode
	mode := filemode.Regular
	if info.Mode()&0o111 != 0 {
		mode = filemode.Executable
	}
	if info.Mode()&os.ModeSymlink != 0 {
		mode = filemode.Symlink
	}

	// Read file contents
	// #nosec G304 -- filePath comes from walking the repository tree, not external input
	content, err := os.ReadFile(filePath) //nolint:gosec // filePath comes from walking the repository
	if err != nil {
		return plumbing.ZeroHash, 0, fmt.Errorf("failed to read file: %w", err)
	}

	// Create blob object
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	obj.SetSize(int64(len(content)))

	writer, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, 0, fmt.Errorf("failed to get object writer: %w", err)
	}

	_, err = writer.Write(content)
	if err != nil {
		_ = writer.Close()
		return plumbing.ZeroHash, 0, fmt.Errorf("failed to write blob content: %w", err)
	}
	if err := writer.Close(); err != nil {
		return plumbing.ZeroHash, 0, fmt.Errorf("failed to close blob writer: %w", err)
	}

	hash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, 0, fmt.Errorf("failed to store blob object: %w", err)
	}

	return hash, mode, nil
}

// addDirectoryToEntriesWithAbsPath recursively adds all files in a directory to the entries map.
func addDirectoryToEntriesWithAbsPath(repo *git.Repository, dirPathAbs, dirPathRel string, entries map[string]object.TreeEntry) error {
	err := filepath.Walk(dirPathAbs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip symlinks to prevent reading files outside the metadata directory.
		// A symlink could point to sensitive files (e.g., /etc/passwd) which would
		// then be captured in the checkpoint and stored in git history.
		// NOTE: filepath.Walk uses os.Stat (follows symlinks), so info.Mode() never
		// reports ModeSymlink. We use os.Lstat to check the entry itself.
		// This check MUST come before IsDir() because Walk follows symlinked
		// directories and would recurse into them otherwise.
		linfo, lstatErr := os.Lstat(path)
		if lstatErr != nil {
			return fmt.Errorf("failed to lstat %s: %w", path, lstatErr)
		}
		if linfo.Mode()&os.ModeSymlink != 0 {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			return nil
		}

		// Calculate relative path within the directory, then join with dirPathRel for tree entry
		relWithinDir, err := filepath.Rel(dirPathAbs, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path for %s: %w", path, err)
		}

		// Prevent path traversal via symlinks pointing outside the metadata dir
		if strings.HasPrefix(relWithinDir, "..") {
			return fmt.Errorf("path traversal detected: %s", relWithinDir)
		}

		treePath := filepath.ToSlash(filepath.Join(dirPathRel, relWithinDir))

		// Use redacted blob creation for metadata files (transcripts, prompts, etc.)
		// to ensure PII and secrets are redacted before writing to git.
		blobHash, mode, err := createRedactedBlobFromFile(repo, path, treePath)
		if err != nil {
			return fmt.Errorf("failed to create blob for %s: %w", path, err)
		}
		entries[treePath] = object.TreeEntry{
			Name: treePath,
			Mode: mode,
			Hash: blobHash,
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to walk directory %s: %w", dirPathAbs, err)
	}
	return nil
}

// treeNode represents a node in our tree structure.
type treeNode struct {
	entries map[string]*treeNode // subdirectories
	files   []object.TreeEntry   // files in this directory
}

// addDirectoryToChanges walks a filesystem directory and returns TreeChange entries
// for each file, suitable for use with ApplyTreeChanges.
// dirPathAbs is the absolute filesystem path; dirPathRel is the git tree-relative path.
func addDirectoryToChanges(repo *git.Repository, dirPathAbs, dirPathRel string) ([]TreeChange, error) {
	var changes []TreeChange
	err := filepath.Walk(dirPathAbs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip symlinks (same security rationale as addDirectoryToEntriesWithAbsPath)
		linfo, lstatErr := os.Lstat(path)
		if lstatErr != nil {
			return fmt.Errorf("failed to lstat %s: %w", path, lstatErr)
		}
		if linfo.Mode()&os.ModeSymlink != 0 {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if info.IsDir() {
			return nil
		}

		relWithinDir, relErr := filepath.Rel(dirPathAbs, path)
		if relErr != nil {
			return fmt.Errorf("failed to get relative path for %s: %w", path, relErr)
		}
		if strings.HasPrefix(relWithinDir, "..") {
			return fmt.Errorf("path traversal detected: %s", relWithinDir)
		}

		treePath := filepath.ToSlash(filepath.Join(dirPathRel, relWithinDir))

		blobHash, mode, blobErr := createRedactedBlobFromFile(repo, path, treePath)
		if blobErr != nil {
			return fmt.Errorf("failed to create blob for %s: %w", path, blobErr)
		}
		changes = append(changes, TreeChange{
			Path:  treePath,
			Entry: &object.TreeEntry{Mode: mode, Hash: blobHash},
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to walk directory %s: %w", dirPathAbs, err)
	}
	return changes, nil
}

// BuildTreeFromEntries builds a proper git tree structure from flattened file entries.
// Exported for use by strategy package (push_common.go, session_test.go)
func BuildTreeFromEntries(ctx context.Context, repo *git.Repository, entries map[string]object.TreeEntry) (plumbing.Hash, error) {
	// Build a tree structure
	root := &treeNode{
		entries: make(map[string]*treeNode),
		files:   []object.TreeEntry{},
	}

	// Insert all entries into the tree structure
	for fullPath, entry := range entries {
		normalizedPath, err := normalizeGitTreePath(fullPath)
		if err != nil {
			logInvalidGitTreePath(ctx, "build tree entry", fullPath, err)
			continue
		}
		parts := strings.Split(normalizedPath, "/")
		insertIntoTree(root, parts, entry)
	}

	// Recursively build tree objects from bottom up
	return buildTreeObject(repo, root)
}

func normalizeRepoRelativeTreePath(repoRoot, path string) (string, error) {
	if rel := paths.ToRelativePath(path, repoRoot); rel != "" && rel != "." {
		return normalizeGitTreePath(rel)
	}

	return normalizeGitTreePath(path)
}

// insertIntoTree inserts a file entry into the tree structure.
func insertIntoTree(node *treeNode, pathParts []string, entry object.TreeEntry) {
	if len(pathParts) == 1 {
		// This is a file in the current directory
		node.files = append(node.files, object.TreeEntry{
			Name: pathParts[0],
			Mode: entry.Mode,
			Hash: entry.Hash,
		})
		return
	}

	// This is in a subdirectory
	dirName := pathParts[0]
	if node.entries[dirName] == nil {
		node.entries[dirName] = &treeNode{
			entries: make(map[string]*treeNode),
			files:   []object.TreeEntry{},
		}
	}
	insertIntoTree(node.entries[dirName], pathParts[1:], entry)
}

// buildTreeObject recursively builds tree objects from a treeNode.
func buildTreeObject(repo *git.Repository, node *treeNode) (plumbing.Hash, error) {
	var treeEntries []object.TreeEntry

	// Add files
	treeEntries = append(treeEntries, node.files...)

	// Recursively build subtrees
	for name, subnode := range node.entries {
		subHash, err := buildTreeObject(repo, subnode)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		treeEntries = append(treeEntries, object.TreeEntry{
			Name: name,
			Mode: filemode.Dir,
			Hash: subHash,
		})
	}

	// Sort entries (git requires sorted entries)
	sortTreeEntries(treeEntries)

	// Create tree object
	tree := &object.Tree{Entries: treeEntries}

	obj := repo.Storer.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to encode tree: %w", err)
	}

	hash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to store tree: %w", err)
	}

	return hash, nil
}

// sortTreeEntries sorts tree entries in git's required order.
// Git sorts tree entries by name, with directories having a trailing /
func sortTreeEntries(entries []object.TreeEntry) {
	sort.Slice(entries, func(i, j int) bool {
		nameI := entries[i].Name
		nameJ := entries[j].Name
		if entries[i].Mode == filemode.Dir {
			nameI += "/"
		}
		if entries[j].Mode == filemode.Dir {
			nameJ += "/"
		}
		return nameI < nameJ
	})
}

// collectChangedFiles collects all changed files (modified tracked + untracked non-ignored)
// using git CLI. This is much faster than filesystem walk and respects all gitignore sources
// including global gitignore (core.excludesfile).
//
// Uses git CLI instead of go-git because go-git's worktree.Status() does not respect
// global gitignore, which can cause globally ignored files to appear as untracked.
// See: https://github.com/GrayCodeAI/trace/pull/129
//
// changedFilesResult contains both changed and deleted files from git status.
type changedFilesResult struct {
	Changed []string // Files to include (modified, added, untracked, renamed, etc.)
	Deleted []string // Files that were deleted (need to be excluded from checkpoint tree)
}

// filterGitIgnoredFiles removes gitignored files from the list using `git check-ignore`.
// This prevents secrets in gitignored files (e.g., .env) from leaking into shadow branch
// commits when agents report them as modified/new in their transcripts.
// On failure, fails closed (returns nil) to avoid leaking secrets.
func filterGitIgnoredFiles(ctx context.Context, repo *git.Repository, files []string) []string {
	if len(files) == 0 {
		return files
	}

	wt, err := repo.Worktree()
	if err != nil {
		logging.Warn(logging.WithComponent(ctx, "checkpoint"),
			"failed to inspect worktree for gitignore filtering, excluding all files from checkpoint",
			slog.String("error", err.Error()))
		return nil
	}
	repoRoot := wt.Filesystem.Root()

	// Use git check-ignore to identify which files are ignored.
	// Pass files via stdin (-z for NUL-separated, --stdin) to handle special characters.
	// Use --no-index so even tracked files that still match ignore rules are filtered.
	cmd := exec.CommandContext(ctx, "git", "check-ignore", "--no-index", "-z", "--stdin")
	cmd.Dir = repoRoot
	cmd.Stdin = strings.NewReader(strings.Join(files, "\x00") + "\x00")

	output, err := cmd.Output()
	if err != nil {
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			// Exit code 1 means no files are ignored — all files are safe.
			return files
		}
		// Any other failure (exit 128, git not found, etc.): fail closed.
		// A missing checkpoint is better than leaked secrets.
		logging.Warn(logging.WithComponent(ctx, "checkpoint"),
			"git check-ignore failed, excluding all files from checkpoint",
			slog.String("error", err.Error()))
		return nil
	}

	// Parse NUL-separated output of ignored file names
	ignored := make(map[string]struct{})
	for _, name := range strings.Split(string(output), "\x00") {
		if name != "" {
			ignored[name] = struct{}{}
		}
	}

	// Filter: keep only files that are not ignored
	var kept []string
	filteredCount := 0
	for _, file := range files {
		if _, isIgnored := ignored[file]; isIgnored {
			filteredCount++
			continue
		}
		kept = append(kept, file)
	}

	if filteredCount > 0 {
		logging.Debug(logging.WithComponent(ctx, "checkpoint"),
			"filtered gitignored files from checkpoint",
			slog.Int("count", filteredCount))
	}

	return kept
}

// collectChangedFiles returns all changed files from git status for the first checkpoint.
//
// For the first checkpoint, we need to capture:
// - Modified tracked files (user's uncommitted changes)
// - Untracked non-ignored files (new files not yet added to git)
// - Renamed/copied files (both source removal and destination)
// - Deleted files (to exclude from checkpoint tree)
//
// The base tree from HEAD already contains all unchanged tracked files.
//
// Uses `git status --porcelain -z` for reliable parsing of filenames with special characters.
func collectChangedFiles(ctx context.Context, repo *git.Repository) (changedFilesResult, error) {
	// Get worktree root directory for running git command
	wt, err := repo.Worktree()
	if err != nil {
		return changedFilesResult{}, fmt.Errorf("failed to get worktree: %w", err)
	}
	repoRoot := wt.Filesystem.Root()

	// Use -z for NUL-separated output (handles quoted filenames with spaces/special chars)
	// Use -uall to list individual untracked files instead of collapsed directories.
	// Note: CLAUDE.md warns against -uall for user-facing display, but we need the full list
	// for checkpointing.
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", "-z", "-uall")
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		return changedFilesResult{}, fmt.Errorf("failed to get git status in %s: %w", repoRoot, err)
	}

	changedSeen := make(map[string]struct{})
	deletedSeen := make(map[string]struct{})

	// Parse NUL-separated output
	// Format: XY filename\0 (for most entries)
	// For renames/copies: XY newname\0oldname\0
	entries := strings.Split(string(output), "\x00")

	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 3 {
			continue
		}

		// git status --porcelain format: XY filename
		// X = staging status, Y = worktree status
		staging := entry[0]
		wtStatus := entry[1]
		filename := entry[3:] // No TrimSpace needed with -z format

		// Handle R/C (rename/copy) first - they have a second entry we must skip
		// even if the new filename is an infrastructure path
		if staging == 'R' || staging == 'C' {
			// Renamed or copied: current entry is new name, next entry is old name
			if !paths.IsInfrastructurePath(filename) {
				changedSeen[filename] = struct{}{}
			}
			// The old name follows as the next NUL-separated entry - must always skip it
			if i+1 < len(entries) && entries[i+1] != "" {
				oldName := entries[i+1]
				if staging == 'R' && !paths.IsInfrastructurePath(oldName) {
					// For renames, old file is effectively deleted
					deletedSeen[oldName] = struct{}{}
				}
				i++ // Skip the old name entry
			}
			continue
		}

		// Skip .trace directory for non-R/C entries
		if paths.IsInfrastructurePath(filename) {
			continue
		}

		// Handle different status codes
		switch {
		case staging == 'D' || wtStatus == 'D':
			// Deleted file - track separately
			deletedSeen[filename] = struct{}{}

		case wtStatus == 'M' || wtStatus == 'A':
			// Modified or added in worktree
			changedSeen[filename] = struct{}{}

		case staging == '?' && wtStatus == '?':
			// Untracked file
			changedSeen[filename] = struct{}{}

		case staging == 'A' || staging == 'M':
			// Staged add or modify
			changedSeen[filename] = struct{}{}

		case staging == 'T' || wtStatus == 'T':
			// Type change (e.g., file to symlink)
			changedSeen[filename] = struct{}{}

		case staging == 'U' || wtStatus == 'U':
			// Unmerged (conflict) - include current file state
			changedSeen[filename] = struct{}{}
		}
	}

	changed := make([]string, 0, len(changedSeen))
	for file := range changedSeen {
		changed = append(changed, file)
	}

	deleted := make([]string, 0, len(deletedSeen))
	for file := range deletedSeen {
		deleted = append(deleted, file)
	}

	return changedFilesResult{Changed: changed, Deleted: deleted}, nil
}
