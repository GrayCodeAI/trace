package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"

	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/checkpoint/remote"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cli/summarize"
	"github.com/GrayCodeAI/trace/cli/trailers"
	transcriptcompact "github.com/GrayCodeAI/trace/cli/transcript/compact"
	"github.com/GrayCodeAI/trace/redact"

	"charm.land/lipgloss/v2"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/storer"
)

// renderAmbiguousPrefixFailure prints a styled failure block describing an
// ambiguous prefix. kind is a noun phrase like "commits" or "temporary
// checkpoints" used in the "matches N <kind>" header row.
func renderAmbiguousPrefixFailure(errW io.Writer, prefix, kind string, matches []ambiguousMatch) {
	styles := newStatusStyles(errW)
	rows := []explainRow{
		{Label: "matches", Value: fmt.Sprintf("%d %s", len(matches), kind)},
	}
	for _, m := range matches {
		ts := ""
		if !m.Timestamp.IsZero() {
			ts = "  " + m.Timestamp.Format("2006-01-02 15:04:05")
		}
		sess := ""
		if m.SessionID != "" {
			sess = "  session " + m.SessionID
		}
		rows = append(rows, explainRow{Label: "", Value: "• " + m.ShortID + ts + sess})
	}
	rows = append(rows, explainRow{Label: "hint", Value: "use a longer prefix or a full SHA"})
	label := fmt.Sprintf("Ambiguous checkpoint prefix %q", prefix)
	fmt.Fprint(errW, styles.renderFailure(label, rows))
}

// renderExplainFailure prints a styled failure block to errW and returns the
// error wrapped as *SilentError so main.go does not double-print. Used at
// every explain call site that has a friendly, structured error to surface.
func renderExplainFailure(errW io.Writer, label string, rows []explainRow, structured error) error {
	fmt.Fprint(errW, newStatusStyles(errW).renderFailure(label, rows))
	return NewSilentError(structured)
}

// buildAmbiguousCommitMatches converts a slice of plumbing.Hash matches
// (from resolveCommitUnambiguous) into ambiguousMatch entries with
// abbreviated short IDs and author timestamps. Caps at 5 entries to keep
// the failure block readable when a short prefix collides on many
// commits.
func buildAmbiguousCommitMatches(repo *git.Repository, hashes []plumbing.Hash) []ambiguousMatch {
	const maxMatches = 5
	matches := make([]ambiguousMatch, 0, len(hashes))
	for i, h := range hashes {
		if i >= maxMatches {
			break
		}
		m := ambiguousMatch{ShortID: abbreviateCommitHash(repo, h)}
		if commit, err := repo.CommitObject(h); err == nil {
			m.Timestamp = commit.Author.When
		}
		matches = append(matches, m)
	}
	return matches
}

// buildAmbiguousCheckpointMatches converts a slice of CheckpointID matches
// into ambiguousMatch entries enriched with timestamps and session IDs from
// the loaded committed-checkpoint listing. Caps at 5 entries to keep the
// failure block readable when a short prefix collides on many checkpoints.
func buildAmbiguousCheckpointMatches(ids []id.CheckpointID, committed []checkpoint.CommittedInfo) []ambiguousMatch {
	const maxMatches = 5
	infoByID := make(map[id.CheckpointID]checkpoint.CommittedInfo, len(committed))
	for _, info := range committed {
		infoByID[info.CheckpointID] = info
	}
	matches := make([]ambiguousMatch, 0, len(ids))
	for i, cpID := range ids {
		if i >= maxMatches {
			break
		}
		m := ambiguousMatch{ShortID: cpID.String()}
		if info, ok := infoByID[cpID]; ok {
			m.Timestamp = info.CreatedAt
			m.SessionID = info.SessionID
		}
		matches = append(matches, m)
	}
	return matches
}

