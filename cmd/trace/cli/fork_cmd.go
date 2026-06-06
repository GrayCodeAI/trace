package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/checkpoint/remote"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/session"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/trailers"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/versioninfo"
	"github.com/google/uuid"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/spf13/cobra"
)

// forkBranchPrefix is the namespace for branches created by `trace fork`.
// Fork branches point at the code commit of the source checkpoint, giving the
// new session an independent ref to build on for A/B comparison.
const forkBranchPrefix = "trace/fork/"

// forkResult captures the outcome of a fork so the command layer can render it
// and tests can assert on it without parsing stdout.
type forkResult struct {
	// SourceCheckpointID is the fully-resolved checkpoint that was forked.
	SourceCheckpointID id.CheckpointID

	// SourceSessionID is the session the source checkpoint belonged to.
	SourceSessionID string

	// NewSessionID is the freshly-allocated session for the fork.
	NewSessionID string

	// ForkBranch is the branch ref created for the fork (empty when no code
	// commit could be resolved and only session state was cloned).
	ForkBranch string

	// BaseCommit is the commit the fork branch points at (empty when unresolved).
	BaseCommit string
}

// newForkCmd builds `trace fork <checkpoint-id>`: it clones a committed
// checkpoint into a new, independent session for A/B testing. The clone copies
// the source session's transcript reference, token-usage baseline, and
// metadata, allocates a fresh session ID, and — when the source checkpoint maps
// to a code commit — branches that commit so the fork has its own ref to work
// on without disturbing the original.
func newForkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fork <checkpoint-id>",
		Short: "Clone a checkpoint into a new independent session for A/B testing",
		Long: `Clone a checkpoint into a new, independent session.

Fork reads a committed checkpoint, allocates a fresh session ID, and derives a
new session state from the source: the transcript reference, token-usage
baseline, and user metadata are copied so the fork starts from the same context
as the original. When the checkpoint maps to a code commit, fork also creates a
branch (trace/fork/<session>) pointing at that commit, giving the new session an
independent ref to experiment on. The original session and its checkpoints are
left untouched.

Examples:
  trace fork a1b2c3d4e5f6
  trace fork a1b2c3      # 6+ hex-char prefix is accepted`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if _, err := paths.WorktreeRoot(ctx); err != nil {
				cmd.SilenceUsage = true
				return errors.New("not a git repository")
			}
			return runFork(ctx, cmd.OutOrStdout(), args[0])
		},
	}
	return cmd
}

// runFork resolves the checkpoint, clones it into a new session, and prints the
// result. The checkpoint argument may be a full 12-hex-char ID or a prefix.
func runFork(ctx context.Context, w io.Writer, checkpointArg string) error {
	repo, err := openRepository(ctx)
	if err != nil {
		return fmt.Errorf("not a git repository: %w", err)
	}

	v1Store, v2Store, preferV2 := newForkStores(ctx, repo)

	cpID, err := resolveForkCheckpointID(ctx, checkpointArg, v1Store, v2Store, preferV2)
	if err != nil {
		return err
	}

	reader, _, err := checkpoint.ResolveCommittedReaderForCheckpoint(ctx, cpID, v1Store, v2Store, preferV2)
	if err != nil {
		if errors.Is(err, checkpoint.ErrCheckpointNotFound) {
			return fmt.Errorf("checkpoint %s not found", cpID)
		}
		return fmt.Errorf("failed to read checkpoint %s: %w", cpID, err)
	}

	// Read the first session's content for the metadata we derive the fork from
	// (transcript reference, token usage, agent/model, branch).
	content, err := reader.ReadSessionContent(ctx, cpID, 0)
	if err != nil {
		return fmt.Errorf("failed to read checkpoint %s content: %w", cpID, err)
	}

	result, err := forkSession(ctx, repo, cpID, &content.Metadata)
	if err != nil {
		return err
	}

	printForkResult(w, result)
	return nil
}

// newForkStores builds the v1/v2 checkpoint stores the same way the explain
// path does, so fork resolves checkpoints identically (with on-demand blob
// fetching for treeless clones).
func newForkStores(ctx context.Context, repo *git.Repository) (*checkpoint.GitStore, *checkpoint.V2GitStore, bool) {
	v2URL, err := remote.FetchURL(ctx)
	if err != nil {
		v2URL = ""
	}

	v1Store := checkpoint.NewGitStore(repo)
	v1Store.SetBlobFetcher(FetchBlobsByHash)

	v2Store := checkpoint.NewV2GitStore(repo, v2URL)
	v2Store.SetBlobFetcher(FetchBlobsByHash)

	return v1Store, v2Store, settings.IsCheckpointsV2Enabled(ctx)
}

