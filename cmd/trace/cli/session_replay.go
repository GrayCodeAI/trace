package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/transcript"
	"github.com/spf13/cobra"
)

// replayEntry represents a single step in the replayed session.
type replayEntry struct {
	Index     int
	Type      string // "user", "assistant", "tool"
	Content   string
	ToolName  string
	Timestamp string
}

func newSessionReplayCmd() *cobra.Command {
	var noPagerFlag bool
	var stepFlag bool

	cmd := &cobra.Command{
		Use:   "replay [session-id]",
		Short: "Replay a recorded session interactively",
		Long: `Play back a recorded agent session, stepping through messages and tool calls.

Without a session ID, replays the most recent session in the current worktree.
Use --step to advance one message at a time (press Enter for next, 'q' to quit).

Examples:
  trace session replay                     Replay most recent session
  trace session replay <session-id>        Replay a specific session
  trace session replay --step              Step through messages interactively
  trace session replay <session-id> --json Output replay data as JSON`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionReplay(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args, noPagerFlag, stepFlag)
		},
	}

	cmd.Flags().BoolVar(&noPagerFlag, "no-pager", false, "Disable pager output")
	cmd.Flags().BoolVar(&stepFlag, "step", false, "Step through messages interactively (press Enter for next)")

	return cmd
}

func runSessionReplay(ctx context.Context, w, errW io.Writer, args []string, noPager, step bool) error {
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

	entries, err := buildReplayEntries(ctx, state)
	if err != nil {
		return fmt.Errorf("failed to load transcript: %w", err)
	}

	if len(entries) == 0 {
		fmt.Fprintln(w, "No transcript entries found for this session.")
		return nil
	}

	if step {
		return runStepReplay(w, errW, state, entries)
	}
	return runFullReplay(w, state, entries, noPager)
}

// buildReplayEntries loads and parses the transcript for a session into replay entries.
func buildReplayEntries(_ context.Context, state *strategy.SessionState) ([]replayEntry, error) {
	var transcriptBytes []byte

	// Read the transcript file directly from the recorded path
	if state.TranscriptPath != "" {
		data, readErr := os.ReadFile(state.TranscriptPath) //nolint:gosec // controlled path from session state
		if readErr == nil {
			transcriptBytes = data
		}
	}

	if len(transcriptBytes) == 0 {
		return nil, nil
	}

	return parseTranscriptToReplayEntries(transcriptBytes)
}

// parseTranscriptToReplayEntries parses transcript bytes into ordered replay entries.
func parseTranscriptToReplayEntries(data []byte) ([]replayEntry, error) {
	lines, err := transcript.ParseFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("parsing transcript: %w", err)
	}

	var entries []replayEntry
	index := 0

	for _, line := range lines {
		switch line.Type {
		case transcript.TypeUser:
			content := transcript.ExtractUserContent(line.Message)
			if content == "" {
				continue
			}
			entries = append(entries, replayEntry{
				Index:   index,
				Type:    "user",
				Content: content,
			})
			index++

		case transcript.TypeAssistant:
			blocks := parseAssistantBlocks(line.Message)
			for _, block := range blocks {
				entries = append(entries, replayEntry{
					Index:    index,
					Type:     block.Type,
					Content:  block.Content,
					ToolName: block.ToolName,
				})
				index++
			}
		}
	}

	return entries, nil
}

// replayBlock represents a parsed assistant message block.
type replayBlock struct {
	Type     string // "assistant" or "tool"
	Content  string
	ToolName string
}

// parseAssistantBlocks extracts text and tool_use blocks from an assistant message.
func parseAssistantBlocks(msg json.RawMessage) []replayBlock {
	var parsed struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text,omitempty"`
			Name  string          `json:"name,omitempty"`
			Input json.RawMessage `json:"input,omitempty"`
		} `json:"content"`
	}
	if err := json.Unmarshal(msg, &parsed); err != nil {
		return nil
	}

	var blocks []replayBlock
	for _, c := range parsed.Content {
		switch c.Type {
		case transcript.ContentTypeText:
			if strings.TrimSpace(c.Text) != "" {
				blocks = append(blocks, replayBlock{
					Type:    "assistant",
					Content: strings.TrimSpace(c.Text),
				})
			}
		case transcript.ContentTypeToolUse:
			toolDetail := c.Name
			if len(c.Input) > 0 {
				// Summarize tool input
				var inputMap map[string]interface{}
				if json.Unmarshal(c.Input, &inputMap) == nil {
					if fp, ok := inputMap["file_path"].(string); ok && fp != "" {
						toolDetail = fmt.Sprintf("%s (%s)", c.Name, fp)
					} else if cmd, ok := inputMap["command"].(string); ok && cmd != "" {
						cmdPreview := cmd
						if len(cmdPreview) > 60 {
							cmdPreview = cmdPreview[:60] + "..."
						}
						toolDetail = fmt.Sprintf("%s: %s", c.Name, cmdPreview)
					}
				}
			}
			blocks = append(blocks, replayBlock{
				Type:     "tool",
				ToolName: c.Name,
				Content:  toolDetail,
			})
		}
	}
	return blocks
}

