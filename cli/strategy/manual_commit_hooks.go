package strategy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent/types"

	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/interactive"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/session"
	"github.com/GrayCodeAI/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cli/stringutil"
	"github.com/GrayCodeAI/trace/cli/trailers"
	"github.com/GrayCodeAI/trace/perf"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// ttyResult represents the outcome of a TTY confirmation prompt.
type ttyResult int

const (
	ttyResultLink       ttyResult = iota // Link: add the checkpoint trailer
	ttyResultSkip                        // Skip: don't add the trailer
	ttyResultLinkAlways                  // Link and remember: add trailer + save "always" preference
)

// askConfirmTTY prompts the user via /dev/tty whether to link a commit to session context.
// This requires a controlling terminal — callers must check
// interactive.CanPromptInteractively() first and handle the no-TTY case
// (agent subprocesses, CI) themselves.
//
// header is displayed as the first line (e.g., "Trace: Active Claude Code session").
// detail lines are displayed indented below the header.
func askConfirmTTY(header string, details []string, prompt string, defaultYes bool) ttyResult {
	defaultResult := ttyResultSkip
	if defaultYes {
		defaultResult = ttyResultLink
	}

	// In test mode, don't try to interact with the real TTY — use the default.
	if interactive.UnderTest() {
		return defaultResult
	}

	// Open /dev/tty for both reading and writing.
	// This is the controlling terminal, which works even when stdin/stderr are redirected
	// (e.g., human runs git commit -m where stdin is not a pipe).
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return defaultResult
	}
	defer tty.Close()

	// Write to tty directly, not stderr, since git hooks may redirect stderr to /dev/null
	fmt.Fprintf(tty, "\n%s\n", header)
	for _, line := range details {
		fmt.Fprintf(tty, "  %s\n", line)
	}

	// Show prompt with option descriptions
	fmt.Fprintf(tty, "\n%s\n", prompt)
	if defaultYes {
		fmt.Fprint(tty, "  [Y]es / [n]o / [a]lways (remember my choice): ")
	} else {
		fmt.Fprint(tty, "  [y]es / [N]o / [a]lways (remember my choice): ")
	}

	// Read response
	reader := bufio.NewReader(tty)
	response, err := reader.ReadString('\n')
	if err != nil {
		return defaultResult
	}

	response = strings.TrimSpace(strings.ToLower(response))
	switch response {
	case "y", "yes":
		return ttyResultLink
	case "n", "no":
		return ttyResultSkip
	case "a", "always":
		return ttyResultLinkAlways
	default:
		// Empty or invalid input - use default
		return defaultResult
	}
}

// saveCommitLinkingAlways persists commit_linking = "always" to settings.local.json.
// Uses raw JSON merge to set only the commit_linking field without affecting other
// fields. This avoids writing unintended defaults (e.g., enabled: true) when the
// local settings file doesn't exist yet.
func saveCommitLinkingAlways(ctx context.Context) error {
	localPath, err := paths.AbsPath(ctx, settings.TraceSettingsLocalFile)
	if err != nil {
		return fmt.Errorf("resolving local settings path: %w", err)
	}

	// Read existing file as raw JSON map to preserve all existing fields.
	// If the file doesn't exist, start with an empty map so we only write commit_linking.
	var raw map[string]json.RawMessage
	data, readErr := os.ReadFile(localPath) //nolint:gosec // path is from AbsPath
	if readErr == nil {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parsing local settings: %w", err)
		}
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("reading local settings: %w", readErr)
	}
	if raw == nil {
		raw = make(map[string]json.RawMessage)
	}

	raw["commit_linking"] = json.RawMessage(`"` + settings.CommitLinkingAlways + `"`)

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling local settings: %w", err)
	}
	out = append(out, '\n')

	if err := os.MkdirAll(filepath.Dir(localPath), 0o750); err != nil {
		return fmt.Errorf("creating settings directory: %w", err)
	}
	//nolint:gosec // G306: settings file is config, not secrets; 0o644 is appropriate
	if err := os.WriteFile(localPath, out, 0o644); err != nil {
		return fmt.Errorf("writing local settings: %w", err)
	}
	return nil
}

// CommitMsg is called by the git commit-msg hook after the user edits the message.
// If the message contains only our trailer (no actual user content), strip it
// so git will abort the commit due to empty message.

