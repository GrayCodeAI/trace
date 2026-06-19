package strategy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cli/vercelconfig"
	"github.com/GrayCodeAI/trace/redact"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// Common branch name constants for default branch detection.
const (
	branchMain   = "main"
	branchMaster = "master"
	// originRemote is the default git remote name used for fetch/push fallbacks.
	originRemote = "origin"
	// Strategy name constants
	StrategyNameManualCommit = "manual-commit"
)

// MaxCommitTraversalDepth is the safety limit for walking git commit history.
// Prevents unbounded traversal in repositories with very long histories.
const MaxCommitTraversalDepth = 1000

// errStop is a sentinel error used to break out of git log iteration.
// Shared across strategies that iterate through git commits.
// NOTE: A similar sentinel exists in checkpoint/temporary.go - this is intentional.
// Each package needs its own package-scoped sentinel for git log iteration patterns.
var errStop = errors.New("stop iteration")

// IsEmptyRepository returns true if the repository has no commits yet.
// After git-init, HEAD points to an unborn branch (e.g., refs/heads/main)
// whose target does not yet exist. repo.Head() returns ErrReferenceNotFound
// in this case.
func IsEmptyRepository(repo *git.Repository) bool {
	_, err := repo.Head()
	return errors.Is(err, plumbing.ErrReferenceNotFound)
}

// EnsureSetup ensures the strategy is properly set up.
func EnsureSetup(ctx context.Context) error {
	if err := EnsureTraceGitignore(ctx); err != nil {
		return err
	}

	// Ensure the trace/checkpoints/v1 orphan branch exists for permanent session storage
	repo, err := OpenRepository(ctx)
	if err != nil {
		return fmt.Errorf("failed to open git repository: %w", err)
	}
	if err := vercelconfig.InitSettings(ctx); err != nil {
		return fmt.Errorf("failed to initialize vercel settings: %w", err)
	}
	if err := EnsureMetadataBranch(repo); err != nil {
		return fmt.Errorf("failed to ensure metadata branch: %w", err)
	}

	// Install generic hooks (they delegate to strategy at runtime)
	if !IsGitHookInstalled(ctx) {
		localDev, absoluteHookPath := hookSettingsFromConfig(ctx)
		if _, err := InstallGitHook(ctx, true, localDev, absoluteHookPath); err != nil {
			return fmt.Errorf("failed to install git hooks: %w", err)
		}
	}
	return nil
}

// FetchTmpRefPrefix is the namespace for temporary refs used by fetch helpers
// to land a fetched hash before safely promoting it to a final ref (via
// PromoteTmpRefSafely). Prefer using the named constants below when possible.
const FetchTmpRefPrefix = "refs/trace-fetch-tmp/"

// V2MainFetchTmpRef is the staging ref for fetches that target V2MainRefName.
// Shared between the cli package's origin-based fetches and the strategy
// package's checkpoint_remote URL-based fetch — those code paths never run
// concurrently (they are sequenced in explain and resume), so reusing one
// staging ref is safe and avoids divergent conventions.
const V2MainFetchTmpRef = FetchTmpRefPrefix + "v2-main"

// PromoteTmpRefSafely reads tmpRefName (the ref a fetch just landed into),
// advances destRefName to its hash via SafelyAdvanceLocalRef, then removes
// the tmp ref. The cleanup is deferred so the tmp ref is reaped even when
// the advance fails.
//
// label is a short human-readable name used in error messages (e.g.
// "v2 /main", "trace/checkpoints/v1"). Typical use:
//
//	// fetch with refspec "+<src>:<V2MainFetchTmpRef>"
//	return PromoteTmpRefSafely(ctx, V2MainFetchTmpRef, paths.V2MainRefName, "v2 /main")
func PromoteTmpRefSafely(ctx context.Context, tmpRefName, destRefName plumbing.ReferenceName, label string) error {
	repo, err := OpenRepository(ctx)
	if err != nil {
		return fmt.Errorf("failed to open repository for %s promote: %w", label, err)
	}
	defer func() { _ = repo.Storer.RemoveReference(tmpRefName) }() //nolint:errcheck // cleanup is best-effort

	tmpRef, err := repo.Reference(tmpRefName, true)
	if err != nil {
		return fmt.Errorf("%s not found after fetch (tmp ref %s missing): %w", label, tmpRefName, err)
	}
	if err := SafelyAdvanceLocalRef(ctx, repo, destRefName, tmpRef.Hash()); err != nil {
		return fmt.Errorf("failed to advance local %s: %w", label, err)
	}
	return nil
}

