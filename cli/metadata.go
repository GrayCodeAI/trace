package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/GrayCodeAI/trace/cli/jsonutil"
	"github.com/GrayCodeAI/trace/cli/osroot"
	"github.com/GrayCodeAI/trace/cli/paths"
)

// TaskCheckpoint contains the checkpoint information for a task
type TaskCheckpoint struct {
	SessionID      string `json:"session_id"`
	ToolUseID      string `json:"tool_use_id"`
	CheckpointUUID string `json:"checkpoint_uuid"`
	AgentID        string `json:"agent_id,omitempty"`
}

// TaskMetadataDir returns the path to a task's metadata directory
// within the session metadata directory.
func TaskMetadataDir(sessionMetadataDir, toolUseID string) string {
	return filepath.Join(sessionMetadataDir, "tasks", toolUseID)
}

// WriteTaskCheckpoint writes the checkpoint.json file to the task metadata directory.
// Creates the directory if it doesn't exist.
// Uses os.Root for traversal-resistant file writes within the metadata directory.
func WriteTaskCheckpoint(taskMetadataDir string, checkpoint *TaskCheckpoint) error {
	// Create directory if it doesn't exist (MkdirAll not available on os.Root)
	if err := os.MkdirAll(taskMetadataDir, 0o750); err != nil {
		return fmt.Errorf("failed to create task metadata directory: %w", err)
	}

	data, err := jsonutil.MarshalIndentWithNewline(checkpoint, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal checkpoint: %w", err)
	}

	// Use os.Root scoped to the metadata directory for traversal-resistant write
	root, err := os.OpenRoot(taskMetadataDir)
	if err != nil {
		return fmt.Errorf("failed to open task metadata directory: %w", err)
	}
	defer root.Close()

	if err := osroot.WriteFile(root, paths.CheckpointFileName, data, 0o600); err != nil {
		return fmt.Errorf("failed to write checkpoint file: %w", err)
	}

	return nil
}

// ReadTaskCheckpoint reads the checkpoint file from the task metadata directory.
// Uses os.Root for traversal-resistant file reads within the metadata directory.
func ReadTaskCheckpoint(taskMetadataDir string) (*TaskCheckpoint, error) {
	root, err := os.OpenRoot(taskMetadataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to open task metadata directory: %w", err)
	}
	defer root.Close()

	data, err := osroot.ReadFile(root, paths.CheckpointFileName)
	if err != nil {
		return nil, fmt.Errorf("failed to read checkpoint file: %w", err)
	}

	var checkpoint TaskCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return nil, fmt.Errorf("failed to unmarshal checkpoint: %w", err)
	}

	return &checkpoint, nil
}

// WriteTaskPrompt writes the task prompt to the task metadata directory.
// Uses os.Root for traversal-resistant file writes within the metadata directory.
func WriteTaskPrompt(taskMetadataDir, prompt string) error {
	root, err := os.OpenRoot(taskMetadataDir)
	if err != nil {
		return fmt.Errorf("failed to open task metadata directory: %w", err)
	}
	defer root.Close()

	if err := osroot.WriteFile(root, paths.PromptFileName, []byte(prompt), 0o600); err != nil {
		return fmt.Errorf("failed to write prompt file: %w", err)
	}
	return nil
}

// CopyAgentTranscript copies a subagent's transcript to the task metadata directory.
// If the source transcript doesn't exist, this is a no-op (not an error).
func CopyAgentTranscript(srcTranscript, taskMetadataDir, agentID string) error {
	// Check if source exists
	if _, err := os.Stat(srcTranscript); os.IsNotExist(err) {
		// Source doesn't exist, nothing to copy
		return nil
	}

	dstTranscript := filepath.Join(taskMetadataDir, fmt.Sprintf("agent-%s.jsonl", agentID))
	return copyFile(srcTranscript, dstTranscript)
}
