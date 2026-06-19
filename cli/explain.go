package cli

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cli/summarize"
	"github.com/GrayCodeAI/trace/cli/trailers"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage/filesystem"
	"github.com/spf13/cobra"
)

const defaultCheckpointSummaryTimeout = 30 * time.Second

const (
	pagerEnvVar       = "PAGER"
	lessEnvVar        = "LESS"
	lessPagerName     = "less"
	lessRawControlEnv = "LESS=-R"
	windowsGOOS       = "windows"
)

var checkpointSummaryTimeout = defaultCheckpointSummaryTimeout

var generateTranscriptSummary = summarize.GenerateFromTranscript

// errCannotGenerateTemporaryCheckpoint is returned by runExplainCheckpoint when
// --generate is requested for a target that does not match any committed
// checkpoint. runExplainAuto uses errors.Is to detect this case and fall back
// to resolving the target as a git commit ref.
var errCannotGenerateTemporaryCheckpoint = errors.New("cannot generate summary for temporary checkpoint")

type explainCheckpointLookup struct {
	repo                *git.Repository
	v1Store             *checkpoint.GitStore
	v2Store             *checkpoint.V2GitStore
	preferCheckpointsV2 bool
	committed           []checkpoint.CommittedInfo
}

// generateOrRawLabel returns the user-facing verb for the action the user
// requested, used in error messages when a commit target has no trailer.
func generateOrRawLabel(generate bool) string {
	if generate {
		return "generate summary"
	}
	return "show raw transcript"
}

// printNoTrailerMessage renders the friendly message shown when a resolved
// commit has no Trace-Checkpoint trailer in read-only modes. Takes the
// repo so the hash can be abbreviated to the minimum unique length for
// this repo's object set (matching git's --abbrev behavior).
func printNoTrailerMessage(w io.Writer, repo *git.Repository, hash plumbing.Hash) {
	styles := newStatusStyles(w)
	rows := []explainRow{
		{Label: "commit", Value: abbreviateCommitHash(repo, hash)},
		{Label: "reason", Value: "no Trace-Checkpoint trailer"},
		{Label: "hint", Value: "this commit was not created during a Trace session,"},
		{Label: "", Value: "or the trailer was removed"},
	}
	fmt.Fprint(w, styles.renderFailure("No associated Trace checkpoint", rows))
}

// errAmbiguousCommitPrefix is returned by resolveCommitUnambiguous when a
// hex prefix matches more than one commit. Callers use errors.Is to detect
// this case and surface the full wrapped message verbatim.
var errAmbiguousCommitPrefix = errors.New("ambiguous commit prefix")

// commitHashesWithPrefix enumerates all commit hashes in the repo whose
// SHA starts with the given hex prefix. Returns nil when the storer is not
// a *filesystem.Storage or the prefix isn't decodable as hex.
//
// Per PR review (discussion_r3113804961): the reviewer specifically
// suggested repo.Storer.(*filesystem.Storage).HashesWithPrefix followed by
// commit filtering. Using this primitive both in resolution (detect
// ambiguous user input) and in display (dynamically abbreviate shown
// hashes to the minimum unique length).
func commitHashesWithPrefix(repo *git.Repository, prefix string) []plumbing.Hash {
	s, ok := repo.Storer.(*filesystem.Storage)
	if !ok {
		return nil
	}
	// Truncate to even length for byte-aligned hex decoding.
	evenHex := prefix[:len(prefix)&^1]
	decoded, err := hex.DecodeString(evenHex)
	if err != nil || len(decoded) == 0 {
		return nil
	}
	candidates, err := s.HashesWithPrefix(decoded)
	if err != nil {
		return nil
	}
	var commits []plumbing.Hash
	for _, h := range candidates {
		// HashesWithPrefix matches on even byte boundaries; filter the
		// dangling nybble for odd-length prefixes.
		if len(evenHex) != len(prefix) && !strings.HasPrefix(h.String(), prefix) {
			continue
		}
		if _, err := repo.CommitObject(h); err != nil {
			continue
		}
		commits = append(commits, h)
	}
	return commits
}