// SafelyAdvanceLocalRef updates localRefName to point at targetHash, except
// when the existing local ref is already at or ahead of targetHash. In that
// case it leaves the local ref unchanged to avoid rewinding locally-ahead
// work. Otherwise (local missing, behind, or diverged) it updates the ref to
// targetHash.
//
// The ancestry check walks from the local ref (which has full history), so
// callers that fetched with --depth=1 do not break the check.
func SafelyAdvanceLocalRef(ctx context.Context, repo *git.Repository, localRefName plumbing.ReferenceName, targetHash plumbing.Hash) error {
	currentLocal, localErr := repo.Reference(localRefName, true)
	if localErr == nil {
		if currentLocal.Hash() == targetHash {
			return nil
		}
		if IsAncestorOf(ctx, repo, targetHash, currentLocal.Hash()) {
			return nil
		}
	}

	newRef := plumbing.NewHashReference(localRefName, targetHash)
	if err := repo.Storer.SetReference(newRef); err != nil {
		return fmt.Errorf("failed to update local ref %s: %w", localRefName, err)
	}
	return nil
}

// IsAncestorOf checks if commit is an ancestor of (or equal to) target.
// Returns true if target can reach commit by following parent links.
// Limits search to MaxCommitTraversalDepth commits to avoid excessive traversal.
func IsAncestorOf(ctx context.Context, repo *git.Repository, commit, target plumbing.Hash) bool {
	if commit == target {
		return true
	}

	iter, err := repo.Log(&git.LogOptions{From: target})
	if err != nil {
		return false
	}
	defer iter.Close()

	found := false
	count := 0
	_ = iter.ForEach(func(c *object.Commit) error { //nolint:errcheck // Best-effort search, errors are non-fatal
		if err := ctx.Err(); err != nil {
			return err //nolint:wrapcheck // Propagating context cancellation
		}
		count++
		if count > MaxCommitTraversalDepth {
			return errStop
		}
		if c.Hash == commit {
			found = true
			return errStop
		}
		return nil
	})

	return found
}

// ListCheckpoints returns all checkpoints from the trace/checkpoints/v1 branch.
// Scans sharded paths: <id[:2]>/<id[2:]>/ directories containing metadata.json.
func ListCheckpoints(ctx context.Context) ([]CheckpointInfo, error) {
	repo, err := OpenRepository(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open git repository: %w", err)
	}

	// Warn (once per process) if metadata branches are disconnected
	WarnIfMetadataDisconnected()

	refName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	ref, err := repo.Reference(refName, true)
	if err != nil {
		//nolint:nilerr // No sessions branch yet is expected, return empty list
		return []CheckpointInfo{}, nil
	}

	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get commit object: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get commit tree: %w", err)
	}

	var checkpoints []CheckpointInfo

	// Scan sharded structure: <2-char-prefix>/<remaining-id>/metadata.json
	// The tree has 2-character directories (hex buckets)
	for _, bucketEntry := range tree.Entries {
		if bucketEntry.Mode != filemode.Dir {
			continue
		}
		// Bucket should be 2 hex chars
		if len(bucketEntry.Name) != 2 {
			continue
		}

		bucketTree, treeErr := repo.TreeObject(bucketEntry.Hash)
		if treeErr != nil {
			continue
		}

		// Each entry in the bucket is the remaining part of the checkpoint ID
		for _, checkpointEntry := range bucketTree.Entries {
			if checkpointEntry.Mode != filemode.Dir {
				continue
			}

			checkpointTree, cpTreeErr := repo.TreeObject(checkpointEntry.Hash)
			if cpTreeErr != nil {
				continue
			}

			// Reconstruct checkpoint ID: <bucket><remaining>
			checkpointIDStr := bucketEntry.Name + checkpointEntry.Name
			checkpointID, cpErr := id.NewCheckpointID(checkpointIDStr)
			if cpErr != nil {
				// Skip invalid checkpoint IDs
				continue
			}

			info := CheckpointInfo{
				CheckpointID: checkpointID,
			}

			// Get details from metadata file (CheckpointSummary format)
			if summary, ok := decodeSummaryLiteFromTree(checkpointTree); ok {
				info.CheckpointsCount = summary.CheckpointsCount
				info.FilesTouched = summary.FilesTouched
				info.SessionCount = len(summary.Sessions)

				// Read session-level metadata for Agent, SessionID, CreatedAt, SessionIDs
				for i, sessionPaths := range summary.Sessions {
					if sessionPaths.Metadata == "" {
						continue
					}
					// SessionFilePaths contains absolute paths with leading "/"
					// Strip the leading "/" for tree.File() which expects paths without leading slash
					sessionMetadataPath := strings.TrimPrefix(sessionPaths.Metadata, "/")
					sessionMeta, sErr := decodeSessionMetadataLite(tree, sessionMetadataPath)
					if sErr != nil {
						continue
					}
					info.SessionIDs = append(info.SessionIDs, sessionMeta.SessionID)
					// Use first session's metadata for Agent, SessionID, CreatedAt
					if i == 0 {
						info.Agent = sessionMeta.Agent
						info.SessionID = sessionMeta.SessionID
						info.CreatedAt = sessionMeta.CreatedAt
						info.IsTask = sessionMeta.IsTask
						info.ToolUseID = sessionMeta.ToolUseID
					}
				}
			}

			checkpoints = append(checkpoints, info)
		}
	}

	// Sort by time (most recent first)
	sort.Slice(checkpoints, func(i, j int) bool {
		return checkpoints[i].CreatedAt.After(checkpoints[j].CreatedAt)
	})

	return checkpoints, nil
}

