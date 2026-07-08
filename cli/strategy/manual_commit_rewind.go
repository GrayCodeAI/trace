package strategy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	cpkg "github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/osroot"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cli/trailers"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// committedReader provides read access to committed checkpoint data.
// Both checkpoint.GitStore (v1) and checkpoint.V2GitStore implement this interface.
type committedReader interface {
	ReadCommitted(ctx context.Context, checkpointID id.CheckpointID) (*cpkg.CheckpointSummary, error)
	ReadSessionContent(ctx context.Context, checkpointID id.CheckpointID, sessionIndex int) (*cpkg.SessionContent, error)
}

// GetRewindPoints returns available rewind points.
// Uses checkpoint.GitStore.ListTemporaryCheckpoints for reading from shadow branches.
func (s *ManualCommitStrategy) GetRewindPoints(ctx context.Context, limit int) ([]RewindPoint, error) {
	repo, err := OpenRepository(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open git repository: %w", err)
	}

	// Get checkpoint store
	store, err := s.getCheckpointStore()
	if err != nil {
		return nil, fmt.Errorf("failed to get checkpoint store: %w", err)
	}

	// Get current HEAD to find matching shadow branch
	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}

	// Find sessions for current HEAD
	sessions, err := s.findSessionsForCommit(ctx, head.Hash().String())
	if err != nil {
		// Log error but continue to check for logs-only points
		sessions = nil
	}

	var allPoints []RewindPoint

	// Collect checkpoint points from active sessions using checkpoint.GitStore
	// Cache session prompts by session ID to avoid re-reading the same prompt file
	sessionPrompts := make(map[string]string)

	for _, state := range sessions {
		checkpoints, err := store.ListTemporaryCheckpoints(ctx, state.BaseCommit, state.WorktreeID, state.SessionID, limit)
		if err != nil {
			continue // Error reading checkpoints, skip this session
		}

		for _, cp := range checkpoints {
			// Get session prompt (cached by session ID)
			sessionPrompt, ok := sessionPrompts[cp.SessionID]
			if !ok {
				sessionPrompt = readSessionPrompt(repo, cp.CommitHash, cp.MetadataDir)
				sessionPrompts[cp.SessionID] = sessionPrompt
			}

			allPoints = append(allPoints, RewindPoint{
				ID:               cp.CommitHash.String(),
				Message:          cp.Message,
				MetadataDir:      cp.MetadataDir,
				Date:             cp.Timestamp,
				IsTaskCheckpoint: cp.IsTaskCheckpoint,
				ToolUseID:        cp.ToolUseID,
				SessionID:        cp.SessionID,
				SessionPrompt:    sessionPrompt,
				Agent:            state.AgentType,
			})
		}
	}

	// Sort by date, most recent first
	sort.Slice(allPoints, func(i, j int) bool {
		return allPoints[i].Date.After(allPoints[j].Date)
	})

	if len(allPoints) > limit {
		allPoints = allPoints[:limit]
	}

	// Also include logs-only points from commit history
	logsOnlyPoints, err := s.GetLogsOnlyRewindPoints(ctx, limit)
	if err == nil && len(logsOnlyPoints) > 0 {
		// Build set of existing point IDs for deduplication
		existingIDs := make(map[string]bool)
		for _, p := range allPoints {
			existingIDs[p.ID] = true
		}

		// Add logs-only points that aren't already in the list
		for _, p := range logsOnlyPoints {
			if !existingIDs[p.ID] {
				allPoints = append(allPoints, p)
			}
		}

		// Re-sort by date
		sort.Slice(allPoints, func(i, j int) bool {
			return allPoints[i].Date.After(allPoints[j].Date)
		})

		// Re-trim to limit
		if len(allPoints) > limit {
			allPoints = allPoints[:limit]
		}
	}

	return allPoints, nil
}

