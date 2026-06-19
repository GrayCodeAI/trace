package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/session"
	"github.com/GrayCodeAI/trace/cli/strategy"
)

func TestInfoCmd_JSONOutput(t *testing.T) {
	setupStopTestRepo(t)

	ctx := context.Background()

	state := makeSessionState("test-info-json", session.PhaseIdle)
	state.AgentType = testAgentClaude
	state.ModelName = "claude-opus-4-6[1m]"
	state.WorktreeID = "my-feature"
	state.StepCount = 2
	state.LastCheckpointID = testCheckpointID
	state.TokenUsage = &agent.TokenUsage{
		InputTokens:     100,
		CacheReadTokens: 5000,
		OutputTokens:    500,
	}
	state.LastPrompt = testPromptFixLogin
	state.FilesTouched = []string{"auth.go"}

	if err := strategy.SaveSessionState(ctx, state); err != nil {
		t.Fatalf("SaveSessionState() error = %v", err)
	}

	cmd := newInfoCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"test-info-json", "--json"})

	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("expected valid JSON, got parse error: %v\noutput: %s", err, stdout.String())
	}

	if result["session_id"] != "test-info-json" {
		t.Errorf("expected session_id 'test-info-json', got: %v", result["session_id"])
	}
	if result["agent"] != testAgentClaude {
		t.Errorf("expected agent %q, got: %v", testAgentClaude, result["agent"])
	}
	if result["status"] != "idle" {
		t.Errorf("expected status 'idle', got: %v", result["status"])
	}
	if result["last_checkpoint_id"] != testCheckpointID {
		t.Errorf("expected last_checkpoint_id %q, got: %v", testCheckpointID, result["last_checkpoint_id"])
	}

	tokens, ok := result["tokens"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected tokens object, got: %T", result["tokens"])
	}
	total, ok := tokens["total"].(float64)
	if !ok {
		t.Fatalf("expected total to be float64, got: %T", tokens["total"])
	}
	if total != 5600 {
		t.Errorf("expected total tokens 5600, got: %v", total)
	}
}

func TestInfoCmd_EndedSession(t *testing.T) {
	setupStopTestRepo(t)

	ctx := context.Background()
	endedAt := time.Now().Add(-24 * time.Hour)

	state := makeSessionState("test-info-ended", session.PhaseEnded)
	state.EndedAt = &endedAt
	state.AgentType = testAgentClaude
	state.StepCount = 1
	state.LastCheckpointID = "b79b35cd956d"

	if err := strategy.SaveSessionState(ctx, state); err != nil {
		t.Fatalf("SaveSessionState() error = %v", err)
	}

	cmd := newInfoCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"test-info-ended"})

	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Status:      ended") {
		t.Errorf("expected 'Status:      ended' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Ended:") {
		t.Errorf("expected 'Ended:' line in output, got:\n%s", out)
	}
	if !strings.Contains(out, "Checkpoint:  b79b35cd956d") {
		t.Errorf("expected checkpoint ID in output, got:\n%s", out)
	}
}

func TestInfoCmd_NotGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)
	paths.ClearWorktreeRootCache()
	session.ClearGitCommonDirCache()

	cmd := newSessionsCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"info", "some-id"})

	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected error for non-git directory, got nil")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("expected 'not a git repository' error, got: %v", err)
	}
}

// --- helper function tests ---

func TestSessionWorktreeLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		state    *strategy.SessionState
		expected string
	}{
		{
			name:     "uses WorktreeID when set",
			state:    &strategy.SessionState{WorktreeID: "my-feature", WorktreePath: "/some/path/my-feature"},
			expected: "my-feature",
		},
		{
			name:     "falls back to filepath.Base of WorktreePath",
			state:    &strategy.SessionState{WorktreePath: "/Users/dev/repo/.worktrees/feature-branch"},
			expected: "feature-branch",
		},
		{
			name:     "returns (unknown) when both empty",
			state:    &strategy.SessionState{},
			expected: unknownPlaceholder,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sessionWorktreeLabel(tt.state)
			if got != tt.expected {
				t.Errorf("sessionWorktreeLabel() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestSessionPhaseLabel(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name     string
		state    *strategy.SessionState
		expected string
	}{
		{
			name:     "active phase",
			state:    &strategy.SessionState{Phase: session.PhaseActive},
			expected: "active",
		},
		{
			name:     "idle phase",
			state:    &strategy.SessionState{Phase: session.PhaseIdle},
			expected: "idle",
		},
		{
			name:     "ended when EndedAt set",
			state:    &strategy.SessionState{Phase: session.PhaseIdle, EndedAt: &now},
			expected: "ended",
		},
		{
			name:     "empty phase defaults to idle",
			state:    &strategy.SessionState{},
			expected: "idle",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sessionPhaseLabel(tt.state)
			if got != tt.expected {
				t.Errorf("sessionPhaseLabel() = %q, want %q", got, tt.expected)
			}
		})
	}
}
