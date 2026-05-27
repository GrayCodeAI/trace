package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/transcript"
	"github.com/spf13/cobra"
)

// sessionAnalytics holds computed analytics for a session.
type sessionAnalytics struct {
	SessionID       string           `json:"session_id"`
	AgentType       string           `json:"agent_type"`
	ModelName       string           `json:"model_name,omitempty"`
	Duration        time.Duration    `json:"duration"`
	DurationDisplay string           `json:"duration_display"`
	MessageCount    int              `json:"message_count"`
	UserMessages    int              `json:"user_messages"`
	AssistantMsgs   int              `json:"assistant_messages"`
	ToolCalls       int              `json:"tool_calls"`
	ToolUsage       map[string]int   `json:"tool_usage"`
	TokenUsage      *analyticsTokens `json:"token_usage,omitempty"`
	FilesTouched    int              `json:"files_touched"`
	StepCount       int              `json:"step_count"`
	StartedAt       time.Time        `json:"started_at"`
	EndedAt         *time.Time       `json:"ended_at,omitempty"`
}

// analyticsTokens provides token usage summary.
type analyticsTokens struct {
	Total      int `json:"total"`
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	APICalls   int `json:"api_calls"`
}

func newSessionAnalyticsCmd() *cobra.Command {
	var jsonFlag bool
	var allFlag bool

	cmd := &cobra.Command{
		Use:   "analytics [session-id]",
		Short: "Show session analytics and statistics",
		Long: `Display analytics for a session including message counts, tool usage
frequency, session duration, and token usage estimates.

Without a session ID, shows analytics for the most recent session in
the current worktree. Use --all to see aggregate analytics across all
sessions.

Examples:
  trace session analytics                       Analytics for most recent session
  trace session analytics <session-id>          Analytics for a specific session
  trace session analytics --all                 Aggregate analytics for all sessions
  trace session analytics <session-id> --json   Output as JSON`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionAnalytics(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args, jsonFlag, allFlag)
		},
	}

	cmd.Flags().BoolVar(&jsonFlag, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&allFlag, "all", false, "Show aggregate analytics for all sessions")

	return cmd
}

func runSessionAnalytics(ctx context.Context, w, errW io.Writer, args []string, jsonOutput, showAll bool) error {
	if _, err := paths.WorktreeRoot(ctx); err != nil {
		return errors.New("not a git repository")
	}

	if showAll {
		return runAggregateAnalytics(ctx, w, jsonOutput)
	}

	// Resolve session ID
	sessionID := ""
	if len(args) > 0 {
		sessionID = args[0]
	} else {
		sessionID = strategy.FindMostRecentSession(ctx)
		if sessionID == "" {
			fmt.Fprintln(w, "No active session found in this worktree.")
			return nil
		}
	}

	state, err := strategy.LoadSessionState(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to load session: %w", err)
	}
	if state == nil {
		fmt.Fprintln(errW, "Session not found.")
		return NewSilentError(fmt.Errorf("session not found: %s", sessionID))
	}

	analytics, err := computeSessionAnalytics(ctx, state)
	if err != nil {
		return fmt.Errorf("failed to compute analytics: %w", err)
	}

	if jsonOutput {
		return writeAnalyticsJSON(w, analytics)
	}
	return writeAnalyticsText(w, analytics)
}

// computeSessionAnalytics computes analytics from session state and transcript.
func computeSessionAnalytics(ctx context.Context, state *strategy.SessionState) (*sessionAnalytics, error) {
	a := &sessionAnalytics{
		SessionID:    state.SessionID,
		AgentType:    string(state.AgentType),
		ModelName:    state.ModelName,
		StartedAt:    state.StartedAt,
		EndedAt:      state.EndedAt,
		ToolUsage:    make(map[string]int),
		FilesTouched: len(state.FilesTouched),
	}

	// Compute duration
	if state.EndedAt != nil {
		a.Duration = state.EndedAt.Sub(state.StartedAt)
	} else if state.LastInteractionTime != nil {
		a.Duration = state.LastInteractionTime.Sub(state.StartedAt)
	} else {
		a.Duration = time.Since(state.StartedAt)
	}
	a.DurationDisplay = FormatDuration(a.Duration)

	// Token usage
	if state.TokenUsage != nil {
		total := state.TokenUsage.InputTokens + state.TokenUsage.CacheCreationTokens +
			state.TokenUsage.CacheReadTokens + state.TokenUsage.OutputTokens
		a.TokenUsage = &analyticsTokens{
			Total:      total,
			Input:      state.TokenUsage.InputTokens,
			Output:     state.TokenUsage.OutputTokens,
			CacheRead:  state.TokenUsage.CacheReadTokens,
			CacheWrite: state.TokenUsage.CacheCreationTokens,
			APICalls:   state.TokenUsage.APICallCount,
		}
	}

	// Parse transcript for message/tool counts
	if state.TranscriptPath != "" {
		data, err := loadTranscriptForAnalytics(ctx, state)
		if err == nil && len(data) > 0 {
			analyzeTranscript(data, a)
		}
	}

	return a, nil
}

