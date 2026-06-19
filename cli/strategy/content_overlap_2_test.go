package strategy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStagedFilesOverlapWithContent_ModifiedFile tests that a modified file
// (exists in HEAD) always counts as overlap.
func TestStagedFilesOverlapWithContent_ModifiedFile(t *testing.T) {
	t.Parallel()
	dir := setupGitRepo(t)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	// Initial file is created by setupGitRepo
	// Modify it and stage
	testFile := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("modified content"), 0o644))
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add("test.txt")
	require.NoError(t, err)

	// Create shadow branch (content doesn't matter for modified files)
	createShadowBranchWithContent(t, repo, "abc1234", "e3b0c4", map[string][]byte{
		"test.txt": []byte("shadow content"),
	})

	// Get shadow tree
	shadowBranch := checkpoint.ShadowBranchNameForCommit("abc1234", "e3b0c4")
	shadowRef, err := repo.Reference(plumbing.NewBranchReferenceName(shadowBranch), true)
	require.NoError(t, err)
	shadowCommit, err := repo.CommitObject(shadowRef.Hash())
	require.NoError(t, err)
	shadowTree, err := shadowCommit.Tree()
	require.NoError(t, err)

	// Modified file should count as overlap regardless of content
	result := stagedFilesOverlapWithContent(context.Background(), repo, shadowTree, []string{"test.txt"}, []string{"test.txt"})
	assert.True(t, result, "Modified file should always count as overlap")
}

// TestStagedFilesOverlapWithContent_NewFile_ContentMatch tests that a new file
// with matching content counts as overlap.
func TestStagedFilesOverlapWithContent_NewFile_ContentMatch(t *testing.T) {
	t.Parallel()
	dir := setupGitRepo(t)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	// Create a NEW file (doesn't exist in HEAD)
	content := []byte("new file content")
	newFile := filepath.Join(dir, "newfile.txt")
	require.NoError(t, os.WriteFile(newFile, content, 0o644))
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add("newfile.txt")
	require.NoError(t, err)

	// Create shadow branch with SAME content
	createShadowBranchWithContent(t, repo, "def5678", "e3b0c4", map[string][]byte{
		"newfile.txt": content,
	})

	// Get shadow tree
	shadowBranch := checkpoint.ShadowBranchNameForCommit("def5678", "e3b0c4")
	shadowRef, err := repo.Reference(plumbing.NewBranchReferenceName(shadowBranch), true)
	require.NoError(t, err)
	shadowCommit, err := repo.CommitObject(shadowRef.Hash())
	require.NoError(t, err)
	shadowTree, err := shadowCommit.Tree()
	require.NoError(t, err)

	// New file with matching content should count as overlap
	result := stagedFilesOverlapWithContent(context.Background(), repo, shadowTree, []string{"newfile.txt"}, []string{"newfile.txt"})
	assert.True(t, result, "New file with matching content should count as overlap")
}

// TestStagedFilesOverlapWithContent_NewFile_ContentMismatch tests that a new file
// with different content does NOT count as overlap (reverted & replaced scenario).
func TestStagedFilesOverlapWithContent_NewFile_ContentMismatch(t *testing.T) {
	t.Parallel()
	dir := setupGitRepo(t)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	// Create a NEW file with different content than shadow branch
	newFile := filepath.Join(dir, "newfile.txt")
	require.NoError(t, os.WriteFile(newFile, []byte("user replaced content"), 0o644))
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add("newfile.txt")
	require.NoError(t, err)

	// Create shadow branch with DIFFERENT content (agent's original)
	createShadowBranchWithContent(t, repo, "ghi9012", "e3b0c4", map[string][]byte{
		"newfile.txt": []byte("agent original content"),
	})

	// Get shadow tree
	shadowBranch := checkpoint.ShadowBranchNameForCommit("ghi9012", "e3b0c4")
	shadowRef, err := repo.Reference(plumbing.NewBranchReferenceName(shadowBranch), true)
	require.NoError(t, err)
	shadowCommit, err := repo.CommitObject(shadowRef.Hash())
	require.NoError(t, err)
	shadowTree, err := shadowCommit.Tree()
	require.NoError(t, err)

	// New file with different content should NOT count as overlap
	result := stagedFilesOverlapWithContent(context.Background(), repo, shadowTree, []string{"newfile.txt"}, []string{"newfile.txt"})
	assert.False(t, result, "New file with mismatched content should not count as overlap")
}