// resolveForkCheckpointID accepts a full checkpoint ID or a hex prefix and
// resolves it to a single committed checkpoint. Returns a clear error when the
// prefix is ambiguous or matches nothing.
func resolveForkCheckpointID(
	ctx context.Context,
	arg string,
	v1Store *checkpoint.GitStore,
	v2Store *checkpoint.V2GitStore,
	preferV2 bool,
) (id.CheckpointID, error) {
	arg = strings.TrimSpace(strings.ToLower(arg))

	// Exact, fully-formed ID: use it directly.
	if cpID, err := id.NewCheckpointID(arg); err == nil {
		return cpID, nil
	}

	committed, err := listCommittedForExplain(ctx, v1Store, v2Store, preferV2)
	if err != nil {
		return id.EmptyCheckpointID, fmt.Errorf("failed to list checkpoints: %w", err)
	}

	var matches []id.CheckpointID
	seen := make(map[string]bool)
	for _, info := range committed {
		s := info.CheckpointID.String()
		if seen[s] {
			continue
		}
		if strings.HasPrefix(s, arg) {
			seen[s] = true
			matches = append(matches, info.CheckpointID)
		}
	}

	switch len(matches) {
	case 0:
		return id.EmptyCheckpointID, fmt.Errorf("no checkpoint matches %q", arg)
	case 1:
		return matches[0], nil
	default:
		return id.EmptyCheckpointID, fmt.Errorf("ambiguous checkpoint prefix %q matches %d checkpoints (e.g. %s, %s)",
			arg, len(matches), matches[0], matches[1])
	}
}

// forkSession allocates a new session ID, optionally branches the source code
// commit, and writes a derived session state. The source checkpoint's session
// is read-only here — we only derive from its metadata.
func forkSession(
	ctx context.Context,
	repo *git.Repository,
	cpID id.CheckpointID,
	meta *checkpoint.CommittedMetadata,
) (forkResult, error) {
	newSessionID := generateForkSessionID()

	result := forkResult{
		SourceCheckpointID: cpID,
		SourceSessionID:    meta.SessionID,
		NewSessionID:       newSessionID,
	}

	// Resolve the code commit the checkpoint maps to and branch it. This is the
	// conservative "clone workspace state" path: a single git ref operation that
	// gives the fork an independent starting point without copying a worktree.
	commitHash, branchName := forkCodeCommit(ctx, repo, cpID, meta, newSessionID)
	if !commitHash.IsZero() {
		ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branchName), commitHash)
		if err := repo.Storer.SetReference(ref); err != nil {
			return forkResult{}, fmt.Errorf("failed to create fork branch %s: %w", branchName, err)
		}
		result.ForkBranch = branchName
		result.BaseCommit = commitHash.String()
	}

	if err := writeForkSessionState(ctx, newSessionID, result.BaseCommit, meta); err != nil {
		return forkResult{}, err
	}

	return result, nil
}

// forkCodeCommit finds the code commit carrying the checkpoint's
// Trace-Checkpoint trailer and returns it alongside the fork branch name. It
// searches the branch recorded in the checkpoint metadata first, then falls
// back to HEAD. Returns a zero hash when no commit can be resolved (e.g. the
// checkpoint was never committed onto a reachable branch), in which case the
// caller clones session state only.
func forkCodeCommit(
	ctx context.Context,
	repo *git.Repository,
	cpID id.CheckpointID,
	meta *checkpoint.CommittedMetadata,
	newSessionID string,
) (plumbing.Hash, string) {
	branchName := forkBranchPrefix + shortForkID(newSessionID)

	starts := forkSearchStarts(repo, meta)
	for _, start := range starts {
		if hash := findCommitByCheckpointTrailer(ctx, repo, start, cpID); !hash.IsZero() {
			return hash, branchName
		}
	}

	logging.Debug(ctx, "fork: no code commit found for checkpoint, cloning session state only",
		"checkpoint_id", cpID.String())
	return plumbing.ZeroHash, branchName
}

// forkSearchStarts returns the commit hashes to start trailer searches from:
// the branch recorded in the checkpoint metadata (if it resolves) followed by
// HEAD. Order matters — the recorded branch is the most likely home of the
// checkpoint commit.
func forkSearchStarts(repo *git.Repository, meta *checkpoint.CommittedMetadata) []plumbing.Hash {
	var starts []plumbing.Hash
	seen := make(map[plumbing.Hash]bool)

	add := func(h plumbing.Hash) {
		if !h.IsZero() && !seen[h] {
			seen[h] = true
			starts = append(starts, h)
		}
	}

	if meta.Branch != "" {
		if ref, err := repo.Reference(plumbing.NewBranchReferenceName(meta.Branch), true); err == nil {
			add(ref.Hash())
		}
	}
	if head, err := repo.Head(); err == nil {
		add(head.Hash())
	}
	return starts
}

