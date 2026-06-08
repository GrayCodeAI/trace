package logging

import (
	"context"
	"testing"
)

// testComponent and testAgent are defined in logger_test.go

func TestWithSession(t *testing.T) {
	ctx := context.Background()
	sessionID := "2025-01-15-test-session"

	ctx = WithSession(ctx, sessionID)

	got := SessionIDFromContext(ctx)
	if got != sessionID {
		t.Errorf("SessionIDFromContext() = %q, want %q", got, sessionID)
	}
}

func TestWithSession_SetsParentFromExisting(t *testing.T) {
	ctx := context.Background()
	parentSessionID := "2025-01-15-parent-session"
	childSessionID := "2025-01-15-child-session"

	// Set parent session
	ctx = WithSession(ctx, parentSessionID)

	// Set child session - should automatically set parent
	ctx = WithSession(ctx, childSessionID)

	gotSession := SessionIDFromContext(ctx)
	gotParent := ParentSessionIDFromContext(ctx)

	if gotSession != childSessionID {
		t.Errorf("SessionIDFromContext() = %q, want %q", gotSession, childSessionID)
	}
	if gotParent != parentSessionID {
		t.Errorf("ParentSessionIDFromContext() = %q, want %q", gotParent, parentSessionID)
	}
}

func TestWithParentSession(t *testing.T) {
	ctx := context.Background()
	parentSessionID := "2025-01-15-explicit-parent"

	ctx = WithParentSession(ctx, parentSessionID)

	got := ParentSessionIDFromContext(ctx)
	if got != parentSessionID {
		t.Errorf("ParentSessionIDFromContext() = %q, want %q", got, parentSessionID)
	}
}

func TestWithToolCall(t *testing.T) {
	ctx := context.Background()
	toolCallID := "toolu_01ABC123XYZ"

	ctx = WithToolCall(ctx, toolCallID)

	got := ToolCallIDFromContext(ctx)
	if got != toolCallID {
		t.Errorf("ToolCallIDFromContext() = %q, want %q", got, toolCallID)
	}
}

func TestWithComponent(t *testing.T) {
	ctx := context.Background()

	ctx = WithComponent(ctx, testComponent)

	got := ComponentFromContext(ctx)
	if got != testComponent {
		t.Errorf("ComponentFromContext() = %q, want %q", got, testComponent)
	}
}

func TestWithAgent(t *testing.T) {
	ctx := context.Background()

	ctx = WithAgent(ctx, testAgent)

	got := AgentFromContext(ctx)
	if got != testAgent {
		t.Errorf("AgentFromContext() = %q, want %q", got, testAgent)
	}
}

func TestContextValues_Empty(t *testing.T) {
	ctx := context.Background()

	// All should return empty strings for unset context
	if got := SessionIDFromContext(ctx); got != "" {
		t.Errorf("SessionIDFromContext() on empty = %q, want empty", got)
	}
	if got := ParentSessionIDFromContext(ctx); got != "" {
		t.Errorf("ParentSessionIDFromContext() on empty = %q, want empty", got)
	}
	if got := ToolCallIDFromContext(ctx); got != "" {
		t.Errorf("ToolCallIDFromContext() on empty = %q, want empty", got)
	}
	if got := ComponentFromContext(ctx); got != "" {
		t.Errorf("ComponentFromContext() on empty = %q, want empty", got)
	}
	if got := AgentFromContext(ctx); got != "" {
		t.Errorf("AgentFromContext() on empty = %q, want empty", got)
	}
}

func TestContextValues_Chaining(t *testing.T) {
	ctx := context.Background()

	// Chain multiple values
	ctx = WithSession(ctx, "session-1")
	ctx = WithToolCall(ctx, "tool-1")
	ctx = WithComponent(ctx, testComponent)
	ctx = WithAgent(ctx, testAgent)

	// All values should be preserved
	if got := SessionIDFromContext(ctx); got != "session-1" {
		t.Errorf("SessionIDFromContext() = %q, want 'session-1'", got)
	}
	if got := ToolCallIDFromContext(ctx); got != "tool-1" {
		t.Errorf("ToolCallIDFromContext() = %q, want 'tool-1'", got)
	}
	if got := ComponentFromContext(ctx); got != testComponent {
		t.Errorf("ComponentFromContext() = %q, want %q", got, testComponent)
	}
	if got := AgentFromContext(ctx); got != testAgent {
		t.Errorf("AgentFromContext() = %q, want %q", got, testAgent)
	}
}

func TestAttrsFromContext(t *testing.T) {
	ctx := context.Background()
	ctx = WithSession(ctx, "session-123")
	ctx = WithParentSession(ctx, "parent-456")
	ctx = WithToolCall(ctx, "tool-789")
	ctx = WithComponent(ctx, testComponent)
	ctx = WithAgent(ctx, testAgent)

	// Pass empty string for globalSessionID to include context session_id
	attrs := attrsFromContext(ctx, "")

	// Should have 5 attrs
	if len(attrs) != 5 {
		t.Errorf("attrsFromContext() returned %d attrs, want 5", len(attrs))
	}

	// Verify attr values
	attrMap := make(map[string]string)
	for _, attr := range attrs {
		attrMap[attr.Key] = attr.Value.String()
	}

	if attrMap["session_id"] != "session-123" {
		t.Errorf("session_id = %q, want 'session-123'", attrMap["session_id"])
	}
	if attrMap["parent_session_id"] != "parent-456" {
		t.Errorf("parent_session_id = %q, want 'parent-456'", attrMap["parent_session_id"])
	}
	if attrMap["tool_call_id"] != "tool-789" {
		t.Errorf("tool_call_id = %q, want 'tool-789'", attrMap["tool_call_id"])
	}
	if attrMap["component"] != testComponent {
		t.Errorf("component = %q, want %q", attrMap["component"], testComponent)
	}
	if attrMap["agent"] != testAgent {
		t.Errorf("agent = %q, want %q", attrMap["agent"], testAgent)
	}
}

func TestAttrsFromContext_Partial(t *testing.T) {
	ctx := context.Background()
	ctx = WithSession(ctx, "session-only")

	// Pass empty string for globalSessionID to include context session_id
	attrs := attrsFromContext(ctx, "")

	// Should only have 1 attr (session_id) since others are empty
	if len(attrs) != 1 {
		t.Errorf("attrsFromContext() returned %d attrs, want 1", len(attrs))
	}

	if attrs[0].Key != "session_id" || attrs[0].Value.String() != "session-only" {
		t.Errorf("Expected session_id='session-only', got %s=%s", attrs[0].Key, attrs[0].Value.String())
	}
}

func TestAttrsFromContext_SkipsSessionWhenGlobalSet(t *testing.T) {
	ctx := context.Background()
	ctx = WithSession(ctx, "context-session")
	ctx = WithToolCall(ctx, "tool-123")

	// Pass a global session ID - context session_id should be skipped
	attrs := attrsFromContext(ctx, "global-session")

	// Should only have 1 attr (tool_call_id) since session_id is skipped
	if len(attrs) != 1 {
		t.Errorf("attrsFromContext() returned %d attrs, want 1 (session_id should be skipped)", len(attrs))
	}

	if attrs[0].Key != "tool_call_id" || attrs[0].Value.String() != "tool-123" {
		t.Errorf("Expected tool_call_id='tool-123', got %s=%s", attrs[0].Key, attrs[0].Value.String())
	}
}
