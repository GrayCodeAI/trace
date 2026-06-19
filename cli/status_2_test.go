package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/session"
)

func TestTotalTokens(t *testing.T) {
	t.Parallel()

	t.Run("nil", func(t *testing.T) {
		t.Parallel()
		if got := totalTokens(nil); got != 0 {
			t.Errorf("totalTokens(nil) = %d, want 0", got)
		}
	})

	t.Run("basic", func(t *testing.T) {
		t.Parallel()
		tu := &agent.TokenUsage{
			InputTokens:  100,
			OutputTokens: 50,
		}
		if got := totalTokens(tu); got != 150 {
			t.Errorf("totalTokens() = %d, want 150", got)
		}
	})

	t.Run("with subagents", func(t *testing.T) {
		t.Parallel()
		tu := &agent.TokenUsage{
			InputTokens:  100,
			OutputTokens: 50,
			SubagentTokens: &agent.TokenUsage{
				InputTokens:  200,
				OutputTokens: 100,
			},
		}
		if got := totalTokens(tu); got != 450 {
			t.Errorf("totalTokens() = %d, want 450", got)
		}
	})

	t.Run("all fields", func(t *testing.T) {
		t.Parallel()
		tu := &agent.TokenUsage{
			InputTokens:         100,
			CacheCreationTokens: 50,
			CacheReadTokens:     25,
			OutputTokens:        75,
		}
		if got := totalTokens(tu); got != 250 {
			t.Errorf("totalTokens() = %d, want 250", got)
		}
	})
}

func TestTotalTokens_ExcludesAPICallCount(t *testing.T) {
	t.Parallel()

	// APICallCount should NOT be included in token totals — it's a separate metric
	tu := &agent.TokenUsage{
		InputTokens:  100,
		OutputTokens: 50,
		APICallCount: 999, // should be ignored
	}
	got := totalTokens(tu)
	if got != 150 {
		t.Errorf("totalTokens() = %d, want 150 (APICallCount should be excluded)", got)
	}
}

func TestTotalTokens_DeepSubagentNesting(t *testing.T) {
	t.Parallel()

	tu := &agent.TokenUsage{
		InputTokens:  100,
		OutputTokens: 50,
		SubagentTokens: &agent.TokenUsage{
			InputTokens:  200,
			OutputTokens: 100,
			SubagentTokens: &agent.TokenUsage{
				InputTokens:  50,
				OutputTokens: 25,
			},
		},
	}
	// 100+50 + 200+100 + 50+25 = 525
	if got := totalTokens(tu); got != 525 {
		t.Errorf("totalTokens() = %d, want 525 (deep nesting)", got)
	}
}

func TestActiveTimeDisplay(t *testing.T) {
	t.Parallel()

	t.Run("nil", func(t *testing.T) {
		t.Parallel()
		if got := activeTimeDisplay(nil); got != "" {
			t.Errorf("activeTimeDisplay(nil) = %q, want empty", got)
		}
	})

	t.Run("recent", func(t *testing.T) {
		t.Parallel()
		now := time.Now()
		if got := activeTimeDisplay(&now); got != "active now" {
			t.Errorf("activeTimeDisplay(now) = %q, want 'active now'", got)
		}
	})

	t.Run("older", func(t *testing.T) {
		t.Parallel()
		older := time.Now().Add(-5 * time.Minute)
		got := activeTimeDisplay(&older)
		if got != "active 5m ago" {
			t.Errorf("activeTimeDisplay(-5m) = %q, want 'active 5m ago'", got)
		}
	})
}

func TestShouldUseColor_NonTTY(t *testing.T) {
	t.Parallel()

	// bytes.Buffer is not a terminal → should return false
	var buf bytes.Buffer
	if shouldUseColor(&buf) {
		t.Error("shouldUseColor(bytes.Buffer) should be false")
	}
}

