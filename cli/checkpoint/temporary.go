package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/jsonutil"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/trailers"
	"github.com/GrayCodeAI/trace/cli/validation"
	"github.com/GrayCodeAI/trace/redact"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

const (
	// ShadowBranchPrefix is the prefix for shadow branches.
	ShadowBranchPrefix = "trace/"

	// ShadowBranchHashLength is the number of hex characters used in shadow branch names.
	// Shadow branches are named "trace/<hash>" using the first 12 characters of the commit hash.
	// Increased from 7 to 12 to reduce collision probability in large repositories.
	ShadowBranchHashLength = 12

	// WorktreeIDHashLength is the number of hex characters used for worktree ID hash.
	// Increased from 6 to 10 to reduce collision probability when multiple worktrees
	// share similar base commits.
	WorktreeIDHashLength = 10
)

// HashWorktreeID returns a short hash of the worktree identifier.
// Used to create unique shadow branch names per worktree.
func HashWorktreeID(worktreeID string) string {
	h := sha256.Sum256([]byte(worktreeID))
	return hex.EncodeToString(h[:])[:WorktreeIDHashLength]
}

// WriteTemporary writes a temporary checkpoint to a shadow branch.
// Shadow branches are named trace/<base-commit-short-hash>.
// Returns the result containing commit hash and whether it was skipped.
// If the new tree hash matches the last checkpoint's tree hash, the checkpoint
// is skipped to avoid duplicate commits (deduplication).
func (s *GitStore) WriteTemporary(ctx context.Context, opts WriteTemporaryOptions) (WriteTemporaryResult, error) {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	// Validate base commit - required for shadow branch naming
	if opts.BaseCommit == "" {
		return WriteTemporaryResult{}, errors.New("BaseCommit is required for temporary checkpoint")
	}

	// Validate session ID to prevent path traversal
	if err := validation.ValidateSessionID(opts.SessionID); err != nil {
		return WriteTemporaryResult{}, fmt.Errorf("invalid temporary checkpoint options: %w", err)
	}

	// Serialize all storer access in-process. go-git's filesystem storer is
	// not safe for concurrent read+write even across separate Repository
	// instances that share the same .git directory.

	// Get shadow branch name
	shadowBranchName := ShadowBranchNameForCommit(opts.BaseCommit, opts.WorktreeID)

	// Collect all files to include
	var allFiles []string
	var allDeletedFiles []string
	if opts.IsFirstCheckpoint {
		// For the first checkpoint, capture all changed files (modified tracked + untracked)
		// using `git status --porcelain -z` which respects both repo and global .gitignore.
		// This is much faster than filesystem walk. The base tree from HEAD already contains
		// all unchanged tracked files. We also capture user's pre-existing deletions.
		result, err := collectChangedFiles(ctx, s.repo)
		if err != nil {
			return WriteTemporaryResult{}, fmt.Errorf("failed to collect changed files: %w", err)
		}
		allFiles = result.Changed
		// Merge user's pre-existing deletions with agent's deletions
		allDeletedFiles = result.Deleted
		allDeletedFiles = append(allDeletedFiles, opts.DeletedFiles...)
	} else {
		// For subsequent checkpoints, only include modified/new files.
		// Filter out gitignored files — agent transcripts may report files like .env
		// that exist on disk but are gitignored. Without filtering, secrets in gitignored
		// files would leak into the shadow branch and could be pushed to remotes.
		candidateFiles := make([]string, 0, len(opts.ModifiedFiles)+len(opts.NewFiles))
		candidateFiles = append(candidateFiles, opts.ModifiedFiles...)
		candidateFiles = append(candidateFiles, opts.NewFiles...)
		allFiles = filterGitIgnoredFiles(ctx, s.repo, candidateFiles)
		allDeletedFiles = opts.DeletedFiles
	}

	// Create checkpoint commit message (constant across retries)
	commitMsg := trailers.FormatShadowCommit(opts.CommitMessage, opts.MetadataDir, opts.SessionID)

	repoRoot, commonDir, err := s.repoDirs(ctx)
	if err != nil {
		return WriteTemporaryResult{}, fmt.Errorf("failed to resolve repo dirs: %w", err)
	}

	var result WriteTemporaryResult
	// withShadowBranchFlock serializes all writers targeting this shadow
	// branch — across goroutines and across processes — so the inner CAS
	// only sees contention from external `git update-ref` callers (rare).
	err = withShadowBranchFlock(commonDir, shadowBranchName, func() error {
		// Open a fresh repo to avoid storer contention with concurrent writers.
		// go-git's storer is not fully thread-safe for concurrent write+read
		// on the same instance. The flock serializes our own writes, but other
		// goroutines may be reading from the shared storer concurrently.
		freshRepo, frErr := s.openFreshRepo()
		if frErr != nil {
			return fmt.Errorf("open repo for checkpoint: %w", frErr)
		}
		store := &GitStore{repo: freshRepo, repoPath: s.repoPath, blobFetcher: s.blobFetcher}

		// Tiny CAS retry budget: with the flock held, races against our own
		// code are impossible. Retries cover the pathological case of an
		// external writer (a user invoking `git update-ref` manually, etc.).
		for attempt := range shadowRefMaxRetries {
			parentHash, baseTreeHash, gErr := store.getOrCreateShadowBranch(shadowBranchName)
			if gErr != nil {
				return fmt.Errorf("failed to get shadow branch: %w", gErr)
			}

			// Get the last checkpoint's tree hash for deduplication
			var lastTreeHash plumbing.Hash
			if parentHash != plumbing.ZeroHash {
				if lastCommit, lcErr := store.repo.CommitObject(parentHash); lcErr == nil {
					lastTreeHash = lastCommit.TreeHash
				}
			}

			treeHash, tErr := store.buildTreeWithChanges(ctx, baseTreeHash, allFiles, allDeletedFiles, opts.MetadataDir, opts.MetadataDirAbs)
			if tErr != nil {
				return fmt.Errorf("failed to build tree: %w", tErr)
			}

			// Deduplication: skip if tree hash matches the current shadow tip.
			if lastTreeHash != plumbing.ZeroHash && treeHash == lastTreeHash {
				result = WriteTemporaryResult{
					CommitHash: parentHash,
					Skipped:    true,
				}
				return nil
			}

			commitHash, cErr := store.createCommit(ctx, treeHash, parentHash, commitMsg, opts.AuthorName, opts.AuthorEmail)
			if cErr != nil {
				return fmt.Errorf("failed to create commit: %w", cErr)
			}

			refErr := casUpdateShadowBranchRef(ctx, repoRoot, shadowBranchName, commitHash, parentHash)
			if refErr == nil {
				result = WriteTemporaryResult{
					CommitHash: commitHash,
					Skipped:    false,
				}
				return nil
			}
			if !errors.Is(refErr, ErrShadowRefBusy) {
				return fmt.Errorf("failed to update shadow branch reference: %w", refErr)
			}
			// Our commit is now dangling — best-effort remove it so we don't
			// leak loose objects across many losing attempts.
			tryDeleteLooseObject(commonDir, commitHash)
			if bErr := shadowRefBackoff(ctx, attempt); bErr != nil {
				return bErr
			}
		}
		// Retry budget exhausted. With the flock held this means an external
		// writer beat us shadowRefMaxRetries times in a row — surface it in
		// logs so operators can see a stuck shadow branch.
		logging.Warn(
			logging.WithComponent(ctx, "checkpoint"),
			"shadow branch CAS retry budget exhausted",
			slog.String("shadow_branch", shadowBranchName),
			slog.Int("retries", shadowRefMaxRetries),
		)
		return fmt.Errorf("failed to update shadow branch reference after %d CAS retries: %w", shadowRefMaxRetries, ErrShadowRefBusy)
	})
	if err != nil {
		return WriteTemporaryResult{}, err
	}
	return result, nil
}

