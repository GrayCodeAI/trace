package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/session"
	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cli/validation"
	"github.com/spf13/cobra"
)

// sessionExport is the JSON envelope for exported sessions.
// Designed for team sharing: contains session metadata and transcript data
// that can be imported by another team member.
type sessionExport struct {
	// Format version for forward compatibility
	Version int `json:"version"`
	// Export metadata
	ExportedAt time.Time `json:"exported_at"`
	ExportedBy string    `json:"exported_by,omitempty"`

	// Session state
	SessionID    string     `json:"session_id"`
	AgentType    string     `json:"agent_type,omitempty"`
	ModelName    string     `json:"model_name,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	Phase        string     `json:"phase,omitempty"`
	StepCount    int        `json:"step_count"`
	LastPrompt   string     `json:"last_prompt,omitempty"`
	FilesTouched []string   `json:"files_touched,omitempty"`
	WorktreePath string     `json:"worktree_path,omitempty"`

	// Token usage
	TokenUsage *exportTokenUsage `json:"token_usage,omitempty"`

	// Transcript data (raw JSONL bytes, base64 not needed since JSON handles byte arrays)
	Transcript []byte `json:"transcript,omitempty"`
}

// exportTokenUsage mirrors agent.TokenUsage for the export format.
type exportTokenUsage struct {
	InputTokens         int `json:"input_tokens"`
	CacheCreationTokens int `json:"cache_creation_tokens"`
	CacheReadTokens     int `json:"cache_read_tokens"`
	OutputTokens        int `json:"output_tokens"`
	APICallCount        int `json:"api_call_count"`
}

func newSessionExportCmd() *cobra.Command {
	var outputFlag string
	var formatFlag string

	cmd := &cobra.Command{
		Use:   "export [session-id]",
		Short: "Export a session for team sharing",
		Long: `Export a session's metadata and transcript to a portable JSON file.

The exported file can be shared with team members and imported with
'trace session import'. Without a session ID, exports the most recent
session in the current worktree.

Use --format asciinema to render the transcript as an asciinema v2 cast
(a .cast file playable with 'asciinema play') instead of the Trace export
envelope.

Examples:
  trace session export                          Export most recent session
  trace session export <session-id>             Export a specific session
  trace session export <session-id> -o out.json Export to a specific file
  trace session export --format asciinema       Export as an asciinema cast`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionExport(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args, outputFlag, formatFlag)
		},
	}

	cmd.Flags().StringVarP(&outputFlag, "output", "o", "", "Write export to file (default: <session-id>.json)")
	cmd.Flags().StringVar(&formatFlag, "format", "json", "Export format: json (Trace envelope) or asciinema (cast)")

	return cmd
}

func newSessionImportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Import a shared session",
		Long: `Import a session exported by 'trace session import'.

Reads the JSON export file and creates a local session state entry.
The session will appear in 'trace session list' with an 'imported' marker.

Examples:
  trace session import session-export.json
  trace session import /path/to/shared-session.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionImport(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0])
		},
	}

	return cmd
}

func runSessionExport(ctx context.Context, w, errW io.Writer, args []string, output, format string) error {
	if _, err := paths.WorktreeRoot(ctx); err != nil {
		return errors.New("not a git repository")
	}

	// Resolve session ID
	var sessionID string
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

	switch format {
	case "", "json":
		// fall through to the default JSON envelope export below.
	case "asciinema":
		return runSessionExportAsciinema(ctx, w, state, output)
	default:
		return fmt.Errorf("unsupported export format %q (want json or asciinema)", format)
	}

	export := buildSessionExport(state)

	// Try to load transcript data
	transcriptBytes, err := loadTranscriptForExport(ctx, state)
	if err == nil {
		export.Transcript = transcriptBytes
	}
	export.Transcript = transcriptBytes

	// Determine output destination
	if output == "" {
		output = sessionID + ".json"
	}

	// Write to file
	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal export: %w", err)
	}

	if err := os.WriteFile(output, data, 0o600); err != nil {
		return fmt.Errorf("failed to write export file: %w", err)
	}

	sty := newStatusStyles(w)
	rows := []explainRow{
		{Label: "session", Value: sessionID},
		{Label: "file", Value: output},
		{Label: "size", Value: formatByteCount(len(data))},
	}
	if len(transcriptBytes) > 0 {
		rows = append(rows, explainRow{Label: "transcript", Value: fmt.Sprintf("%d bytes", len(transcriptBytes))})
	}
	fmt.Fprint(w, sty.renderSuccess("Session exported", rows))
	return nil
}

