//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranscriptBuilder_Basic(t *testing.T) {
	t.Parallel()
	builder := NewTranscriptBuilder()

	builder.AddUserMessage("Hello")
	builder.AddAssistantMessage("Hi there!")

	content := builder.String()

	if !strings.Contains(content, `"type":"user"`) {
		t.Error("transcript should contain user message")
	}
	if !strings.Contains(content, `"Hello"`) {
		t.Error("transcript should contain user content")
	}
	if !strings.Contains(content, `"type":"assistant"`) {
		t.Error("transcript should contain assistant message")
	}
	if !strings.Contains(content, `"Hi there!"`) {
		t.Error("transcript should contain assistant content")
	}
}

func TestTranscriptBuilder_ToolUse(t *testing.T) {
	t.Parallel()
	builder := NewTranscriptBuilder()

	builder.AddUserMessage("Create a file")
	toolID := builder.AddToolUse("mcp__acp__Write", "/test/file.txt", "content")
	builder.AddToolResult(toolID)
	builder.AddAssistantMessage("Done!")

	content := builder.String()

	if !strings.Contains(content, `"name":"mcp__acp__Write"`) {
		t.Error("transcript should contain tool name")
	}
	if !strings.Contains(content, `"file_path":"/test/file.txt"`) {
		t.Error("transcript should contain file path")
	}
	if !strings.Contains(content, `"tool_result"`) {
		t.Error("transcript should contain tool result")
	}
	if !strings.Contains(content, toolID) {
		t.Errorf("transcript should contain tool use ID: %s", toolID)
	}
}

func TestTranscriptBuilder_TaskToolUse(t *testing.T) {
	t.Parallel()
	builder := NewTranscriptBuilder()

	builder.AddUserMessage("Do something with a subagent")
	taskID := builder.AddTaskToolUse("toolu_task1", "Create some files")
	resultUUID := builder.AddTaskToolResult(taskID, "agent_abc123")

	content := builder.String()

	if !strings.Contains(content, `"name":"Task"`) {
		t.Error("transcript should contain Task tool")
	}
	if !strings.Contains(content, `"prompt":"Create some files"`) {
		t.Error("transcript should contain task prompt")
	}
	if !strings.Contains(content, "toolu_task1") {
		t.Error("transcript should contain task tool use ID")
	}
	if resultUUID == "" {
		t.Error("AddTaskToolResult should return the UUID")
	}
}

func TestTranscriptBuilder_WriteToFile(t *testing.T) {
	t.Parallel()
	builder := NewTranscriptBuilder()
	builder.AddUserMessage("Test prompt")
	builder.AddAssistantMessage("Test response")

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "subdir", "transcript.jsonl")

	err := builder.WriteToFile(filePath)
	if err != nil {
		t.Fatalf("WriteToFile failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Error("file should exist after WriteToFile")
	}

	// Verify content is valid JSONL
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d", len(lines))
	}

	// Each line should be valid JSON
	for i, line := range lines {
		var msg map[string]interface{}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Errorf("line %d is not valid JSON: %v", i, err)
		}
	}
}

func TestTranscriptBuilder_LastUUID(t *testing.T) {
	t.Parallel()
	builder := NewTranscriptBuilder()

	if builder.LastUUID() != "" {
		t.Error("LastUUID should be empty for new builder")
	}

	builder.AddUserMessage("First")
	uuid1 := builder.LastUUID()

	builder.AddAssistantMessage("Second")
	uuid2 := builder.LastUUID()

	if uuid1 == uuid2 {
		t.Error("UUIDs should be different for different messages")
	}
	if uuid2 == "" {
		t.Error("LastUUID should not be empty after adding message")
	}
}

func TestTranscriptBuilder_ToolUseIDsAreUnique(t *testing.T) {
	t.Parallel()
	builder := NewTranscriptBuilder()

	id1 := builder.AddToolUse("Write", "/file1.txt", "content1")
	id2 := builder.AddToolUse("Write", "/file2.txt", "content2")
	id3 := builder.AddToolUse("Write", "/file3.txt", "content3")

	if id1 == id2 || id2 == id3 || id1 == id3 {
		t.Errorf("tool use IDs should be unique: %s, %s, %s", id1, id2, id3)
	}
}