// ReadTemporary reads the latest checkpoint from a shadow branch.
// Returns nil if the shadow branch doesn't exist.
// worktreeID should be empty for main worktree or the internal git worktree name for linked worktrees.
func (s *GitStore) ReadTemporary(ctx context.Context, baseCommit, worktreeID string) (*ReadTemporaryResult, error) {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err //nolint:wrapcheck // Propagating context cancellation
	}

	shadowBranchName := ShadowBranchNameForCommit(baseCommit, worktreeID)
	refName := plumbing.NewBranchReferenceName(shadowBranchName)

	ref, err := s.repo.Reference(refName, true)
	if err != nil {
		return nil, nil //nolint:nilnil,nilerr // Branch not found is an expected case
	}

	commit, err := s.repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get commit object: %w", err)
	}

	// Extract session ID and metadata dir from commit trailers
	sessionID, _ := trailers.ParseSession(commit.Message)
	metadataDir, _ := trailers.ParseMetadata(commit.Message)

	return &ReadTemporaryResult{
		CommitHash:  ref.Hash(),
		TreeHash:    commit.TreeHash,
		SessionID:   sessionID,
		MetadataDir: metadataDir,
		Timestamp:   commit.Author.When,
	}, nil
}

// ListTemporary lists all shadow branches with their checkpoint info.
func (s *GitStore) ListTemporary(ctx context.Context) ([]TemporaryInfo, error) {
	StorerMu.Lock()
	defer StorerMu.Unlock()
	return s.listTemporary(ctx)
}

