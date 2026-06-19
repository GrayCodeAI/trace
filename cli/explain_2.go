package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/claudecode"
	"github.com/GrayCodeAI/trace/cli/agent/external"
	"github.com/GrayCodeAI/trace/cli/agent/geminicli"
	"github.com/GrayCodeAI/trace/cli/agent/opencode"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/checkpoint/remote"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cli/summarize"
	"github.com/GrayCodeAI/trace/cli/trailers"
	"github.com/GrayCodeAI/trace/cli/transcript"
	"github.com/GrayCodeAI/trace/redact"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
)

func newExplainCheckpointLookup(ctx context.Context) (*explainCheckpointLookup, error) {
	repo, err := openRepository(ctx)
	if err != nil {
		return nil, fmt.Errorf("not a git repository: %w", err)
	}

	v2URL, err := remote.FetchURL(ctx)
	if err != nil {
		logging.Debug(
			ctx, "explain: using origin for v2 store fetch remote",
			slog.String("error", err.Error()),
		)
		v2URL = ""
	}

	// FetchBlobsByHash uses `git fetch-pack` for blob SHAs (porcelain
	// `git fetch` fails against partial-clone repos with "did not send all
	// necessary objects"). Falls back to a full metadata-branch fetch if
	// fetch-pack also can't reach the blobs.
	v1Store := checkpoint.NewGitStore(repo)
	v1Store.SetBlobFetcher(FetchBlobsByHash)

	v2Store := checkpoint.NewV2GitStore(repo, v2URL)
	v2Store.SetBlobFetcher(FetchBlobsByHash)

	lookup := &explainCheckpointLookup{
		repo:                repo,
		v1Store:             v1Store,
		v2Store:             v2Store,
		preferCheckpointsV2: settings.IsCheckpointsV2Enabled(ctx),
	}

	committed, err := listCommittedForExplain(ctx, lookup.v1Store, lookup.v2Store, lookup.preferCheckpointsV2)
	if err != nil {
		return nil, fmt.Errorf("failed to list checkpoints: %w", err)
	}
	lookup.committed = committed
	return lookup, nil
}

func listCommittedForExplain(ctx context.Context, v1Store *checkpoint.GitStore, v2Store *checkpoint.V2GitStore, preferCheckpointsV2 bool) ([]checkpoint.CommittedInfo, error) {
	v1Committed, v1Err := v1Store.ListCommitted(ctx)

	if !preferCheckpointsV2 {
		if v1Err != nil {
			return nil, fmt.Errorf("listing v1 checkpoints: %w", v1Err)
		}
		return v1Committed, nil
	}

	v2Committed, v2Err := v2Store.ListCommitted(ctx)
	if v2Err != nil {
		logging.Debug(
			ctx, "v2 ListCommitted failed, using v1 only",
			slog.String("error", v2Err.Error()),
		)
		if v1Err != nil {
			return nil, fmt.Errorf("listing checkpoints: %w", v1Err)
		}
		return v1Committed, nil
	}

	if v1Err != nil {
		logging.Debug(
			ctx, "v1 ListCommitted failed, returning v2 only",
			slog.String("error", v1Err.Error()),
		)
		return v2Committed, nil
	}

	// Merge v2 and v1 results so pre-v2 checkpoints remain visible during transition.
	seen := make(map[id.CheckpointID]struct{}, len(v2Committed))
	for _, c := range v2Committed {
		seen[c.CheckpointID] = struct{}{}
	}
	committedCheckpoints := make([]checkpoint.CommittedInfo, 0, len(v2Committed)+len(v1Committed))
	committedCheckpoints = append(committedCheckpoints, v2Committed...)
	for _, c := range v1Committed {
		if _, ok := seen[c.CheckpointID]; !ok {
			committedCheckpoints = append(committedCheckpoints, c)
		}
	}
	return committedCheckpoints, nil
}

func readLatestSessionContentForExplain(ctx context.Context, reader checkpoint.CommittedReader, checkpointID id.CheckpointID, summary *checkpoint.CheckpointSummary) (*checkpoint.SessionContent, error) {
	if summary == nil || len(summary.Sessions) == 0 {
		return nil, checkpoint.ErrCheckpointNotFound
	}

	latestIndex := len(summary.Sessions) - 1
	content, err := reader.ReadSessionContent(ctx, checkpointID, latestIndex)
	if err != nil {
		return nil, fmt.Errorf("reading session %d content: %w", latestIndex, err)
	}
	return content, nil
}