func (s *ManualCommitStrategy) CommitMsg(_ context.Context, commitMsgFile string) error { //nolint:unparam // error return is part of the hook contract; callers check it
	content, err := os.ReadFile(commitMsgFile) //nolint:gosec // Path comes from git hook
	if err != nil {
		return nil //nolint:nilerr // Hook must be silent on failure
	}

	message := string(content)

	// Check if our trailer is present (ParseCheckpoint validates format, so found==true means valid)
	if _, found := trailers.ParseCheckpoint(message); !found {
		// No trailer, nothing to do
		return nil
	}

	// Check if there's any user content (non-comment, non-trailer lines)
	if !hasUserContent(message) {
		// No user content - strip the trailer so git aborts
		message = stripCheckpointTrailer(message)
		if err := os.WriteFile(commitMsgFile, []byte(message), 0o600); err != nil {
			return nil //nolint:nilerr // Hook must be silent on failure
		}
	}

	return nil
}

// PostRewrite is called by the git post-rewrite hook after amend/rebase
// operations. It keeps session linkage aligned with rewritten commit SHAs in
// the current worktree.
func (s *ManualCommitStrategy) PostRewrite(ctx context.Context, rewriteType string, r io.Reader) error {
	logCtx := logging.WithComponent(ctx, "checkpoint")
	if rewriteType != "amend" && rewriteType != "rebase" {
		logging.Debug(
			logCtx, "post-rewrite: unsupported rewrite type, skipping",
			slog.String("rewrite_type", rewriteType),
		)
		return nil
	}

	rewrites, err := parsePostRewritePairs(r)
	if err != nil {
		return err
	}
	if len(rewrites) == 0 {
		return nil
	}

	worktreePath, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return nil //nolint:nilerr // Hook must be resilient
	}

	sessions, err := s.findSessionsForWorktree(ctx, worktreePath)
	if err != nil || len(sessions) == 0 {
		return nil //nolint:nilerr // Hook must be resilient
	}

	repo, err := OpenRepository(ctx)
	if err != nil {
		return nil //nolint:nilerr // Hook must be resilient
	}

	for _, state := range sessions {
		changed, err := s.remapSessionForRewrite(ctx, repo, state, rewrites)
		if err != nil {
			logging.Warn(
				logCtx, "post-rewrite: failed to remap session linkage",
				slog.String("session_id", state.SessionID),
				slog.String("error", err.Error()),
			)
			continue
		}
		if !changed {
			continue
		}
		if err := s.saveSessionState(ctx, state); err != nil {
			logging.Warn(
				logCtx, "post-rewrite: failed to save remapped session state",
				slog.String("session_id", state.SessionID),
				slog.String("error", err.Error()),
			)
			continue
		}
		logging.Info(
			logCtx, "post-rewrite: remapped session linkage",
			slog.String("session_id", state.SessionID),
			slog.String("rewrite_type", rewriteType),
			slog.String("base_commit", truncateHash(state.BaseCommit)),
		)
	}

	return nil
}

func parsePostRewritePairs(r io.Reader) ([]rewritePair, error) {
	scanner := bufio.NewScanner(r)
	var pairs []rewritePair
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("invalid post-rewrite mapping line: %q", line)
		}
		pairs = append(pairs, rewritePair{
			OldSHA: fields[0],
			NewSHA: fields[1],
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan post-rewrite input: %w", err)
	}
	return pairs, nil
}

// hasUserContent checks if the message has any content besides comments and our trailer.
func hasUserContent(message string) bool {
	trailerPrefix := trailers.CheckpointTrailerKey + ":"
	for _, line := range strings.Split(message, "\n") {
		trimmed := strings.TrimSpace(line)
		// Skip empty lines
		if trimmed == "" {
			continue
		}
		// Skip comment lines
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Skip our trailer line
		if strings.HasPrefix(trimmed, trailerPrefix) {
			continue
		}
		// Found user content
		return true
	}
	return false
}

