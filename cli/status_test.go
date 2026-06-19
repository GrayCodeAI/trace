package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/session"
	"github.com/GrayCodeAI/trace/cli/testutil"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
)

func TestResolveWorktreeBranch_RegularRepo(t *testing.T) {
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	_, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}

	// Read the default branch name directly from HEAD to avoid hard-coding it
	headData, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	wantBranch := strings.TrimPrefix(strings.TrimSpace(string(headData)), "ref: refs/heads/")

	branch := resolveWorktreeBranch(context.Background(), dir)
	if branch != wantBranch {
		t.Errorf("resolveWorktreeBranch() = %q, want %q", branch, wantBranch)
	}
}

func TestResolveWorktreeBranch_DetachedHEAD(t *testing.T) {
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}

	// Create a commit so we can detach HEAD
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	testFile := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := wt.Add("test.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	hash, err := wt.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "Test",
			Email: "test@test.com",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Detach HEAD by writing the raw hash to .git/HEAD
	headPath := filepath.Join(dir, ".git", "HEAD")
	if err := os.WriteFile(headPath, []byte(hash.String()+"\n"), 0o644); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}

	branch := resolveWorktreeBranch(context.Background(), dir)
	if branch != "HEAD" {
		t.Errorf("resolveWorktreeBranch() = %q, want %q for detached HEAD", branch, "HEAD")
	}
}

func TestResolveWorktreeBranch_WorktreeGitFile(t *testing.T) {
	// Simulate a worktree where .git is a file pointing to a gitdir
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	// Create a fake gitdir with a HEAD file
	gitdir := filepath.Join(dir, "fake-gitdir")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatalf("mkdir gitdir: %v", err)
	}
	headPath := filepath.Join(gitdir, "HEAD")
	if err := os.WriteFile(headPath, []byte("ref: refs/heads/feature-branch\n"), 0o644); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}

	// Create a worktree-style .git file
	worktreeDir := filepath.Join(dir, "worktree")
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	gitFile := filepath.Join(worktreeDir, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatalf("write .git file: %v", err)
	}

	branch := resolveWorktreeBranch(context.Background(), worktreeDir)
	if branch != "feature-branch" {
		t.Errorf("resolveWorktreeBranch() = %q, want %q", branch, "feature-branch")
	}
}

func TestResolveWorktreeBranch_WorktreeRelativePath(t *testing.T) {
	// Simulate a worktree where .git file uses a relative gitdir path
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}

	// Create the main .git dir structure
	mainGitDir := filepath.Join(dir, "main-repo", ".git", "worktrees", "wt1")
	if err := os.MkdirAll(mainGitDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	headPath := filepath.Join(mainGitDir, "HEAD")
	if err := os.WriteFile(headPath, []byte("ref: refs/heads/develop\n"), 0o644); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}

	// Create worktree directory with relative .git file
	worktreeDir := filepath.Join(dir, "main-repo", "worktrees-dir", "wt1")
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	// Relative path from worktree to the gitdir
	relPath := filepath.Join("..", "..", ".git", "worktrees", "wt1")
	gitFile := filepath.Join(worktreeDir, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: "+relPath+"\n"), 0o644); err != nil {
		t.Fatalf("write .git file: %v", err)
	}

	branch := resolveWorktreeBranch(context.Background(), worktreeDir)
	if branch != "develop" {
		t.Errorf("resolveWorktreeBranch() = %q, want %q", branch, "develop")
	}
}

func TestResolveWorktreeBranch_NonExistentPath(t *testing.T) {
	t.Parallel()
	branch := resolveWorktreeBranch(context.Background(), "/nonexistent/path/that/does/not/exist")
	if branch != "" {
		t.Errorf("resolveWorktreeBranch() = %q, want empty string for non-existent path", branch)
	}
}

