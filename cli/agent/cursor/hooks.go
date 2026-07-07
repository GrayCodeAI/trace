package cursor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/jsonutil"
	"github.com/GrayCodeAI/trace/cli/paths"
)

// Ensure CursorAgent implements HookSupport
var (
	_ agent.HookSupport = (*CursorAgent)(nil)
)

// Cursor hook names - these become subcommands under `hawk trace hooks cursor`
const (
	HookNameSessionStart       = "session-start"
	HookNameSessionEnd         = "session-end"
	HookNameBeforeSubmitPrompt = "before-submit-prompt"
	HookNameStop               = "stop"
	HookNamePreCompact         = "pre-compact"
	HookNameSubagentStart      = "subagent-start"
	HookNameSubagentStop       = "subagent-stop"
)

// HooksFileName is the hooks file used by Cursor.
const HooksFileName = "hooks.json"

// traceHookPrefixes are command prefixes that identify Trace hooks.
// Both the current "hawk trace" forms and the legacy bare-"trace" / cmd/trace
// forms are listed so previously-installed hooks are still recognised for
// upgrade and removal.
var traceHookPrefixes = []string{
	"hawk trace ",
	`go run "$(git rev-parse --show-toplevel)"/cmd/hawk trace `,
	"trace ",
	`go run "$(git rev-parse --show-toplevel)"/cmd/trace/main.go `,
}

// HookNames returns the hook verbs Cursor supports.
// These become subcommands: trace hooks cursor <verb>
func (c *CursorAgent) HookNames() []string {
	return []string{
		HookNameSessionStart,
		HookNameSessionEnd,
		HookNameBeforeSubmitPrompt,
		HookNameStop,
		HookNamePreCompact,
		HookNameSubagentStart,
		HookNameSubagentStop,
	}
}