// stripCheckpointTrailer removes the Trace-Checkpoint trailer line from the message.
func stripCheckpointTrailer(message string) string {
	trailerPrefix := trailers.CheckpointTrailerKey + ":"
	var result []string
	for _, line := range strings.Split(message, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), trailerPrefix) {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

// isGitSequenceOperation checks if git is currently in the middle of a rebase,
// cherry-pick, or revert operation. During these operations, commits are being
// replayed and should not be linked to agent sessions.
//
// Detects:
//   - rebase: .git/rebase-merge/ or .git/rebase-apply/ directories
//   - cherry-pick: .git/CHERRY_PICK_HEAD file
//   - revert: .git/REVERT_HEAD file
func isGitSequenceOperation(ctx context.Context) bool {
	// Get git directory (handles worktrees and relative paths correctly)
	gitDir, err := GetGitDir(ctx)
	if err != nil {
		return false // Can't determine, assume not in sequence operation
	}

	// Check for rebase state directories
	if _, err := os.Stat(filepath.Join(gitDir, "rebase-merge")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(gitDir, "rebase-apply")); err == nil {
		return true
	}

	// Check for cherry-pick and revert state files
	if _, err := os.Stat(filepath.Join(gitDir, "CHERRY_PICK_HEAD")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(gitDir, "REVERT_HEAD")); err == nil {
		return true
	}

	return false
}

// PrepareCommitMsg is called by the git prepare-commit-msg hook.
// Adds an Trace-Checkpoint trailer to the commit message with a stable checkpoint ID.
// Only adds a trailer if there's actually new session content to condense.
// The actual condensation happens in PostCommit - if the user removes the trailer,
// the commit will not be linked to the session (useful for "manual" commits).
// For amended commits, preserves the existing checkpoint ID.
//
// The source parameter indicates how the commit was initiated:
//   - "" or "template": normal editor flow - adds trailer with explanatory comment
//   - "message": using -m or -F flag - prompts user interactively via /dev/tty
//   - "merge", "squash": skip trailer entirely (auto-generated messages)
//   - "commit": amend operation - preserves existing trailer or restores from LastCheckpointID
//

func (s *ManualCommitStrategy) PrepareCommitMsg(ctx context.Context, commitMsgFile string, source string) error {
	logCtx := logging.WithComponent(ctx, "checkpoint")

	// Skip during rebase, cherry-pick, or revert operations
	// These are replaying existing commits and should not be linked to agent sessions
	if isGitSequenceOperation(ctx) {
		logging.Debug(
			logCtx, "prepare-commit-msg: skipped during git sequence operation",
			slog.String("strategy", "manual-commit"),
			slog.String("source", source),
		)
		return nil
	}

	// Skip for merge and squash sources
	// These are auto-generated messages - not from Claude sessions
	switch source {
	case "merge", "squash":
		logging.Debug(
			logCtx, "prepare-commit-msg: skipped for source",
			slog.String("strategy", "manual-commit"),
			slog.String("source", source),
		)
		return nil
	}

	// Handle amend (source="commit") separately: preserve or restore trailer
	if source == "commit" {
		return s.handleAmendCommitMsg(ctx, commitMsgFile)
	}

	_, openRepoSpan := perf.Start(ctx, "open_repository")
	repo, err := OpenRepository(ctx)
	if err != nil {
		openRepoSpan.RecordError(err)
		openRepoSpan.End()
		return nil
	}
	openRepoSpan.End()

	_, findSessionsSpan := perf.Start(ctx, "find_sessions_for_worktree")
	worktreePath, err := paths.WorktreeRoot(ctx)
	if err != nil {
		findSessionsSpan.RecordError(err)
		findSessionsSpan.End()
		return nil
	}

	// Find all active sessions for this worktree
	// We match by worktree (not BaseCommit) because the user may have made
	// intermediate commits without entering new prompts, causing HEAD to diverge
	sessions, err := s.findSessionsForWorktree(ctx, worktreePath)
	if err != nil || len(sessions) == 0 {
		findSessionsSpan.RecordError(err)
		findSessionsSpan.End()
		// No active sessions or error listing - silently skip (hooks must be resilient)
		logging.Debug(
			logCtx, "prepare-commit-msg: no active sessions",
			slog.String("strategy", "manual-commit"),
			slog.String("source", source),
		)
		return nil
	}
	findSessionsSpan.End()

	s.warnIfAttributionDiverged(ctx, sessions)

	// Fast path: skip content detection for mid-turn agent commits.
	if s.tryAgentCommitFastPath(ctx, commitMsgFile, sessions, source) {
		return nil
	}

	// Check if any session has new content to condense
	_, filterSessionsSpan := perf.Start(ctx, "filter_sessions_with_content")
	sessionsWithContent := s.filterSessionsWithNewContent(ctx, repo, sessions)
	filterSessionsSpan.End()

	if len(sessionsWithContent) == 0 {
		// No new content — no trailer needed
		logging.Debug(
			logCtx, "prepare-commit-msg: no content to link",
			slog.String("strategy", "manual-commit"),
			slog.String("source", source),
			slog.Int("sessions_found", len(sessions)),
		)
		return nil
	}

	// Read current commit message
	_, readCommitMessageSpan := perf.Start(ctx, "read_commit_message")
	content, err := os.ReadFile(commitMsgFile) //nolint:gosec // commitMsgFile is provided by git hook
	if err != nil {
		readCommitMessageSpan.RecordError(err)
		readCommitMessageSpan.End()
		return nil
	}

	message := string(content)

	// Check if trailer already exists (ParseCheckpoint validates format, so found==true means valid)
	if existingCpID, found := trailers.ParseCheckpoint(message); found {
		readCommitMessageSpan.End()
		// Trailer already exists (e.g., amend) - keep it
		logging.Debug(
			logCtx, "prepare-commit-msg: trailer already exists",
			slog.String("strategy", "manual-commit"),
			slog.String("source", source),
			slog.String("existing_checkpoint_id", existingCpID.String()),
		)
		return nil
	}
	readCommitMessageSpan.End()

	// Generate a fresh checkpoint ID and resolve session metadata
	_, resolveMetadataSpan := perf.Start(ctx, "resolve_session_metadata")
	checkpointID, err := id.Generate()
	if err != nil {
		resolveMetadataSpan.RecordError(err)
		resolveMetadataSpan.End()
		return fmt.Errorf("failed to generate checkpoint ID: %w", err)
	}

	// Determine agent type and last prompt from session
	var agentType types.AgentType
	var lastPrompt string
	if len(sessionsWithContent) > 0 {
		firstSession := sessionsWithContent[0]
		if firstSession.AgentType != "" {
			agentType = firstSession.AgentType
		}
		lastPrompt = s.getLastPrompt(ctx, repo, firstSession)
	}

	// Prepare prompt for display: collapse newlines/whitespace, then truncate (rune-safe)
	displayPrompt := stringutil.TruncateRunes(stringutil.CollapseWhitespace(lastPrompt), 80, "...")

	// Load commit_linking setting to decide whether to prompt
	commitLinking := settings.CommitLinkingPrompt // safe default
	if stngs, loadErr := settings.Load(ctx); loadErr == nil {
		commitLinking = stngs.GetCommitLinking()
	}
	resolveMetadataSpan.End()

	// Add trailer differently based on commit source
	// NOTE: TTY confirmation (askConfirmTTY) is intentionally NOT wrapped in a span
	// because it blocks on user input and would skew timing.
	switch source {
	case "message":
		// Using -m or -F: behavior depends on TTY availability and commit_linking setting
		switch {
		case !interactive.CanPromptInteractively():
			// No TTY (agent subprocess, CI) — auto-link without prompting
			message = addCheckpointTrailer(message, checkpointID)
		case commitLinking == settings.CommitLinkingAlways:
			// User previously chose "always" — auto-link without prompting
			message = addCheckpointTrailer(message, checkpointID)
		default:
			// Human at terminal — prompt interactively
			header := "Trace: Active " + string(agentType) + " session detected"
			var details []string
			if displayPrompt != "" {
				details = append(details, "Last prompt: "+displayPrompt)
			}

			result := askConfirmTTY(header, details, "Link this commit to session context?", true)
			if result == ttyResultSkip {
				logging.Debug(
					logCtx, "prepare-commit-msg: user declined trailer",
					slog.String("strategy", "manual-commit"),
					slog.String("source", source),
				)
				return nil
			}
			if result == ttyResultLinkAlways {
				// Persist preference so future commits auto-link (non-fatal if it fails)
				if saveErr := saveCommitLinkingAlways(ctx); saveErr != nil {
					logging.Warn(
						logCtx, "prepare-commit-msg: failed to save commit_linking=always",
						slog.String("error", saveErr.Error()),
					)
				}
			}
			message = addCheckpointTrailer(message, checkpointID)
		}
	default:
		// Normal editor flow: add trailer with explanatory comment (will be stripped by git)
		message = addCheckpointTrailerWithComment(message, checkpointID, string(agentType), displayPrompt)
	}

	logging.Info(
		logCtx, "prepare-commit-msg: trailer added",
		slog.String("strategy", "manual-commit"),
		slog.String("source", source),
		slog.String("checkpoint_id", checkpointID.String()),
	)

	// Write updated message back
	_, writeCommitMessageSpan := perf.Start(ctx, "write_commit_message")
	if err := os.WriteFile(commitMsgFile, []byte(message), 0o600); err != nil { //nolint:gosec // path from git hook arg
		writeCommitMessageSpan.RecordError(err)
		writeCommitMessageSpan.End()
		return nil
	}
	writeCommitMessageSpan.End()

	return nil
}

// handleAmendCommitMsg handles the prepare-commit-msg hook for amend operations
// (source="commit"). It preserves existing trailers or restores from LastCheckpointID.
func (s *ManualCommitStrategy) handleAmendCommitMsg(ctx context.Context, commitMsgFile string) error {
	logCtx := logging.WithComponent(ctx, "checkpoint")
	// Read current commit message
	content, err := os.ReadFile(commitMsgFile) //nolint:gosec // commitMsgFile is provided by git hook
	if err != nil {
		return nil //nolint:nilerr // Hook must be silent on failure
	}

	message := string(content)

	// If message already has a trailer, keep it unchanged
	if existingCpID, found := trailers.ParseCheckpoint(message); found {
		logging.Debug(
			logCtx, "prepare-commit-msg: amend preserves existing trailer",
			slog.String("strategy", "manual-commit"),
			slog.String("checkpoint_id", existingCpID.String()),
		)
		return nil
	}

	// No trailer in message — check if any session has LastCheckpointID to restore
	worktreePath, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return nil //nolint:nilerr // Hook must be silent on failure
	}

	sessions, err := s.findSessionsForWorktree(ctx, worktreePath)
	if err != nil || len(sessions) == 0 {
		return nil //nolint:nilerr // No sessions - nothing to restore
	}

	// For amend, HEAD^ is the commit being amended, and HEAD is where we are now.
	// We need to match sessions whose BaseCommit equals HEAD (the commit being amended
	// was created from this base). This prevents stale sessions from injecting
	// unrelated checkpoint IDs.
	repo, repoErr := OpenRepository(ctx)
	if repoErr != nil {
		return nil //nolint:nilerr // Hook must be silent on failure
	}
	head, headErr := repo.Head()
	if headErr != nil {
		return nil //nolint:nilerr // Hook must be silent on failure
	}
	currentHead := head.Hash().String()

	// Find first matching session with LastCheckpointID to restore.
	// LastCheckpointID is set after condensation completes.
	for _, state := range sessions {
		if state.BaseCommit != currentHead {
			continue
		}
		if state.LastCheckpointID.IsEmpty() {
			continue
		}
		cpID := state.LastCheckpointID
		source := "LastCheckpointID"

		// Restore the trailer
		message = addCheckpointTrailer(message, cpID)
		if writeErr := os.WriteFile(commitMsgFile, []byte(message), 0o600); writeErr != nil { //nolint:gosec // path from git hook arg
			return nil //nolint:nilerr // Hook must be silent on failure
		}

		logging.Info(
			logCtx, "prepare-commit-msg: restored trailer on amend",
			slog.String("strategy", "manual-commit"),
			slog.String("checkpoint_id", cpID.String()),
			slog.String("session_id", state.SessionID),
			slog.String("source", source),
		)
		return nil
	}

	// No checkpoint ID found - leave message unchanged
	logging.Debug(
		logCtx, "prepare-commit-msg: amend with no checkpoint to restore",
		slog.String("strategy", "manual-commit"),
	)
	return nil
}