func TestResolveWorktreeBranch_NotARepo(t *testing.T) {
	dir := t.TempDir()
	// No .git directory or file
	branch := resolveWorktreeBranch(context.Background(), dir)
	if branch != "" {
		t.Errorf("resolveWorktreeBranch() = %q, want empty string for non-repo directory", branch)
	}
}

func TestResolveWorktreeBranch_ReftableStub(t *testing.T) {
	t.Parallel()

	// Simulate a reftable repo where .git/HEAD contains "ref: refs/heads/.invalid"
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/.invalid\n"), 0o644); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}

	branch := resolveWorktreeBranch(context.Background(), dir)
	// Should fall back to git, which will fail on this fake repo and return "HEAD"
	if branch != "HEAD" {
		t.Errorf("resolveWorktreeBranch() = %q, want %q for reftable stub", branch, "HEAD")
	}
}

func TestRunStatus_Enabled(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, false, false); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	if !strings.Contains(stdout.String(), "Enabled") {
		t.Errorf("Expected output to show 'Enabled', got: %s", stdout.String())
	}
}

func TestRunStatus_Disabled(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsDisabled)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, false, false); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	if !strings.Contains(stdout.String(), "Disabled") {
		t.Errorf("Expected output to show 'Disabled', got: %s", stdout.String())
	}
}

func TestRunStatus_NotSetUp(t *testing.T) {
	setupTestRepo(t)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, false, false); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "○ not set up") {
		t.Errorf("Expected output to show '○ not set up', got: %s", output)
	}
	if !strings.Contains(output, "trace enable") {
		t.Errorf("Expected output to mention 'trace enable', got: %s", output)
	}
}

func TestRunStatus_NotGitRepository(t *testing.T) {
	setupTestDir(t) // No git init

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, false, false); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	if !strings.Contains(stdout.String(), "✕ not a git repository") {
		t.Errorf("Expected output to show '✕ not a git repository', got: %s", stdout.String())
	}
}

func TestRunStatus_LocalSettingsOnly(t *testing.T) {
	setupTestRepo(t)
	writeLocalSettings(t, `{"enabled": true}`)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, true, false); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	output := stdout.String()
	// Should show effective status first (dot + Enabled + separator + strategy)
	if !strings.Contains(output, "Enabled") {
		t.Errorf("Expected output to show 'Enabled', got: %s", output)
	}
	// Should show per-file details
	if !strings.Contains(output, "Local") || !strings.Contains(output, "enabled") {
		t.Errorf("Expected output to show 'Local' and 'enabled', got: %s", output)
	}
	if strings.Contains(output, "Project") {
		t.Errorf("Should not show Project settings when only local exists, got: %s", output)
	}
}

func TestRunStatus_BothProjectAndLocal(t *testing.T) {
	setupTestRepo(t)
	// Project: enabled=true, strategy=manual-commit
	// Local: enabled=false, strategy=manual-commit
	// Detailed mode shows effective status first, then each file separately
	writeSettings(t, `{"enabled": true}`)
	writeLocalSettings(t, `{"enabled": false}`)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, true, false); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	output := stdout.String()
	// Should show effective status first (local overrides project)
	if !strings.Contains(output, "Disabled") || !strings.Contains(output, "manual-commit") {
		t.Errorf("Expected output to show effective 'Disabled' with 'manual-commit', got: %s", output)
	}
	// Should show both settings separately
	if !strings.Contains(output, "Project") || !strings.Contains(output, "manual-commit") {
		t.Errorf("Expected output to show Project with manual-commit, got: %s", output)
	}
	if !strings.Contains(output, "Local") || !strings.Contains(output, "disabled") {
		t.Errorf("Expected output to show Local with disabled, got: %s", output)
	}
}