// InstallHooks installs Cursor hooks in .cursor/hooks.json.
// If force is true, removes existing Trace hooks before installing.
// Returns the number of hooks installed.
// Unknown top-level fields and hook types are preserved on round-trip.
func (c *CursorAgent) InstallHooks(ctx context.Context, localDev bool, force bool) (int, error) {
	worktreeRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		worktreeRoot = "."
	}

	hooksPath := filepath.Join(worktreeRoot, ".cursor", HooksFileName)

	// Use raw maps to preserve unknown fields on round-trip
	var rawFile map[string]json.RawMessage
	var rawHooks map[string]json.RawMessage

	// #nosec G304 -- hooksPath is constructed from repo root + fixed path, not external input
	existingData, readErr := os.ReadFile(hooksPath) //nolint:gosec // path is constructed from repo root + fixed path
	if readErr == nil {
		if err := json.Unmarshal(existingData, &rawFile); err != nil {
			return 0, fmt.Errorf("failed to parse existing "+HooksFileName+": %w", err)
		}
		if hooksRaw, ok := rawFile["hooks"]; ok {
			if err := json.Unmarshal(hooksRaw, &rawHooks); err != nil {
				return 0, fmt.Errorf("failed to parse hooks in "+HooksFileName+": %w", err)
			}
		}
		if _, ok := rawFile["version"]; !ok {
			rawFile["version"] = json.RawMessage(`1`)
		}
	} else {
		rawFile = map[string]json.RawMessage{
			"version": json.RawMessage(`1`),
		}
	}

	if rawHooks == nil {
		rawHooks = make(map[string]json.RawMessage)
	}

	// Parse only the hook types we manage
	var sessionStart, sessionEnd, beforeSubmitPrompt, stop, preCompact, subagentStart, subagentStop []CursorHookEntry
	parseCursorHookType(rawHooks, "sessionStart", &sessionStart)
	parseCursorHookType(rawHooks, "sessionEnd", &sessionEnd)
	parseCursorHookType(rawHooks, "beforeSubmitPrompt", &beforeSubmitPrompt)
	parseCursorHookType(rawHooks, "stop", &stop)
	parseCursorHookType(rawHooks, "preCompact", &preCompact)
	parseCursorHookType(rawHooks, "subagentStart", &subagentStart)
	parseCursorHookType(rawHooks, "subagentStop", &subagentStop)

	// If force is true, remove all existing Trace hooks first
	if force {
		sessionStart = removeTraceHooks(sessionStart)
		sessionEnd = removeTraceHooks(sessionEnd)
		beforeSubmitPrompt = removeTraceHooks(beforeSubmitPrompt)
		stop = removeTraceHooks(stop)
		preCompact = removeTraceHooks(preCompact)
		subagentStart = removeTraceHooks(subagentStart)
		subagentStop = removeTraceHooks(subagentStop)
	}

	// Define hook commands
	var cmdPrefix string
	if localDev {
		cmdPrefix = `go run "$(git rev-parse --show-toplevel)"/cmd/hawk trace hooks cursor `
	} else {
		cmdPrefix = "hawk trace hooks cursor "
	}

	sessionStartCmd := cmdPrefix + HookNameSessionStart
	sessionEndCmd := cmdPrefix + HookNameSessionEnd
	beforeSubmitPromptCmd := cmdPrefix + HookNameBeforeSubmitPrompt
	stopCmd := cmdPrefix + HookNameStop
	preCompactCmd := cmdPrefix + HookNamePreCompact
	subagentStartCmd := cmdPrefix + HookNameSubagentStart
	subagentEndCmd := cmdPrefix + HookNameSubagentStop
	if !localDev {
		sessionStartCmd = agent.WrapProductionSilentHookCommand(sessionStartCmd)
		sessionEndCmd = agent.WrapProductionSilentHookCommand(sessionEndCmd)
		beforeSubmitPromptCmd = agent.WrapProductionSilentHookCommand(beforeSubmitPromptCmd)
		stopCmd = agent.WrapProductionSilentHookCommand(stopCmd)
		preCompactCmd = agent.WrapProductionSilentHookCommand(preCompactCmd)
		subagentStartCmd = agent.WrapProductionSilentHookCommand(subagentStartCmd)
		subagentEndCmd = agent.WrapProductionSilentHookCommand(subagentEndCmd)
	}

	count := 0

	// Add hooks if they don't exist
	if !hookCommandExists(sessionStart, sessionStartCmd) {
		sessionStart = append(sessionStart, CursorHookEntry{Command: sessionStartCmd})
		count++
	}
	if !hookCommandExists(sessionEnd, sessionEndCmd) {
		sessionEnd = append(sessionEnd, CursorHookEntry{Command: sessionEndCmd})
		count++
	}
	if !hookCommandExists(beforeSubmitPrompt, beforeSubmitPromptCmd) {
		beforeSubmitPrompt = append(beforeSubmitPrompt, CursorHookEntry{Command: beforeSubmitPromptCmd})
		count++
	}
	if !hookCommandExists(stop, stopCmd) {
		stop = append(stop, CursorHookEntry{Command: stopCmd})
		count++
	}
	if !hookCommandExists(preCompact, preCompactCmd) {
		preCompact = append(preCompact, CursorHookEntry{Command: preCompactCmd})
		count++
	}
	if !hookCommandExists(subagentStart, subagentStartCmd) {
		subagentStart = append(subagentStart, CursorHookEntry{Command: subagentStartCmd})
		count++
	}
	if !hookCommandExists(subagentStop, subagentEndCmd) {
		subagentStop = append(subagentStop, CursorHookEntry{Command: subagentEndCmd})
		count++
	}

	if count == 0 {
		return 0, nil
	}

	// Marshal modified hook types back into rawHooks
	marshalCursorHookType(rawHooks, "sessionStart", sessionStart)
	marshalCursorHookType(rawHooks, "sessionEnd", sessionEnd)
	marshalCursorHookType(rawHooks, "beforeSubmitPrompt", beforeSubmitPrompt)
	marshalCursorHookType(rawHooks, "stop", stop)
	marshalCursorHookType(rawHooks, "preCompact", preCompact)
	marshalCursorHookType(rawHooks, "subagentStart", subagentStart)
	marshalCursorHookType(rawHooks, "subagentStop", subagentStop)

	// Marshal hooks and update raw file
	hooksJSON, err := jsonutil.MarshalWithNoHTMLEscape(rawHooks)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal hooks: %w", err)
	}
	rawFile["hooks"] = hooksJSON

	// Write to file
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o750); err != nil {
		return 0, fmt.Errorf("failed to create .cursor directory: %w", err)
	}

	output, err := jsonutil.MarshalIndentWithNewline(rawFile, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("failed to marshal "+HooksFileName+": %w", err)
	}

	if err := os.WriteFile(hooksPath, output, 0o600); err != nil {
		return 0, fmt.Errorf("failed to write "+HooksFileName+": %w", err)
	}

	return count, nil
}

