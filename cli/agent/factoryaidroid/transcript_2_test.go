package factoryaidroid

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/GrayCodeAI/trace/cli/transcript"
)

// makeWriteToolLine returns a Droid-format JSONL line with a Write tool_use for the given file.
func makeWriteToolLine(t *testing.T, id, filePath string) string {
	t.Helper()
	return makeFileToolLine(t, "Write", id, filePath)
}

// makeEditToolLine returns a Droid-format JSONL line with an Edit tool_use for the given file.
func makeEditToolLine(t *testing.T, id, filePath string) string {
	t.Helper()
	return makeFileToolLine(t, "Edit", id, filePath)
}

// makeTaskToolUseLine returns a Droid-format JSONL line with a Task tool_use (spawning a subagent).
func makeTaskToolUseLine(t *testing.T, id, toolUseID string) string {
	t.Helper()
	innerMsg := mustMarshal(t, map[string]interface{}{
		"role": "assistant",
		"content": []map[string]interface{}{
			{
				"type":  "tool_use",
				"id":    toolUseID,
				"name":  "Task",
				"input": map[string]string{"prompt": "do something"},
			},
		},
	})
	line := mustMarshal(t, map[string]interface{}{
		"type":    "message",
		"id":      id,
		"message": json.RawMessage(innerMsg),
	})
	return string(line)
}

// makeTaskResultLine returns a Droid-format JSONL user line with a tool_result containing agentId.
func makeTaskResultLine(t *testing.T, id, toolUseID, agentID string) string {
	t.Helper()
	innerMsg := mustMarshal(t, map[string]interface{}{
		"role": "user",
		"content": []map[string]interface{}{
			{
				"type":        "tool_result",
				"tool_use_id": toolUseID,
				"content":     "agentId: " + agentID,
			},
		},
	})
	line := mustMarshal(t, map[string]interface{}{
		"type":    "message",
		"id":      id,
		"message": json.RawMessage(innerMsg),
	})
	return string(line)
}

// makeUserTextLine returns a Droid-format JSONL line with a user text message (array content).
func makeUserTextLine(t *testing.T, id, text string) string {
	t.Helper()
	innerMsg := mustMarshal(t, map[string]interface{}{
		"role": "user",
		"content": []map[string]interface{}{
			{"type": "text", "text": text},
		},
	})
	line := mustMarshal(t, map[string]interface{}{
		"type":    "message",
		"id":      id,
		"message": json.RawMessage(innerMsg),
	})
	return string(line)
}

// makeAssistantTextLine returns a Droid-format JSONL line with an assistant text message.
func makeAssistantTextLine(t *testing.T, id, text string) string {
	t.Helper()
	innerMsg := mustMarshal(t, map[string]interface{}{
		"role": "assistant",
		"content": []map[string]interface{}{
			{"type": "text", "text": text},
		},
	})
	line := mustMarshal(t, map[string]interface{}{
		"type":    "message",
		"id":      id,
		"message": json.RawMessage(innerMsg),
	})
	return string(line)
}