// renderExplainBody routes a markdown body through the brand renderer when
// the writer supports color, and returns the markdown source verbatim
// otherwise. Single point of policy for every explain body section.
func renderExplainBody(w io.Writer, md string) string {
	if !shouldUseColor(w) {
		return md
	}
	rendered, err := defaultRenderTerminalMarkdown(w, md)
	if err != nil {
		logging.Debug(context.Background(), "explain markdown render failed", slog.String("error", err.Error()))
		return md
	}
	return rendered
}

// formatCheckpointOutput formats checkpoint data based on verbosity level.
// When verbose is false: summary only (ID, session, timestamp, tokens, intent).
// When verbose is true: adds files, associated commits, and scoped transcript for this checkpoint.
// When full is true: shows parsed full session transcript instead of scoped transcript.
//
// Transcript scope is controlled by CheckpointTranscriptStart in metadata, which indicates
// where this checkpoint's content begins in the full session transcript.
//
// Author is displayed when available (only for committed checkpoints).
// Associated commits are git commits that reference this checkpoint via Trace-Checkpoint trailer.
func formatCheckpointOutput(summary *checkpoint.CheckpointSummary, content *checkpoint.SessionContent, checkpointID id.CheckpointID, associatedCommits []associatedCommit, author checkpoint.Author, verbose, full bool, w io.Writer) string {
	var sb strings.Builder
	meta := content.Metadata
	styles := newStatusStyles(w)

	// Scope the transcript to this checkpoint's portion
	// If CheckpointTranscriptStart > 0, we slice the transcript to only include
	// content from that point onwards (excluding earlier checkpoint content)
	scopedTranscript := scopeTranscriptForCheckpoint(content.Transcript, meta.GetTranscriptStart(), meta.Agent)

	// Extract prompts from the scoped transcript for intent extraction
	scopedPrompts := extractPromptsFromTranscript(scopedTranscript, meta.Agent)

	sb.WriteString(formatCheckpointHeader(summary, meta, checkpointID, associatedCommits, author, styles))
	sb.WriteString(styles.horizontalRule(styles.width))
	sb.WriteString("\n")

	if meta.Summary != nil {
		md := buildSummaryMarkdown(meta.Summary)
		if verbose || full {
			md += buildFilesMarkdown(meta.FilesTouched)
		}
		if shouldUseColor(w) {
			rendered, err := defaultRenderTerminalMarkdown(w, md)
			if err != nil {
				logging.Debug(context.Background(), "explain markdown render failed", slog.String("error", err.Error()))
				sb.WriteString(md)
			} else {
				sb.WriteString(rendered)
			}
		} else {
			sb.WriteString(md)
		}
	} else {
		intent := extractIntent(scopedPrompts, content.Prompts)

		var files []string
		if verbose || full {
			files = meta.FilesTouched
		}

		hint := fmt.Sprintf("Not generated yet. Run `trace explain --generate %s` to create an AI summary.", checkpointID)
		md := buildNoSummaryMarkdown(intent, files, hint)
		sb.WriteString(renderExplainBody(w, md))
	}

	if verbose || full {
		label := "Transcript (checkpoint scope)"
		if full {
			label = "Transcript (full session)"
		}
		sb.WriteString("\n")
		sb.WriteString(styles.sectionRule(label, styles.width))
		sb.WriteString("\n")
		appendTranscriptSection(&sb, verbose, full, content.Transcript, scopedTranscript, content.Prompts, meta.Agent)
	}

	return sb.String()
}

// appendTranscriptSection appends the appropriate transcript section to the builder
// based on verbosity level. Full mode shows the trace session, verbose shows checkpoint scope.
// fullTranscript is the trace session transcript, scopedContent is either scoped transcript bytes
// or a pre-formatted string (for backwards compat), and scopedFallback is used when scoped parsing fails.
func appendTranscriptSection(sb *strings.Builder, verbose, full bool, fullTranscript, scopedTranscript []byte, scopedFallback string, agentType types.AgentType) {
	switch {
	case full:
		sb.WriteString(formatTranscriptBytes(fullTranscript, "", agentType))

	case verbose:
		sb.WriteString(formatTranscriptBytes(scopedTranscript, scopedFallback, agentType))
	}
}

