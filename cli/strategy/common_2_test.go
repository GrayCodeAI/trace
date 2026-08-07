package strategy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent"
	_ "github.com/GrayCodeAI/trace/cli/agent/claudecode"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/testutil"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetGitAuthorFromRepo(t *testing.T) {
	// Cannot use t.Parallel() because subtests use t.Setenv to isolate global git config.

	tests := []struct {
		name        string
		localName   string
		localEmail  string
		globalName  string
		globalEmail string
		wantName    string
		wantEmail   string
	}{
		{
			name:       "both set locally",
			localName:  "Local User",
			localEmail: "local@example.com",
			wantName:   "Local User",
			wantEmail:  "local@example.com",
		},
		{
			name:        "only name set locally falls back to global for email",
			localName:   "Local User",
			globalEmail: "global@example.com",
			wantName:    "Local User",
			wantEmail:   "global@example.com",
		},
		{
			name:       "only email set locally falls back to global for name",
			localEmail: "local@example.com",
			globalName: "Global User",
			wantName:   "Global User",
			wantEmail:  "local@example.com",
		},
		{
			name:        "nothing set locally falls back to global for both",
			globalName:  "Global User",
			globalEmail: "global@example.com",
			wantName:    "Global User",
			wantEmail:   "global@example.com",
		},
		{
			name:      "nothing set anywhere returns defaults",
			wantName:  "Unknown",
			wantEmail: "unknown@local",
		},
		{
			name:        "local takes precedence over global",
			localName:   "Local User",
			localEmail:  "local@example.com",
			globalName:  "Global User",
			globalEmail: "global@example.com",
			wantName:    "Local User",
			wantEmail:   "local@example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useAutoConfigLoader(t)

			// Isolate global git config by pointing HOME to a temp dir
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", "")

			// Write global .gitconfig if needed
			if tt.globalName != "" || tt.globalEmail != "" {
				globalCfg := "[user]\n"
				if tt.globalName != "" {
					globalCfg += "\tname = " + tt.globalName + "\n"
				}
				if tt.globalEmail != "" {
					globalCfg += "\temail = " + tt.globalEmail + "\n"
				}
				if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(globalCfg), 0o644); err != nil {
					t.Fatalf("failed to write global gitconfig: %v", err)
				}
			}

			// Create a repo for config resolution
			dir := t.TempDir()
			repo, err := git.PlainInit(dir, false)
			if err != nil {
				t.Fatalf("failed to init repo: %v", err)
			}

			// Set local config if needed
			if tt.localName != "" || tt.localEmail != "" {
				cfg, err := repo.Config()
				if err != nil {
					t.Fatalf("failed to get repo config: %v", err)
				}
				cfg.User.Name = tt.localName
				cfg.User.Email = tt.localEmail
				if err := repo.SetConfig(cfg); err != nil {
					t.Fatalf("failed to set repo config: %v", err)
				}
			}

			gotName, gotEmail := GetGitAuthorFromRepo(repo)
			if gotName != tt.wantName {
				t.Errorf("name = %q, want %q", gotName, tt.wantName)
			}
			if gotEmail != tt.wantEmail {
				t.Errorf("email = %q, want %q", gotEmail, tt.wantEmail)
			}
		})
	}
}

func TestIsProtectedPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path      string
		protected bool
	}{
		{".git", true},
		{".git/objects", true},
		{".trace", true},
		{".trace/metadata/session.json", true},
		{".claude", true},
		{".claude/settings.json", true},
		{".gemini", true},
		{".gemini/settings.json", true},
		{"src/main.go", false},
		{"README.md", false},
		{".gitignore", false},
		{".github/workflows/ci.yml", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			if got := isProtectedPath(tt.path); got != tt.protected {
				t.Errorf("isProtectedPath(%q) = %v, want %v", tt.path, got, tt.protected)
			}
		})
	}
}

