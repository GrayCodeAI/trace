package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/strategy"
)

// sampleTranscript is a minimal Claude Code style JSONL transcript with one
// user message and one assistant message containing a text block and a tool_use.
const sampleTranscript = `{"type":"user","uuid":"u1","message":{"role":"user","content":"add a test"}}
{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[{"type":"text","text":"Sure, writing the test now."},{"type":"tool_use","name":"Write","input":{"file_path":"x_test.go"}}]}}
`

func TestRenderAsciinemaCast_HeaderAndEvents(t *testing.T) {
	state := &strategy.SessionState{
		SessionID: "cast-session",
		StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	cast, err := renderAsciinemaCast(state, []byte(sampleTranscript))
	if err != nil {
		t.Fatalf("renderAsciinemaCast: %v", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(cast))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// First line must be a valid v2 header.
	if !scanner.Scan() {
		t.Fatal("cast is empty; expected a header line")
	}
	var header asciinemaHeader
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		t.Fatalf("header is not valid JSON: %v\nline: %s", err, scanner.Text())
	}
	if header.Version != 2 {
		t.Errorf("header version = %d, want 2", header.Version)
	}
	if header.Width <= 0 || header.Height <= 0 {
		t.Errorf("header width/height must be positive, got %dx%d", header.Width, header.Height)
	}
	if header.Timestamp != state.StartedAt.Unix() {
		t.Errorf("header timestamp = %d, want %d", header.Timestamp, state.StartedAt.Unix())
	}

	// Remaining lines must each be a [time, "o", data] event array.
	var events int
	var sawUser, sawAssistant, sawTool bool
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var row []json.RawMessage
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatalf("event is not a JSON array: %v\nline: %s", err, line)
		}
		if len(row) != 3 {
			t.Fatalf("event must have 3 elements, got %d: %s", len(row), line)
		}
		var ts float64
		if err := json.Unmarshal(row[0], &ts); err != nil {
			t.Fatalf("event time not a number: %v", err)
		}
		var code string
		if err := json.Unmarshal(row[1], &code); err != nil || code != "o" {
			t.Fatalf("event code = %q (err %v), want \"o\"", code, err)
		}
		var data string
		if err := json.Unmarshal(row[2], &data); err != nil {
			t.Fatalf("event data not a string: %v", err)
		}
		if ts <= 0 {
			t.Errorf("event time should advance past 0, got %v", ts)
		}
		switch {
		case strings.Contains(data, "add a test"):
			sawUser = true
		case strings.Contains(data, "writing the test"):
			sawAssistant = true
		case strings.Contains(data, "tool: Write"):
			sawTool = true
		}
		events++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if events == 0 {
		t.Fatal("expected at least one output event, got none")
	}
	if !sawUser {
		t.Error("missing user prompt event")
	}
	if !sawAssistant {
		t.Error("missing assistant text event")
	}
	if !sawTool {
		t.Error("missing tool_use event")
	}
}

func TestRenderAsciinemaCast_EmptyTranscript(t *testing.T) {
	state := &strategy.SessionState{SessionID: "empty-cast"}
	cast, err := renderAsciinemaCast(state, nil)
	if err != nil {
		t.Fatalf("renderAsciinemaCast: %v", err)
	}
	// Must still produce a valid header line even with no events.
	line, _, _ := bufio.NewReader(bytes.NewReader(cast)).ReadLine()
	var header asciinemaHeader
	if err := json.Unmarshal(line, &header); err != nil {
		t.Fatalf("header invalid for empty transcript: %v", err)
	}
	if header.Version != 2 {
		t.Errorf("version = %d, want 2", header.Version)
	}
}