// resolveCommitUnambiguous resolves a ref to a commit hash, returning
// errAmbiguousCommitPrefix (and the matching hashes) when a hex-prefix input
// matches more than one commit. go-git v6's ResolveRevision silently picks
// the first candidate in ambiguous cases (its source explicitly says "for
// speed purposes don't bother to detect the ambiguity"), which could pick
// the wrong commit. Non-hex refs (HEAD, branch names, HEAD~1) bypass the
// ambiguity check via commitHashesWithPrefix returning nil.
//
// The structured ambiguous return lets callers render a styled failure
// block (with each match's timestamp/session) without re-resolving the
// matches themselves.
func resolveCommitUnambiguous(repo *git.Repository, ref string) (plumbing.Hash, []plumbing.Hash, error) {
	hash, err := repo.ResolveRevision(plumbing.Revision(ref))
	if err != nil {
		return plumbing.ZeroHash, nil, err //nolint:wrapcheck // caller contextualizes
	}
	matches := commitHashesWithPrefix(repo, ref)
	if len(matches) <= 1 {
		return *hash, nil, nil
	}
	return plumbing.ZeroHash, matches, errAmbiguousCommitPrefix
}

// abbreviateCommitHash returns the shortest prefix of hash unique among
// commit objects in the repo, matching git's --abbrev-commit auto-growth
// so displayed short SHAs stay unambiguous as the repo grows. Falls back
// to a fixed 12-char prefix if the storer doesn't support fast prefix
// lookup, or to the full hash if somehow never unique.
func abbreviateCommitHash(repo *git.Repository, hash plumbing.Hash) string {
	full := hash.String()
	for length := 7; length < len(full); length++ {
		matches := commitHashesWithPrefix(repo, full[:length])
		if matches == nil {
			return full[:12]
		}
		if len(matches) <= 1 {
			return full[:length]
		}
	}
	return full
}

// interaction holds a single prompt and its responses for display.
type interaction struct {
	Prompt    string
	Responses []string // Multiple responses can occur between tool calls
	Files     []string
}

// associatedCommit holds information about a git commit associated with a checkpoint.
type associatedCommit struct {
	SHA      string
	ShortSHA string
	Message  string
	Author   string
	Email    string
	Date     time.Time
}

// checkpointDetail holds detailed information about a checkpoint for display.
type checkpointDetail struct {
	Index            int
	ShortID          string
	Timestamp        time.Time
	IsTaskCheckpoint bool
	Message          string
	// Interactions contains all prompt/response pairs in this checkpoint.
	// Most strategies have one, but shadow condensations may have multiple.
	Interactions []interaction
	// Files is the aggregate list of all files modified (for backwards compat)
	Files []string
}