// listTemporary is the unlocked internal implementation. Callers must hold StorerMu.
func (s *GitStore) listTemporary(ctx context.Context) ([]TemporaryInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err //nolint:wrapcheck // Propagating context cancellation
	}

	iter, err := s.repo.Branches()
	if err != nil {
		return nil, fmt.Errorf("failed to list branches: %w", err)
	}

	var results []TemporaryInfo
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		if err := ctx.Err(); err != nil {
			return err //nolint:wrapcheck // Propagating context cancellation
		}
		branchName := ref.Name().Short()
		if !strings.HasPrefix(branchName, ShadowBranchPrefix) {
			return nil
		}

		// Skip the sessions branch
		if branchName == paths.MetadataBranchName {
			return nil
		}

		commit, commitErr := s.repo.CommitObject(ref.Hash())
		if commitErr != nil {
			//nolint:nilerr // Skip branches we can't read (non-fatal)
			return nil
		}

		sessionID, _ := trailers.ParseSession(commit.Message)

		// Extract base commit from branch name (handles new "trace/<commit>-<worktreeHash>" format)
		baseCommit, _, _ := ParseShadowBranchName(branchName)

		results = append(results, TemporaryInfo{
			BranchName:   branchName,
			BaseCommit:   baseCommit,
			LatestCommit: ref.Hash(),
			SessionID:    sessionID,
			Timestamp:    commit.Author.When,
		})

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to iterate branches: %w", err)
	}

	return results, nil
}