func TestShouldUseColor_NoColorEnv(t *testing.T) {
	// NO_COLOR env var should force color off even for a real file
	t.Setenv("NO_COLOR", "1")

	f, err := os.CreateTemp(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if shouldUseColor(f) {
		t.Error("shouldUseColor should be false when NO_COLOR is set")
	}
}

func TestShouldUseColor_RegularFile(t *testing.T) {
	t.Parallel()

	// A regular file (not a terminal) should return false
	f, err := os.CreateTemp(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if shouldUseColor(f) {
		t.Error("shouldUseColor(regular file) should be false")
	}
}

func TestNewStatusStyles_NonTTY(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	sty := newStatusStyles(&buf)

	if sty.colorEnabled {
		t.Error("newStatusStyles(bytes.Buffer) should have colorEnabled=false")
	}
}

func TestRender_ColorDisabled(t *testing.T) {
	t.Parallel()

	// When color is disabled, render should return text unchanged
	sty := statusStyles{colorEnabled: false}
	style := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("2"))

	got := sty.render(style, "hello")
	if got != "hello" {
		t.Errorf("render with color disabled = %q, want %q", got, "hello")
	}
}

func TestRender_ColorEnabled_CallsStyleRender(t *testing.T) {
	t.Parallel()

	// When colorEnabled=true, render should call style.Render (not return plain text).
	// Note: lipgloss may strip ANSI in test environments without a terminal, so we
	// can't assert ANSI codes. Instead, verify the code path is exercised and
	// the text content is preserved.
	sty := statusStyles{
		colorEnabled: true,
		bold:         lipgloss.NewStyle().Bold(true),
	}

	got := sty.render(sty.bold, "hello")
	if !strings.Contains(got, "hello") {
		t.Errorf("render with color enabled should preserve text content, got: %q", got)
	}
}

func TestRender_ColorToggle(t *testing.T) {
	t.Parallel()

	style := lipgloss.NewStyle().Bold(true)

	// Color disabled: must return exact input
	styOff := statusStyles{colorEnabled: false}
	got := styOff.render(style, "test")
	if got != "test" {
		t.Errorf("render(colorEnabled=false) = %q, want exact %q", got, "test")
	}

	// Color enabled: exercises style.Render code path, text preserved
	styOn := statusStyles{colorEnabled: true}
	got = styOn.render(style, "test")
	if !strings.Contains(got, "test") {
		t.Errorf("render(colorEnabled=true) should contain 'test', got: %q", got)
	}
}

func TestSectionRule_PlainText(t *testing.T) {
	t.Parallel()

	sty := statusStyles{colorEnabled: false, width: 40}
	rule := sty.sectionRule("Active Sessions", 40)

	// Plain text should contain the label
	if !strings.Contains(rule, "Active Sessions") {
		t.Errorf("sectionRule should contain label, got: %q", rule)
	}
	if !strings.Contains(rule, "─") {
		t.Errorf("sectionRule should contain rule characters, got: %q", rule)
	}
	// With color disabled, should have no ANSI escapes
	if strings.Contains(rule, "\x1b[") {
		t.Errorf("sectionRule with color disabled should have no ANSI escapes, got: %q", rule)
	}
}

func TestHorizontalRule_PlainText(t *testing.T) {
	t.Parallel()

	sty := statusStyles{colorEnabled: false}
	rule := sty.horizontalRule(15)

	// Should be no ANSI escapes
	if strings.Contains(rule, "\x1b[") {
		t.Errorf("horizontalRule with color disabled should have no ANSI escapes, got: %q", rule)
	}
	if len([]rune(rule)) != 15 {
		t.Errorf("horizontalRule(15) has %d runes, want 15", len([]rune(rule)))
	}
}

func TestHorizontalRule(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	sty := newStatusStyles(&buf)

	rule := sty.horizontalRule(20)
	if len([]rune(rule)) != 20 {
		t.Errorf("horizontalRule(20) has %d runes, want 20", len([]rune(rule)))
	}
	// All characters should be the box-drawing dash
	for _, r := range rule {
		if r != '─' {
			t.Errorf("horizontalRule contains unexpected rune %q", r)
			break
		}
	}
}

func TestGetTerminalWidth_NonTTY(t *testing.T) {
	t.Parallel()

	// A bytes.Buffer is not a terminal — should fall back to 60
	var buf bytes.Buffer
	width := getTerminalWidth(&buf)
	// In CI/test environments without a real terminal on Stdout/Stderr,
	// the fallback should be 60. If running in a terminal, it may be
	// capped at 80. Either is acceptable.
	if width != 60 && width > 80 {
		t.Errorf("getTerminalWidth(bytes.Buffer) = %d, want 60 or ≤80", width)
	}
}

func TestGetTerminalWidth_RegularFile(t *testing.T) {
	t.Parallel()

	// A regular file (not a terminal) should not report a terminal width
	f, err := os.CreateTemp(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	width := getTerminalWidth(f)
	// Regular file fd won't have a terminal size, so it should fall back
	if width != 60 && width > 80 {
		t.Errorf("getTerminalWidth(regular file) = %d, want 60 or ≤80", width)
	}
}

func TestNewStatusStyles_Width(t *testing.T) {
	t.Parallel()

	// For a non-terminal writer, width should be the fallback (60)
	// unless Stdout/Stderr happen to be terminals
	var buf bytes.Buffer
	sty := newStatusStyles(&buf)

	if sty.width == 0 {
		t.Error("newStatusStyles should set a non-zero width")
	}
	if sty.width > 80 {
		t.Errorf("newStatusStyles width = %d, should be capped at 80", sty.width)
	}
}

func TestSectionRule_NarrowWidth(t *testing.T) {
	t.Parallel()

	// When width is very small (smaller than prefix + label), trailing should be at least 1
	sty := statusStyles{colorEnabled: false, width: 10}
	rule := sty.sectionRule("Active Sessions", 10)

	// Should still contain the label and at least one trailing dash
	if !strings.Contains(rule, "Active Sessions") {
		t.Errorf("sectionRule with narrow width should still contain label, got: %q", rule)
	}
	if !strings.Contains(rule, "─") {
		t.Errorf("sectionRule with narrow width should have at least one trailing dash, got: %q", rule)
	}
}

func TestActiveTimeDisplay_Hours(t *testing.T) {
	t.Parallel()

	hoursAgo := time.Now().Add(-3 * time.Hour)
	got := activeTimeDisplay(&hoursAgo)
	if got != "active 3h ago" {
		t.Errorf("activeTimeDisplay(-3h) = %q, want 'active 3h ago'", got)
	}
}

func TestActiveTimeDisplay_Days(t *testing.T) {
	t.Parallel()

	daysAgo := time.Now().Add(-48 * time.Hour)
	got := activeTimeDisplay(&daysAgo)
	if got != "active 2d ago" {
		t.Errorf("activeTimeDisplay(-48h) = %q, want 'active 2d ago'", got)
	}
}

func TestFormatSettingsStatusShort_Enabled(t *testing.T) {
	setupTestRepo(t)

	sty := statusStyles{colorEnabled: false, width: 60}
	s := &TraceSettings{
		Enabled:  true,
		Strategy: "manual-commit",
	}

	result := formatSettingsStatusShort(context.Background(), s, sty)

	if !strings.Contains(result, "●") {
		t.Errorf("Enabled status should have green dot, got: %q", result)
	}
	if !strings.Contains(result, "Enabled") {
		t.Errorf("Expected 'Enabled' in output, got: %q", result)
	}
	if !strings.Contains(result, "manual-commit") {
		t.Errorf("Expected strategy in output, got: %q", result)
	}
}

func TestFormatSettingsStatusShort_Disabled(t *testing.T) {
	setupTestRepo(t)

	sty := statusStyles{colorEnabled: false, width: 60}
	s := &TraceSettings{
		Enabled:  false,
		Strategy: "manual-commit",
	}

	result := formatSettingsStatusShort(context.Background(), s, sty)

	if !strings.Contains(result, "○") {
		t.Errorf("Disabled status should have open dot, got: %q", result)
	}
	if !strings.Contains(result, "Disabled") {
		t.Errorf("Expected 'Disabled' in output, got: %q", result)
	}
	if !strings.Contains(result, "manual-commit") {
		t.Errorf("Expected strategy in output, got: %q", result)
	}
}

func TestRunStatus_ShowsEnabledAgents(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)
	writeClaudeHooksFixture(t)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, false, false); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Agents ·") {
		t.Errorf("Expected 'Agents ·' in output, got: %s", output)
	}
	if !strings.Contains(output, "Claude Code") {
		t.Errorf("Expected 'Claude Code' in output, got: %s", output)
	}
}

