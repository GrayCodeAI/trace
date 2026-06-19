package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"
)

func TestListCommittedForExplain_V2Disabled_ReturnsV1Only(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte("x"), 0o644))
	_, err = wt.Add("f.txt")
	require.NoError(t, err)
	_, err = wt.Commit("init", &git.CommitOptions{
		Author: &object.Signature{Name: "T", Email: "t@t.com", When: time.Now()},
	})
	require.NoError(t, err)

	v1Store := checkpoint.NewGitStore(repo)
	v2Store := checkpoint.NewV2GitStore(repo, "origin")
	ctx := context.Background()

	transcript := []byte(`{"type":"user","message":{"content":[{"type":"text","text":"hello"}]}}` + "\n")

	v1ID := id.MustCheckpointID("ccc777888999")
	require.NoError(t, v1Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID: v1ID,
		SessionID:    "session-v1",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted(transcript),
		AuthorName:   "T",
		AuthorEmail:  "t@t.com",
	}))

	// v2 also has a checkpoint, but v2 is disabled — should only see v1.
	v2ID := id.MustCheckpointID("ddd000111222")
	require.NoError(t, v2Store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID: v2ID,
		SessionID:    "session-v2",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted(transcript),
		AuthorName:   "T",
		AuthorEmail:  "t@t.com",
	}))

	results, err := listCommittedForExplain(ctx, v1Store, v2Store, false)
	require.NoError(t, err)

	foundIDs := make(map[id.CheckpointID]bool)
	for _, r := range results {
		foundIDs[r.CheckpointID] = true
	}
	require.True(t, foundIDs[v1ID], "v1 checkpoint should be returned")
	require.False(t, foundIDs[v2ID], "v2-only checkpoint should NOT appear when v2 is disabled")
}

func TestFormatCheckpointOutput_Short(t *testing.T) {
	summary := &checkpoint.CheckpointSummary{
		CheckpointID:     id.MustCheckpointID("abc123def456"),
		CheckpointsCount: 3,
		FilesTouched:     []string{"main.go", "util.go"},
		TokenUsage: &agent.TokenUsage{
			InputTokens:  10000,
			OutputTokens: 5000,
		},
	}
	content := &checkpoint.SessionContent{
		Metadata: checkpoint.CommittedMetadata{
			CheckpointID:     "abc123def456",
			SessionID:        "2026-01-21-test-session",
			CreatedAt:        time.Date(2026, 1, 21, 10, 30, 0, 0, time.UTC),
			FilesTouched:     []string{"main.go", "util.go"},
			CheckpointsCount: 3,
			TokenUsage: &agent.TokenUsage{
				InputTokens:  10000,
				OutputTokens: 5000,
			},
		},
		Prompts: "Add a new feature",
	}

	// Default mode: empty commit message (not shown anyway in default mode)
	output := formatCheckpointOutput(summary, content, id.MustCheckpointID("abc123def456"), nil, checkpoint.Author{}, false, false, &bytes.Buffer{})

	// Should show checkpoint ID
	if !strings.Contains(output, "abc123def456") {
		t.Error("expected checkpoint ID in output")
	}
	// Should show session ID
	if !strings.Contains(output, "2026-01-21-test-session") {
		t.Error("expected session ID in output")
	}
	// Should show timestamp
	if !strings.Contains(output, "2026-01-21") {
		t.Error("expected timestamp in output")
	}
	// Should show token usage (10000 + 5000 = 15000), formatted compactly.
	if !strings.Contains(output, "  tokens   15k") {
		t.Error("expected token count in output")
	}
	// Should show Intent heading (markdown body)
	if !strings.Contains(output, "## Intent") {
		t.Errorf("expected '## Intent' heading in no-color output, got:\n%s", output)
	}
	// Should show Summary heading with --generate hint affordance
	if !strings.Contains(output, "## Summary") {
		t.Errorf("expected '## Summary' heading in no-color output, got:\n%s", output)
	}
	if !strings.Contains(output, "trace explain --generate") {
		t.Errorf("expected --generate hint in summary affordance, got:\n%s", output)
	}
	// Should NOT show full file list in default mode
	if strings.Contains(output, "main.go") {
		t.Error("default output should not show file list (use --full)")
	}
}

