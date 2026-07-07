package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/geminicli"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/interactive"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cli/trailers"

	"github.com/go-git/go-git/v6/plumbing/object"
	"golang.org/x/term"
)

// runExplainBranchDefault shows all checkpoints on the current branch grouped by date.
// This is a convenience wrapper that calls runExplainBranchWithFilter with no filter.
func runExplainBranchDefault(ctx context.Context, w io.Writer, noPager bool) error {
	return runExplainBranchWithFilter(ctx, w, noPager, "")
}

// outputExplainContent outputs content with optional pager support.
func outputExplainContent(w io.Writer, content string, noPager bool) {
	if noPager {
		fmt.Fprint(w, content)
	} else {
		outputWithPager(w, content)
	}
}

// runExplainCommit looks up the checkpoint associated with a commit.
// Extracts the Trace-Checkpoint trailer and delegates to checkpoint detail view.
// If no trailer found, shows a message indicating no associated checkpoint.
func runExplainCommit(ctx context.Context, w, errW io.Writer, commitRef string, noPager, verbose, full, rawTranscript, generate, force, searchAll bool) error {
	repo, err := openRepository(ctx)
	if err != nil {
		return fmt.Errorf("not a git repository: %w", err)
	}

	// Resolve the commit reference, erroring on hex-prefix ambiguity
	// instead of silently picking the first matching commit.
	hash, ambiguousMatches, err := resolveCommitUnambiguous(repo, commitRef)
	if err != nil {
		if errors.Is(err, errAmbiguousCommitPrefix) {
			renderAmbiguousPrefixFailure(errW, commitRef, "commits", buildAmbiguousCommitMatches(repo, ambiguousMatches))
			return NewSilentError(err)
		}
		return renderExplainFailure(errW, "Commit not found", []explainRow{
			{Label: "ref", Value: commitRef},
		}, fmt.Errorf("commit not found: %s", commitRef))
	}

	commit, err := repo.CommitObject(hash)
	if err != nil {
		return fmt.Errorf("failed to get commit: %w", err)
	}

	// Extract Trace-Checkpoint trailer
	checkpointID, hasCheckpoint := trailers.ParseCheckpoint(commit.Message)
	if !hasCheckpoint {
		// Side-effect modes must error so scripts can distinguish "done"
		// from "didn't happen"; read-only modes print a friendly message.
		if generate || rawTranscript {
			return fmt.Errorf("cannot %s: commit %s has no Trace-Checkpoint trailer", generateOrRawLabel(generate), abbreviateCommitHash(repo, hash))
		}
		printNoTrailerMessage(w, repo, hash)
		return nil
	}

	// Delegate to checkpoint detail view, forwarding the full flag set so
	// --generate / --raw-transcript / --force work via --commit as well.
	return runExplainCheckpoint(ctx, w, errW, checkpointID.String(), noPager, verbose, full, rawTranscript, generate, force, searchAll)
}

// formatSessionInfo formats session information for display.
//
// NOTE: This function has no production caller — `trace explain --session`
// flows through formatBranchCheckpoints (the list view filtered by session),
// not through here. It is kept for tests that exercise the per-checkpoint
// markdown body shape used elsewhere; restyling it for the brand format was
// not worth the diff. If the CLI ever grows a session-detail surface, revisit.
func formatSessionInfo(session *strategy.Session, sourceRef string, checkpoints []checkpointDetail) string {
	var sb strings.Builder

	// Session header
	fmt.Fprintf(&sb, "Session: %s\n", session.ID)
	fmt.Fprintf(&sb, "Strategy: %s\n", session.Strategy)

	if !session.StartTime.IsZero() {
		fmt.Fprintf(&sb, "Started: %s\n", session.StartTime.Format("2006-01-02 15:04:05"))
	}

	if sourceRef != "" {
		fmt.Fprintf(&sb, "Source Ref: %s\n", sourceRef)
	}

	fmt.Fprintf(&sb, "Checkpoints: %d\n", len(checkpoints))

	// Checkpoint details
	for _, cp := range checkpoints {
		sb.WriteString("\n")

		// Checkpoint header
		taskMarker := ""
		if cp.IsTaskCheckpoint {
			taskMarker = " [Task]"
		}
		fmt.Fprintf(&sb, "─── Checkpoint %d [%s] %s%s ───\n",
			cp.Index, cp.ShortID, cp.Timestamp.Format("2006-01-02 15:04"), taskMarker)
		sb.WriteString("\n")

		// Display all interactions in this checkpoint
		for i, inter := range cp.Interactions {
			// For multiple interactions, add a sub-header
			if len(cp.Interactions) > 1 {
				fmt.Fprintf(&sb, "### Interaction %d\n\n", i+1)
			}

			// Prompt section
			if inter.Prompt != "" {
				sb.WriteString("## Prompt\n\n")
				sb.WriteString(inter.Prompt)
				sb.WriteString("\n\n")
			}

			// Response section
			if len(inter.Responses) > 0 {
				sb.WriteString("## Responses\n\n")
				sb.WriteString(strings.Join(inter.Responses, "\n\n"))
				sb.WriteString("\n\n")
			}

			// Files modified for this interaction
			if len(inter.Files) > 0 {
				fmt.Fprintf(&sb, "Files Modified (%d):\n", len(inter.Files))
				for _, file := range inter.Files {
					fmt.Fprintf(&sb, "  - %s\n", file)
				}
				sb.WriteString("\n")
			}
		}

		// If no interactions, show message and/or files
		if len(cp.Interactions) == 0 {
			// Show commit message as summary when no transcript available
			if cp.Message != "" {
				sb.WriteString(cp.Message)
				sb.WriteString("\n\n")
			}
			// Show aggregate files if available
			if len(cp.Files) > 0 {
				fmt.Fprintf(&sb, "Files Modified (%d):\n", len(cp.Files))
				for _, file := range cp.Files {
					fmt.Fprintf(&sb, "  - %s\n", file)
				}
			}
		}
	}

	return sb.String()
}

