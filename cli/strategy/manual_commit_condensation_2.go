package strategy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/factoryaidroid"
	"github.com/GrayCodeAI/trace/cli/agent/geminicli"
	"github.com/GrayCodeAI/trace/cli/agent/opencode"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	cpkg "github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/session"
	"github.com/GrayCodeAI/trace/cli/textutil"
	"github.com/GrayCodeAI/trace/cli/transcript"
	"github.com/GrayCodeAI/trace/cli/transcript/compact"
	"github.com/GrayCodeAI/trace/cli/versioninfo"
	"github.com/GrayCodeAI/trace/redact"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

func calculateSessionAttributions(ctx context.Context, repo *git.Repository, shadowRef *plumbing.Reference, sessionData *ExtractedSessionData, state *SessionState, opts ...attributionOpts) *cpkg.InitialAttribution {
	// Calculate initial attribution using accumulated prompt attribution data.
	// This uses user edits captured at each prompt start (before agent works),
	// plus any user edits after the final checkpoint (shadow → head).
	//
	// When shadowRef is nil (agent committed mid-turn before SaveStep),
	// HEAD is used as the shadow tree. This is correct because the agent's
	// commit IS HEAD — there are no user edits between agent work and commit.
	logCtx := logging.WithComponent(ctx, "attribution")

	var o attributionOpts
	if len(opts) > 0 {
		o = opts[0]
	}

	headTree := o.headTree
	if headTree == nil {
		headRef, headErr := repo.Head()
		if headErr != nil {
			logging.Debug(logCtx, "attribution skipped: failed to get HEAD",
				slog.String("error", headErr.Error()))
			return nil
		}

		headCommit, commitErr := repo.CommitObject(headRef.Hash())
		if commitErr != nil {
			logging.Debug(logCtx, "attribution skipped: failed to get HEAD commit",
				slog.String("error", commitErr.Error()))
			return nil
		}

		var treeErr error
		headTree, treeErr = headCommit.Tree()
		if treeErr != nil {
			logging.Debug(logCtx, "attribution skipped: failed to get HEAD tree",
				slog.String("error", treeErr.Error()))
			return nil
		}
	}

	// Get shadow tree: from pre-resolved cache, shadow branch, or HEAD (agent committed directly).
	shadowTree := o.shadowTree
	if shadowTree == nil {
		if shadowRef != nil {
			shadowCommit, shadowErr := repo.CommitObject(shadowRef.Hash())
			if shadowErr != nil {
				logging.Debug(logCtx, "attribution skipped: failed to get shadow commit",
					slog.String("error", shadowErr.Error()),
					slog.String("shadow_ref", shadowRef.Hash().String()))
				return nil
			}
			var shadowTreeErr error
			shadowTree, shadowTreeErr = shadowCommit.Tree()
			if shadowTreeErr != nil {
				logging.Debug(logCtx, "attribution skipped: failed to get shadow tree",
					slog.String("error", shadowTreeErr.Error()))
				return nil
			}
		} else {
			// No shadow branch: agent committed mid-turn. Use HEAD as shadow
			// because the agent's work is the commit itself.
			logging.Debug(logCtx, "attribution: using HEAD as shadow (no shadow branch)")
			shadowTree = headTree
		}
	}

	// Get base tree (state before session started)
	var baseTree *object.Tree
	attrBase := state.AttributionBaseCommit
	if attrBase == "" {
		attrBase = state.BaseCommit // backward compat
	}
	if baseCommit, baseErr := repo.CommitObject(plumbing.NewHash(attrBase)); baseErr == nil {
		if tree, baseTErr := baseCommit.Tree(); baseTErr == nil {
			baseTree = tree
		} else {
			logging.Debug(logCtx, "attribution: base tree unavailable",
				slog.String("error", baseTErr.Error()))
		}
	} else {
		logging.Debug(logCtx, "attribution: base commit unavailable",
			slog.String("error", baseErr.Error()),
			slog.String("attribution_base", attrBase))
	}

	// Include PendingPromptAttribution if it was never moved to PromptAttributions.
	// This happens when an agent commits mid-turn without calling SaveStep (e.g., Codex).
	// PendingPromptAttribution is set during UserPromptSubmit but only moved to
	// PromptAttributions during SaveStep. Without this, mid-turn commits have no PA
	// data and pre-session worktree dirt cannot be identified for baseline exclusion.
	promptAttrs := state.PromptAttributions
	if state.PendingPromptAttribution != nil {
		promptAttrs = append(promptAttrs, *state.PendingPromptAttribution)
	}

	// Log accumulated prompt attributions for debugging
	var totalUserAdded, totalUserRemoved int
	for i, pa := range promptAttrs {
		totalUserAdded += pa.UserLinesAdded
		totalUserRemoved += pa.UserLinesRemoved
		logging.Debug(logCtx, "prompt attribution data",
			slog.Int("checkpoint", pa.CheckpointNumber),
			slog.Int("user_added", pa.UserLinesAdded),
			slog.Int("user_removed", pa.UserLinesRemoved),
			slog.Int("agent_added", pa.AgentLinesAdded),
			slog.Int("agent_removed", pa.AgentLinesRemoved),
			slog.Int("index", i))
	}

	attribution := CalculateAttributionWithAccumulated(ctx, AttributionParams{
		BaseTree:              baseTree,
		ShadowTree:            shadowTree,
		HeadTree:              headTree,
		ParentTree:            o.parentTree,
		FilesTouched:          sessionData.FilesTouched,
		PromptAttributions:    promptAttrs,
		RepoDir:               o.repoDir,
		ParentCommitHash:      o.parentCommitHash,
		AttributionBaseCommit: attrBase,
		HeadCommitHash:        o.headCommitHash,
		AllAgentFiles:         o.allAgentFiles,
	})

	if attribution != nil {
		logging.Info(logCtx, "attribution calculated",
			slog.Int("agent_lines", attribution.AgentLines),
			slog.Int("human_added", attribution.HumanAdded),
			slog.Int("human_modified", attribution.HumanModified),
			slog.Int("human_removed", attribution.HumanRemoved),
			slog.Int("total_committed", attribution.TotalCommitted),
			slog.Float64("agent_percentage", attribution.AgentPercentage),
			slog.Int("accumulated_user_added", totalUserAdded),
			slog.Int("accumulated_user_removed", totalUserRemoved),
			slog.Int("files_touched", len(sessionData.FilesTouched)))
	}

	return attribution
}