const (
	traceGitignore     = ".trace/.gitignore"
	traceDir           = ".trace"
	gitDir             = ".git"
	shadowBranchPrefix = "trace/"
)

// isProtectedPath returns true if relPath is inside a directory that should
// never be modified or deleted during rewind or other destructive operations.
// Protected directories include git internals, trace metadata, and all
// registered agent config directories.
func isProtectedPath(relPath string) bool {
	for _, dir := range protectedDirs() {
		if paths.IsSubpath(dir, relPath) {
			return true
		}
	}
	return false
}

// protectedDirs returns the list of directories to protect. This combines
// static infrastructure dirs with agent-reported dirs from the registry.
// The result is cached via sync.Once since it's called per-file when filtering untracked files.
//
// NOTE: The cache is never invalidated. In production this is fine (the agent registry
// is populated at init time and never changes). However, tests that mutate the agent
// registry after the first call to protectedDirs/isProtectedPath will see stale results.
// If you need to test isProtectedPath with a custom registry, either:
//   - run those tests in a separate process, or
//   - call resetProtectedDirsForTest() to clear the cache.
func protectedDirs() []string {
	protectedDirsOnce.Do(func() {
		protectedDirsCache = append([]string{gitDir, traceDir}, agent.AllProtectedDirs()...)
	})
	return protectedDirsCache
}

var (
	protectedDirsOnce  sync.Once
	protectedDirsCache []string
)

var initRedactionOnce sync.Once

// EnsureRedactionConfigured loads PII redaction settings and configures the
// redact package. No-op if PII is not enabled in settings.
// Must be called at each process entry point before checkpoint writes
// (e.g., hook PersistentPreRunE, doctor PreRun).
func EnsureRedactionConfigured() {
	initRedactionOnce.Do(func() {
		ctx := context.Background()
		s, err := settings.Load(ctx)
		if err != nil {
			logCtx := logging.WithComponent(ctx, "redaction")
			logging.Warn(logCtx, "failed to load settings for PII redaction", slog.String("error", err.Error()))
			return
		}
		if s.Redaction == nil || s.Redaction.PII == nil || !s.Redaction.PII.Enabled {
			return
		}
		pii := s.Redaction.PII
		cfg := redact.PIIConfig{
			Enabled:        true,
			Categories:     make(map[redact.PIICategory]bool),
			CustomPatterns: pii.CustomPatterns,
		}
		// Email and phone default to true when PII is enabled; address defaults to false.
		cfg.Categories[redact.PIIEmail] = pii.Email == nil || *pii.Email
		cfg.Categories[redact.PIIPhone] = pii.Phone == nil || *pii.Phone
		cfg.Categories[redact.PIIAddress] = pii.Address != nil && *pii.Address
		redact.ConfigurePII(cfg)
	})
}

// resolveAgentType picks the best agent type from the context and existing state.
// Priority: existing state > context value.
func resolveAgentType(ctxAgentType types.AgentType, state *SessionState) types.AgentType {
	if state != nil && state.AgentType != "" {
		return state.AgentType
	}
	return ctxAgentType
}