// formatTranscriptBytes formats transcript bytes into a human-readable string.
// It parses the transcript (JSONL for Claude, JSON for Gemini) and formats it using the condensed format.
// The fallback is used for backwards compatibility when transcript parsing fails or is empty.
func formatTranscriptBytes(transcriptBytes []byte, fallback string, agentType types.AgentType) string {
	if len(transcriptBytes) == 0 {
		if fallback != "" {
			return fallback + "\n"
		}
		return "  (none)\n"
	}

	// transcriptBytes is read from checkpoint storage, which redacts on write.
	condensed, err := summarize.BuildCondensedTranscriptFromBytes(redact.AlreadyRedacted(transcriptBytes), agentType)
	if err != nil || len(condensed) == 0 {
		condensed, err = buildCondensedCompactTranscriptEntries(transcriptBytes)
	}
	if err != nil || len(condensed) == 0 {
		if fallback != "" {
			return fallback + "\n"
		}
		return "  (failed to parse transcript)\n"
	}

	input := summarize.Input{Transcript: condensed}
	return summarize.FormatCondensedTranscript(input)
}

func buildCondensedCompactTranscriptEntries(transcriptBytes []byte) ([]summarize.Entry, error) {
	compactEntries, err := transcriptcompact.BuildCondensedEntries(transcriptBytes)
	if err != nil {
		return nil, fmt.Errorf("parsing compact transcript: %w", err)
	}

	entries := make([]summarize.Entry, 0, len(compactEntries))
	for _, entry := range compactEntries {
		switch entry.Type {
		case "user":
			entries = append(entries, summarize.Entry{Type: summarize.EntryTypeUser, Content: entry.Content})
		case "assistant":
			entries = append(entries, summarize.Entry{Type: summarize.EntryTypeAssistant, Content: entry.Content})
		case "tool": //nolint:goconst // semantic label, not worth a constant
			entries = append(entries, summarize.Entry{Type: summarize.EntryTypeTool, ToolName: entry.ToolName, ToolDetail: entry.ToolDetail})
		}
	}

	if len(entries) == 0 {
		return nil, errors.New("no parseable compact transcript entries")
	}

	return entries, nil
}

// formatCheckpointHeader builds the metadata block above the summary body.
// When color is enabled, values are styled with the shared status palette;
// otherwise the same compact shape is returned as plain text.
func formatCheckpointHeader(
	summary *checkpoint.CheckpointSummary,
	meta checkpoint.CommittedMetadata,
	cpID id.CheckpointID,
	commits []associatedCommit,
	author checkpoint.Author,
	styles statusStyles,
) string {
	var sb strings.Builder

	headline := "● Checkpoint " + cpID.String()
	if styles.colorEnabled {
		bullet := styles.render(lipgloss.NewStyle().Foreground(lipgloss.Color("#fb923c")), "●")
		key := styles.render(styles.bold, "Checkpoint")
		val := styles.render(lipgloss.NewStyle().Foreground(lipgloss.Color("#fb923c")), cpID.String())
		headline = bullet + " " + key + " " + val
	}
	sb.WriteString(headline)
	sb.WriteString("\n")

	writeRow := func(label, value string) {
		paddedLabel := fmt.Sprintf("%-9s", label)
		if styles.colorEnabled {
			paddedLabel = styles.render(styles.dim, paddedLabel)
		}
		fmt.Fprintf(&sb, "  %s%s\n", paddedLabel, value)
	}

	writeRow("session", meta.SessionID)
	writeRow("created", meta.CreatedAt.Format("2006-01-02 15:04:05"))
	if author.Name != "" {
		writeRow("author", fmt.Sprintf("%s <%s>", author.Name, author.Email))
	}

	tokenUsage := meta.TokenUsage
	if tokenUsage == nil && summary != nil {
		tokenUsage = summary.TokenUsage
	}
	if tokenUsage != nil {
		total := tokenUsage.InputTokens + tokenUsage.CacheCreationTokens +
			tokenUsage.CacheReadTokens + tokenUsage.OutputTokens
		tokensVal := formatTokenCount(total)
		if styles.colorEnabled {
			tokensVal = styles.render(styles.yellow, tokensVal)
		}
		writeRow("tokens", tokensVal)
	}

	switch {
	case commits == nil:
	case len(commits) == 0:
		writeRow("commits", "(none on this branch)")
	case len(commits) == 1:
		c := commits[0]
		writeRow("commits", fmt.Sprintf("%s %s", c.ShortSHA, c.Message))
	default:
		writeRow("commits", fmt.Sprintf("(%d)", len(commits)))
		for _, c := range commits {
			fmt.Fprintf(&sb, "           %s %s %s\n",
				c.ShortSHA, c.Date.Format("2006-01-02"), c.Message)
		}
	}

	return sb.String()
}