// pagerLookupEnv is overridable for tests so pager env-gate behavior can be
// asserted without depending on the host's PAGER / LESS settings.
var pagerLookupEnv = os.Getenv

// buildPagerCmd constructs the pager subprocess and injects LESS=-R when the
// default Unix pager is less and the user has not customized PAGER or LESS.
func buildPagerCmd(ctx context.Context) (*exec.Cmd, string) {
	pager := pagerLookupEnv(pagerEnvVar)
	if pager == "" {
		if runtime.GOOS == windowsGOOS {
			pager = "more"
		} else {
			pager = lessPagerName
		}
	}

	cmd := exec.CommandContext(ctx, pager) // #nosec G204 -- pager comes from the PAGER env var (or a hardcoded default), a standard trusted user-configuration mechanism
	if pager == lessPagerName && pagerLookupEnv(pagerEnvVar) == "" && pagerLookupEnv(lessEnvVar) == "" {
		cmd.Env = upsertEnv(os.Environ(), lessEnvVar, "-R")
	}
	return cmd, pager
}

func upsertEnv(env []string, key, value string) []string {
	prefix := key + "="
	entry := prefix + value
	result := make([]string, 0, len(env)+1)
	replaced := false
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			if !replaced {
				result = append(result, entry)
				replaced = true
			}
			continue
		}
		result = append(result, e)
	}
	if !replaced {
		result = append(result, entry)
	}
	return result
}

// removeEnvKey returns env with every entry for key dropped. Useful when a
// outputWithPager outputs content through a pager if stdout is a terminal and content is long.
func outputWithPager(w io.Writer, content string) {
	// Check if we're writing to stdout and it's a terminal
	if f, ok := w.(*os.File); ok && f == os.Stdout && interactive.IsTerminalWriter(w) {
		// Get terminal height
		_, height, err := term.GetSize(int(f.Fd())) //nolint:gosec // G115: same as above
		if err != nil {
			height = 24 // Default fallback
		}

		// Count lines in content
		lineCount := strings.Count(content, "\n")

		// Use pager if content exceeds terminal height
		if lineCount > height-2 {
			// Use context.Background() intentionally — pagers are interactive
			// processes that handle signals (including SIGINT) themselves.
			// Using the cancellable ctx would cause exec.CommandContext to
			// SIGKILL the pager on Ctrl+C, preventing it from restoring
			// terminal state (raw mode, echo, etc.).
			cmd, _ := buildPagerCmd(context.Background())
			cmd.Stdin = strings.NewReader(content)
			cmd.Stdout = f
			cmd.Stderr = os.Stderr

			if err := cmd.Run(); err != nil {
				// Fallback to direct output if pager fails
				fmt.Fprint(w, content)
			}
			return
		}
	}

	// Direct output for non-terminal or short content
	fmt.Fprint(w, content)
}

// Constants for formatting output
const (
	// maxIntentDisplayLength is the maximum length for intent text before truncation
	maxIntentDisplayLength = 80
	// maxMessageDisplayLength is the maximum length for checkpoint messages before truncation
	maxMessageDisplayLength = 80
	// maxPromptDisplayLength is the maximum length for session prompts before truncation
	maxPromptDisplayLength = 60
	// checkpointIDDisplayLength is the number of characters to show from checkpoint IDs
	checkpointIDDisplayLength = 12
)

