# Logging Architecture

## Overview

The CLI uses Go's `log/slog` package for structured JSON logging. All logs are written to a single file `.trace/logs/trace.log` and help debug hook execution and CLI behavior. When a session ID is known, the `session_id` attribute on each log line allows filtering by session.

## Log Levels

| Level | Purpose |
|-------|---------|
| DEBUG | Wrapper breadcrumbs (hook invoked/completed), detailed diagnostics |
| INFO | Handler logs with full context - the primary level for tracing |
| WARN | Unexpected conditions that don't block execution |
| ERROR | Failures that prevent operation completion |

## Configuration

```bash
# Environment variable (takes precedence)
export TRACE_LOG_LEVEL=debug

# Or in .trace/settings.json
{"log_level": "debug"}
```

## Tracing Model

Logs use a hierarchical tracing model inspired by OpenTelemetry concepts:

### Identifiers

| Field | Scope | Description |
|-------|-------|-------------|
| `session_id` | Root trace | Trace session ID, auto-added to all log entries |
| `tool_use_id` | Span | Unique ID for a subagent task lifecycle |
| `agent_id` | Span metadata | The subagent's ID (returned by Claude Code) |

### Hierarchy

```
session_id: 2025-12-31-abc123           ← root trace (all logs)
├── user-prompt-submit                   ← agent-level (no tool_use_id)
├── pre-task (tool_use_id: X)           ← span X starts
│   ├── post-todo (tool_use_id: X)      ← within span X
│   └── post-todo (tool_use_id: X)
├── post-task (tool_use_id: X)          ← span X ends
├── pre-task (tool_use_id: Y)           ← span Y starts
├── post-task (tool_use_id: Y)          ← span Y ends
└── stop                                 ← agent-level (no tool_use_id)
```

### Filtering Examples

```bash
# All logs for a session
jq 'select(.session_id == "2025-12-31-abc123")' .trace/logs/trace.log

# All logs for a specific subagent task
jq 'select(.tool_use_id == "X")' .trace/logs/trace.log

# All subagent activity
jq 'select(.hook_type == "subagent")' .trace/logs/trace.log

# Tail logs in real time
tail -f .trace/logs/trace.log | jq .
```

## Current Gaps

### Prompt-Level Correlation

Agent-level hooks (`user-prompt-submit`, `stop`) lack a correlation ID to tie them to tool activity that happens between them. Within a user prompt:

```
user-prompt-submit              ← no tool_use_id
  ├── Edit (tool_use_id: A)     ← not currently logged
  ├── Edit (tool_use_id: B)     ← not currently logged
  └── Bash (tool_use_id: C)     ← not currently logged
stop                            ← no tool_use_id
```

Future consideration: Generate a `prompt_id` at `user-prompt-submit` and persist for subsequent hooks.

### Tool Use Logging

Currently only Task and TodoWrite tool uses are hooked. Other tool uses (Edit, Write, Bash) are not logged. This is intentional for checkpoint purposes but limits observability.

## Privacy: No User Data in Logs

**Critical rule: Logs must not contain user data.**

### What NOT to log

- User prompts or prompt content
- Task descriptions (the `prompt` field passed to Task tool)
- File contents
- Command outputs
- Any data that could contain PII or sensitive information

### What IS safe to log

- Hook names and types
- Tool names (e.g., "Edit", "Task", "Bash")
- Subagent types (e.g., "general-purpose", "Explore")
- IDs (session_id, tool_use_id, agent_id, checkpoint_id)
- Paths (transcript_path, but not file contents)
- Timing (duration_ms)
- Success/failure status
- Strategy name
- File counts (modified_files, new_files, deleted_files)
- Branch names (shadow_branch)

### Example

```go
// GOOD - logs metadata only
logging.Info(ctx, "post-task",
    slog.String("hook", "post-task"),
    slog.String("tool_use_id", input.ToolUseID),
    slog.String("subagent_type", subagentType),  // e.g., "general-purpose"
)

// BAD - logs user content
logging.Info(ctx, "post-task",
    slog.String("task_description", taskDescription),  // Contains user prompt!
    slog.String("prompt", input.Prompt),               // User data!
)
```

## Components

Logs are tagged with a `component` field indicating the logging source:

| Component | Description |
|-----------|-------------|
| `hooks` | Hook execution (agent hooks, git hooks) |
| `checkpoint` | Checkpoint operations (saves, condensation, branch cleanup) |

## Implementation Details

### Files

| File | Purpose |
|------|---------|
| `cli/logging/logger.go` | Core logging infrastructure |
| `cli/logging/context.go` | Context helpers (WithComponent, WithSession) |
| `cli/hooks_git_cmd.go` | Git hook logging (uses gitHookContext helper) |
| `cli/hooks_claudecode_handlers.go` | Claude Code hook logging |
| `cli/hook_registry.go` | Hook wrapper logging |
| `cli/strategy/manual_commit_git.go` | Manual-commit checkpoint logging |
| `cli/strategy/manual_commit_hooks.go` | Condensation and branch cleanup logging |

### Log Entry Structure

**Hook log example:**
```json
{
  "time": "2025-12-31T12:27:52.853381+11:00",
  "level": "INFO",
  "msg": "post-task",
  "session_id": "2025-12-31-abc123",
  "component": "hooks",
  "hook": "post-task",
  "hook_type": "subagent",
  "tool_use_id": "toolu_abc123",
  "agent_id": "agent_xyz",
  "subagent_type": "general-purpose"
}
```

**Checkpoint log example:**
```json
{
  "time": "2025-12-31T12:28:00.123456+11:00",
  "level": "INFO",
  "msg": "checkpoint saved",
  "session_id": "2025-12-31-abc123",
  "component": "checkpoint",
  "strategy": "manual-commit",
  "checkpoint_type": "session",
  "checkpoint_count": 3,
  "modified_files": 2,
  "new_files": 1,
  "deleted_files": 0,
  "shadow_branch": "trace/a1b2c3d",
  "branch_created": false
}
```

**Task checkpoint log example:**
```json
{
  "time": "2025-12-31T12:28:30.789012+11:00",
  "level": "INFO",
  "msg": "task checkpoint saved",
  "session_id": "2025-12-31-abc123",
  "component": "checkpoint",
  "strategy": "manual-commit",
  "checkpoint_type": "task",
  "tool_use_id": "toolu_xyz789",
  "subagent_type": "general-purpose",
  "modified_files": 5,
  "new_files": 2,
  "deleted_files": 0,
  "shadow_branch": "trace/a1b2c3d",
  "branch_created": false
}
```

### Buffered I/O

Logs use an 8KB buffer (`bufio.Writer`) to batch writes and reduce syscall overhead. The buffer is flushed on `logging.Close()`.
