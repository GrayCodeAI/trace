package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/claudecode"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/summarize"
	"github.com/GrayCodeAI/trace/cli/testutil"
	"github.com/GrayCodeAI/trace/cli/trailers"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/require"
)

func TestNewExplainCmd(t *testing.T) {
	cmd := newExplainCmd()

	if cmd.Name() != "explain" {
		t.Errorf("expected command name to be 'explain', got %s", cmd.Name())
	}
	if cmd.Use != "explain [checkpoint-id | commit-sha]" {
		t.Errorf("expected Use %q, got %q", "explain [checkpoint-id | commit-sha]", cmd.Use)
	}

	// Verify flags exist
	sessionFlag := cmd.Flags().Lookup("session")
	if sessionFlag == nil {
		t.Error("expected --session flag to exist")
	}

	commitFlag := cmd.Flags().Lookup("commit")
	if commitFlag == nil {
		t.Error("expected --commit flag to exist")
	}

	generateFlag := cmd.Flags().Lookup("generate")
	if generateFlag == nil {
		t.Error("expected --generate flag to exist")
	}

	forceFlag := cmd.Flags().Lookup("force")
	if forceFlag == nil {
		t.Error("expected --force flag to exist")
	}
}

func TestExplainCmd_SearchAllFlag(t *testing.T) {
	cmd := newExplainCmd()

	// Verify --search-all flag exists
	flag := cmd.Flags().Lookup("search-all")
	require.NotNil(t, flag, "expected --search-all flag to exist")

	if flag.DefValue != "false" {
		t.Errorf("expected default value 'false', got %q", flag.DefValue)
	}
}

// rowsHaveValue searches rows for a value substring (in either Label or Value).
// Used by formatCheckpointSummaryError tests to assert that envelope text or
// hint phrasing surfaces somewhere in the structured rows.
func rowsHaveValue(rows []explainRow, want string) bool {
	for _, r := range rows {
		if strings.Contains(r.Value, want) || strings.Contains(r.Label, want) {
			return true
		}
	}
	return false
}

func TestFormatCheckpointSummaryError_Auth(t *testing.T) {
	t.Parallel()
	label, rows, err := formatCheckpointSummaryError(&claudecode.ClaudeError{Kind: claudecode.ClaudeErrorAuth, Message: "Invalid API key"}, 0)
	if !strings.Contains(strings.ToLower(label), "authentication failed") {
		t.Errorf("missing 'authentication failed' in label %q", label)
	}
	if !rowsHaveValue(rows, "Invalid API key") {
		t.Errorf("missing envelope message in rows: %+v", rows)
	}
	if err == nil {
		t.Fatal("expected structured error")
	}
}

func TestFormatCheckpointSummaryError_RateLimit(t *testing.T) {
	t.Parallel()
	label, _, err := formatCheckpointSummaryError(&claudecode.ClaudeError{Kind: claudecode.ClaudeErrorRateLimit, Message: "429"}, 0)
	if !strings.Contains(label, "rate limit") {
		t.Errorf("missing rate-limit phrasing in label: %q", label)
	}
	if err == nil {
		t.Fatal("expected structured error")
	}
}

func TestFormatCheckpointSummaryError_Config(t *testing.T) {
	t.Parallel()
	_, rows, err := formatCheckpointSummaryError(&claudecode.ClaudeError{Kind: claudecode.ClaudeErrorConfig, Message: "model not found"}, 0)
	if !rowsHaveValue(rows, "model not found") {
		t.Errorf("envelope message not surfaced in rows: %+v", rows)
	}
	if err == nil {
		t.Fatal("expected structured error")
	}
}

func TestFormatCheckpointSummaryError_CLIMissing(t *testing.T) {
	t.Parallel()
	label, _, err := formatCheckpointSummaryError(&claudecode.ClaudeError{Kind: claudecode.ClaudeErrorCLIMissing}, 0)
	if !strings.Contains(label, "not installed") {
		t.Errorf("missing cli-missing phrasing in label: %q", label)
	}
	if err == nil {
		t.Fatal("expected structured error")
	}
}

