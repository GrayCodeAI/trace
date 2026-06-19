package strategy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/external"
	"github.com/GrayCodeAI/trace/cli/agent/geminicli"
	"github.com/GrayCodeAI/trace/cli/agent/opencode"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	cpkg "github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cli/summarize"
	"github.com/GrayCodeAI/trace/cli/transcript"
	"github.com/GrayCodeAI/trace/perf"
	"github.com/GrayCodeAI/trace/redact"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

var (
	discoverExternalSummaryProviders = external.DiscoverAndRegister
	isSummaryProviderCLIAvailable    = agent.IsSummaryCLIAvailable
)

// listCheckpoints returns all checkpoints from the metadata branch.
// Uses checkpoint.GitStore.ListCommitted() for reading from trace/checkpoints/v1.
func (s *ManualCommitStrategy) listCheckpoints(ctx context.Context) ([]CheckpointInfo, error) {
	store, err := s.getCheckpointStore()
	if err != nil {
		return nil, fmt.Errorf("failed to get checkpoint store: %w", err)
	}

	committed, err := store.ListCommitted(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list committed checkpoints: %w", err)
	}

	// Convert from checkpoint.CommittedInfo to strategy.CheckpointInfo
	result := make([]CheckpointInfo, 0, len(committed))
	for _, c := range committed {
		result = append(result, CheckpointInfo{
			CheckpointID:     c.CheckpointID,
			SessionID:        c.SessionID,
			CreatedAt:        c.CreatedAt,
			CheckpointsCount: c.CheckpointsCount,
			FilesTouched:     c.FilesTouched,
			Agent:            c.Agent,
			IsTask:           c.IsTask,
			ToolUseID:        c.ToolUseID,
			SessionCount:     c.SessionCount,
			SessionIDs:       c.SessionIDs,
		})
	}

	return result, nil
}

// getCheckpointLog returns the transcript for a specific checkpoint ID.
// Uses checkpoint.GitStore.ReadCommitted() for reading from trace/checkpoints/v1.
func (s *ManualCommitStrategy) getCheckpointLog(ctx context.Context, checkpointID id.CheckpointID) ([]byte, error) {
	store, err := s.getCheckpointStore()
	if err != nil {
		return nil, fmt.Errorf("failed to get checkpoint store: %w", err)
	}

	content, err := store.ReadLatestSessionContent(ctx, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("failed to read checkpoint: %w", err)
	}
	if content == nil {
		return nil, fmt.Errorf("checkpoint not found: %s", checkpointID)
	}
	if len(content.Transcript) == 0 {
		return nil, fmt.Errorf("no transcript found for checkpoint: %s", checkpointID)
	}

	return content.Transcript, nil
}

// condenseOpts provides pre-resolved git objects to avoid redundant reads.
type condenseOpts struct {
	shadowRef        *plumbing.Reference // Pre-resolved shadow branch ref (nil = resolve from repo)
	headTree         *object.Tree        // Pre-resolved HEAD tree (passed through to calculateSessionAttributions)
	parentTree       *object.Tree        // Pre-resolved parent tree (nil for initial commits, for consistent non-agent line counting)
	repoDir          string              // Repository worktree path for git CLI commands
	parentCommitHash string              // HEAD's first parent hash for per-commit non-agent file detection
	headCommitHash   string              // HEAD commit hash (passed through for attribution)
	allAgentFiles    map[string]struct{} // Union of all sessions' FilesTouched for cross-session exclusion (nil = single-session)
}

var redactSessionJSONLBytes = redact.JSONLBytes

