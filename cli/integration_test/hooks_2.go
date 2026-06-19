//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/GrayCodeAI/trace/cli/testutil"
)

// SimulateGeminiBeforeAgentWithOutput is a convenience method on TestEnv.
func (env *TestEnv) SimulateGeminiBeforeAgentWithOutput(sessionID string) HookOutput {
	env.T.Helper()
	runner := NewGeminiHookRunner(env.RepoDir, env.GeminiProjectDir, env.T)
	return runner.SimulateGeminiBeforeAgentWithOutput(sessionID)
}

// SimulateGeminiAfterAgent is a convenience method on TestEnv.
func (env *TestEnv) SimulateGeminiAfterAgent(sessionID, transcriptPath string) error {
	env.T.Helper()
	runner := NewGeminiHookRunner(env.RepoDir, env.GeminiProjectDir, env.T)
	return runner.SimulateGeminiAfterAgent(sessionID, transcriptPath)
}

// SimulateGeminiSessionEnd is a convenience method on TestEnv.
func (env *TestEnv) SimulateGeminiSessionEnd(sessionID, transcriptPath string) error {
	env.T.Helper()
	runner := NewGeminiHookRunner(env.RepoDir, env.GeminiProjectDir, env.T)
	return runner.SimulateGeminiSessionEnd(sessionID, transcriptPath)
}

// --- Factory AI Droid Hook Runner ---

// FactoryDroidHookRunner executes Factory AI Droid hooks in the test environment.
type FactoryDroidHookRunner struct {
	RepoDir string
	T       interface {
		Helper()
		Fatalf(format string, args ...interface{})
		Logf(format string, args ...interface{})
	}
}

// NewFactoryDroidHookRunner creates a new Factory Droid hook runner.
func NewFactoryDroidHookRunner(repoDir string, t interface {
	Helper()
	Fatalf(format string, args ...interface{})
	Logf(format string, args ...interface{})
},
) *FactoryDroidHookRunner {
	return &FactoryDroidHookRunner{
		RepoDir: repoDir,
		T:       t,
	}
}

// runDroidHookWithInput runs a Factory Droid hook with the given input.
func (r *FactoryDroidHookRunner) runDroidHookWithInput(hookName string, input interface{}) error {
	r.T.Helper()

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("failed to marshal hook input: %w", err)
	}

	return r.runDroidHookInRepoDir(hookName, inputJSON)
}

func (r *FactoryDroidHookRunner) runDroidHookInRepoDir(hookName string, inputJSON []byte) error {
	cmd := exec.Command(getTestBinary(), "hooks", "factoryai-droid", hookName)
	cmd.Dir = r.RepoDir
	cmd.Stdin = bytes.NewReader(inputJSON)
	cmd.Env = os.Environ()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("hook %s failed: %w\nInput: %s\nOutput: %s",
			hookName, err, inputJSON, output)
	}

	r.T.Logf("Droid hook %s output: %s", hookName, output)
	return nil
}

// runDroidHookWithOutput runs a Factory Droid hook and returns both stdout and stderr separately.
func (r *FactoryDroidHookRunner) runDroidHookWithOutput(hookName string, inputJSON []byte) HookOutput {
	cmd := exec.Command(getTestBinary(), "hooks", "factoryai-droid", hookName)
	cmd.Dir = r.RepoDir
	cmd.Stdin = bytes.NewReader(inputJSON)
	cmd.Env = os.Environ()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return HookOutput{
		Stdout: stdout.Bytes(),
		Stderr: stderr.Bytes(),
		Err:    err,
	}
}

// SimulateUserPromptSubmit simulates the UserPromptSubmit hook for Factory Droid.
func (r *FactoryDroidHookRunner) SimulateUserPromptSubmit(sessionID string) error {
	r.T.Helper()

	input := map[string]string{
		"session_id":      sessionID,
		"transcript_path": "",
		"prompt":          "test prompt",
	}

	return r.runDroidHookWithInput("user-prompt-submit", input)
}