func TestRunStatus_BothProjectAndLocal_Short(t *testing.T) {
	setupTestRepo(t)
	// Project: enabled=true, strategy=manual-commit
	// Local: enabled=false, strategy=manual-commit
	// Short mode shows merged/effective settings
	writeSettings(t, `{"enabled": true}`)
	writeLocalSettings(t, `{"enabled": false}`)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, false, false); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	output := stdout.String()
	// Should show merged/effective state (local overrides project)
	if !strings.Contains(output, "Disabled") || !strings.Contains(output, "manual-commit") {
		t.Errorf("Expected output to show 'Disabled' with 'manual-commit', got: %s", output)
	}
}

func TestRunStatus_ShowsManualCommitStrategy(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, `{"enabled": false}`)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, true, false); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	output := stdout.String()
	// Should show effective status first
	if !strings.Contains(output, "Disabled") || !strings.Contains(output, "manual-commit") {
		t.Errorf("Expected output to show effective 'Disabled' with 'manual-commit', got: %s", output)
	}
	// Should show per-file details
	if !strings.Contains(output, "Project") || !strings.Contains(output, "disabled") {
		t.Errorf("Expected output to show 'Project' and 'disabled', got: %s", output)
	}
}

func TestTimeAgo(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{"just now", 10 * time.Second, "just now"},
		{"30 seconds", 30 * time.Second, "just now"},
		{"1 minute", 1 * time.Minute, "1m ago"},
		{"5 minutes", 5 * time.Minute, "5m ago"},
		{"59 minutes", 59 * time.Minute, "59m ago"},
		{"1 hour", 1 * time.Hour, "1h ago"},
		{"3 hours", 3 * time.Hour, "3h ago"},
		{"23 hours", 23 * time.Hour, "23h ago"},
		{"1 day", 24 * time.Hour, "1d ago"},
		{"7 days", 7 * 24 * time.Hour, "7d ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := timeAgo(time.Now().Add(-tt.duration))
			if got != tt.want {
				t.Errorf("timeAgo(%v ago) = %q, want %q", tt.duration, got, tt.want)
			}
		})
	}
}