// CondenseSession condenses a session's shadow branch to permanent storage.
// checkpointID is the 12-hex-char value from the Trace-Checkpoint trailer.
// Metadata is stored at sharded path: <checkpoint_id[:2]>/<checkpoint_id[2:]>/
// Uses checkpoint.GitStore.WriteCommitted for the git operations.
//
// For mid-session commits (no Stop/SaveStep called yet), the shadow branch may not exist.
// In this case, data is extracted from the live transcript instead.
func (s *ManualCommitStrategy) CondenseSession(ctx context.Context, repo *git.Repository, checkpointID id.CheckpointID, state *SessionState, committedFiles map[string]struct{}, opts ...condenseOpts) (*CondenseResult, error) {
	ag, _ := agent.GetByAgentType(state.AgentType) //nolint:errcheck // ag may be nil for unknown agent types; callers use type assertions so nil is safe
	var o condenseOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	logCtx := logging.WithComponent(ctx, "checkpoint")
	condenseStart := time.Now()

	shadowBranchName := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)
	ref, hasShadowBranch := resolveShadowRef(repo, shadowBranchName, o.shadowRef)

	// Re-resolve transcript path before any reads — handles agents that relocate
	// transcripts mid-session (e.g., Cursor CLI flat → nested layout change).
	// Errors are ignored; downstream readers handle missing transcripts gracefully.
	resolveTranscriptPath(state) //nolint:errcheck,gosec // best-effort; downstream readers handle missing files

	extractStart := time.Now()
	_, extractSessionDataSpan := perf.Start(ctx, "extract_session_data")
	var shadowHash plumbing.Hash
	if hasShadowBranch {
		shadowHash = ref.Hash()
	}
	sessionData, extractErr := s.extractOrCreateSessionData(ctx, repo, ag, shadowHash, hasShadowBranch, state)
	if extractErr != nil {
		extractSessionDataSpan.RecordError(extractErr)
		extractSessionDataSpan.End()
		return nil, extractErr
	}
	extractSessionDataSpan.End()
	extractDuration := time.Since(extractStart)

	// Backfill session state token usage from the freshly-extracted transcript.
	// Copilot CLI writes session.shutdown after the hooks return, so by condensation
	// time we can recover the authoritative full-session total from the transcript
	// while keeping checkpoint metadata scoped to CheckpointTranscriptStart.
	if backfillUsage := sessionStateBackfillTokenUsage(ctx, ag, state.AgentType, sessionData.Transcript, sessionData.TokenUsage); backfillUsage != nil {
		state.TokenUsage = backfillUsage
	}

	// Skip gate: if there is no transcript AND no files touched, there is nothing
	// meaningful to condense. Return early to avoid writing metadata-only stubs.
	//
	// This check MUST run before filterFilesTouched. That function's fallback
	// assigns all committed files to sessions with empty FilesTouched (designed
	// for mid-turn commits where SaveStep hasn't run yet). Without this ordering,
	// genuinely empty sessions (no transcript, no shadow branch, no tracked files)
	// would acquire committed files from the fallback and bypass this gate.
	if len(sessionData.Transcript) == 0 && len(sessionData.FilesTouched) == 0 {
		logging.Info(
			logCtx, "session skipped: no transcript or files to condense",
			slog.String("session_id", state.SessionID),
			slog.String("agent_type", string(state.AgentType)),
			slog.String("checkpoint_id", checkpointID.String()),
			slog.Bool("has_shadow_branch", hasShadowBranch),
			slog.String("transcript_path", state.TranscriptPath),
		)
		return newSkippedResult(checkpointID, state.SessionID), nil
	}

	filterFilesTouched(sessionData, committedFiles, state)

	redactedTranscript, redactDuration := redactOrDrop(logCtx, sessionData.Transcript, state.SessionID, checkpointID)
	if skipped := skipIfPostRedactionEmpty(logCtx, redactedTranscript, sessionData, state, checkpointID); skipped != nil {
		return skipped, nil
	}

	// Get checkpoint store
	store, err := s.getCheckpointStore()
	if err != nil {
		return nil, fmt.Errorf("failed to get checkpoint store: %w", err)
	}

	// Get author info
	authorName, authorEmail := GetGitAuthorFromRepo(repo)

	// Determine attribution base commit
	attrBase := state.AttributionBaseCommit
	if attrBase == "" {
		attrBase = state.BaseCommit
	}

	attributionStart := time.Now()
	attrCtx, attributionSpan := perf.Start(ctx, "calculate_session_attribution")
	attribution := calculateSessionAttributions(attrCtx, repo, ref, sessionData, state, attributionOpts{
		headTree:              o.headTree,
		parentTree:            o.parentTree,
		repoDir:               o.repoDir,
		attributionBaseCommit: attrBase,
		parentCommitHash:      o.parentCommitHash,
		headCommitHash:        o.headCommitHash,
		allAgentFiles:         o.allAgentFiles,
	})
	attributionSpan.End()
	attributionDuration := time.Since(attributionStart)

	// Get current branch name
	branchName := GetCurrentBranchName(repo)

	var summary *cpkg.Summary
	if settings.IsSummarizeEnabled(ctx) && redactedTranscript.Len() > 0 {
		summary = generateSummary(ctx, redactedTranscript, sessionData.FilesTouched, state)
	}

	// Build write options (shared by v1 and v2)
	writeOpts := cpkg.WriteCommittedOptions{
		CheckpointID:                checkpointID,
		SessionID:                   state.SessionID,
		Strategy:                    StrategyNameManualCommit,
		Branch:                      branchName,
		Transcript:                  redactedTranscript,
		Prompts:                     sessionData.Prompts,
		FilesTouched:                sessionData.FilesTouched,
		CheckpointsCount:            state.StepCount,
		EphemeralBranch:             shadowBranchName,
		AuthorName:                  authorName,
		AuthorEmail:                 authorEmail,
		Agent:                       state.AgentType,
		Model:                       state.ModelName,
		TurnID:                      state.TurnID,
		TranscriptIdentifierAtStart: state.TranscriptIdentifierAtStart,
		CheckpointTranscriptStart:   state.CheckpointTranscriptStart,
		TokenUsage:                  sessionData.TokenUsage,
		SessionMetrics:              buildSessionMetrics(state),
		InitialAttribution:          attribution,
		PromptAttributionsJSON:      marshalPromptAttributionsIncludingPending(state),
		Summary:                     summary,
	}

	compactResult := buildExternalCompactTranscript(ctx, ag, state)
	if compactResult == nil {
		internalResult := buildInternalCompactTranscript(ctx, ag, redactedTranscript, state)
		compactResult = &internalResult
	}
	writeOpts.CompactTranscript = compactResult.Transcript
	writeOpts.CompactTranscriptStart = compactResult.StartLine

	v2 := settings.CheckpointsVersion(ctx) == 2

	// Write checkpoint metadata to the primary store.
	writeV1Start := time.Now()
	writeCtx, writeCommittedSpan := perf.Start(ctx, "write_committed_v1")
	if !v2 {
		if err := store.WriteCommitted(writeCtx, writeOpts); err != nil {
			writeCommittedSpan.RecordError(err)
			writeCommittedSpan.End()
			return nil, fmt.Errorf("failed to write checkpoint metadata: %w", err)
		}
	}
	writeCommittedSpan.End()
	writeV1Duration := time.Since(writeV1Start)

	writeV2Start := time.Now()
	writeV2Ctx, writeCommittedV2Span := perf.Start(ctx, "write_committed_v2")
	if v2 {
		if err := writeCommittedV2(writeV2Ctx, repo, writeOpts); err != nil {
			writeCommittedV2Span.RecordError(err)
			writeCommittedV2Span.End()
			return nil, fmt.Errorf("failed to write checkpoint metadata to v2: %w", err)
		}
	} else {
		writeCommittedV2IfEnabled(writeV2Ctx, repo, writeOpts)
	}
	writeTaskMetadataV2IfEnabled(writeV2Ctx, repo, checkpointID, state.SessionID, ref)
	writeCommittedV2Span.End()
	writeV2Duration := time.Since(writeV2Start)

	logging.Debug(
		logCtx, "condense timings",
		slog.String("session_id", state.SessionID),
		slog.String("checkpoint_id", checkpointID.String()),
		slog.Int64("extract_session_data_ms", extractDuration.Milliseconds()),
		slog.Int64("calculate_session_attribution_ms", attributionDuration.Milliseconds()),
		slog.Int64("redact_transcript_ms", redactDuration.Milliseconds()),
		slog.Int64("compact_transcript_v2_ms", compactResult.Duration.Milliseconds()),
		slog.Int64("write_committed_v1_ms", writeV1Duration.Milliseconds()),
		slog.Int64("write_committed_v2_ms", writeV2Duration.Milliseconds()),
		slog.Int64("total_ms", time.Since(condenseStart).Milliseconds()),
		slog.Int("transcript_bytes", len(sessionData.Transcript)),
		slog.Int("transcript_lines", sessionData.FullTranscriptLines),
	)

	// Count scoped (new-only) compact lines, not full compact lines,
	// so state.CompactTranscriptStart accumulates correctly.
	compactLines := 0
	if compactResult.Transcript != nil {
		fullLines := countCompactLines(compactResult.Transcript)
		compactLines = fullLines - compactResult.StartLine
	}

	return &CondenseResult{
		CheckpointID:           checkpointID,
		SessionID:              state.SessionID,
		CheckpointsCount:       state.StepCount,
		FilesTouched:           sessionData.FilesTouched,
		Prompts:                sessionData.Prompts,
		TotalTranscriptLines:   sessionData.FullTranscriptLines,
		CompactTranscriptLines: compactLines,
		Transcript:             sessionData.Transcript,
	}, nil
}