// SimulateUserPromptSubmitWithOutput simulates the UserPromptSubmit hook and returns the output.
func (r *FactoryDroidHookRunner) SimulateUserPromptSubmitWithOutput(sessionID string) HookOutput {
	r.T.Helper()

	input := map[string]string{
		"session_id":      sessionID,
		"transcript_path": "",
		"prompt":          "test prompt",
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return HookOutput{Err: fmt.Errorf("failed to marshal hook input: %w", err)}
	}

	return r.runDroidHookWithOutput("user-prompt-submit", inputJSON)
}

// SimulateStop simulates the Stop hook for Factory Droid.
func (r *FactoryDroidHookRunner) SimulateStop(sessionID, transcriptPath string) error {
	r.T.Helper()

	input := map[string]string{
		"session_id":      sessionID,
		"transcript_path": transcriptPath,
	}

	return r.runDroidHookWithInput("stop", input)
}

// SimulateSessionStart simulates the SessionStart hook for Factory Droid.
func (r *FactoryDroidHookRunner) SimulateSessionStart(sessionID string) error {
	r.T.Helper()

	input := map[string]string{
		"session_id":      sessionID,
		"transcript_path": "",
	}

	return r.runDroidHookWithInput("session-start", input)
}

// SimulateSessionStartWithOutput simulates the SessionStart hook and returns the output.
func (r *FactoryDroidHookRunner) SimulateSessionStartWithOutput(sessionID string) HookOutput {
	r.T.Helper()

	input := map[string]string{
		"session_id":      sessionID,
		"transcript_path": "",
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return HookOutput{Err: fmt.Errorf("failed to marshal hook input: %w", err)}
	}

	return r.runDroidHookWithOutput("session-start", inputJSON)
}

// SimulateSessionEnd simulates the SessionEnd hook for Factory Droid.
func (r *FactoryDroidHookRunner) SimulateSessionEnd(sessionID, transcriptPath string) error {
	r.T.Helper()

	input := map[string]string{
		"session_id":      sessionID,
		"transcript_path": transcriptPath,
	}

	return r.runDroidHookWithInput("session-end", input)
}

// SimulatePreTask simulates the PreToolUse[Task] hook for Factory Droid.
func (r *FactoryDroidHookRunner) SimulatePreTask(sessionID, transcriptPath, toolUseID string) error {
	r.T.Helper()

	input := map[string]interface{}{
		"session_id":      sessionID,
		"transcript_path": transcriptPath,
		"tool_use_id":     toolUseID,
		"tool_input": map[string]string{
			"subagent_type": "general-purpose",
			"description":   "test task",
		},
	}

	return r.runDroidHookWithInput("pre-tool-use", input)
}

// SimulatePostTask simulates the PostToolUse[Task] hook for Factory Droid.
func (r *FactoryDroidHookRunner) SimulatePostTask(input PostTaskInput) error {
	r.T.Helper()

	hookInput := map[string]interface{}{
		"session_id":      input.SessionID,
		"transcript_path": input.TranscriptPath,
		"tool_use_id":     input.ToolUseID,
		"tool_input":      map[string]string{},
		"tool_response": map[string]string{
			"agentId": input.AgentID,
		},
	}

	return r.runDroidHookWithInput("post-tool-use", hookInput)
}

// FactoryDroidSession represents a simulated Factory AI Droid session.
type FactoryDroidSession struct {
	ID             string
	TranscriptPath string
	env            *TestEnv
}

// NewFactoryDroidSession creates a new simulated Factory Droid session.
func (env *TestEnv) NewFactoryDroidSession() *FactoryDroidSession {
	env.T.Helper()

	env.SessionCounter++
	sessionID := fmt.Sprintf("droid-session-%d", env.SessionCounter)
	transcriptPath := filepath.Join(env.RepoDir, ".trace", "tmp", sessionID+".jsonl")

	return &FactoryDroidSession{
		ID:             sessionID,
		TranscriptPath: transcriptPath,
		env:            env,
	}
}