// TestFormatCheckpointSummaryError_TypedBranchesHandleEmptyMessage guards against
// the null-result-envelope regression: Claude can emit is_error:true with a real
// HTTP status (401/429/4xx) but result:null, producing a ClaudeError with Message="".
// The Auth/RateLimit/Config branches must not render a bare colon in label or rows.
func TestFormatCheckpointSummaryError_TypedBranchesHandleEmptyMessage(t *testing.T) {
	t.Parallel()
	kinds := []claudecode.ClaudeErrorKind{
		claudecode.ClaudeErrorAuth,
		claudecode.ClaudeErrorRateLimit,
		claudecode.ClaudeErrorConfig,
	}
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			label, rows, err := formatCheckpointSummaryError(&claudecode.ClaudeError{Kind: kind}, 0)
			if err == nil {
				t.Fatal("expected structured error")
			}
			// Label must not end with a bare colon (the classic regression of
			// rendering "...: " with nothing after it).
			if strings.HasSuffix(strings.TrimSpace(label), ":") {
				t.Errorf("label ends with bare colon: %q", label)
			}
			for _, r := range rows {
				if strings.HasSuffix(strings.TrimSpace(r.Value), ":") {
					t.Errorf("row value ends with bare colon: %q (full: %+v)", r.Value, rows)
				}
			}
		})
	}
}

func TestFormatCheckpointSummaryError_DeadlineExceeded(t *testing.T) {
	t.Parallel()
	label, rows, err := formatCheckpointSummaryError(fmt.Errorf("wrapped: %w", context.DeadlineExceeded), 5*time.Minute)
	if !strings.Contains(label, "timed out") {
		t.Errorf("expected 'timed out' in label, got %q", label)
	}
	if !strings.Contains(label, "5m") {
		t.Errorf("expected '5m' in label, got %q", label)
	}
	if len(rows) == 0 {
		t.Fatal("expected rows for causes/try")
	}
	if err == nil {
		t.Fatal("expected structured error")
	}
	if !strings.Contains(err.Error(), "safety deadline") {
		t.Errorf("expected 'safety deadline' in structured error, got %q", err)
	}
	// Negative guards against regressions:
	//   - Hardcoded "Claude" / "sonnet" / "Anthropic" would misdirect users of
	//     alternate summary providers (codex, gemini).
	combined := label + "\n" + err.Error()
	var combinedSb194 strings.Builder
	for _, r := range rows {
		combinedSb194.WriteString("\n" + r.Label + " " + r.Value)
	}
	combined += combinedSb194.String()
	for _, unwanted := range []string{"summary_timeout_seconds", "Claude", "sonnet", "Anthropic", "anthropic.com"} {
		if strings.Contains(combined, unwanted) {
			t.Errorf("unexpected %q in provider-neutral timeout message: %q", unwanted, combined)
		}
	}
}

func TestFormatCheckpointSummaryError_Canceled(t *testing.T) {
	t.Parallel()
	label, _, err := formatCheckpointSummaryError(fmt.Errorf("wrapped: %w", context.Canceled), 0)
	if !strings.Contains(label, "canceled") {
		t.Errorf("missing canceled in label: %q", label)
	}
	if err == nil {
		t.Fatal("expected structured error")
	}
}

func TestFormatCheckpointSummaryError_Passthrough(t *testing.T) {
	t.Parallel()
	_, rows, err := formatCheckpointSummaryError(errors.New("something else"), 0)
	if err == nil {
		t.Fatal("expected structured error")
	}
	combined := err.Error()
	var combinedSb219 strings.Builder
	for _, r := range rows {
		combinedSb219.WriteString(" " + r.Value)
	}
	combined += combinedSb219.String()
	if !strings.Contains(combined, "something else") {
		t.Errorf("original error not preserved in structured error or rows: %q rows=%+v", err, rows)
	}
}