// redactOrDrop runs redactSessionTranscript and, on failure, logs a warning
// and returns empty bytes. Drop-on-failure is the long-standing contract here:
// hooks have no retry path, and a failed redaction must not block the commit.
func redactOrDrop(logCtx context.Context, transcript []byte, sessionID string, checkpointID id.CheckpointID) (redact.RedactedBytes, time.Duration) {
	redactedTranscript, redactDuration, err := redactSessionTranscript(logCtx, transcript)
	if err != nil {
		logging.Warn(
			logCtx, "failed to redact transcript secrets, dropping transcript for checkpoint",
			slog.String("session_id", sessionID),
			slog.String("checkpoint_id", checkpointID.String()),
			slog.String("error", err.Error()),
		)
		return redact.RedactedBytes{}, redactDuration
	}
	return redactedTranscript, redactDuration
}

// skipIfPostRedactionEmpty returns a Skipped result when redaction emptied the
// transcript AND the filtered FilesTouched is also empty. Without this, a
// session that passed the pre-redaction gate but got its transcript dropped by
// a malformed-JSONL redaction error would write a metadata-only stub.
func skipIfPostRedactionEmpty(logCtx context.Context, redactedTranscript redact.RedactedBytes, sessionData *ExtractedSessionData, state *SessionState, checkpointID id.CheckpointID) *CondenseResult {
	if redactedTranscript.Len() > 0 || len(sessionData.FilesTouched) > 0 {
		return nil
	}
	logging.Info(
		logCtx, "session skipped: nothing to persist after redaction",
		slog.String("session_id", state.SessionID),
		slog.String("agent_type", string(state.AgentType)),
		slog.String("checkpoint_id", checkpointID.String()),
	)
	return newSkippedResult(checkpointID, state.SessionID)
}