// buildFilesMarkdown renders touched files as a markdown block for verbose
// and full output when an AI summary is present.
func buildFilesMarkdown(files []string) string {
	if len(files) == 0 {
		return "\n## Files\n\n*(none)*\n"
	}
	var sb strings.Builder
	sb.WriteString("\n## Files\n\n")
	for _, f := range files {
		fmt.Fprintf(&sb, "- `%s`\n", escapeInlineCodeText(f))
	}
	return sb.String()
}

// buildSummaryMarkdown renders a checkpoint AI summary into the brand
// markdown shape used by entire's TTY renderer. The output is also the
// source of truth for non-TTY callers, which write it verbatim.
func buildSummaryMarkdown(s *checkpoint.Summary) string {
	if s == nil {
		return ""
	}
	var sb strings.Builder

	fmt.Fprintf(&sb, "## Intent\n\n%s\n\n", escapeSummaryText(s.Intent))
	fmt.Fprintf(&sb, "## Outcome\n\n%s\n\n", escapeSummaryText(s.Outcome))

	if hasAnyLearning(s.Learnings) {
		sb.WriteString("## Learnings\n\n")
		if len(s.Learnings.Repo) > 0 {
			sb.WriteString("### Repository\n\n")
			for _, item := range s.Learnings.Repo {
				fmt.Fprintf(&sb, "- %s\n", escapeSummaryText(item))
			}
			sb.WriteString("\n")
		}
		if len(s.Learnings.Code) > 0 {
			sb.WriteString("### Code\n\n")
			for _, item := range s.Learnings.Code {
				fmt.Fprintf(&sb, "- %s\n", formatCodeLearning(item))
			}
			sb.WriteString("\n")
		}
		if len(s.Learnings.Workflow) > 0 {
			sb.WriteString("### Workflow\n\n")
			for _, item := range s.Learnings.Workflow {
				fmt.Fprintf(&sb, "- %s\n", escapeSummaryText(item))
			}
			sb.WriteString("\n")
		}
	}

	if len(s.Friction) > 0 {
		sb.WriteString("## Friction\n\n")
		for _, item := range s.Friction {
			fmt.Fprintf(&sb, "- %s\n", escapeSummaryText(item))
		}
		sb.WriteString("\n")
	}

	if len(s.OpenItems) > 0 {
		sb.WriteString("## Open Items\n\n")
		for _, item := range s.OpenItems {
			fmt.Fprintf(&sb, "- %s\n", escapeSummaryText(item))
		}
		sb.WriteString("\n")
	}

	return strings.TrimRight(sb.String(), "\n") + "\n"
}

func hasAnyLearning(l checkpoint.LearningsSummary) bool {
	return len(l.Repo) > 0 || len(l.Code) > 0 || len(l.Workflow) > 0
}

func formatCodeLearning(c checkpoint.CodeLearning) string {
	path := escapeSummaryText(c.Path)
	finding := escapeSummaryText(c.Finding)
	switch {
	case c.Line > 0 && c.EndLine > 0:
		return fmt.Sprintf("`%s:%d-%d` — %s", path, c.Line, c.EndLine, finding)
	case c.Line > 0:
		return fmt.Sprintf("`%s:%d` — %s", path, c.Line, finding)
	default:
		return fmt.Sprintf("`%s` — %s", path, finding)
	}
}