// GetLogsOnlyRewindPoints finds commits in the current branch's history that have
// condensed session logs on the trace/checkpoints/v1 branch. These are commits that
// were created with session data but the shadow branch has been condensed.
//
// The function works by:
// 1. Getting all checkpoints from the trace/checkpoints/v1 branch
// 2. Building a map of checkpoint ID -> checkpoint info
// 3. Scanning the current branch history for commits with Trace-Checkpoint trailers
// 4. Matching by checkpoint ID (stable across amend/rebase)
func (s *ManualCommitStrategy) GetLogsOnlyRewindPoints(ctx context.Context, limit int) ([]RewindPoint, error) {
	repo, err := OpenRepository(ctx)
	if err != nil {
		return nil, err
	}

	// Get all checkpoints from trace/checkpoints/v1 branch
	checkpoints, err := s.listCheckpoints(ctx)
	if err != nil {
		// No checkpoints yet is fine
		return nil, nil //nolint:nilerr // Expected when no checkpoints exist
	}

	if len(checkpoints) == 0 {
		return nil, nil
	}

	// Build map of checkpoint ID -> checkpoint info
	// Checkpoint ID is the stable link from Trace-Checkpoint trailer
	checkpointInfoMap := make(map[id.CheckpointID]CheckpointInfo)
	for _, cp := range checkpoints {
		if !cp.CheckpointID.IsEmpty() {
			checkpointInfoMap[cp.CheckpointID] = cp
		}
	}

	// Get metadata branch tree for reading session prompts (best-effort, ignore errors)
	metadataTree, _ := GetMetadataBranchTree(repo) //nolint:errcheck // Best-effort for session prompts

	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}

	// Use LogOptions with Order=LogOrderCommitterTime to traverse all parents of merge commits.
	iter, err := repo.Log(&git.LogOptions{
		From:  head.Hash(),
		Order: git.LogOrderCommitterTime,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get commit log: %w", err)
	}

	var points []RewindPoint
	count := 0

	err = iter.ForEach(func(c *object.Commit) error {
		if err := ctx.Err(); err != nil {
			return err //nolint:wrapcheck // Propagating context cancellation
		}
		if count >= logsOnlyScanLimit {
			return errStop
		}
		count++

		// Extract all checkpoint IDs from Trace-Checkpoint trailers.
		// Squash merge commits may contain multiple trailers from the original commits.
		allCpIDs := trailers.ParseAllCheckpoints(c.Message)
		if len(allCpIDs) == 0 {
			return nil
		}

		// Resolve to the latest checkpoint by creation time (consistent with `trace resume`).
		cpInfo, found := ResolveLatestCheckpointFromMap(allCpIDs, checkpointInfoMap)
		if !found {
			return nil
		}

		// Create logs-only rewind point
		message := strings.Split(c.Message, "\n")[0]

		// Read session prompts from metadata tree
		var sessionPrompt string
		var sessionPrompts []string
		if metadataTree != nil {
			//lint:ignore SA1019 // already present in codebase
			checkpointPath := paths.CheckpointPath(cpInfo.CheckpointID)
			// For multi-session checkpoints, read all prompts
			if cpInfo.SessionCount > 1 && len(cpInfo.SessionIDs) > 1 {
				sessionPrompts = ReadAllSessionPromptsFromTree(metadataTree, checkpointPath, cpInfo.SessionCount, cpInfo.SessionIDs)
				// Use the last (most recent) prompt as the main session prompt
				if len(sessionPrompts) > 0 {
					sessionPrompt = sessionPrompts[len(sessionPrompts)-1]
				}
			} else {
				sessionPrompt = ReadSessionPromptFromTree(metadataTree, checkpointPath)
				if sessionPrompt != "" {
					sessionPrompts = []string{sessionPrompt}
				}
			}
		}

		points = append(points, RewindPoint{
			ID:             c.Hash.String(),
			Message:        message,
			Date:           c.Author.When,
			IsLogsOnly:     true,
			CheckpointID:   cpInfo.CheckpointID,
			Agent:          cpInfo.Agent,
			SessionID:      cpInfo.SessionID,
			SessionPrompt:  sessionPrompt,
			SessionCount:   cpInfo.SessionCount,
			SessionIDs:     cpInfo.SessionIDs,
			SessionPrompts: sessionPrompts,
		})

		return nil
	})

	if err != nil && !errors.Is(err, errStop) {
		return nil, fmt.Errorf("error iterating commits: %w", err)
	}

	if len(points) > limit {
		points = points[:limit]
	}

	return points, nil
}