// PostCommit is called by the git post-commit hook after a commit is created.
// Uses the session state machine to determine what action to take per session:
//   - ACTIVE → condense immediately (each commit gets its own checkpoint)
//   - IDLE → condense immediately
//   - ENDED → condense if files touched, discard if empty
//
// After condensation for ACTIVE sessions, remaining uncommitted files are
// carried forward to a new shadow branch so the next commit gets its own checkpoint.
//
// Shadow branches are only deleted when ALL sessions sharing the branch are non-active
// and were condensed during this PostCommit.

// postCommitActionHandler implements session.ActionHandler for PostCommit.
// Each session in the loop gets its own handler with per-session context.
// Handler methods use the *State parameter from ApplyTransition (same pointer
// as the state being transitioned) rather than capturing state separately.
type postCommitActionHandler struct {
	s                          *ManualCommitStrategy
	ctx                        context.Context
	repo                       *git.Repository
	checkpointID               id.CheckpointID
	head                       *plumbing.Reference
	commit                     *object.Commit
	newHead                    string
	repoDir                    string
	shadowBranchName           string
	shadowBranchesToDelete     map[string]struct{}
	committedFileSet           map[string]struct{}
	hasNew                     bool
	filesTouchedBefore         []string
	sessionsWithCommittedFiles int // number of processable sessions that have tracked files

	// Cached git objects — resolved once per PostCommit invocation to avoid
	// redundant reads across filesOverlapWithContent, filesWithRemainingAgentChanges,
	// CondenseSession, and calculateSessionAttributions.
	headTree      *object.Tree        // HEAD commit tree (shared across all sessions)
	parentTree    *object.Tree        // HEAD's first parent tree (shared, nil for initial commits)
	shadowRef     *plumbing.Reference // Per-session shadow branch ref (nil if branch doesn't exist)
	shadowTree    *object.Tree        // Per-session shadow commit tree (nil if branch doesn't exist)
	allAgentFiles map[string]struct{} // Union of all sessions' FilesTouched for cross-session attribution

	// Output: set by handler methods, read by caller after TransitionAndLog.
	// condensed is true only when CondenseSession wrote data to the metadata branch.
	// Both failures and skips (no transcript/files) leave condensed=false, which
	// correctly preserves shadow branches and defers FullyCondensed marking.
	condensed bool
}