func TestFormatCheckpointOutput_Verbose(t *testing.T) {
	// Transcript with user prompts that match what we expect to see
	transcriptContent := []byte(`{"type":"user","uuid":"u1","message":{"content":"Add a new feature"}}
{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"I'll add the feature"}]}}
{"type":"user","uuid":"u2","message":{"content":"Fix the bug"}}
{"type":"assistant","uuid":"a2","message":{"content":[{"type":"text","text":"Fixed it"}]}}
{"type":"user","uuid":"u3","message":{"content":"Refactor the code"}}
`)

	summary := &checkpoint.CheckpointSummary{
		CheckpointID:     id.MustCheckpointID("abc123def456"),
		CheckpointsCount: 3,
		FilesTouched:     []string{"main.go", "util.go", "config.yaml"},
		TokenUsage: &agent.TokenUsage{
			InputTokens:  10000,
			OutputTokens: 5000,
		},
	}
	content := &checkpoint.SessionContent{
		Metadata: checkpoint.CommittedMetadata{
			CheckpointID:              "abc123def456",
			SessionID:                 "2026-01-21-test-session",
			CreatedAt:                 time.Date(2026, 1, 21, 10, 30, 0, 0, time.UTC),
			FilesTouched:              []string{"main.go", "util.go", "config.yaml"},
			CheckpointsCount:          3,
			CheckpointTranscriptStart: 0, // All content is this checkpoint's
			TokenUsage: &agent.TokenUsage{
				InputTokens:  10000,
				OutputTokens: 5000,
			},
		},
		Prompts:    "Add a new feature\nFix the bug\nRefactor the code",
		Transcript: transcriptContent,
	}

	output := formatCheckpointOutput(summary, content, id.MustCheckpointID("abc123def456"), nil, checkpoint.Author{}, true, false, &bytes.Buffer{})

	// Should show checkpoint ID (like default)
	if !strings.Contains(output, "abc123def456") {
		t.Error("expected checkpoint ID in output")
	}
	// Should show session ID (like default)
	if !strings.Contains(output, "2026-01-21-test-session") {
		t.Error("expected session ID in output")
	}
	// Verbose should show files (with backticks in markdown list items)
	if !strings.Contains(output, "`main.go`") {
		t.Error("verbose output should show files")
	}
	if !strings.Contains(output, "`util.go`") {
		t.Error("verbose output should show all files")
	}
	if !strings.Contains(output, "`config.yaml`") {
		t.Error("verbose output should show all files")
	}
	// Should show "## Files (N)" markdown heading
	if !strings.Contains(output, "## Files (3)") {
		t.Errorf("verbose output should have '## Files (3)' heading, got:\n%s", output)
	}
	// Verbose should show scoped transcript section
	if !strings.Contains(output, "Transcript (checkpoint scope)") {
		t.Error("verbose output should have Transcript (checkpoint scope) section")
	}
	if !strings.Contains(output, "Add a new feature") {
		t.Error("verbose output should show prompts")
	}
}

func TestFormatCheckpointOutput_Verbose_NoCommitMessage(t *testing.T) {
	summary := &checkpoint.CheckpointSummary{
		CheckpointID:     id.MustCheckpointID("abc123def456"),
		CheckpointsCount: 1,
		FilesTouched:     []string{"main.go"},
	}
	content := &checkpoint.SessionContent{
		Metadata: checkpoint.CommittedMetadata{
			CheckpointID:     "abc123def456",
			SessionID:        "2026-01-21-test-session",
			CreatedAt:        time.Date(2026, 1, 21, 10, 30, 0, 0, time.UTC),
			FilesTouched:     []string{"main.go"},
			CheckpointsCount: 1,
		},
		Prompts: "Add a feature",
	}

	// When commit message is empty, should not show Commit section
	output := formatCheckpointOutput(summary, content, id.MustCheckpointID("abc123def456"), nil, checkpoint.Author{}, true, false, &bytes.Buffer{})

	if strings.Contains(output, "  commits") {
		t.Error("verbose output should not show Commits section when nil (not searched)")
	}
}