// WriteTemporaryTask writes a task checkpoint to a shadow branch.
// Task checkpoints include both code changes and task-specific metadata.
// Returns the commit hash of the created checkpoint.
func (s *GitStore) WriteTemporaryTask(ctx context.Context, opts WriteTemporaryTaskOptions) (plumbing.Hash, error) {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	// Validate base commit - required for shadow branch naming
	if opts.BaseCommit == "" {
		return plumbing.ZeroHash, errors.New("BaseCommit is required for task checkpoint")
	}

	// Validate identifiers to prevent path traversal and malformed data
	if err := validation.ValidateSessionID(opts.SessionID); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("invalid task checkpoint options: %w", err)
	}
	if err := validation.ValidateToolUseID(opts.ToolUseID); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("invalid task checkpoint options: %w", err)
	}
	if err := validation.ValidateAgentID(opts.AgentID); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("invalid task checkpoint options: %w", err)
	}

	// Get shadow branch name
	shadowBranchName := ShadowBranchNameForCommit(opts.BaseCommit, opts.WorktreeID)

	// Collect all files to include in the commit.
	// Filter out gitignored files — subagent transcripts may report files like .env
	// that exist on disk but are gitignored. Without filtering, secrets would leak
	// into the shadow branch.
	candidateFiles := make([]string, 0, len(opts.ModifiedFiles)+len(opts.NewFiles))
	candidateFiles = append(candidateFiles, opts.ModifiedFiles...)
	candidateFiles = append(candidateFiles, opts.NewFiles...)
	allFiles := filterGitIgnoredFiles(ctx, s.repo, candidateFiles)

	repoRoot, commonDir, err := s.repoDirs(ctx)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to resolve repo dirs: %w", err)
	}

	var resultHash plumbing.Hash
	err = withShadowBranchFlock(commonDir, shadowBranchName, func() error {
		freshRepo, frErr := s.openFreshRepo()
		if frErr != nil {
			return fmt.Errorf("open repo for task checkpoint: %w", frErr)
		}
		store := &GitStore{repo: freshRepo, repoPath: s.repoPath, blobFetcher: s.blobFetcher}

		for attempt := range shadowRefMaxRetries {
			parentHash, baseTreeHash, gErr := store.getOrCreateShadowBranch(shadowBranchName)
			if gErr != nil {
				return fmt.Errorf("failed to get shadow branch: %w", gErr)
			}

			newTreeHash, tErr := store.buildTreeWithChanges(ctx, baseTreeHash, allFiles, opts.DeletedFiles, "", "")
			if tErr != nil {
				return fmt.Errorf("failed to build tree: %w", tErr)
			}

			newTreeHash, tErr = store.addTaskMetadataToTree(ctx, newTreeHash, opts)
			if tErr != nil {
				return fmt.Errorf("failed to add task metadata: %w", tErr)
			}

			commitHash, cErr := store.createCommit(ctx, newTreeHash, parentHash, opts.CommitMessage, opts.AuthorName, opts.AuthorEmail)
			if cErr != nil {
				return fmt.Errorf("failed to create commit: %w", cErr)
			}

			refErr := casUpdateShadowBranchRef(ctx, repoRoot, shadowBranchName, commitHash, parentHash)
			if refErr == nil {
				resultHash = commitHash
				return nil
			}
			if !errors.Is(refErr, ErrShadowRefBusy) {
				return fmt.Errorf("failed to update shadow branch reference: %w", refErr)
			}
			tryDeleteLooseObject(commonDir, commitHash)
			if bErr := shadowRefBackoff(ctx, attempt); bErr != nil {
				return bErr
			}
		}
		logging.Warn(
			logging.WithComponent(ctx, "checkpoint"),
			"shadow branch CAS retry budget exhausted (task checkpoint)",
			slog.String("shadow_branch", shadowBranchName),
			slog.Int("retries", shadowRefMaxRetries),
		)
		return fmt.Errorf("failed to update shadow branch reference after %d CAS retries: %w", shadowRefMaxRetries, ErrShadowRefBusy)
	})
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return resultHash, nil
}

