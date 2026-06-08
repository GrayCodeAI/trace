package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
)

// Compile-time interface assertions.
var (
	_ agent.HookSupport        = (*CodexAgent)(nil)
	_ agent.HookResponseWriter = (*CodexAgent)(nil)
)

// WriteHookResponse outputs a JSON hook response to stdout.
// Codex reads the systemMessage field and displays it to the user.
func (c *CodexAgent) WriteHookResponse(message string) error {
	resp := struct {
		SystemMessage string `json:"systemMessage,omitempty"`
	}{SystemMessage: message}
	if err := json.NewEncoder(os.Stdout).Encode(resp); err != nil {
		return fmt.Errorf("failed to encode hook response: %w", err)
	}
	return nil
}

// Codex hook names — these become subcommands under `trace hooks codex`
const (
	HookNameSessionStart     = "session-start"
	HookNameUserPromptSubmit = "user-prompt-submit"
	HookNameStop             = "stop"
	HookNamePreToolUse       = "pre-tool-use"
	HookNamePostToolUse      = "post-tool-use"
)

// HookNames returns the hook verbs Codex supports.
func (c *CodexAgent) HookNames() []string {
	return []string{
		HookNameSessionStart,
		HookNameUserPromptSubmit,
		HookNameStop,
		HookNamePreToolUse,
		HookNamePostToolUse,
	}
}

// ParseHookEvent translates a Codex hook into a normalized lifecycle Event.
// Returns nil if the hook has no lifecycle significance.
func (c *CodexAgent) ParseHookEvent(_ context.Context, hookName string, stdin io.Reader) (*agent.Event, error) {
	switch hookName {
	case HookNameSessionStart:
		return c.parseSessionStart(stdin)
	case HookNameUserPromptSubmit:
		return c.parseTurnStart(stdin)
	case HookNameStop:
		return c.parseTurnEnd(stdin)
	case HookNamePreToolUse:
		// PreToolUse has no lifecycle significance — pass through
		return nil, nil //nolint:nilnil // nil event = no lifecycle action
	case HookNamePostToolUse:
		return c.parsePostToolUse(stdin)
	default:
		return nil, nil //nolint:nilnil // Unknown hooks have no lifecycle action
	}
}

func (c *CodexAgent) parseSessionStart(stdin io.Reader) (*agent.Event, error) {
	raw, err := agent.ReadAndParseHookInput[sessionStartRaw](stdin)
	if err != nil {
		return nil, err
	}
	return &agent.Event{
		Type:       agent.SessionStart,
		SessionID:  raw.SessionID,
		SessionRef: derefString(raw.TranscriptPath),
		Model:      raw.Model,
		Timestamp:  time.Now(),
	}, nil
}

func (c *CodexAgent) parseTurnStart(stdin io.Reader) (*agent.Event, error) {
	raw, err := agent.ReadAndParseHookInput[userPromptSubmitRaw](stdin)
	if err != nil {
		return nil, err
	}
	return &agent.Event{
		Type:       agent.TurnStart,
		SessionID:  raw.SessionID,
		SessionRef: derefString(raw.TranscriptPath),
		Prompt:     raw.Prompt,
		Model:      raw.Model,
		Timestamp:  time.Now(),
	}, nil
}

func (c *CodexAgent) parseTurnEnd(stdin io.Reader) (*agent.Event, error) {
	raw, err := agent.ReadAndParseHookInput[stopRaw](stdin)
	if err != nil {
		return nil, err
	}
	return &agent.Event{
		Type:       agent.TurnEnd,
		SessionID:  raw.SessionID,
		SessionRef: derefString(raw.TranscriptPath),
		Model:      raw.Model,
		Timestamp:  time.Now(),
	}, nil
}

func (c *CodexAgent) parsePostToolUse(stdin io.Reader) (*agent.Event, error) {
	raw, err := agent.ReadAndParseHookInput[postToolUseRaw](stdin)
	if err != nil {
		return nil, err
	}

	// Only apply_patch carries file changes worth tracking.
	if raw.ToolName != "apply_patch" {
		return nil, nil //nolint:nilnil // non-mutating tools have no lifecycle action
	}

	var input applyPatchInput
	if err := json.Unmarshal(raw.ToolInput, &input); err != nil {
		return nil, fmt.Errorf("failed to parse apply_patch input: %w", err)
	}

	added, updated, deleted := parseApplyPatchFiles(input.Patch)
	if len(added) == 0 && len(updated) == 0 && len(deleted) == 0 {
		return nil, nil //nolint:nilnil // empty patch has no lifecycle action
	}

	return &agent.Event{
		Type:          agent.ToolUse,
		SessionID:     raw.SessionID,
		SessionRef:    derefString(raw.TranscriptPath),
		ToolName:      raw.ToolName,
		ToolUseID:     raw.ToolUseID,
		ModifiedFiles: updated,
		NewFiles:      added,
		DeletedFiles:  deleted,
		Timestamp:     time.Now(),
	}, nil
}

// parseApplyPatchFiles extracts file paths from a Codex apply_patch envelope.
// The patch format uses markers:
//
//	*** Add File: path
//	*** Update File: path
//	*** Delete File: path
func parseApplyPatchFiles(patch string) (added, updated, deleted []string) {
	for line := range strings.SplitSeq(patch, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "*** Add File:"):
			if p := strings.TrimSpace(strings.TrimPrefix(line, "*** Add File:")); p != "" {
				added = append(added, p)
			}
		case strings.HasPrefix(line, "*** Update File:"):
			if p := strings.TrimSpace(strings.TrimPrefix(line, "*** Update File:")); p != "" {
				updated = append(updated, p)
			}
		case strings.HasPrefix(line, "*** Delete File:"):
			if p := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File:")); p != "" {
				deleted = append(deleted, p)
			}
		}
	}
	return added, updated, deleted
}