func TestFormatCheckpointOutput_Full(t *testing.T) {
	// Use proper transcript format that matches actual Claude transcripts
	transcriptData := `{"type":"user","message":{"content":"Add a new feature"}}
{"type":"assistant","message":{"content":[{"type":"text","text":"I'll add that feature for you."}]}}`

	summary := &checkpoint.CheckpointSummary{
		CheckpointID:     id.MustCheckpointID("abc123def456"),
		CheckpointsCount: 3,
		FilesTouched:     []string{"main.go", "util.go"},
		TokenUsage: &agent.TokenUsage{
			InputTokens:  10000,
			OutputTokens: 5000,
		},
	}
	content := &checkpoint.SessionContent{
		Metadata: checkpoint.CommittedMetadata{
			CheckpointID:     "abc123def456",
			SessionID:        "2026-01-21-test-session",
			CreatedAt:        time.Date(2026, 1, 21, 10, 30, 0, 0, time.UTC),
			FilesTouched:     []string{"main.go", "util.go"},
			CheckpointsCount: 3,
			TokenUsage: &agent.TokenUsage{
				InputTokens:  10000,
				OutputTokens: 5000,
			},
		},
		Prompts:    "Add a new feature",
		Transcript: []byte(transcriptData),
	}

	output := formatCheckpointOutput(summary, content, id.MustCheckpointID("abc123def456"), nil, checkpoint.Author{}, false, true, &bytes.Buffer{})

	// Should show checkpoint ID (like default)
	if !strings.Contains(output, "abc123def456") {
		t.Error("expected checkpoint ID in output")
	}
	// Full should also include verbose sections (## Files heading)
	if !strings.Contains(output, "## Files (2)") {
		t.Errorf("full output should include '## Files (2)' heading, got:\n%s", output)
	}
	// Full shows full session transcript (not scoped)
	if !strings.Contains(output, "Transcript (full session)") {
		t.Error("full output should have Transcript (full session) section")
	}
	// Should contain actual transcript content (parsed format)
	if !strings.Contains(output, "Add a new feature") {
		t.Error("full output should show transcript content")
	}
	if !strings.Contains(output, "[Assistant]") {
		t.Error("full output should show assistant messages in parsed transcript")
	}
}

func TestFormatCheckpointOutput_WithSummary(t *testing.T) {
	cpID := id.MustCheckpointID("abc123456789")
	summary := &checkpoint.CheckpointSummary{
		CheckpointID: cpID,
		FilesTouched: []string{"file1.go", "file2.go"},
	}
	content := &checkpoint.SessionContent{
		Metadata: checkpoint.CommittedMetadata{
			CheckpointID: cpID,
			SessionID:    "2026-01-22-test-session",
			CreatedAt:    time.Date(2026, 1, 22, 10, 30, 0, 0, time.UTC),
			FilesTouched: []string{"file1.go", "file2.go"},
			Summary: &checkpoint.Summary{
				Intent:  "Implement user authentication",
				Outcome: "Added login and logout functionality",
				Learnings: checkpoint.LearningsSummary{
					Repo:     []string{"Uses JWT for auth tokens"},
					Code:     []checkpoint.CodeLearning{{Path: "auth.go", Line: 42, Finding: "Token validation happens here"}},
					Workflow: []string{"Always run tests after auth changes"},
				},
				Friction:  []string{"Had to refactor session handling"},
				OpenItems: []string{"Add password reset flow"},
			},
		},
		Prompts: "Add user authentication",
	}

	// Test default output (non-verbose) with summary
	output := formatCheckpointOutput(summary, content, cpID, nil, checkpoint.Author{}, false, false, &bytes.Buffer{})

	// Should show AI-generated intent and outcome as markdown.
	if !strings.Contains(output, "## Intent\n\nImplement user authentication") {
		t.Errorf("expected AI intent in output, got:\n%s", output)
	}
	if !strings.Contains(output, "## Outcome\n\nAdded login and logout functionality") {
		t.Errorf("expected AI outcome in output, got:\n%s", output)
	}
	// Summary markdown includes all generated summary sections.
	if !strings.Contains(output, "## Learnings") {
		t.Errorf("summary output should show learnings, got:\n%s", output)
	}

	// Test verbose output with summary
	verboseOutput := formatCheckpointOutput(summary, content, cpID, nil, checkpoint.Author{}, true, false, &bytes.Buffer{})

	// Verbose should show learnings sections
	if !strings.Contains(verboseOutput, "## Learnings") {
		t.Errorf("verbose output should show learnings, got:\n%s", verboseOutput)
	}
	if !strings.Contains(verboseOutput, "### Repository") {
		t.Errorf("verbose output should show repository learnings, got:\n%s", verboseOutput)
	}
	if !strings.Contains(verboseOutput, "Uses JWT for auth tokens") {
		t.Errorf("verbose output should show repo learning content, got:\n%s", verboseOutput)
	}
	if !strings.Contains(verboseOutput, "### Code") {
		t.Errorf("verbose output should show code learnings, got:\n%s", verboseOutput)
	}
	if !strings.Contains(verboseOutput, "`auth.go:42`") {
		t.Errorf("verbose output should show code learning with line number, got:\n%s", verboseOutput)
	}
	if !strings.Contains(verboseOutput, "### Workflow") {
		t.Errorf("verbose output should show workflow learnings, got:\n%s", verboseOutput)
	}
	if !strings.Contains(verboseOutput, "## Friction") {
		t.Errorf("verbose output should show friction, got:\n%s", verboseOutput)
	}
	if !strings.Contains(verboseOutput, "## Open Items") {
		t.Errorf("verbose output should show open items, got:\n%s", verboseOutput)
	}
}