func newSkippedResult(checkpointID id.CheckpointID, sessionID string) *CondenseResult {
	return &CondenseResult{
		CheckpointID: checkpointID,
		SessionID:    sessionID,
		Skipped:      true,
	}
}

// redactSessionTranscript redacts the transcript once for use by both the compact
// package and the checkpoint stores. Returns the redacted bytes and the duration
// of the redaction operation for perf logging.
func redactSessionTranscript(ctx context.Context, transcript []byte) (redact.RedactedBytes, time.Duration, error) {
	start := time.Now()
	_, span := perf.Start(ctx, "redact_transcript")
	defer span.End()

	if len(transcript) == 0 {
		return redact.RedactedBytes{}, time.Since(start), nil
	}

	redacted, err := redactSessionJSONLBytes(transcript)
	if err != nil {
		span.RecordError(err)
		return redact.RedactedBytes{}, time.Since(start), fmt.Errorf("failed to redact transcript secrets: %w", err)
	}
	return redacted, time.Since(start), nil
}

// resolveShadowRef returns the shadow branch reference, preferring a pre-resolved
// ref when available and falling back to a repo lookup.
func resolveShadowRef(repo *git.Repository, branchName string, preResolved *plumbing.Reference) (ref *plumbing.Reference, exists bool) {
	if preResolved != nil {
		return preResolved, true
	}
	refName := plumbing.NewBranchReferenceName(branchName)
	resolved, err := repo.Reference(refName, true)
	if err != nil {
		return nil, false
	}
	return resolved, true
}

// filterFilesTouched narrows sessionData.FilesTouched to files present in
// committedFiles. When no prior files were recorded, it falls back to the
// committed set (minus Trace metadata) — but only when sessionHasEvidenceOfWork
// is true. The fallback was originally unconditional, which let sessions that
// were registered at SessionStart but never produced anything (e.g. ephemeral
// Codex sessions whose hooks fired with a null transcript_path and never
// reached SaveStep) inherit another session's committed files.
func filterFilesTouched(sessionData *ExtractedSessionData, committedFiles map[string]struct{}, state *SessionState) {
	if len(committedFiles) == 0 {
		return
	}
	if len(sessionData.FilesTouched) > 0 {
		filtered := make([]string, 0, len(sessionData.FilesTouched))
		for _, f := range sessionData.FilesTouched {
			if _, ok := committedFiles[f]; ok {
				filtered = append(filtered, f)
			}
		}
		sessionData.FilesTouched = filtered
		return
	}
	if !sessionHasEvidenceOfWork(sessionData, state) {
		return
	}
	sessionData.FilesTouched = committedFilesExcludingMetadata(committedFiles)
}

