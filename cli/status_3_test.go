package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/session"
)

func TestWriteActiveSessions_NotStaleWhenRecent(t *testing.T) {
	setupTestRepo(t)

	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatalf("NewStateStore() error = %v", err)
	}

	now := time.Now()
	recentInteraction := now.Add(-5 * time.Minute)

	states := []*session.State{
		{
			SessionID:           "fresh-session-1",
			WorktreePath:        "/Users/test/repo",
			StartedAt:           now.Add(-30 * time.Minute),
			LastInteractionTime: &recentInteraction,
			Phase:               session.PhaseActive,
			LastPrompt:          "add feature",
			AgentType:           "Claude Code",
		},
	}

	for _, s := range states {
		if err := store.Save(context.Background(), s); err != nil {
			t.Fatalf("Save() error = %v", err)
		}
	}

	var buf bytes.Buffer
	sty := newStatusStyles(&buf)
	writeActiveSessions(context.Background(), &buf, sty)

	output := buf.String()

	if strings.Contains(output, "stale") {
		t.Errorf("Session with recent interaction should NOT show stale indicator, got: %s", output)
	}
}

func TestFormatSettingsStatus_Separators(t *testing.T) {
	t.Parallel()

	sty := statusStyles{colorEnabled: false, width: 60}
	s := &TraceSettings{
		Enabled:  true,
		Strategy: "manual-commit",
	}

	result := formatSettingsStatus("Project", s, sty)

	// Should use · as separator (plain text, no ANSI)
	if !strings.Contains(result, "·") {
		t.Errorf("Expected '·' separators in output, got: %q", result)
	}
}

func TestRunStatusJSON_Enabled(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)
	writeClaudeHooksFixture(t)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, false, true); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	var result statusJSON
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if !result.Enabled {
		t.Error("Expected enabled=true")
	}
	found := false
	for _, a := range result.Agents {
		if a == "Claude Code" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected agents to contain 'Claude Code', got %v", result.Agents)
	}
	if result.Error != "" {
		t.Errorf("Expected no error, got %q", result.Error)
	}
}

func TestRunStatusJSON_Disabled(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsDisabled)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, false, true); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	var result statusJSON
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if result.Enabled {
		t.Error("Expected enabled=false")
	}
}

func TestRunStatusJSON_NotSetUp(t *testing.T) {
	setupTestRepo(t)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, false, true); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	var result statusJSON
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if result.Enabled {
		t.Error("Expected enabled=false")
	}
	if result.Error != "not set up" {
		t.Errorf("Expected error='not set up', got %q", result.Error)
	}
}

func TestRunStatusJSON_NotGitRepo(t *testing.T) {
	setupTestDir(t)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, false, true); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	var result statusJSON
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if result.Enabled {
		t.Error("Expected enabled=false")
	}
	if result.Error != "not a git repository" {
		t.Errorf("Expected error='not a git repository', got %q", result.Error)
	}
}

func TestRunStatusJSON_WithActiveSessions(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)
	writeClaudeHooksFixture(t)

	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatalf("NewStateStore() error = %v", err)
	}

	state := &session.State{
		SessionID:    "test-json-session",
		WorktreePath: "/test/repo",
		StartedAt:    time.Now(),
		Phase:        session.PhaseActive,
		AgentType:    "Claude Code",
		ModelName:    "sonnet-4.1",
	}
	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, false, true); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	var result statusJSON
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if len(result.ActiveSessions) != 1 {
		t.Fatalf("Expected 1 active session, got %d", len(result.ActiveSessions))
	}
	s := result.ActiveSessions[0]
	if s.Agent != "Claude Code" {
		t.Errorf("Expected agent='Claude Code', got %q", s.Agent)
	}
	if s.Model != "sonnet-4.1" {
		t.Errorf("Expected model='sonnet-4.1', got %q", s.Model)
	}
	if s.Status != "active" {
		t.Errorf("Expected status='active', got %q", s.Status)
	}
}

func TestRunStatusJSON_DeduplicatesSessions(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)

	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatalf("NewStateStore() error = %v", err)
	}

	now := time.Now()
	states := []*session.State{
		{
			SessionID:    "codex-idle-1",
			WorktreePath: "/test/repo",
			StartedAt:    now.Add(-30 * time.Minute),
			Phase:        session.PhaseIdle,
			AgentType:    "Codex",
		},
		{
			SessionID:    "codex-idle-2",
			WorktreePath: "/test/repo",
			StartedAt:    now.Add(-20 * time.Minute),
			Phase:        session.PhaseIdle,
			AgentType:    "Codex",
		},
		{
			SessionID:    "codex-active",
			WorktreePath: "/test/repo",
			StartedAt:    now.Add(-5 * time.Minute),
			Phase:        session.PhaseActive,
			AgentType:    "Codex",
			ModelName:    "codex-mini",
		},
	}
	for _, s := range states {
		if err := store.Save(context.Background(), s); err != nil {
			t.Fatalf("Save() error = %v", err)
		}
	}

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, false, true); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	var result statusJSON
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if len(result.ActiveSessions) != 1 {
		t.Fatalf("Expected 1 deduplicated session, got %d", len(result.ActiveSessions))
	}
	s := result.ActiveSessions[0]
	if s.Agent != "Codex" {
		t.Errorf("Expected agent='Codex', got %q", s.Agent)
	}
	if s.Status != "active" {
		t.Errorf("Expected status='active' (active wins over idle), got %q", s.Status)
	}
	if s.Model != "codex-mini" {
		t.Errorf("Expected model='codex-mini' from active session, got %q", s.Model)
	}
}