func newExplainCmd() *cobra.Command {
	var sessionFlag string
	var commitFlag string
	var checkpointFlag string
	var noPagerFlag bool
	var shortFlag bool
	var fullFlag bool
	var rawTranscriptFlag bool
	var generateFlag bool
	var forceFlag bool
	var searchAllFlag bool
	var jsonFlag bool
	var transcriptFlag bool
	sessionIndex := -1
	listLimit := 0 // 0 means "use default (branchCheckpointsLimit)"

	cmd := &cobra.Command{
		Use:   "explain [checkpoint-id | commit-sha]",
		Short: "Explain a session, commit, or checkpoint",
		Long: `Explain provides human-readable context about sessions, commits, and checkpoints.

Use this command to understand what happened during agent-driven development,
either for self-review or to understand a teammate's work.

By default, shows checkpoints on the current branch. Pass a checkpoint ID or
commit SHA as a positional argument to explain a specific item, or use flags.

Viewing specific items:
  trace explain <id-or-sha>           Auto-detects checkpoint ID or commit SHA
  trace explain --checkpoint <id>     Force interpretation as checkpoint ID
  trace explain --commit <ref>        Force interpretation as commit ref

Filtering the list view:
  --session      Filter checkpoints by session ID (or prefix)

Output verbosity levels (when explaining a specific item):
  Default:         Detailed view with scoped prompts (ID, session, tokens, intent, prompts, files)
  --short          Summary only (ID, session, timestamp, tokens, intent)
  --full           Parsed full transcript (all prompts/responses from trace session)
  --raw-transcript Raw transcript file (JSONL format)

Machine-readable export modes (additive surface for external consumers):
  --json           Metadata-only JSON. Lists checkpoints when no target is given;
                   emits a single checkpoint envelope when a target is supplied.
                   Transcript bytes are NEVER embedded in the JSON envelope.
  --transcript     Stream the normalized compact transcript bytes (JSONL on
                   /main) to stdout for the selected session. Pair with
                   --raw-transcript for the per-agent raw transcript instead.
  --session-index  Pick a session within a multi-session checkpoint (0-based).
                   Defaults to the latest session. Only meaningful with
                   --transcript or --raw-transcript.
  --limit          Cap the number of checkpoints returned by the list view.
                   Defaults to 100. When the cap is hit, a stderr note
                   says how many were skipped. Only meaningful with --json.

Summary generation:
  --generate    Generate an AI summary for the checkpoint
  --force       Regenerate even if a summary already exists (requires --generate)

Performance options:
  --search-all  Remove branch/depth limits when searching for commits (may be slow)

Checkpoint detail view shows:
  - Author of the checkpoint
  - Associated git commits that reference the checkpoint
  - Prompts and responses from the session

Note: --session filters the list view; the positional arg, --commit, and --checkpoint are mutually exclusive.`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 1 {
				return fmt.Errorf("accepts at most 1 argument (checkpoint ID or commit SHA), received %d\nHint: use --session to filter the list view, or pass a single checkpoint ID / commit SHA", len(args))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check if Trace is disabled
			if checkDisabledGuard(cmd.Context(), cmd.OutOrStdout()) {
				return nil
			}

			// Only initialize logging when inside a git worktree to avoid
			// creating .trace/logs/ in arbitrary directories.
			if _, err := paths.WorktreeRoot(cmd.Context()); err == nil {
				logging.SetLogLevelGetter(GetLogLevel)
				if err := logging.Init(cmd.Context(), ""); err == nil {
					defer logging.Close()
				}
			}

			// Positional arg is mutually exclusive with --checkpoint, --commit, --session
			var positional string
			if len(args) > 0 {
				positional = args[0]
				if checkpointFlag != "" || commitFlag != "" || sessionFlag != "" {
					return errors.New("cannot combine positional argument with --checkpoint, --commit, or --session")
				}
			}

			// --generate and --raw-transcript need a specific target — either the
			// positional arg, --checkpoint/-c, or --commit (which forwards to
			// the checkpoint path via the commit's Trace-Checkpoint trailer).
			hasCheckpointTarget := checkpointFlag != "" || commitFlag != "" || positional != ""
			if generateFlag && !hasCheckpointTarget {
				return errors.New("--generate requires a checkpoint ID or commit SHA (positional), --checkpoint/-c, or --commit flag")
			}
			if forceFlag && !generateFlag {
				return errors.New("--force requires --generate flag")
			}
			if rawTranscriptFlag && !hasCheckpointTarget {
				return errors.New("--raw-transcript requires a checkpoint ID or commit SHA (positional), --checkpoint/-c, or --commit flag")
			}
			if transcriptFlag && !hasCheckpointTarget {
				return errors.New("--transcript requires a checkpoint ID or commit SHA (positional), --checkpoint/-c, or --commit flag")
			}
			if cmd.Flags().Changed("session-index") {
				if !transcriptFlag && !rawTranscriptFlag {
					return errors.New("--session-index only applies with --transcript or --raw-transcript")
				}
				if sessionIndex < 0 {
					return errors.New("--session-index must be non-negative")
				}
			}
			if cmd.Flags().Changed("limit") {
				if !jsonFlag {
					return errors.New("--limit only applies with --json")
				}
				if listLimit <= 0 {
					return errors.New("--limit must be positive")
				}
			}

			// Export modes — emit machine-readable output and skip the prose pipeline.
			// --raw-transcript also routes here when --session-index is explicit; the
			// legacy raw-transcript path (with spinner + prefetch) handles the default
			// case where the caller wants the latest session.
			rawWithSessionIndex := rawTranscriptFlag && cmd.Flags().Changed("session-index")
			if jsonFlag || transcriptFlag || rawWithSessionIndex {
				return runExplainExport(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), explainExportOptions{
					sessionFilter:  sessionFlag,
					commitRef:      commitFlag,
					checkpointFlag: checkpointFlag,
					target:         positional,
					json:           jsonFlag,
					transcript:     transcriptFlag,
					rawTranscript:  rawTranscriptFlag,
					sessionIndex:   sessionIndex,
					listLimit:      listLimit,
				})
			}

			// Convert short flag to verbose (verbose = !short)
			verbose := !shortFlag
			return runExplain(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), sessionFlag, commitFlag, checkpointFlag, positional, noPagerFlag, verbose, fullFlag, rawTranscriptFlag, generateFlag, forceFlag, searchAllFlag)
		},
	}

	cmd.Flags().StringVar(&sessionFlag, "session", "", "Filter checkpoints by session ID (or prefix)")
	cmd.Flags().StringVar(&commitFlag, "commit", "", "Explain a specific commit (SHA or ref, \"commit-ish\")")
	cmd.Flags().StringVarP(&checkpointFlag, "checkpoint", "c", "", "Explain a specific checkpoint (ID or prefix)")
	cmd.Flags().BoolVar(&noPagerFlag, "no-pager", false, "Disable pager output")
	cmd.Flags().BoolVarP(&shortFlag, "short", "s", false, "Show summary only (omit prompts and files)")
	cmd.Flags().BoolVar(&fullFlag, "full", false, "Show full parsed transcript (all prompts/responses)")
	cmd.Flags().BoolVar(&rawTranscriptFlag, "raw-transcript", false, "Show raw transcript file (JSONL format)")
	cmd.Flags().BoolVar(&generateFlag, "generate", false, "Generate an AI summary for the checkpoint")
	cmd.Flags().BoolVar(&forceFlag, "force", false, "Regenerate summary even if one already exists (requires --generate)")
	cmd.Flags().BoolVar(&searchAllFlag, "search-all", false, "Search all commits (no branch/depth limit, may be slow)")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "Output metadata as JSON (no transcript bytes)")
	cmd.Flags().BoolVar(&transcriptFlag, "transcript", false, "Stream compact normalized transcript bytes to stdout (pair with --raw-transcript for the per-agent raw transcript)")
	cmd.Flags().IntVar(&sessionIndex, "session-index", -1, "Session index within a multi-session checkpoint (0-based, defaults to latest)")
	cmd.Flags().IntVar(&listLimit, "limit", 0, "Cap the list view at N checkpoints (default: 100). Only meaningful with --json.")

	// Verbosity / transcript output modes are mutually exclusive
	cmd.MarkFlagsMutuallyExclusive("short", "full", "raw-transcript", "transcript", "json")
	// --generate and --raw-transcript are incompatible (summary would be generated but not shown)
	cmd.MarkFlagsMutuallyExclusive("generate", "raw-transcript")
	// --generate is a write op; export modes are reader-only
	cmd.MarkFlagsMutuallyExclusive("generate", "json")
	cmd.MarkFlagsMutuallyExclusive("generate", "transcript")

	return cmd
}

