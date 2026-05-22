package codex

import (
	"context"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/agent"
	"github.com/stretchr/testify/require"
)

func TestParseHookEvent_SessionStart(t *testing.T) {
	t.Parallel()
	ag := &CodexAgent{}
	input := `{
		"session_id": "550e8400-e29b-41d4-a716-446655440000",
		"transcript_path": "/Users/test/.codex/rollouts/01/01/rollout-20260324-550e8400.jsonl",
		"cwd": "/tmp/testrepo",
		"hook_event_name": "SessionStart",
		"model": "gpt-4.1",
		"permission_mode": "default",
		"source": "startup"
	}`

	event, err := ag.ParseHookEvent(context.Background(), HookNameSessionStart, strings.NewReader(input))
	require.NoError(t, err)
	require.NotNil(t, event)
	require.Equal(t, agent.SessionStart, event.Type)
	require.Equal(t, "550e8400-e29b-41d4-a716-446655440000", event.SessionID)
	require.Equal(t, "/Users/test/.codex/rollouts/01/01/rollout-20260324-550e8400.jsonl", event.SessionRef)
	require.Equal(t, "gpt-4.1", event.Model)
}

func TestParseHookEvent_SessionStartNullTranscript(t *testing.T) {
	t.Parallel()
	ag := &CodexAgent{}
	input := `{
		"session_id": "test-uuid",
		"transcript_path": null,
		"cwd": "/tmp/testrepo",
		"hook_event_name": "SessionStart",
		"model": "gpt-4.1",
		"permission_mode": "default",
		"source": "startup"
	}`

	event, err := ag.ParseHookEvent(context.Background(), HookNameSessionStart, strings.NewReader(input))
	require.NoError(t, err)
	require.NotNil(t, event)
	require.Equal(t, agent.SessionStart, event.Type)
	require.Equal(t, "test-uuid", event.SessionID)
	require.Empty(t, event.SessionRef)
}

func TestParseHookEvent_UserPromptSubmit(t *testing.T) {
	t.Parallel()
	ag := &CodexAgent{}
	input := `{
		"session_id": "test-uuid",
		"turn_id": "turn-123",
		"transcript_path": "/tmp/rollout.jsonl",
		"cwd": "/tmp/testrepo",
		"hook_event_name": "UserPromptSubmit",
		"model": "gpt-4.1",
		"permission_mode": "default",
		"prompt": "Create a hello.txt file"
	}`

	event, err := ag.ParseHookEvent(context.Background(), HookNameUserPromptSubmit, strings.NewReader(input))
	require.NoError(t, err)
	require.NotNil(t, event)
	require.Equal(t, agent.TurnStart, event.Type)
	require.Equal(t, "test-uuid", event.SessionID)
	require.Equal(t, "/tmp/rollout.jsonl", event.SessionRef)
	require.Equal(t, "Create a hello.txt file", event.Prompt)
	require.Equal(t, "gpt-4.1", event.Model)
}

func TestParseHookEvent_Stop(t *testing.T) {
	t.Parallel()
	ag := &CodexAgent{}
	input := `{
		"session_id": "test-uuid",
		"turn_id": "turn-123",
		"transcript_path": "/tmp/rollout.jsonl",
		"cwd": "/tmp/testrepo",
		"hook_event_name": "Stop",
		"model": "gpt-4.1",
		"permission_mode": "default",
		"stop_hook_active": true,
		"last_assistant_message": "Done creating file."
	}`

	event, err := ag.ParseHookEvent(context.Background(), HookNameStop, strings.NewReader(input))
	require.NoError(t, err)
	require.NotNil(t, event)
	require.Equal(t, agent.TurnEnd, event.Type)
	require.Equal(t, "test-uuid", event.SessionID)
	require.Equal(t, "/tmp/rollout.jsonl", event.SessionRef)
	require.Equal(t, "gpt-4.1", event.Model)
}

func TestParseHookEvent_PreToolUse_ReturnsNil(t *testing.T) {
	t.Parallel()
	ag := &CodexAgent{}
	// PreToolUse is a pass-through — should return nil event
	event, err := ag.ParseHookEvent(context.Background(), HookNamePreToolUse, strings.NewReader("{}"))
	require.NoError(t, err)
	require.Nil(t, event)
}

func TestParseHookEvent_UnknownHook_ReturnsNil(t *testing.T) {
	t.Parallel()
	ag := &CodexAgent{}
	event, err := ag.ParseHookEvent(context.Background(), "unknown-hook", strings.NewReader("{}"))
	require.NoError(t, err)
	require.Nil(t, event)
}