// sessionHasEvidenceOfWork returns true when the session looks like a real
// participant — either it produced a readable transcript or a prior SaveStep
// recorded a checkpoint (StepCount > 0). False means the session was likely
// registered but never did anything; treating such a session as the author of
// the committed files would attribute another session's work to it.
func sessionHasEvidenceOfWork(sessionData *ExtractedSessionData, state *SessionState) bool {
	if len(sessionData.Transcript) > 0 {
		return true
	}
	return state != nil && state.StepCount > 0
}

// extractOrCreateSessionData tries to extract session data from the shadow branch,
// live transcript, or creates empty session data as a fallback. The empty case is
// handled by the skip gate in CondenseSession.
func (s *ManualCommitStrategy) extractOrCreateSessionData(ctx context.Context, repo *git.Repository, ag agent.Agent, shadowHash plumbing.Hash, hasShadowBranch bool, state *SessionState) (*ExtractedSessionData, error) {
	switch {
	case hasShadowBranch:
		// Shadow branch exists (from SaveStep commits) — extract transcript and
		// metadata from the branch tree, preferring the live transcript if fresher.
		data, err := s.extractSessionData(ctx, repo, shadowHash, state.SessionID, state.FilesTouched, state.AgentType, state.TranscriptPath, state.CheckpointTranscriptStart, state.Phase.IsActive())
		if err != nil {
			return nil, fmt.Errorf("failed to extract session data: %w", err)
		}
		return data, nil
	case state.TranscriptPath != "":
		// No shadow branch but a live transcript path is known — read directly
		// from disk. This handles mid-session commits before SaveStep runs.
		if state.Phase.IsActive() {
			prepareTranscriptIfNeeded(ctx, ag, state.TranscriptPath)
		}
		data, err := s.extractSessionDataFromLiveTranscript(ctx, state)
		if err != nil {
			return nil, fmt.Errorf("failed to extract session data from live transcript: %w", err)
		}
		return data, nil
	default:
		// No shadow branch and no transcript path — create empty session data.
		// This happens for sessions where the agent never set TranscriptPath
		// (e.g., Codex hooks may send null transcript_path). The skip gate in
		// CondenseSession will skip condensation if nothing is found.
		logging.Debug(
			logging.WithComponent(ctx, "checkpoint"),
			"no shadow branch and no transcript path, returning empty session data",
			slog.String("session_id", state.SessionID),
			slog.String("agent_type", string(state.AgentType)),
		)
		return &ExtractedSessionData{
			FilesTouched: state.FilesTouched,
		}, nil
	}
}

// compactTranscriptResult holds the output of compact transcript generation.
type compactTranscriptResult struct {
	Transcript []byte        // Trace Transcript Format (JSONL), redacted. Nil means "skip".
	StartLine  int           // Compact transcript line offset at checkpoint start.
	Duration   time.Duration // Time spent producing the compact transcript.
}

// compactAndRedactExternalTranscript calls the external agent's compact-transcript
// subcommand and redacts the result. Returns (nil, false) if the agent is not
// external. Returns (nil, true) if the agent is external but compaction failed.
func compactAndRedactExternalTranscript(ctx context.Context, ag agent.Agent, state *SessionState) (transcript []byte, isExternal bool) {
	compactor, ok := agent.AsTranscriptCompactor(ag)
	if !ok {
		if _, isCap := ag.(agent.CapabilityDeclarer); isCap {
			logging.Warn(
				ctx, "external transcript compaction unavailable, skipping transcript.jsonl",
				slog.String("session_id", state.SessionID),
				slog.String("agent", string(ag.Name())),
			)
			return nil, true
		}
		return nil, false
	}

	compacted := compactTranscriptForExternalAgent(ctx, compactor, state.SessionID, state.TranscriptPath)
	if compacted == nil {
		return nil, true
	}

	redacted, err := redactSessionJSONLBytes(compacted.Transcript)
	if err != nil {
		logging.Warn(
			ctx, "failed to redact external compact transcript, dropping",
			slog.String("session_id", state.SessionID),
			slog.String("agent", string(compactor.Name())),
			slog.String("error", err.Error()),
		)
		return nil, true
	}
	return redacted.Bytes(), true
}