func TestFormatCheckpointOutput_SummaryStartsAfterTightHeaderRule(t *testing.T) {
	t.Parallel()

	cpID := id.MustCheckpointID("abc123456789")
	summary := &checkpoint.CheckpointSummary{CheckpointID: cpID}
	content := &checkpoint.SessionContent{
		Metadata: checkpoint.CommittedMetadata{
			CheckpointID: cpID,
			SessionID:    "2026-01-22-test-session",
			CreatedAt:    time.Date(2026, 1, 22, 10, 30, 0, 0, time.UTC),
			Summary: &checkpoint.Summary{
				Intent:  "Implement user authentication",
				Outcome: "Added login and logout functionality",
			},
		},
	}

	output := formatCheckpointOutput(summary, content, cpID, nil, checkpoint.Author{}, false, false, &bytes.Buffer{})
	rule := strings.Repeat("─", 60)
	want := "  created  2026-01-22 10:30:00\n" + rule + "\n## Intent"

	if !strings.Contains(output, want) {
		t.Fatalf("expected summary to start immediately after header rule, got:\n%s", output)
	}
}

func TestBuildSummaryMarkdown_FullSummary(t *testing.T) {
	t.Parallel()

	summary := &checkpoint.Summary{
		Intent:  "Rotate session tokens on logout",
		Outcome: "Logout now mints a new token",
		Learnings: checkpoint.LearningsSummary{
			Repo: []string{"Auth lives behind the auth_v2 gate"},
			Code: []checkpoint.CodeLearning{
				{Path: "auth/session.go", Line: 42, Finding: "Rotate before cookie clear"},
			},
			Workflow: []string{"Manual curl confirmed the path"},
		},
		Friction:  []string{"go-git v5 reset deleted .trace"},
		OpenItems: []string{"Backfill rotation for legacy cookies"},
	}

	got := buildSummaryMarkdown(summary)

	want := "## Intent\n\n" +
		"Rotate session tokens on logout\n\n" +
		"## Outcome\n\n" +
		"Logout now mints a new token\n\n" +
		"## Learnings\n\n" +
		"### Repository\n\n" +
		"- Auth lives behind the auth_v2 gate\n\n" +
		"### Code\n\n" +
		"- `auth/session.go:42` — Rotate before cookie clear\n\n" +
		"### Workflow\n\n" +
		"- Manual curl confirmed the path\n\n" +
		"## Friction\n\n" +
		"- go-git v5 reset deleted .trace\n\n" +
		"## Open Items\n\n" +
		"- Backfill rotation for legacy cookies\n"

	if got != want {
		t.Errorf("buildSummaryMarkdown mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestBuildSummaryMarkdown_NoLearnings(t *testing.T) {
	t.Parallel()

	summary := &checkpoint.Summary{
		Intent:  "Trivial fix",
		Outcome: "Fixed",
	}

	got := buildSummaryMarkdown(summary)

	if strings.Contains(got, "## Learnings") {
		t.Errorf("expected no Learnings heading when all subsections empty, got:\n%s", got)
	}
	if !strings.Contains(got, "## Intent\n\nTrivial fix\n\n") {
		t.Errorf("expected Intent block, got:\n%s", got)
	}
	if !strings.Contains(got, "## Outcome\n\nFixed\n") {
		t.Errorf("expected Outcome block, got:\n%s", got)
	}
}

func TestBuildSummaryMarkdown_PartialLearnings(t *testing.T) {
	t.Parallel()

	summary := &checkpoint.Summary{
		Intent:  "i",
		Outcome: "o",
		Learnings: checkpoint.LearningsSummary{
			Code: []checkpoint.CodeLearning{
				{Path: "a.go", Finding: "x"},
			},
		},
	}

	got := buildSummaryMarkdown(summary)

	if !strings.Contains(got, "## Learnings") {
		t.Errorf("expected Learnings heading when Code populated, got:\n%s", got)
	}
	if !strings.Contains(got, "### Code") {
		t.Errorf("expected Code subsection, got:\n%s", got)
	}
	if strings.Contains(got, "### Repository") {
		t.Errorf("did not expect Repository subsection, got:\n%s", got)
	}
	if strings.Contains(got, "### Workflow") {
		t.Errorf("did not expect Workflow subsection, got:\n%s", got)
	}
}

func TestBuildSummaryMarkdown_CodeLineVariants(t *testing.T) {
	t.Parallel()

	summary := &checkpoint.Summary{
		Intent:  "i",
		Outcome: "o",
		Learnings: checkpoint.LearningsSummary{
			Code: []checkpoint.CodeLearning{
				{Path: "a.go", Line: 10, EndLine: 20, Finding: "range"},
				{Path: "b.go", Line: 5, Finding: "single"},
				{Path: "c.go", Finding: "no-line"},
			},
		},
	}

	got := buildSummaryMarkdown(summary)

	wantLines := []string{
		"- `a.go:10-20` — range",
		"- `b.go:5` — single",
		"- `c.go` — no-line",
	}
	for _, line := range wantLines {
		if !strings.Contains(got, line) {
			t.Errorf("expected line %q in output, got:\n%s", line, got)
		}
	}
}

func TestBuildSummaryMarkdown_EmptyFrictionAndOpenItems(t *testing.T) {
	t.Parallel()

	summary := &checkpoint.Summary{
		Intent:  "i",
		Outcome: "o",
	}

	got := buildSummaryMarkdown(summary)

	if strings.Contains(got, "## Friction") {
		t.Errorf("did not expect Friction heading, got:\n%s", got)
	}
	if strings.Contains(got, "## Open Items") {
		t.Errorf("did not expect Open Items heading, got:\n%s", got)
	}
}

func TestBuildSummaryMarkdown_BacktickEscape(t *testing.T) {
	t.Parallel()

	summary := &checkpoint.Summary{
		Intent:  "Use the `foo` command",
		Outcome: "Wrapped in `bar`",
	}

	got := buildSummaryMarkdown(summary)

	if strings.Contains(got, "`foo`") {
		t.Errorf("expected backticks to be neutralized in Intent, got:\n%s", got)
	}
	if strings.Contains(got, "`bar`") {
		t.Errorf("expected backticks to be neutralized in Outcome, got:\n%s", got)
	}
	if !strings.Contains(got, "Use the ‘foo‘ command") {
		t.Errorf("expected U+2018 substitution in Intent, got:\n%s", got)
	}
}

func TestBuildSummaryMarkdown_NilSummary(t *testing.T) {
	t.Parallel()

	if got := buildSummaryMarkdown(nil); got != "" {
		t.Errorf("expected empty string for nil summary, got %q", got)
	}
}

func TestBuildFilesMarkdown_RendersPathsAsInlineCode(t *testing.T) {
	t.Parallel()

	got := buildFilesMarkdown([]string{
		"normal.go",
		"- tricky [path].go",
		"dir/`quoted`.go",
	})

	wantLines := []string{
		"- `normal.go`",
		"- `- tricky [path].go`",
		"- `dir/‘quoted‘.go`",
	}
	for _, line := range wantLines {
		if !strings.Contains(got, line) {
			t.Errorf("expected escaped file line %q in output, got:\n%s", line, got)
		}
	}
}

func TestFormatCheckpointHeader_FullMetadataPlain(t *testing.T) {
	t.Parallel()

	cpID := id.MustCheckpointID("a3b2c4d5e6f7")
	summary := &checkpoint.CheckpointSummary{
		TokenUsage: &agent.TokenUsage{InputTokens: 18432},
	}
	meta := checkpoint.CommittedMetadata{
		SessionID: "2026-04-29-7f3c1a",
		CreatedAt: time.Date(2026, 4, 29, 14, 22, 8, 0, time.UTC),
	}
	commits := []associatedCommit{{
		ShortSHA: "9f2c11a",
		Message:  "feat(auth): rotate session tokens on logout",
		Date:     time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC),
	}}
	author := checkpoint.Author{Name: "Peyton Montei", Email: "peyton@trace.io"}
	styles := statusStyles{colorEnabled: false, width: 60}

	got := formatCheckpointHeader(summary, meta, cpID, commits, author, styles)

	wantLines := []string{
		"● Checkpoint a3b2c4d5e6f7",
		"  session  2026-04-29-7f3c1a",
		"  created  2026-04-29 14:22:08",
		"  author   Peyton Montei <peyton@trace.io>",
		"  tokens   18.4k",
		"  commits  9f2c11a feat(auth): rotate session tokens on logout",
	}
	for _, line := range wantLines {
		if !strings.Contains(got, line) {
			t.Errorf("expected line %q in header, got:\n%s", line, got)
		}
	}
}

func TestFormatCheckpointHeader_NoAuthor(t *testing.T) {
	t.Parallel()

	cpID := id.MustCheckpointID("a3b2c4d5e6f7")
	meta := checkpoint.CommittedMetadata{
		SessionID: "s",
		CreatedAt: time.Date(2026, 4, 29, 14, 22, 8, 0, time.UTC),
	}
	styles := statusStyles{colorEnabled: false, width: 60}

	got := formatCheckpointHeader(nil, meta, cpID, nil, checkpoint.Author{}, styles)

	if strings.Contains(got, "  author") {
		t.Errorf("did not expect author row when Name empty, got:\n%s", got)
	}
}

func TestFormatCheckpointHeader_NoCommits(t *testing.T) {
	t.Parallel()

	cpID := id.MustCheckpointID("a3b2c4d5e6f7")
	meta := checkpoint.CommittedMetadata{
		SessionID: "s",
		CreatedAt: time.Date(2026, 4, 29, 14, 22, 8, 0, time.UTC),
	}
	styles := statusStyles{colorEnabled: false, width: 60}

	got := formatCheckpointHeader(nil, meta, cpID, nil, checkpoint.Author{}, styles)

	if strings.Contains(got, "  commits") {
		t.Errorf("did not expect commits row when commits is nil, got:\n%s", got)
	}
}

func TestFormatCheckpointHeader_MultipleCommits(t *testing.T) {
	t.Parallel()

	cpID := id.MustCheckpointID("a3b2c4d5e6f7")
	meta := checkpoint.CommittedMetadata{
		SessionID: "s",
		CreatedAt: time.Date(2026, 4, 29, 14, 22, 8, 0, time.UTC),
	}
	commits := []associatedCommit{
		{ShortSHA: "aaa1111", Message: "first", Date: time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)},
		{ShortSHA: "bbb2222", Message: "second", Date: time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)},
	}
	styles := statusStyles{colorEnabled: false, width: 60}

	got := formatCheckpointHeader(nil, meta, cpID, commits, checkpoint.Author{}, styles)

	if !strings.Contains(got, "  commits  (2)") {
		t.Errorf("expected commits row with count (2), got:\n%s", got)
	}
	if !strings.Contains(got, "           aaa1111 2026-04-29 first") {
		t.Errorf("expected first commit line aligned under value column, got:\n%s", got)
	}
	if !strings.Contains(got, "           bbb2222 2026-04-29 second") {
		t.Errorf("expected second commit line aligned under value column, got:\n%s", got)
	}
}