// CreateDroidTranscript creates a Droid-envelope JSONL transcript file.
// Droid wraps messages as {"type":"message","id":"...","message":{"role":"...","content":[...]}},
// unlike Claude Code which uses {"type":"assistant","uuid":"...","message":{"content":[...]}}.
func (s *FactoryDroidSession) CreateDroidTranscript(prompt string, changes []FileChange) string {
	var lines []map[string]interface{}

	// User message with prompt
	lines = append(lines, map[string]interface{}{
		"type": "message",
		"id":   "m1",
		"message": map[string]interface{}{
			"role": "user",
			"content": []map[string]interface{}{
				{"type": "text", "text": prompt},
			},
		},
	})

	// Assistant message with tool uses
	assistantContent := []interface{}{
		map[string]interface{}{"type": "text", "text": "I'll help you with that."},
	}
	for i, change := range changes {
		assistantContent = append(assistantContent, map[string]interface{}{
			"type":  "tool_use",
			"id":    fmt.Sprintf("toolu_%d", i+1),
			"name":  "Write",
			"input": map[string]string{"file_path": change.Path, "content": change.Content},
		})
	}
	lines = append(lines, map[string]interface{}{
		"type": "message",
		"id":   "m2",
		"message": map[string]interface{}{
			"role":    "assistant",
			"content": assistantContent,
		},
	})

	// Tool results
	toolResultContent := make([]map[string]interface{}, 0, len(changes))
	for i := range changes {
		toolResultContent = append(toolResultContent, map[string]interface{}{
			"type":        "tool_result",
			"tool_use_id": fmt.Sprintf("toolu_%d", i+1),
			"content":     "Success",
		})
	}
	lines = append(lines, map[string]interface{}{
		"type": "message",
		"id":   "m3",
		"message": map[string]interface{}{
			"role":    "user",
			"content": toolResultContent,
		},
	})

	// Final assistant message
	lines = append(lines, map[string]interface{}{
		"type": "message",
		"id":   "m4",
		"message": map[string]interface{}{
			"role": "assistant",
			"content": []map[string]interface{}{
				{"type": "text", "text": "Done!"},
			},
		},
	})

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(s.TranscriptPath), 0o755); err != nil {
		s.env.T.Fatalf("failed to create transcript dir: %v", err)
	}

	// Write as JSONL
	file, err := os.Create(s.TranscriptPath)
	if err != nil {
		s.env.T.Fatalf("failed to create transcript file: %v", err)
	}
	defer func() { _ = file.Close() }()

	encoder := json.NewEncoder(file)
	for _, line := range lines {
		if err := encoder.Encode(line); err != nil {
			s.env.T.Fatalf("failed to encode transcript line: %v", err)
		}
	}

	return s.TranscriptPath
}

// SimulateFactoryDroidUserPromptSubmit is a convenience method on TestEnv.
func (env *TestEnv) SimulateFactoryDroidUserPromptSubmit(sessionID string) error {
	env.T.Helper()
	runner := NewFactoryDroidHookRunner(env.RepoDir, env.T)
	return runner.SimulateUserPromptSubmit(sessionID)
}

// SimulateFactoryDroidUserPromptSubmitWithOutput is a convenience method on TestEnv.
func (env *TestEnv) SimulateFactoryDroidUserPromptSubmitWithOutput(sessionID string) HookOutput {
	env.T.Helper()
	runner := NewFactoryDroidHookRunner(env.RepoDir, env.T)
	return runner.SimulateUserPromptSubmitWithOutput(sessionID)
}