// UninstallHooks removes Trace hooks from Cursor HooksFileName.
// Unknown top-level fields and hook types are preserved on round-trip.
func (c *CursorAgent) UninstallHooks(ctx context.Context) error {
	worktreeRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		worktreeRoot = "."
	}
	hooksPath := filepath.Join(worktreeRoot, ".cursor", HooksFileName)
	// #nosec G304 -- hooksPath is constructed from repo root + fixed path, not external input
	data, err := os.ReadFile(hooksPath) //nolint:gosec // path is constructed from repo root + fixed path
	if err != nil {
		return nil //nolint:nilerr // No hooks file means nothing to uninstall
	}

	var rawFile map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawFile); err != nil {
		return fmt.Errorf("failed to parse "+HooksFileName+": %w", err)
	}

	var rawHooks map[string]json.RawMessage
	if hooksRaw, ok := rawFile["hooks"]; ok {
		if err := json.Unmarshal(hooksRaw, &rawHooks); err != nil {
			return fmt.Errorf("failed to parse hooks in "+HooksFileName+": %w", err)
		}
	}
	if rawHooks == nil {
		rawHooks = make(map[string]json.RawMessage)
	}

	// Parse only the hook types we manage
	var sessionStart, sessionEnd, beforeSubmitPrompt, stop, preCompact, subagentStart, subagentStop []CursorHookEntry
	parseCursorHookType(rawHooks, "sessionStart", &sessionStart)
	parseCursorHookType(rawHooks, "sessionEnd", &sessionEnd)
	parseCursorHookType(rawHooks, "beforeSubmitPrompt", &beforeSubmitPrompt)
	parseCursorHookType(rawHooks, "stop", &stop)
	parseCursorHookType(rawHooks, "preCompact", &preCompact)
	parseCursorHookType(rawHooks, "subagentStart", &subagentStart)
	parseCursorHookType(rawHooks, "subagentStop", &subagentStop)

	// Remove Trace hooks from all hook types
	sessionStart = removeTraceHooks(sessionStart)
	sessionEnd = removeTraceHooks(sessionEnd)
	beforeSubmitPrompt = removeTraceHooks(beforeSubmitPrompt)
	stop = removeTraceHooks(stop)
	preCompact = removeTraceHooks(preCompact)
	subagentStart = removeTraceHooks(subagentStart)
	subagentStop = removeTraceHooks(subagentStop)

	// Marshal modified hook types back into rawHooks
	marshalCursorHookType(rawHooks, "sessionStart", sessionStart)
	marshalCursorHookType(rawHooks, "sessionEnd", sessionEnd)
	marshalCursorHookType(rawHooks, "beforeSubmitPrompt", beforeSubmitPrompt)
	marshalCursorHookType(rawHooks, "stop", stop)
	marshalCursorHookType(rawHooks, "preCompact", preCompact)
	marshalCursorHookType(rawHooks, "subagentStart", subagentStart)
	marshalCursorHookType(rawHooks, "subagentStop", subagentStop)

	// Marshal hooks back (preserving unknown hook types)
	if len(rawHooks) > 0 {
		hooksJSON, err := jsonutil.MarshalWithNoHTMLEscape(rawHooks)
		if err != nil {
			return fmt.Errorf("failed to marshal hooks: %w", err)
		}
		rawFile["hooks"] = hooksJSON
	} else {
		delete(rawFile, "hooks")
	}

	// Write back
	output, err := jsonutil.MarshalIndentWithNewline(rawFile, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal "+HooksFileName+": %w", err)
	}

	if err := os.WriteFile(hooksPath, output, 0o600); err != nil {
		return fmt.Errorf("failed to write "+HooksFileName+": %w", err)
	}

	return nil
}