// parentCommitHash returns the first parent's hash as a string, or empty for initial commits.
func (h *postCommitActionHandler) parentCommitHash() string {
	if h.commit.NumParents() > 0 && len(h.commit.ParentHashes) > 0 {
		return h.commit.ParentHashes[0].String()
	}
	return ""
}

func (h *postCommitActionHandler) HandleCondense(state *session.State) error {
	logCtx := logging.WithComponent(h.ctx, "checkpoint")
	shouldCondense := h.shouldCondenseWithOverlapCheck(state.Phase.IsActive(), state.LastInteractionTime)

	logging.Debug(
		logCtx, "post-commit: HandleCondense decision",
		slog.String("session_id", state.SessionID),
		slog.String("phase", string(state.Phase)),
		slog.Bool("has_new", h.hasNew),
		slog.Bool("should_condense", shouldCondense),
		slog.String("shadow_branch", h.shadowBranchName),
	)

	if shouldCondense {
		h.condensed = h.s.condenseAndUpdateState(h.ctx, h.repo, h.checkpointID, state, h.head, h.shadowBranchName, h.shadowBranchesToDelete, h.committedFileSet, condenseOpts{
			shadowRef:        h.shadowRef,
			headTree:         h.headTree,
			parentTree:       h.parentTree,
			repoDir:          h.repoDir,
			parentCommitHash: h.parentCommitHash(),
			headCommitHash:   h.newHead,
			allAgentFiles:    h.allAgentFiles,
		})
	} else {
		h.s.updateBaseCommitIfChanged(h.ctx, state, h.newHead)
	}
	return nil
}