func TestWriteActiveSessions(t *testing.T) {
	setupTestRepo(t)

	// Create a state store with test data
	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatalf("NewStateStore() error = %v", err)
	}

	now := time.Now()
	recentInteraction := now.Add(-5 * time.Minute)

	// Create active sessions with token usage
	states := []*session.State{
		{
			SessionID:           "abc-1234-session",
			WorktreePath:        "/Users/test/repo",
			StartedAt:           now.Add(-2 * time.Hour),
			LastInteractionTime: &recentInteraction,
			LastPrompt:          "Fix auth bug in login flow",
			AgentType:           types.AgentType("Claude Code"),
			TokenUsage: &agent.TokenUsage{
				InputTokens:  800,
				OutputTokens: 400,
			},
		},
		{
			SessionID:    "def-5678-session",
			WorktreePath: "/Users/test/repo",
			StartedAt:    now.Add(-15 * time.Minute),
			LastPrompt:   "Add dark mode support for the trace application and all components",
			AgentType:    agent.AgentTypeCursor,
			TokenUsage: &agent.TokenUsage{
				InputTokens:  500,
				OutputTokens: 300,
			},
		},
		{
			SessionID:    "ghi-9012-session",
			WorktreePath: "/Users/test/repo/.worktrees/3",
			StartedAt:    now.Add(-5 * time.Minute),
		},
	}

	for _, s := range states {
		if err := store.Save(context.Background(), s); err != nil {
			t.Fatalf("Save() error = %v", err)
		}
	}

	var buf bytes.Buffer
	sty := newStatusStyles(&buf)
	writeActiveSessions(context.Background(), &buf, sty)

	output := buf.String()

	// Should contain "Active Sessions" in section header
	if !strings.Contains(output, "Active Sessions") {
		t.Errorf("Expected 'Active Sessions' header, got: %s", output)
	}

	// Should contain agent labels (without brackets in new format)
	if !strings.Contains(output, "Claude Code") {
		t.Errorf("Expected agent label 'Claude Code', got: %s", output)
	}
	if !strings.Contains(output, "Cursor") {
		t.Errorf("Expected agent label 'Cursor', got: %s", output)
	}
	// Session without AgentType should show unknown placeholder
	if !strings.Contains(output, unknownPlaceholder) {
		t.Errorf("Expected '%s' for missing agent type, got: %s", unknownPlaceholder, output)
	}

	// Should contain full session IDs
	if !strings.Contains(output, "abc-1234-session") {
		t.Errorf("Expected full session ID 'abc-1234-session', got: %s", output)
	}
	if !strings.Contains(output, "def-5678-session") {
		t.Errorf("Expected full session ID 'def-5678-session', got: %s", output)
	}
	if !strings.Contains(output, "ghi-9012-session") {
		t.Errorf("Expected full session ID 'ghi-9012-session', got: %s", output)
	}

	// Should contain first prompts with chevron
	if !strings.Contains(output, "> \"Fix auth bug in login flow\"") {
		t.Errorf("Expected first prompt with chevron, got: %s", output)
	}

	// Session without LastPrompt should NOT show a prompt line
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "ghi-9012-session") {
			if strings.Contains(line, "\"") {
				t.Errorf("Session without prompt should not show quoted text on first line, got: %s", line)
			}
		}
	}

	// Should show "active 5m ago" for session with LastInteractionTime that differs from StartedAt
	if !strings.Contains(output, "active 5m ago") {
		t.Errorf("Expected 'active 5m ago' for session with LastInteractionTime, got: %s", output)
	}

	// Session started 15m ago with no LastInteractionTime should NOT show "active" in stats
	for _, line := range lines {
		if strings.Contains(line, "Cursor") {
			if strings.Contains(line, "active") {
				t.Errorf("Session without LastInteractionTime should not show 'active', got: %s", line)
			}
		}
	}

	// Should contain per-session token counts
	if !strings.Contains(output, "tokens 1.2k") {
		t.Errorf("Expected per-session 'tokens 1.2k' for first session (800+400), got: %s", output)
	}

	// Should contain aggregate footer with session count (no total tokens in footer)
	if !strings.Contains(output, "3 sessions") {
		t.Errorf("Expected aggregate '3 sessions' in footer, got: %s", output)
	}

	// Should NOT contain phase indicators (removed)
	if strings.Contains(output, "● active") || strings.Contains(output, "● idle") || strings.Contains(output, "● ended") {
		t.Errorf("Output should not contain phase indicators, got: %s", output)
	}

	// Should NOT contain file counts (removed)
	if strings.Contains(output, "files ") {
		t.Errorf("Output should not contain file counts, got: %s", output)
	}
}

func TestWriteActiveSessions_ActiveTimeOmittedWhenClose(t *testing.T) {
	setupTestRepo(t)

	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatalf("NewStateStore() error = %v", err)
	}

	now := time.Now()
	// LastInteractionTime is only 30 seconds after StartedAt — should be omitted
	startedAt := now.Add(-10 * time.Minute)
	lastInteraction := startedAt.Add(30 * time.Second)

	state := &session.State{
		SessionID:           "close-time-session",
		WorktreePath:        "/Users/test/repo",
		StartedAt:           startedAt,
		LastInteractionTime: &lastInteraction,
		LastPrompt:          "test prompt",
		AgentType:           types.AgentType("Claude Code"),
	}

	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	var buf bytes.Buffer
	sty := newStatusStyles(&buf)
	writeActiveSessions(context.Background(), &buf, sty)

	output := buf.String()
	// Should not show "active Xm ago" when LastInteractionTime is close to StartedAt
	// But "active" may appear in phase indicator, so check for the specific pattern
	if strings.Contains(output, "active 10m ago") || strings.Contains(output, "active 9m ago") {
		t.Errorf("Expected no separate 'active' time when LastInteractionTime is close to StartedAt, got: %s", output)
	}
}