// resolvePromptTree picks the best metadata tree for reading session prompts.
// Prefers v2 when enabled (same sharded layout as v1), falls back to v1.
func resolvePromptTree(v1Tree, v2Tree *object.Tree, preferV2 bool) *object.Tree {
	if preferV2 && v2Tree != nil {
		return v2Tree
	}
	if v1Tree != nil {
		return v1Tree
	}
	return v2Tree // Last resort: use v2 even if not preferred
}

// readV2ContentFromMain reads session content from the v2 /main ref only —
// metadata, prompts, and the compact transcript (transcript.jsonl). This is the
// primary read path for default display modes that don't need the raw transcript
// stored on /full/* refs.
func readV2ContentFromMain(ctx context.Context, v2Reader *checkpoint.V2GitStore, checkpointID id.CheckpointID, summary *checkpoint.CheckpointSummary) (*checkpoint.SessionContent, error) {
	if summary == nil || len(summary.Sessions) == 0 {
		return nil, checkpoint.ErrCheckpointNotFound
	}

	latestIndex := len(summary.Sessions) - 1

	content, err := v2Reader.ReadSessionMetadataAndPrompts(ctx, checkpointID, latestIndex)
	if err != nil {
		return nil, fmt.Errorf("reading session %d metadata: %w", latestIndex, err)
	}

	// ReadSessionMetadataAndPrompts reads the compact transcript from the same
	// session tree. Reset transcript offsets when compact data is present.
	if len(content.Transcript) > 0 {
		content.Metadata.CheckpointTranscriptStart = 0
		//lint:ignore SA1019 // Set for backward compat with older CLI readers
		content.Metadata.TranscriptLinesAtStart = 0
		return content, nil
	}

	// No compact transcript on /main — fall back to the raw transcript on
	// /full/current for the most accurate display before resorting to prompt.txt.
	fullContent, fullErr := v2Reader.ReadSessionContent(ctx, checkpointID, latestIndex)
	if fullErr == nil && len(fullContent.Transcript) > 0 {
		content.Transcript = fullContent.Transcript
		return content, nil
	}

	// Last resort: return metadata + prompts without transcript.
	return content, nil
}

// generateCheckpointSummary generates an AI summary for a checkpoint and persists it.
// The summary is generated from the scoped transcript (only this checkpoint's portion),
// not the trace session transcript.
func generateCheckpointSummary(ctx context.Context, w, errW io.Writer, v1Store *checkpoint.GitStore, v2Store *checkpoint.V2GitStore, checkpointID id.CheckpointID, cpSummary *checkpoint.CheckpointSummary, content *checkpoint.SessionContent, force bool) error {
	// Check if summary already exists
	if content.Metadata.Summary != nil && !force {
		return renderExplainFailure(errW, "Summary already exists", []explainRow{
			{Label: "id", Value: checkpointID.String()},
			{Label: "try", Value: fmt.Sprintf("trace explain --generate --force %s", checkpointID)},
		}, fmt.Errorf("checkpoint %s already has a summary", checkpointID))
	}

	// Check if transcript exists
	if len(content.Transcript) == 0 {
		return renderExplainFailure(errW, "Checkpoint has no transcript", []explainRow{
			{Label: "id", Value: checkpointID.String()},
		}, fmt.Errorf("checkpoint %s has no transcript to summarize", checkpointID))
	}

	// Scope the transcript to only this checkpoint's portion
	scopedTranscript := scopeTranscriptForCheckpoint(content.Transcript, content.Metadata.GetTranscriptStart(), content.Metadata.Agent)
	if len(scopedTranscript) == 0 {
		return renderExplainFailure(errW, "Checkpoint has no transcript content (scoped)", []explainRow{
			{Label: "id", Value: checkpointID.String()},
		}, fmt.Errorf("checkpoint %s has no transcript content for this checkpoint (scoped)", checkpointID))
	}
	provider, err := resolveCheckpointSummaryProvider(ctx, w)
	if err != nil {
		return fmt.Errorf("failed to resolve summary provider: %w", err)
	}
	scopedTranscript = maybeCompactExternalTranscriptForSummary(ctx, scopedTranscript, content.Metadata.Agent)

	// Generate summary using shared helper
	logging.Info(ctx, "generating checkpoint summary")
	if errW != nil {
		fmt.Fprintln(errW, "Generating checkpoint summary...")
	}

	start := time.Now()
	summary, appliedDeadline, err := generateCheckpointAISummary(ctx, scopedTranscript, cpSummary.FilesTouched, content.Metadata.Agent, provider.Generator)
	if err != nil {
		label, rows, structured := formatCheckpointSummaryError(err, appliedDeadline)
		styles := newStatusStyles(errW)
		fmt.Fprint(errW, styles.renderFailure(label, rows))
		return NewSilentError(structured)
	}
	elapsed := time.Since(start)

	// Persist to both stores; at least one must succeed.
	v1Err := v1Store.UpdateSummary(ctx, checkpointID, summary)
	var v2Err error
	if v2Store != nil {
		v2Err = v2Store.UpdateSummary(ctx, checkpointID, summary)
	}

	switch {
	case v1Err != nil && (v2Store == nil || v2Err != nil):
		// No store succeeded — hard error.
		if v2Err != nil {
			return fmt.Errorf("failed to save summary: v1: %w, v2: %w", v1Err, v2Err)
		}
		return fmt.Errorf("failed to save summary: %w", v1Err)
	case v1Err != nil:
		logging.Debug(
			ctx, "v1 UpdateSummary failed (v2 succeeded)",
			slog.String("checkpoint_id", checkpointID.String()),
			slog.String("error", v1Err.Error()),
		)
	case v2Err != nil:
		logging.Debug(
			ctx, "v2 UpdateSummary failed (v1 succeeded)",
			slog.String("checkpoint_id", checkpointID.String()),
			slog.String("error", v2Err.Error()),
		)
	}

	styles := newStatusStyles(w)
	rows := summaryProviderRows(provider)
	rows = append(rows, explainRow{Label: "duration", Value: formatSummaryDuration(elapsed)})
	fmt.Fprint(w, styles.renderSuccess(fmt.Sprintf("Summary generated for %s", checkpointID), rows))
	return nil
}