// makeAssistantTokenLine returns a Droid-format JSONL line with an assistant message that has usage data.
func makeAssistantTokenLine(t *testing.T, id, msgID string, inputTokens, outputTokens int) string {
	t.Helper()
	innerMsg := mustMarshal(t, map[string]interface{}{
		"role": "assistant",
		"id":   msgID,
		"usage": map[string]int{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	})
	line := mustMarshal(t, map[string]interface{}{
		"type":    "message",
		"id":      id,
		"message": json.RawMessage(innerMsg),
	})
	return string(line)
}

func TestExtractPrompts(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	transcriptPath := tmpDir + "/transcript.jsonl"

	writeJSONLFile(
		t, transcriptPath,
		makeUserTextLine(t, "u1", "Fix the login bug"),
		makeAssistantTextLine(t, "a1", "I'll fix the login bug."),
		makeUserTextLine(t, "u2", "Now add tests"),
	)

	ag := &FactoryAIDroidAgent{}
	prompts, err := ag.ExtractPrompts(transcriptPath, 0)
	if err != nil {
		t.Fatalf("ExtractPrompts() error = %v", err)
	}

	if len(prompts) != 2 {
		t.Fatalf("ExtractPrompts() got %d prompts, want 2", len(prompts))
	}
	if prompts[0] != "Fix the login bug" {
		t.Errorf("prompts[0] = %q, want %q", prompts[0], "Fix the login bug")
	}
	if prompts[1] != "Now add tests" {
		t.Errorf("prompts[1] = %q, want %q", prompts[1], "Now add tests")
	}
}

func TestExtractPrompts_StripsIDETags(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	transcriptPath := tmpDir + "/transcript.jsonl"

	// User message with IDE context tags injected by VSCode extension
	promptWithTags := `<ide_opened_file>/repo/main.go</ide_opened_file>Fix the bug`
	writeJSONLFile(
		t, transcriptPath,
		makeUserTextLine(t, "u1", promptWithTags),
	)

	ag := &FactoryAIDroidAgent{}
	prompts, err := ag.ExtractPrompts(transcriptPath, 0)
	if err != nil {
		t.Fatalf("ExtractPrompts() error = %v", err)
	}

	if len(prompts) != 1 {
		t.Fatalf("ExtractPrompts() got %d prompts, want 1", len(prompts))
	}
	if prompts[0] != "Fix the bug" {
		t.Errorf("prompts[0] = %q, want %q (IDE tags should be stripped)", prompts[0], "Fix the bug")
	}
}

func TestExtractPrompts_WithOffset(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	transcriptPath := tmpDir + "/transcript.jsonl"

	writeJSONLFile(
		t, transcriptPath,
		makeUserTextLine(t, "u1", "First prompt"),
		makeAssistantTextLine(t, "a1", "Done."),
		makeUserTextLine(t, "u2", "Second prompt"),
		makeAssistantTextLine(t, "a2", "Done again."),
	)

	ag := &FactoryAIDroidAgent{}
	// Skip first 2 lines (first user+assistant turn)
	prompts, err := ag.ExtractPrompts(transcriptPath, 2)
	if err != nil {
		t.Fatalf("ExtractPrompts() error = %v", err)
	}

	if len(prompts) != 1 {
		t.Fatalf("ExtractPrompts() got %d prompts, want 1", len(prompts))
	}
	if prompts[0] != "Second prompt" {
		t.Errorf("prompts[0] = %q, want %q", prompts[0], "Second prompt")
	}
}

func TestExtractSummary(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	transcriptPath := tmpDir + "/transcript.jsonl"

	writeJSONLFile(
		t, transcriptPath,
		makeUserTextLine(t, "u1", "Fix the bug"),
		makeAssistantTextLine(t, "a1", "Working on it..."),
		makeUserTextLine(t, "u2", "Thanks"),
		makeAssistantTextLine(t, "a2", "All done! The login bug is fixed."),
	)

	ag := &FactoryAIDroidAgent{}
	summary, err := ag.ExtractSummary(transcriptPath)
	if err != nil {
		t.Fatalf("ExtractSummary() error = %v", err)
	}

	if summary != "All done! The login bug is fixed." {
		t.Errorf("ExtractSummary() = %q, want %q", summary, "All done! The login bug is fixed.")
	}
}

func TestExtractSummary_SkipsToolUseBlocks(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	transcriptPath := tmpDir + "/transcript.jsonl"

	// Last assistant message has tool_use (no text), second-to-last has text
	writeJSONLFile(
		t, transcriptPath,
		makeUserTextLine(t, "u1", "Edit main.go"),
		makeAssistantTextLine(t, "a1", "I updated the file."),
		makeWriteToolLine(t, "a2", "/repo/main.go"),
	)

	ag := &FactoryAIDroidAgent{}
	summary, err := ag.ExtractSummary(transcriptPath)
	if err != nil {
		t.Fatalf("ExtractSummary() error = %v", err)
	}

	// Should find "I updated the file." since the tool_use message has no text block
	if summary != "I updated the file." {
		t.Errorf("ExtractSummary() = %q, want %q", summary, "I updated the file.")
	}
}

func TestExtractSummary_EmptyTranscript(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	transcriptPath := tmpDir + "/transcript.jsonl"
	if err := os.WriteFile(transcriptPath, []byte(""), 0o600); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	ag := &FactoryAIDroidAgent{}
	summary, err := ag.ExtractSummary(transcriptPath)
	if err != nil {
		t.Fatalf("ExtractSummary() error = %v", err)
	}

	if summary != "" {
		t.Errorf("ExtractSummary() = %q, want empty string", summary)
	}
}

func TestParseDroidTranscript_MalformedLines(t *testing.T) {
	t.Parallel()

	// Transcript with some broken JSON lines interspersed with valid ones
	data := []byte(
		`{"type":"message","id":"m1","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}` + "\n" +
			`{"broken json` + "\n" +
			`not even close to json` + "\n" +
			`{"type":"message","id":"m2","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}` + "\n" +
			`{"type":"session_event","data":"ignored"}` + "\n",
	)

	lines, _, err := ParseDroidTranscriptFromBytes(data, 0)
	if err != nil {
		t.Fatalf("ParseDroidTranscriptFromBytes() error = %v", err)
	}

	// Only the 2 valid "message" type lines should be parsed
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (malformed lines should be silently skipped)", len(lines))
	}
	if lines[0].Type != transcript.TypeUser {
		t.Errorf("lines[0].Type = %q, want %q", lines[0].Type, transcript.TypeUser)
	}
	if lines[1].Type != transcript.TypeAssistant {
		t.Errorf("lines[1].Type = %q, want %q", lines[1].Type, transcript.TypeAssistant)
	}
}