func TestReadLatestSessionPromptFromCommittedTree(t *testing.T) {
	t.Parallel()

	// Checkpoint ID "a3b2c4d5e6f7" -> path "a3/b2c4d5e6f7"
	cpID := id.MustCheckpointID("a3b2c4d5e6f7")

	t.Run("single session reads from 0/prompt.txt", func(t *testing.T) {
		t.Parallel()
		tree := buildCommittedTree(t, map[string]string{
			"a3/b2c4d5e6f7/0/prompt.txt": "Implement login feature",
		})

		got := ReadLatestSessionPromptFromCommittedTree(tree, cpID, 1)
		if got != "Implement login feature" {
			t.Errorf("got %q, want %q", got, "Implement login feature")
		}
	})

	t.Run("multi session reads from latest session", func(t *testing.T) {
		t.Parallel()
		tree := buildCommittedTree(t, map[string]string{
			"a3/b2c4d5e6f7/0/prompt.txt": "First session prompt",
			"a3/b2c4d5e6f7/1/prompt.txt": "Second session prompt",
			"a3/b2c4d5e6f7/2/prompt.txt": "Third session prompt",
		})

		got := ReadLatestSessionPromptFromCommittedTree(tree, cpID, 3)
		if got != "Third session prompt" {
			t.Errorf("got %q, want %q", got, "Third session prompt")
		}
	})

	t.Run("falls back to session 0 when computed index missing", func(t *testing.T) {
		t.Parallel()
		// Tree only has session 0, but sessionCount says 3
		tree := buildCommittedTree(t, map[string]string{
			"a3/b2c4d5e6f7/0/prompt.txt": "Fallback prompt",
		})

		got := ReadLatestSessionPromptFromCommittedTree(tree, cpID, 3)
		if got != "Fallback prompt" {
			t.Errorf("got %q, want %q", got, "Fallback prompt")
		}
	})

	t.Run("returns empty for missing prompt.txt", func(t *testing.T) {
		t.Parallel()
		// Session directory exists but no prompt.txt
		tree := buildCommittedTree(t, map[string]string{
			"a3/b2c4d5e6f7/0/metadata.json": `{"session_id":"test"}`,
		})

		got := ReadLatestSessionPromptFromCommittedTree(tree, cpID, 1)
		if got != "" {
			t.Errorf("got %q, want empty string", got)
		}
	})

	t.Run("returns empty for missing checkpoint path", func(t *testing.T) {
		t.Parallel()
		// Tree has a different checkpoint ID
		tree := buildCommittedTree(t, map[string]string{
			"ff/aabbccddee/0/prompt.txt": "Wrong checkpoint",
		})

		got := ReadLatestSessionPromptFromCommittedTree(tree, cpID, 1)
		if got != "" {
			t.Errorf("got %q, want empty string", got)
		}
	})

	t.Run("returns empty for zero session count", func(t *testing.T) {
		t.Parallel()
		tree := buildCommittedTree(t, map[string]string{
			"a3/b2c4d5e6f7/0/prompt.txt": "Some prompt",
		})

		// sessionCount=0 triggers latestIndex=max(0-1,0)=0, should still read session 0
		got := ReadLatestSessionPromptFromCommittedTree(tree, cpID, 0)
		if got != "Some prompt" {
			t.Errorf("got %q, want %q", got, "Some prompt")
		}
	})

	t.Run("falls back to earlier session when latest has no prompt", func(t *testing.T) {
		t.Parallel()
		// Session 1 (latest) has no prompt.txt, session 0 does.
		// This happens when a test session gets condensed alongside a real one.
		tree := buildCommittedTree(t, map[string]string{
			"a3/b2c4d5e6f7/0/prompt.txt":    "Real session prompt",
			"a3/b2c4d5e6f7/1/metadata.json": `{"session_id":"test"}`,
		})

		got := ReadLatestSessionPromptFromCommittedTree(tree, cpID, 2)
		if got != "Real session prompt" {
			t.Errorf("got %q, want %q", got, "Real session prompt")
		}
	})

	t.Run("falls back through multiple empty sessions to find prompt", func(t *testing.T) {
		t.Parallel()
		// Sessions 2 and 1 have no prompt, session 0 does.
		tree := buildCommittedTree(t, map[string]string{
			"a3/b2c4d5e6f7/0/prompt.txt":    "Original prompt",
			"a3/b2c4d5e6f7/1/metadata.json": `{"session_id":"s1"}`,
			"a3/b2c4d5e6f7/2/metadata.json": `{"session_id":"s2"}`,
		})

		got := ReadLatestSessionPromptFromCommittedTree(tree, cpID, 3)
		if got != "Original prompt" {
			t.Errorf("got %q, want %q", got, "Original prompt")
		}
	})

	t.Run("returns empty when no session has a prompt", func(t *testing.T) {
		t.Parallel()
		tree := buildCommittedTree(t, map[string]string{
			"a3/b2c4d5e6f7/0/metadata.json": `{"session_id":"s0"}`,
			"a3/b2c4d5e6f7/1/metadata.json": `{"session_id":"s1"}`,
		})

		got := ReadLatestSessionPromptFromCommittedTree(tree, cpID, 2)
		if got != "" {
			t.Errorf("got %q, want empty string", got)
		}
	})

	t.Run("falls back when latest has empty prompt.txt", func(t *testing.T) {
		t.Parallel()
		// Latest session has a prompt.txt file but it's empty — should fall back.
		tree := buildCommittedTree(t, map[string]string{
			"a3/b2c4d5e6f7/0/prompt.txt": "Real prompt",
			"a3/b2c4d5e6f7/1/prompt.txt": "",
		})

		got := ReadLatestSessionPromptFromCommittedTree(tree, cpID, 2)
		if got != "Real prompt" {
			t.Errorf("got %q, want %q", got, "Real prompt")
		}
	})

	t.Run("extracts first prompt from multi-prompt content", func(t *testing.T) {
		t.Parallel()
		tree := buildCommittedTree(t, map[string]string{
			"a3/b2c4d5e6f7/0/prompt.txt": "First prompt\n\n---\n\nSecond prompt",
		})

		got := ReadLatestSessionPromptFromCommittedTree(tree, cpID, 1)
		if got != "First prompt" {
			t.Errorf("got %q, want %q", got, "First prompt")
		}
	})
}