// addTaskMetadataToTree adds task checkpoint metadata to a git tree.
// When IsIncremental is true, only adds the incremental checkpoint file.
//
// Uses ApplyTreeChanges (tree surgery) instead of FlattenTree+BuildTreeFromEntries,
// so only affected subtrees are read/rebuilt.
func (s *GitStore) addTaskMetadataToTree(ctx context.Context, baseTreeHash plumbing.Hash, opts WriteTemporaryTaskOptions) (plumbing.Hash, error) {
	// Compute metadata paths
	sessionMetadataDir := paths.TraceMetadataDir + "/" + opts.SessionID
	taskMetadataDir := sessionMetadataDir + "/tasks/" + opts.ToolUseID

	var changes []TreeChange

	if opts.IsIncremental {
		// Incremental checkpoint: only add the checkpoint file
		var incData json.RawMessage
		if opts.IncrementalData != nil {
			redacted, redactErr := redact.JSONLBytes(opts.IncrementalData)
			if redactErr != nil {
				return plumbing.ZeroHash, fmt.Errorf("failed to redact incremental checkpoint: %w", redactErr)
			}
			incData = json.RawMessage(redacted.Bytes())
		}
		incrementalCheckpoint := struct {
			Type      string          `json:"type"`
			ToolUseID string          `json:"tool_use_id"`
			Timestamp time.Time       `json:"timestamp"`
			Data      json.RawMessage `json:"data"`
		}{
			Type:      opts.IncrementalType,
			ToolUseID: opts.ToolUseID,
			Timestamp: time.Now().UTC(),
			Data:      incData,
		}
		cpData, err := jsonutil.MarshalIndentWithNewline(incrementalCheckpoint, "", "  ")
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("failed to marshal incremental checkpoint: %w", err)
		}

		cpBlobHash, err := CreateBlobFromContent(s.repo, cpData)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("failed to create incremental checkpoint blob: %w", err)
		}
		cpFilename := fmt.Sprintf("%03d-%s.json", opts.IncrementalSequence, opts.ToolUseID)
		cpPath := taskMetadataDir + "/checkpoints/" + cpFilename
		changes = append(changes, TreeChange{
			Path:  cpPath,
			Entry: &object.TreeEntry{Mode: filemode.Regular, Hash: cpBlobHash},
		})
	} else {
		// Final checkpoint: add transcripts and checkpoint.json

		// Add session transcript (with chunking support for large transcripts)
		if opts.TranscriptPath != "" {
			if transcriptContent, readErr := os.ReadFile(opts.TranscriptPath); readErr == nil {
				agentType := agent.DetectAgentTypeFromContent(transcriptContent)

				// Chunk if necessary
				chunks, chunkErr := agent.ChunkTranscript(ctx, transcriptContent, agentType)
				if chunkErr != nil {
					logging.Warn(
						ctx, "failed to chunk transcript, checkpoint will be saved without transcript",
						slog.String("error", chunkErr.Error()),
						slog.String("session_id", opts.SessionID),
					)
				} else {
					for i, chunk := range chunks {
						chunkPath := sessionMetadataDir + "/" + agent.ChunkFileName(paths.TranscriptFileName, i)
						blobHash, blobErr := CreateBlobFromContent(s.repo, chunk)
						if blobErr != nil {
							logging.Warn(
								ctx, "failed to create blob for transcript chunk",
								slog.String("error", blobErr.Error()),
								slog.String("session_id", opts.SessionID),
								slog.Int("chunk_index", i),
							)
							continue
						}
						changes = append(changes, TreeChange{
							Path:  chunkPath,
							Entry: &object.TreeEntry{Mode: filemode.Regular, Hash: blobHash},
						})
					}
				}
			}
		}

		// Add subagent transcript if available
		if opts.SubagentTranscriptPath != "" && opts.AgentID != "" {
			if agentContent, readErr := os.ReadFile(opts.SubagentTranscriptPath); readErr == nil {
				redacted, jsonlErr := redact.JSONLBytes(agentContent)
				if jsonlErr != nil {
					logging.Warn(
						ctx, "subagent transcript is not valid JSONL, falling back to plain redaction",
						slog.String("path", opts.SubagentTranscriptPath),
						slog.String("error", jsonlErr.Error()),
					)
					agentContent = redact.Bytes(agentContent)
				} else {
					agentContent = redacted.Bytes()
				}
				if blobHash, blobErr := CreateBlobFromContent(s.repo, agentContent); blobErr == nil {
					agentPath := taskMetadataDir + "/agent-" + opts.AgentID + ".jsonl"
					changes = append(changes, TreeChange{
						Path:  agentPath,
						Entry: &object.TreeEntry{Mode: filemode.Regular, Hash: blobHash},
					})
				}
			}
		}

		// Add checkpoint.json
		checkpointJSON := fmt.Sprintf(`{
  "session_id": %q,
  "tool_use_id": %q,
  "checkpoint_uuid": %q,
  "agent_id": %q
}`, opts.SessionID, opts.ToolUseID, opts.CheckpointUUID, opts.AgentID)

		blobHash, err := CreateBlobFromContent(s.repo, []byte(checkpointJSON))
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("failed to create checkpoint blob: %w", err)
		}
		checkpointPath := taskMetadataDir + "/checkpoint.json"
		changes = append(changes, TreeChange{
			Path:  checkpointPath,
			Entry: &object.TreeEntry{Mode: filemode.Regular, Hash: blobHash},
		})
	}

	return ApplyTreeChanges(ctx, s.repo, baseTreeHash, changes)
}