// formatSummaryDuration rounds wall-clock generation time to a human-friendly value.
func formatSummaryDuration(d time.Duration) string {
	return d.Round(100 * time.Millisecond).String()
}

func maybeCompactExternalTranscriptForSummary(ctx context.Context, scopedTranscript []byte, agentType types.AgentType) []byte {
	if transcriptHasSummaryContent(scopedTranscript, agentType) {
		return scopedTranscript
	}

	ag, err := agent.GetByAgentType(agentType)
	if err != nil {
		external.DiscoverAndRegister(ctx)
		ag, err = agent.GetByAgentType(agentType)
	}
	if err != nil || !external.IsExternal(ag) {
		return scopedTranscript
	}

	compactor, ok := agent.AsTranscriptCompactor(ag)
	if !ok {
		return scopedTranscript
	}

	tmpFile, err := os.CreateTemp("", "trace-summary-transcript-*.jsonl")
	if err != nil {
		logging.Debug(ctx, "external summary compaction unavailable",
			slog.String("agent", string(agentType)),
			slog.String("error", err.Error()))
		return scopedTranscript
	}
	tmpPath := tmpFile.Name()
	defer func() {
		if removeErr := os.Remove(tmpPath); removeErr != nil {
			logging.Debug(ctx, "failed to remove temporary summary transcript",
				slog.String("path", tmpPath),
				slog.String("error", removeErr.Error()))
		}
	}()

	if _, err := tmpFile.Write(scopedTranscript); err != nil {
		_ = tmpFile.Close()
		logging.Debug(ctx, "external summary compaction transcript write failed",
			slog.String("agent", string(agentType)),
			slog.String("error", err.Error()))
		return scopedTranscript
	}
	if err := tmpFile.Close(); err != nil {
		logging.Debug(ctx, "external summary compaction transcript close failed",
			slog.String("agent", string(agentType)),
			slog.String("error", err.Error()))
		return scopedTranscript
	}

	compacted, err := compactor.CompactTranscript(ctx, tmpPath)
	if err != nil || compacted == nil || len(compacted.Transcript) == 0 {
		if err != nil {
			logging.Debug(ctx, "external summary compaction failed",
				slog.String("agent", string(agentType)),
				slog.String("error", err.Error()))
		}
		return scopedTranscript
	}

	redacted, err := redact.JSONLBytes(compacted.Transcript)
	if err != nil {
		logging.Debug(ctx, "external summary compaction redaction failed",
			slog.String("agent", string(agentType)),
			slog.String("error", err.Error()))
		return scopedTranscript
	}
	redactedTranscript := redacted.Bytes()
	if !transcriptHasSummaryContent(redactedTranscript, agentType) {
		return scopedTranscript
	}

	logging.Debug(ctx, "using external compact transcript for summary generation",
		slog.String("agent", string(agentType)))
	return redactedTranscript
}