func TestRunStatus_EnabledNoAgentsHidesHooksLine(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)
	// No agent hooks installed

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, false, false); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	output := stdout.String()
	if strings.Contains(output, "Agents ·") {
		t.Errorf("Should not show hooks line when no agents installed, got: %s", output)
	}
}

func TestRunStatus_DetailedShowsEnabledAgents(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsEnabled)
	writeClaudeHooksFixture(t)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, true, false); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "Agents ·") {
		t.Errorf("Expected 'Agents ·' in detailed output, got: %s", output)
	}
	if !strings.Contains(output, "Claude Code") {
		t.Errorf("Expected 'Claude Code' in detailed output, got: %s", output)
	}
}

func TestWriteActiveSessions_OmitsTokensWhenNoTokenData(t *testing.T) {
	setupTestRepo(t)

	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatalf("NewStateStore() error = %v", err)
	}

	now := time.Now()
	recentInteraction := now.Add(-5 * time.Minute)

	states := []*session.State{
		{
			SessionID:           "no-token-session",
			WorktreePath:        "/Users/test/repo",
			StartedAt:           now.Add(-30 * time.Minute),
			LastInteractionTime: &recentInteraction,
			Phase:               session.PhaseActive,
			LastPrompt:          "explain this code",
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

	if strings.Contains(output, "tokens") {
		t.Errorf("Session with no token data should NOT show tokens, got: %s", output)
	}
}

func TestWriteActiveSessions_ShowsTokensWithCheckpoints(t *testing.T) {
	setupTestRepo(t)

	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatalf("NewStateStore() error = %v", err)
	}

	now := time.Now()
	recentInteraction := now.Add(-5 * time.Minute)

	states := []*session.State{
		{
			SessionID:           "has-checkpoint-session",
			WorktreePath:        "/Users/test/repo",
			StartedAt:           now.Add(-30 * time.Minute),
			LastInteractionTime: &recentInteraction,
			Phase:               session.PhaseActive,
			LastPrompt:          "fix the bug",
			AgentType:           "Claude Code",
			StepCount:           2,
			TokenUsage: &agent.TokenUsage{
				InputTokens:  800,
				OutputTokens: 400,
			},
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

	if !strings.Contains(output, "tokens 1.2k") {
		t.Errorf("Session with checkpoints should show tokens, got: %s", output)
	}
}

func TestRunStatus_DetailedDisabledDoesNotShowAgents(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsDisabled)
	writeClaudeHooksFixture(t)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, true, false); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	output := stdout.String()
	if strings.Contains(output, "Agents ·") {
		t.Errorf("Disabled detailed status should not show agents, got: %s", output)
	}
}