// EnsureMetadataBranch creates or updates the local trace/checkpoints/v1 branch.
// If the remote-tracking branch (origin/trace/checkpoints/v1) exists and the local
// branch is missing or empty, creates/updates the local branch from it.
// Otherwise creates an empty orphan.
func EnsureMetadataBranch(repo *git.Repository) error {
	refName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)

	// Check if remote-tracking branch exists (e.g., after clone/fetch)
	remoteRefName := plumbing.NewRemoteReferenceName("origin", paths.MetadataBranchName)
	remoteRef, remoteErr := repo.Reference(remoteRefName, true)
	if remoteErr != nil && !errors.Is(remoteErr, plumbing.ErrReferenceNotFound) {
		return fmt.Errorf("failed to check remote metadata branch: %w", remoteErr)
	}

	// Check if local branch already exists
	localRef, err := repo.Reference(refName, true)
	if err == nil {
		if remoteErr == nil && localRef.Hash() != remoteRef.Hash() {
			// Local and remote exist but differ — determine relationship
			isEmpty, checkErr := isEmptyMetadataBranch(repo, localRef)
			if checkErr != nil {
				return fmt.Errorf("failed to check metadata branch contents: %w", checkErr)
			}
			if isEmpty {
				// Empty orphan — just point to remote
				ref := plumbing.NewHashReference(refName, remoteRef.Hash())
				if setErr := repo.Storer.SetReference(ref); setErr != nil {
					return fmt.Errorf("failed to update metadata branch from remote: %w", setErr)
				}
				fmt.Fprintf(os.Stderr, "[trace] Updated local branch '%s' from origin\n", paths.MetadataBranchName)
			} else {
				// Local has real data and differs from remote — if disconnected
				// (no common ancestor), reconciliation happens at pre-push time
				// or via 'trace doctor'. Read paths warn but do not auto-fix.
				logging.Debug(
					context.Background(), "metadata branch differs from remote, reconciliation deferred to read/write time",
					"local_hash", localRef.Hash().String()[:7],
					"remote_hash", remoteRef.Hash().String()[:7],
				)
			}
		}
		return nil
	}
	if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return fmt.Errorf("failed to check metadata branch: %w", err)
	}

	// Local branch doesn't exist — create from remote if available
	if remoteErr == nil {
		ref := plumbing.NewHashReference(refName, remoteRef.Hash())
		if err := repo.Storer.SetReference(ref); err != nil {
			return fmt.Errorf("failed to create metadata branch from remote: %w", err)
		}
		fmt.Fprintf(os.Stderr, "✓ Created local branch '%s' from origin\n", paths.MetadataBranchName)
		return nil
	}

	// No local or remote branch — create empty orphan
	emptyTree := &object.Tree{Entries: []object.TreeEntry{}}
	obj := repo.Storer.NewEncodedObject()
	if err := emptyTree.Encode(obj); err != nil {
		return fmt.Errorf("failed to encode empty tree: %w", err)
	}
	emptyTreeHash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return fmt.Errorf("failed to store empty tree: %w", err)
	}
	emptyTreeHash, err = vercelconfig.MaybeMergeMetadataBranchConfig(repo, emptyTreeHash)
	if err != nil {
		return fmt.Errorf("failed to initialize metadata branch vercel config: %w", err)
	}

	// Create orphan commit (no parent)
	now := time.Now()
	authorName, authorEmail := GetGitAuthorFromRepo(repo)
	sig := object.Signature{
		Name:  authorName,
		Email: authorEmail,
		When:  now,
	}

	commit := &object.Commit{
		TreeHash:  emptyTreeHash,
		Author:    sig,
		Committer: sig,
		Message:   "Initialize metadata branch\n\nThis branch stores session metadata.\n",
	}
	// Note: No ParentHashes - this is an orphan commit

	commitObj := repo.Storer.NewEncodedObject()
	if err := commit.Encode(commitObj); err != nil {
		return fmt.Errorf("failed to encode orphan commit: %w", err)
	}
	commitHash, err := repo.Storer.SetEncodedObject(commitObj)
	if err != nil {
		return fmt.Errorf("failed to store orphan commit: %w", err)
	}

	// Create branch reference
	ref := plumbing.NewHashReference(refName, commitHash)
	if err := repo.Storer.SetReference(ref); err != nil {
		return fmt.Errorf("failed to create metadata branch: %w", err)
	}

	fmt.Fprintf(os.Stderr, "  ✓ Created orphan branch %s for session metadata\n", paths.MetadataBranchName)
	return nil
}