// committedFilesExcludingMetadata returns committed files with CLI metadata paths filtered out.
// `.trace/` files are created by `trace enable`, not by the agent, and should not be
// attributed as agent work when used as a fallback for sessions with no FilesTouched.
func committedFilesExcludingMetadata(committedFiles map[string]struct{}) []string {
	result := make([]string, 0, len(committedFiles))
	for f := range committedFiles {
		if strings.HasPrefix(f, ".trace/") || strings.HasPrefix(f, paths.TraceMetadataDir+"/") {
			continue
		}
		result = append(result, f)
	}
	slices.Sort(result)
	return result
}

// extractSessionData extracts session data from the shadow branch.
// filesTouched is the list of files tracked during the session (from SessionState.FilesTouched).
// agentType identifies the agent (e.g., "Gemini CLI", "Claude Code") to determine transcript format.
// liveTranscriptPath, when non-empty and readable, is preferred over the shadow branch copy.
// This handles the case where SaveStep was skipped (no code changes) but the transcript
// continued growing — the shadow branch copy would be stale.
// checkpointTranscriptStart is the line offset (Claude) or message index (Gemini) where the current checkpoint began.
func (s *ManualCommitStrategy) extractSessionData(ctx context.Context, repo *git.Repository, shadowRef plumbing.Hash, sessionID string, filesTouched []string, agentType types.AgentType, liveTranscriptPath string, checkpointTranscriptStart int, isActive bool) (*ExtractedSessionData, error) {
	ag, _ := agent.GetByAgentType(agentType) //nolint:errcheck // ag may be nil for unknown agent types; callers use type assertions so nil is safe
	commit, err := repo.CommitObject(shadowRef)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit object: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get commit tree: %w", err)
	}

	data := &ExtractedSessionData{}
	// sessionID is already an "trace session ID" (with date prefix)
	metadataDir := paths.SessionMetadataDirFromSessionID(sessionID)

	// Extract transcript — prefer the live file when available, fall back to shadow branch.
	// The shadow branch copy may be stale if the last turn ended without code changes
	// (SaveStep is only called when there are file modifications).
	var fullTranscript string
	if liveTranscriptPath != "" {
		// Ensure transcript file exists (OpenCode creates it lazily via `opencode export`).
		// Only wait for flush when the session is active — for idle/ended sessions the
		// transcript is already fully flushed (the Stop hook completed the flush).
		if isActive {
			prepareTranscriptIfNeeded(ctx, ag, liveTranscriptPath)
		}
		if liveData, readErr := os.ReadFile(liveTranscriptPath); readErr == nil && len(liveData) > 0 { //nolint:gosec // path from session state
			fullTranscript = string(liveData)
		}
	}
	if fullTranscript == "" {
		// Fall back to shadow branch copy
		if file, fileErr := tree.File(metadataDir + "/" + paths.TranscriptFileName); fileErr == nil {
			if content, contentErr := file.Contents(); contentErr == nil {
				fullTranscript = content
			}
		} else if file, fileErr := tree.File(metadataDir + "/" + paths.TranscriptFileNameLegacy); fileErr == nil {
			if content, contentErr := file.Contents(); contentErr == nil {
				fullTranscript = content
			}
		}
	}

	// Process transcript based on agent type
	if fullTranscript != "" {
		data.Transcript = []byte(fullTranscript)
		data.FullTranscriptLines = countTranscriptItems(agentType, fullTranscript)
		// Read prompts from shadow branch tree (source of truth after SaveStep)
		if file, fileErr := tree.File(metadataDir + "/" + paths.PromptFileName); fileErr == nil {
			if content, contentErr := file.Contents(); contentErr == nil && content != "" {
				data.Prompts = splitPromptContent(content)
			}
		}
		// Filesystem fallback (written at turn start, covers mid-turn commits)
		if len(data.Prompts) == 0 {
			data.Prompts = readPromptsFromFilesystem(ctx, sessionID)
		}
	}

	// Use tracked files from session state (not all files in tree)
	data.FilesTouched = filesTouched

	// Calculate token usage from the extracted transcript portion
	if len(data.Transcript) > 0 {
		// Derive subagents directory from the live transcript path when available.
		// Pattern: <transcriptDir>/<sessionID>/subagents (same as manual_commit_hooks.go)
		var subagentsDir string
		if liveTranscriptPath != "" {
			subagentsDir = filepath.Join(filepath.Dir(liveTranscriptPath), sessionID, "subagents")
		}
		data.TokenUsage = agent.CalculateTokenUsage(ctx, ag, data.Transcript, checkpointTranscriptStart, subagentsDir)
	}

	return data, nil
}