func escapeSummaryText(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "`", "‘")
}

func escapeInlineCodeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "`", "‘")
}

// runExplainDefault shows all checkpoints on the current branch.
// This is the default view when no flags are provided.
func runExplainDefault(ctx context.Context, w io.Writer, noPager bool) error {
	return runExplainBranchDefault(ctx, w, noPager)
}

// branchCheckpointsLimit is the max checkpoints to show in branch view
const branchCheckpointsLimit = 100

// commitScanLimit is how far back to scan git history for checkpoints
const commitScanLimit = 500

// errStopIteration is used to stop commit iteration early
var errStopIteration = errors.New("stop iteration")

// getCurrentWorktreeHash returns the hashed worktree ID for the current working directory.
// This is used to filter shadow branches to only those belonging to this worktree.
func getCurrentWorktreeHash(ctx context.Context) string {
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return ""
	}
	worktreeID, err := paths.GetWorktreeID(repoRoot)
	if err != nil {
		return ""
	}
	return checkpoint.HashWorktreeID(worktreeID)
}

// computeReachableFromMain returns a set of commit hashes on the main/default branch's first-parent chain.
// On the default branch itself, returns an empty map (no filtering needed).
// Only first-parent commits are included — commits from side branches merged into main are excluded,
// since those could be feature branch commits that shouldn't be filtered out.
func computeReachableFromMain(ctx context.Context, repo *git.Repository) map[plumbing.Hash]bool {
	reachableFromMain := make(map[plumbing.Hash]bool)

	isOnDefault, _ := strategy.IsOnDefaultBranch(repo)
	if isOnDefault {
		return reachableFromMain // No filtering needed on default branch
	}

	// Resolve main branch hash
	var mainBranchHash plumbing.Hash
	if defaultBranchName := strategy.GetDefaultBranchName(repo); defaultBranchName != "" {
		ref, refErr := repo.Reference(plumbing.ReferenceName("refs/heads/"+defaultBranchName), true)
		if refErr != nil {
			ref, refErr = repo.Reference(plumbing.ReferenceName("refs/remotes/origin/"+defaultBranchName), true)
		}
		if refErr == nil {
			mainBranchHash = ref.Hash()
		}
	}
	if mainBranchHash == plumbing.ZeroHash {
		mainBranchHash = strategy.GetMainBranchHash(repo)
	}
	if mainBranchHash == plumbing.ZeroHash {
		return reachableFromMain
	}

	// Walk main's first-parent chain to build the set
	_ = walkFirstParentCommits(ctx, repo, mainBranchHash, strategy.MaxCommitTraversalDepth, func(c *object.Commit) error { //nolint:errcheck // Best-effort
		reachableFromMain[c.Hash] = true
		return nil
	})

	return reachableFromMain
}

// walkFirstParentCommits walks the first-parent chain starting from `from`,
// calling fn for each commit. It stops after visiting `limit` commits (0 = no limit).
// This avoids the full DAG traversal that repo.Log() does, which follows ALL parents
// of merge commits and can walk into unrelated branch history (e.g., main's full
// history after merging main into a feature branch).
func walkFirstParentCommits(ctx context.Context, repo *git.Repository, from plumbing.Hash, limit int, fn func(*object.Commit) error) error {
	current, err := repo.CommitObject(from)
	if err != nil {
		return fmt.Errorf("failed to get commit %s: %w", from, err)
	}

	for count := 0; limit <= 0 || count < limit; count++ {
		if err := ctx.Err(); err != nil {
			return err //nolint:wrapcheck // Propagating context cancellation
		}
		if err := fn(current); err != nil {
			if errors.Is(err, errStopIteration) {
				return nil
			}
			return err
		}

		// Follow first parent only (skip merge parents).
		// When there are no parents or parent lookup fails, we've reached the
		// end of the chain — this is a normal termination, not an error.
		if current.NumParents() == 0 {
			return nil
		}
		parentHash := current.Hash
		current, err = current.Parent(0)
		if err != nil {
			return fmt.Errorf("failed to load first parent of commit %s: %w", parentHash, err)
		}
	}
	return nil
}