func transcriptHasSummaryContent(transcriptBytes []byte, agentType types.AgentType) bool {
	entries, err := summarize.BuildCondensedTranscriptFromBytes(redact.AlreadyRedacted(transcriptBytes), agentType)
	return err == nil && len(entries) > 0
}

// generateCheckpointAISummary returns the generated summary, the effective
// deadline applied to the underlying call (which may be shorter than
// checkpointSummaryTimeout if the parent context had an earlier deadline),
// and any error. The effective deadline is returned so the caller can render
// the true timeout value in user-facing error messages instead of always
// showing the package default.
func generateCheckpointAISummary(ctx context.Context, scopedTranscript []byte, filesTouched []string, agentType types.AgentType, generator summarize.Generator) (*checkpoint.Summary, time.Duration, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, checkpointSummaryTimeout)
	timeoutDuration := checkpointSummaryTimeout
	if deadline, ok := timeoutCtx.Deadline(); ok {
		timeoutDuration = time.Until(deadline)
	}
	defer cancel()

	// scopedTranscript is either read from checkpoint storage (redacted on
	// write) or replaced by external compact output redacted before use.
	summary, err := generateTranscriptSummary(timeoutCtx, redact.AlreadyRedacted(scopedTranscript), filesTouched, agentType, generator)
	if err != nil {
		// Only classify as ctx cancel/deadline when the error chain actually
		// contains the sentinel. Relying on timeoutCtx.Err() here loses typed
		// errors (e.g. *ClaudeError) when the subprocess returned a real
		// structured failure while timeoutCtx.Err() is non-nil for any reason
		// (parent cancelled, deadline already elapsed, etc.).
		if errors.Is(err, context.Canceled) {
			return nil, timeoutDuration, fmt.Errorf("summary generation canceled: %w", err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, timeoutDuration, fmt.Errorf("summary generation timed out after %s: %w", formatSummaryTimeout(timeoutDuration), err)
		}
		return nil, timeoutDuration, err
	}

	return summary, timeoutDuration, nil
}