// buildExternalCompactTranscript produces the compact transcript for external
// agents by calling the agent's compact-transcript subcommand and redacting
// the result. Returns nil if the agent is not external (caller should use
// buildInternalCompactTranscript instead).
func buildExternalCompactTranscript(ctx context.Context, ag agent.Agent, state *SessionState) *compactTranscriptResult {
	if !settings.IsCheckpointsV2Enabled(ctx) {
		return nil
	}

	compactStart := time.Now()
	compactCtx, compactSpan := perf.Start(ctx, "compact_transcript_v2")
	defer compactSpan.End()

	transcript, isExternal := compactAndRedactExternalTranscript(compactCtx, ag, state)
	if !isExternal {
		return nil
	}
	if transcript == nil {
		return &compactTranscriptResult{Duration: time.Since(compactStart)}
	}

	startLine := state.CompactTranscriptStart
	fullLines := countCompactLines(transcript)
	if fullLines < startLine {
		logging.Warn(
			compactCtx, "external compact transcript shorter than previous compact transcript start; resetting compact transcript start",
			slog.String("session_id", state.SessionID),
			slog.String("agent", string(ag.Name())),
			slog.Int("compact_transcript_lines", fullLines),
			slog.Int("previous_compact_transcript_start", startLine),
		)
		startLine = 0
	}

	return &compactTranscriptResult{
		Transcript: transcript,
		StartLine:  startLine,
		Duration:   time.Since(compactStart),
	}
}

// buildInternalCompactTranscript produces the compact transcript for built-in
// agents from already-redacted transcript bytes.
func buildInternalCompactTranscript(ctx context.Context, ag agent.Agent, redacted redact.RedactedBytes, state *SessionState) compactTranscriptResult {
	if !settings.IsCheckpointsV2Enabled(ctx) {
		return compactTranscriptResult{}
	}

	compactStart := time.Now()
	compactCtx, compactSpan := perf.Start(ctx, "compact_transcript_v2")
	defer compactSpan.End()

	// Generate scoped compact (only new content) for line counting and offset calculation.
	scopedCompact := compactTranscriptForV2(compactCtx, ag, redacted, state.CheckpointTranscriptStart)
	// Generate full compact (cumulative) for storage — v2 /main replaces
	// the session's transcript.jsonl on each write, so we must include all
	// prior content, not just the new portion.
	fullCompact := compactTranscriptForV2(compactCtx, ag, redacted, 0)
	startLine := computeCompactTranscriptStart(compactCtx, ag, state, redacted.Bytes(), scopedCompact)

	return compactTranscriptResult{
		Transcript: fullCompact,
		StartLine:  startLine,
		Duration:   time.Since(compactStart),
	}
}

func compactTranscriptForExternalAgent(
	ctx context.Context,
	compactor agent.TranscriptCompactor,
	sessionID string,
	transcriptPath string,
) *agent.CompactedTranscript {
	if transcriptPath == "" {
		logging.Warn(
			ctx, "external transcript compaction skipped: missing session transcript path",
			slog.String("session_id", sessionID),
			slog.String("agent", string(compactor.Name())),
		)
		return nil
	}

	compacted, err := compactor.CompactTranscript(ctx, transcriptPath)
	if err != nil {
		logging.Warn(
			ctx, "external transcript compaction failed, skipping transcript.jsonl on /main",
			slog.String("session_id", sessionID),
			slog.String("agent", string(compactor.Name())),
			slog.String("error", err.Error()),
		)
		return nil
	}
	if compacted == nil {
		logging.Warn(
			ctx, "external transcript compaction returned nil transcript",
			slog.String("session_id", sessionID),
			slog.String("agent", string(compactor.Name())),
		)
		return nil
	}
	if len(bytes.TrimSpace(compacted.Transcript)) == 0 {
		logging.Warn(
			ctx, "external transcript compaction returned empty transcript",
			slog.String("session_id", sessionID),
			slog.String("agent", string(compactor.Name())),
		)
		return nil
	}
	if !bytes.HasSuffix(compacted.Transcript, []byte{'\n'}) {
		compacted.Transcript = append(compacted.Transcript, '\n')
	}
	if len(compacted.Assets) > 0 {
		logging.Warn(
			ctx, "external transcript compaction returned assets that are not yet persisted",
			slog.String("session_id", sessionID),
			slog.String("agent", string(compactor.Name())),
			slog.Int("asset_count", len(compacted.Assets)),
		)
	}
	return compacted
}