// SimulateFactoryDroidStop is a convenience method on TestEnv.
func (env *TestEnv) SimulateFactoryDroidStop(sessionID, transcriptPath string) error {
	env.T.Helper()
	runner := NewFactoryDroidHookRunner(env.RepoDir, env.T)
	return runner.SimulateStop(sessionID, transcriptPath)
}

// SimulateFactoryDroidSessionStart is a convenience method on TestEnv.
func (env *TestEnv) SimulateFactoryDroidSessionStart(sessionID string) error {
	env.T.Helper()
	runner := NewFactoryDroidHookRunner(env.RepoDir, env.T)
	return runner.SimulateSessionStart(sessionID)
}

// SimulateFactoryDroidSessionStartWithOutput is a convenience method on TestEnv.
func (env *TestEnv) SimulateFactoryDroidSessionStartWithOutput(sessionID string) HookOutput {
	env.T.Helper()
	runner := NewFactoryDroidHookRunner(env.RepoDir, env.T)
	return runner.SimulateSessionStartWithOutput(sessionID)
}

// SimulateFactoryDroidSessionEnd is a convenience method on TestEnv.
func (env *TestEnv) SimulateFactoryDroidSessionEnd(sessionID, transcriptPath string) error {
	env.T.Helper()
	runner := NewFactoryDroidHookRunner(env.RepoDir, env.T)
	return runner.SimulateSessionEnd(sessionID, transcriptPath)
}

// SimulateFactoryDroidPreTask is a convenience method on TestEnv.
func (env *TestEnv) SimulateFactoryDroidPreTask(sessionID, transcriptPath, toolUseID string) error {
	env.T.Helper()
	runner := NewFactoryDroidHookRunner(env.RepoDir, env.T)
	return runner.SimulatePreTask(sessionID, transcriptPath, toolUseID)
}

// SimulateFactoryDroidPostTask is a convenience method on TestEnv.
func (env *TestEnv) SimulateFactoryDroidPostTask(input PostTaskInput) error {
	env.T.Helper()
	runner := NewFactoryDroidHookRunner(env.RepoDir, env.T)
	return runner.SimulatePostTask(input)
}

// --- OpenCode Hook Runner ---

// OpenCodeHookRunner executes OpenCode hooks in the test environment.
type OpenCodeHookRunner struct {
	RepoDir            string
	OpenCodeProjectDir string
	T                  interface {
		Helper()
		Fatalf(format string, args ...interface{})
		Logf(format string, args ...interface{})
	}
}

// NewOpenCodeHookRunner creates a new OpenCode hook runner for the given repo directory.
func NewOpenCodeHookRunner(repoDir, openCodeProjectDir string, t interface {
	Helper()
	Fatalf(format string, args ...interface{})
	Logf(format string, args ...interface{})
},
) *OpenCodeHookRunner {
	return &OpenCodeHookRunner{
		RepoDir:            repoDir,
		OpenCodeProjectDir: openCodeProjectDir,
		T:                  t,
	}
}

func (r *OpenCodeHookRunner) runOpenCodeHookWithInput(hookName string, input interface{}) error {
	r.T.Helper()

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("failed to marshal hook input: %w", err)
	}

	return r.runOpenCodeHookInRepoDir(hookName, inputJSON)
}

func (r *OpenCodeHookRunner) runOpenCodeHookInRepoDir(hookName string, inputJSON []byte) error {
	// Command structure: trace hooks opencode <hook-name>
	cmd := exec.Command(getTestBinary(), "hooks", "opencode", hookName)
	cmd.Dir = r.RepoDir
	cmd.Stdin = bytes.NewReader(inputJSON)
	cmd.Env = append(
		testutil.GitIsolatedEnv(),
		"TRACE_TEST_OPENCODE_PROJECT_DIR="+r.OpenCodeProjectDir,
		"TRACE_TEST_OPENCODE_MOCK_EXPORT=1", // Use pre-written mock transcript instead of calling opencode export
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("hook %s failed: %w\nInput: %s\nOutput: %s",
			hookName, err, inputJSON, output)
	}

	r.T.Logf("OpenCode hook %s output: %s", hookName, output)
	return nil
}