// loadTranscriptForAnalytics loads transcript bytes for analytics computation.
func loadTranscriptForAnalytics(ctx context.Context, state *strategy.SessionState) ([]byte, error) {
	if state.TranscriptPath == "" {
		return nil, errors.New("no transcript path")
	}
	return loadTranscriptForExport(ctx, state)
}

// analyzeTranscript parses transcript and populates message/tool counts.
func analyzeTranscript(data []byte, a *sessionAnalytics) {
	lines, err := transcript.ParseFromBytes(data)
	if err != nil {
		return
	}

	for _, line := range lines {
		switch line.Type {
		case transcript.TypeUser:
			a.UserMessages++
			a.MessageCount++
		case transcript.TypeAssistant:
			a.AssistantMsgs++
			a.MessageCount++
			// Parse tool usage from assistant messages
			tools := extractToolNames(line.Message)
			for _, t := range tools {
				a.ToolCalls++
				a.ToolUsage[t]++
			}
		}
	}
}

// extractToolNames extracts tool names from an assistant message.
func extractToolNames(msg json.RawMessage) []string {
	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Name string `json:"name,omitempty"`
		} `json:"content"`
	}
	if err := json.Unmarshal(msg, &parsed); err != nil {
		return nil
	}

	var names []string
	for _, c := range parsed.Content {
		if c.Type == transcript.ContentTypeToolUse && c.Name != "" {
			names = append(names, c.Name)
		}
	}
	return names
}

// writeAnalyticsJSON writes analytics as JSON.
func writeAnalyticsJSON(w io.Writer, a *sessionAnalytics) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(a)
}

// writeAnalyticsText writes analytics as formatted text.
func writeAnalyticsText(w io.Writer, a *sessionAnalytics) error {
	sty := newStatusStyles(w)

	// Header
	fmt.Fprintln(w, sty.sectionRule(fmt.Sprintf("Session Analytics: %s", a.SessionID), sty.width))
	fmt.Fprintln(w)

	// Overview
	writeLabelValue(w, "Agent", a.AgentType, sty)
	if a.ModelName != "" {
		writeLabelValue(w, "Model", a.ModelName, sty)
	}
	writeLabelValue(w, "Duration", a.DurationDisplay, sty)
	writeLabelValue(w, "Started", a.StartedAt.Local().Format("2006-01-02 15:04:05"), sty)
	if a.EndedAt != nil {
		writeLabelValue(w, "Ended", a.EndedAt.Local().Format("2006-01-02 15:04:05"), sty)
	}
	fmt.Fprintln(w)

	// Messages section
	fmt.Fprintln(w, sty.render(sty.bold, "  Messages"))
	fmt.Fprintln(w)
	writeLabelValue(w, "Total", fmt.Sprintf("%d", a.MessageCount), sty)
	writeLabelValue(w, "User", fmt.Sprintf("%d", a.UserMessages), sty)
	writeLabelValue(w, "Assistant", fmt.Sprintf("%d", a.AssistantMsgs), sty)
	writeLabelValue(w, "Tool calls", fmt.Sprintf("%d", a.ToolCalls), sty)
	fmt.Fprintln(w)

	// Tool usage section (if any)
	if len(a.ToolUsage) > 0 {
		fmt.Fprintln(w, sty.render(sty.bold, "  Tool Usage"))
		fmt.Fprintln(w)

		// Sort tools by count (descending)
		type toolCount struct {
			name  string
			count int
		}
		var sorted []toolCount
		for name, count := range a.ToolUsage {
			sorted = append(sorted, toolCount{name, count})
		}
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].count > sorted[j].count
		})

		for _, tc := range sorted {
			bar := renderUsageBar(tc.count, maxToolCount(a.ToolUsage), 30)
			fmt.Fprintf(w, "    %-20s %s %d\n", tc.name, bar, tc.count)
		}
		fmt.Fprintln(w)
	}

	// Token usage section
	if a.TokenUsage != nil {
		fmt.Fprintln(w, sty.render(sty.bold, "  Token Usage"))
		fmt.Fprintln(w)
		writeLabelValue(w, "Total", formatTokenCount(a.TokenUsage.Total), sty)
		writeLabelValue(w, "Input", formatTokenCount(a.TokenUsage.Input), sty)
		writeLabelValue(w, "Output", formatTokenCount(a.TokenUsage.Output), sty)
		if a.TokenUsage.CacheRead > 0 {
			writeLabelValue(w, "Cache read", formatTokenCount(a.TokenUsage.CacheRead), sty)
		}
		if a.TokenUsage.CacheWrite > 0 {
			writeLabelValue(w, "Cache write", formatTokenCount(a.TokenUsage.CacheWrite), sty)
		}
		if a.TokenUsage.APICalls > 0 {
			writeLabelValue(w, "API calls", fmt.Sprintf("%d", a.TokenUsage.APICalls), sty)
		}
		fmt.Fprintln(w)
	}

	// Files section
	writeLabelValue(w, "Files touched", fmt.Sprintf("%d", a.FilesTouched), sty)
	fmt.Fprintln(w)

	fmt.Fprintln(w, sty.horizontalRule(sty.width))
	return nil
}