// TestFormatCheckpointSummaryError_Unknown covers the three branches of the
// default-case suffix builder. Guards against users seeing
// "Claude failed to generate the summary:" with nothing after the colon
// (the null-result and no-stderr-OOM scenarios).
func TestFormatCheckpointSummaryError_Unknown(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  *claudecode.ClaudeError
		want string // substring that must appear in the label or rows
	}{
		{"APIStatus when Message empty", &claudecode.ClaudeError{Kind: claudecode.ClaudeErrorUnknown, APIStatus: 500}, "500"},
		{"ExitCode when Message empty", &claudecode.ClaudeError{Kind: claudecode.ClaudeErrorUnknown, ExitCode: 137}, "137"},
		{"Negative ExitCode renders as abnormal, not -1", &claudecode.ClaudeError{Kind: claudecode.ClaudeErrorUnknown, ExitCode: -1}, "abnormal"},
		{"All-zero fields render a diagnostic sentinel, not empty", &claudecode.ClaudeError{Kind: claudecode.ClaudeErrorUnknown}, "no diagnostic detail"},
		{"Message takes precedence", &claudecode.ClaudeError{Kind: claudecode.ClaudeErrorUnknown, Message: "something weird"}, "something weird"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			label, rows, err := formatCheckpointSummaryError(tc.err, 0)
			if err == nil {
				t.Fatal("expected structured error")
			}
			if strings.HasSuffix(strings.TrimSpace(label), ":") {
				t.Errorf("label ends with bare colon: %q", label)
			}
			combined := label
			var combinedSb260 strings.Builder
			for _, r := range rows {
				combinedSb260.WriteString(" " + r.Value)
			}
			combined += combinedSb260.String()
			if !strings.Contains(combined, tc.want) {
				t.Errorf("missing %q in %q", tc.want, combined)
			}
		})
	}
}

// TestExplainCmd_PositionalArgConflictsWithFlags verifies that combining a
// positional target with --checkpoint, --commit, or --session is rejected.
// The bare-positional happy path (auto-resolution to a checkpoint ID or commit
// ref) is covered by the TestRunExplainAuto_* tests in this file.
func TestExplainCmd_PositionalArgConflictsWithFlags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
	}{
		{"positional arg with --checkpoint", []string{"abc123", "--checkpoint", "def456"}},
		{"positional arg with -c", []string{"abc123", "-c", "def456"}},
		{"positional arg with --commit", []string{"abc123", "--commit", "HEAD"}},
		{"positional arg with --session", []string{"abc123", "--session", "sess-1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := newExplainCmd()
			var stdout, stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			cmd.SetArgs(tt.args)

			err := cmd.Execute()
			if err == nil {
				t.Fatalf("expected error when combining positional arg with flags, got nil")
			}
			if !strings.Contains(err.Error(), "cannot combine positional argument") {
				t.Errorf("expected 'cannot combine positional argument' error, got: %v", err)
			}
		})
	}
}

// runExplainAutoTestRepo seeds a git repo and returns the initial commit's hash.
func runExplainAutoTestRepo(t *testing.T) (repo *git.Repository, initialCommit plumbing.Hash) {
	t.Helper()
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	testutil.WriteFile(t, tmpDir, "seed.txt", "seed")
	testutil.GitAdd(t, tmpDir, "seed.txt")
	testutil.GitCommit(t, tmpDir, "seed commit")

	opened, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)
	head, err := opened.Head()
	require.NoError(t, err)
	return opened, head.Hash()
}

// TestRunExplainAuto_NoMatchReturnsCompositeError verifies that a target
// that's neither a checkpoint ID/prefix nor a resolvable git ref returns
// the composite "no checkpoint or commit found" error — proving the
// checkpoint-first → commit-fallback routing chains correctly all the way
// to the final error.
func TestRunExplainAuto_NoMatchReturnsCompositeError(t *testing.T) {
	runExplainAutoTestRepo(t)

	var out, errOut bytes.Buffer
	err := runExplainAuto(context.Background(), &out, &errOut, "abababababab", false, false, false, false, false, false, false)

	require.Error(t, err)
	require.ErrorContains(t, err, `no checkpoint or commit found matching "abababababab"`)
}

// TestRunExplainAuto_CommitRefWithCheckpointTrailer verifies that a commit
// SHA passed positionally falls through to commit resolution and delegates
// to the checkpoint path with the ID from the Trace-Checkpoint trailer.
func TestRunExplainAuto_CommitRefWithCheckpointTrailer(t *testing.T) {
	repo, _ := runExplainAutoTestRepo(t)
	ctx := context.Background()

	cpID := id.MustCheckpointID("deadbeefcafe")
	require.NoError(t, checkpoint.NewGitStore(repo).WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID: cpID,
		SessionID:    "session-auto",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte(`{"type":"user","message":{"content":[{"type":"text","text":"hello"}]}}` + "\n")),
		AuthorName:   "Test",
		AuthorEmail:  "test@example.com",
	}))

	wt, err := repo.Worktree()
	require.NoError(t, err)
	tmpDir := wt.Filesystem.Root()
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "feature.txt"), []byte("feature"), 0o644))
	_, err = wt.Add("feature.txt")
	require.NoError(t, err)
	commitHash, err := wt.Commit(trailers.AppendCheckpointTrailer("Implement feature", cpID.String()), &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	require.NoError(t, err)

	var out, errOut bytes.Buffer
	err = runExplainAuto(ctx, &out, &errOut, commitHash.String(), true, false, false, false, false, false, false)
	require.NoError(t, err)
	require.Contains(t, out.String(), cpID.String(), "expected checkpoint header resolved via trailer")
}