// getBranchCheckpoints returns checkpoints relevant to the current branch.
// This is strategy-agnostic - it queries checkpoints directly from the checkpoint store.
//
// Behavior:
//   - On feature branches: only show checkpoints unique to this branch (not in main)
//   - On default branch (main/master): show all checkpoints in history (up to limit)
//   - Includes both committed checkpoints (trace/checkpoints/v1) and temporary checkpoints (shadow branches)
func getBranchCheckpoints(ctx context.Context, repo *git.Repository, limit int) ([]strategy.RewindPoint, error) {
	// Warn (once per process) if metadata branches are disconnected
	strategy.WarnIfMetadataDisconnected()

	v1Store := checkpoint.NewGitStore(repo)
	v2URL, err := remote.FetchURL(ctx)
	if err != nil {
		logging.Debug(
			ctx, "explain: using origin for branch checkpoint v2 store fetch remote",
			slog.String("error", err.Error()),
		)
		v2URL = ""
	}
	v2Store := checkpoint.NewV2GitStore(repo, v2URL)
	preferCheckpointsV2 := settings.IsCheckpointsV2Enabled(ctx)

	// Get all committed checkpoints for lookup (v2-aware with v1 fallback).
	committedInfos, err := listCommittedForExplain(ctx, v1Store, v2Store, preferCheckpointsV2)
	if err != nil {
		committedInfos = nil // Continue without committed checkpoints
	}

	// Build map of checkpoint ID -> committed info
	committedByID := make(map[id.CheckpointID]checkpoint.CommittedInfo)
	for _, info := range committedInfos {
		if !info.CheckpointID.IsEmpty() {
			committedByID[info.CheckpointID] = info
		}
	}

	head, err := repo.Head()
	if err != nil {
		// Unborn HEAD (no commits yet) - return empty list instead of erroring
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return []strategy.RewindPoint{}, nil
		}
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}

	// Check if we're on the default branch (needed for getReachableTemporaryCheckpoints)
	isOnDefault, _ := strategy.IsOnDefaultBranch(repo)

	// Fetch metadata trees for reading session prompts (cheap tree lookups).
	// Try v2 /main first, fall back to v1 metadata branch.
	v1MetadataTree, _ := strategy.GetMetadataBranchTree(repo)   //nolint:errcheck // Best-effort
	v2MetadataTree, _ := strategy.GetV2MetadataBranchTree(repo) //nolint:errcheck // Best-effort
	promptTree := resolvePromptTree(v1MetadataTree, v2MetadataTree, preferCheckpointsV2)

	var points []strategy.RewindPoint

	collectCheckpoint := func(c *object.Commit) {
		cpID, found := trailers.ParseCheckpoint(c.Message)
		if !found {
			return
		}
		cpInfo, found := committedByID[cpID]
		if !found {
			return
		}

		message := strings.Split(c.Message, "\n")[0]
		point := strategy.RewindPoint{
			ID:               c.Hash.String(),
			Message:          message,
			Date:             c.Committer.When,
			IsLogsOnly:       true, // Committed checkpoints are logs-only
			CheckpointID:     cpID,
			SessionID:        cpInfo.SessionID,
			IsTaskCheckpoint: cpInfo.IsTask,
			ToolUseID:        cpInfo.ToolUseID,
			Agent:            cpInfo.Agent,
		}
		// Read session prompt from metadata tree (best-effort).
		// Read prompt.txt directly from the latest session subdirectory instead of
		// parsing the full transcript — prompt.txt is tiny vs multi-MB transcripts.
		if promptTree != nil {
			point.SessionPrompt = strategy.ReadLatestSessionPromptFromCommittedTree(promptTree, cpID, cpInfo.SessionCount)
		}

		points = append(points, point)
	}

	if isOnDefault {
		// On the default branch, use full DAG walk to find checkpoint commits
		// on merged feature branches (second parents of merge commits).
		iter, iterErr := repo.Log(&git.LogOptions{
			From:  head.Hash(),
			Order: git.LogOrderCommitterTime,
		})
		if iterErr != nil {
			return nil, fmt.Errorf("failed to get commit log: %w", iterErr)
		}
		defer iter.Close()

		count := 0
		err = iter.ForEach(func(c *object.Commit) error {
			if err := ctx.Err(); err != nil {
				return err //nolint:wrapcheck // Propagating context cancellation
			}
			if count >= commitScanLimit {
				return storer.ErrStop
			}
			count++
			collectCheckpoint(c)
			return nil
		})
	} else {
		// On feature branches, use first-parent walk with branch filtering.
		// This avoids walking into main's full history through merge commit parents.
		reachableFromMain := computeReachableFromMain(ctx, repo)

		err = walkFirstParentCommits(ctx, repo, head.Hash(), commitScanLimit, func(c *object.Commit) error {
			// Once we hit a commit reachable from main on the first-parent chain,
			// all earlier ancestors are also shared-with-main, so stop scanning.
			if reachableFromMain[c.Hash] {
				return errStopIteration
			}
			collectCheckpoint(c)
			return nil
		})
	}

	if err != nil {
		return nil, fmt.Errorf("error iterating commits: %w", err)
	}

	// Get temporary checkpoints from ALL shadow branches whose base commit is reachable from HEAD.
	tempPoints := getReachableTemporaryCheckpoints(ctx, repo, v1Store, head.Hash(), isOnDefault, limit)
	points = append(points, tempPoints...)

	// Sort by date, most recent first
	sort.Slice(points, func(i, j int) bool {
		return points[i].Date.After(points[j].Date)
	})

	// Apply limit
	if len(points) > limit {
		points = points[:limit]
	}

	return points, nil
}

