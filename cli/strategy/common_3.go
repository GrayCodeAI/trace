package strategy

import (
	"context"
	"log/slog"
	"strings"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/paths"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// getSessionDescriptionFromTree reads the first line of prompt.txt from a git tree.
// This is the tree-based equivalent of getSessionDescription (which reads from filesystem).
//
// If metadataDir is provided, looks for files at metadataDir/prompt.txt.
// If metadataDir is empty, first tries the root of the tree (for when the tree is already
// the session directory), then falls back to
// searching for .trace/metadata/*/prompt.txt (for full worktree trees).
func getSessionDescriptionFromTree(tree *object.Tree, metadataDir string) string {
	// Helper to read first line from a file in tree
	readFirstLine := func(path string) string {
		file, err := tree.File(path)
		if err != nil {
			return ""
		}
		content, err := file.Contents()
		if err != nil {
			return ""
		}
		lines := strings.SplitN(content, "\n", 2)
		if len(lines) > 0 && lines[0] != "" {
			return strings.TrimSpace(lines[0])
		}
		return ""
	}

	// If metadataDir is provided, look there directly
	if metadataDir != "" {
		if desc := readFirstLine(metadataDir + "/" + paths.PromptFileName); desc != "" {
			return desc
		}
		return NoDescription
	}

	// No metadataDir provided - first try looking at the root of the tree
	// (used when the tree is already the session directory)
	if desc := readFirstLine(paths.PromptFileName); desc != "" {
		return desc
	}

	// Fall back to searching for .trace/metadata/*/prompt.txt
	// (used when the tree is the full worktree)
	var desc string
	//nolint:errcheck // We ignore errors here as we're just searching for a description
	_ = tree.Files().ForEach(func(f *object.File) error {
		if desc != "" {
			return nil // Already found description
		}
		name := f.Name
		if strings.Contains(name, ".trace/metadata/") && strings.HasSuffix(name, "/"+paths.PromptFileName) {
			content, err := f.Contents()
			if err != nil {
				return nil //nolint:nilerr // Skip files we can't read, continue searching
			}
			lines := strings.SplitN(content, "\n", 2)
			if len(lines) > 0 && lines[0] != "" {
				desc = strings.TrimSpace(lines[0])
			}
		}
		return nil
	})

	if desc != "" {
		return desc
	}
	return NoDescription
}

// GetGitAuthorFromRepo retrieves the git user.name and user.email,
// checking both the repository-local config and the global ~/.gitconfig.
// Delegates to checkpoint.GetGitAuthorFromRepo — this wrapper exists so
// callers within the strategy package don't need a qualified import.
func GetGitAuthorFromRepo(repo *git.Repository) (name, email string) {
	return checkpoint.GetGitAuthorFromRepo(repo)
}

// GetCurrentBranchName returns the short name of the current branch if HEAD points to a branch.
// Returns an empty string if in detached HEAD state or if there's an error reading HEAD.
// This is used to capture branch metadata for checkpoints.
func GetCurrentBranchName(repo *git.Repository) string {
	head, err := repo.Head()
	if err != nil || !head.Name().IsBranch() {
		return ""
	}
	return head.Name().Short()
}

// getMainBranchHash returns the hash of the main branch (main or master).
// Returns ZeroHash if no main branch is found.
func GetMainBranchHash(repo *git.Repository) plumbing.Hash {
	// Try common main branch names
	for _, branchName := range []string{branchMain, branchMaster} {
		// Try local branch first
		ref, err := repo.Reference(plumbing.NewBranchReferenceName(branchName), true)
		if err == nil {
			return ref.Hash()
		}
		// Try remote tracking branch
		ref, err = repo.Reference(plumbing.NewRemoteReferenceName("origin", branchName), true)
		if err == nil {
			return ref.Hash()
		}
	}
	return plumbing.ZeroHash
}

// GetDefaultBranchName returns the name of the default branch.
// First checks origin/HEAD, then falls back to checking if main/master exists.
// Returns empty string if unable to determine.
// NOTE: Duplicated from cli/git_operations.go - see ENT-129 for consolidation.
func GetDefaultBranchName(repo *git.Repository) string {
	// Try to get the symbolic reference for origin/HEAD
	// Use resolved=false to get the symbolic ref itself, then extract its target
	ref, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", "HEAD"), false)
	if err == nil && ref != nil && ref.Type() == plumbing.SymbolicReference {
		target := ref.Target().String()
		if branchName, found := strings.CutPrefix(target, "refs/remotes/origin/"); found {
			return branchName
		}
	}

	// Fallback: check if origin/main or origin/master exists
	if _, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", branchMain), true); err == nil {
		return branchMain
	}
	if _, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", branchMaster), true); err == nil {
		return branchMaster
	}

	// Final fallback: check local branches
	if _, err := repo.Reference(plumbing.NewBranchReferenceName(branchMain), true); err == nil {
		return branchMain
	}
	if _, err := repo.Reference(plumbing.NewBranchReferenceName(branchMaster), true); err == nil {
		return branchMaster
	}

	return ""
}

// IsOnDefaultBranch checks if the repository HEAD is on the default branch.
// Returns (isOnDefault, currentBranchName).
// NOTE: Duplicated from cli/git_operations.go - see ENT-129 for consolidation.
func IsOnDefaultBranch(repo *git.Repository) (bool, string) {
	currentBranch := GetCurrentBranchName(repo)
	if currentBranch == "" {
		return false, ""
	}

	defaultBranch := GetDefaultBranchName(repo)
	if defaultBranch == "" {
		// Can't determine default, check common names
		if currentBranch == branchMain || currentBranch == branchMaster {
			return true, currentBranch
		}
		return false, currentBranch
	}

	return currentBranch == defaultBranch, currentBranch
}

// prepareTranscriptForState ensures the transcript is up-to-date for the given session.
// Only prepares for ACTIVE sessions — IDLE/ENDED sessions are already flushed.
// Resolves the agent from state.AgentType internally. Multiple calls are safe but
// not free — callers should avoid redundant calls for performance.
func prepareTranscriptForState(ctx context.Context, state *SessionState) {
	if !state.Phase.IsActive() || state.TranscriptPath == "" || state.AgentType == "" {
		return
	}
	ag, err := agent.GetByAgentType(state.AgentType)
	if err != nil {
		logging.Debug(
			ctx, "prepareTranscriptForState: unknown agent type",
			slog.String("session_id", state.SessionID),
			slog.String("agent_type", string(state.AgentType)),
			slog.Any("error", err),
		)
		return
	}
	prepareTranscriptIfNeeded(ctx, ag, state.TranscriptPath)
}

// prepareTranscriptIfNeeded calls PrepareTranscript for agents that implement
// the TranscriptPreparer interface. This ensures transcript files exist before
// they are read (e.g., OpenCode creates its transcript lazily via `opencode export`).
// Errors are silently ignored — this is best-effort for hook paths.
func prepareTranscriptIfNeeded(ctx context.Context, ag agent.Agent, transcriptPath string) {
	if ag == nil || transcriptPath == "" {
		return
	}
	if preparer, ok := agent.AsTranscriptPreparer(ag); ok {
		// Best-effort: callers handle missing files gracefully.
		// Transcript may not be available yet (e.g., agent not installed).
		_ = preparer.PrepareTranscript(ctx, transcriptPath) //nolint:errcheck // Best-effort in hook path
	}
}