// ListTemporaryCheckpoints lists all checkpoint commits on a shadow branch.
// This returns individual commits (rewind points), not just branch info.
// The sessionID filter, if provided, limits results to commits from that session.
// worktreeID should be empty for main worktree or the internal git worktree name for linked worktrees.
func (s *GitStore) ListTemporaryCheckpoints(ctx context.Context, baseCommit, worktreeID, sessionID string, limit int) ([]TemporaryCheckpointInfo, error) {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	shadowBranchName := ShadowBranchNameForCommit(baseCommit, worktreeID)
	return s.listCheckpointsForBranch(ctx, shadowBranchName, sessionID, limit)
}

// ListCheckpointsForBranch lists checkpoint commits for a shadow branch by name.
// Use this when you already have the full branch name (e.g., from ListTemporary).
// The sessionID filter, if provided, limits results to commits from that session.
func (s *GitStore) ListCheckpointsForBranch(ctx context.Context, branchName, sessionID string, limit int) ([]TemporaryCheckpointInfo, error) {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	return s.listCheckpointsForBranch(ctx, branchName, sessionID, limit)
}

// listCheckpointsForBranch lists checkpoint commits for a specific shadow branch name.
// This is an internal helper used by ListTemporaryCheckpoints, ListCheckpointsForBranch, and ListAllTemporaryCheckpoints.
func (s *GitStore) listCheckpointsForBranch(ctx context.Context, shadowBranchName, sessionID string, limit int) ([]TemporaryCheckpointInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err //nolint:wrapcheck // Propagating context cancellation
	}

	refName := plumbing.NewBranchReferenceName(shadowBranchName)

	ref, err := s.repo.Reference(refName, true)
	if err != nil {
		return nil, nil //nolint:nilerr // No shadow branch is expected case
	}

	iter, err := s.repo.Log(&git.LogOptions{From: ref.Hash()})
	if err != nil {
		return nil, fmt.Errorf("failed to get commit log: %w", err)
	}

	var results []TemporaryCheckpointInfo
	count := 0

	err = iter.ForEach(func(c *object.Commit) error {
		if err := ctx.Err(); err != nil {
			return err //nolint:wrapcheck // Propagating context cancellation
		}
		if count >= limit*5 { // Scan more to allow for session filtering
			return errStop
		}
		count++

		// Verify commit belongs to target session via Trace-Session trailer
		commitSessionID, hasTrailer := trailers.ParseSession(c.Message)
		if !hasTrailer {
			return nil // Skip commits without session trailer
		}
		if sessionID != "" && commitSessionID != sessionID {
			return nil // Skip commits from other sessions
		}

		// Get first line of message
		message := c.Message
		if idx := strings.Index(message, "\n"); idx > 0 {
			message = message[:idx]
		}

		info := TemporaryCheckpointInfo{
			CommitHash: c.Hash,
			Message:    message,
			SessionID:  commitSessionID,
			Timestamp:  c.Author.When,
		}

		// Check for task checkpoint first
		taskMetadataDir, foundTask := trailers.ParseTaskMetadata(c.Message)
		if foundTask {
			info.IsTaskCheckpoint = true
			info.MetadataDir = taskMetadataDir
			info.ToolUseID = extractToolUseIDFromPath(taskMetadataDir)
		} else {
			metadataDir, found := trailers.ParseMetadata(c.Message)
			if found {
				info.MetadataDir = metadataDir
			}
		}

		results = append(results, info)

		if len(results) >= limit {
			return errStop
		}
		return nil
	})

	if err != nil && !errors.Is(err, errStop) {
		return nil, fmt.Errorf("error iterating commits: %w", err)
	}

	return results, nil
}