// generateSummary produces an LLM-generated summary of the session transcript.
// The transcript must be pre-redacted to avoid sending secrets to the LLM.
// Returns nil if the scoped transcript is empty or generation fails.
func generateSummary(ctx context.Context, redactedTranscript redact.RedactedBytes, filesTouched []string, state *SessionState) *cpkg.Summary {
	summarizeCtx := logging.WithComponent(ctx, "summarize")
	transcriptBytes := redactedTranscript.Bytes()

	var scopedTranscript []byte
	switch state.AgentType {
	case agent.AgentTypeGemini:
		scoped, sliceErr := geminicli.SliceFromMessage(transcriptBytes, state.CheckpointTranscriptStart)
		if sliceErr != nil {
			logging.Warn(summarizeCtx, "failed to scope Gemini transcript for summary",
				slog.String("session_id", state.SessionID),
				slog.String("error", sliceErr.Error()))
		}
		scopedTranscript = scoped
	case agent.AgentTypeOpenCode:
		scoped, sliceErr := opencode.SliceFromMessage(transcriptBytes, state.CheckpointTranscriptStart)
		if sliceErr != nil {
			logging.Warn(summarizeCtx, "failed to scope OpenCode transcript for summary",
				slog.String("session_id", state.SessionID),
				slog.String("error", sliceErr.Error()))
		}
		scopedTranscript = scoped
	case agent.AgentTypeCodex, agent.AgentTypeClaudeCode, agent.AgentTypeCursor, agent.AgentTypeFactoryAIDroid, agent.AgentTypeUnknown:
		scopedTranscript = transcript.SliceFromLine(transcriptBytes, state.CheckpointTranscriptStart)
	}

	if len(scopedTranscript) == 0 {
		return nil
	}

	generator := buildSummaryGenerator(summarizeCtx)
	// scopedTranscript is sliced from redactedTranscript, which was redacted earlier in CondenseSession.
	summary, err := summarize.GenerateFromTranscript(summarizeCtx, redact.AlreadyRedacted(scopedTranscript), filesTouched, state.AgentType, generator)
	if err != nil {
		logging.Warn(summarizeCtx, "summary generation failed",
			slog.String("session_id", state.SessionID),
			slog.String("error", err.Error()))
		return nil
	}
	logging.Info(summarizeCtx, "summary generated",
		slog.String("session_id", state.SessionID))
	return summary
}

// buildSummaryGenerator returns a Generator based on the configured summary provider.
// Returns nil if no provider is configured (GenerateFromTranscript falls back to ClaudeGenerator).
//
// The return type is the summarize.Generator interface rather than the concrete
// adapter pointer so callers can't accidentally hold a non-nil interface that
// wraps a nil pointer (the classic Go nil-interface footgun).
func buildSummaryGenerator(ctx context.Context) summarize.Generator {
	s, err := settings.Load(ctx)
	if err != nil {
		// Warn (not Debug): this is the auto-summarize hot path on every commit.
		// A settings-load failure silently downgrades the user's configured
		// provider to the default, and Debug would hide that from operators.
		logging.Warn(ctx, "could not load settings for summary provider, using default",
			"error", err.Error())
		return nil
	}
	if s.SummaryGeneration == nil || s.SummaryGeneration.Provider == "" {
		return nil
	}

	providerName := types.AgentName(s.SummaryGeneration.Provider)
	ag, err := agent.Get(providerName)
	if err != nil {
		discoverExternalSummaryProviders(ctx)
		ag, err = agent.Get(providerName)
		if err != nil {
			logging.Warn(ctx, "configured summary provider not available, using default",
				"provider", s.SummaryGeneration.Provider, "error", err.Error())
			return nil
		}
	}

	tg, ok := agent.AsTextGenerator(ag)
	if !ok {
		logging.Warn(ctx, "configured summary provider does not support text generation, using default",
			"provider", s.SummaryGeneration.Provider)
		return nil
	}

	// Check binary on PATH, not DetectPresence — a repo can use one agent
	// for development while a different agent generates summaries. Fall back
	// silently (Warn log) because this runs in the post-commit hook and a
	// hard error would block the commit.
	if !external.IsExternal(ag) && !isSummaryProviderCLIAvailable(providerName) {
		logging.Warn(ctx, "configured summary provider CLI binary not on PATH, using default",
			"provider", s.SummaryGeneration.Provider)
		return nil
	}

	return &summarize.TextGeneratorAdapter{
		TextGenerator: tg,
		Model:         summarize.ResolveModel(providerName, s.SummaryGeneration.Model),
	}
}