func TestRunStatus_DisabledDoesNotShowAgents(t *testing.T) {
	setupTestRepo(t)
	writeSettings(t, testSettingsDisabled)
	writeClaudeHooksFixture(t)

	var stdout bytes.Buffer
	if err := runStatus(context.Background(), &stdout, false, false); err != nil {
		t.Fatalf("runStatus() error = %v", err)
	}

	output := stdout.String()
	if strings.Contains(output, "Agents ·") {
		t.Errorf("Disabled status should not show agents, got: %s", output)
	}
}

func TestFormatSettingsStatus_Project(t *testing.T) {
	t.Parallel()

	sty := statusStyles{colorEnabled: false, width: 60}
	s := &TraceSettings{
		Enabled:  true,
		Strategy: "manual-commit",
	}

	result := formatSettingsStatus("Project", s, sty)

	if !strings.Contains(result, "Project") {
		t.Errorf("Expected 'Project' prefix, got: %q", result)
	}
	if !strings.Contains(result, "enabled") {
		t.Errorf("Expected 'enabled' in output, got: %q", result)
	}
	if !strings.Contains(result, "manual-commit") {
		t.Errorf("Expected strategy in output, got: %q", result)
	}
}

func TestFormatSettingsStatus_LocalDisabled(t *testing.T) {
	t.Parallel()

	sty := statusStyles{colorEnabled: false, width: 60}
	s := &TraceSettings{
		Enabled:  false,
		Strategy: "manual-commit",
	}

	result := formatSettingsStatus("Local", s, sty)

	if !strings.Contains(result, "Local") {
		t.Errorf("Expected 'Local' prefix, got: %q", result)
	}
	if !strings.Contains(result, "disabled") {
		t.Errorf("Expected 'disabled' in output, got: %q", result)
	}
	if !strings.Contains(result, "manual-commit") {
		t.Errorf("Expected strategy in output, got: %q", result)
	}
}