// SimulateOpenCodeSessionStart simulates the session-start hook for OpenCode.
// Note: The plugin now sends only session_id, not transcript_path.
func (r *OpenCodeHookRunner) SimulateOpenCodeSessionStart(sessionID, _ string) error {
	r.T.Helper()

	input := map[string]string{
		"session_id": sessionID,
	}

	return r.runOpenCodeHookWithInput("session-start", input)
}

// SimulateOpenCodeTurnStart simulates the turn-start hook for OpenCode.
// This is equivalent to Claude Code's UserPromptSubmit.
// Note: The plugin now sends only session_id and prompt, not transcript_path.
func (r *OpenCodeHookRunner) SimulateOpenCodeTurnStart(sessionID, _, prompt string) error {
	r.T.Helper()

	input := map[string]string{
		"session_id": sessionID,
		"prompt":     prompt,
	}

	return r.runOpenCodeHookWithInput("turn-start", input)
}

// SimulateOpenCodeTurnEnd simulates the turn-end hook for OpenCode.
// This is equivalent to Claude Code's Stop hook.
// Note: The plugin now sends only session_id. The Go handler calls `opencode export`
// to get the transcript. For tests, we write a mock export JSON file first.
func (r *OpenCodeHookRunner) SimulateOpenCodeTurnEnd(sessionID, transcriptPath string) error {
	r.T.Helper()

	// For integration tests, write the mock transcript to the location where the
	// lifecycle handler expects it (.trace/tmp/<session_id>.json)
	if transcriptPath != "" {
		srcData, err := os.ReadFile(transcriptPath)
		if err != nil {
			r.T.Fatalf("SimulateOpenCodeTurnEnd: failed to read transcript from %q: %v", transcriptPath, err)
		}
		destDir := filepath.Join(r.RepoDir, ".trace", "tmp")
		if err := os.MkdirAll(destDir, 0o755); err != nil {
			r.T.Fatalf("SimulateOpenCodeTurnEnd: failed to create directory %q: %v", destDir, err)
		}
		destPath := filepath.Join(destDir, sessionID+".json")
		if err := os.WriteFile(destPath, srcData, 0o644); err != nil {
			r.T.Fatalf("SimulateOpenCodeTurnEnd: failed to write transcript to %q: %v", destPath, err)
		}
	}

	input := map[string]string{
		"session_id": sessionID,
	}

	return r.runOpenCodeHookWithInput("turn-end", input)
}

// SimulateOpenCodeSessionEnd simulates the session-end hook for OpenCode.
// Note: The plugin now sends only session_id, not transcript_path.
func (r *OpenCodeHookRunner) SimulateOpenCodeSessionEnd(sessionID, _ string) error {
	r.T.Helper()

	input := map[string]string{
		"session_id": sessionID,
	}

	return r.runOpenCodeHookWithInput("session-end", input)
}

// OpenCodeSession represents a simulated OpenCode session.
type OpenCodeSession struct {
	ID             string // Raw session ID (e.g., "opencode-session-1")
	TranscriptPath string
	env            *TestEnv
	msgCounter     int
	// messages accumulates all messages across turns, matching real `opencode export`
	// behavior where each export returns the full session history.
	messages []map[string]interface{}
}

// NewOpenCodeSession creates a new simulated OpenCode session.
func (env *TestEnv) NewOpenCodeSession() *OpenCodeSession {
	env.T.Helper()

	env.SessionCounter++
	sessionID := fmt.Sprintf("opencode-session-%d", env.SessionCounter)
	transcriptPath := filepath.Join(env.OpenCodeProjectDir, sessionID+".json")

	return &OpenCodeSession{
		ID:             sessionID,
		TranscriptPath: transcriptPath,
		env:            env,
	}
}