func TestIsEmptyRepository(t *testing.T) {
	t.Parallel()
	t.Run("empty repo returns true", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		repo, err := git.PlainInit(dir, false)
		if err != nil {
			t.Fatalf("failed to init repo: %v", err)
		}
		if !IsEmptyRepository(repo) {
			t.Error("IsEmptyRepository() = false, want true for empty repo")
		}
	})

	t.Run("repo with commit returns false", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		repo, err := git.PlainInit(dir, false)
		if err != nil {
			t.Fatalf("failed to init repo: %v", err)
		}

		// Create a commit
		testFile := filepath.Join(dir, "test.txt")
		if err := os.WriteFile(testFile, []byte("content"), 0o644); err != nil {
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

		if IsEmptyRepository(repo) {
			t.Error("IsEmptyRepository() = true, want false for repo with commit")
		}
	})
}

// openRepoHeadTree opens the repo at dir and returns the HEAD commit tree.
func openRepoHeadTree(t *testing.T, dir string) *object.Tree {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)
	return tree
}

func TestReadAgentTypeFromTree_OnlyClaude(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, ".claude/settings.json", `{}`)
	testutil.GitAdd(t, dir, ".claude/settings.json")
	testutil.GitCommit(t, dir, "init")

	tree := openRepoHeadTree(t, dir)
	result := ReadAgentTypeFromTree(tree, "nonexistent-path")
	assert.Equal(t, agent.AgentTypeClaudeCode, result)
}

func TestReadAgentTypeFromTree_OnlyGemini(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	testutil.WriteFile(t, dir, ".gemini/settings.json", `{}`)
	testutil.GitAdd(t, dir, ".gemini/settings.json")
	testutil.GitCommit(t, dir, "init")

	tree := openRepoHeadTree(t, dir)
	result := ReadAgentTypeFromTree(tree, "nonexistent-path")
	assert.Equal(t, agent.AgentTypeGemini, result)
}

// buildCommittedTree builds a committed tree from a path→content map and
// returns the resulting *object.Tree. Paths may be nested (e.g.
// "a3/b2c4d5e6f7/0/prompt.txt").
func buildCommittedTree(t *testing.T, fileContents map[string]string) *object.Tree {
	t.Helper()

	repo, err := git.PlainInit(t.TempDir(), false)
	require.NoError(t, err)

	entries := make(map[string]object.TreeEntry, len(fileContents))
	for filePath, content := range fileContents {
		blob := repo.Storer.NewEncodedObject()
		blob.SetType(plumbing.BlobObject)
		blob.SetSize(int64(len(content)))
		writer, err := blob.Writer()
		require.NoError(t, err)
		_, err = writer.Write([]byte(content))
		require.NoError(t, err)
		require.NoError(t, writer.Close())

		blobHash, err := repo.Storer.SetEncodedObject(blob)
		require.NoError(t, err)

		entries[filePath] = object.TreeEntry{
			Name: filePath,
			Mode: filemode.Regular,
			Hash: blobHash,
		}
	}

	treeHash, err := checkpoint.BuildTreeFromEntries(context.Background(), repo, entries)
	require.NoError(t, err)

	tree, err := repo.TreeObject(treeHash)
	require.NoError(t, err)
	return tree
}