func buildSessionExport(state *strategy.SessionState) sessionExport {
	export := sessionExport{
		Version:      1,
		ExportedAt:   time.Now(),
		SessionID:    state.SessionID,
		AgentType:    string(state.AgentType),
		ModelName:    state.ModelName,
		StartedAt:    state.StartedAt,
		EndedAt:      state.EndedAt,
		Phase:        string(state.Phase),
		StepCount:    state.StepCount,
		LastPrompt:   state.LastPrompt,
		FilesTouched: state.FilesTouched,
		WorktreePath: state.WorktreePath,
	}

	if state.TokenUsage != nil {
		export.TokenUsage = &exportTokenUsage{
			InputTokens:         state.TokenUsage.InputTokens,
			CacheCreationTokens: state.TokenUsage.CacheCreationTokens,
			CacheReadTokens:     state.TokenUsage.CacheReadTokens,
			OutputTokens:        state.TokenUsage.OutputTokens,
			APICallCount:        state.TokenUsage.APICallCount,
		}
	}

	return export
}

// loadTranscriptForExport loads transcript data from the session's transcript path.
func loadTranscriptForExport(_ context.Context, state *strategy.SessionState) ([]byte, error) {
	if state.TranscriptPath == "" {
		return nil, errors.New("no transcript path recorded")
	}

	data, err := os.ReadFile(state.TranscriptPath) //nolint:gosec // controlled path from session state
	if err != nil {
		return nil, fmt.Errorf("reading transcript: %w", err)
	}
	return data, nil
}

// formatByteCount formats a byte count for human-readable display.
func formatByteCount(n int) string {
	const (
		kb = 1024
		mb = 1024 * kb
	)
	switch {
	case n >= mb:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func runSessionImport(ctx context.Context, w, errW io.Writer, filePath string) error {
	if _, err := paths.WorktreeRoot(ctx); err != nil {
		return errors.New("not a git repository")
	}

	// Read the export file
	data, err := os.ReadFile(filePath) //nolint:gosec // user-specified file path
	if err != nil {
		return fmt.Errorf("failed to read import file: %w", err)
	}

	var export sessionExport
	if err := json.Unmarshal(data, &export); err != nil {
		return fmt.Errorf("invalid import file format: %w", err)
	}

	// Validate version
	if export.Version < 1 {
		return fmt.Errorf("unsupported export version: %d", export.Version)
	}
	if export.SessionID == "" {
		return errors.New("import file missing session ID")
	}

	// Validate session ID
	if err := validation.ValidateSessionID(export.SessionID); err != nil {
		return fmt.Errorf("invalid session ID in import file: %w", err)
	}

	// Check if session already exists
	existing, _ := strategy.LoadSessionState(ctx, export.SessionID) //nolint:errcheck // best-effort existence check
	if existing != nil {
		fmt.Fprintf(errW, "Session %s already exists locally. Skipping import.\n", export.SessionID)
		return NewSilentError(fmt.Errorf("session %s already exists", export.SessionID))
	}

	// Build session state from export
	state := &strategy.SessionState{
		SessionID:    export.SessionID,
		AgentType:    types.AgentType(export.AgentType),
		ModelName:    export.ModelName,
		StartedAt:    export.StartedAt,
		EndedAt:      export.EndedAt,
		Phase:        session.PhaseFromString(export.Phase),
		StepCount:    export.StepCount,
		LastPrompt:   export.LastPrompt,
		FilesTouched: export.FilesTouched,
	}

	if export.TokenUsage != nil {
		state.TokenUsage = &agent.TokenUsage{
			InputTokens:         export.TokenUsage.InputTokens,
			CacheCreationTokens: export.TokenUsage.CacheCreationTokens,
			CacheReadTokens:     export.TokenUsage.CacheReadTokens,
			OutputTokens:        export.TokenUsage.OutputTokens,
			APICallCount:        export.TokenUsage.APICallCount,
		}
	}

	// Save the imported session state
	if err := strategy.SaveSessionState(ctx, state); err != nil {
		return fmt.Errorf("failed to save imported session: %w", err)
	}

	sty := newStatusStyles(w)
	rows := []explainRow{
		{Label: "session", Value: export.SessionID},
		{Label: "agent", Value: export.AgentType},
		{Label: "from", Value: export.ExportedAt.Format("2006-01-02 15:04")},
	}
	if export.Transcript != nil {
		rows = append(rows, explainRow{Label: "transcript", Value: fmt.Sprintf("%d bytes", len(export.Transcript))})
	}
	fmt.Fprint(w, sty.renderSuccess("Session imported", rows))
	return nil
}