// CreateOpenCodeTranscript creates an OpenCode export JSON transcript file for the session.
// Each call appends new messages to the accumulated session history, matching real
// `opencode export` behavior where each export returns the full session history.
func (s *OpenCodeSession) CreateOpenCodeTranscript(prompt string, changes []FileChange) string {
	// User message
	s.msgCounter++
	s.messages = append(s.messages, map[string]interface{}{
		"info": map[string]interface{}{
			"id":   fmt.Sprintf("msg-%d", s.msgCounter),
			"role": "user",
			"time": map[string]interface{}{"created": 1708300000 + s.msgCounter},
		},
		"parts": []map[string]interface{}{
			{"type": "text", "text": prompt},
		},
	})

	// Assistant message with tool calls for file changes
	s.msgCounter++
	var parts []map[string]interface{}
	parts = append(parts, map[string]interface{}{
		"type": "text",
		"text": "I'll help you with that.",
	})
	for i, change := range changes {
		parts = append(parts, map[string]interface{}{
			"type":   "tool",
			"tool":   "write",
			"callID": fmt.Sprintf("call-%d", i+1),
			"state": map[string]interface{}{
				"status": "completed",
				"input":  map[string]string{"filePath": change.Path},
				"output": "File written: " + change.Path,
			},
		})
	}
	parts = append(parts, map[string]interface{}{
		"type": "text",
		"text": "Done!",
	})

	s.messages = append(s.messages, map[string]interface{}{
		"info": map[string]interface{}{
			"id":   fmt.Sprintf("msg-%d", s.msgCounter),
			"role": "assistant",
			"time": map[string]interface{}{
				"created":   1708300000 + s.msgCounter,
				"completed": 1708300000 + s.msgCounter + 5,
			},
			"tokens": map[string]interface{}{
				"input":     150,
				"output":    80,
				"reasoning": 10,
				"cache":     map[string]int{"read": 5, "write": 15},
			},
			"cost": 0.003,
		},
		"parts": parts,
	})

	// Build export session format with accumulated messages
	exportSession := map[string]interface{}{
		"info": map[string]interface{}{
			"id": s.ID,
		},
		"messages": s.messages,
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(s.TranscriptPath), 0o755); err != nil {
		s.env.T.Fatalf("failed to create transcript dir: %v", err)
	}

	// Write export JSON transcript
	data, err := json.MarshalIndent(exportSession, "", "  ")
	if err != nil {
		s.env.T.Fatalf("failed to marshal transcript: %v", err)
	}
	if err := os.WriteFile(s.TranscriptPath, data, 0o644); err != nil {
		s.env.T.Fatalf("failed to write transcript: %v", err)
	}

	return s.TranscriptPath
}

// SimulateOpenCodeSessionStart is a convenience method on TestEnv.
func (env *TestEnv) SimulateOpenCodeSessionStart(sessionID, transcriptPath string) error {
	env.T.Helper()
	runner := NewOpenCodeHookRunner(env.RepoDir, env.OpenCodeProjectDir, env.T)
	return runner.SimulateOpenCodeSessionStart(sessionID, transcriptPath)
}

// SimulateOpenCodeTurnStart is a convenience method on TestEnv.
func (env *TestEnv) SimulateOpenCodeTurnStart(sessionID, transcriptPath, prompt string) error {
	env.T.Helper()
	runner := NewOpenCodeHookRunner(env.RepoDir, env.OpenCodeProjectDir, env.T)
	return runner.SimulateOpenCodeTurnStart(sessionID, transcriptPath, prompt)
}

// SimulateOpenCodeTurnEnd is a convenience method on TestEnv.
func (env *TestEnv) SimulateOpenCodeTurnEnd(sessionID, transcriptPath string) error {
	env.T.Helper()
	runner := NewOpenCodeHookRunner(env.RepoDir, env.OpenCodeProjectDir, env.T)
	return runner.SimulateOpenCodeTurnEnd(sessionID, transcriptPath)
}