// ListAllTemporaryCheckpoints lists checkpoint commits from ALL shadow branches.
// This is used for checkpoint lookup when the base commit is unknown (e.g., HEAD advanced since session start).
// The sessionID filter, if provided, limits results to commits from that session.
func (s *GitStore) ListAllTemporaryCheckpoints(ctx context.Context, sessionID string, limit int) ([]TemporaryCheckpointInfo, error) {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err //nolint:wrapcheck // Propagating context cancellation
	}

	// List all shadow branches
	branches, err := s.listTemporary(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list shadow branches: %w", err)
	}

	var results []TemporaryCheckpointInfo

	// Iterate through each shadow branch and collect checkpoints
	for _, branch := range branches {
		// Use the branch name directly to get checkpoints
		branchCheckpoints, branchErr := s.listCheckpointsForBranch(ctx, branch.BranchName, sessionID, limit)
		if branchErr != nil {
			if errors.Is(branchErr, context.Canceled) || errors.Is(branchErr, context.DeadlineExceeded) {
				return nil, branchErr
			}
			continue // Skip branches we can't read
		}
		results = append(results, branchCheckpoints...)
		if len(results) >= limit {
			results = results[:limit]
			break
		}
	}

	return results, nil
}

// extractToolUseIDFromPath extracts the ToolUseID from a task metadata directory path.
// Task metadata dirs have format: .trace/metadata/<session>/tasks/<toolUseID>
func extractToolUseIDFromPath(metadataDir string) string {
	parts := strings.Split(metadataDir, "/")
	if len(parts) >= 2 && parts[len(parts)-2] == "tasks" {
		return parts[len(parts)-1]
	}
	return ""
}

// errStop is a sentinel error used to break out of git log iteration.
var errStop = errors.New("stop iteration")

// GetTranscriptFromCommit retrieves the transcript from a specific commit's tree.
// This is used for shadow branch checkpoints where the transcript is stored in the commit tree
// rather than on the trace/checkpoints/v1 branch.
// commitHash is the commit to read from, metadataDir is the path within the tree.
// agentType is used for reassembling chunked transcripts in the correct format.
// Handles both chunked and non-chunked transcripts.
func (s *GitStore) GetTranscriptFromCommit(ctx context.Context, commitHash plumbing.Hash, metadataDir string, agentType types.AgentType) ([]byte, error) {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	commit, err := s.repo.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get commit tree: %w", err)
	}

	// Try to get the metadata subtree for chunk detection
	subTree, subTreeErr := tree.Tree(metadataDir)
	if subTreeErr == nil {
		// Use the helper function that handles chunking.
		// Wrap in FetchingTree with nil fetcher (temporary reads are always local).
		ft := &FetchingTree{inner: subTree}
		transcript, err := readTranscriptFromTree(ctx, ft, agentType)
		if err == nil && transcript != nil {
			return transcript, nil
		}
	}

	// Fall back to direct file access (for backwards compatibility)
	transcriptPath := metadataDir + "/" + paths.TranscriptFileName
	if file, fileErr := tree.File(transcriptPath); fileErr == nil {
		content, contentErr := file.Contents()
		if contentErr == nil {
			return []byte(content), nil
		}
	}

	transcriptPath = metadataDir + "/" + paths.TranscriptFileNameLegacy
	if file, fileErr := tree.File(transcriptPath); fileErr == nil {
		content, contentErr := file.Contents()
		if contentErr == nil {
			return []byte(content), nil
		}
	}

	return nil, ErrNoTranscript
}

