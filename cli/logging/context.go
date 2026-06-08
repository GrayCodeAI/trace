package logging

import (
	"context"

	"github.com/GrayCodeAI/trace/cli/agent/types"
)

// Context keys for logging values.
// Using private types to avoid key collisions.
type contextKey int

const (
	sessionIDKey contextKey = iota
	parentSessionIDKey
	toolCallIDKey
	componentKey
	agentKey
)

// WithSession adds a session ID to the context.
// If the context already has a session ID, it becomes the parent session ID.
func WithSession(ctx context.Context, sessionID string) context.Context {
	// If there's an existing session, it becomes the parent
	existing := SessionIDFromContext(ctx)
	if existing != "" && existing != sessionID {
		ctx = context.WithValue(ctx, parentSessionIDKey, existing)
	}
	return context.WithValue(ctx, sessionIDKey, sessionID)
}

// WithParentSession explicitly sets the parent session ID.
// Use this when you need to set the parent explicitly rather than
// having it inferred from an existing session.
func WithParentSession(ctx context.Context, parentSessionID string) context.Context {
	return context.WithValue(ctx, parentSessionIDKey, parentSessionID)
}

// WithToolCall adds a tool call ID to the context.
func WithToolCall(ctx context.Context, toolCallID string) context.Context {
	return context.WithValue(ctx, toolCallIDKey, toolCallID)
}

// WithComponent adds a component name to the context.
// Component names help identify the subsystem generating logs (e.g., "hooks", "strategy", "session").
func WithComponent(ctx context.Context, component string) context.Context {
	return context.WithValue(ctx, componentKey, component)
}

// WithAgent adds an agent name to the context.
// Agent names identify the AI agent generating activity (e.g., "claude-code", "cursor", "aider").
func WithAgent(ctx context.Context, agentName types.AgentName) context.Context {
	return context.WithValue(ctx, agentKey, string(agentName))
}

// SessionIDFromContext extracts the session ID from the context.
// Returns empty string if not set.
func SessionIDFromContext(ctx context.Context) string {
	if v := ctx.Value(sessionIDKey); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ParentSessionIDFromContext extracts the parent session ID from the context.
// Returns empty string if not set.
func ParentSessionIDFromContext(ctx context.Context) string {
	if v := ctx.Value(parentSessionIDKey); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ToolCallIDFromContext extracts the tool call ID from the context.
// Returns empty string if not set.
func ToolCallIDFromContext(ctx context.Context) string {
	if v := ctx.Value(toolCallIDKey); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ComponentFromContext extracts the component name from the context.
// Returns empty string if not set.
func ComponentFromContext(ctx context.Context) string {
	if v := ctx.Value(componentKey); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// AgentFromContext extracts the agent name from the context.
// Returns empty string if not set.
func AgentFromContext(ctx context.Context) string {
	if v := ctx.Value(agentKey); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