// getReachableTemporaryCheckpoints returns temporary checkpoints from shadow branches
// whose base commit is reachable from the given HEAD hash and that belong to this worktree.
// For default branches, all shadow branches for this worktree are included.
// For feature branches, only shadow branches whose base commit is in HEAD's history are included.
func getReachableTemporaryCheckpoints(ctx context.Context, repo *git.Repository, store *checkpoint.GitStore, headHash plumbing.Hash, isOnDefault bool, limit int) []strategy.RewindPoint {
	var points []strategy.RewindPoint

	// Compute current worktree's hash for filtering shadow branches
	currentWorktreeHash := getCurrentWorktreeHash(ctx)

	shadowBranches, _ := store.ListTemporary(ctx) //nolint:errcheck // Best-effort
	for _, sb := range shadowBranches {
		// Filter by worktree: only show shadow branches belonging to this worktree.
		// Skip filtering if currentWorktreeHash is empty (error computing it) to avoid
		// accidentally filtering out ALL shadow branches.
		_, branchWorktreeHash, parsed := checkpoint.ParseShadowBranchName(sb.BranchName)
		if currentWorktreeHash != "" && parsed && branchWorktreeHash != "" && branchWorktreeHash != currentWorktreeHash {
			continue
		}

		// Check if this shadow branch's base commit is reachable from current HEAD
		if !isShadowBranchReachable(ctx, repo, sb.BaseCommit, headHash, isOnDefault) {
			continue
		}

		// List checkpoints from this shadow branch
		tempCheckpoints, _ := store.ListCheckpointsForBranch(ctx, sb.BranchName, "", limit) //nolint:errcheck // Best-effort
		for _, tc := range tempCheckpoints {
			point := convertTemporaryCheckpoint(repo, tc)
			if point != nil {
				points = append(points, *point)
			}
		}
	}

	return points
}

// isShadowBranchReachable checks if a shadow branch's base commit is reachable from HEAD.
// For default branches, all shadow branches are considered reachable.
// For feature branches, we check if any commit with the base commit prefix is in HEAD's history.
func isShadowBranchReachable(ctx context.Context, repo *git.Repository, baseCommit string, headHash plumbing.Hash, isOnDefault bool) bool {
	// For default branch: all shadow branches are potentially relevant
	if isOnDefault {
		return true
	}

	// Check if base commit hash prefix matches any commit in HEAD's first-parent chain
	found := false
	_ = walkFirstParentCommits(ctx, repo, headHash, commitScanLimit, func(c *object.Commit) error { //nolint:errcheck // Best-effort
		if strings.HasPrefix(c.Hash.String(), baseCommit) {
			found = true
			return errStopIteration
		}
		return nil
	})

	return found
}