// runExplain routes to the appropriate explain function based on flags and the
// optional positional target.
func runExplain(ctx context.Context, w, errW io.Writer, sessionID, commitRef, checkpointID, target string, noPager, verbose, full, rawTranscript, generate, force, searchAll bool) error {
	// Count mutually exclusive flags (--commit and --checkpoint are mutually exclusive)
	// --session is now a filter for the list view, not a separate mode
	flagCount := 0
	if commitRef != "" {
		flagCount++
	}
	if checkpointID != "" {
		flagCount++
	}
	// If --session is combined with --commit or --checkpoint, that's still an error
	if sessionID != "" && flagCount > 0 {
		return errors.New("cannot specify multiple of --session, --commit, --checkpoint")
	}
	if flagCount > 1 {
		return errors.New("cannot specify multiple of --session, --commit, --checkpoint")
	}

	// Route to appropriate handler
	if target != "" {
		return runExplainAuto(ctx, w, errW, target, noPager, verbose, full, rawTranscript, generate, force, searchAll)
	}
	if commitRef != "" {
		return runExplainCommit(ctx, w, errW, commitRef, noPager, verbose, full, rawTranscript, generate, force, searchAll)
	}
	if checkpointID != "" {
		return runExplainCheckpoint(ctx, w, errW, checkpointID, noPager, verbose, full, rawTranscript, generate, force, searchAll)
	}

	// Default or with session filter: show list view (optionally filtered by session)
	return runExplainBranchWithFilter(ctx, w, noPager, sessionID)
}