// runFullReplay outputs the full replay to the writer.
func runFullReplay(w io.Writer, state *strategy.SessionState, entries []replayEntry, _ bool) error {
	sty := newStatusStyles(w)

	// Header
	fmt.Fprintln(w, sty.sectionRule("Session Replay: "+state.SessionID, sty.width))
	fmt.Fprintln(w)

	agentLabel := string(state.AgentType)
	if agentLabel == "" {
		agentLabel = unknownPlaceholder
	}
	fmt.Fprintf(w, "  Agent:    %s\n", agentLabel)
	if state.ModelName != "" {
		fmt.Fprintf(w, "  Model:    %s\n", state.ModelName)
	}
	fmt.Fprintf(w, "  Started:  %s\n", state.StartedAt.Local().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(w, "  Messages: %d\n", len(entries))
	fmt.Fprintln(w)
	fmt.Fprintln(w, sty.horizontalRule(sty.width))
	fmt.Fprintln(w)

	for _, entry := range entries {
		renderReplayEntry(w, entry, sty)
	}

	fmt.Fprintln(w, sty.horizontalRule(sty.width))
	fmt.Fprintln(w)
	return nil
}

// runStepReplay steps through entries interactively.
func runStepReplay(w, errW io.Writer, state *strategy.SessionState, entries []replayEntry) error {
	sty := newStatusStyles(w)

	fmt.Fprintln(w, sty.sectionRule("Session Replay: "+state.SessionID, sty.width))
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %d messages to replay. Press Enter for next, 'q' to quit.\n", len(entries))
	fmt.Fprintln(w)
	fmt.Fprintln(w, sty.horizontalRule(sty.width))
	fmt.Fprintln(w)

	reader := bufio.NewReader(os.Stdin)
	for i, entry := range entries {
		renderReplayEntry(w, entry, sty)
		fmt.Fprintln(w)

		if i < len(entries)-1 {
			fmt.Fprint(errW, "  [Enter] next  [q] quit > ")
			input, err := reader.ReadString('\n')
			if err != nil {
				return err
			}
			input = strings.TrimSpace(input)
			if strings.ToLower(input) == "q" {
				fmt.Fprintln(w, "\n  Replay stopped.")
				return nil
			}
		}
	}

	fmt.Fprintln(w, sty.horizontalRule(sty.width))
	fmt.Fprintf(w, "  Replay complete. %d messages shown.\n", len(entries))
	fmt.Fprintln(w)
	return nil
}

// renderReplayEntry writes a single replay entry to the writer.
func renderReplayEntry(w io.Writer, entry replayEntry, sty statusStyles) {
	switch entry.Type {
	case "user":
		fmt.Fprintln(w, sty.render(sty.bold, "  You:"))
		printWrapped(w, entry.Content, "    ", 76)
	case "assistant":
		fmt.Fprintln(w, sty.render(sty.cyan, "  Assistant:"))
		printWrapped(w, entry.Content, "    ", 76)
	case "tool":
		fmt.Fprintf(w, "  %s %s\n",
			sty.render(sty.dim, "[tool]"),
			sty.render(sty.agent, entry.ToolName))
		if entry.Content != entry.ToolName {
			printWrapped(w, entry.Content, "    ", 76)
		}
	}
}

// printWrapped writes text with indentation and word-wrapping.
func printWrapped(w io.Writer, text, indent string, width int) {
	remaining := width
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			fmt.Fprintln(w)
			remaining = width
			continue
		}
		words := strings.Fields(line)
		for _, word := range words {
			if remaining < len(word)+1 && remaining < width {
				fmt.Fprintln(w)
				fmt.Fprint(w, indent)
				remaining = width - len(indent)
			}
			if remaining == width-len(indent) {
				fmt.Fprint(w, indent)
			}
			fmt.Fprint(w, word+" ")
			remaining -= len(word) + 1
		}
	}
	fmt.Fprintln(w)
}