func TestFormatCheckpointHeader_EmptyCommitsSlice(t *testing.T) {
	t.Parallel()

	cpID := id.MustCheckpointID("a3b2c4d5e6f7")
	meta := checkpoint.CommittedMetadata{
		SessionID: "s",
		CreatedAt: time.Date(2026, 4, 29, 14, 22, 8, 0, time.UTC),
	}
	styles := statusStyles{colorEnabled: false, width: 60}

	got := formatCheckpointHeader(nil, meta, cpID, []associatedCommit{}, checkpoint.Author{}, styles)

	if !strings.Contains(got, "  commits  (none on this branch)") {
		t.Errorf("expected explicit none row when commits slice is empty, got:\n%s", got)
	}
}

func TestFormatCheckpointHeader_NoTokenUsage(t *testing.T) {
	t.Parallel()

	cpID := id.MustCheckpointID("a3b2c4d5e6f7")
	meta := checkpoint.CommittedMetadata{
		SessionID: "s",
		CreatedAt: time.Date(2026, 4, 29, 14, 22, 8, 0, time.UTC),
	}
	styles := statusStyles{colorEnabled: false, width: 60}

	got := formatCheckpointHeader(nil, meta, cpID, nil, checkpoint.Author{}, styles)

	if strings.Contains(got, "  tokens") {
		t.Errorf("did not expect tokens row when both meta and summary are nil, got:\n%s", got)
	}
}