// ResolveLatestCheckpointFromMap picks the checkpoint with the latest CreatedAt
// from a list of checkpoint IDs. Filters to IDs present in infoMap, then picks
// the one with the most recent CreatedAt.
func ResolveLatestCheckpointFromMap(cpIDs []id.CheckpointID, infoMap map[id.CheckpointID]CheckpointInfo) (CheckpointInfo, bool) {
	var found bool
	var latest CheckpointInfo
	for _, cpID := range cpIDs {
		info, ok := infoMap[cpID]
		if !ok {
			continue
		}
		if !found || info.CreatedAt.After(latest.CreatedAt) {
			latest = info
			found = true
		}
	}
	return latest, found
}

// Rewind restores the working directory to a checkpoint.
//

func (s *ManualCommitStrategy) Rewind(ctx context.Context, w, errW io.Writer, point RewindPoint) error {
	repo, err := OpenRepository(ctx)
	if err != nil {
		return fmt.Errorf("failed to open git repository: %w", err)
	}

	// Get the checkpoint commit
	commitHash := plumbing.NewHash(point.ID)
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		return fmt.Errorf("failed to get commit: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return fmt.Errorf("failed to get tree: %w", err)
	}

	// Reset the shadow branch to the rewound checkpoint
	// This ensures the next checkpoint will only include prompts from this point forward
	if err := s.resetShadowBranchToCheckpoint(ctx, repo, commit); err != nil {
		// Log warning but don't fail - file restoration is the primary operation
		fmt.Fprintf(os.Stderr, "[trace] Warning: failed to reset shadow branch: %v\n", err)
	}

	// Load session state to get untracked files that existed at session start
	sessionID, hasSessionTrailer := trailers.ParseSession(commit.Message)
	var preservedUntrackedFiles map[string]bool
	if hasSessionTrailer {
		state, stateErr := s.loadSessionState(ctx, sessionID)
		if stateErr == nil && state != nil && len(state.UntrackedFilesAtStart) > 0 {
			preservedUntrackedFiles = make(map[string]bool)
			for _, f := range state.UntrackedFilesAtStart {
				preservedUntrackedFiles[f] = true
			}
		}
	}

	// Build set of files in the checkpoint tree (excluding metadata)
	checkpointFiles := make(map[string]bool)
	err = tree.Files().ForEach(func(f *object.File) error {
		if err := ctx.Err(); err != nil {
			return err //nolint:wrapcheck // Propagating context cancellation
		}
		if !strings.HasPrefix(f.Name, traceDir) {
			checkpointFiles[f.Name] = true
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to list checkpoint files: %w", err)
	}

	// Get HEAD tree to identify tracked files
	head, err := repo.Head()
	if err != nil {
		return fmt.Errorf("failed to get HEAD: %w", err)
	}
	headCommit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return fmt.Errorf("failed to get HEAD commit: %w", err)
	}
	headTree, err := headCommit.Tree()
	if err != nil {
		return fmt.Errorf("failed to get HEAD tree: %w", err)
	}

	// Build set of files tracked in HEAD
	trackedFiles := make(map[string]bool)
	if iterErr := headTree.Files().ForEach(func(f *object.File) error {
		if err := ctx.Err(); err != nil {
			return err //nolint:wrapcheck // Propagating context cancellation
		}
		trackedFiles[f.Name] = true
		return nil
	}); iterErr != nil {
		logging.Debug(
			ctx, "HEAD tree iteration error during rewind",
			slog.Any("error", iterErr),
		)
	}

	// Get repository root to walk from there
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		repoRoot = "." // Fallback to current directory
	}

	// Open os.Root at repo root for traversal-resistant file operations.
	// Defense-in-depth: paths come from git tree objects (trusted) but we
	// still scope operations to prevent any escape from the repo root.
	repoRootHandle, err := os.OpenRoot(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to open repo root: %w", err)
	}
	defer repoRootHandle.Close()

	// Find and delete untracked files that aren't in the checkpoint.
	// Uses git ls-files to only consider non-ignored files, avoiding walks through
	// large ignored directories like node_modules/.
	untrackedNow, err := collectUntrackedFiles(ctx)
	if err != nil {
		// Non-fatal - continue with restoration
		fmt.Fprintf(errW, "Warning: error listing untracked files: %v\n", err)
	}
	for _, relPath := range untrackedNow {
		// If file is in checkpoint, it will be restored
		if checkpointFiles[relPath] {
			continue
		}

		// If file is tracked in HEAD, don't delete (user's committed work)
		if trackedFiles[relPath] {
			continue
		}

		// If file existed at session start, preserve it (untracked user files)
		if preservedUntrackedFiles[relPath] {
			continue
		}

		// File is untracked and not in checkpoint - delete it via os.Root
		if removeErr := osroot.Remove(repoRootHandle, relPath); removeErr == nil {
			fmt.Fprintf(w, "  Deleted: %s\n", relPath)
		}
	}

	// Restore files from checkpoint
	err = tree.Files().ForEach(func(f *object.File) error {
		if err := ctx.Err(); err != nil {
			return err //nolint:wrapcheck // Propagating context cancellation
		}
		// Skip metadata directories - these are for checkpoint storage, not working dir
		if strings.HasPrefix(f.Name, traceDir) {
			return nil
		}

		contents, err := f.Contents()
		if err != nil {
			return fmt.Errorf("failed to read file %s: %w", f.Name, err)
		}

		// Ensure directory exists (MkdirAll not available on os.Root)
		absPath := filepath.Join(repoRoot, f.Name)
		dir := filepath.Dir(absPath)
		if dir != "." {
			//nolint:gosec // G301: Need 0o755 for user directories during rewind
			// #nosec G301 -- restoring the user's project working tree during rewind; directories must match standard project directory permissions (0o755), not private data
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", dir, err)
			}
		}

		// Write file with appropriate permissions via os.Root
		var perm os.FileMode = 0o644
		if f.Mode == filemode.Executable {
			perm = 0o755
		}
		if err := osroot.WriteFile(repoRootHandle, f.Name, []byte(contents), perm); err != nil {
			return fmt.Errorf("failed to write file %s: %w", f.Name, err)
		}

		fmt.Fprintf(w, "  Restored: %s\n", f.Name)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to iterate tree files: %w", err)
	}

	fmt.Fprintln(w)
	if len(point.ID) >= 7 {
		fmt.Fprintf(w, "Restored files from shadow commit %s\n", point.ID[:7])
	} else {
		fmt.Fprintf(w, "Restored files from shadow commit %s\n", point.ID)
	}
	fmt.Fprintln(w)

	return nil
}