// TestRunExplainAuto_CommitWithoutTrailer covers the trailer-less commit
// dispatch: read-only modes print a friendly message and exit 0, while
// --generate / --raw-transcript must error so scripts can distinguish
// "done" from "didn't happen" (Cursor Bugbot finding on PR #990).
func TestRunExplainAuto_CommitWithoutTrailer(t *testing.T) {
	_, initial := runExplainAutoTestRepo(t)
	shortSHA := initial.String()[:7]

	tests := []struct {
		name        string
		rawTrans    bool
		generate    bool
		wantErr     bool
		wantContain string // substring required in err (if wantErr) or out (if !wantErr)
	}{
		{"read-only prints friendly message", false, false, false, "✗ No associated Trace checkpoint"},
		{"--generate errors", false, true, true, "cannot generate summary"},
		{"--raw-transcript errors", true, false, true, "cannot show raw transcript"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			err := runExplainAuto(context.Background(), &out, &errOut, initial.String(), true, false, false, tc.rawTrans, tc.generate, false, false)
			if tc.wantErr {
				require.Error(t, err)
				require.ErrorContains(t, err, tc.wantContain)
				require.ErrorContains(t, err, shortSHA)
			} else {
				require.NoError(t, err)
				require.Contains(t, out.String(), tc.wantContain)
				require.Contains(t, out.String(), shortSHA)
			}
		})
	}
}

// TestRunExplainCheckpoint_NotFoundSentinels verifies the typed-error
// contract runExplainAuto depends on: non-matching targets return an error
// wrapping checkpoint.ErrCheckpointNotFound (for errors.Is detection),
// regardless of --generate. The old code returned the temp-checkpoint
// sentinel speculatively for --generate, breaking fallback routing.
func TestRunExplainCheckpoint_NotFoundSentinels(t *testing.T) {
	runExplainAutoTestRepo(t)

	for _, generate := range []bool{false, true} {
		t.Run(fmt.Sprintf("generate=%v", generate), func(t *testing.T) {
			var out, errOut bytes.Buffer
			err := runExplainCheckpoint(context.Background(), &out, &errOut, "abababababab", false, false, false, false, generate, false, false)

			require.Error(t, err)
			require.ErrorIs(t, err, checkpoint.ErrCheckpointNotFound)
			require.NotErrorIs(t, err, errCannotGenerateTemporaryCheckpoint,
				"sentinel must not fire unless a real temp checkpoint was matched")
		})
	}
}