// findCommitByCheckpointTrailer walks history from start looking for the commit
// whose message carries Trace-Checkpoint: <cpID>. Returns the zero hash when not
// found. The walk is bounded by go-git's log iterator; ctx cancellation stops
// it early.
func findCommitByCheckpointTrailer(
	ctx context.Context,
	repo *git.Repository,
	start plumbing.Hash,
	cpID id.CheckpointID,
) plumbing.Hash {
	iter, err := repo.Log(&git.LogOptions{From: start, Order: git.LogOrderCommitterTime})
	if err != nil {
		return plumbing.ZeroHash
	}
	defer iter.Close()

	var found plumbing.Hash
	_ = iter.ForEach(func(c *object.Commit) error { //nolint:errcheck // sentinel-based early stop
		if ctx.Err() != nil {
			return ctx.Err() //nolint:wrapcheck // propagating cancellation to stop the walk
		}
		for _, parsed := range trailers.ParseAllCheckpoints(c.Message) {
			if parsed == cpID {
				found = c.Hash
				return errStopForkWalk
			}
		}
		return nil
	})
	return found
}

// errStopForkWalk halts the commit walk once the target checkpoint commit is
// found.
var errStopForkWalk = errors.New("stop fork walk")

// writeForkSessionState persists a new session state derived from the source
// checkpoint metadata. The fork starts ENDED (no live agent attached yet) and
// records its provenance under metadata so it can be traced back to the source.
func writeForkSessionState(
	ctx context.Context,
	newSessionID string,
	baseCommit string,
	meta *checkpoint.CommittedMetadata,
) error {
	stateStore, err := session.NewStateStore(ctx)
	if err != nil {
		return fmt.Errorf("failed to open session store: %w", err)
	}

	now := time.Now()
	state := &session.State{
		SessionID:           newSessionID,
		CLIVersion:          versioninfo.Version,
		BaseCommit:          baseCommit,
		StartedAt:           now,
		LastInteractionTime: &now,
		Phase:               session.PhaseEnded,
		AgentType:           meta.Agent,
		ModelName:           meta.Model,
		// Token-usage baseline: the fork inherits the source's accumulated usage
		// so analytics on the fork measure incremental cost from the fork point.
		TokenUsage: cloneTokenUsage(meta.TokenUsage),
		// Transcript reference is cloned so resume can locate the source context.
		Metadata: forkMetadata(meta, newSessionID),
	}
	if baseCommit != "" {
		state.AttributionBaseCommit = baseCommit
	}

	if err := stateStore.Save(ctx, state); err != nil {
		return fmt.Errorf("failed to save fork session state: %w", err)
	}
	return nil
}

// forkMetadata builds the new session's metadata map: the source's user tags
// are copied verbatim, then fork-provenance keys are layered on top so the
// fork can always be traced back to its origin.
func forkMetadata(meta *checkpoint.CommittedMetadata, newSessionID string) map[string]string {
	m := make(map[string]string, 4)
	// Carry forward user-defined tags would require the source session state;
	// the committed metadata does not store them, so we record provenance only.
	m["forked_from_checkpoint"] = meta.CheckpointID.String()
	if meta.SessionID != "" {
		m["forked_from_session"] = meta.SessionID
	}
	m["fork_session"] = newSessionID
	return m
}

// cloneTokenUsage returns a deep copy of the source token usage so the fork's
// baseline cannot be mutated through the shared pointer. Returns nil when the
// source has no usage data.
func cloneTokenUsage(src *agent.TokenUsage) *agent.TokenUsage {
	if src == nil {
		return nil
	}
	cp := *src
	if src.SubagentTokens != nil {
		sub := *src.SubagentTokens
		cp.SubagentTokens = &sub
	}
	return &cp
}

// generateForkSessionID allocates a fresh, path-safe session ID for the fork.
// Distinct from agent-provided session IDs so a fork never collides with an
// existing tracked session.
func generateForkSessionID() string {
	return "fork-" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// shortForkID returns a stable short slug for branch naming derived from the
// session ID. Branch names must avoid path separators and stay readable.
func shortForkID(sessionID string) string {
	s := strings.TrimPrefix(sessionID, "fork-")
	if len(s) > id.ShortIDLength {
		s = s[:id.ShortIDLength]
	}
	return s
}

// printForkResult renders the new session ID and how to resume it.
func printForkResult(w io.Writer, r forkResult) {
	fmt.Fprintf(w, "Forked checkpoint %s into a new session.\n\n", r.SourceCheckpointID)
	fmt.Fprintf(w, "  source session:  %s\n", displayOrDash(r.SourceSessionID))
	fmt.Fprintf(w, "  new session:     %s\n", r.NewSessionID)
	if r.ForkBranch != "" {
		fmt.Fprintf(w, "  fork branch:     %s\n", r.ForkBranch)
		fmt.Fprintf(w, "  base commit:     %s\n", r.BaseCommit)
	} else {
		fmt.Fprintf(w, "  fork branch:     (none — no code commit resolved; session state cloned)\n")
	}
	fmt.Fprintf(w, "\nResume this fork with:\n")
	if r.ForkBranch != "" {
		fmt.Fprintf(w, "  git switch %s\n", r.ForkBranch)
	}
	fmt.Fprintf(w, "  trace session resume %s\n", r.NewSessionID)
}

// displayOrDash renders an em-dash for empty values so columns stay aligned.
func displayOrDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