// resetShadowBranchToCheckpoint resets the shadow branch HEAD to the given checkpoint.
// This ensures that when the user commits after rewinding, the next checkpoint will only
// include prompts from the rewound point, not prompts from later checkpoints.
func (s *ManualCommitStrategy) resetShadowBranchToCheckpoint(ctx context.Context, repo *git.Repository, commit *object.Commit) error {
	// Extract session ID from the checkpoint commit's Trace-Session trailer
	sessionID, found := trailers.ParseSession(commit.Message)
	if !found {
		return errors.New("checkpoint has no Trace-Session trailer")
	}

	// Load session state to get the shadow branch name
	state, err := s.loadSessionState(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to load session state: %w", err)
	}
	if state == nil {
		return fmt.Errorf("session %s not found", sessionID)
	}

	// Reset the shadow branch to the checkpoint commit
	shadowBranchName := getShadowBranchNameForCommit(state.BaseCommit, state.WorktreeID)
	refName := plumbing.NewBranchReferenceName(shadowBranchName)

	// Update the reference to point to the checkpoint commit
	ref := plumbing.NewHashReference(refName, commit.Hash)
	if err := repo.Storer.SetReference(ref); err != nil {
		return fmt.Errorf("failed to update shadow branch: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[trace] Reset shadow branch %s to checkpoint %s\n", shadowBranchName, commit.Hash.String()[:7])
	return nil
}

// CanRewind checks if rewinding is possible.
// For manual-commit strategy, rewind restores files from a checkpoint - uncommitted changes are expected
// and will be replaced by the checkpoint contents. Returns true with a warning message showing
// what changes will be reverted.
func (s *ManualCommitStrategy) CanRewind(ctx context.Context) (bool, string, error) {
	return checkCanRewindWithWarning(ctx)
}

// PreviewRewind returns what will happen if rewinding to the given point.
// This allows showing warnings about untracked files that will be deleted.
func (s *ManualCommitStrategy) PreviewRewind(ctx context.Context, point RewindPoint) (*RewindPreview, error) {
	// Logs-only points don't modify the working directory
	if point.IsLogsOnly {
		return &RewindPreview{}, nil
	}

	repo, err := OpenRepository(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open git repository: %w", err)
	}

	// Get the checkpoint commit
	commitHash := plumbing.NewHash(point.ID)
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	// Load session state to get untracked files that existed at session start
	sessionID, hasSessionTrailer := trailers.ParseSession(commit.Message)
	var preservedUntrackedFiles map[string]bool
	if hasSessionTrailer {
		state, stateErr := s.loadSessionState(ctx, sessionID)
		if stateErr == nil && state != nil && len(state.UntrackedFilesAtStart) > 0 {
			preservedUntrackedFiles = make(map[string]bool)
			for _, f := range state.UntrackedFilesAtStart {
				preservedUntrackedFiles[f] = true
			}
		}
	}

	// Build set of files in the checkpoint tree (excluding metadata)
	checkpointFiles := make(map[string]bool)
	var filesToRestore []string
	err = tree.Files().ForEach(func(f *object.File) error {
		if err := ctx.Err(); err != nil {
			return err //nolint:wrapcheck // Propagating context cancellation
		}
		if !strings.HasPrefix(f.Name, traceDir) {
			checkpointFiles[f.Name] = true
			filesToRestore = append(filesToRestore, f.Name)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list checkpoint files: %w", err)
	}

	// Get HEAD tree to identify tracked files
	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD: %w", err)
	}
	headCommit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD commit: %w", err)
	}
	headTree, err := headCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get HEAD tree: %w", err)
	}

	// Build set of files tracked in HEAD
	trackedFiles := make(map[string]bool)
	//nolint:errcheck // Error is not critical for preview
	_ = headTree.Files().ForEach(func(f *object.File) error {
		if err := ctx.Err(); err != nil {
			return err //nolint:wrapcheck // Propagating context cancellation
		}
		trackedFiles[f.Name] = true
		return nil
	})

	// Find untracked files that would be deleted.
	// Uses git ls-files to only consider non-ignored files.
	var filesToDelete []string
	untrackedNow, untrackedErr := collectUntrackedFiles(ctx)
	if untrackedErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not list untracked files for preview: %v\n", untrackedErr)
		return &RewindPreview{
			FilesToRestore: filesToRestore,
			FilesToDelete:  filesToDelete,
		}, nil
	}
	for _, relPath := range untrackedNow {
		if checkpointFiles[relPath] {
			continue
		}
		if trackedFiles[relPath] {
			continue
		}
		if preservedUntrackedFiles[relPath] {
			continue
		}
		filesToDelete = append(filesToDelete, relPath)
	}

	// Sort for consistent output
	sort.Strings(filesToRestore)
	sort.Strings(filesToDelete)

	return &RewindPreview{
		FilesToRestore: filesToRestore,
		FilesToDelete:  filesToDelete,
	}, nil
}