// extractSessionDataFromLiveTranscript extracts session data directly from the live transcript file.
// This is used for mid-session commits where no shadow branch exists yet.
func (s *ManualCommitStrategy) extractSessionDataFromLiveTranscript(ctx context.Context, state *SessionState) (*ExtractedSessionData, error) {
	data := &ExtractedSessionData{}

	ag, _ := agent.GetByAgentType(state.AgentType) //nolint:errcheck // ag may be nil for unknown agent types; callers use type assertions so nil is safe

	// Resolve the transcript path (handles agents that relocate mid-session).
	transcriptPath, resolveErr := resolveTranscriptPath(state)
	if resolveErr != nil {
		return nil, resolveErr
	}

	liveData, err := os.ReadFile(transcriptPath) //nolint:gosec // path validated by resolveTranscriptPath
	if err != nil {
		return nil, fmt.Errorf("failed to read live transcript: %w", err)
	}

	if len(liveData) == 0 {
		return nil, errors.New("live transcript is empty")
	}

	fullTranscript := string(liveData)
	data.Transcript = liveData
	data.FullTranscriptLines = countTranscriptItems(state.AgentType, fullTranscript)
	data.Prompts = readPromptsFromFilesystem(ctx, state.SessionID)

	// Resolve files touched: prefers hook-populated state, falls back to transcript extraction
	data.FilesTouched = s.resolveFilesTouched(ctx, state)

	// Calculate token usage from the extracted transcript portion
	if len(data.Transcript) > 0 {
		// Derive subagents directory from the transcript path when available.
		// Pattern: <transcriptDir>/<sessionID>/subagents (same as manual_commit_hooks.go)
		var subagentsDir string
		if state.TranscriptPath != "" {
			subagentsDir = filepath.Join(filepath.Dir(state.TranscriptPath), state.SessionID, "subagents")
		}
		data.TokenUsage = agent.CalculateTokenUsage(ctx, ag, data.Transcript, state.CheckpointTranscriptStart, subagentsDir)
	}

	return data, nil
}