// isEmptyMetadataBranch returns true if the branch ref points to a commit with an empty tree.
// Only checks the tip commit — if a data commit sits on top of an empty orphan, this returns
// false, which is correct: the bug this detects creates a single empty orphan as the tip.
func isEmptyMetadataBranch(repo *git.Repository, ref *plumbing.Reference) (bool, error) {
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return false, fmt.Errorf("failed to get commit: %w", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return false, fmt.Errorf("failed to get tree: %w", err)
	}
	return len(tree.Entries) == 0, nil
}

// sessionMetadataLite contains only the fields needed from session-level metadata.json.
// Using a minimal struct avoids allocating large nested objects (Summary, InitialAttribution,
// TokenUsage, etc.) that CommittedMetadata carries but callers never need here.
type sessionMetadataLite struct {
	SessionID string          `json:"session_id"`
	Agent     types.AgentType `json:"agent,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	IsTask    bool            `json:"is_task,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
}

// checkpointSummaryLite contains only the fields needed from the root metadata.json.
// Avoids allocating TokenUsage and other heavy fields from CheckpointSummary.
type checkpointSummaryLite struct {
	CheckpointID     id.CheckpointID               `json:"checkpoint_id"`
	CheckpointsCount int                           `json:"checkpoints_count"`
	FilesTouched     []string                      `json:"files_touched"`
	Sessions         []checkpoint.SessionFilePaths `json:"sessions"`
}

// decodeSessionMetadataLite reads a session metadata.json from the tree using a streaming
// json.Decoder and a minimal struct to avoid allocating large unused fields.
func decodeSessionMetadataLite(tree checkpoint.FileReader, metadataPath string) (*sessionMetadataLite, error) {
	file, err := tree.File(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("session metadata file %s: %w", metadataPath, err)
	}
	reader, err := file.Reader()
	if err != nil {
		return nil, fmt.Errorf("session metadata reader %s: %w", metadataPath, err)
	}
	defer reader.Close()

	var meta sessionMetadataLite
	if err := json.NewDecoder(reader).Decode(&meta); err != nil {
		return nil, fmt.Errorf("decode session metadata %s: %w", metadataPath, err)
	}
	return &meta, nil
}

// decodeSummaryLiteFromTree reads and decodes metadata.json from a checkpoint tree
// using a streaming decoder and minimal struct. Returns the decoded summary and true
// if successful with at least one session, or zero value and false otherwise.
func decodeSummaryLiteFromTree(checkpointTree checkpoint.FileReader) (checkpointSummaryLite, bool) {
	metadataFile, fileErr := checkpointTree.File(paths.MetadataFileName)
	if fileErr != nil {
		return checkpointSummaryLite{}, false
	}
	reader, readerErr := metadataFile.Reader()
	if readerErr != nil {
		return checkpointSummaryLite{}, false
	}
	defer reader.Close()

	var summary checkpointSummaryLite
	if err := json.NewDecoder(reader).Decode(&summary); err != nil || len(summary.Sessions) == 0 {
		return checkpointSummaryLite{}, false
	}
	return summary, true
}

// ReadCheckpointMetadata reads metadata.json from a checkpoint path on trace/checkpoints/v1.
// With the new format, root metadata.json is a CheckpointSummary with Agents array.
// This function reads the summary and extracts relevant fields into CheckpointInfo,
// also reading session-level metadata for IsTask/ToolUseID fields.
//
// Uses streaming json.Decoder and minimal structs to avoid loading large nested
// objects (Summary, InitialAttribution, TokenUsage) into memory.
func ReadCheckpointMetadata(tree checkpoint.FileReader, checkpointPath string) (*CheckpointInfo, error) {
	metadataPath := checkpointPath + "/metadata.json"
	file, err := tree.File(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to find metadata at %s: %w", metadataPath, err)
	}

	// Session metadata paths in the summary are absolute (e.g., "/ca/b75de47439/0/metadata.json").
	// For a full tree, strip the leading "/" to get tree-relative paths.
	normalizePath := func(raw string) string {
		return strings.TrimPrefix(raw, "/")
	}
	return decodeCheckpointInfo(file, tree, checkpointPath, normalizePath)
}