func TestParseHookEvent_EmptyInput_ReturnsError(t *testing.T) {
	t.Parallel()
	ag := &CodexAgent{}
	_, err := ag.ParseHookEvent(context.Background(), HookNameSessionStart, strings.NewReader(""))
	require.Error(t, err)
}

func TestParseHookEvent_MalformedJSON_ReturnsError(t *testing.T) {
	t.Parallel()
	ag := &CodexAgent{}
	_, err := ag.ParseHookEvent(context.Background(), HookNameSessionStart, strings.NewReader("{invalid json"))
	require.Error(t, err)
}

func TestParseHookEvent_PostToolUse_ApplyPatch(t *testing.T) {
	t.Parallel()
	ag := &CodexAgent{}
	input := `{
		"session_id": "test-uuid",
		"turn_id": "turn-1",
		"transcript_path": null,
		"cwd": "/tmp/repo",
		"hook_event_name": "PostToolUse",
		"model": "gpt-5",
		"permission_mode": "default",
		"tool_name": "apply_patch",
		"tool_use_id": "call-patch",
		"tool_input": {"patch": "*** Add File: a.go\n+hello\n*** Update File: b.go\n@@\n-old\n+new\n*** Delete File: c.go\n*** End Patch\n"},
		"tool_response": "Patch applied successfully."
	}`

	event, err := ag.ParseHookEvent(context.Background(), HookNamePostToolUse, strings.NewReader(input))
	require.NoError(t, err)
	require.NotNil(t, event)
	require.Equal(t, agent.ToolUse, event.Type)
	require.Equal(t, "test-uuid", event.SessionID)
	require.Equal(t, "apply_patch", event.ToolName)
	require.Equal(t, []string{"a.go"}, event.NewFiles)
	require.Equal(t, []string{"b.go"}, event.ModifiedFiles)
	require.Equal(t, []string{"c.go"}, event.DeletedFiles)
}

func TestParseHookEvent_PostToolUse_NonApplyPatch_ReturnsNil(t *testing.T) {
	t.Parallel()
	ag := &CodexAgent{}
	input := `{
		"session_id": "test-uuid",
		"turn_id": "turn-1",
		"transcript_path": null,
		"cwd": "/tmp/repo",
		"hook_event_name": "PostToolUse",
		"model": "gpt-5",
		"permission_mode": "default",
		"tool_name": "shell",
		"tool_use_id": "call-shell",
		"tool_input": {"command": ["echo", "hi"]},
		"tool_response": "hi\n"
	}`

	event, err := ag.ParseHookEvent(context.Background(), HookNamePostToolUse, strings.NewReader(input))
	require.NoError(t, err)
	require.Nil(t, event)
}

func TestParseApplyPatchFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		patch       string
		wantAdded   []string
		wantUpdated []string
		wantDeleted []string
	}{
		{
			name: "all three operations",
			patch: "*** Begin Patch\n" +
				"*** Add File: docs/added.md\n" +
				"+# added\n" +
				"*** Update File: src/changed.go\n" +
				"@@\n" +
				"-old\n" +
				"+new\n" +
				"*** Delete File: tmp/gone.txt\n" +
				"*** End Patch\n",
			wantAdded:   []string{"docs/added.md"},
			wantUpdated: []string{"src/changed.go"},
			wantDeleted: []string{"tmp/gone.txt"},
		},
		{
			name:        "empty patch",
			patch:       "",
			wantAdded:   nil,
			wantUpdated: nil,
			wantDeleted: nil,
		},
		{
			name: "only adds",
			patch: "*** Add File: a.go\n" +
				"+line1\n" +
				"*** Add File: b.go\n" +
				"+line2\n",
			wantAdded:   []string{"a.go", "b.go"},
			wantUpdated: nil,
			wantDeleted: nil,
		},
		{
			name: "no markers",
			patch: "*** Begin Patch\n" +
				"@@\n" +
				"-old\n" +
				"+new\n" +
				"*** End Patch\n",
			wantAdded:   nil,
			wantUpdated: nil,
			wantDeleted: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			added, updated, deleted := parseApplyPatchFiles(tt.patch)
			require.Equal(t, tt.wantAdded, added)
			require.Equal(t, tt.wantUpdated, updated)
			require.Equal(t, tt.wantDeleted, deleted)
		})
	}
}