// formatBranchCheckpoints formats checkpoint information for a branch.
// Groups commits by checkpoint ID and shows the prompt for each checkpoint.
// If sessionFilter is non-empty, only shows checkpoints matching that session ID (or prefix).
func formatBranchCheckpoints(w io.Writer, branchName string, points []strategy.RewindPoint, sessionFilter string) string {
	var sb strings.Builder
	styles := newStatusStyles(w)

	// Filter by session if specified (must happen before counting)
	if sessionFilter != "" {
		var filtered []strategy.RewindPoint
		for _, p := range points {
			if p.SessionID == sessionFilter || strings.HasPrefix(p.SessionID, sessionFilter) {
				filtered = append(filtered, p)
			}
		}
		points = filtered
	}

	// Group by checkpoint ID so the count matches the rendered group count
	groups := groupByCheckpointID(points)

	branchRows := []explainRow{
		{Label: "branch", Value: branchName},
	}
	if sessionFilter != "" {
		branchRows = append(branchRows, explainRow{Label: "session", Value: sessionFilter})
	}
	branchRows = append(branchRows, explainRow{Label: "checkpoints", Value: strconv.Itoa(len(groups))})

	sb.WriteString(styles.metadataRows(branchRows))
	sb.WriteString("\n")

	if len(groups) == 0 {
		sb.WriteString("No checkpoints found on this branch.\n")
		sb.WriteString("Checkpoints will appear here after you save changes during an agent session.\n")
		return sb.String()
	}

	// Output each checkpoint group
	for _, group := range groups {
		formatCheckpointGroup(&sb, group, styles)
		sb.WriteString("\n")
	}

	return sb.String()
}

// checkpointGroup represents a group of commits sharing the same checkpoint ID.
type checkpointGroup struct {
	checkpointID string
	prompt       string
	isTemporary  bool // true if any commit is not logs-only (can be rewound)
	isTask       bool // true if this is a task checkpoint
	commits      []commitEntry
}

// commitEntry represents a single git commit within a checkpoint.
type commitEntry struct {
	date    time.Time
	gitSHA  string // short git SHA
	message string
}

// groupByCheckpointID groups rewind points by their checkpoint ID.
// Returns groups sorted by latest commit timestamp (most recent first).
func groupByCheckpointID(points []strategy.RewindPoint) []checkpointGroup {
	if len(points) == 0 {
		return nil
	}

	// Build map of checkpoint ID -> group
	groupMap := make(map[string]*checkpointGroup)
	var order []string // Track insertion order for stable iteration

	for _, point := range points {
		// Determine the checkpoint ID to use for grouping
		cpID := point.CheckpointID.String()
		if cpID == "" {
			// Temporary checkpoints: group by session ID to preserve per-session prompts
			// Use session ID prefix for readability (format: YYYY-MM-DD-uuid)
			cpID = point.SessionID
			if cpID == "" {
				cpID = "temporary" // Fallback if no session ID
			}
		}

		group, exists := groupMap[cpID]
		if !exists {
			group = &checkpointGroup{
				checkpointID: cpID,
				prompt:       point.SessionPrompt,
				isTemporary:  !point.IsLogsOnly,
				isTask:       point.IsTaskCheckpoint,
			}
			groupMap[cpID] = group
			order = append(order, cpID)
		}

		// Short git SHA (7 chars)
		gitSHA := point.ID
		if len(gitSHA) > 7 {
			gitSHA = gitSHA[:7]
		}

		group.commits = append(group.commits, commitEntry{
			date:    point.Date,
			gitSHA:  gitSHA,
			message: point.Message,
		})

		// Update flags - if any commit is temporary/task, the group is too
		if !point.IsLogsOnly {
			group.isTemporary = true
		}
		if point.IsTaskCheckpoint {
			group.isTask = true
		}
		// Update prompt if the group's prompt is empty but this point has one
		if group.prompt == "" && point.SessionPrompt != "" {
			group.prompt = point.SessionPrompt
		}
	}

	// Sort commits within each group by date (most recent first)
	for _, group := range groupMap {
		sort.Slice(group.commits, func(i, j int) bool {
			return group.commits[i].date.After(group.commits[j].date)
		})
	}

	// Build result slice in order, then sort by latest commit
	result := make([]checkpointGroup, 0, len(order))
	for _, cpID := range order {
		result = append(result, *groupMap[cpID])
	}

	// Sort groups by latest commit timestamp (most recent first)
	sort.Slice(result, func(i, j int) bool {
		// Each group's commits are already sorted, so first commit is latest
		if len(result[i].commits) == 0 {
			return false
		}
		if len(result[j].commits) == 0 {
			return true
		}
		return result[i].commits[0].date.After(result[j].commits[0].date)
	})

	return result
}