// writeLabelValue writes a styled label-value pair.
func writeLabelValue(w io.Writer, label, value string, sty statusStyles) {
	padded := fmt.Sprintf("  %-14s", label)
	if sty.colorEnabled {
		padded = sty.render(sty.dim, padded)
	}
	fmt.Fprintf(w, "%s%s\n", padded, value)
}

// maxToolCount returns the maximum count in the tool usage map.
func maxToolCount(usage map[string]int) int {
	max := 0
	for _, c := range usage {
		if c > max {
			max = c
		}
	}
	return max
}

// renderUsageBar renders a simple ASCII bar chart.
func renderUsageBar(count, max, width int) string {
	if max == 0 || count == 0 {
		return strings.Repeat(" ", width)
	}
	filled := (count * width) / max
	if filled == 0 {
		filled = 1
	}
	return strings.Repeat("#", filled) + strings.Repeat(" ", width-filled)
}

// runAggregateAnalytics computes analytics across all sessions.
func runAggregateAnalytics(ctx context.Context, w io.Writer, jsonOutput bool) error {
	states, err := strategy.ListSessionStates(ctx)
	if err != nil {
		return fmt.Errorf("failed to list sessions: %w", err)
	}

	var active []*strategy.SessionState
	for _, s := range states {
		if s != nil {
			active = append(active, s)
		}
	}

	if len(active) == 0 {
		fmt.Fprintln(w, "No sessions found.")
		return nil
	}

	// Compute aggregate stats
	agg := &sessionAnalytics{
		SessionID: "(all sessions)",
		ToolUsage: make(map[string]int),
	}

	totalDuration := time.Duration(0)
	for _, s := range active {
		agg.FilesTouched += len(s.FilesTouched)

		if s.TokenUsage != nil {
			total := s.TokenUsage.InputTokens + s.TokenUsage.CacheCreationTokens +
				s.TokenUsage.CacheReadTokens + s.TokenUsage.OutputTokens
			if agg.TokenUsage == nil {
				agg.TokenUsage = &analyticsTokens{}
			}
			agg.TokenUsage.Total += total
			agg.TokenUsage.Input += s.TokenUsage.InputTokens
			agg.TokenUsage.Output += s.TokenUsage.OutputTokens
			agg.TokenUsage.CacheRead += s.TokenUsage.CacheReadTokens
			agg.TokenUsage.CacheWrite += s.TokenUsage.CacheCreationTokens
			agg.TokenUsage.APICalls += s.TokenUsage.APICallCount
		}

		if s.EndedAt != nil {
			totalDuration += s.EndedAt.Sub(s.StartedAt)
		} else if s.LastInteractionTime != nil {
			totalDuration += s.LastInteractionTime.Sub(s.StartedAt)
		}

		// Count checkpoints
		agg.StepCount += s.StepCount
	}

	agg.Duration = totalDuration
	agg.DurationDisplay = FormatDuration(totalDuration)

	if jsonOutput {
		return writeAnalyticsJSON(w, agg)
	}

	sty := newStatusStyles(w)
	fmt.Fprintln(w, sty.sectionRule(fmt.Sprintf("Aggregate Analytics (%d sessions)", len(active)), sty.width))
	fmt.Fprintln(w)
	writeLabelValue(w, "Sessions", fmt.Sprintf("%d", len(active)), sty)
	writeLabelValue(w, "Total time", agg.DurationDisplay, sty)
	writeLabelValue(w, "Checkpoints", fmt.Sprintf("%d", agg.StepCount), sty)
	writeLabelValue(w, "Files touched", fmt.Sprintf("%d", agg.FilesTouched), sty)

	if agg.TokenUsage != nil {
		fmt.Fprintln(w)
		fmt.Fprintln(w, sty.render(sty.bold, "  Token Usage (All Sessions)"))
		fmt.Fprintln(w)
		writeLabelValue(w, "Total", formatTokenCount(agg.TokenUsage.Total), sty)
		writeLabelValue(w, "Input", formatTokenCount(agg.TokenUsage.Input), sty)
		writeLabelValue(w, "Output", formatTokenCount(agg.TokenUsage.Output), sty)
		if agg.TokenUsage.APICalls > 0 {
			writeLabelValue(w, "API calls", fmt.Sprintf("%d", agg.TokenUsage.APICalls), sty)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, sty.horizontalRule(sty.width))

	return nil
}