// SimulateOpenCodeSessionEnd is a convenience method on TestEnv.
func (env *TestEnv) SimulateOpenCodeSessionEnd(sessionID, transcriptPath string) error {
	env.T.Helper()
	runner := NewOpenCodeHookRunner(env.RepoDir, env.OpenCodeProjectDir, env.T)
	return runner.SimulateOpenCodeSessionEnd(sessionID, transcriptPath)
}

// CopyTranscriptToTraceTmp copies an OpenCode transcript to .trace/tmp/<sessionID>.json.
// This simulates what `opencode export` does in production. Required for mid-turn commits
// where PrepareTranscript calls fetchAndCacheExport, which in mock mode expects the file
// to already exist at .trace/tmp/<sessionID>.json.
func (env *TestEnv) CopyTranscriptToTraceTmp(sessionID, transcriptPath string) {
	env.T.Helper()

	srcData, err := os.ReadFile(transcriptPath)
	if err != nil {
		env.T.Fatalf("CopyTranscriptToTraceTmp: failed to read transcript from %q: %v", transcriptPath, err)
	}
	destDir := filepath.Join(env.RepoDir, ".trace", "tmp")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		env.T.Fatalf("CopyTranscriptToTraceTmp: failed to create directory %q: %v", destDir, err)
	}
	destPath := filepath.Join(destDir, sessionID+".json")
	if err := os.WriteFile(destPath, srcData, 0o644); err != nil {
		env.T.Fatalf("CopyTranscriptToTraceTmp: failed to write transcript to %q: %v", destPath, err)
	}
}

// CodexHookRunner executes Codex CLI hooks in the test environment.
type CodexHookRunner struct {
	RepoDir string
	T       interface {
		Helper()
		Fatalf(format string, args ...interface{})
		Logf(format string, args ...interface{})
	}
}

// NewCodexHookRunner creates a hook runner for Codex hooks in the given repo.
func NewCodexHookRunner(repoDir string, t interface {
	Helper()
	Fatalf(format string, args ...interface{})
	Logf(format string, args ...interface{})
},
) *CodexHookRunner {
	return &CodexHookRunner{
		RepoDir: repoDir,
		T:       t,
	}
}

// runCodexHook runs a Codex hook by name with the given JSON input via stdin.
func (r *CodexHookRunner) runCodexHook(hookName string, input interface{}) error {
	r.T.Helper()

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("failed to marshal hook input: %w", err)
	}

	cmd := exec.Command(getTestBinary(), "hooks", "codex", hookName)
	cmd.Dir = r.RepoDir
	cmd.Stdin = bytes.NewReader(inputJSON)
	cmd.Env = testutil.GitIsolatedEnv()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("codex hook %s failed: %w\nInput: %s\nOutput: %s",
			hookName, err, inputJSON, output)
	}

	r.T.Logf("Codex hook %s output: %s", hookName, output)
	return nil
}

// SimulateCodexPostToolUseApplyPatch simulates a Codex PostToolUse hook
// for an apply_patch tool invocation. The patch string is wrapped in the
// Codex tool_input envelope before being dispatched.
func (r *CodexHookRunner) SimulateCodexPostToolUseApplyPatch(sessionID, cwd, patch string) error {
	r.T.Helper()

	input := map[string]any{
		"session_id":      sessionID,
		"turn_id":         "t1",
		"transcript_path": nil,
		"cwd":             cwd,
		"hook_event_name": "PostToolUse",
		"model":           "gpt-5",
		"permission_mode": "default",
		"tool_name":       "apply_patch",
		"tool_use_id":     "call-patch",
		"tool_input":      map[string]any{"patch": patch},
		"tool_response":   "Patch applied successfully.",
	}

	return r.runCodexHook("post-tool-use", input)
}