// ReadCheckpointMetadataFromSubtree reads checkpoint metadata from a tree that is
// already rooted at the checkpoint directory (e.g., after tree.Tree(checkpointID.Path())).
// checkpointPath is the original sharded path (e.g., "ca/b75de47439") and is used
// to strip the prefix from absolute session metadata paths stored in the summary.
func ReadCheckpointMetadataFromSubtree(tree checkpoint.FileReader, checkpointPath string) (*CheckpointInfo, error) {
	file, err := tree.File(paths.MetadataFileName)
	if err != nil {
		return nil, fmt.Errorf("failed to find %s in checkpoint subtree: %w", paths.MetadataFileName, err)
	}

	// Session metadata paths are absolute from the tree root (e.g., "/ca/b75de47439/0/metadata.json").
	// Strip the checkpoint prefix to get paths relative to the subtree (e.g., "0/metadata.json").
	prefix := "/" + checkpointPath + "/"
	normalizePath := func(raw string) string {
		return strings.TrimPrefix(raw, prefix)
	}
	return decodeCheckpointInfo(file, tree, checkpointPath, normalizePath)
}

// decodeCheckpointInfo is the shared implementation for ReadCheckpointMetadata and
// ReadCheckpointMetadataFromSubtree. It decodes the root metadata.json, reads
// per-session metadata, and populates a CheckpointInfo.
//
// normalizePath transforms absolute session metadata paths from the summary into
// paths that are valid for tree.File() lookups (the transform differs depending on
// whether tree is a full metadata branch tree or a checkpoint subtree).
func decodeCheckpointInfo(
	file checkpoint.FileOpener,
	tree checkpoint.FileReader,
	checkpointPath string,
	normalizePath func(string) string,
) (*CheckpointInfo, error) {
	reader, err := file.Reader()
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata: %w", err)
	}
	defer reader.Close()

	// Try to parse as CheckpointSummary first (new format) using lite struct
	var summary checkpointSummaryLite
	if decodeErr := json.NewDecoder(reader).Decode(&summary); decodeErr == nil {
		if len(summary.Sessions) > 0 {
			info := &CheckpointInfo{
				CheckpointID:     summary.CheckpointID,
				CheckpointsCount: summary.CheckpointsCount,
				FilesTouched:     summary.FilesTouched,
				SessionCount:     len(summary.Sessions),
			}

			// Read all sessions' metadata to populate SessionIDs and get other fields from first session
			var sessionIDs []string
			for i, sessionPaths := range summary.Sessions {
				if sessionPaths.Metadata == "" {
					continue
				}
				sessionMetadataPath := normalizePath(sessionPaths.Metadata)
				sessionMeta, sErr := decodeSessionMetadataLite(tree, sessionMetadataPath)
				if sErr != nil {
					logging.Debug(
						context.Background(), "decodeCheckpointInfo: session metadata decode failed",
						slog.Int("session_index", i),
						slog.String("metadata_path", sessionMetadataPath),
						slog.String("checkpoint_path", checkpointPath),
						slog.String("error", sErr.Error()),
					)
					continue
				}
				sessionIDs = append(sessionIDs, sessionMeta.SessionID)
				if i == 0 {
					info.Agent = sessionMeta.Agent
					info.SessionID = sessionMeta.SessionID
					info.CreatedAt = sessionMeta.CreatedAt
					info.IsTask = sessionMeta.IsTask
					info.ToolUseID = sessionMeta.ToolUseID
				}
			}
			info.SessionIDs = sessionIDs
			return info, nil
		}
	}

	// Fall back to parsing as CheckpointInfo (old format or direct info).
	// Re-read the file since the decoder consumed the reader.
	fallbackReader, err := file.Reader()
	if err != nil {
		return nil, fmt.Errorf("failed to re-read metadata: %w", err)
	}
	defer fallbackReader.Close()

	var metadata CheckpointInfo
	if err := json.NewDecoder(fallbackReader).Decode(&metadata); err != nil {
		return nil, fmt.Errorf("failed to parse metadata: %w", err)
	}
	return &metadata, nil
}

// GetMetadataBranchTree returns the tree object for the trace/checkpoints/v1 branch.
func GetMetadataBranchTree(repo *git.Repository) (*object.Tree, error) {
	refName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	ref, err := repo.Reference(refName, true)
	if err != nil {
		return nil, fmt.Errorf("failed to get metadata branch reference: %w", err)
	}

	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get metadata branch commit: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get metadata branch tree: %w", err)
	}
	return tree, nil
}

