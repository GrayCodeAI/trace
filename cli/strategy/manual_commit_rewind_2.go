package strategy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	cpkg "github.com/GrayCodeAI/trace/cli/checkpoint"
	"github.com/GrayCodeAI/trace/cli/checkpoint/id"
	"github.com/GrayCodeAI/trace/cli/interactive"
	"github.com/GrayCodeAI/trace/cli/paths"

	"charm.land/huh/v2"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
)

// readSessionPrompt reads the first prompt from the session's prompt.txt file stored in git.
// Returns an empty string if the prompt cannot be read.
func readSessionPrompt(repo *git.Repository, commitHash plumbing.Hash, metadataDir string) string {
	// Get the commit and its tree
	commit, err := repo.CommitObject(commitHash)
	if err != nil {
		return ""
	}

	tree, err := commit.Tree()
	if err != nil {
		return ""
	}

	// Look for prompt.txt in the metadata directory
	promptPath := metadataDir + "/" + paths.PromptFileName
	promptEntry, err := tree.File(promptPath)
	if err != nil {
		return ""
	}

	content, err := promptEntry.Contents()
	if err != nil {
		return ""
	}

	return ExtractFirstPrompt(content)
}

// SessionRestoreStatus represents the status of a session being restored.
type SessionRestoreStatus int

const (
	StatusNew             SessionRestoreStatus = iota // Local file doesn't exist
	StatusUnchanged                                   // Local and checkpoint are the same
	StatusCheckpointNewer                             // Checkpoint has newer entries
	StatusLocalNewer                                  // Local has newer entries (conflict)
)

// SessionRestoreInfo contains information about a session being restored.
type SessionRestoreInfo struct {
	SessionID      string
	Prompt         string               // First prompt preview for display
	Status         SessionRestoreStatus // Status of this session
	LocalTime      time.Time
	CheckpointTime time.Time
}

// classifySessionsForRestore checks all sessions in a checkpoint and returns info
// about each session, including whether local logs have newer timestamps.
// repoRoot is used to compute per-session agent directories.
// Sessions without agent metadata are skipped (cannot determine target directory).
func (s *ManualCommitStrategy) classifySessionsForRestore(ctx context.Context, repoRoot string, store committedReader, checkpointID id.CheckpointID, summary *cpkg.CheckpointSummary) []SessionRestoreInfo {
	var sessions []SessionRestoreInfo

	totalSessions := len(summary.Sessions)
	// Check all sessions (0-based indexing)
	for i := range totalSessions {
		content, err := store.ReadSessionContent(ctx, checkpointID, i)
		if err != nil || content == nil || len(content.Transcript) == 0 {
			continue
		}

		sessionID := content.Metadata.SessionID
		if sessionID == "" || content.Metadata.Agent == "" {
			continue
		}

		sessionAgent, agErr := ResolveAgentForRewind(content.Metadata.Agent)
		if agErr != nil {
			continue
		}

		// Compute transcript path from current repo location for cross-machine portability.
		sessionAgentDir, dirErr := sessionAgent.GetSessionDir(repoRoot)
		if dirErr != nil {
			continue
		}
		localPath := sessionAgent.ResolveSessionFile(sessionAgentDir, sessionID)

		localTime := paths.GetLastTimestampFromFile(localPath)
		checkpointTime := paths.GetLastTimestampFromBytes(content.Transcript)
		status := ClassifyTimestamps(localTime, checkpointTime)

		sessions = append(sessions, SessionRestoreInfo{
			SessionID:      sessionID,
			Prompt:         ExtractFirstPrompt(content.Prompts),
			Status:         status,
			LocalTime:      localTime,
			CheckpointTime: checkpointTime,
		})
	}

	return sessions
}

// ClassifyTimestamps determines the restore status based on local and checkpoint timestamps.
func ClassifyTimestamps(localTime, checkpointTime time.Time) SessionRestoreStatus {
	// Local file doesn't exist (no timestamp found)
	if localTime.IsZero() {
		return StatusNew
	}

	// Can't determine checkpoint time - treat as new/safe
	if checkpointTime.IsZero() {
		return StatusNew
	}

	// Compare timestamps
	if localTime.After(checkpointTime) {
		return StatusLocalNewer
	}
	if checkpointTime.After(localTime) {
		return StatusCheckpointNewer
	}
	return StatusUnchanged
}

// StatusToText returns a human-readable status string.
func StatusToText(status SessionRestoreStatus) string {
	switch status {
	case StatusNew:
		return "(new)"
	case StatusUnchanged:
		return "(unchanged)"
	case StatusCheckpointNewer:
		return "(checkpoint is newer)"
	case StatusLocalNewer:
		return "(local is newer)" // shouldn't appear in non-conflict list
	default:
		return ""
	}
}

// PromptOverwriteNewerLogs asks the user for confirmation to overwrite local
// session logs that have newer timestamps than the checkpoint versions.
func PromptOverwriteNewerLogs(errW io.Writer, sessions []SessionRestoreInfo) (bool, error) {
	if !interactive.CanPromptInteractively() {
		return false, errors.New("cannot prompt to overwrite local session logs in non-interactive mode; rerun with --force to overwrite or use a TTY to confirm")
	}

	// Separate conflicting and non-conflicting sessions
	var conflicting, nonConflicting []SessionRestoreInfo
	for _, s := range sessions {
		if s.Status == StatusLocalNewer {
			conflicting = append(conflicting, s)
		} else {
			nonConflicting = append(nonConflicting, s)
		}
	}

	fmt.Fprintf(errW, "\nWarning: Local session log(s) have newer entries than the checkpoint:\n")
	for _, info := range conflicting {
		// Show prompt if available, otherwise fall back to session ID
		if info.Prompt != "" {
			fmt.Fprintf(errW, "  \"%s\"\n", info.Prompt)
		} else {
			fmt.Fprintf(errW, "  Session: %s\n", info.SessionID)
		}
		fmt.Fprintf(errW, "    Local last entry:      %s\n", info.LocalTime.Local().Format("2006-01-02 15:04:05"))
		fmt.Fprintf(errW, "    Checkpoint last entry: %s\n", info.CheckpointTime.Local().Format("2006-01-02 15:04:05"))
	}

	// Show non-conflicting sessions with their status
	if len(nonConflicting) > 0 {
		fmt.Fprintf(errW, "\nThese other session(s) will also be restored:\n")
		for _, info := range nonConflicting {
			statusText := StatusToText(info.Status)
			if info.Prompt != "" {
				fmt.Fprintf(errW, "  \"%s\" %s\n", info.Prompt, statusText)
			} else {
				fmt.Fprintf(errW, "  Session: %s %s\n", info.SessionID, statusText)
			}
		}
	}

	fmt.Fprintf(errW, "\nOverwriting will lose the newer local entries.\n\n")

	var confirmed bool
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Overwrite local session logs with checkpoint versions?").
				Value(&confirmed),
		),
	)
	if isAccessibleMode() {
		form = form.WithAccessible(true)
	}

	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, fmt.Errorf("failed to get confirmation: %w", err)
	}

	return confirmed, nil
}