func writeTemporaryCheckpointForExplainTest(t *testing.T) string {
	t.Helper()

	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	wt, err := repo.Worktree()
	require.NoError(t, err)

	testFile := filepath.Join(tmpDir, "temp.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("initial content"), 0o644))
	_, err = wt.Add("temp.txt")
	require.NoError(t, err)
	initialCommit, err := wt.Commit("initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	require.NoError(t, err)

	sessionID := "2026-01-27-temp-session"
	metadataDir := filepath.Join(tmpDir, ".trace", "metadata", sessionID)
	require.NoError(t, os.MkdirAll(metadataDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(metadataDir, paths.PromptFileName), []byte("temporary checkpoint prompt"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(metadataDir, "full.jsonl"), []byte(`{"type":"user","message":{"content":[{"type":"text","text":"temporary checkpoint"}]}}`+"\n"), 0o644))

	require.NoError(t, os.WriteFile(testFile, []byte("updated content"), 0o644))

	result, err := checkpoint.NewGitStore(repo).WriteTemporary(context.Background(), checkpoint.WriteTemporaryOptions{
		SessionID:         sessionID,
		BaseCommit:        initialCommit.String()[:7],
		ModifiedFiles:     []string{"temp.txt"},
		MetadataDir:       ".trace/metadata/" + sessionID,
		MetadataDirAbs:    metadataDir,
		CommitMessage:     "temporary checkpoint with code changes",
		AuthorName:        "Test",
		AuthorEmail:       "test@example.com",
		IsFirstCheckpoint: false,
	})
	require.NoError(t, err)
	require.False(t, result.Skipped)

	return result.CommitHash.String()
}

func TestRunExplainAuto_GenerateTemporaryCheckpointDoesNotFallBackToCommit(t *testing.T) {
	tempCheckpointSHA := writeTemporaryCheckpointForExplainTest(t)

	var out, errOut bytes.Buffer
	err := runExplainAuto(context.Background(), &out, &errOut, tempCheckpointSHA, true, false, false, false, true, false, false)

	require.Error(t, err)
	require.ErrorIs(t, err, errCannotGenerateTemporaryCheckpoint)
	require.NotErrorIs(t, err, checkpoint.ErrCheckpointNotFound)
	require.NotContains(t, err.Error(), "no Trace-Checkpoint trailer")
}

// TestRunExplainAuto_TemporaryCheckpointRendersIdentityBullet verifies the
// brand identity-bullet shape is used for temporary checkpoints, with the
// "after commit" affordance text in the summary block.
func TestRunExplainAuto_TemporaryCheckpointRendersIdentityBullet(t *testing.T) {
	tempCheckpointSHA := writeTemporaryCheckpointForExplainTest(t)
	shortID := tempCheckpointSHA[:7]

	var out, errOut bytes.Buffer
	// noPager=true to suppress the pager's terminal-only path so output lands
	// in the buffer; generate=false so we read (and don't try to summarize).
	err := runExplainAuto(context.Background(), &out, &errOut, tempCheckpointSHA, true, false, false, false, false, false, false)
	require.NoError(t, err)

	output := out.String()
	if !strings.Contains(output, fmt.Sprintf("● Checkpoint %s [temporary]", shortID)) {
		t.Errorf("expected '● Checkpoint %s [temporary]' identity bullet, got:\n%s", shortID, output)
	}
	if !strings.Contains(output, "## Summary") {
		t.Errorf("expected '## Summary' heading in temporary output, got:\n%s", output)
	}
	if !strings.Contains(output, "Temporary checkpoints can be summarized after commit") {
		t.Errorf("expected 'after commit' affordance in temporary output, got:\n%s", output)
	}
}

// collidingShaPrefix creates commits until two share a 2-char SHA prefix
// and returns that prefix. 2 chars is the smallest even-byte boundary
// HashesWithPrefix uses, so a collision at this length reliably exercises
// the ambiguity detection path without SHA mining.
func collidingShaPrefix(t *testing.T, repo *git.Repository, tmpDir string) string {
	t.Helper()
	wt, err := repo.Worktree()
	require.NoError(t, err)

	seen := make(map[string]int)
	for i := range 300 {
		testutil.WriteFile(t, tmpDir, "f.txt", fmt.Sprintf("content-%d", i))
		_, err = wt.Add("f.txt")
		require.NoError(t, err)
		h, err := wt.Commit(fmt.Sprintf("commit %d", i), &git.CommitOptions{
			Author: &object.Signature{Name: "Test", Email: "t@e.com", When: time.Now().Add(time.Duration(i) * time.Second)},
		})
		require.NoError(t, err)
		p := h.String()[:2]
		seen[p]++
		if seen[p] >= 2 {
			return p
		}
	}
	t.Skip("could not produce colliding 2-char SHA prefix in 300 iterations")
	return ""
}

// TestResolveCommitUnambiguous_MultipleCommitMatches verifies the reviewer-
// flagged bug: go-git v6's ResolveRevision silently returns the first
// candidate when a hex prefix matches multiple commits. With the helper
// wrapping it, ambiguity must surface as errAmbiguousCommitPrefix.
func TestResolveCommitUnambiguous_MultipleCommitMatches(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	prefix := collidingShaPrefix(t, repo, tmpDir)

	_, matches, err := resolveCommitUnambiguous(repo, prefix)
	require.Error(t, err)
	require.ErrorIs(t, err, errAmbiguousCommitPrefix)
	require.GreaterOrEqual(t, len(matches), 2, "expected ambiguous matches slice")
}

// TestRunExplainCommit_AmbiguousPrintsToErrWAndReturnsSilent verifies the
// ambiguous-prefix path: the styled failure block lands on errW, the
// returned error is a *SilentError (so main.go does not double-print),
// and stdout stays empty.
func TestRunExplainCommit_AmbiguousPrintsToErrWAndReturnsSilent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	prefix := collidingShaPrefix(t, repo, tmpDir)

	var out, errOut bytes.Buffer
	err = runExplainCommit(context.Background(), &out, &errOut, prefix, true, false, false, false, false, false, false)

	var silent *SilentError
	if !errors.As(err, &silent) {
		t.Fatalf("expected *SilentError, got %T: %v", err, err)
	}
	if !strings.Contains(errOut.String(), "✗ Ambiguous checkpoint prefix") {
		t.Errorf("missing styled failure on errW:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "matches") {
		t.Errorf("expected 'matches' row in errW:\n%s", errOut.String())
	}
	if out.String() != "" {
		t.Errorf("did not expect anything on stdout:\n%s", out.String())
	}
}

// TestRunExplainCheckpoint_AmbiguousCommittedPrefixPrintsToErrWAndReturnsSilent
// verifies that an ambiguous prefix matching multiple committed checkpoints
// renders the styled failure block to errW (not stdout) and returns a
// *SilentError so main.go does not double-print. Mirrors the commit-side
// ambiguity test.
func TestRunExplainCheckpoint_AmbiguousCommittedPrefixPrintsToErrWAndReturnsSilent(t *testing.T) {
	repo, _ := runExplainAutoTestRepo(t)
	ctx := context.Background()

	// Seed two committed checkpoints sharing a hex prefix.
	store := checkpoint.NewGitStore(repo)
	transcriptBytes := redact.AlreadyRedacted([]byte(`{"type":"user","message":{"content":[{"type":"text","text":"hello"}]}}` + "\n"))
	for _, cpID := range []id.CheckpointID{
		id.MustCheckpointID("e7aaaaaaaaaa"),
		id.MustCheckpointID("e7bbbbbbbbbb"),
	} {
		require.NoError(t, store.WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
			CheckpointID: cpID,
			SessionID:    "session-" + cpID.String(),
			Strategy:     "manual-commit",
			Transcript:   transcriptBytes,
			AuthorName:   "Test",
			AuthorEmail:  "test@example.com",
		}))
	}

	var out, errOut bytes.Buffer
	err := runExplainCheckpoint(ctx, &out, &errOut, "e7", true, false, false, false, false, false, false)

	var silent *SilentError
	if !errors.As(err, &silent) {
		t.Fatalf("expected *SilentError, got %T: %v", err, err)
	}
	if !strings.Contains(errOut.String(), "✗ Ambiguous checkpoint prefix") {
		t.Errorf("missing styled failure on errW:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "matches") {
		t.Errorf("expected 'matches' row in errW:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "committed checkpoints") {
		t.Errorf("expected 'committed checkpoints' kind in errW:\n%s", errOut.String())
	}
	if out.String() != "" {
		t.Errorf("did not expect anything on stdout:\n%s", out.String())
	}
}

// TestResolveCommitUnambiguous_UniquePrefixSucceeds verifies a full SHA
// resolves to the expected hash without triggering ambiguity detection.
func TestResolveCommitUnambiguous_UniquePrefixSucceeds(t *testing.T) {
	_, initial := runExplainAutoTestRepo(t)
	repo, err := git.PlainOpen(".")
	require.NoError(t, err)

	got, matches, err := resolveCommitUnambiguous(repo, initial.String())
	require.NoError(t, err)
	require.Nil(t, matches, "no ambiguous matches expected")
	require.Equal(t, initial, got)
}

// TestAbbreviateCommitHash_GrowsOnCollision verifies the helper grows past
// the default 7 chars when necessary — matching git's --abbrev auto-growth.
// The same 2-char SHA collision we construct for resolution is enough to
// force abbreviation beyond 2 chars (though in practice 7 still tends to
// be unique for ~300 commits).
func TestAbbreviateCommitHash_GrowsOnCollision(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	testutil.InitRepo(t, tmpDir)
	repo, err := git.PlainOpen(tmpDir)
	require.NoError(t, err)

	prefix := collidingShaPrefix(t, repo, tmpDir)

	// Find a hash whose SHA starts with the colliding prefix.
	hashes := commitHashesWithPrefix(repo, prefix)
	require.GreaterOrEqual(t, len(hashes), 2)

	abbrev := abbreviateCommitHash(repo, hashes[0])
	require.True(t, strings.HasPrefix(hashes[0].String(), abbrev), "abbreviation must be a prefix of the full hash")
	require.GreaterOrEqual(t, len(abbrev), 7, "abbreviation must be at least git's default of 7 chars")
	require.LessOrEqual(t, len(abbrev), 40, "abbreviation cannot exceed full hash length")
}

// TestAbbreviateCommitHash_UsesSevenByDefault verifies the helper returns
// the 7-char default when there's no collision, matching git's behavior.
func TestAbbreviateCommitHash_UsesSevenByDefault(t *testing.T) {
	_, initial := runExplainAutoTestRepo(t)
	repo, err := git.PlainOpen(".")
	require.NoError(t, err)

	abbrev := abbreviateCommitHash(repo, initial)
	require.Equal(t, initial.String()[:7], abbrev)
}

// TestRunExplainAuto_GenerateAmbiguousPrefixRefused guards the Codex finding
// that a short positional arg matching both a committed-checkpoint prefix
// and a git revision must not silently write a summary to the wrong
// checkpoint. SHA mining isn't practical, so we construct the collision by
// picking a checkpoint ID that starts with the seed commit's abbreviation.
func TestRunExplainAuto_GenerateAmbiguousPrefixRefused(t *testing.T) {
	repo, _ := runExplainAutoTestRepo(t)
	ctx := context.Background()

	head, err := repo.Head()
	require.NoError(t, err)
	commitPrefix := head.Hash().String()[:7]
	collisionID := id.MustCheckpointID(commitPrefix + "aaaaa")

	require.NoError(t, checkpoint.NewGitStore(repo).WriteCommitted(ctx, checkpoint.WriteCommittedOptions{
		CheckpointID: collisionID,
		SessionID:    "session-collision",
		Strategy:     "manual-commit",
		Transcript:   redact.AlreadyRedacted([]byte(`{"type":"user","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n")),
		AuthorName:   "Test",
		AuthorEmail:  "test@example.com",
	}))

	var out, errOut bytes.Buffer
	err = runExplainAuto(ctx, &out, &errOut, commitPrefix, true, false, false, false, true, false, false)

	require.Error(t, err)
	require.ErrorContains(t, err, "ambiguous target")
	require.ErrorContains(t, err, "--commit")
	require.ErrorContains(t, err, "--checkpoint")
}

// TestExplainCmd_CommitFlagWithGenerateValidates verifies --commit +
// --generate passes flag validation (previously hasCheckpointTarget
// excluded commitFlag, so the explicit form couldn't invoke generate).
func TestExplainCmd_CommitFlagWithGenerateValidates(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	testutil.InitRepo(t, tmpDir)
	testutil.WriteFile(t, tmpDir, "f.txt", "x")
	testutil.GitAdd(t, tmpDir, "f.txt")
	testutil.GitCommit(t, tmpDir, "seed")

	cmd := newExplainCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--commit", "HEAD", "--generate"})

	// Command will fail downstream (no trailer on seed commit), but must
	// not fail at flag validation.
	if err := cmd.Execute(); err != nil {
		require.NotContains(t, err.Error(), "--generate requires")
	}
}

func TestGenerateCheckpointAISummary_AddsDefaultTimeoutWithoutParentDeadline(t *testing.T) {
	tmpTimeout := checkpointSummaryTimeout
	tmpGenerator := generateTranscriptSummary
	t.Cleanup(func() {
		checkpointSummaryTimeout = tmpTimeout
		generateTranscriptSummary = tmpGenerator
	})

	checkpointSummaryTimeout = 50 * time.Millisecond

	var gotDeadline time.Time
	generateTranscriptSummary = func(
		ctx context.Context,
		_ redact.RedactedBytes,
		_ []string,
		_ types.AgentType,
		_ summarize.Generator,
	) (*checkpoint.Summary, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return nil, errors.New("expected deadline on summary context")
		}
		gotDeadline = deadline
		return &checkpoint.Summary{Intent: "intent", Outcome: "outcome"}, nil
	}

	start := time.Now()
	summary, _, err := generateCheckpointAISummary(context.Background(), []byte("transcript"), nil, agent.AgentTypeClaudeCode, nil)
	if err != nil {
		t.Fatalf("generateCheckpointAISummary() error = %v", err)
	}
	if summary == nil {
		t.Fatal("expected summary")
	}
	if gotDeadline.IsZero() {
		t.Fatal("expected deadline to be set")
	}
	if remaining := gotDeadline.Sub(start); remaining < 30*time.Millisecond || remaining > 200*time.Millisecond {
		t.Fatalf("deadline offset = %s, want around %s", remaining, checkpointSummaryTimeout)
	}
}
