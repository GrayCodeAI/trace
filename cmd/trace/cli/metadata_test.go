package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/paths"
)

func TestTaskMetadataDir(t *testing.T) {
	sessionMetadataDir := ".trace/metadata/2025-01-28-abc123"
	toolUseID := "toolu_xyz789"

	expected := ".trace/metadata/2025-01-28-abc123/tasks/toolu_xyz789"
	got := TaskMetadataDir(sessionMetadataDir, toolUseID)

	if got != expected {
		t.Errorf("TaskMetadataDir() = %v, want %v", got, expected)
	}
}

func TestTaskCheckpoint(t *testing.T) {
	tmpDir := t.TempDir()
	taskMetadataDir := filepath.Join(tmpDir, "tasks", "toolu_test123")

	checkpoint := &TaskCheckpoint{
		SessionID:      "session-abc",
		ToolUseID:      "toolu_test123",
		CheckpointUUID: "uuid-checkpoint-123",
		AgentID:        "agent_subagent_001",
	}

	// Test writing checkpoint
	err := WriteTaskCheckpoint(taskMetadataDir, checkpoint)
	if err != nil {
		t.Fatalf("WriteTaskCheckpoint() error = %v", err)
	}

	// Verify directory was created
	if _, err := os.Stat(taskMetadataDir); os.IsNotExist(err) {
		t.Error("Task metadata directory was not created")
	}

	// Verify checkpoint file was created with correct content
	checkpointFile := filepath.Join(taskMetadataDir, paths.CheckpointFileName)
	data, err := os.ReadFile(checkpointFile)
	if err != nil {
		t.Fatalf("Failed to read %s: %v", paths.CheckpointFileName, err)
	}

	var loaded TaskCheckpoint
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Failed to unmarshal %s: %v", paths.CheckpointFileName, err)
	}

	if loaded.SessionID != checkpoint.SessionID {
		t.Errorf("SessionID = %v, want %v", loaded.SessionID, checkpoint.SessionID)
	}
	if loaded.ToolUseID != checkpoint.ToolUseID {
		t.Errorf("ToolUseID = %v, want %v", loaded.ToolUseID, checkpoint.ToolUseID)
	}
	if loaded.CheckpointUUID != checkpoint.CheckpointUUID {
		t.Errorf("CheckpointUUID = %v, want %v", loaded.CheckpointUUID, checkpoint.CheckpointUUID)
	}
	if loaded.AgentID != checkpoint.AgentID {
		t.Errorf("AgentID = %v, want %v", loaded.AgentID, checkpoint.AgentID)
	}

	// Test reading checkpoint
	readCheckpoint, err := ReadTaskCheckpoint(taskMetadataDir)
	if err != nil {
		t.Fatalf("ReadTaskCheckpoint() error = %v", err)
	}
	if readCheckpoint.SessionID != checkpoint.SessionID {
		t.Errorf("Read SessionID = %v, want %v", readCheckpoint.SessionID, checkpoint.SessionID)
	}
}

func TestWriteTaskPrompt(t *testing.T) {
	tmpDir := t.TempDir()
	taskMetadataDir := filepath.Join(tmpDir, "tasks", "toolu_test")

	// Create directory first
	if err := os.MkdirAll(taskMetadataDir, 0o755); err != nil {
		t.Fatalf("Failed to create dir: %v", err)
	}

	prompt := "Please implement the feature described in task-01.md"
	err := WriteTaskPrompt(taskMetadataDir, prompt)
	if err != nil {
		t.Fatalf("WriteTaskPrompt() error = %v", err)
	}

	// Verify prompt file was created
	promptFile := filepath.Join(taskMetadataDir, paths.PromptFileName)
	data, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatalf("Failed to read %s: %v", paths.PromptFileName, err)
	}

	if string(data) != prompt {
		t.Errorf("prompt.txt content = %v, want %v", string(data), prompt)
	}
}

func TestCopyAgentTranscript(t *testing.T) {
	tmpDir := t.TempDir()

	// Create source transcript
	srcDir := filepath.Join(tmpDir, "source")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("Failed to create source dir: %v", err)
	}

	srcTranscript := filepath.Join(srcDir, "agent-test_agent.jsonl")
	transcriptContent := `{"type":"user","uuid":"u1","message":{"content":"test"}}
{"type":"assistant","uuid":"a1","message":{"content":[]}}`
	if err := os.WriteFile(srcTranscript, []byte(transcriptContent), 0o644); err != nil {
		t.Fatalf("Failed to create source transcript: %v", err)
	}

	// Create destination directory
	dstDir := filepath.Join(tmpDir, "dest", "tasks", "toolu_test")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatalf("Failed to create dest dir: %v", err)
	}

	// Test copy
	agentID := "test_agent"
	err := CopyAgentTranscript(srcTranscript, dstDir, agentID)
	if err != nil {
		t.Fatalf("CopyAgentTranscript() error = %v", err)
	}

	// Verify destination file
	dstTranscript := filepath.Join(dstDir, "agent-test_agent.jsonl")
	data, err := os.ReadFile(dstTranscript)
	if err != nil {
		t.Fatalf("Failed to read copied transcript: %v", err)
	}

	if string(data) != transcriptContent {
		t.Errorf("Copied transcript content mismatch")
	}
}

func TestCopyAgentTranscript_SourceNotExists(t *testing.T) {
	tmpDir := t.TempDir()
	dstDir := filepath.Join(tmpDir, "dest")
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatalf("Failed to create dest dir: %v", err)
	}

	// Should not error when source doesn't exist
	err := CopyAgentTranscript("/nonexistent/path.jsonl", dstDir, "test")
	if err != nil {
		t.Errorf("CopyAgentTranscript() should not error for non-existent source, got %v", err)
	}
}