// TestStagedFilesOverlapWithContent_NoOverlap tests that non-overlapping files
// return false.
func TestStagedFilesOverlapWithContent_NoOverlap(t *testing.T) {
	t.Parallel()
	dir := setupGitRepo(t)

	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)

	// Stage a file NOT in filesTouched
	otherFile := filepath.Join(dir, "other.txt")
	require.NoError(t, os.WriteFile(otherFile, []byte("other content"), 0o644))
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add("other.txt")
	require.NoError(t, err)

	// Create shadow branch
	createShadowBranchWithContent(t, repo, "jkl3456", "e3b0c4", map[string][]byte{
		"session.txt": []byte("session content"),
	})

	// Get shadow tree
	shadowBranch := checkpoint.ShadowBranchNameForCommit("jkl3456", "e3b0c4")
	shadowRef, err := repo.Reference(plumbing.NewBranchReferenceName(shadowBranch), true)
	require.NoError(t, err)
	shadowCommit, err := repo.CommitObject(shadowRef.Hash())
	require.NoError(t, err)
	shadowTree, err := shadowCommit.Tree()
	require.NoError(t, err)

	// Staged file "other.txt" is not in filesTouched "session.txt"
	result := stagedFilesOverlapWithContent(context.Background(), repo, shadowTree, []string{"other.txt"}, []string{"session.txt"})
	assert.False(t, result, "Non-overlapping files should return false")
}

// TestStagedFilesOverlapWithContent_DeletedFile tests that a deleted file
// (exists in HEAD but staged for deletion) DOES count as overlap.
// The agent's action of deleting the file is being committed, so the session
// context should be linked to this commit.
func TestStagedFilesOverlapWithContent_DeletedFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)

	worktree, err := repo.Worktree()
	require.NoError(t, err)

	// Create and commit a file that will be deleted
	filePath := filepath.Join(dir, "to_delete.txt")
	err = os.WriteFile(filePath, []byte("original content"), 0o644)
	require.NoError(t, err)
	_, err = worktree.Add("to_delete.txt")
	require.NoError(t, err)
	_, err = worktree.Commit("Add to_delete.txt", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)

	// Create shadow branch (simulating agent work on the file)
	createShadowBranchWithContent(t, repo, "mno7890", "e3b0c4", map[string][]byte{
		"to_delete.txt": []byte("agent modified content"),
	})

	// Stage the file for deletion (git rm)
	_, err = worktree.Remove("to_delete.txt")
	require.NoError(t, err)

	// Get shadow tree
	shadowBranch := checkpoint.ShadowBranchNameForCommit("mno7890", "e3b0c4")
	shadowRef, err := repo.Reference(plumbing.NewBranchReferenceName(shadowBranch), true)
	require.NoError(t, err)
	shadowCommit, err := repo.CommitObject(shadowRef.Hash())
	require.NoError(t, err)
	shadowTree, err := shadowCommit.Tree()
	require.NoError(t, err)

	// Deleted file SHOULD count as overlap - the agent's deletion is being committed
	result := stagedFilesOverlapWithContent(context.Background(), repo, shadowTree, []string{"to_delete.txt"}, []string{"to_delete.txt"})
	assert.True(t, result, "Deleted file should count as overlap (agent's deletion being committed)")
}