// marshalPromptAttributionsIncludingPending builds the complete prompt attribution slice
// (including PendingPromptAttribution for mid-turn commits) and encodes it to JSON.
// This must stay consistent with the slice used by calculateSessionAttributions so the
// persisted diagnostics match the computed InitialAttribution.
func marshalPromptAttributionsIncludingPending(state *SessionState) json.RawMessage {
	pas := make([]PromptAttribution, len(state.PromptAttributions), len(state.PromptAttributions)+1)
	copy(pas, state.PromptAttributions)
	if state.PendingPromptAttribution != nil {
		pas = append(pas, *state.PendingPromptAttribution)
	}
	if len(pas) == 0 {
		return nil
	}
	data, err := json.Marshal(pas)
	if err != nil {
		return nil
	}
	return data
}

// buildSessionMetrics creates a SessionMetrics from session state if any metrics are available.
// Returns nil if no hook-provided metrics exist (e.g., for agents that don't report them).
func buildSessionMetrics(state *SessionState) *cpkg.SessionMetrics {
	if state.SessionDurationMs == 0 && state.SessionTurnCount == 0 && state.ContextTokens == 0 && state.ContextWindowSize == 0 {
		return nil
	}
	return &cpkg.SessionMetrics{
		DurationMs:        state.SessionDurationMs,
		TurnCount:         state.SessionTurnCount,
		ContextTokens:     state.ContextTokens,
		ContextWindowSize: state.ContextWindowSize,
	}
}

func hasTokenUsageData(usage *agent.TokenUsage) bool {
	if usage == nil {
		return false
	}

	if usage.InputTokens > 0 || usage.CacheCreationTokens > 0 || usage.CacheReadTokens > 0 || usage.OutputTokens > 0 || usage.APICallCount > 0 {
		return true
	}

	return hasTokenUsageData(usage.SubagentTokens)
}

// sessionStateBackfillTokenUsage returns the best session-level token usage to
// persist in session state after condensation.
func sessionStateBackfillTokenUsage(ctx context.Context, ag agent.Agent, agentType types.AgentType, transcript []byte, checkpointUsage *agent.TokenUsage) *agent.TokenUsage {
	if agentType == agent.AgentTypeCopilotCLI && len(transcript) > 0 {
		fullSessionUsage := agent.CalculateTokenUsage(ctx, ag, transcript, 0, "")
		if hasTokenUsageData(fullSessionUsage) {
			return fullSessionUsage
		}
		logging.Debug(ctx, "copilot-cli: full-session token read produced no data, falling back to checkpoint usage")
	}

	if agentType == agent.AgentTypeCopilotCLI && hasTokenUsageData(checkpointUsage) {
		return checkpointUsage
	}

	if checkpointUsage != nil && checkpointUsage.InputTokens > 0 {
		return checkpointUsage
	}

	return nil
}

// attributionOpts provides pre-resolved git objects to avoid redundant reads.
type attributionOpts struct {
	headTree              *object.Tree        // HEAD commit tree (already resolved by PostCommit)
	shadowTree            *object.Tree        // Shadow branch tree (already resolved by PostCommit)
	parentTree            *object.Tree        // Parent commit tree (nil for initial commits, for consistent non-agent line counting)
	repoDir               string              // Repository worktree path for git CLI commands
	parentCommitHash      string              // HEAD's first parent hash (preferred diff base for non-agent files)
	attributionBaseCommit string              // Base commit hash for non-agent file detection (empty = fall back to go-git tree walk)
	headCommitHash        string              // HEAD commit hash for non-agent file detection (empty = fall back to go-git tree walk)
	allAgentFiles         map[string]struct{} // Union of all sessions' FilesTouched (nil = single-session)
}