// countTranscriptItems counts lines (JSONL) or messages (JSON) in a transcript.
// For Claude Code and JSONL-based agents, this counts lines.
// For Gemini CLI, OpenCode, and JSON-based agents, this counts messages.
// Returns 0 if the content is empty or malformed.
func countTranscriptItems(agentType types.AgentType, content string) int {
	if content == "" {
		return 0
	}

	// OpenCode uses export JSON format with {"info": {...}, "messages": [...]}
	if agentType == agent.AgentTypeOpenCode {
		session, err := opencode.ParseExportSession([]byte(content))
		if err == nil && session != nil {
			return len(session.Messages)
		}
		return 0
	}

	// Try Gemini format first if agentType is Gemini, or as fallback if Unknown
	if agentType == agent.AgentTypeGemini || agentType == agent.AgentTypeUnknown {
		transcript, err := geminicli.ParseTranscript([]byte(content))
		if err == nil && transcript != nil && len(transcript.Messages) > 0 {
			return len(transcript.Messages)
		}
		// If agentType is explicitly Gemini but parsing failed, return 0
		if agentType == agent.AgentTypeGemini {
			return 0
		}
		// Otherwise fall through to JSONL parsing for Unknown type
	}

	// Claude Code and other JSONL-based agents
	allLines := strings.Split(content, "\n")
	// Trim trailing empty lines (from final \n in JSONL)
	for len(allLines) > 0 && strings.TrimSpace(allLines[len(allLines)-1]) == "" {
		allLines = allLines[:len(allLines)-1]
	}
	return len(allLines)
}

// extractUserPrompts extracts all user prompts from transcript content.
// Returns prompts with IDE context tags stripped (e.g., <ide_opened_file>).
func extractUserPrompts(agentType types.AgentType, content string) []string {
	if content == "" {
		return nil
	}

	// Droid has its own envelope format — use its parser to normalize first
	if agentType == agent.AgentTypeFactoryAIDroid {
		lines, _, err := factoryaidroid.ParseDroidTranscriptFromBytes([]byte(content), 0)
		if err != nil {
			return nil
		}
		var prompts []string
		for _, line := range lines {
			if line.Type != transcript.TypeUser {
				continue
			}
			if text := transcript.ExtractUserContent(line.Message); text != "" {
				if stripped := textutil.StripIDEContextTags(text); stripped != "" {
					prompts = append(prompts, stripped)
				}
			}
		}
		return prompts
	}

	// OpenCode uses JSONL with a different per-line schema than Claude Code
	if agentType == agent.AgentTypeOpenCode {
		prompts, err := opencode.ExtractAllUserPrompts([]byte(content))
		if err == nil && len(prompts) > 0 {
			cleaned := make([]string, 0, len(prompts))
			for _, prompt := range prompts {
				if stripped := textutil.StripIDEContextTags(prompt); stripped != "" {
					cleaned = append(cleaned, stripped)
				}
			}
			return cleaned
		}
		return nil
	}

	// Try Gemini format first if agentType is Gemini, or as fallback if Unknown
	if agentType == agent.AgentTypeGemini || agentType == agent.AgentTypeUnknown {
		prompts, err := geminicli.ExtractAllUserPrompts([]byte(content))
		if err == nil && len(prompts) > 0 {
			// Strip IDE context tags for consistency with Claude Code handling
			cleaned := make([]string, 0, len(prompts))
			for _, prompt := range prompts {
				if stripped := textutil.StripIDEContextTags(prompt); stripped != "" {
					cleaned = append(cleaned, stripped)
				}
			}
			return cleaned
		}
		// If agentType is explicitly Gemini but parsing failed, return nil
		if agentType == agent.AgentTypeGemini {
			return nil
		}
		// Otherwise fall through to JSONL parsing for Unknown type
	}

	// Claude Code and other JSONL-based agents
	return extractUserPromptsFromLines(strings.Split(content, "\n"))
}