func (h *postCommitActionHandler) HandleCondenseIfFilesTouched(state *session.State) error {
	logCtx := logging.WithComponent(h.ctx, "checkpoint")
	shouldCondense := len(state.FilesTouched) > 0 && h.shouldCondenseWithOverlapCheck(state.Phase.IsActive(), state.LastInteractionTime)

	logging.Debug(
		logCtx, "post-commit: HandleCondenseIfFilesTouched decision",
		slog.String("session_id", state.SessionID),
		slog.String("phase", string(state.Phase)),
		slog.Bool("has_new", h.hasNew),
		slog.Int("files_touched", len(state.FilesTouched)),
		slog.Bool("should_condense", shouldCondense),
		slog.String("shadow_branch", h.shadowBranchName),
	)

	if shouldCondense {
		h.condensed = h.s.condenseAndUpdateState(h.ctx, h.repo, h.checkpointID, state, h.head, h.shadowBranchName, h.shadowBranchesToDelete, h.committedFileSet, condenseOpts{
			shadowRef:        h.shadowRef,
			headTree:         h.headTree,
			parentTree:       h.parentTree,
			repoDir:          h.repoDir,
			parentCommitHash: h.parentCommitHash(),
			headCommitHash:   h.newHead,
			allAgentFiles:    h.allAgentFiles,
		})
	} else {
		h.s.updateBaseCommitIfChanged(h.ctx, state, h.newHead)
	}
	return nil
}