func TestWriteActiveSessions_NoSessions(t *testing.T) {
	setupTestRepo(t)

	var buf bytes.Buffer
	sty := newStatusStyles(&buf)
	writeActiveSessions(context.Background(), &buf, sty)

	// Should produce no output when there are no sessions
	if buf.Len() != 0 {
		t.Errorf("Expected empty output with no sessions, got: %s", buf.String())
	}
}

func TestWriteActiveSessions_EndedSessionsExcluded(t *testing.T) {
	setupTestRepo(t)

	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatalf("NewStateStore() error = %v", err)
	}

	endedAt := time.Now()
	state := &session.State{
		SessionID:    "ended-session",
		WorktreePath: "/Users/test/repo",
		StartedAt:    time.Now().Add(-10 * time.Minute),
		EndedAt:      &endedAt,
	}

	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	var buf bytes.Buffer
	sty := newStatusStyles(&buf)
	writeActiveSessions(context.Background(), &buf, sty)

	// Should produce no output when all sessions are ended
	if buf.Len() != 0 {
		t.Errorf("Expected empty output with only ended sessions, got: %s", buf.String())
	}
}

func TestWriteActiveSessions_ShowsDivergenceWarningWhenBaseCommitStale(t *testing.T) {
	setupTestRepo(t)

	repoDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}

	// Create two commits: the session tracks the first, HEAD is the second
	testutil.WriteFile(t, repoDir, "tracked.txt", "base")
	testutil.GitAdd(t, repoDir, "tracked.txt")
	testutil.GitCommit(t, repoDir, "base commit")
	baseCommit := testutil.GetHeadHash(t, repoDir)

	testutil.WriteFile(t, repoDir, "tracked.txt", "base\nnew")
	testutil.GitAdd(t, repoDir, "tracked.txt")
	testutil.GitCommit(t, repoDir, "second commit")

	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatalf("NewStateStore() error = %v", err)
	}

	now := time.Now()
	state := &session.State{
		SessionID:             "stale-base-session",
		WorktreePath:          repoDir,
		StartedAt:             now.Add(-10 * time.Minute),
		BaseCommit:            baseCommit,
		AttributionBaseCommit: baseCommit,
	}
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	var buf bytes.Buffer
	sty := newStatusStyles(&buf)
	writeActiveSessions(context.Background(), &buf, sty)

	output := buf.String()
	if !strings.Contains(output, "tracking diverged from current HEAD") {
		t.Fatalf("expected divergence warning when BaseCommit != HEAD, got: %s", output)
	}

	// Verify session state was NOT mutated (read-only)
	reloaded, err := store.Load(context.Background(), state.SessionID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if reloaded.BaseCommit != baseCommit {
		t.Fatalf("BaseCommit was mutated: got %q, want %q", reloaded.BaseCommit, baseCommit)
	}
	if reloaded.AttributionBaseCommit != baseCommit {
		t.Fatalf("AttributionBaseCommit was mutated: got %q, want %q", reloaded.AttributionBaseCommit, baseCommit)
	}
}

func TestWriteActiveSessions_NoWarningWhenReconciled(t *testing.T) {
	setupTestRepo(t)

	repoDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}

	testutil.WriteFile(t, repoDir, "tracked.txt", "content")
	testutil.GitAdd(t, repoDir, "tracked.txt")
	testutil.GitCommit(t, repoDir, "initial commit")
	headCommit := testutil.GetHeadHash(t, repoDir)

	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatalf("NewStateStore() error = %v", err)
	}

	now := time.Now()
	state := &session.State{
		SessionID:             "reconciled-session",
		WorktreePath:          repoDir,
		StartedAt:             now.Add(-10 * time.Minute),
		BaseCommit:            headCommit,
		AttributionBaseCommit: headCommit,
	}
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	var buf bytes.Buffer
	sty := newStatusStyles(&buf)
	writeActiveSessions(context.Background(), &buf, sty)

	output := buf.String()
	if strings.Contains(output, "diverged") || strings.Contains(output, "attribution") {
		t.Fatalf("expected no divergence or attribution warning when BaseCommit == HEAD and AttributionBaseCommit == BaseCommit, got: %s", output)
	}
}