// convertTemporaryCheckpoint converts a TemporaryCheckpointInfo to a RewindPoint.
// Returns nil if the checkpoint should be skipped (no tree changes or can't be read).
//
// Filtering uses hasAnyChanges (O(1) tree hash comparison) rather than hasCodeChanges
// (O(files) full diff). This means metadata-only checkpoints (.trace/ changes without
// code changes) are kept — only true no-ops (identical tree as parent) are dropped.
// This trade-off is intentional for list-view performance.
func convertTemporaryCheckpoint(repo *git.Repository, tc checkpoint.TemporaryCheckpointInfo) *strategy.RewindPoint {
	shadowCommit, commitErr := repo.CommitObject(tc.CommitHash)
	if commitErr != nil {
		return nil
	}

	// Skip no-op commits where the tree is identical to the parent's.
	// Note: this keeps metadata-only changes (e.g. transcript updates in .trace/)
	// since those produce a different tree hash. See hasAnyChanges godoc.
	if !hasAnyChanges(shadowCommit) {
		return nil
	}

	// Read session prompt from the shadow branch commit's tree (not from trace/checkpoints/v1)
	// Temporary checkpoints store their metadata in the shadow branch, not in trace/checkpoints/v1
	var sessionPrompt string
	shadowTree, treeErr := shadowCommit.Tree()
	if treeErr == nil {
		sessionPrompt = strategy.ReadSessionPromptFromTree(shadowTree, tc.MetadataDir)
	}

	return &strategy.RewindPoint{
		ID:               tc.CommitHash.String(),
		Message:          tc.Message,
		MetadataDir:      tc.MetadataDir,
		Date:             tc.Timestamp,
		IsTaskCheckpoint: tc.IsTaskCheckpoint,
		ToolUseID:        tc.ToolUseID,
		SessionID:        tc.SessionID,
		SessionPrompt:    sessionPrompt,
		IsLogsOnly:       false, // Temporary checkpoints can be fully rewound
	}
}

// runExplainBranchWithFilter shows checkpoints on the current branch, optionally filtered by session.
// This is strategy-agnostic - it queries checkpoints directly.
func runExplainBranchWithFilter(ctx context.Context, w io.Writer, noPager bool, sessionFilter string) error {
	repo, err := openRepository(ctx)
	if err != nil {
		return fmt.Errorf("not a git repository: %w", err)
	}

	// Get current branch name
	branchName := strategy.GetCurrentBranchName(repo)
	if branchName == "" {
		// Detached HEAD state or unborn HEAD - try to use short commit hash if possible
		head, headErr := repo.Head()
		if headErr != nil {
			// Unborn HEAD (no commits yet) - treat as empty history instead of erroring
			if errors.Is(headErr, plumbing.ErrReferenceNotFound) {
				branchName = "HEAD (no commits yet)"
			} else {
				return fmt.Errorf("failed to get HEAD: %w", headErr)
			}
		} else {
			branchName = "HEAD (" + head.Hash().String()[:7] + ")"
		}
	}

	// Get checkpoints for this branch (strategy-agnostic)
	points, err := getBranchCheckpoints(ctx, repo, branchCheckpointsLimit)
	if err != nil {
		// If context was cancelled (e.g. user hit Ctrl+C), exit silently
		if ctx.Err() != nil {
			return NewSilentError(ctx.Err())
		}
		// Log the error but continue with empty list so user sees helpful message
		logging.Warn(ctx, "failed to get branch checkpoints", "error", err)
		points = nil
	}

	// Format output
	output := formatBranchCheckpoints(w, branchName, points, sessionFilter)

	outputExplainContent(w, output, noPager)
	return nil
}
