package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/GrayCodeAI/trace/cli/strategy"
	"github.com/GrayCodeAI/trace/cli/transcript"
)

// asciinema v2 cast defaults. The format is a header JSON object on the first
// line followed by one JSON array per event: [time, "o", data].
// See https://docs.asciinema.org/manual/asciicast/v2/
const (
	asciinemaVersion   = 2
	asciinemaWidth     = 80
	asciinemaHeight    = 24
	asciinemaEventCode = "o" // output event
	// asciinemaEventDelay is the synthetic gap (seconds) inserted between
	// reconstructed events, since transcripts don't record real terminal timing.
	asciinemaEventDelay = 1.5
)

// asciinemaHeader is the first line of an asciinema v2 cast file.
type asciinemaHeader struct {
	Version   int    `json:"version"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Timestamp int64  `json:"timestamp,omitempty"`
	Title     string `json:"title,omitempty"`
}

// runSessionExportAsciinema renders a session's transcript as an asciinema v2
// cast. Timing is synthetic (the JSONL transcripts don't capture real terminal
// timing), so each reconstructed line advances the clock by a fixed delay.
func runSessionExportAsciinema(_ context.Context, w io.Writer, state *strategy.SessionState, output string) error {
	transcriptBytes, err := loadTranscriptForExport(nil, state)
	if err != nil {
		return fmt.Errorf("failed to load transcript: %w", err)
	}

	cast, err := renderAsciinemaCast(state, transcriptBytes)
	if err != nil {
		return fmt.Errorf("failed to render cast: %w", err)
	}

	if output == "" {
		output = state.SessionID + ".cast"
	}
	if err := os.WriteFile(output, cast, 0o600); err != nil {
		return fmt.Errorf("failed to write cast file: %w", err)
	}

	sty := newStatusStyles(w)
	rows := []explainRow{
		{Label: "session", Value: state.SessionID},
		{Label: "file", Value: output},
		{Label: "format", Value: "asciinema v2"},
		{Label: "size", Value: formatByteCount(len(cast))},
	}
	fmt.Fprint(w, sty.renderSuccess("Session exported", rows))
	return nil
}

// renderAsciinemaCast builds an asciinema v2 cast from transcript bytes. The
// returned bytes are a header line followed by newline-delimited event arrays.
// Exposed (lowercase, package-internal) and side-effect free so it can be unit
// tested without touching the filesystem.
func renderAsciinemaCast(state *strategy.SessionState, transcriptBytes []byte) ([]byte, error) {
	header := asciinemaHeader{
		Version: asciinemaVersion,
		Width:   asciinemaWidth,
		Height:  asciinemaHeight,
	}
	if state != nil {
		if !state.StartedAt.IsZero() {
			header.Timestamp = state.StartedAt.Unix()
		}
		header.Title = "trace session " + state.SessionID
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(header); err != nil {
		return nil, fmt.Errorf("encode header: %w", err)
	}

	events := transcriptToAsciinemaEvents(transcriptBytes)
	for _, ev := range events {
		// Each event is a 3-element heterogeneous array: [time, "o", data].
		row := []any{ev.time, asciinemaEventCode, ev.data}
		line, err := json.Marshal(row)
		if err != nil {
			return nil, fmt.Errorf("encode event: %w", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}

	return buf.Bytes(), nil
}

// asciinemaEvent is one terminal output event with synthetic timing.
type asciinemaEvent struct {
	time float64
	data string
}

// transcriptToAsciinemaEvents reconstructs terminal output events from a JSONL
// transcript. User messages are rendered as prompt lines; assistant text blocks
// and tool calls are rendered as output. Timing is synthetic: each event
// advances the clock by asciinemaEventDelay.
func transcriptToAsciinemaEvents(transcriptBytes []byte) []asciinemaEvent {
	if len(transcriptBytes) == 0 {
		return nil
	}

	lines, err := transcript.ParseFromBytes(transcriptBytes)
	if err != nil {
		return nil
	}

	var events []asciinemaEvent
	clock := 0.0
	add := func(text string) {
		if text == "" {
			return
		}
		clock += asciinemaEventDelay
		// asciinema treats "\r\n" as the line separator for terminal rendering.
		events = append(events, asciinemaEvent{
			time: clock,
			data: normalizeCastText(text),
		})
	}

	for _, line := range lines {
		switch line.Type {
		case transcript.TypeUser:
			if content := transcript.ExtractUserContent(line.Message); content != "" {
				add("$ " + content + "\n")
			}
		case transcript.TypeAssistant:
			for _, block := range extractAssistantBlocks(line.Message) {
				add(block)
			}
		}
	}

	return events
}

// extractAssistantBlocks pulls renderable text from an assistant message:
// text blocks verbatim and tool_use blocks as a short "[tool: name]" marker.
func extractAssistantBlocks(message json.RawMessage) []string {
	var msg transcript.AssistantMessage
	if err := json.Unmarshal(message, &msg); err != nil {
		return nil
	}
	var out []string
	for _, block := range msg.Content {
		switch block.Type {
		case transcript.ContentTypeText:
			if block.Text != "" {
				out = append(out, block.Text+"\n")
			}
		case transcript.ContentTypeToolUse:
			if block.Name != "" {
				out = append(out, fmt.Sprintf("[tool: %s]\n", block.Name))
			}
		}
	}
	return out
}

// normalizeCastText converts bare newlines to CRLF so terminal players render
// each line starting at the left margin.
func normalizeCastText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\n", "\r\n")
}