// runExplainAuto resolves a positional target as either a checkpoint ID
// (or prefix) or a git commit ref. Ordering: checkpoint path first (which
// also handles shadow-branch temp checkpoints), falling back to commit
// resolution only on checkpoint.ErrCheckpointNotFound. --generate runs
// an ambiguity pre-check to avoid writing a summary to the wrong
// checkpoint on short-prefix collisions.
func runExplainAuto(ctx context.Context, w, errW io.Writer, target string, noPager, verbose, full, rawTranscript, generate, force, searchAll bool) error {
	stop := startSpinner(errW, "Loading checkpoints")
	lookup, lookupErr := newExplainCheckpointLookup(ctx)
	stop("")
	if generate {
		if err := runExplainAutoAmbiguityGuard(ctx, target, lookup, lookupErr); err != nil {
			return err
		}
	}
	checkpointErr := runExplainCheckpointWithLookup(ctx, w, errW, target, noPager, verbose, full, rawTranscript, generate, force, searchAll, lookup, lookupErr)
	if checkpointErr == nil {
		return nil
	}
	// Fall back to commit resolution ONLY when nothing (committed or temp)
	// matched the target. errCannotGenerateTemporaryCheckpoint signals that
	// we DID match a temp checkpoint but --generate is unsupported for it;
	// falling back to commit in that case would produce a misleading
	// "no trailer" error for the shadow-branch commit.
	if !errors.Is(checkpointErr, checkpoint.ErrCheckpointNotFound) {
		return checkpointErr
	}
	logging.Debug(ctx, "explain auto: checkpoint lookup failed, trying commit fallback",
		slog.String("target", target),
		slog.String("checkpoint_error", checkpointErr.Error()))

	if lookupErr != nil {
		// Composed message beats errors.Join here — the latter renders
		// two lines (one per error) and users act on the first/stale one.
		return fmt.Errorf("no checkpoint matched %q, and commit fallback failed: %w", target, lookupErr)
	}
	hash, ambiguousMatches, resolveErr := resolveCommitUnambiguous(lookup.repo, target)
	if resolveErr != nil {
		if errors.Is(resolveErr, errAmbiguousCommitPrefix) {
			renderAmbiguousPrefixFailure(errW, target, "commits", buildAmbiguousCommitMatches(lookup.repo, ambiguousMatches))
			return NewSilentError(resolveErr)
		}
		logging.Debug(ctx, "explain auto: git ref resolution failed",
			slog.String("target", target),
			slog.String("error", resolveErr.Error()))
		return fmt.Errorf("no checkpoint or commit found matching %q", target)
	}
	commit, commitErr := lookup.repo.CommitObject(hash)
	if commitErr != nil {
		return fmt.Errorf("failed to get commit %s: %w", abbreviateCommitHash(lookup.repo, hash), commitErr)
	}
	cpID, hasCheckpoint := trailers.ParseCheckpoint(commit.Message)
	if !hasCheckpoint {
		// Side-effect modes must error — silently succeeding would leave
		// scripts unable to distinguish "done" from "didn't happen".
		if generate || rawTranscript {
			return fmt.Errorf("cannot %s: commit %s has no Trace-Checkpoint trailer", generateOrRawLabel(generate), abbreviateCommitHash(lookup.repo, hash))
		}
		printNoTrailerMessage(w, lookup.repo, hash)
		return nil
	}
	logging.Debug(ctx, "explain auto: resolved commit to checkpoint via trailer",
		slog.String("target", target),
		slog.String("commit", abbreviateCommitHash(lookup.repo, hash)),
		slog.String("checkpoint_id", cpID.String()))
	return runExplainCheckpointWithLookup(ctx, w, errW, cpID.String(), noPager, verbose, full, rawTranscript, generate, force, searchAll, lookup, nil)
}

// runExplainAutoAmbiguityGuard refuses --generate when the positional
// target resolves as both a git revision and a committed-checkpoint prefix.
// Writing a summary to the wrong checkpoint is destructive; read-only flows
// tolerate the same ambiguity by preferring the checkpoint path.
//
// Best-effort: on repo/list failures we return nil so the main flow
// surfaces the real error instead of double-reporting.
func runExplainAutoAmbiguityGuard(ctx context.Context, target string, lookup *explainCheckpointLookup, lookupErr error) error {
	// Targets longer than a checkpoint ID can't prefix-match one.
	// This is coupled to checkpoint IDs being fixed-width; longer targets
	// cannot be prefixes of committed checkpoint IDs.
	if len(target) > id.ShortIDLength {
		return nil
	}
	if lookupErr != nil {
		logging.Warn(ctx, "explain ambiguity guard degraded: failed to prepare checkpoint lookup",
			"target", target,
			"error", lookupErr)
		return nil
	}
	hash, err := lookup.repo.ResolveRevision(plumbing.Revision(target))
	if err != nil {
		return nil //nolint:nilerr // target isn't a git ref, so no checkpoint/ref ambiguity is possible; fall through to normal resolution which reports the real error
	}
	if lookup == nil {
		logging.Warn(ctx, "explain ambiguity guard degraded: checkpoint lookup unavailable",
			"target", target)
		return nil
	}
	if lookup.committed == nil {
		logging.Warn(ctx, "explain ambiguity guard degraded: committed checkpoint list unavailable",
			"target", target)
		return nil
	}
	for _, info := range lookup.committed {
		if strings.HasPrefix(info.CheckpointID.String(), target) {
			return fmt.Errorf("ambiguous target %q with --generate: matches both git revision %s and checkpoint prefix (e.g. %s)\nUse --commit <ref> or --checkpoint <id> to disambiguate", target, abbreviateCommitHash(lookup.repo, *hash), info.CheckpointID)
		}
	}
	return nil
}