// extractUserPromptsFromLines extracts user prompts from JSONL transcript lines.
// IDE-injected context tags (like <ide_opened_file>) are stripped from the results.
func extractUserPromptsFromLines(lines []string) []string {
	var prompts []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		// Check for user message:
		// - Claude Code uses "type": "human" or "type": "user"
		// - Cursor uses "role": "user"
		msgType, _ := entry["type"].(string) //nolint:errcheck // type assertion on interface{} from JSON
		msgRole, _ := entry["role"].(string) //nolint:errcheck // type assertion on interface{} from JSON
		isUser := msgType == "human" || msgType == "user" || msgRole == "user"
		if !isUser {
			continue
		}

		// Extract message content
		message, ok := entry["message"].(map[string]interface{})
		if !ok {
			continue
		}

		// Handle string content
		if content, ok := message["content"].(string); ok && content != "" {
			cleaned := textutil.StripIDEContextTags(content)
			if cleaned != "" {
				prompts = append(prompts, cleaned)
			}
			continue
		}

		// Handle array content (e.g., multiple text blocks from VSCode)
		if arr, ok := message["content"].([]interface{}); ok {
			var texts []string
			for _, item := range arr {
				if m, ok := item.(map[string]interface{}); ok {
					if m["type"] == "text" {
						if text, ok := m["text"].(string); ok {
							texts = append(texts, text)
						}
					}
				}
			}
			if len(texts) > 0 {
				cleaned := textutil.StripIDEContextTags(strings.Join(texts, "\n\n"))
				if cleaned != "" {
					prompts = append(prompts, cleaned)
				}
			}
		}
	}
	return prompts
}

// splitPromptContent splits prompt.txt content on the "\n\n---\n\n" separator.
// Returns nil if content is empty.
func splitPromptContent(content string) []string {
	if content == "" {
		return nil
	}
	parts := strings.Split(content, "\n\n---\n\n")
	var result []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// readPromptsFromFilesystem reads prompt.txt from the filesystem session metadata directory.
// This file is written at turn start and updated at each SaveStep, providing prompt data
// even for mid-turn commits where the shadow branch may not have been updated.
func readPromptsFromFilesystem(ctx context.Context, sessionID string) []string {
	sessionDir := paths.SessionMetadataDirFromSessionID(sessionID)
	sessionDirAbs, err := paths.AbsPath(ctx, sessionDir)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(sessionDirAbs, paths.PromptFileName)) //nolint:gosec // path from session ID
	if err != nil || len(data) == 0 {
		return nil
	}
	return splitPromptContent(string(data))
}

// clearFilesystemPrompt removes the filesystem prompt.txt for a session.
// Called after condensation so subsequent checkpoints start fresh.
func clearFilesystemPrompt(ctx context.Context, sessionID string) {
	sessionDir := paths.SessionMetadataDirFromSessionID(sessionID)
	sessionDirAbs, err := paths.AbsPath(ctx, sessionDir)
	if err != nil {
		return
	}
	promptPath := filepath.Join(sessionDirAbs, paths.PromptFileName)
	_ = os.Remove(promptPath)
}

// CondenseSessionByID condenses a session by its ID and cleans up.
// This is used by "trace doctor" to salvage stuck sessions.
func (s *ManualCommitStrategy) CondenseSessionByID(ctx context.Context, sessionID string) error {
	logCtx := logging.WithComponent(ctx, "condense-by-id")

	// Load session state
	state, err := s.loadSessionState(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to load session state: %w", err)
	}
	if state == nil {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	// Open repository
	repo, err := OpenRepository(ctx)
	if err != nil {
		return fmt.Errorf("failed to open repository: %w", err)
	}

	// Generate a checkpoint ID
	checkpointID, err := id.Generate()
	if err != nil {
		return fmt.Errorf("failed to generate checkpoint ID: %w", err)
	}

	// Check if shadow branch exists (required for condensation)
	shadowBranchName := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)
	refName := plumbing.NewBranchReferenceName(shadowBranchName)
	_, refErr := repo.Reference(refName, true)
	hasShadowBranch := refErr == nil

	if !hasShadowBranch {
		// No shadow branch means no checkpoint data to condense.
		// Just clean up the state file.
		logging.Info(
			logCtx, "no shadow branch for session, clearing state only",
			slog.String("session_id", sessionID),
			slog.String("shadow_branch", shadowBranchName),
		)
		if err := s.clearSessionState(ctx, sessionID); err != nil {
			return fmt.Errorf("failed to clear session state: %w", err)
		}
		return nil
	}

	// Condense the session
	result, err := s.CondenseSession(ctx, repo, checkpointID, state, nil)
	if err != nil {
		return fmt.Errorf("failed to condense session: %w", err)
	}

	if result.Skipped {
		// Nothing to condense. Mark fully condensed so trace doctor doesn't
		// keep retrying this empty session on every invocation.
		logging.Info(
			logCtx, "session condensation skipped (no transcript or files), marking fully condensed",
			slog.String("session_id", sessionID),
		)
		state.FullyCondensed = true
		return s.saveSessionState(ctx, state)
	}

	logging.Info(
		logCtx, "session condensed by ID",
		slog.String("session_id", sessionID),
		slog.String("checkpoint_id", result.CheckpointID.String()),
		slog.Int("checkpoints_condensed", result.CheckpointsCount),
	)

	// Update session state: reset step count and transition to idle
	state.StepCount = 0
	state.CheckpointTranscriptStart = result.TotalTranscriptLines
	state.CompactTranscriptStart += result.CompactTranscriptLines
	state.CheckpointTranscriptSize = int64(len(result.Transcript))
	state.Phase = session.PhaseIdle
	state.LastCheckpointID = checkpointID
	state.LastCheckpointCommitHash = state.BaseCommit
	state.RealignAttributionBase(state.BaseCommit)
	state.PromptAttributions = nil
	state.PendingPromptAttribution = nil

	if err := s.saveSessionState(ctx, state); err != nil {
		return fmt.Errorf("failed to save session state: %w", err)
	}

	// Clean up shadow branch if no other sessions need it
	if err := s.cleanupShadowBranchIfUnused(ctx, repo, shadowBranchName, sessionID); err != nil {
		logging.Warn(
			logCtx, "failed to clean up shadow branch",
			slog.String("shadow_branch", shadowBranchName),
			slog.String("error", err.Error()),
		)
		// Non-fatal: condensation succeeded, shadow branch cleanup is best-effort
	}

	return nil
}

