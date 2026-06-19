package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/jsonutil"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/trailers"
	"github.com/GrayCodeAI/trace/cli/vercelconfig"
	"github.com/GrayCodeAI/trace/redact"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// redactCodeLearnings redacts only the Finding field, preserving Path/Line/EndLine.
func redactCodeLearnings(cls []CodeLearning) []CodeLearning {
	if cls == nil {
		return nil
	}
	out := make([]CodeLearning, len(cls))
	for i, cl := range cls {
		out[i] = CodeLearning{
			Path:    cl.Path,
			Line:    cl.Line,
			EndLine: cl.EndLine,
			Finding: redact.String(cl.Finding),
		}
	}
	return out
}

// readMetadataFromBlob reads CommittedMetadata from a blob hash.
func (s *GitStore) readMetadataFromBlob(hash plumbing.Hash) (*CommittedMetadata, error) {
	return readJSONFromBlob[CommittedMetadata](s.repo, hash)
}

// buildCommitMessage constructs the commit message with proper trailers.
// The commit subject is always "Checkpoint: <id>" for consistency.
// If CommitSubject is provided (e.g., for task checkpoints), it's included in the body.
func (s *GitStore) buildCommitMessage(opts WriteCommittedOptions, taskMetadataPath string) string {
	var commitMsg strings.Builder

	// Subject line is always the checkpoint ID for consistent formatting
	fmt.Fprintf(&commitMsg, "Checkpoint: %s\n\n", opts.CheckpointID)

	// Include custom description in body if provided (e.g., task checkpoint details)
	if opts.CommitSubject != "" {
		commitMsg.WriteString(opts.CommitSubject + "\n\n")
	}
	fmt.Fprintf(&commitMsg, "%s: %s\n", trailers.SessionTrailerKey, opts.SessionID)
	fmt.Fprintf(&commitMsg, "%s: %s\n", trailers.StrategyTrailerKey, opts.Strategy)
	if opts.Agent != "" {
		fmt.Fprintf(&commitMsg, "%s: %s\n", trailers.AgentTrailerKey, opts.Agent)
	}
	if opts.EphemeralBranch != "" {
		fmt.Fprintf(&commitMsg, "%s: %s\n", trailers.EphemeralBranchTrailerKey, opts.EphemeralBranch)
	}
	if taskMetadataPath != "" {
		fmt.Fprintf(&commitMsg, "%s: %s\n", trailers.MetadataTaskTrailerKey, taskMetadataPath)
	}

	return commitMsg.String()
}