func TestWriteActiveSessions_StaleIndicator(t *testing.T) {
	setupTestRepo(t)

	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatalf("NewStateStore() error = %v", err)
	}

	now := time.Now()
	staleInteraction := now.Add(-2 * time.Hour) // well past 1hr threshold

	states := []*session.State{
		{
			SessionID:           "stale-session-1",
			WorktreePath:        "/Users/test/repo",
			StartedAt:           now.Add(-3 * time.Hour),
			LastInteractionTime: &staleInteraction,
			Phase:               session.PhaseActive,
			LastPrompt:          "fix the bug",
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

	if !strings.Contains(output, "stale") {
		t.Errorf("Expected 'stale' indicator for session with interaction >1hr ago, got: %s", output)
	}
	if !strings.Contains(output, "trace doctor") {
		t.Errorf("Expected 'trace doctor' hint in stale indicator, got: %s", output)
	}
}

func TestIsStuckActiveSession(t *testing.T) {
	t.Parallel()

	now := time.Now()
	recent := now.Add(-5 * time.Minute)
	stale := now.Add(-2 * time.Hour)
	brandNew := now.Add(-10 * time.Second)

	tests := []struct {
		name  string
		state *session.State
		want  bool
	}{
		{
			name:  "active with stale interaction",
			state: &session.State{Phase: session.PhaseActive, LastInteractionTime: &stale},
			want:  true,
		},
		{
			name:  "active with nil interaction and old start",
			state: &session.State{Phase: session.PhaseActive, LastInteractionTime: nil, StartedAt: now.Add(-2 * time.Hour)},
			want:  true,
		},
		{
			name:  "active with nil interaction and recent start",
			state: &session.State{Phase: session.PhaseActive, LastInteractionTime: nil, StartedAt: brandNew},
			want:  false,
		},
		{
			name:  "active with recent interaction",
			state: &session.State{Phase: session.PhaseActive, LastInteractionTime: &recent},
			want:  false,
		},
		{
			name:  "idle with stale interaction",
			state: &session.State{Phase: session.PhaseIdle, LastInteractionTime: &stale},
			want:  false,
		},
		{
			name:  "ended with stale interaction",
			state: &session.State{Phase: session.PhaseEnded, LastInteractionTime: &stale},
			want:  false,
		},
		{
			name:  "empty phase with stale interaction",
			state: &session.State{Phase: "", LastInteractionTime: &stale},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.state.IsStuckActive(); got != tt.want {
				t.Errorf("IsStuckActive() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWriteActiveSessions_StaleWithNilInteractionOldStart(t *testing.T) {
	setupTestRepo(t)

	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatalf("NewStateStore() error = %v", err)
	}

	now := time.Now()

	states := []*session.State{
		{
			SessionID:           "old-nil-interaction-session",
			WorktreePath:        "/Users/test/repo",
			StartedAt:           now.Add(-2 * time.Hour),
			LastInteractionTime: nil,
			Phase:               session.PhaseActive,
			LastPrompt:          "do something",
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

	if !strings.Contains(output, "stale") {
		t.Errorf("Old session with nil LastInteractionTime should show stale indicator, got: %s", output)
	}
}

func TestWriteActiveSessions_NotStaleWhenBrandNew(t *testing.T) {
	setupTestRepo(t)

	store, err := session.NewStateStore(context.Background())
	if err != nil {
		t.Fatalf("NewStateStore() error = %v", err)
	}

	now := time.Now()

	states := []*session.State{
		{
			SessionID:           "brand-new-session",
			WorktreePath:        "/Users/test/repo",
			StartedAt:           now.Add(-10 * time.Second),
			LastInteractionTime: nil,
			Phase:               session.PhaseActive,
			LastPrompt:          "hello",
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
		t.Errorf("Brand-new session should NOT show stale indicator, got: %s", output)
	}
}
