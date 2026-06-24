package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/codex"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/jsonutil"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/validation"
	"github.com/GrayCodeAI/trace/cli/versioninfo"
	"github.com/GrayCodeAI/trace/perf"
	"github.com/GrayCodeAI/trace/redact"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// errStopIteration is used to stop commit iteration early in GetCheckpointAuthor.
var errStopIteration = errors.New("stop iteration")

// chunkTranscript is an indirection over agent.ChunkTranscript so tests can
// count or intercept chunking calls (e.g., to verify the short-circuit avoids
// re-chunking identical content). Production code paths always use the
// unwrapped function.
var chunkTranscript = agent.ChunkTranscript

// WriteCommitted writes a committed checkpoint to the trace/checkpoints/v1 branch.
// Checkpoints are stored at sharded paths: <id[:2]>/<id[2:]>/
//
// For task checkpoints (IsTask=true), additional files are written under tasks/<tool-use-id>/:
//   - For incremental checkpoints: checkpoints/NNN-<tool-use-id>.json
//   - For final checkpoints: checkpoint.json and agent-<agent-id>.jsonl
func (s *GitStore) WriteCommitted(ctx context.Context, opts WriteCommittedOptions) error {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	// Validate identifiers to prevent path traversal and malformed data
	if opts.CheckpointID.IsEmpty() {
		return errors.New("invalid checkpoint options: checkpoint ID is required")
	}
	if err := validation.ValidateSessionID(opts.SessionID); err != nil {
		return fmt.Errorf("invalid checkpoint options: %w", err)
	}
	if err := validation.ValidateToolUseID(opts.ToolUseID); err != nil {
		return fmt.Errorf("invalid checkpoint options: %w", err)
	}
	if err := validation.ValidateAgentID(opts.AgentID); err != nil {
		return fmt.Errorf("invalid checkpoint options: %w", err)
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

	// Use sharded path: <id[:2]>/<id[2:]>/
	basePath := opts.CheckpointID.Path() + "/"
	checkpointPath := opts.CheckpointID.Path()

	// Flatten only the checkpoint subtree (O(files in checkpoint))
	entries, err := s.flattenCheckpointEntries(rootTreeHash, checkpointPath)
	if err != nil {
		return err
	}

	// Track task metadata path for commit trailer
	var taskMetadataPath string

	// Handle task checkpoints
	if opts.IsTask && opts.ToolUseID != "" {
		taskMetadataPath, err = s.writeTaskCheckpointEntries(ctx, opts, basePath, entries)
		if err != nil {
			return err
		}
	}

	// Write standard checkpoint entries (transcript, prompts, context, metadata)
	if err := s.writeStandardCheckpointEntries(ctx, opts, basePath, entries); err != nil {
		return err
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

	commitMsg := s.buildCommitMessage(opts, taskMetadataPath)
	newCommitHash, err := s.createCommit(ctx, newTreeHash, parentHash, commitMsg, opts.AuthorName, opts.AuthorEmail)
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

// flattenCheckpointEntries reads only the entries under a specific checkpoint path
// from the sessions branch tree. This is O(files in checkpoint) instead of O(all checkpoints).
// Returns an empty map if the checkpoint doesn't exist yet.
func (s *GitStore) flattenCheckpointEntries(rootTreeHash plumbing.Hash, checkpointPath string) (map[string]object.TreeEntry, error) {
	entries := make(map[string]object.TreeEntry)
	if rootTreeHash == plumbing.ZeroHash {
		return entries, nil
	}

	rootTree, err := s.repo.TreeObject(rootTreeHash)
	if err != nil {
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			return entries, nil // Tree doesn't exist yet
		}
		return nil, fmt.Errorf("failed to read root tree %s: %w", rootTreeHash, err)
	}

	subtree, err := rootTree.Tree(checkpointPath)
	if err != nil {
		return entries, nil //nolint:nilerr // Checkpoint doesn't exist yet
	}

	// Flatten just this subtree with the full path prefix
	if err := FlattenTree(s.repo, subtree, checkpointPath, entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// spliceCheckpointSubtree builds a tree from checkpoint-local entries and installs it
// at the correct shard location in the root tree using O(depth) tree surgery.
// basePath is like "a3/b2c4d5e6f7/" (with trailing slash).
// Returns the new root tree hash.
func (s *GitStore) spliceCheckpointSubtree(ctx context.Context, rootTreeHash plumbing.Hash, checkpointID id.CheckpointID, basePath string, entries map[string]object.TreeEntry) (plumbing.Hash, error) {
	// Convert entries to relative paths (strip basePath prefix)
	relEntries := make(map[string]object.TreeEntry, len(entries))
	for path, entry := range entries {
		relPath := strings.TrimPrefix(path, basePath)
		if relPath == path {
			continue // Entry doesn't have the expected prefix
		}
		relEntries[relPath] = entry
	}

	// Build the checkpoint subtree from relative entries
	checkpointTreeHash, err := BuildTreeFromEntries(ctx, s.repo, relEntries)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("failed to build checkpoint subtree: %w", err)
	}

	// Splice into root tree at the shard path using tree surgery
	// Path: ["a3"] with entry "b2c4d5e6f7" pointing to the checkpoint tree
	shardPrefix := string(checkpointID[:2])
	shardSuffix := string(checkpointID[2:])
	return UpdateSubtree(s.repo, rootTreeHash, []string{shardPrefix}, []object.TreeEntry{
		{Name: shardSuffix, Mode: filemode.Dir, Hash: checkpointTreeHash},
	}, UpdateSubtreeOptions{MergeMode: MergeKeepExisting})
}

// writeTaskCheckpointEntries writes task-specific checkpoint entries and returns the task metadata path.
func (s *GitStore) writeTaskCheckpointEntries(ctx context.Context, opts WriteCommittedOptions, basePath string, entries map[string]object.TreeEntry) (string, error) {
	taskPath := basePath + "tasks/" + opts.ToolUseID + "/"

	if opts.IsIncremental {
		return s.writeIncrementalTaskCheckpoint(opts, taskPath, entries)
	}
	return s.writeFinalTaskCheckpoint(ctx, opts, taskPath, entries)
}

// writeIncrementalTaskCheckpoint writes an incremental checkpoint file during task execution.
func (s *GitStore) writeIncrementalTaskCheckpoint(opts WriteCommittedOptions, taskPath string, entries map[string]object.TreeEntry) (string, error) {
	incData, err := redact.JSONLBytes(opts.IncrementalData)
	if err != nil {
		return "", fmt.Errorf("failed to redact incremental checkpoint: %w", err)
	}
	checkpoint := incrementalCheckpointData{
		Type:      opts.IncrementalType,
		ToolUseID: opts.ToolUseID,
		Timestamp: time.Now().UTC(),
		Data:      json.RawMessage(incData.Bytes()),
	}
	cpData, err := jsonutil.MarshalIndentWithNewline(checkpoint, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal incremental checkpoint: %w", err)
	}
	cpBlobHash, err := CreateBlobFromContent(s.repo, cpData)
	if err != nil {
		return "", fmt.Errorf("failed to create incremental checkpoint blob: %w", err)
	}

	cpFilename := fmt.Sprintf("%03d-%s.json", opts.IncrementalSequence, opts.ToolUseID)
	cpPath := taskPath + "checkpoints/" + cpFilename
	entries[cpPath] = object.TreeEntry{
		Name: cpPath,
		Mode: filemode.Regular,
		Hash: cpBlobHash,
	}
	return cpPath, nil
}

// writeFinalTaskCheckpoint writes the final checkpoint.json and subagent transcript.
func (s *GitStore) writeFinalTaskCheckpoint(ctx context.Context, opts WriteCommittedOptions, taskPath string, entries map[string]object.TreeEntry) (string, error) {
	checkpoint := taskCheckpointData{
		SessionID:      opts.SessionID,
		ToolUseID:      opts.ToolUseID,
		CheckpointUUID: opts.CheckpointUUID,
		AgentID:        opts.AgentID,
	}
	checkpointData, err := jsonutil.MarshalIndentWithNewline(checkpoint, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal task checkpoint: %w", err)
	}
	blobHash, err := CreateBlobFromContent(s.repo, checkpointData)
	if err != nil {
		return "", fmt.Errorf("failed to create task checkpoint blob: %w", err)
	}

	checkpointFile := taskPath + "checkpoint.json"
	entries[checkpointFile] = object.TreeEntry{
		Name: checkpointFile,
		Mode: filemode.Regular,
		Hash: blobHash,
	}

	// Write subagent transcript if available
	if opts.SubagentTranscriptPath != "" && opts.AgentID != "" {
		agentContent, readErr := os.ReadFile(opts.SubagentTranscriptPath)
		if readErr == nil {
			// Try JSONL-aware redaction first; fall back to plain string redaction
			// if the content is not valid JSONL (avoids silently dropping the transcript).
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

			agentBlobHash, agentBlobErr := CreateBlobFromContent(s.repo, agentContent)
			if agentBlobErr == nil {
				agentPath := taskPath + "agent-" + opts.AgentID + ".jsonl"
				entries[agentPath] = object.TreeEntry{
					Name: agentPath,
					Mode: filemode.Regular,
					Hash: agentBlobHash,
				}
			}
		}
	}

	// Return task path without trailing slash
	return taskPath[:len(taskPath)-1], nil
}

// writeStandardCheckpointEntries writes session files to numbered subdirectories and
// maintains a CheckpointSummary at the root level with aggregated statistics.
//
// Structure:
//
//	basePath/
//	├── metadata.json         # CheckpointSummary (aggregated stats)
//	├── 1/                    # First session
//	│   ├── metadata.json     # CommittedMetadata (session-specific, includes initial_attribution)
//	│   ├── full.jsonl
//	│   ├── prompt.txt
//	│   └── content_hash.txt
//	├── 2/                    # Second session
//	└── ...
func (s *GitStore) writeStandardCheckpointEntries(ctx context.Context, opts WriteCommittedOptions, basePath string, entries map[string]object.TreeEntry) error {
	// Read existing summary to get current session count
	var existingSummary *CheckpointSummary
	metadataPath := basePath + paths.MetadataFileName
	if entry, exists := entries[metadataPath]; exists {
		existing, err := s.readSummaryFromBlob(entry.Hash)
		if err == nil {
			existingSummary = existing
		}
	}

	// Determine session index: reuse existing slot if session ID matches, otherwise append
	sessionIndex := s.findSessionIndex(ctx, basePath, existingSummary, entries, opts.SessionID)

	// Refuse if slot 0 already holds metadata for a DIFFERENT session ID.
	// findSessionIndex only returns 0 when existingSummary is nil (fresh write)
	// or when the summary claims slot 0 belongs to us — either way, the tree
	// actually holding session-0 metadata for someone else is a corruption /
	// stale-summary shape. Writing through it would overwrite data we don't
	// know about. Bail instead of silently clobbering.
	//
	// We read and capture BEFORE writeSessionToSubdirectory clears the subtree,
	// otherwise we'd only ever see our own write.
	if sessionIndex == 0 {
		if entry, exists := entries[fmt.Sprintf("%s0/%s", basePath, paths.MetadataFileName)]; exists {
			if existingMeta, readErr := s.readMetadataFromBlob(entry.Hash); readErr == nil && existingMeta.SessionID != opts.SessionID {
				logging.Error(ctx, "refusing checkpoint write: session 0 holds a different sessionID",
					slog.String("checkpoint_id", opts.CheckpointID.String()),
					slog.String("existing_session_id", existingMeta.SessionID),
					slog.String("write_session_id", opts.SessionID),
					slog.Bool("existing_summary_nil", existingSummary == nil))
				return fmt.Errorf(
					"refusing to overwrite session 0 of checkpoint %s: existing session ID %q differs from write session ID %q. The checkpoint tree is inconsistent (session 0 belongs to a different session than this write claims). No automated repair exists for this shape — please report it along with the output of `git ls-tree trace/checkpoints/v1 %s/`",
					opts.CheckpointID, existingMeta.SessionID, opts.SessionID, opts.CheckpointID.Path(),
				)
			}
		}
	}

	// Write session files to numbered subdirectory
	sessionPath := fmt.Sprintf("%s%d/", basePath, sessionIndex)
	sessionFilePaths, err := s.writeSessionToSubdirectory(ctx, opts, sessionPath, entries)
	if err != nil {
		return err
	}

	// Copy additional metadata files from directory if specified (to session subdirectory)
	if opts.MetadataDir != "" {
		if err := s.copyMetadataDir(opts.MetadataDir, sessionPath, entries); err != nil {
			return fmt.Errorf("failed to copy metadata directory: %w", err)
		}
	}

	// Build the sessions array
	var sessions []SessionFilePaths
	if existingSummary != nil {
		sessions = make([]SessionFilePaths, max(len(existingSummary.Sessions), sessionIndex+1))
		copy(sessions, existingSummary.Sessions)
	} else {
		sessions = make([]SessionFilePaths, 1)
	}
	sessions[sessionIndex] = sessionFilePaths

	// Update root metadata.json with CheckpointSummary
	return s.writeCheckpointSummary(opts, basePath, entries, sessions)
}

// writeSessionToSubdirectory writes a single session's files to a numbered subdirectory.
// Returns the absolute file paths from the git tree root for the sessions map.
func (s *GitStore) writeSessionToSubdirectory(ctx context.Context, opts WriteCommittedOptions, sessionPath string, entries map[string]object.TreeEntry) (SessionFilePaths, error) {
	filePaths := SessionFilePaths{}

	// Clear any existing entries at this path so stale files from a previous
	// write (e.g. prompt.txt) don't persist on overwrite.
	for key := range entries {
		if strings.HasPrefix(key, sessionPath) {
			delete(entries, key)
		}
	}

	// Write transcript
	wroteTranscript, err := s.writeTranscript(ctx, opts, sessionPath, entries)
	if err != nil {
		return filePaths, err
	}
	if wroteTranscript {
		filePaths.Transcript = "/" + sessionPath + paths.TranscriptFileName
		filePaths.ContentHash = "/" + sessionPath + paths.ContentHashFileName
	}

	// Write prompts
	if len(opts.Prompts) > 0 {
		promptContent := redact.String(JoinPrompts(opts.Prompts))
		blobHash, err := CreateBlobFromContent(s.repo, []byte(promptContent))
		if err != nil {
			return filePaths, err
		}
		entries[sessionPath+paths.PromptFileName] = object.TreeEntry{
			Name: sessionPath + paths.PromptFileName,
			Mode: filemode.Regular,
			Hash: blobHash,
		}
		filePaths.Prompt = "/" + sessionPath + paths.PromptFileName
	}

	// Write session-level metadata.json (CommittedMetadata with all fields including initial_attribution)
	sessionMetadata := CommittedMetadata{
		CheckpointID:                opts.CheckpointID,
		SessionID:                   opts.SessionID,
		Strategy:                    opts.Strategy,
		CreatedAt:                   checkpointCreatedAt(opts),
		Branch:                      opts.Branch,
		CheckpointsCount:            opts.CheckpointsCount,
		FilesTouched:                opts.FilesTouched,
		Agent:                       opts.Agent,
		Model:                       opts.Model,
		TurnID:                      opts.TurnID,
		Kind:                        opts.Kind,
		ReviewSkills:                opts.ReviewSkills,
		ReviewPrompt:                opts.ReviewPrompt,
		InvestigateRunID:            opts.InvestigateRunID,
		InvestigateTopic:            opts.InvestigateTopic,
		IsTask:                      opts.IsTask,
		ToolUseID:                   opts.ToolUseID,
		TranscriptIdentifierAtStart: opts.TranscriptIdentifierAtStart,
		CheckpointTranscriptStart:   opts.CheckpointTranscriptStart,
		TranscriptLinesAtStart:      opts.CheckpointTranscriptStart, // Deprecated: kept for backward compat
		TokenUsage:                  opts.TokenUsage,
		SessionMetrics:              opts.SessionMetrics,
		InitialAttribution:          opts.InitialAttribution,
		PromptAttributions:          opts.PromptAttributionsJSON,
		Summary:                     redactSummary(opts.Summary),
		CLIVersion:                  versioninfo.Version,
	}

	metadataJSON, err := jsonutil.MarshalIndentWithNewline(sessionMetadata, "", "  ")
	if err != nil {
		return filePaths, fmt.Errorf("failed to marshal session metadata: %w", err)
	}
	metadataHash, err := CreateBlobFromContent(s.repo, metadataJSON)
	if err != nil {
		return filePaths, err
	}
	entries[sessionPath+paths.MetadataFileName] = object.TreeEntry{
		Name: sessionPath + paths.MetadataFileName,
		Mode: filemode.Regular,
		Hash: metadataHash,
	}
	filePaths.Metadata = "/" + sessionPath + paths.MetadataFileName

	return filePaths, nil
}

// writeCheckpointSummary writes the root-level CheckpointSummary with aggregated statistics.
// sessions is the complete sessions array (already built by the caller).
func (s *GitStore) writeCheckpointSummary(opts WriteCommittedOptions, basePath string, entries map[string]object.TreeEntry, sessions []SessionFilePaths) error {
	checkpointsCount, filesTouched, tokenUsage, err := s.reaggregateFromEntries(basePath, len(sessions), entries)
	if err != nil {
		return fmt.Errorf("failed to aggregate session stats: %w", err)
	}

	combinedAttribution := opts.CombinedAttribution
	if combinedAttribution == nil {
		rootMetadataPath := basePath + paths.MetadataFileName
		if entry, exists := entries[rootMetadataPath]; exists {
			existingSummary, readErr := s.readSummaryFromBlob(entry.Hash)
			if readErr == nil {
				combinedAttribution = existingSummary.CombinedAttribution
			}
		}
	}

	summary := CheckpointSummary{
		CheckpointID:        opts.CheckpointID,
		CLIVersion:          versioninfo.Version,
		Strategy:            opts.Strategy,
		Branch:              opts.Branch,
		CheckpointsCount:    checkpointsCount,
		FilesTouched:        filesTouched,
		Sessions:            sessions,
		TokenUsage:          tokenUsage,
		CombinedAttribution: combinedAttribution,
		HasReview:           opts.Kind == "agent_review",
		HasInvestigation:    opts.Kind == "agent_investigate",
	}

	metadataJSON, err := jsonutil.MarshalIndentWithNewline(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal checkpoint summary: %w", err)
	}
	metadataHash, err := CreateBlobFromContent(s.repo, metadataJSON)
	if err != nil {
		return err
	}
	entries[basePath+paths.MetadataFileName] = object.TreeEntry{
		Name: basePath + paths.MetadataFileName,
		Mode: filemode.Regular,
		Hash: metadataHash,
	}
	return nil
}

// UpdateCheckpointSummary updates root-level checkpoint metadata fields that depend
// on the full set of sessions already written to the checkpoint.
func (s *GitStore) UpdateCheckpointSummary(ctx context.Context, checkpointID id.CheckpointID, combinedAttribution *InitialAttribution) error {
	StorerMu.Lock()
	defer StorerMu.Unlock()

	if err := ctx.Err(); err != nil {
		return err //nolint:wrapcheck // Propagating context cancellation
	}

	if err := s.ensureSessionsBranch(ctx); err != nil {
		return fmt.Errorf("failed to ensure sessions branch: %w", err)
	}

	parentHash, rootTreeHash, err := s.getSessionsBranchRef()
	if err != nil {
		return err
	}

	basePath := checkpointID.Path() + "/"
	checkpointPath := checkpointID.Path()
	entries, err := s.flattenCheckpointEntries(rootTreeHash, checkpointPath)
	if err != nil {
		return err
	}

	rootMetadataPath := basePath + paths.MetadataFileName
	entry, exists := entries[rootMetadataPath]
	if !exists {
		return ErrCheckpointNotFound
	}

	summary, err := s.readSummaryFromBlob(entry.Hash)
	if err != nil {
		return fmt.Errorf("failed to read checkpoint summary: %w", err)
	}
	summary.CombinedAttribution = combinedAttribution

	metadataJSON, err := jsonutil.MarshalIndentWithNewline(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal checkpoint summary: %w", err)
	}
	metadataHash, err := CreateBlobFromContent(s.repo, metadataJSON)
	if err != nil {
		return fmt.Errorf("failed to create checkpoint summary blob: %w", err)
	}
	entries[rootMetadataPath] = object.TreeEntry{
		Name: rootMetadataPath,
		Mode: filemode.Regular,
		Hash: metadataHash,
	}

	newTreeHash, err := s.spliceCheckpointSubtree(ctx, rootTreeHash, checkpointID, basePath, entries)
	if err != nil {
		return err
	}

	authorName, authorEmail := GetGitAuthorFromRepo(s.repo)
	commitMsg := fmt.Sprintf("Update checkpoint summary for %s", checkpointID)
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

// findSessionIndex returns the index of an existing session with the given ID,
// or the next available index if not found. This prevents duplicate session entries.
func (s *GitStore) findSessionIndex(ctx context.Context, basePath string, existingSummary *CheckpointSummary, entries map[string]object.TreeEntry, sessionID string) int {
	if existingSummary == nil {
		return 0
	}
	for i := range len(existingSummary.Sessions) {
		path := fmt.Sprintf("%s%d/%s", basePath, i, paths.MetadataFileName)
		if entry, exists := entries[path]; exists {
			meta, err := s.readMetadataFromBlob(entry.Hash)
			if err != nil {
				logging.Warn(
					ctx, "failed to read session metadata during dedup check",
					slog.Int("session_index", i),
					slog.String("session_id", sessionID),
					slog.String("error", err.Error()),
				)
				continue
			}
			if meta.SessionID == sessionID {
				return i
			}
		}
	}
	return len(existingSummary.Sessions)
}

// reaggregateFromEntries reads all session metadata from the entries map and
// reaggregates CheckpointsCount, FilesTouched, and TokenUsage.
func (s *GitStore) reaggregateFromEntries(basePath string, sessionCount int, entries map[string]object.TreeEntry) (int, []string, *agent.TokenUsage, error) {
	var totalCount int
	var allFiles []string
	var totalTokens *agent.TokenUsage

	for i := range sessionCount {
		path := fmt.Sprintf("%s%d/%s", basePath, i, paths.MetadataFileName)
		entry, exists := entries[path]
		if !exists {
			return 0, nil, nil, fmt.Errorf("session %d metadata not found at %s", i, path)
		}
		meta, err := s.readMetadataFromBlob(entry.Hash)
		if err != nil {
			return 0, nil, nil, fmt.Errorf("failed to read session %d metadata: %w", i, err)
		}
		totalCount += meta.CheckpointsCount
		allFiles = mergeFilesTouched(allFiles, meta.FilesTouched)
		totalTokens = aggregateTokenUsage(totalTokens, meta.TokenUsage)
	}

	return totalCount, allFiles, totalTokens, nil
}

func checkpointCreatedAt(opts WriteCommittedOptions) time.Time {
	if opts.CreatedAt.IsZero() {
		return time.Now().UTC()
	}
	return opts.CreatedAt.UTC()
}

// readJSONFromBlob reads JSON from a blob hash and decodes it to the given type.
func readJSONFromBlob[T any](repo *git.Repository, hash plumbing.Hash) (*T, error) {
	blob, err := repo.BlobObject(hash)
	if err != nil {
		return nil, fmt.Errorf("failed to get blob: %w", err)
	}

	reader, err := blob.Reader()
	if err != nil {
		return nil, fmt.Errorf("failed to get blob reader: %w", err)
	}
	defer reader.Close()

	var result T
	if err := json.NewDecoder(reader).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode: %w", err)
	}

	return &result, nil
}

// readSummaryFromBlob reads CheckpointSummary from a blob hash.
func (s *GitStore) readSummaryFromBlob(hash plumbing.Hash) (*CheckpointSummary, error) {
	return readJSONFromBlob[CheckpointSummary](s.repo, hash)
}

// aggregateTokenUsage sums two TokenUsage structs.
// Returns nil if both inputs are nil.
func aggregateTokenUsage(a, b *agent.TokenUsage) *agent.TokenUsage {
	if a == nil && b == nil {
		return nil
	}
	result := &agent.TokenUsage{}
	if a != nil {
		result.InputTokens = a.InputTokens
		result.CacheCreationTokens = a.CacheCreationTokens
		result.CacheReadTokens = a.CacheReadTokens
		result.OutputTokens = a.OutputTokens
		result.APICallCount = a.APICallCount
	}
	if b != nil {
		result.InputTokens += b.InputTokens
		result.CacheCreationTokens += b.CacheCreationTokens
		result.CacheReadTokens += b.CacheReadTokens
		result.OutputTokens += b.OutputTokens
		result.APICallCount += b.APICallCount
	}
	return result
}

// writeTranscript writes the transcript and content hash to the checkpoint entries.
// Returns (true, nil) if files were written, (false, nil) if transcript was empty.
func (s *GitStore) writeTranscript(ctx context.Context, opts WriteCommittedOptions, basePath string, entries map[string]object.TreeEntry) (bool, error) {
	logCtx := logging.WithComponent(ctx, "checkpoint")
	transcriptBytes := opts.Transcript.Bytes()

	// TranscriptPath fallback: data read from disk is an untrusted source,
	// so we redact it here. The in-memory path (opts.Transcript) is already
	// pre-redacted by the caller — enforced by the RedactedBytes type.
	if len(transcriptBytes) == 0 && opts.TranscriptPath != "" {
		rawData, readErr := os.ReadFile(opts.TranscriptPath)
		if readErr != nil {
			// Non-fatal: transcript may not exist yet
			rawData = nil
		}
		if len(rawData) > 0 {
			redacted, redactErr := redact.JSONLBytes(rawData)
			if redactErr != nil {
				return false, fmt.Errorf("failed to redact transcript from file: %w", redactErr)
			}
			transcriptBytes = redacted.Bytes()
		}
	}
	if len(transcriptBytes) == 0 {
		return false, nil
	}

	if opts.Agent == agent.AgentTypeCodex {
		transcriptBytes = codex.SanitizePortableTranscript(transcriptBytes)
	}

	// Chunk the transcript if it's too large
	chunkStart := time.Now()
	chunkCtx, chunkTranscriptSpan := perf.Start(ctx, "chunk_transcript")
	chunks, err := agent.ChunkTranscript(chunkCtx, transcriptBytes, opts.Agent)
	if err != nil {
		chunkTranscriptSpan.RecordError(err)
		chunkTranscriptSpan.End()
		return false, fmt.Errorf("failed to chunk transcript: %w", err)
	}
	chunkTranscriptSpan.End()
	chunkDuration := time.Since(chunkStart)

	// Write chunk files
	blobStart := time.Now()
	blobCtx, writeTranscriptBlobsSpan := perf.Start(chunkCtx, "write_transcript_blobs")
	for i, chunk := range chunks {
		chunkPath := basePath + agent.ChunkFileName(paths.TranscriptFileName, i)
		blobHash, err := CreateBlobFromContent(s.repo, chunk)
		if err != nil {
			writeTranscriptBlobsSpan.RecordError(err)
			writeTranscriptBlobsSpan.End()
			return false, err
		}
		entries[chunkPath] = object.TreeEntry{
			Name: chunkPath,
			Mode: filemode.Regular,
			Hash: blobHash,
		}
	}
	writeTranscriptBlobsSpan.End()
	blobDuration := time.Since(blobStart)

	// Content hash for deduplication (hash of full transcript)
	contentHashStart := time.Now()
	_, contentHashSpan := perf.Start(blobCtx, "write_transcript_content_hash")
	contentHash := fmt.Sprintf("sha256:%x", sha256.Sum256(transcriptBytes))
	hashBlob, err := CreateBlobFromContent(s.repo, []byte(contentHash))
	if err != nil {
		contentHashSpan.RecordError(err)
		contentHashSpan.End()
		return false, err
	}
	entries[basePath+paths.ContentHashFileName] = object.TreeEntry{
		Name: basePath + paths.ContentHashFileName,
		Mode: filemode.Regular,
		Hash: hashBlob,
	}
	contentHashSpan.End()

	logging.Debug(
		logCtx, "write transcript timings",
		slog.String("session_id", opts.SessionID),
		slog.String("checkpoint_id", opts.CheckpointID.String()),
		slog.String("agent", string(opts.Agent)),
		slog.Int64("chunk_transcript_ms", chunkDuration.Milliseconds()),
		slog.Int64("write_transcript_blobs_ms", blobDuration.Milliseconds()),
		slog.Int64("write_transcript_content_hash_ms", time.Since(contentHashStart).Milliseconds()),
		slog.Int("transcript_bytes", len(transcriptBytes)),
		slog.Int("chunk_count", len(chunks)),
	)
	return true, nil
}

// mergeFilesTouched combines two file lists, removing duplicates.
// All paths are normalized to forward slashes for platform-agnostic storage.
func mergeFilesTouched(existing, additional []string) []string {
	seen := make(map[string]bool)
	var result []string

	for _, f := range existing {
		f = filepath.ToSlash(f)
		if !seen[f] {
			seen[f] = true
			result = append(result, f)
		}
	}
	for _, f := range additional {
		f = filepath.ToSlash(f)
		if !seen[f] {
			seen[f] = true
			result = append(result, f)
		}
	}

	sort.Strings(result)
	return result
}

// redactSummary returns a copy of the summary with text fields redacted.
// Structural fields (Path, Line, EndLine) are preserved.
// NOTE: When adding new text fields to Summary, LearningsSummary, or CodeLearning,
// update this function to include them in redaction.
func redactSummary(s *Summary) *Summary {
	if s == nil {
		return nil
	}
	return &Summary{
		Intent:    redact.String(s.Intent),
		Outcome:   redact.String(s.Outcome),
		Friction:  redactStringSlice(s.Friction),
		OpenItems: redactStringSlice(s.OpenItems),
		Learnings: LearningsSummary{
			Repo:     redactStringSlice(s.Learnings.Repo),
			Workflow: redactStringSlice(s.Learnings.Workflow),
			Code:     redactCodeLearnings(s.Learnings.Code),
		},
	}
}

// redactStringSlice applies redact.String to each element.
func redactStringSlice(ss []string) []string {
	if ss == nil {
		return nil
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = redact.String(s)
	}
	return out
}