func TestCalculateTotalTokenUsageFromTranscript_WithSubagentFiles(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	transcriptPath := tmpDir + "/transcript.jsonl"
	subagentsDir := tmpDir + "/tasks/toolu_task1"

	if err := os.MkdirAll(subagentsDir, 0o755); err != nil {
		t.Fatalf("failed to create subagents dir: %v", err)
	}

	// Main transcript: assistant message with tokens + Task spawning subagent "sub1"
	writeJSONLFile(
		t, transcriptPath,
		makeAssistantTokenLine(t, "a1", "msg_main1", 100, 50),
		makeTaskToolUseLine(t, "a2", "toolu_task2"),
		makeTaskResultLine(t, "u2", "toolu_task2", "sub99"),
	)

	// Subagent transcript: assistant message with its own tokens
	writeJSONLFile(
		t, subagentsDir+"/agent-sub99.jsonl",
		makeAssistantTokenLine(t, "sa1", "msg_sub1", 200, 80),
		makeAssistantTokenLine(t, "sa2", "msg_sub2", 150, 60),
	)

	usage, err := CalculateTotalTokenUsageFromTranscript(transcriptPath, 0, subagentsDir)
	if err != nil {
		t.Fatalf("CalculateTotalTokenUsageFromTranscript() error: %v", err)
	}

	// Main agent: 100 input, 50 output, 1 API call
	if usage.InputTokens != 100 {
		t.Errorf("main InputTokens = %d, want 100", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("main OutputTokens = %d, want 50", usage.OutputTokens)
	}
	if usage.APICallCount != 1 {
		t.Errorf("main APICallCount = %d, want 1", usage.APICallCount)
	}

	// Subagent tokens should be aggregated
	if usage.SubagentTokens == nil {
		t.Fatal("SubagentTokens is nil, expected subagent token data")
	}
	if usage.SubagentTokens.InputTokens != 350 {
		t.Errorf("subagent InputTokens = %d, want 350 (200+150)", usage.SubagentTokens.InputTokens)
	}
	if usage.SubagentTokens.OutputTokens != 140 {
		t.Errorf("subagent OutputTokens = %d, want 140 (80+60)", usage.SubagentTokens.OutputTokens)
	}
	if usage.SubagentTokens.APICallCount != 2 {
		t.Errorf("subagent APICallCount = %d, want 2", usage.SubagentTokens.APICallCount)
	}
}

func TestCleanModelName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "custom prefix stripped",
			raw:  "custom:Gemini-2.5-Pro-0",
			want: "Gemini-2.5-Pro-0",
		},
		{
			name: "no prefix unchanged",
			raw:  "claude-opus-4-6",
			want: "claude-opus-4-6",
		},
		{
			name: "empty string",
			raw:  "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := cleanModelName(tt.raw)
			if got != tt.want {
				t.Errorf("cleanModelName(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestExtractModelFromTranscript_SettingsFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	transcriptPath := tmpDir + "/session.jsonl"
	settingsPath := tmpDir + "/session.settings.json"

	// Write a transcript file (content doesn't matter for model extraction)
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"session_start"}`+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Write the settings file with the model
	settingsData := `{"model":"custom:Gemini-2.5-Pro-0","reasoningEffort":"none"}`
	if err := os.WriteFile(settingsPath, []byte(settingsData), 0o644); err != nil {
		t.Fatalf("failed to write settings: %v", err)
	}

	model := ExtractModelFromTranscript(transcriptPath)
	if model != "Gemini-2.5-Pro-0" {
		t.Errorf("ExtractModelFromTranscript() = %q, want %q", model, "Gemini-2.5-Pro-0")
	}
}

func TestExtractModelFromTranscript_NoCustomPrefix(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	transcriptPath := tmpDir + "/session.jsonl"
	settingsPath := tmpDir + "/session.settings.json"

	if err := os.WriteFile(transcriptPath, []byte(`{"type":"session_start"}`+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	settingsData := `{"model":"claude-opus-4-6"}`
	if err := os.WriteFile(settingsPath, []byte(settingsData), 0o644); err != nil {
		t.Fatalf("failed to write settings: %v", err)
	}

	model := ExtractModelFromTranscript(transcriptPath)
	if model != "claude-opus-4-6" {
		t.Errorf("ExtractModelFromTranscript() = %q, want %q", model, "claude-opus-4-6")
	}
}

func TestExtractModelFromTranscript_NoSettingsFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	transcriptPath := tmpDir + "/session.jsonl"

	// Write transcript but no settings file
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"session_start"}`+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	model := ExtractModelFromTranscript(transcriptPath)
	if model != "" {
		t.Errorf("ExtractModelFromTranscript() = %q, want empty", model)
	}
}

func TestExtractModelFromTranscript_CorruptSettingsFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	transcriptPath := tmpDir + "/session.jsonl"
	settingsPath := tmpDir + "/session.settings.json"

	if err := os.WriteFile(transcriptPath, []byte(`{"type":"session_start"}`+"\n"), 0o644); err != nil {
		t.Fatalf("failed to write transcript: %v", err)
	}

	// Write invalid JSON to the settings file
	if err := os.WriteFile(settingsPath, []byte(`{not valid json`), 0o644); err != nil {
		t.Fatalf("failed to write settings: %v", err)
	}

	model := ExtractModelFromTranscript(transcriptPath)
	if model != "" {
		t.Errorf("ExtractModelFromTranscript() = %q, want empty for corrupt settings", model)
	}
}

func TestExtractModelFromTranscript_EmptyPath(t *testing.T) {
	t.Parallel()

	model := ExtractModelFromTranscript("")
	if model != "" {
		t.Errorf("ExtractModelFromTranscript(\"\") = %q, want empty", model)
	}
}