// RestoreLogsOnly restores session logs from a logs-only rewind point.
// This fetches the transcript from trace/checkpoints/v1 and writes it to the agent's session directory.
// Does not modify the working directory.
// When multiple sessions were condensed to the same checkpoint, ALL sessions are restored.
// If force is false, prompts for confirmation when local logs have newer timestamps.
// Returns info about each restored session so callers can print correct per-session resume commands.
func (s *ManualCommitStrategy) RestoreLogsOnly(ctx context.Context, w, errW io.Writer, point RewindPoint, force bool) ([]RestoredSession, error) {
	if !point.IsLogsOnly {
		return nil, errors.New("not a logs-only rewind point")
	}

	if point.CheckpointID.IsEmpty() {
		return nil, errors.New("missing checkpoint ID")
	}

	// Resolve which store has this checkpoint. Try v2 first when enabled.
	// The chosen reader is used for all subsequent reads (summary + session content)
	// to avoid mixed v1/v2 reads. No per-session fallback to v1: during dual-write,
	// both stores receive the same data, so if v2 has the summary it also has the
	// transcripts on /full/* refs.
	var reader committedReader
	var summary *cpkg.CheckpointSummary

	if settings.IsCheckpointsV2Enabled(ctx) {
		v2Store, v2Err := s.getV2CheckpointStore(ctx)
		if v2Err == nil {
			v2Summary, readErr := v2Store.ReadCommitted(ctx, point.CheckpointID)
			if readErr != nil {
				logging.Debug(
					ctx, "v2 ReadCommitted failed, falling back to v1",
					slog.String("checkpoint_id", string(point.CheckpointID)),
					slog.String("error", readErr.Error()),
				)
			} else if v2Summary != nil {
				reader = v2Store
				summary = v2Summary
			}
		}
	}

	if summary == nil {
		v1Store, err := s.getCheckpointStore()
		if err != nil {
			return nil, fmt.Errorf("failed to get checkpoint store: %w", err)
		}
		summary, err = v1Store.ReadCommitted(ctx, point.CheckpointID)
		if err != nil {
			return nil, fmt.Errorf("failed to read checkpoint: %w", err)
		}
		reader = v1Store
	}
	if summary == nil {
		return nil, fmt.Errorf("checkpoint not found: %s", point.CheckpointID)
	}

	// Get worktree root for agent session directory lookup
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get worktree root: %w", err)
	}

	// Check for newer local logs if not forcing
	if !force {
		sessions := s.classifySessionsForRestore(ctx, repoRoot, reader, point.CheckpointID, summary)
		hasConflicts := false
		for _, sess := range sessions {
			if sess.Status == StatusLocalNewer {
				hasConflicts = true
				break
			}
		}
		if hasConflicts {
			shouldOverwrite, promptErr := PromptOverwriteNewerLogs(errW, sessions)
			if promptErr != nil {
				return nil, promptErr
			}
			if !shouldOverwrite {
				fmt.Fprintf(w, "Resume cancelled. Local session logs preserved.\n")
				return nil, nil
			}
		}
	}

	// Count sessions to restore
	totalSessions := len(summary.Sessions)
	if totalSessions > 1 {
		fmt.Fprintf(w, "Restoring %d sessions from checkpoint:\n", totalSessions)
	}

	// Restore all sessions (oldest to newest, using 0-based indexing)
	var restored []RestoredSession
	for i := range totalSessions {
		content, readErr := reader.ReadSessionContent(ctx, point.CheckpointID, i)
		if readErr != nil {
			if !errors.Is(readErr, cpkg.ErrNoTranscript) {
				fmt.Fprintf(errW, "  Warning: failed to read session %d: %v\n", i, readErr)
			}
			continue
		}
		if content == nil || len(content.Transcript) == 0 {
			continue
		}

		sessionID := content.Metadata.SessionID
		if sessionID == "" {
			fmt.Fprintf(errW, "  Warning: session %d has no session ID, skipping\n", i)
			continue
		}

		// Resolve per-session agent from metadata — skip if agent is unknown
		if content.Metadata.Agent == "" {
			fmt.Fprintf(errW, "  Warning: session %d (%s) has no agent metadata, skipping (cannot determine target directory)\n", i, sessionID)
			continue
		}
		sessionAgent, agErr := ResolveAgentForRewind(content.Metadata.Agent)
		if agErr != nil {
			fmt.Fprintf(errW, "  Warning: session %d (%s) has unknown agent %q, skipping\n", i, sessionID, content.Metadata.Agent)
			continue
		}

		// Compute transcript path from current repo location for cross-machine portability.
		sessionAgentDir, dirErr := sessionAgent.GetSessionDir(repoRoot)
		if dirErr != nil {
			fmt.Fprintf(errW, "  Warning: failed to get session dir for session %d: %v\n", i, dirErr)
			continue
		}
		sessionFile := sessionAgent.ResolveSessionFile(sessionAgentDir, sessionID)
		if resolver, ok := sessionAgent.(agent.RestoredSessionPathResolver); ok {
			resolvedFile, resolveErr := resolver.ResolveRestoredSessionFile(sessionAgentDir, sessionID, content.Transcript)
			if resolveErr != nil {
				fmt.Fprintf(errW, "  Warning: failed to resolve restored session path for session %d (%s): %v (using fallback path)\n", i, sessionID, resolveErr)
			} else {
				sessionFile = resolvedFile
			}
		}

		// Get first prompt for display
		promptPreview := ExtractFirstPrompt(content.Prompts)

		if totalSessions > 1 {
			isLatest := i == totalSessions-1
			if promptPreview != "" {
				if isLatest {
					fmt.Fprintf(w, "  Session %d (latest): %s\n", i+1, promptPreview)
				} else {
					fmt.Fprintf(w, "  Session %d: %s\n", i+1, promptPreview)
				}
			}
			fmt.Fprintf(w, "    Writing to: %s\n", sessionFile)
		} else {
			fmt.Fprintf(w, "Writing transcript to: %s\n", sessionFile)
		}

		// Ensure parent directory exists (session file may be in a different dir than sessionAgentDir)
		if mkdirErr := os.MkdirAll(filepath.Dir(sessionFile), 0o750); mkdirErr != nil {
			fmt.Fprintf(errW, "    Warning: failed to create directory: %v\n", mkdirErr)
			continue
		}

		agentSession := &agent.AgentSession{
			SessionID:  sessionID,
			AgentName:  sessionAgent.Name(),
			RepoPath:   repoRoot,
			SessionRef: sessionFile,
			NativeData: content.Transcript,
		}
		if writeErr := sessionAgent.WriteSession(ctx, agentSession); writeErr != nil {
			if totalSessions > 1 {
				fmt.Fprintf(errW, "    Warning: failed to write session: %v\n", writeErr)
				continue
			}
			return nil, fmt.Errorf("failed to write session: %w", writeErr)
		}

		restored = append(restored, RestoredSession{
			SessionID: sessionID,
			Agent:     sessionAgent.Type(),
			Prompt:    promptPreview,
			CreatedAt: content.Metadata.CreatedAt,
		})
	}

	return restored, nil
}

// ResolveAgentForRewind resolves the agent from checkpoint metadata.
func ResolveAgentForRewind(agentType types.AgentType) (agent.Agent, error) {
	ag, err := agent.GetByAgentType(agentType)
	if err != nil {
		return nil, fmt.Errorf("resolving agent %q: %w", agentType, err)
	}
	return ag, nil
}