// formatCheckpointGroup formats a single checkpoint group for display.
// The list view headline puts the checkpoint ID first (in bold orange),
// followed by indicators and the prompt — which cascades from
// SessionPrompt → latest commit message → dimmed `(no prompt recorded)`.
func formatCheckpointGroup(sb *strings.Builder, group checkpointGroup, styles statusStyles) {
	cpID := group.checkpointID
	if len(cpID) > checkpointIDDisplayLength {
		cpID = cpID[:checkpointIDDisplayLength]
	}

	// Indicators (Task / temporary). Skip [temporary] when cpID already says so.
	var indicators []string
	if group.isTask {
		indicators = append(indicators, "[Task]")
	}
	if group.isTemporary && cpID != "temporary" {
		indicators = append(indicators, "[temporary]")
	}

	// Prompt cascade: SessionPrompt → latest commit message → dimmed placeholder.
	// Quote user prompts; commit subjects render bare.
	var promptText string
	var promptIsPlaceholder bool
	switch {
	case group.prompt != "":
		promptText = fmt.Sprintf("%q", strategy.TruncateDescription(group.prompt, maxPromptDisplayLength))
	case len(group.commits) > 0 && group.commits[0].message != "":
		promptText = strategy.TruncateDescription(group.commits[0].message, maxPromptDisplayLength)
	default:
		promptText = "(no prompt recorded)"
		promptIsPlaceholder = true
	}
	if promptIsPlaceholder {
		promptText = styles.render(styles.dim, promptText)
	}

	// Build suffix: "[Task]  [temporary]  <prompt>" with two-space separators.
	parts := append([]string{}, indicators...)
	parts = append(parts, promptText)
	suffix := strings.Join(parts, "  ")

	sb.WriteString(styles.listIdentityBullet(cpID, suffix))

	// List commits under this checkpoint.
	for _, commit := range group.commits {
		dateTimeStr := commit.date.Format("01-02 15:04")
		message := strategy.TruncateDescription(commit.message, maxMessageDisplayLength)
		fmt.Fprintf(sb, "  %s (%s) %s\n", dateTimeStr, commit.gitSHA, message)
	}
}

// countLines counts the number of lines in a byte slice.
// For JSONL content (where each line ends with \n), this returns the line count.
// Empty content returns 0.
func countLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	count := 0
	for _, b := range content {
		if b == '\n' {
			count++
		}
	}
	return count
}

// transcriptOffset returns the appropriate offset for scoping a transcript.
// For Claude Code (JSONL), this is the line count. For Gemini (JSON), this is the message count.
func transcriptOffset(transcriptBytes []byte, agentType types.AgentType) int {
	switch agentType {
	case agent.AgentTypeGemini:
		t, err := geminicli.ParseTranscript(transcriptBytes)
		if err != nil {
			return 0
		}
		return len(t.Messages)
	case agent.AgentTypeClaudeCode, agent.AgentTypeOpenCode, agent.AgentTypeCursor, agent.AgentTypeFactoryAIDroid, agent.AgentTypeUnknown:
		return countLines(transcriptBytes)
	}
	return countLines(transcriptBytes)
}

// hasCodeChanges returns true if the commit has changes to non-metadata files.
// Uses a full tree diff to distinguish code changes from .trace/ metadata-only changes.
// Returns false only if the commit has a parent AND only modified .trace/ metadata files.
//
// WARNING: This is expensive via go-git (resolves many tree/blob objects from packfiles).
// For list views with many checkpoints, use hasAnyChanges instead.
func hasCodeChanges(commit *object.Commit) bool {
	// First commit on shadow branch captures working copy state - always meaningful
	if commit.NumParents() == 0 {
		return true
	}

	parent, err := commit.Parent(0)
	if err != nil {
		return true // Can't check, assume meaningful
	}

	commitTree, err := commit.Tree()
	if err != nil {
		return true
	}

	parentTree, err := parent.Tree()
	if err != nil {
		return true
	}

	changes, err := parentTree.Diff(commitTree)
	if err != nil {
		return true
	}

	// Check if any non-metadata file was changed
	for _, change := range changes {
		name := change.To.Name
		if name == "" {
			name = change.From.Name
		}
		// Skip .trace/ metadata files
		if !strings.HasPrefix(name, ".trace/") {
			return true
		}
	}

	return false
}

// hasAnyChanges is a lightweight alternative to hasCodeChanges that compares
// tree hashes without doing a full diff. Returns true if the commit's tree
// differs from its parent's tree. This may include metadata-only changes,
// but is O(1) instead of O(files) — suitable for list views.
func hasAnyChanges(commit *object.Commit) bool {
	if commit.NumParents() == 0 {
		return true
	}
	parent, err := commit.Parent(0)
	if err != nil {
		return true
	}
	return commit.TreeHash != parent.TreeHash
}