func TestWriteActiveSessions_ShowsSoftWarningWhenAttributionDiverged(t *testing.T) {
	setupTestRepo(t)

	repoDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}

	testutil.WriteFile(t, repoDir, "tracked.txt", "content")
	testutil.GitAdd(t, repoDir, "tracked.txt")
	testutil.GitCommit(t, repoDir, "initial commit")
	headCommit := testutil.GetHeadHash(t, repoDir)

	// Simulate: hooks already reconciled BaseCommit to HEAD, but
	// AttributionBaseCommit is still pointing at the old commit (stale).
	oldBaseCommit := strings.Repeat("a", 40)

	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatalf("NewStateStore() error = %v", err)
	}

	now := time.Now()
	state := &session.State{
		SessionID:             "attribution-diverged-session",
		WorktreePath:          repoDir,
		StartedAt:             now.Add(-10 * time.Minute),
		BaseCommit:            headCommit,
		AttributionBaseCommit: oldBaseCommit,
	}
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	var buf bytes.Buffer
	sty := newStatusStyles(&buf)
	writeActiveSessions(context.Background(), &buf, sty)

	output := buf.String()
	if !strings.Contains(output, "attribution") {
		t.Fatalf("expected attribution warning when AttributionBaseCommit != BaseCommit, got: %s", output)
	}

	// Verify session state was NOT mutated (read-only)
	reloaded, err := store.Load(context.Background(), state.SessionID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if reloaded.BaseCommit != headCommit {
		t.Fatalf("BaseCommit was mutated: got %q, want %q", reloaded.BaseCommit, headCommit)
	}
	if reloaded.AttributionBaseCommit != oldBaseCommit {
		t.Fatalf("AttributionBaseCommit was mutated: got %q, want %q", reloaded.AttributionBaseCommit, oldBaseCommit)
	}
}

// TestComputeSessionDivergenceWarnings_EmptyBaseCommit_EmitsLinkageWarning verifies
// that a partially-initialized session (BaseCommit == "") produces an explicit
// "linkage incomplete" warning rather than silently disappearing from status.
// Silently skipping such sessions was flagged as an observability regression:
// operators lose the clearest signal that the session cannot be migrated or
// attributed until reinitialization.
func TestComputeSessionDivergenceWarnings_EmptyBaseCommit_EmitsLinkageWarning(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	head := headLinkage{commitHash: strings.Repeat("e", 40)}

	active := []*session.State{
		{
			SessionID:             "partially-initialized",
			WorktreePath:          repoRoot,
			BaseCommit:            "",
			AttributionBaseCommit: strings.Repeat("a", 40),
		},
	}

	warnings := computeSessionDivergenceWarnings(repoRoot, active, head)

	msg, ok := warnings["partially-initialized"]
	if !ok {
		t.Fatal("expected a linkage-incomplete warning for session with empty BaseCommit, got none")
	}
	if !strings.Contains(msg, "linkage incomplete") {
		t.Fatalf("expected warning to mention linkage incomplete, got %q", msg)
	}
	// Must NOT be the attribution-divergence message — that would be misleading
	// since the session isn't diverged; it's un-initialized.
	if strings.Contains(msg, "attribution base diverged") {
		t.Fatalf("empty-BaseCommit session should not produce attribution-divergence wording, got %q", msg)
	}
}

func TestFormatTokenCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input int
		want  string
	}{
		{0, "0"},
		{500, "500"},
		{999, "999"},
		{1000, "1k"},
		{1200, "1.2k"},
		{4800, "4.8k"},
		{14300, "14.3k"},
		{100000, "100k"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			got := formatTokenCount(tt.input)
			if got != tt.want {
				t.Errorf("formatTokenCount(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