// incrementalCheckpointData represents an incremental checkpoint during subagent execution.
// This mirrors strategy.SubagentCheckpoint but avoids import cycles.
type incrementalCheckpointData struct {
	Type      string          `json:"type"`
	ToolUseID string          `json:"tool_use_id"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

// taskCheckpointData represents a final task checkpoint.
// This mirrors strategy.TaskCheckpoint but avoids import cycles.
type taskCheckpointData struct {
	SessionID      string `json:"session_id"`
	ToolUseID      string `json:"tool_use_id"`
	CheckpointUUID string `json:"checkpoint_uuid"`
	AgentID        string `json:"agent_id,omitempty"`
}

// ReadCommitted reads a committed checkpoint's summary by ID from the trace/checkpoints/v1 branch.
// Returns only the CheckpointSummary (paths + aggregated stats), not actual content.
// Use ReadSessionContent to read actual transcript/prompts/context.
// Returns nil, nil if the checkpoint doesn't exist.
//
// The storage format uses numbered subdirectories for each session (0-based):
//
//	<checkpoint-id>/
//	├── metadata.json      # CheckpointSummary with sessions map
//	├── 0/                 # First session
//	│   ├── metadata.json  # Session-specific metadata
//	│   └── full.jsonl     # Transcript
//	├── 1/                 # Second session
//	└── ...
func (s *GitStore) ReadCommitted(ctx context.Context, checkpointID id.CheckpointID) (*CheckpointSummary, error) {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	return s.readCommitted(ctx, checkpointID)
}

// readCommitted is the unlocked internal implementation. Callers must hold storerMu.
func (s *GitStore) readCommitted(ctx context.Context, checkpointID id.CheckpointID) (*CheckpointSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err //nolint:wrapcheck // Propagating context cancellation
	}

	ft, err := s.getFetchingTree(ctx)
	if err != nil {
		return nil, nil //nolint:nilnil,nilerr // No sessions branch means no checkpoint exists
	}

	checkpointPath := checkpointID.Path()
	checkpointTree, err := ft.Tree(checkpointPath)
	if err != nil {
		return nil, nil //nolint:nilnil,nilerr // Checkpoint directory not found
	}

	// Read root metadata.json as CheckpointSummary (auto-fetches blob if needed)
	metadataFile, err := checkpointTree.File(paths.MetadataFileName)
	if err != nil {
		return nil, nil //nolint:nilnil,nilerr // metadata.json not found
	}

	content, err := metadataFile.Contents()
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata.json: %w", err)
	}

	var summary CheckpointSummary
	if err := json.Unmarshal([]byte(content), &summary); err != nil {
		return nil, fmt.Errorf("failed to parse metadata.json: %w", err)
	}

	return &summary, nil
}

// ReadSessionMetadata reads only the metadata.json for a specific session within a checkpoint.
// This is a lightweight read that avoids fetching transcript/prompt blobs.
// sessionIndex is 0-based.
func (s *GitStore) ReadSessionMetadata(ctx context.Context, checkpointID id.CheckpointID, sessionIndex int) (*CommittedMetadata, error) {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err //nolint:wrapcheck // Propagating context cancellation
	}

	ft, err := s.getFetchingTree(ctx)
	if err != nil {
		return nil, ErrCheckpointNotFound
	}

	checkpointPath := checkpointID.Path()
	sessionPath := fmt.Sprintf("%s/%d", checkpointPath, sessionIndex)
	sessionTree, err := ft.Tree(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("%w: session %d not found: %w", ErrCheckpointNotFound, sessionIndex, err)
	}

	metadataFile, err := sessionTree.File(paths.MetadataFileName)
	if err != nil {
		return nil, fmt.Errorf("metadata.json not found for session %d: %w", sessionIndex, err)
	}

	content, err := metadataFile.Contents()
	if err != nil {
		return nil, fmt.Errorf("failed to read session metadata: %w", err)
	}

	var metadata CommittedMetadata
	if err := json.Unmarshal([]byte(content), &metadata); err != nil {
		return nil, fmt.Errorf("failed to parse session metadata: %w", err)
	}

	return &metadata, nil
}

// ReadSessionContent reads the actual content for a specific session within a checkpoint.
// sessionIndex is 0-based (0 for first session, 1 for second, etc.).
// Returns the session's metadata, transcript, prompts, and context.
// Returns ErrCheckpointNotFound if the checkpoint or session doesn't exist.
// Returns ErrNoTranscript if the session exists but has no transcript.
func (s *GitStore) ReadSessionContent(ctx context.Context, checkpointID id.CheckpointID, sessionIndex int) (*SessionContent, error) {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	return s.readSessionContent(ctx, checkpointID, sessionIndex)
}

// readSessionContent is the unlocked internal implementation. Callers must hold storerMu.
func (s *GitStore) readSessionContent(ctx context.Context, checkpointID id.CheckpointID, sessionIndex int) (*SessionContent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err //nolint:wrapcheck // Propagating context cancellation
	}

	ft, err := s.getFetchingTree(ctx)
	if err != nil {
		return nil, ErrCheckpointNotFound
	}

	checkpointPath := checkpointID.Path()
	checkpointTree, err := ft.Tree(checkpointPath)
	if err != nil {
		return nil, ErrCheckpointNotFound
	}

	// Get the session subdirectory
	sessionDir := strconv.Itoa(sessionIndex)
	sessionTree, err := checkpointTree.Tree(sessionDir)
	if err != nil {
		return nil, fmt.Errorf("%w: session %d not found: %w", ErrCheckpointNotFound, sessionIndex, err)
	}

	result := &SessionContent{}

	// Read session-specific metadata (auto-fetches blob if needed)
	var agentType types.AgentType
	if metadataFile, fileErr := sessionTree.File(paths.MetadataFileName); fileErr == nil {
		if content, contentErr := metadataFile.Contents(); contentErr == nil {
			if jsonErr := json.Unmarshal([]byte(content), &result.Metadata); jsonErr == nil {
				agentType = result.Metadata.Agent
			}
		}
	}

	// Read transcript (auto-fetches blobs if needed)
	if transcript, transcriptErr := readTranscriptFromTree(ctx, sessionTree, agentType); transcriptErr == nil && transcript != nil {
		result.Transcript = transcript
	}

	// Read prompts (auto-fetches blob if needed)
	if file, fileErr := sessionTree.File(paths.PromptFileName); fileErr == nil {
		if content, contentErr := file.Contents(); contentErr == nil {
			result.Prompts = content
		}
	}

	if len(result.Transcript) == 0 {
		return nil, ErrNoTranscript
	}

	return result, nil
}

// ReadLatestSessionContent is a convenience method that reads the latest session's content.
// This is equivalent to ReadSessionContent(ctx, checkpointID, len(summary.Sessions)-1).
func (s *GitStore) ReadLatestSessionContent(ctx context.Context, checkpointID id.CheckpointID) (*SessionContent, error) {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	return s.readLatestSessionContent(ctx, checkpointID)
}

// readLatestSessionContent is the unlocked internal implementation. Callers must hold storerMu.
func (s *GitStore) readLatestSessionContent(ctx context.Context, checkpointID id.CheckpointID) (*SessionContent, error) {
	summary, err := s.readCommitted(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if summary == nil {
		return nil, ErrCheckpointNotFound
	}
	if len(summary.Sessions) == 0 {
		return nil, fmt.Errorf("checkpoint has no sessions: %s", checkpointID)
	}

	latestIndex := len(summary.Sessions) - 1
	return s.readSessionContent(ctx, checkpointID, latestIndex)
}

// ReadSessionContentByID reads a session's content by its session ID.
// This is useful when you have the session ID but don't know its index within the checkpoint.
// Returns ErrCheckpointNotFound if the checkpoint doesn't exist.
// Returns an error if no session with the given ID exists in the checkpoint.
func (s *GitStore) ReadSessionContentByID(ctx context.Context, checkpointID id.CheckpointID, sessionID string) (*SessionContent, error) {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	summary, err := s.readCommitted(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if summary == nil {
		return nil, ErrCheckpointNotFound
	}

	// Iterate through sessions to find the one with matching session ID
	for i := range len(summary.Sessions) {
		content, readErr := s.readSessionContent(ctx, checkpointID, i)
		if readErr != nil {
			continue
		}
		if content != nil && content.Metadata.SessionID == sessionID {
			return content, nil
		}
	}

	return nil, fmt.Errorf("session %q not found in checkpoint %s", sessionID, checkpointID)
}

// ListCommitted lists all committed checkpoints from the trace/checkpoints/v1 branch.
// Scans sharded paths: <id[:2]>/<id[2:]>/ directories containing metadata.json.
//

func (s *GitStore) ListCommitted(ctx context.Context) ([]CommittedInfo, error) {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err //nolint:wrapcheck // Propagating context cancellation
	}

	tree, err := s.getSessionsBranchTree()
	if err != nil {
		return []CommittedInfo{}, nil //nolint:nilerr // No sessions branch means empty list
	}

	var checkpoints []CommittedInfo

	// Scan sharded structure: <2-char-prefix>/<remaining-id>/metadata.json
	_ = WalkCheckpointShards(s.repo, tree, func(checkpointID id.CheckpointID, cpTreeHash plumbing.Hash) error { //nolint:errcheck // callback never returns errors
		checkpointTree, cpTreeErr := s.repo.TreeObject(cpTreeHash)
		if cpTreeErr != nil {
			return nil //nolint:nilerr // skip unreadable entries, continue walking
		}

		info := CommittedInfo{
			CheckpointID: checkpointID,
		}

		// Get details from root metadata file (CheckpointSummary format)
		if metadataFile, fileErr := checkpointTree.File(paths.MetadataFileName); fileErr == nil {
			if content, contentErr := metadataFile.Contents(); contentErr == nil {
				var summary CheckpointSummary
				if err := json.Unmarshal([]byte(content), &summary); err == nil {
					info.CheckpointsCount = summary.CheckpointsCount
					info.FilesTouched = summary.FilesTouched
					info.SessionCount = len(summary.Sessions)

					// Read session metadata from latest session to get Agent, SessionID, CreatedAt
					if len(summary.Sessions) > 0 {
						latestIndex := len(summary.Sessions) - 1
						latestDir := strconv.Itoa(latestIndex)
						if sessionTree, treeErr := checkpointTree.Tree(latestDir); treeErr == nil {
							if sessionMetadataFile, smErr := sessionTree.File(paths.MetadataFileName); smErr == nil {
								if sessionContent, scErr := sessionMetadataFile.Contents(); scErr == nil {
									var sessionMetadata CommittedMetadata
									if json.Unmarshal([]byte(sessionContent), &sessionMetadata) == nil {
										info.Agent = sessionMetadata.Agent
										info.SessionID = sessionMetadata.SessionID
										info.CreatedAt = sessionMetadata.CreatedAt
									}
								}
							}
						}
					}
				}
			}
		}

		checkpoints = append(checkpoints, info)
		return nil
	})

	// Sort by time (most recent first)
	sort.Slice(checkpoints, func(i, j int) bool {
		return checkpoints[i].CreatedAt.After(checkpoints[j].CreatedAt)
	})

	return checkpoints, nil
}

// GetTranscript retrieves the transcript for a specific checkpoint ID.
// Returns the latest session's transcript.
func (s *GitStore) GetTranscript(ctx context.Context, checkpointID id.CheckpointID) ([]byte, error) {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	content, err := s.readLatestSessionContent(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if len(content.Transcript) == 0 {
		return nil, fmt.Errorf("no transcript found for checkpoint: %s", checkpointID)
	}
	return content.Transcript, nil
}

// GetSessionLog retrieves the session transcript and session ID for a checkpoint.
// This is the primary method for looking up session logs by checkpoint ID.
// Returns ErrCheckpointNotFound if the checkpoint doesn't exist.
// Returns ErrNoTranscript if the checkpoint exists but has no transcript.
func (s *GitStore) GetSessionLog(ctx context.Context, cpID id.CheckpointID) ([]byte, string, error) {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	content, err := s.readLatestSessionContent(ctx, cpID)
	if err != nil {
		return nil, "", err
	}
	return content.Transcript, content.Metadata.SessionID, nil
}

// LookupSessionLog is a convenience function that opens the repository and retrieves
// a session log by checkpoint ID. This is the primary entry point for callers that
// don't already have a GitStore instance.
// Returns ErrCheckpointNotFound if the checkpoint doesn't exist.
// Returns ErrNoTranscript if the checkpoint exists but has no transcript.
func LookupSessionLog(ctx context.Context, cpID id.CheckpointID) ([]byte, string, error) {
	repo, err := git.PlainOpenWithOptions(".", &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return nil, "", fmt.Errorf("failed to open git repository: %w", err)
	}
	store := NewGitStore(repo)
	return store.GetSessionLog(ctx, cpID)
}

// UpdateSummary updates the summary field in the latest session's metadata.
// Returns ErrCheckpointNotFound if the checkpoint doesn't exist.
func (s *GitStore) UpdateSummary(ctx context.Context, checkpointID id.CheckpointID, summary *Summary) error {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	if err := ctx.Err(); err != nil {
		return err //nolint:wrapcheck // Propagating context cancellation
	}

	// Ensure sessions branch exists
	if err := s.ensureSessionsBranch(ctx); err != nil {
		return fmt.Errorf("failed to ensure sessions branch: %w", err)
	}

	// Get branch ref and root tree hash (O(1), no flatten)
	parentHash, rootTreeHash, err := s.getSessionsBranchRef()
	if err != nil {
		return err
	}

	// Flatten only the checkpoint subtree
	basePath := checkpointID.Path() + "/"
	checkpointPath := checkpointID.Path()
	entries, err := s.flattenCheckpointEntries(rootTreeHash, checkpointPath)
	if err != nil {
		return err
	}

	// Read root CheckpointSummary to find the latest session
	rootMetadataPath := basePath + paths.MetadataFileName
	entry, exists := entries[rootMetadataPath]
	if !exists {
		return ErrCheckpointNotFound
	}

	checkpointSummary, err := s.readSummaryFromBlob(entry.Hash)
	if err != nil {
		return fmt.Errorf("failed to read checkpoint summary: %w", err)
	}

	// Find the latest session's metadata path (0-based indexing)
	latestIndex := len(checkpointSummary.Sessions) - 1
	sessionMetadataPath := fmt.Sprintf("%s%d/%s", basePath, latestIndex, paths.MetadataFileName)
	sessionEntry, exists := entries[sessionMetadataPath]
	if !exists {
		return fmt.Errorf("session metadata not found at %s", sessionMetadataPath)
	}

	// Read and update session metadata
	existingMetadata, err := s.readMetadataFromBlob(sessionEntry.Hash)
	if err != nil {
		return fmt.Errorf("failed to read session metadata: %w", err)
	}

	// Update the summary
	existingMetadata.Summary = redactSummary(summary)

	// Write updated session metadata
	metadataJSON, err := jsonutil.MarshalIndentWithNewline(existingMetadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}
	metadataHash, err := CreateBlobFromContent(s.repo, metadataJSON)
	if err != nil {
		return fmt.Errorf("failed to create metadata blob: %w", err)
	}
	entries[sessionMetadataPath] = object.TreeEntry{
		Name: sessionMetadataPath,
		Mode: filemode.Regular,
		Hash: metadataHash,
	}

	// Build checkpoint subtree and splice into root (O(depth) tree surgery)
	newTreeHash, err := s.spliceCheckpointSubtree(ctx, rootTreeHash, checkpointID, basePath, entries)
	if err != nil {
		return err
	}

	authorName, authorEmail := GetGitAuthorFromRepo(s.repo)
	commitMsg := fmt.Sprintf("Update summary for checkpoint %s (session: %s)", checkpointID, existingMetadata.SessionID)
	newCommitHash, err := s.createCommit(ctx, newTreeHash, parentHash, commitMsg, authorName, authorEmail)
	if err != nil {
		return err
	}

	refName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	newRef := plumbing.NewHashReference(refName, newCommitHash)
	if err := s.repo.Storer.SetReference(newRef); err != nil {
		return fmt.Errorf("failed to set branch reference: %w", err)
	}

	return nil
}

// UpdateCommitted replaces the transcript, prompts, and context for an existing
// committed checkpoint. Uses replace semantics: the full session transcript is
// written, replacing whatever was stored at initial condensation time.
//
// This is called at stop time to finalize all checkpoints from the current turn
// with the complete session transcript (from prompt to stop event).
//
// Returns ErrCheckpointNotFound if the checkpoint doesn't exist.
func (s *GitStore) UpdateCommitted(ctx context.Context, opts UpdateCommittedOptions) error {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	if opts.CheckpointID.IsEmpty() {
		return errors.New("invalid update options: checkpoint ID is required")
	}

	// Ensure sessions branch exists
	if err := s.ensureSessionsBranch(ctx); err != nil {
		return fmt.Errorf("failed to ensure sessions branch: %w", err)
	}

	// Get branch ref and root tree hash (O(1), no flatten)
	parentHash, rootTreeHash, err := s.getSessionsBranchRef()
	if err != nil {
		return err
	}

	// Flatten only the checkpoint subtree
	basePath := opts.CheckpointID.Path() + "/"
	checkpointPath := opts.CheckpointID.Path()
	entries, err := s.flattenCheckpointEntries(rootTreeHash, checkpointPath)
	if err != nil {
		return err
	}

	// Read root CheckpointSummary to find the session slot
	rootMetadataPath := basePath + paths.MetadataFileName
	entry, exists := entries[rootMetadataPath]
	if !exists {
		return ErrCheckpointNotFound
	}

	checkpointSummary, err := s.readSummaryFromBlob(entry.Hash)
	if err != nil {
		return fmt.Errorf("failed to read checkpoint summary: %w", err)
	}
	if len(checkpointSummary.Sessions) == 0 {
		return ErrCheckpointNotFound
	}

	// Find session index matching opts.SessionID
	sessionIndex := -1
	for i := range len(checkpointSummary.Sessions) {
		metaPath := fmt.Sprintf("%s%d/%s", basePath, i, paths.MetadataFileName)
		if metaEntry, metaExists := entries[metaPath]; metaExists {
			meta, metaErr := s.readMetadataFromBlob(metaEntry.Hash)
			if metaErr == nil && meta.SessionID == opts.SessionID {
				sessionIndex = i
				break
			}
		}
	}
	if sessionIndex == -1 {
		// Fall back to latest session; log so mismatches are diagnosable.
		sessionIndex = len(checkpointSummary.Sessions) - 1
		logging.Debug(
			ctx, "UpdateCommitted: session ID not found, falling back to latest",
			slog.String("session_id", opts.SessionID),
			slog.String("checkpoint_id", string(opts.CheckpointID)),
			slog.Int("fallback_index", sessionIndex),
		)
	}

	sessionPath := fmt.Sprintf("%s%d/", basePath, sessionIndex)

	// Replace transcript (full replace, not append).
	// Transcript is pre-redacted by the caller (enforced by RedactedBytes type).
	if opts.Transcript.Len() > 0 {
		if err := s.replaceTranscript(ctx, opts.Transcript, opts.Agent, opts.PrecomputedBlobs, sessionPath, entries); err != nil {
			return fmt.Errorf("failed to replace transcript: %w", err)
		}
	}

	// Replace prompts (apply redaction as safety net)
	if len(opts.Prompts) > 0 {
		promptContent := redact.String(JoinPrompts(opts.Prompts))
		blobHash, err := CreateBlobFromContent(s.repo, []byte(promptContent))
		if err != nil {
			return fmt.Errorf("failed to create prompt blob: %w", err)
		}
		entries[sessionPath+paths.PromptFileName] = object.TreeEntry{
			Name: sessionPath + paths.PromptFileName,
			Mode: filemode.Regular,
			Hash: blobHash,
		}
	}

	// Build checkpoint subtree and splice into root (O(depth) tree surgery)
	newTreeHash, err := s.spliceCheckpointSubtree(ctx, rootTreeHash, opts.CheckpointID, basePath, entries)
	if err != nil {
		return err
	}
	newTreeHash, err = s.maybeMergeVercelConfig(ctx, newTreeHash)
	if err != nil {
		return err
	}

	authorName, authorEmail := GetGitAuthorFromRepo(s.repo)
	commitMsg := fmt.Sprintf("Finalize transcript for Checkpoint: %s", opts.CheckpointID)
	newCommitHash, err := s.createCommit(ctx, newTreeHash, parentHash, commitMsg, authorName, authorEmail)
	if err != nil {
		return err
	}

	refName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	newRef := plumbing.NewHashReference(refName, newCommitHash)
	if err := s.repo.Storer.SetReference(newRef); err != nil {
		return fmt.Errorf("failed to set branch reference: %w", err)
	}

	return nil
}

// replaceTranscript writes the full transcript content, replacing any existing transcript.
// Also removes any chunk files from a previous write and updates the content hash.
//
// Short-circuits when the existing content_hash.txt already matches the new
// transcript's sha256 — in that case the chunk entries are preserved as-is and
// no chunking/zlib happens. Use precomputed (non-nil) to reuse blob hashes
// computed once across multiple checkpoints.
func (s *GitStore) replaceTranscript(ctx context.Context, transcript redact.RedactedBytes, agentType types.AgentType, precomputed *PrecomputedTranscriptBlobs, sessionPath string, entries map[string]object.TreeEntry) error {
	// Ignore precompute if invariants are violated — fall back to fresh chunking.
	if precomputed != nil && !precomputed.isUsable() {
		precomputed = nil
	}

	// Compute the new content-hash string (cheap — SHA-256 over transcript bytes).
	var newContentHash string
	if precomputed != nil {
		newContentHash = precomputed.ContentHash
	} else {
		newContentHash = fmt.Sprintf("sha256:%x", sha256.Sum256(transcript.Bytes()))
	}

	// Short-circuit: if the existing content_hash.txt already matches, the
	// chunk entries currently in `entries` represent the same content. Leave
	// everything as-is and skip chunking + zlib.
	hashPath := sessionPath + paths.ContentHashFileName
	if existing, ok := entries[hashPath]; ok {
		if blob, err := s.repo.BlobObject(existing.Hash); err == nil {
			if rdr, rerr := blob.Reader(); rerr == nil {
				existingHash, readErr := io.ReadAll(rdr)
				_ = rdr.Close()
				if readErr == nil && string(existingHash) == newContentHash {
					return nil
				}
			}
		}
	}

	// Remove existing transcript files (base + any chunks)
	transcriptBase := sessionPath + paths.TranscriptFileName
	for key := range entries {
		if key == transcriptBase || strings.HasPrefix(key, transcriptBase+".") {
			delete(entries, key)
		}
	}

	// Resolve chunk hashes from precompute, or chunk + blob-write now.
	var chunkHashes []plumbing.Hash
	if precomputed != nil {
		chunkHashes = precomputed.ChunkHashes
	} else {
		chunks, err := chunkTranscript(ctx, transcript.Bytes(), agentType)
		if err != nil {
			return fmt.Errorf("failed to chunk transcript: %w", err)
		}
		chunkHashes = make([]plumbing.Hash, len(chunks))
		for i, chunk := range chunks {
			blobHash, err := CreateBlobFromContent(s.repo, chunk)
			if err != nil {
				return fmt.Errorf("failed to create transcript blob: %w", err)
			}
			chunkHashes[i] = blobHash
		}
	}

	// Record chunk files in the tree at v1 (full.jsonl) naming.
	for i, blobHash := range chunkHashes {
		chunkPath := sessionPath + agent.ChunkFileName(paths.TranscriptFileName, i)
		entries[chunkPath] = object.TreeEntry{
			Name: chunkPath,
			Mode: filemode.Regular,
			Hash: blobHash,
		}
	}

	// Content-hash blob.
	var hashBlob plumbing.Hash
	if precomputed != nil {
		hashBlob = precomputed.ContentHashBlob
	} else {
		h, err := CreateBlobFromContent(s.repo, []byte(newContentHash))
		if err != nil {
			return fmt.Errorf("failed to create content hash blob: %w", err)
		}
		hashBlob = h
	}
	entries[hashPath] = object.TreeEntry{
		Name: hashPath,
		Mode: filemode.Regular,
		Hash: hashBlob,
	}

	return nil
}

// PrecomputeTranscriptBlobs chunks the given transcript and writes each chunk
// plus the content-hash blob to the object store once, returning the resulting
// hashes for reuse across multiple UpdateCommitted calls that share the same
// transcript content.
//
// The returned blobs work for both v1 (full.jsonl) and v2 (raw_transcript)
// paths since blob hashes are content-addressed (SHA-1 of chunk bytes). Only
// the tree-entry filenames differ between v1 and v2.
func PrecomputeTranscriptBlobs(ctx context.Context, repo *git.Repository, transcript redact.RedactedBytes, agentType types.AgentType) (*PrecomputedTranscriptBlobs, error) {
	raw := transcript.Bytes()

	chunks, err := chunkTranscript(ctx, raw, agentType)
	if err != nil {
		return nil, fmt.Errorf("failed to chunk transcript: %w", err)
	}

	chunkHashes := make([]plumbing.Hash, len(chunks))
	for i, chunk := range chunks {
		h, err := CreateBlobFromContent(repo, chunk)
		if err != nil {
			return nil, fmt.Errorf("failed to create transcript blob: %w", err)
		}
		chunkHashes[i] = h
	}

	contentHash := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	hashBlob, err := CreateBlobFromContent(repo, []byte(contentHash))
	if err != nil {
		return nil, fmt.Errorf("failed to create content hash blob: %w", err)
	}

	return &PrecomputedTranscriptBlobs{
		ChunkHashes:     chunkHashes,
		ContentHashBlob: hashBlob,
		ContentHash:     contentHash,
	}, nil
}

// ensureSessionsBranch ensures the trace/checkpoints/v1 branch exists.
func (s *GitStore) ensureSessionsBranch(ctx context.Context) error {
	refName := plumbing.NewBranchReferenceName(paths.MetadataBranchName)
	_, err := s.repo.Reference(refName, true)
	if err == nil {
		return nil // Branch exists
	}

	// Create orphan branch with empty tree
	emptyTreeHash, err := BuildTreeFromEntries(ctx, s.repo, make(map[string]object.TreeEntry))
	if err != nil {
		return err
	}
	emptyTreeHash, err = s.maybeMergeVercelConfig(ctx, emptyTreeHash)
	if err != nil {
		return err
	}

	authorName, authorEmail := GetGitAuthorFromRepo(s.repo)
	commitHash, err := s.createCommit(ctx, emptyTreeHash, plumbing.ZeroHash, "Initialize sessions branch", authorName, authorEmail)
	if err != nil {
		return err
	}

	newRef := plumbing.NewHashReference(refName, commitHash)
	if err := s.repo.Storer.SetReference(newRef); err != nil {
		return fmt.Errorf("failed to set branch reference: %w", err)
	}
	return nil
}

func (s *GitStore) maybeMergeVercelConfig(ctx context.Context, rootTreeHash plumbing.Hash) (plumbing.Hash, error) {
	if err := vercelconfig.InitSettings(ctx); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("initialize vercel settings: %w", err)
	}
	mergedTreeHash, err := vercelconfig.MaybeMergeMetadataBranchConfig(s.repo, rootTreeHash)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("merge vercel metadata branch config: %w", err)
	}
	return mergedTreeHash, nil
}