// createShadowBranchWithContent creates a shadow branch with the given file contents.
// This helper directly uses go-git APIs to avoid paths.WorktreeRoot() dependency.
//
//nolint:unparam // worktreeID is kept as a parameter for flexibility even if tests currently use same value
func createShadowBranchWithContent(t *testing.T, repo *git.Repository, baseCommit, worktreeID string, fileContents map[string][]byte) {
	t.Helper()

	shadowBranchName := checkpoint.ShadowBranchNameForCommit(baseCommit, worktreeID)
	refName := plumbing.NewBranchReferenceName(shadowBranchName)

	// Get HEAD for base tree
	head, err := repo.Head()
	require.NoError(t, err)

	headCommit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)

	baseTree, err := headCommit.Tree()
	require.NoError(t, err)

	// Flatten existing tree into map
	entries := make(map[string]object.TreeEntry)
	err = checkpoint.FlattenTree(repo, baseTree, "", entries)
	require.NoError(t, err)

	// Add/update files with provided content
	for filePath, content := range fileContents {
		// Create blob with content
		blob := repo.Storer.NewEncodedObject()
		blob.SetType(plumbing.BlobObject)
		blob.SetSize(int64(len(content)))
		writer, err := blob.Writer()
		require.NoError(t, err)
		_, err = writer.Write(content)
		require.NoError(t, err)
		err = writer.Close()
		require.NoError(t, err)

		blobHash, err := repo.Storer.SetEncodedObject(blob)
		require.NoError(t, err)

		entries[filePath] = object.TreeEntry{
			Name: filePath,
			Mode: filemode.Regular,
			Hash: blobHash,
		}
	}

	// Build tree from entries
	treeHash, err := checkpoint.BuildTreeFromEntries(context.Background(), repo, entries)
	require.NoError(t, err)

	// Create commit
	commit := &object.Commit{
		TreeHash: treeHash,
		Message:  "Test checkpoint",
		Author: object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now(),
		},
		Committer: object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now(),
		},
	}

	commitObj := repo.Storer.NewEncodedObject()
	err = commit.Encode(commitObj)
	require.NoError(t, err)

	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	require.NoError(t, err)

	// Create branch reference
	newRef := plumbing.NewHashReference(refName, commitHash)
	err = repo.Storer.SetReference(newRef)
	require.NoError(t, err)
}

// TestExtractSignificantLines tests the line extraction with length-based filtering.
// Lines must be >= 10 characters after trimming whitespace.
func TestExtractSignificantLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		content  string
		wantKeys []string // lines that should be in the result
		wantNot  []string // lines that should NOT be in the result
	}{
		{
			name: "go function",
			content: `package main

func hello() {
	fmt.Println("hello world")
	return
}`,
			wantKeys: []string{
				"package main",               // 12 chars
				"func hello() {",             // 14 chars
				`fmt.Println("hello world")`, // 26 chars
			},
			wantNot: []string{
				"}",      // 1 char
				"return", // 6 chars
			},
		},
		{
			name: "python function",
			content: `def calculate(x, y):
    result = x + y
    print(f"Result: {result}")
    return result`,
			wantKeys: []string{
				"def calculate(x, y):",       // 20 chars
				"result = x + y",             // 14 chars
				`print(f"Result: {result}")`, // 25 chars
				"return result",              // 13 chars
			},
			wantNot: []string{},
		},
		{
			name: "javascript",
			content: `const handler = async (req) => {
  const data = await fetch(url);
  return data.json();
};`,
			wantKeys: []string{
				"const handler = async (req) => {", // 32 chars
				"const data = await fetch(url);",   // 30 chars
				"return data.json();",              // 19 chars
			},
			wantNot: []string{
				"};", // 2 chars
			},
		},
		{
			name: "short lines filtered",
			content: `a = 1
b = 2
longVariableName = 42`,
			wantKeys: []string{
				"longVariableName = 42", // 21 chars
			},
			wantNot: []string{
				"a = 1", // 5 chars
				"b = 2", // 5 chars
			},
		},
		{
			name: "structural lines filtered by length",
			content: `{
  });
  ]);
  },
}`,
			wantKeys: []string{},
			wantNot: []string{
				"{",   // 1 char
				"});", // 3 chars
				"]);", // 3 chars
				"},",  // 2 chars
				"}",   // 1 char
			},
		},
		{
			name: "regex and special chars kept if long enough",
			content: `short
/^[a-z0-9]+@[a-z]+\.[a-z]{2,}$/
x`,
			wantKeys: []string{
				"/^[a-z0-9]+@[a-z]+\\.[a-z]{2,}$/", // 32 chars - kept even though mostly non-alpha
			},
			wantNot: []string{
				"short", // 5 chars
				"x",     // 1 char
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := extractSignificantLines(tt.content)

			for _, want := range tt.wantKeys {
				if !result[want] {
					t.Errorf("extractSignificantLines() missing expected line: %q", want)
				}
			}

			for _, notWant := range tt.wantNot {
				if result[notWant] {
					t.Errorf("extractSignificantLines() should not contain: %q", notWant)
				}
			}
		})
	}
}