// runExplainCheckpoint explains a specific checkpoint.
// Supports both committed checkpoints (by checkpoint ID) and temporary checkpoints (by git SHA).
// First tries to match committed checkpoints, then falls back to temporary checkpoints.
// When generate is true, generates an AI summary for the checkpoint.
// When force is true, regenerates even if a summary already exists.
// When rawTranscript is true, outputs only the raw transcript file (JSONL format).
// When searchAll is true, searches all commits without branch/depth limits (used for finding associated commits).
//

func runExplainCheckpoint(ctx context.Context, w, errW io.Writer, checkpointIDPrefix string, noPager, verbose, full, rawTranscript, generate, force, searchAll bool) error {
	return runExplainCheckpointWithLookup(ctx, w, errW, checkpointIDPrefix, noPager, verbose, full, rawTranscript, generate, force, searchAll, nil, nil)
}

func runExplainCheckpointWithLookup(ctx context.Context, w, errW io.Writer, checkpointIDPrefix string, noPager, verbose, full, rawTranscript, generate, force, searchAll bool, lookup *explainCheckpointLookup, lookupErr error) error {
	if lookup == nil {
		var err error
		lookup, err = newExplainCheckpointLookup(ctx)
		if err != nil {
			return err
		}
	} else if lookupErr != nil {
		return lookupErr
	}

	// Match the prefix locally; on miss, fetch from remote and retry once.
	matches, lookup := matchCheckpointPrefixWithRemoteFallback(ctx, errW, lookup, checkpointIDPrefix)

	var fullCheckpointID id.CheckpointID
	switch len(matches) {
	case 0:
		// Check temp checkpoints BEFORE returning errCannotGenerateTemporaryCheckpoint
		// so runExplainAuto can distinguish:
		//   - target matched a real temp checkpoint (sentinel returned, no fallback)
		//   - target matched nothing (ErrCheckpointNotFound, safe to fall back to commit)
		// Previously the --generate path bailed before checking temp checkpoints,
		// which made runExplainAuto fall back to commit resolution for temp
		// checkpoint SHAs and produce a misleading "no trailer" error.
		//
		// --generate and --raw-transcript are mutually exclusive at the flag
		// layer, so rawTranscript is always false when generate is true; the
		// direct-to-w write path inside explainTemporaryCheckpoint is not
		// reachable here and won't leak partial output on error.
		output, found, tempErr := explainTemporaryCheckpoint(ctx, w, errW, lookup.repo, lookup.v1Store, checkpointIDPrefix, verbose, full, rawTranscript)
		if tempErr != nil {
			return tempErr
		}
		if found {
			if generate {
				return fmt.Errorf("%w %s (only committed checkpoints supported)", errCannotGenerateTemporaryCheckpoint, checkpointIDPrefix)
			}
			outputExplainContent(w, output, noPager)
			return nil
		}
		return fmt.Errorf("%w: %s", checkpoint.ErrCheckpointNotFound, checkpointIDPrefix)
	case 1:
		fullCheckpointID = matches[0]
	default:
		// Ambiguous prefix: render styled failure block, return SilentError so
		// main.go does not double-print. Matches the temporary-side and
		// commit-side ambiguity paths.
		ambig := buildAmbiguousCheckpointMatches(matches, lookup.committed)
		renderAmbiguousPrefixFailure(errW, checkpointIDPrefix, "committed checkpoints", ambig)
		return NewSilentError(fmt.Errorf("%w: %s matches %d checkpoints", errAmbiguousCommitPrefix, checkpointIDPrefix, len(matches)))
	}

	// One spinner covers the entire data-loading pipeline: prefetch's
	// missing-blob analysis (which spawns one cat-file -e per blob and
	// can take seconds on a deep checkpoint subtree), the prefetch fetch
	// itself, ResolveCommittedReader's metadata read, session content
	// reads, and getAssociatedCommits' git log walk. Stop strictly before
	// any write to w (stdout) so stderr spinner frames and stdout output
	// never interleave.
	stopLoad := startSpinner(errW, fmt.Sprintf("Loading checkpoint %s", fullCheckpointID))

	resolvedReader, summary, content, err := loadCheckpointForExplain(ctx, errW, lookup, fullCheckpointID, full, generate, rawTranscript)
	if err != nil {
		stopLoad("")
		return err
	}
	v2Reader, isCheckpointsV2 := resolvedReader.(*checkpoint.V2GitStore)

	// Handle summary generation — uses raw transcript.
	if generate {
		stopLoad("") // generation prints its own progress to w/errW
		if err := generateCheckpointSummary(ctx, w, errW, lookup.v1Store, lookup.v2Store, fullCheckpointID, summary, content, force); err != nil {
			return err
		}
		// Reload to get the updated summary. After generation we only need
		// /main data for display, so use the /main-only path for v2.
		stopLoad = startSpinner(errW, fmt.Sprintf("Reloading checkpoint %s", fullCheckpointID))
		if isCheckpointsV2 {
			content, err = readV2ContentFromMain(ctx, v2Reader, fullCheckpointID, summary)
		} else {
			content, err = readLatestSessionContentForExplain(ctx, resolvedReader, fullCheckpointID, summary)
		}
		if err != nil {
			stopLoad("")
			return fmt.Errorf("failed to reload checkpoint: %w", err)
		}
	}

	// Handle raw transcript output
	if rawTranscript {
		stopLoad("")
		rawLog, _, rawErr := checkpoint.ResolveRawSessionLogForCheckpoint(ctx, fullCheckpointID, lookup.v1Store, lookup.v2Store, lookup.preferCheckpointsV2)
		if rawErr != nil {
			return fmt.Errorf("failed to read raw transcript: %w", rawErr)
		}
		if len(rawLog) == 0 {
			return fmt.Errorf("checkpoint %s has no transcript", fullCheckpointID)
		}
		// Output raw transcript directly (no pager, no formatting)
		if _, err = w.Write(rawLog); err != nil {
			return fmt.Errorf("failed to write transcript: %w", err)
		}
		return nil
	}

	// Find associated commits (git commits with matching Trace-Checkpoint trailer)
	associatedCommits, _ := getAssociatedCommits(ctx, lookup.repo, fullCheckpointID, searchAll) //nolint:errcheck // Best-effort

	// Derive author from the first associated commit (the user who made the commit).
	// Fall back to GetCheckpointAuthor (walks trace/checkpoints/v1) for checkpoints
	// not reachable from the current branch.
	var author checkpoint.Author
	if len(associatedCommits) > 0 {
		author = checkpoint.Author{
			Name:  associatedCommits[0].Author,
			Email: associatedCommits[0].Email,
		}
	} else {
		author, _ = lookup.v1Store.GetCheckpointAuthor(ctx, fullCheckpointID) //nolint:errcheck // Author is optional
	}

	// Format and output. Stop spinner BEFORE any write to w to keep stderr
	// frames and stdout content from interleaving.
	stopLoad("")
	output := formatCheckpointOutput(summary, content, fullCheckpointID, associatedCommits, author, verbose, full, w)
	outputExplainContent(w, output, noPager)
	return nil
}