func TestFormatCheckpointHeader_TokensFromSummaryFallback(t *testing.T) {
	t.Parallel()

	cpID := id.MustCheckpointID("a3b2c4d5e6f7")
	meta := checkpoint.CommittedMetadata{
		SessionID:  "s",
		CreatedAt:  time.Date(2026, 4, 29, 14, 22, 8, 0, time.UTC),
		TokenUsage: nil,
	}
	summary := &checkpoint.CheckpointSummary{
		TokenUsage: &agent.TokenUsage{InputTokens: 1234},
	}
	styles := statusStyles{colorEnabled: false, width: 60}

	got := formatCheckpointHeader(summary, meta, cpID, nil, checkpoint.Author{}, styles)

	if !strings.Contains(got, "  tokens   1.2k") {
		t.Errorf("expected tokens row from summary fallback, got:\n%s", got)
	}
}

func TestFormatCheckpointHeader_ColorEnabledRenders(t *testing.T) {
	t.Parallel()

	cpID := id.MustCheckpointID("a3b2c4d5e6f7")
	meta := checkpoint.CommittedMetadata{
		SessionID:  "s",
		CreatedAt:  time.Date(2026, 4, 29, 14, 22, 8, 0, time.UTC),
		TokenUsage: &agent.TokenUsage{InputTokens: 1234},
	}
	plainStyles := statusStyles{colorEnabled: false, width: 60}
	colorStyles := statusStyles{
		colorEnabled: true,
		width:        60,
		bold:         lipgloss.NewStyle().Bold(true),
		dim:          lipgloss.NewStyle().Faint(true),
		yellow:       lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
	}

	plain := formatCheckpointHeader(nil, meta, cpID, nil, checkpoint.Author{}, plainStyles)
	styled := formatCheckpointHeader(nil, meta, cpID, nil, checkpoint.Author{}, colorStyles)

	if !strings.Contains(plain, "●") {
		t.Errorf("expected ● glyph in plain output, got:\n%s", plain)
	}
	if !strings.Contains(styled, "●") {
		t.Errorf("expected ● glyph in styled output, got:\n%s", styled)
	}
	if len(styled) <= len(plain) {
		t.Errorf("expected styled length (%d) > plain length (%d)", len(styled), len(plain))
	}
}

