package agent

import (
	"time"

	"github.com/GrayCodeAI/trace/cli/agent/types"
)

// HookType represents agent lifecycle events
type HookType string

const (
	HookSessionStart     HookType = "session_start"
	HookSessionEnd       HookType = "session_end"
	HookUserPromptSubmit HookType = "user_prompt_submit"
	HookStop             HookType = "stop"
	HookPreToolUse       HookType = "pre_tool_use"
	HookPostToolUse      HookType = "post_tool_use"
)

// HookInput contains normalized data from hook callbacks
type HookInput struct {
	HookType  HookType
	SessionID string
	// SessionRef is an agent-specific session reference (file path, db key, etc.)
	SessionRef string
	Timestamp  time.Time

	// UserPrompt is the user's prompt text (from UserPromptSubmit hooks)
	UserPrompt string

	// Tool-specific fields (PreToolUse/PostToolUse)
	ToolName     string
	ToolUseID    string
	ToolInput    []byte // Raw JSON
	ToolResponse []byte // Raw JSON (PostToolUse only)

	// RawData preserves agent-specific data for extension
	RawData map[string]interface{}
}

// SessionChange represents detected session activity (for FileWatcher)
type SessionChange struct {
	SessionID  string
	SessionRef string
	EventType  HookType
	Timestamp  time.Time
}

// TokenUsage represents aggregated token usage for a checkpoint.
// This is agent-agnostic and can be populated by any agent that tracks token usage.
type TokenUsage = types.TokenUsage

// ProgressFn receives streaming progress updates. It must not block — invoke it
// from the same goroutine that reads the stream and keep handlers fast.
type ProgressFn func(progress GenerationProgress)

// ProgressPhase represents a progress phase.
type ProgressPhase string

const (
	// PhaseConnecting is emitted once when the CLI signals it is making the upstream request.
	PhaseConnecting ProgressPhase = "connecting"
	// PhaseFirstToken is emitted once when the upstream responds with the first event,
	// carrying TTFT and input/cache token counts.
	PhaseFirstToken ProgressPhase = "first-token"
	// PhaseGenerating is emitted repeatedly as text or thinking deltas arrive.
	// OutputTokens carries a running estimate based on delta sizes.
	PhaseGenerating ProgressPhase = "generating"
	// PhaseDone is emitted once when the final result event is received without error.
	PhaseDone ProgressPhase = "done"
)

// GenerationProgress reports a snapshot of streaming text generation progress.
// Fields not relevant to the current Phase may be zero-valued.
type GenerationProgress struct {
	Phase             ProgressPhase
	OutputTokens      int // running estimate during PhaseGenerating; final at PhaseDone
	InputTokens       int // populated at PhaseFirstToken
	CachedInputTokens int // populated at PhaseFirstToken
	TTFTms            int // time-to-first-token, populated at PhaseFirstToken
	DurationMs        int // populated at PhaseDone (final result event)
}