// CondenseAndMarkFullyCondensed condenses an ENDED session and marks it
// FullyCondensed in one operation. Used by the session stop hook to eagerly
// clean up sessions so PostCommit doesn't have to process them.
//
// This does NOT call CondenseSessionByID because that method has two behaviors
// we don't want: (1) it calls clearSessionState when no shadow branch exists
// (deletes the state file entirely), and (2) it sets Phase = IDLE. Instead,
// we inline the condensation logic with ENDED-appropriate behavior.
//
// Fail-open: if condensation fails, the session is left in its current state
// and PostCommit will still process it on the next commit.
func (s *ManualCommitStrategy) CondenseAndMarkFullyCondensed(ctx context.Context, sessionID string) error {
	logCtx := logging.WithComponent(ctx, "checkpoint")

	state, err := s.loadSessionState(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to load session state: %w", err)
	}
	if state == nil {
		return nil // No state file
	}

	// Sessions with FilesTouched must be processed by PostCommit for carry-forward
	// tracking — each user commit that overlaps with tracked files gets its own
	// checkpoint. Eagerly condensing here would prevent that 1:1 linkage.
	if len(state.FilesTouched) > 0 {
		return nil
	}

	// Only condense if there's uncondensed data
	if state.StepCount <= 0 {
		// No data and no files — mark FullyCondensed
		state.FullyCondensed = true
		return s.saveSessionState(ctx, state)
	}

	// Check if shadow branch exists — required for condensation
	repo, err := OpenRepository(ctx)
	if err != nil {
		logging.Warn(
			logCtx, "eager condense: failed to open repository",
			slog.String("session_id", sessionID),
			slog.String("error", err.Error()),
		)
		return nil // fail-open
	}

	shadowBranchName := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)
	refName := plumbing.NewBranchReferenceName(shadowBranchName)
	_, refErr := repo.Reference(refName, true)
	hasShadowBranch := refErr == nil

	if !hasShadowBranch {
		// No shadow branch = no checkpoint data to condense.
		// Unlike CondenseSessionByID, we do NOT delete the state file.
		logging.Info(
			logCtx, "eager condense: no shadow branch",
			slog.String("session_id", sessionID),
			slog.String("shadow_branch", shadowBranchName),
		)
		state.StepCount = 0
		state.FullyCondensed = true // FilesTouched is already empty (checked above)
		return s.saveSessionState(ctx, state)
	}

	// Generate checkpoint ID and condense
	checkpointID, err := id.Generate()
	if err != nil {
		logging.Warn(
			logCtx, "eager condense: failed to generate checkpoint ID",
			slog.String("error", err.Error()),
		)
		return nil // fail-open
	}

	// Condense with nil committedFiles (include all FilesTouched)
	result, err := s.CondenseSession(ctx, repo, checkpointID, state, nil)
	if err != nil {
		logging.Warn(
			logCtx, "eager condense on session stop failed, PostCommit will retry",
			slog.String("session_id", sessionID),
			slog.String("error", err.Error()),
		)
		return nil // fail-open
	}

	if result.Skipped {
		// No transcript or files — nothing to condense. Mark fully condensed
		// so PostCommit doesn't keep retrying this empty session.
		logging.Info(
			logCtx, "eager condense skipped (no transcript or files), marking fully condensed",
			slog.String("session_id", sessionID),
		)
		state.FullyCondensed = true
		return s.saveSessionState(ctx, state)
	}

	// Update state — keep Phase = ENDED (unlike CondenseSessionByID which sets IDLE)
	state.StepCount = 0
	state.CheckpointTranscriptStart = result.TotalTranscriptLines
	state.CompactTranscriptStart += result.CompactTranscriptLines
	state.LastCheckpointID = checkpointID
	state.LastCheckpointCommitHash = state.BaseCommit
	state.RealignAttributionBase(state.BaseCommit)
	state.PromptAttributions = nil
	state.PendingPromptAttribution = nil
	state.FullyCondensed = true // FilesTouched is already empty (checked above)
	// Phase stays ENDED — do NOT set to IDLE

	logging.Info(
		logCtx, "eager condense on session stop succeeded",
		slog.String("session_id", sessionID),
		slog.String("checkpoint_id", result.CheckpointID.String()),
	)

	if err := s.saveSessionState(ctx, state); err != nil {
		return fmt.Errorf("failed to save session state: %w", err)
	}

	// Clean up shadow branch
	if err := s.cleanupShadowBranchIfUnused(ctx, repo, shadowBranchName, sessionID); err != nil {
		logging.Warn(
			logCtx, "eager condense: failed to clean up shadow branch",
			slog.String("shadow_branch", shadowBranchName),
			slog.String("error", err.Error()),
		)
	}

	return nil
}