// shouldCondenseWithOverlapCheck returns true if the session should be condensed
// into this commit. Active sessions with recent interaction condense unless they
// have no tracked files and another session claims the committed files (read-only
// gate). Stale ACTIVE and IDLE/ENDED sessions require file overlap evidence
// between tracked files and committed files.
func (h *postCommitActionHandler) shouldCondenseWithOverlapCheck(isActive bool, lastInteraction *time.Time) bool {
	if !h.hasNew {
		return false
	}
	// ACTIVE sessions with recent interaction: skip the overlap check.
	// PrepareCommitMsg already validated this commit is session-related
	// (added trailer). The overlap check is only meaningful when we need
	// heuristic evidence that a commit was related to the session.
	//
	// Exception: when another session's tracked files overlap with the
	// committed files, skip this ACTIVE session if it has no tracked files
	// itself. This prevents read-only sessions (e.g., codex exec from tools
	// like summarize) from being condensed when a different session's commit
	// triggers PostCommit. When no other session claims the committed files,
	// the ACTIVE session is assumed to own the commit.
	//
	// We check LastInteractionTime to avoid condensing stale ACTIVE sessions
	// (agent killed without Stop hook) into every subsequent commit. A stale
	// session has no recent interaction and falls through to the overlap check.
	if isActive && isRecentInteraction(lastInteraction) {
		if h.sessionsWithCommittedFiles > 0 && len(h.filesTouchedBefore) == 0 {
			logging.Debug(
				h.ctx, "post-commit: skipping read-only ACTIVE session (no tracked files, other sessions claim committed files)",
				slog.Int("sessions_with_committed_files", h.sessionsWithCommittedFiles),
			)
			return false
		}
		return true
	}
	if len(h.filesTouchedBefore) == 0 {
		return false // No files tracked = no overlap evidence
	}
	// Only check files that were actually changed in this commit.
	// Without this, files that exist in the tree but weren't changed
	// would pass the "modified file" check in filesOverlapWithContent
	// (because the file exists in the parent tree), causing stale
	// sessions to be incorrectly condensed.
	var committedTouchedFiles []string
	for _, f := range h.filesTouchedBefore {
		if _, ok := h.committedFileSet[f]; ok {
			committedTouchedFiles = append(committedTouchedFiles, f)
		}
	}
	if len(committedTouchedFiles) == 0 {
		return false
	}
	return filesOverlapWithContent(h.ctx, h.repo, h.shadowBranchName, h.commit, committedTouchedFiles, overlapOpts{
		headTree:      h.headTree,
		shadowTree:    h.shadowTree,
		parentTree:    h.parentTree,
		hasParentTree: true,
	})
}

const (
	staleEndedSessionWarnThreshold = 3                   // warn when ≥ this many stale sessions
	staleEndedSessionWarnInterval  = 24 * time.Hour      // rate-limit window
	staleEndedSessionWarnFile      = ".warn-stale-ended" // sentinel file name in trace-sessions/
)

// stderrWriter is the destination for user-facing warnings emitted outside of
// Cobra command output (e.g. from PostCommit). Tests can swap this to capture
// output without mutating the process-global os.Stderr.
var stderrWriter io.Writer = os.Stderr

// warnStaleEndedSessions emits a rate-limited warning to stderr when too many
// non-FullyCondensed ENDED sessions are accumulating.
func warnStaleEndedSessions(ctx context.Context, count int) {
	warnStaleEndedSessionsTo(ctx, count, stderrWriter)
}