// GetV2MetadataBranchTree returns the tree object at the tip of the v2 /main ref.
// The v2 /main ref uses the same sharded checkpoint layout as v1, so
// ReadLatestSessionPromptFromCommittedTree works with either tree.
func GetV2MetadataBranchTree(repo *git.Repository) (*object.Tree, error) {
	refName := plumbing.ReferenceName(paths.V2MainRefName)
	ref, err := repo.Reference(refName, true)
	if err != nil {
		return nil, fmt.Errorf("failed to get v2 /main reference: %w", err)
	}

	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get v2 /main commit: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get v2 /main tree: %w", err)
	}
	return tree, nil
}

// ExtractFirstPrompt extracts and truncates the first meaningful prompt from prompt content.
// Prompts are separated by "\n\n---\n\n". Skips empty prompts and separator-only content.
// Returns empty string if no valid prompt is found.
func ExtractFirstPrompt(content string) string {
	if content == "" {
		return ""
	}

	// Prompts are separated by "\n\n---\n\n"
	// Find the first non-empty prompt
	prompts := strings.Split(content, "\n\n---\n\n")
	var firstPrompt string
	for _, p := range prompts {
		cleaned := strings.TrimSpace(p)
		// Skip empty prompts or prompts that are just dashes/separators
		if cleaned == "" || isOnlySeparators(cleaned) {
			continue
		}
		firstPrompt = cleaned
		break
	}

	if firstPrompt == "" {
		return ""
	}

	return TruncateDescription(firstPrompt, MaxDescriptionLength)
}

// ReadSessionPromptFromTree reads the first meaningful prompt from a checkpoint's prompt.txt file in a git tree.
// Returns an empty string if the prompt cannot be read.
func ReadSessionPromptFromTree(tree *object.Tree, checkpointPath string) string {
	promptPath := checkpointPath + "/" + paths.PromptFileName
	file, err := tree.File(promptPath)
	if err != nil {
		return ""
	}

	content, err := file.Contents()
	if err != nil {
		return ""
	}

	return ExtractFirstPrompt(content)
}

// ReadAgentTypeFromTree reads the agent type from a checkpoint's metadata.json file in a git tree.
// If metadata.json doesn't exist (shadow branches), it falls back to detecting the agent
// from the presence of agent-specific config files (.gemini/settings.json or .claude/).
// Returns agent.AgentTypeUnknown if the agent type cannot be determined.
func ReadAgentTypeFromTree(tree *object.Tree, checkpointPath string) types.AgentType {
	// First, try to read from metadata.json (present in condensed/committed checkpoints)
	metadataPath := checkpointPath + "/" + paths.MetadataFileName
	if file, err := tree.File(metadataPath); err == nil {
		if content, err := file.Contents(); err == nil {
			var metadata checkpoint.CommittedMetadata
			if err := json.Unmarshal([]byte(content), &metadata); err == nil && metadata.Agent != "" {
				return metadata.Agent
			}
		}
	}

	// Fall back to detecting agent from config markers (shadow branches don't have metadata.json).
	// Multiple agent config markers may coexist when users configure multiple agents via
	// `trace configure`. Only return a specific agent type when exactly one agent config
	// marker (directory or file) is present; otherwise return Unknown since we can't
	// determine which agent created the checkpoint.
	var detected types.AgentType
	detectedCount := 0

	if _, err := tree.File(".gemini/settings.json"); err == nil {
		detected = agent.AgentTypeGemini
		detectedCount++
	}
	if _, err := tree.Tree(".claude"); err == nil {
		detected = agent.AgentTypeClaudeCode
		detectedCount++
	}
	if _, err := tree.Tree(".opencode"); err == nil {
		detected = agent.AgentTypeOpenCode
		detectedCount++
	} else if _, err := tree.File("opencode.json"); err == nil {
		detected = agent.AgentTypeOpenCode
		detectedCount++
	}
	if _, err := tree.Tree(".codex"); err == nil {
		detected = agent.AgentTypeCodex
		detectedCount++
	}
	if _, err := tree.Tree(".cursor"); err == nil {
		detected = agent.AgentTypeCursor
		detectedCount++
	}
	if _, err := tree.Tree(".factory"); err == nil {
		detected = agent.AgentTypeFactoryAIDroid
		detectedCount++
	}

	if detectedCount == 1 {
		return detected
	}
	return agent.AgentTypeUnknown
}