// formatCheckpointSummaryError maps typed Claude CLI errors and context
// sentinels to a structured failure block: a user-visible label, supporting
// rows, and a structured error suitable for wrapping in NewSilentError.
//
// The styled rendering happens in the caller (generateCheckpointSummary), which
// renders to errW via newStatusStyles(...).renderFailure(label, rows). This
// split keeps the formatting policy in one place (the failure block) while
// letting the caller still return a *SilentError for main.go's exit handling.
func formatCheckpointSummaryError(err error, deadline time.Duration) (string, []explainRow, error) {
	var claudeErr *claudecode.ClaudeError
	switch {
	case errors.As(err, &claudeErr):
		switch claudeErr.Kind { //nolint:exhaustive // ClaudeErrorUnknown handled by default
		case claudecode.ClaudeErrorAuth:
			label := "Claude authentication failed"
			rows := []explainRow{
				{Label: "try", Value: "run `claude login` and retry"},
			}
			if claudeErr.Message != "" {
				rows = append([]explainRow{{Label: "message", Value: claudeErr.Message}}, rows...)
			}
			//nolint:staticcheck // ST1005: Claude is a proper noun
			//lint:ignore ST1005 // Claude is a proper noun
			return label, rows, fmt.Errorf("Claude authentication failed%s", formatMessageSuffix(claudeErr.Message))
		case claudecode.ClaudeErrorRateLimit:
			label := "Claude rejected the summary request due to rate limits or quota"
			rows := []explainRow{
				{Label: "try", Value: "wait and retry"},
			}
			if claudeErr.Message != "" {
				rows = append([]explainRow{{Label: "message", Value: claudeErr.Message}}, rows...)
			}
			//nolint:staticcheck // ST1005: Claude is a proper noun
			//lint:ignore ST1005 // Claude is a proper noun
			return label, rows, fmt.Errorf("Claude rejected the summary request due to rate limits or quota%s", formatMessageSuffix(claudeErr.Message))
		case claudecode.ClaudeErrorConfig:
			label := "Claude rejected the summary request"
			rows := []explainRow{
				{Label: "try", Value: "check your Claude CLI config and selected model"},
			}
			if claudeErr.Message != "" {
				rows = append([]explainRow{{Label: "message", Value: claudeErr.Message}}, rows...)
			}
			//nolint:staticcheck // ST1005: Claude is a proper noun
			//lint:ignore ST1005 // Claude is a proper noun
			return label, rows, fmt.Errorf("Claude rejected the summary request%s", formatMessageSuffix(claudeErr.Message))
		case claudecode.ClaudeErrorCLIMissing:
			label := "Claude CLI is not installed or not on PATH"
			//nolint:staticcheck // ST1005: Claude is a proper noun
			//lint:ignore ST1005 // Claude is a proper noun
			return label, nil, errors.New("Claude CLI is not installed or not on PATH")
		default:
			label := "Claude failed to generate the summary"
			suffix := formatClaudeErrorSuffix(claudeErr)
			rows := []explainRow{
				{Label: "detail", Value: strings.TrimPrefix(strings.TrimPrefix(suffix, ": "), " ")},
			}
			//nolint:staticcheck // ST1005: Claude is a proper noun
			//lint:ignore ST1005 // Claude is a proper noun
			return label, rows, fmt.Errorf("Claude failed to generate the summary%s", suffix)
		}
	case errors.Is(err, context.DeadlineExceeded):
		// Deliberately provider-neutral: explain --generate supports multiple
		// summary providers (claude-code, codex, gemini, ...), so hardcoding
		// "Claude" / "sonnet" / "Anthropic" here would misdirect users who
		// selected a different provider in .trace/settings.json.
		label := "Summary generation timed out after " + formatSummaryTimeout(deadline)
		rows := []explainRow{
			{Label: "causes", Value: ""},
			{Label: "", Value: "• the selected model is taking longer than expected on a large transcript"},
			{Label: "", Value: "• the summary provider's CLI cannot reach its API (network, VPN, firewall)"},
			{Label: "", Value: "• the provider's API is degraded"},
			{Label: "try", Value: "run the provider CLI directly to confirm it works"},
		}
		return label, rows, fmt.Errorf("summary generation did not return within the %s safety deadline", formatSummaryTimeout(deadline))
	case errors.Is(err, context.Canceled):
		return "Summary generation canceled", nil, errors.New("summary generation canceled")
	default:
		return "Failed to generate summary", []explainRow{{Label: "detail", Value: err.Error()}}, fmt.Errorf("failed to generate summary: %w", err)
	}
}

// formatMessageSuffix formats ": <msg>" when msg is non-empty and "" otherwise.
// Used by the Auth / RateLimit / Config branches of formatCheckpointSummaryError
// to avoid rendering a bare colon when ClaudeError.Message is empty (reachable
// when the CLI envelope is is_error:true with result:null but a real status).
func formatMessageSuffix(msg string) string {
	if msg == "" {
		return ""
	}
	return ": " + msg
}

// formatClaudeErrorSuffix builds a diagnostic suffix for user-facing output
// when we fall through to the default "failed to generate the summary" path.
// Prefers the envelope Message, falls back to HTTP status, then exit code,
// so the user never sees a bare "Claude failed to generate the summary:"
// with nothing after the colon (which happens when Claude returns
// is_error:true with result:null, or when the subprocess crashes with no
// stderr output). ExitCode < 0 means the subprocess did not produce a real
// exit code (e.g. launch failure) — render that as "abnormal termination"
// rather than the misleading "exited with code -1".
func formatClaudeErrorSuffix(e *claudecode.ClaudeError) string {
	if e.Message != "" {
		return ": " + e.Message
	}
	switch {
	case e.APIStatus != 0:
		return fmt.Sprintf(" (Anthropic API returned HTTP %d)", e.APIStatus)
	case e.ExitCode > 0:
		return fmt.Sprintf(" (claude CLI exited with code %d)", e.ExitCode)
	case e.ExitCode < 0:
		return " (claude CLI terminated abnormally — no exit code captured)"
	default:
		return " (no diagnostic detail available from Claude CLI)"
	}
}