// TestHasSignificantContentOverlap tests the content overlap detection logic.
// We require at least 2 matching significant lines to count as overlap.
func TestHasSignificantContentOverlap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		stagedContent string
		shadowContent string
		wantOverlap   bool
	}{
		{
			name:          "two matching significant lines - overlap",
			stagedContent: "this is a significant line\nanother matching line here\nshort",
			shadowContent: "this is a significant line\nanother matching line here\nother",
			wantOverlap:   true,
		},
		{
			name:          "only one matching significant line - no overlap",
			stagedContent: "this is a significant line\ncompletely different staged",
			shadowContent: "this is a significant line\ncompletely different shadow",
			wantOverlap:   false,
		},
		{
			name:          "no matching significant lines",
			stagedContent: "completely different content here",
			shadowContent: "this is the shadow content now",
			wantOverlap:   false,
		},
		{
			name:          "both have only short lines - no significant content",
			stagedContent: "a = 1\nb = 2\nc = 3",
			shadowContent: "x = 1\ny = 2\nz = 3",
			wantOverlap:   false,
		},
		{
			name:          "shadow has significant lines but staged has none",
			stagedContent: "a = 1\nb = 2",
			shadowContent: "this is significant content from shadow",
			wantOverlap:   false,
		},
		{
			name:          "staged has significant lines but shadow has none",
			stagedContent: "this is significant content from staged",
			shadowContent: "x = 1\ny = 2",
			wantOverlap:   false,
		},
		{
			name:          "empty strings",
			stagedContent: "",
			shadowContent: "",
			wantOverlap:   false,
		},
		{
			name:          "single shared line like package main - no overlap (boilerplate)",
			stagedContent: "package main\nfunc NewImplementation() {}",
			shadowContent: "package main\nfunc OriginalCode() {}",
			wantOverlap:   false,
		},
		{
			name:          "multiple shared lines - overlap (user kept agent work)",
			stagedContent: "package main\nfunc SharedFunction() {\nreturn nil",
			shadowContent: "package main\nfunc SharedFunction() {\nreturn nil",
			wantOverlap:   true,
		},
		{
			name:          "very small file with single match - overlap (small file exception)",
			stagedContent: "this is a unique line here\nshort",
			shadowContent: "this is a unique line here\nshort",
			wantOverlap:   true, // Shadow has only 1 significant line, so 1 match counts
		},
		{
			name:          "very small file no match - no overlap",
			stagedContent: "completely different staged content",
			shadowContent: "short",
			wantOverlap:   false, // Shadow is very small but no matching lines
		},
		{
			name:          "large staged vs very small shadow with single match - overlap",
			stagedContent: "line one here\nline two here\nline three here\nshared content line",
			shadowContent: "shared content line\nshort",
			wantOverlap:   true, // Shadow has only 1 significant line, so 1 match counts
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := hasSignificantContentOverlap(tt.stagedContent, tt.shadowContent)
			if got != tt.wantOverlap {
				t.Errorf("hasSignificantContentOverlap() = %v, want %v", got, tt.wantOverlap)
			}
		})
	}
}

// TestTrimLine tests whitespace trimming from lines.
func TestTrimLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want string
	}{
		{"no whitespace", "hello", "hello"},
		{"leading spaces", "   hello", "hello"},
		{"trailing spaces", "hello   ", "hello"},
		{"both leading and trailing spaces", "   hello   ", "hello"},
		{"leading tabs", "\t\thello", "hello"},
		{"trailing tabs", "hello\t\t", "hello"},
		{"mixed whitespace", " \t hello \t ", "hello"},
		{"only spaces", "     ", ""},
		{"only tabs", "\t\t\t", ""},
		{"empty string", "", ""},
		{"spaces in middle preserved", "hello world", "hello world"},
		{"tabs in middle preserved", "hello\tworld", "hello\tworld"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := trimLine(tt.line)
			if got != tt.want {
				t.Errorf("trimLine(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}
