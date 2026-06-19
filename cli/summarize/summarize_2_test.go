package summarize

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/redact"
	"github.com/stretchr/testify/require"
)

func TestBuildCondensedTranscriptFromBytes_Codex_ExecCommandDetail(t *testing.T) {
	t.Parallel()

	codexTranscript := []byte(`{"timestamp":"t1","type":"session_meta","payload":{"id":"s1"}}
{"timestamp":"t2","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Running command."}]}}
{"timestamp":"t3","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"call_1","arguments":"{\"cmd\":\"ls -la\",\"workdir\":\"/repo\"}"}}
{"timestamp":"t4","type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":"total 0"}}
`)

	entries, err := BuildCondensedTranscriptFromBytes(redact.AlreadyRedacted(codexTranscript), agent.AgentTypeCodex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find the tool entry
	var toolEntry *Entry
	for i := range entries {
		if entries[i].Type == EntryTypeTool {
			toolEntry = &entries[i]
			break
		}
	}
	require.NotNil(t, toolEntry, "no tool entry found in entries: %#v", entries)
	if toolEntry.ToolName != "exec_command" {
		t.Fatalf("expected exec_command, got %q", toolEntry.ToolName)
	}
	if toolEntry.ToolDetail != "ls -la" {
		t.Fatalf("expected tool detail 'ls -la', got %q", toolEntry.ToolDetail)
	}
}

func TestBuildCondensedTranscriptFromBytes_OpenCodeUserAndAssistant(t *testing.T) {
	// OpenCode export JSON format
	ocExportJSON := `{
		"info": {"id": "test-session"},
		"messages": [
			{"info": {"id": "msg-1", "role": "user", "time": {"created": 1708300000}}, "parts": [{"type": "text", "text": "Fix the bug in main.go"}]},
			{"info": {"id": "msg-2", "role": "assistant", "time": {"created": 1708300001}}, "parts": [{"type": "text", "text": "I'll fix the bug."}]}
		]
	}`

	entries, err := BuildCondensedTranscriptFromBytes(redact.AlreadyRedacted([]byte(ocExportJSON)), agent.AgentTypeOpenCode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	if entries[0].Type != EntryTypeUser {
		t.Errorf("entry 0: expected type %s, got %s", EntryTypeUser, entries[0].Type)
	}
	if entries[0].Content != "Fix the bug in main.go" {
		t.Errorf("entry 0: unexpected content: %s", entries[0].Content)
	}

	if entries[1].Type != EntryTypeAssistant {
		t.Errorf("entry 1: expected type %s, got %s", EntryTypeAssistant, entries[1].Type)
	}
	if entries[1].Content != "I'll fix the bug." {
		t.Errorf("entry 1: unexpected content: %s", entries[1].Content)
	}
}

func TestBuildCondensedTranscriptFromBytes_OpenCodeToolCalls(t *testing.T) {
	// OpenCode export JSON format with tool calls
	ocExportJSON := `{
		"info": {"id": "test-session"},
		"messages": [
			{"info": {"id": "msg-1", "role": "user", "time": {"created": 1708300000}}, "parts": [{"type": "text", "text": "Edit main.go"}]},
			{"info": {"id": "msg-2", "role": "assistant", "time": {"created": 1708300001}}, "parts": [
				{"type": "text", "text": "Editing now."},
				{"type": "tool", "tool": "edit", "callID": "call-1", "state": {"status": "completed", "input": {"filePath": "main.go"}, "output": "Applied"}},
				{"type": "tool", "tool": "bash", "callID": "call-2", "state": {"status": "completed", "input": {"command": "go test ./..."}, "output": "PASS"}}
			]}
		]
	}`

	entries, err := BuildCondensedTranscriptFromBytes(redact.AlreadyRedacted([]byte(ocExportJSON)), agent.AgentTypeOpenCode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// user + assistant + 2 tool calls
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}

	if entries[2].Type != EntryTypeTool {
		t.Errorf("entry 2: expected type %s, got %s", EntryTypeTool, entries[2].Type)
	}
	if entries[2].ToolName != "edit" {
		t.Errorf("entry 2: expected tool name edit, got %s", entries[2].ToolName)
	}
	if entries[2].ToolDetail != testMainGoFile {
		t.Errorf("entry 2: expected tool detail main.go, got %s", entries[2].ToolDetail)
	}

	if entries[3].ToolName != "bash" {
		t.Errorf("entry 3: expected tool name bash, got %s", entries[3].ToolName)
	}
	if entries[3].ToolDetail != "go test ./..." {
		t.Errorf("entry 3: expected tool detail 'go test ./...', got %s", entries[3].ToolDetail)
	}
}

func TestBuildCondensedTranscriptFromBytes_OpenCodeSkipsEmptyContent(t *testing.T) {
	// OpenCode export JSON format with empty content messages
	ocExportJSON := `{
		"info": {"id": "test-session"},
		"messages": [
			{"info": {"id": "msg-1", "role": "user", "time": {"created": 1708300000}}, "parts": []},
			{"info": {"id": "msg-2", "role": "assistant", "time": {"created": 1708300001}}, "parts": []},
			{"info": {"id": "msg-3", "role": "user", "time": {"created": 1708300010}}, "parts": [{"type": "text", "text": "Real prompt"}]}
		]
	}`

	entries, err := BuildCondensedTranscriptFromBytes(redact.AlreadyRedacted([]byte(ocExportJSON)), agent.AgentTypeOpenCode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (empty content skipped), got %d", len(entries))
	}
	if entries[0].Content != "Real prompt" {
		t.Errorf("expected 'Real prompt', got %s", entries[0].Content)
	}
}

func TestBuildCondensedTranscriptFromBytes_OpenCodeInvalidJSON(t *testing.T) {
	// Invalid JSON now returns an error (not silently skipped like JSONL)
	_, err := BuildCondensedTranscriptFromBytes(redact.AlreadyRedacted([]byte("not json")), agent.AgentTypeOpenCode)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestBuildCondensedTranscriptFromBytes_CompactTranscriptFallback(t *testing.T) {
	compactJSONL := `{"v":1,"agent":"pi","cli_version":"test","type":"user","ts":"2026-01-01T00:00:00Z","content":[{"text":"Create bye.txt"}]}
{"v":1,"agent":"pi","cli_version":"test","type":"assistant","ts":"2026-01-01T00:00:01Z","content":[{"type":"tool_use","id":"tc1","name":"Write","input":{"path":"bye.txt"}},{"type":"text","text":"Created bye.txt"}]}
`

	entries, err := BuildCondensedTranscriptFromBytes(redact.AlreadyRedacted([]byte(compactJSONL)), types.AgentType("Pi"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if entries[0].Type != EntryTypeUser || entries[0].Content != "Create bye.txt" {
		t.Fatalf("unexpected first entry: %+v", entries[0])
	}
	if entries[1].Type != EntryTypeTool || entries[1].ToolName != "Write" || entries[1].ToolDetail != "bye.txt" {
		t.Fatalf("unexpected tool entry: %+v", entries[1])
	}
	if entries[2].Type != EntryTypeAssistant || entries[2].Content != "Created bye.txt" {
		t.Fatalf("unexpected assistant entry: %+v", entries[2])
	}
}

func TestBuildCondensedTranscriptFromBytes_CursorRoleBasedJSONL(t *testing.T) {
	// Cursor transcripts use "role" instead of "type" and wrap user text in <user_query> tags.
	// The transcript parser normalizes role→type, so condensation should work.
	cursorJSONL := `{"role":"user","message":{"content":[{"type":"text","text":"<user_query>\nhello\n</user_query>"}]}}
{"role":"assistant","message":{"content":[{"type":"text","text":"Hi there!"}]}}
{"role":"user","message":{"content":[{"type":"text","text":"<user_query>\nadd one to a file and commit\n</user_query>"}]}}
{"role":"assistant","message":{"content":[{"type":"text","text":"Created one.txt with one and committed."}]}}
`

	entries, err := BuildCondensedTranscriptFromBytes(redact.AlreadyRedacted([]byte(cursorJSONL)), agent.AgentTypeCursor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(entries) == 0 {
		t.Fatal("expected non-empty entries for Cursor transcript, got 0 (role→type normalization may be broken)")
	}

	// Should have 4 entries: 2 user + 2 assistant
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}

	if entries[0].Type != EntryTypeUser {
		t.Errorf("entry 0: expected type %s, got %s", EntryTypeUser, entries[0].Type)
	}
	if !strings.Contains(entries[0].Content, "hello") {
		t.Errorf("entry 0: expected content containing 'hello', got %q", entries[0].Content)
	}

	if entries[1].Type != EntryTypeAssistant {
		t.Errorf("entry 1: expected type %s, got %s", EntryTypeAssistant, entries[1].Type)
	}
	if entries[1].Content != "Hi there!" {
		t.Errorf("entry 1: expected 'Hi there!', got %q", entries[1].Content)
	}

	if entries[2].Type != EntryTypeUser {
		t.Errorf("entry 2: expected type %s, got %s", EntryTypeUser, entries[2].Type)
	}

	if entries[3].Type != EntryTypeAssistant {
		t.Errorf("entry 3: expected type %s, got %s", EntryTypeAssistant, entries[3].Type)
	}
}

func TestBuildCondensedTranscriptFromBytes_CursorNoToolUseBlocks(t *testing.T) {
	// Cursor transcripts have no tool_use blocks — only text content.
	// This verifies we get entries (not an empty result) even without tool calls.
	cursorJSONL := `{"role":"user","message":{"content":[{"type":"text","text":"write a poem"}]}}
{"role":"assistant","message":{"content":[{"type":"text","text":"Here is a poem about code."}]}}
`

	entries, err := BuildCondensedTranscriptFromBytes(redact.AlreadyRedacted([]byte(cursorJSONL)), agent.AgentTypeCursor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// No tool entries should appear
	for i, e := range entries {
		if e.Type == EntryTypeTool {
			t.Errorf("entry %d: unexpected tool entry in Cursor transcript", i)
		}
	}
}

func TestBuildCondensedTranscriptFromBytes_DroidUserAndAssistant(t *testing.T) {
	// Droid uses an envelope: {"type":"message","id":"...","message":{"role":"...","content":[...]}}
	droidJSONL := strings.Join([]string{
		`{"type":"session_start","session":{"session_id":"s1"}}`,
		`{"type":"message","id":"m1","message":{"role":"user","content":[{"type":"text","text":"Help me write a Go function"}]}}`,
		`{"type":"message","id":"m2","message":{"role":"assistant","content":[{"type":"text","text":"Sure, here is a function."}]}}`,
		`{"type":"message","id":"m3","message":{"role":"assistant","content":[{"type":"tool_use","name":"Write","input":{"file_path":"main.go","content":"package main"}}]}}`,
	}, "\n") + "\n"

	entries, err := BuildCondensedTranscriptFromBytes(redact.AlreadyRedacted([]byte(droidJSONL)), agent.AgentTypeFactoryAIDroid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// session_start is skipped; expect: user + assistant text + tool
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	if entries[0].Type != EntryTypeUser {
		t.Errorf("entry 0: expected type %s, got %s", EntryTypeUser, entries[0].Type)
	}
	if entries[0].Content != "Help me write a Go function" {
		t.Errorf("entry 0: unexpected content: %s", entries[0].Content)
	}

	if entries[1].Type != EntryTypeAssistant {
		t.Errorf("entry 1: expected type %s, got %s", EntryTypeAssistant, entries[1].Type)
	}
	if entries[1].Content != "Sure, here is a function." {
		t.Errorf("entry 1: unexpected content: %s", entries[1].Content)
	}

	if entries[2].Type != EntryTypeTool {
		t.Errorf("entry 2: expected type %s, got %s", EntryTypeTool, entries[2].Type)
	}
	if entries[2].ToolName != "Write" {
		t.Errorf("entry 2: expected tool name Write, got %s", entries[2].ToolName)
	}
	if entries[2].ToolDetail != testMainGoFile {
		t.Errorf("entry 2: expected tool detail main.go, got %s", entries[2].ToolDetail)
	}
}

func TestBuildCondensedTranscriptFromBytes_DroidMalformedInput(t *testing.T) {
	// Completely invalid content should return an error from the Droid parser
	_, err := BuildCondensedTranscriptFromBytes(redact.AlreadyRedacted([]byte("not valid jsonl at all{{{")), agent.AgentTypeFactoryAIDroid)
	// Droid parser is lenient — malformed lines are skipped. With no valid messages,
	// it returns an empty slice (not an error).
	if err != nil {
		t.Fatalf("unexpected error for malformed Droid input: %v", err)
	}
}

func TestBuildCondensedTranscriptFromBytes_DroidEmptyTranscript(t *testing.T) {
	entries, err := BuildCondensedTranscriptFromBytes(redact.AlreadyRedacted([]byte("")), agent.AgentTypeFactoryAIDroid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for empty Droid transcript, got %d", len(entries))
	}
}

// mustMarshal is a test helper that marshals v to JSON, failing the test on error.
func mustMarshal(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	return data
}

func TestResolveModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		model    string
		want     string
	}{
		{
			name:     "claude code with empty model defaults to DefaultModel",
			provider: string(agent.AgentNameClaudeCode),
			model:    "",
			want:     DefaultModel,
		},
		{
			name:     "other provider passes model through unchanged",
			provider: "codex",
			model:    "gpt-5",
			want:     "gpt-5",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveModel(types.AgentName(tt.provider), tt.model)
			if got != tt.want {
				t.Errorf("ResolveModel(%q, %q) = %q, want %q", tt.provider, tt.model, got, tt.want)
			}
		})
	}
}