// ShadowBranchExists checks if a shadow branch exists for the given base commit and worktree.
// worktreeID should be empty for main worktree or the internal git worktree name for linked worktrees.
func (s *GitStore) ShadowBranchExists(baseCommit, worktreeID string) bool {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	shadowBranchName := ShadowBranchNameForCommit(baseCommit, worktreeID)
	refName := plumbing.NewBranchReferenceName(shadowBranchName)
	_, err := s.repo.Reference(refName, true)
	return err == nil
}

// DeleteShadowBranch deletes the shadow branch for the given base commit and worktree.
// worktreeID should be empty for main worktree or the internal git worktree name for linked worktrees.
// Uses git CLI instead of go-git's RemoveReference because go-git v5 doesn't properly
// persist deletions with packed refs or worktrees.
func (s *GitStore) DeleteShadowBranch(ctx context.Context, baseCommit, worktreeID string) error {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	shadowBranchName := ShadowBranchNameForCommit(baseCommit, worktreeID)
	cmd := exec.CommandContext(ctx, "git", "branch", "-D", "--", shadowBranchName) // #nosec G204 -- fixed "git" binary; shadowBranchName is internally derived from a commit hash and worktree ID, not remote input
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to delete shadow branch %s: %s: %w", shadowBranchName, strings.TrimSpace(string(output)), err)
	}
	return nil
}

// ShadowBranchNameForCommit returns the shadow branch name for a base commit hash
// and worktree identifier. The worktree ID should be empty for the main worktree
// or the internal git worktree name for linked worktrees.
// Format: trace/<commit[:7]>-<hash(worktreeID)[:6]>
func ShadowBranchNameForCommit(baseCommit, worktreeID string) string {
	commitPart := baseCommit
	if len(baseCommit) >= ShadowBranchHashLength {
		commitPart = baseCommit[:ShadowBranchHashLength]
	}
	worktreeHash := HashWorktreeID(worktreeID)
	return ShadowBranchPrefix + commitPart + "-" + worktreeHash
}

// ParseShadowBranchName extracts the commit prefix and worktree hash from a shadow branch name.
// Input format: "trace/<commit[:12]>-<worktreeHash[:10]>" (also supports legacy 7/6 char format)
// Returns (commitPrefix, worktreeHash, ok). Returns ("", "", false) if not a valid shadow branch.
func ParseShadowBranchName(branchName string) (commitPrefix, worktreeHash string, ok bool) {
	if !strings.HasPrefix(branchName, ShadowBranchPrefix) {
		return "", "", false
	}
	suffix := strings.TrimPrefix(branchName, ShadowBranchPrefix)

	// Find the last dash - everything before is commit prefix, after is worktree hash
	lastDash := strings.LastIndex(suffix, "-")
	if lastDash == -1 || lastDash == 0 || lastDash == len(suffix)-1 {
		// No dash, or dash at start/end - invalid format
		// Could be old format "trace/<commit[:7]>" without worktree hash
		return suffix, "", true // Return as commit prefix with empty worktree hash
	}

	return suffix[:lastDash], suffix[lastDash+1:], true
}

// getOrCreateShadowBranch gets or creates the shadow branch for checkpoints.
// Returns (parentHash, baseTreeHash, error).
func (s *GitStore) getOrCreateShadowBranch(branchName string) (plumbing.Hash, plumbing.Hash, error) {
	refName := plumbing.NewBranchReferenceName(branchName)
	ref, err := s.repo.Reference(refName, true)

	if err == nil {
		// Branch exists
		commit, err := s.repo.CommitObject(ref.Hash())
		if err != nil {
			return plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("failed to get commit object: %w", err)
		}
		return ref.Hash(), commit.TreeHash, nil
	}

	// Branch doesn't exist, use current HEAD's tree as base
	head, err := s.repo.Head()
	if err != nil {
		return plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("failed to get HEAD: %w", err)
	}

	headCommit, err := s.repo.CommitObject(head.Hash())
	if err != nil {
		return plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("failed to get HEAD commit: %w", err)
	}

	return plumbing.ZeroHash, headCommit.TreeHash, nil
}