// loadCheckpointForExplain runs prefetchCheckpointBlobs + summary read +
// session content read for the given checkpoint. Extracts the bulk of the
// data-load pipeline out of runExplainCheckpointWithLookup so that
// function stays under maintidx limits. Caller is responsible for the
// surrounding spinner.
func loadCheckpointForExplain(ctx context.Context, errW io.Writer, lookup *explainCheckpointLookup, cpID id.CheckpointID, full, generate, rawTranscript bool) (checkpoint.CommittedReader, *checkpoint.CheckpointSummary, *checkpoint.SessionContent, error) {
	prefetchCheckpointBlobs(ctx, errW, lookup.repo, cpID, lookup.preferCheckpointsV2)

	reader, summary, err := checkpoint.ResolveCommittedReaderForCheckpoint(ctx, cpID, lookup.v1Store, lookup.v2Store, lookup.preferCheckpointsV2)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to read checkpoint: %w", err)
	}

	// Default display modes for v2 checkpoints read only from /main —
	// metadata, prompts, and the compact transcript. The raw transcript
	// on /full/* refs is never needed for human-readable output and may
	// be unavailable (rotated, not fetched).
	needsRawTranscript := full || generate || rawTranscript
	if v2Reader, ok := reader.(*checkpoint.V2GitStore); ok && !needsRawTranscript {
		content, contentErr := readV2ContentFromMain(ctx, v2Reader, cpID, summary)
		if contentErr != nil {
			return nil, nil, nil, fmt.Errorf("failed to read checkpoint content: %w", contentErr)
		}
		return reader, summary, content, nil
	}
	content, contentErr := readLatestSessionContentForExplain(ctx, reader, cpID, summary)
	if contentErr != nil {
		return nil, nil, nil, fmt.Errorf("failed to read checkpoint content: %w", contentErr)
	}
	return reader, summary, content, nil
}