func formatSummaryTimeout(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return d.Round(10 * time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

// explainTemporaryCheckpoint finds and formats a temporary checkpoint by shadow commit hash prefix.
// Returns the formatted output, whether the checkpoint was found, and an
// optional error. When err is non-nil, the function has already rendered a
// styled failure block to errW; the caller should wrap and return as
// SilentError without printing again.
// Searches ALL shadow branches, not just the one for current HEAD, to find checkpoints
// created from different base commits (e.g., if HEAD advanced since session start).
// The writer w is used for raw transcript output to bypass the pager.
func explainTemporaryCheckpoint(ctx context.Context, w, errW io.Writer, repo *git.Repository, store *checkpoint.GitStore, shaPrefix string, verbose, full, rawTranscript bool) (string, bool, error) {
	// List temporary checkpoints from ALL shadow branches
	// This ensures we find checkpoints even if HEAD has advanced since the session started
	tempCheckpoints, err := store.ListAllTemporaryCheckpoints(ctx, "", branchCheckpointsLimit)
	if err != nil {
		return "", false, nil //nolint:nilerr // best-effort: shadow-branch listing failure is reported as found=false; caller then falls back to ErrCheckpointNotFound with a user-facing hint instead of a raw git error
	}

	// Find checkpoints matching the SHA prefix - check for ambiguity
	var matches []checkpoint.TemporaryCheckpointInfo
	for _, tc := range tempCheckpoints {
		if strings.HasPrefix(tc.CommitHash.String(), shaPrefix) {
			matches = append(matches, tc)
		}
	}

	if len(matches) == 0 {
		return "", false, nil
	}

	if len(matches) > 1 {
		// Multiple matches: render styled failure block, return SilentError.
		ambiguous := make([]ambiguousMatch, 0, len(matches))
		for _, m := range matches {
			shortID := m.CommitHash.String()
			if len(shortID) > 7 {
				shortID = shortID[:7]
			}
			ambiguous = append(ambiguous, ambiguousMatch{
				ShortID:   shortID,
				Timestamp: m.Timestamp,
				SessionID: m.SessionID,
			})
		}
		renderAmbiguousPrefixFailure(errW, shaPrefix, "temporary checkpoints", ambiguous)
		return "", false, NewSilentError(fmt.Errorf("%w: %s matches %d temporary checkpoints", errAmbiguousCommitPrefix, shaPrefix, len(matches)))
	}

	tc := matches[0]

	// Get shadow commit and tree to read metadata
	shadowCommit, commitErr := repo.CommitObject(tc.CommitHash)
	if commitErr != nil {
		return "", false, nil //nolint:nilerr // best-effort: shadow commit may have been GC'd or pruned; treat as not-found so the caller reports ErrCheckpointNotFound rather than an internal git error
	}

	shadowTree, treeErr := shadowCommit.Tree()
	if treeErr != nil {
		return "", false, nil //nolint:nilerr // best-effort: a shadow commit without a readable tree is corrupt/partial; treat as not-found so the caller reports ErrCheckpointNotFound rather than an internal git error
	}

	// Read agent type from shadow branch metadata (stored during checkpoint creation)
	agentType := strategy.ReadAgentTypeFromTree(shadowTree, tc.MetadataDir)

	// Handle raw transcript output
	if rawTranscript {
		transcriptBytes, transcriptErr := store.GetTranscriptFromCommit(ctx, tc.CommitHash, tc.MetadataDir, agentType)
		if transcriptErr != nil || len(transcriptBytes) == 0 {
			shortID := tc.CommitHash.String()[:7]
			return "", false, renderExplainFailure(errW, "Checkpoint has no transcript", []explainRow{
				{Label: "id", Value: shortID},
			}, fmt.Errorf("checkpoint %s has no transcript", shortID))
		}
		// Write directly to writer (no pager, no formatting) - matches committed checkpoint behavior
		if _, writeErr := fmt.Fprint(w, string(transcriptBytes)); writeErr != nil {
			return "", false, fmt.Errorf("failed to write transcript: %w", writeErr)
		}
		return "", true, nil
	}

	// Read prompts from shadow branch
	sessionPrompt := strategy.ReadSessionPromptFromTree(shadowTree, tc.MetadataDir)

	// Build output similar to formatCheckpointOutput but for temporary
	var sb strings.Builder
	shortID := tc.CommitHash.String()[:7]
	styles := newStatusStyles(w)

	label := fmt.Sprintf("Checkpoint %s [temporary]", shortID)
	rows := []explainRow{
		{Label: "session", Value: tc.SessionID},
		{Label: "created", Value: tc.Timestamp.Format("2006-01-02 15:04:05")},
	}
	sb.WriteString(styles.renderIdentity(label, "", rows))

	intent := extractIntent(nil, sessionPrompt)
	hint := "Not generated. Temporary checkpoints can be summarized after commit. Run `trace explain --generate` on the resulting commit."
	sb.WriteString(renderExplainBody(w, buildNoSummaryMarkdown(intent, nil, hint)))

	// Transcript section: full shows trace session, verbose shows checkpoint scope
	// For temporary checkpoints, load transcript and compute scope from parent commit
	var fullTranscript []byte
	var scopedTranscript []byte
	if full || verbose {
		fullTranscript, _ = store.GetTranscriptFromCommit(ctx, tc.CommitHash, tc.MetadataDir, agentType) //nolint:errcheck // Best-effort

		if verbose && len(fullTranscript) > 0 {
			// Compute scoped transcript by finding where parent's transcript ended
			// Each shadow branch commit has the full transcript up to that point,
			// so we diff against parent to get just this checkpoint's activity
			scopedTranscript = fullTranscript // Default to full if no parent
			if shadowCommit.NumParents() > 0 {
				if parent, parentErr := shadowCommit.Parent(0); parentErr == nil {
					parentTranscript, _ := store.GetTranscriptFromCommit(ctx, parent.Hash, tc.MetadataDir, agentType) //nolint:errcheck // Best-effort
					if len(parentTranscript) > 0 {
						parentOffset := transcriptOffset(parentTranscript, agentType)
						scopedTranscript = scopeTranscriptForCheckpoint(fullTranscript, parentOffset, agentType)
					}
				}
			}
		}
	}
	if verbose || full {
		label := "Transcript (checkpoint scope)"
		if full {
			label = "Transcript (full session)"
		}
		sb.WriteString("\n")
		sb.WriteString(styles.sectionRule(label, styles.width))
		sb.WriteString("\n")
	}
	appendTranscriptSection(&sb, verbose, full, fullTranscript, scopedTranscript, sessionPrompt, agentType)

	return sb.String(), true, nil
}

// getAssociatedCommits finds git commits that reference the given checkpoint ID.
// Searches commits on the current branch for Trace-Checkpoint trailer matches.
// When searchAll is true, uses full DAG walk with no depth limit (may be slow).
// This finds checkpoint commits on merged feature branches (second parents of merges).
func getAssociatedCommits(ctx context.Context, repo *git.Repository, checkpointID id.CheckpointID, searchAll bool) ([]associatedCommit, error) {
	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}

	commits := []associatedCommit{} // Initialize as empty slice, not nil (nil means "not searched")
	targetID := checkpointID.String()

	collectCommit := func(c *object.Commit) {
		fullSHA := c.Hash.String()
		shortSHA := fullSHA
		if len(fullSHA) >= 7 {
			shortSHA = fullSHA[:7]
		}
		commits = append(commits, associatedCommit{
			SHA:      fullSHA,
			ShortSHA: shortSHA,
			Message:  strings.Split(c.Message, "\n")[0],
			Author:   c.Author.Name,
			Email:    c.Author.Email,
			Date:     c.Author.When,
		})
	}

	if searchAll {
		// Full DAG walk: follows all parents of merge commits, no depth limit.
		// This finds checkpoint commits on merged feature branches.
		iter, iterErr := repo.Log(&git.LogOptions{
			From:  head.Hash(),
			Order: git.LogOrderCommitterTime,
		})
		if iterErr != nil {
			return nil, fmt.Errorf("failed to get commit log: %w", iterErr)
		}
		defer iter.Close()

		err = iter.ForEach(func(c *object.Commit) error {
			if err := ctx.Err(); err != nil {
				return err //nolint:wrapcheck // Propagating context cancellation
			}
			cpID, found := trailers.ParseCheckpoint(c.Message)
			if found && cpID.String() == targetID {
				collectCommit(c)
			}
			return nil
		})
	} else {
		// First-parent walk with depth limit and branch filtering.
		// Avoids walking into main's history through merge commit parents.
		reachableFromMain := computeReachableFromMain(ctx, repo)

		err = walkFirstParentCommits(ctx, repo, head.Hash(), commitScanLimit, func(c *object.Commit) error {
			// Once we hit a commit reachable from main on the first-parent chain,
			// all earlier ancestors are also shared-with-main, so stop scanning.
			if reachableFromMain[c.Hash] {
				return errStopIteration
			}

			cpID, found := trailers.ParseCheckpoint(c.Message)
			if found && cpID.String() == targetID {
				collectCommit(c)
			}
			return nil
		})
	}

	if err != nil {
		return nil, fmt.Errorf("error iterating commits: %w", err)
	}

	return commits, nil
}

// scopeTranscriptForCheckpoint slices a transcript to include only the portion
// relevant to a specific checkpoint, starting from the given offset.
// For Claude Code (JSONL), the offset is a line number and we slice by line.
// For Gemini (single JSON blob), the offset is a message index and we slice by message.
func scopeTranscriptForCheckpoint(fullTranscript []byte, startOffset int, agentType types.AgentType) []byte {
	switch agentType {
	case agent.AgentTypeGemini:
		scoped, err := geminicli.SliceFromMessage(fullTranscript, startOffset)
		if err != nil {
			return nil
		}
		return scoped
	case agent.AgentTypeOpenCode:
		scoped, err := opencode.SliceFromMessage(fullTranscript, startOffset)
		if err != nil {
			return nil
		}
		return scoped
	case agent.AgentTypeCodex, agent.AgentTypeClaudeCode, agent.AgentTypeCursor, agent.AgentTypeFactoryAIDroid, agent.AgentTypeUnknown:
		return transcript.SliceFromLine(fullTranscript, startOffset)
	}
	return transcript.SliceFromLine(fullTranscript, startOffset)
}

// extractPromptsFromTranscript extracts user prompts from transcript bytes.
// Returns a slice of prompt strings.
func extractPromptsFromTranscript(transcriptBytes []byte, agentType types.AgentType) []string {
	if len(transcriptBytes) == 0 {
		return nil
	}

	// transcriptBytes is read from checkpoint storage, which redacts on write.
	condensed, err := summarize.BuildCondensedTranscriptFromBytes(redact.AlreadyRedacted(transcriptBytes), agentType)
	if err != nil || len(condensed) == 0 {
		condensed, err = buildCondensedCompactTranscriptEntries(transcriptBytes)
	}
	if err != nil || len(condensed) == 0 {
		return nil
	}

	var prompts []string
	for _, entry := range condensed {
		if entry.Type == summarize.EntryTypeUser && entry.Content != "" {
			prompts = append(prompts, entry.Content)
		}
	}
	return prompts
}

// extractIntent picks the user-facing intent line from available prompt sources.
// Preference: first non-empty entry of scopedPrompts, then first non-empty line
// of fallbackPrompts, then "". Truncates to maxIntentDisplayLength.
func extractIntent(scopedPrompts []string, fallbackPrompts string) string {
	for _, p := range scopedPrompts {
		if p == "" {
			continue
		}
		return strategy.TruncateDescription(p, maxIntentDisplayLength)
	}
	for _, line := range strings.Split(fallbackPrompts, "\n") {
		if line == "" {
			continue
		}
		return strategy.TruncateDescription(line, maxIntentDisplayLength)
	}
	return ""
}

// buildNoSummaryMarkdown renders the body for a checkpoint that does not yet
// have an AI summary. It mirrors the `## Intent` / `## Summary` / `## Files`
// shape of the generated case so the brand markdown renderer can take the same
// path. The italic *summary* paragraph is the affordance pointing the user at
// `--generate` (or, for temporary checkpoints, at committing first).
func buildNoSummaryMarkdown(intent string, files []string, summaryHint string) string {
	var sb strings.Builder

	sb.WriteString("## Intent\n\n")
	if intent == "" {
		sb.WriteString("*(no prompt recorded)*\n\n")
	} else {
		fmt.Fprintf(&sb, "%s\n\n", escapeSummaryText(intent))
	}

	fmt.Fprintf(&sb, "## Summary\n\n*%s*\n", escapeSummaryText(summaryHint))

	if len(files) > 0 {
		fmt.Fprintf(&sb, "\n## Files (%d)\n\n", len(files))
		for _, f := range files {
			fmt.Fprintf(&sb, "- `%s`\n", escapeInlineCodeText(f))
		}
	}

	return sb.String()
}

// ambiguousMatch describes one match in an ambiguous-prefix failure.
// SessionID is optional and only set for temporary-checkpoint matches.
type ambiguousMatch struct {
	ShortID   string
	Timestamp time.Time
	SessionID string
}