func TestBuildPagerCmd_LessRInjectedWhenEnvUnset(t *testing.T) {
	oldEnv := pagerLookupEnv
	t.Cleanup(func() { pagerLookupEnv = oldEnv })

	pagerLookupEnv = func(key string) string {
		if key == pagerEnvVar || key == lessEnvVar {
			return ""
		}
		return os.Getenv(key)
	}

	cmd, pager := buildPagerCmd(context.Background())

	if runtime.GOOS == windowsGOOS {
		t.Skip("LESS injection only applies to less on Unix")
	}
	if pager != lessPagerName {
		t.Fatalf("expected resolved pager 'less' on non-Windows, got %q", pager)
	}

	found := false
	for _, e := range cmd.Env {
		if e == lessRawControlEnv {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected LESS=-R in cmd.Env")
	}
}

func TestBuildPagerCmd_ReplacesEmptyLessEnv(t *testing.T) {
	t.Setenv(lessEnvVar, "")

	oldEnv := pagerLookupEnv
	t.Cleanup(func() { pagerLookupEnv = oldEnv })

	pagerLookupEnv = func(key string) string {
		if key == pagerEnvVar || key == lessEnvVar {
			return ""
		}
		return os.Getenv(key)
	}

	cmd, pager := buildPagerCmd(context.Background())

	if runtime.GOOS == windowsGOOS {
		t.Skip("LESS injection only applies to less on Unix")
	}
	if pager != lessPagerName {
		t.Fatalf("expected resolved pager 'less' on non-Windows, got %q", pager)
	}

	lessEntries := 0
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, lessEnvVar+"=") {
			lessEntries++
			if e != lessRawControlEnv {
				t.Errorf("expected %s, got %q", lessRawControlEnv, e)
			}
		}
	}
	if lessEntries != 1 {
		t.Errorf("expected exactly one LESS entry, got %d", lessEntries)
	}
}