// AreHooksInstalled checks if Trace hooks are installed.
func (c *CursorAgent) AreHooksInstalled(ctx context.Context) bool {
	worktreeRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		worktreeRoot = "."
	}
	hooksPath := filepath.Join(worktreeRoot, ".cursor", HooksFileName)
	// #nosec G304 -- hooksPath is constructed from repo root + fixed path, not external input
	data, err := os.ReadFile(hooksPath) //nolint:gosec // path is constructed from repo root + fixed path
	if err != nil {
		return false
	}

	var hooksFile CursorHooksFile
	if err := json.Unmarshal(data, &hooksFile); err != nil {
		return false
	}

	return hasTraceHook(hooksFile.Hooks.SessionStart) ||
		hasTraceHook(hooksFile.Hooks.SessionEnd) ||
		hasTraceHook(hooksFile.Hooks.BeforeSubmitPrompt) ||
		hasTraceHook(hooksFile.Hooks.Stop) ||
		hasTraceHook(hooksFile.Hooks.PreCompact) ||
		hasTraceHook(hooksFile.Hooks.SubagentStart) ||
		hasTraceHook(hooksFile.Hooks.SubagentStop)
}

// GetSupportedHooks returns the hook types Cursor supports.
func (c *CursorAgent) GetSupportedHooks() []agent.HookType {
	return []agent.HookType{
		agent.HookSessionStart,
		agent.HookSessionEnd,
		agent.HookUserPromptSubmit,
		agent.HookStop,
		agent.HookPreToolUse,
		agent.HookPostToolUse,
	}
}

// parseCursorHookType parses a specific hook type from rawHooks into the target slice.
// Silently ignores parse errors (leaves target unchanged).
func parseCursorHookType(rawHooks map[string]json.RawMessage, hookType string, target *[]CursorHookEntry) {
	if data, ok := rawHooks[hookType]; ok {
		//nolint:errcheck,gosec // Intentionally ignoring parse errors - leave target as nil/empty
		json.Unmarshal(data, target) // #nosec G104 -- intentionally ignoring parse errors, leave target as nil/empty
	}
}

// marshalCursorHookType marshals a hook type back into rawHooks.
// If the slice is empty, removes the key from rawHooks.
func marshalCursorHookType(rawHooks map[string]json.RawMessage, hookType string, entries []CursorHookEntry) {
	if len(entries) == 0 {
		delete(rawHooks, hookType)
		return
	}
	data, err := jsonutil.MarshalWithNoHTMLEscape(entries)
	if err != nil {
		return // Silently ignore marshal errors (shouldn't happen)
	}
	rawHooks[hookType] = data
}

// Helper functions for hook management

func hookCommandExists(entries []CursorHookEntry, command string) bool {
	for _, entry := range entries {
		if entry.Command == command {
			return true
		}
	}
	return false
}

func isTraceHook(command string) bool {
	return agent.IsManagedHookCommand(command, traceHookPrefixes)
}

func hasTraceHook(entries []CursorHookEntry) bool {
	for _, entry := range entries {
		if isTraceHook(entry.Command) {
			return true
		}
	}
	return false
}

func removeTraceHooks(entries []CursorHookEntry) []CursorHookEntry {
	result := make([]CursorHookEntry, 0, len(entries))
	for _, entry := range entries {
		if !isTraceHook(entry.Command) {
			result = append(result, entry)
		}
	}
	return result
}