// cleanupShadowBranchIfUnused deletes a shadow branch if no other active sessions reference it.
func (s *ManualCommitStrategy) cleanupShadowBranchIfUnused(ctx context.Context, _ *git.Repository, shadowBranchName, excludeSessionID string) error {
	// List all session states to check if any other session uses this shadow branch
	allStates, err := s.listAllSessionStates(ctx)
	if err != nil {
		return fmt.Errorf("failed to list session states: %w", err)
	}

	for _, state := range allStates {
		if state.SessionID == excludeSessionID {
			continue
		}
		otherShadow := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)
		if otherShadow == shadowBranchName && state.StepCount > 0 {
			// Another session still needs this shadow branch
			return nil
		}
	}

	// No other sessions need it, delete the shadow branch via CLI
	// (go-git v5's RemoveReference doesn't persist with packed refs/worktrees)
	if err := DeleteBranchCLI(ctx, shadowBranchName); err != nil {
		// Branch already gone is not an error
		if errors.Is(err, ErrBranchNotFound) {
			return nil
		}
		return fmt.Errorf("failed to remove shadow branch: %w", err)
	}
	return nil
}

// compactTranscriptForV2 produces the Trace Transcript Format (transcript.jsonl)
// from a redacted agent transcript. Returns nil if compaction cannot be performed
// (nil agent, empty transcript, or compaction error) —
// callers treat nil as "skip writing transcript.jsonl to /main".
func compactTranscriptForV2(ctx context.Context, ag agent.Agent, transcript redact.RedactedBytes, checkpointTranscriptStart int) []byte {
	if ag == nil || transcript.Len() == 0 {
		return nil
	}

	compacted, err := compact.Compact(transcript, compact.MetadataFields{
		Agent:      string(ag.Name()),
		CLIVersion: versioninfo.Version,
		StartLine:  checkpointTranscriptStart,
	})
	if err != nil {
		logging.Warn(
			ctx, "compact transcript generation failed, skipping transcript.jsonl on /main",
			slog.String("agent", string(ag.Name())),
			slog.String("error", err.Error()),
		)
		return nil
	}
	return compacted
}

// countCompactLines returns line count for compact transcript JSONL.
func countCompactLines(compactTranscript []byte) int {
	return bytes.Count(compactTranscript, []byte{'\n'})
}