// prefetchCheckpointBlobs navigates to the checkpoint's subtree(s) — v1
// always, v2 when enabled — collects every locally-missing blob, and
// fetches them all in a single `git fetch-pack` invocation per store.
// Best-effort — failure is logged and the read path falls back to the
// FetchingTree's per-File fetcher.
//
// Caller is expected to wrap this with a spinner; both the missing-blob
// analysis (one cat-file -e per blob) and the actual fetch are silent
// inside this function so the caller's spinner provides continuous
// feedback.
func prefetchCheckpointBlobs(ctx context.Context, _ io.Writer, repo *git.Repository, cpID id.CheckpointID, preferV2 bool) {
	v1FT := buildCheckpointFetchingTree(ctx, repo, cpID, "v1", loadV1MetadataRootTree)
	var v2FT *checkpoint.FetchingTree
	if preferV2 {
		v2FT = buildCheckpointFetchingTree(ctx, repo, cpID, "v2", loadV2MainRootTree)
	}

	missingCount := 0
	if v1FT != nil {
		missingCount += len(v1FT.CollectMissingBlobs())
	}
	if v2FT != nil {
		missingCount += len(v2FT.CollectMissingBlobs())
	}
	if missingCount == 0 {
		return
	}
	logging.Debug(
		ctx, "explain prefetch: fetching missing checkpoint blobs",
		slog.String("checkpoint_id", cpID.String()),
		slog.Int("blob_count", missingCount),
	)

	runPreFetch(ctx, v1FT, cpID, "v1")
	runPreFetch(ctx, v2FT, cpID, "v2")
}

// buildCheckpointFetchingTree navigates to the checkpoint subtree using
// loadRoot and wraps it in a FetchingTree with FetchBlobsByHash. Returns
// nil when the root tree or cp subtree isn't navigable.
func buildCheckpointFetchingTree(ctx context.Context, repo *git.Repository, cpID id.CheckpointID, label string, loadRoot func(*git.Repository) (*object.Tree, error)) *checkpoint.FetchingTree {
	rootTree, err := loadRoot(repo)
	if err != nil {
		return nil
	}
	cpSubtree, err := rootTree.Tree(cpID.Path())
	if err != nil {
		logging.Debug(
			ctx, "explain prefetch: cp subtree not found",
			slog.String("store", label),
			slog.String("checkpoint_id", cpID.String()),
			slog.String("error", err.Error()),
		)
		return nil
	}
	return checkpoint.NewFetchingTree(ctx, cpSubtree, repo.Storer, FetchBlobsByHash)
}

func runPreFetch(ctx context.Context, ft *checkpoint.FetchingTree, cpID id.CheckpointID, label string) {
	if ft == nil {
		return
	}
	prefetched, err := ft.PreFetch()
	if err != nil {
		logging.Debug(
			ctx, "explain prefetch: PreFetch failed",
			slog.String("store", label),
			slog.String("checkpoint_id", cpID.String()),
			slog.String("error", err.Error()),
		)
		return
	}
	if prefetched > 0 {
		logging.Debug(
			ctx, "explain prefetch: blobs fetched in one round-trip",
			slog.String("store", label),
			slog.String("checkpoint_id", cpID.String()),
			slog.Int("blob_count", prefetched),
		)
	}
}

func loadV1MetadataRootTree(repo *git.Repository) (*object.Tree, error) {
	if tree, err := strategy.GetMetadataBranchTree(repo); err == nil {
		return tree, nil
	}
	tree, err := strategy.GetRemoteMetadataBranchTree(repo)
	if err != nil {
		return nil, fmt.Errorf("read v1 metadata tree (local + remote-tracking): %w", err)
	}
	return tree, nil
}

func loadV2MainRootTree(repo *git.Repository) (*object.Tree, error) {
	ref, err := repo.Reference(plumbing.ReferenceName(paths.V2MainRefName), true)
	if err != nil {
		return nil, fmt.Errorf("v2 /main ref not found: %w", err)
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("read v2 /main commit: %w", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("read v2 /main tree: %w", err)
	}
	return tree, nil
}
