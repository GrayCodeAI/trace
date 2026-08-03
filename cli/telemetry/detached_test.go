package telemetry

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// spySendDetached swaps the dispatch hook and returns a counter.
func spySendDetached(t *testing.T) *int {
	t.Helper()
	calls := 0
	orig := sendDetached
	sendDetached = func(string) { calls++ }
	t.Cleanup(func() { sendDetached = orig })
	return &calls
}

// isolateConfigDir redirects the anonymous ID file into a temp dir.
func isolateConfigDir(t *testing.T) {
	t.Helper()
	orig := userConfigDir
	userConfigDir = func() (string, error) { return t.TempDir(), nil }
	t.Cleanup(func() { userConfigDir = orig })
}

func TestEventPayloadSerialization(t *testing.T) {
	payload := EventPayload{
		Event:      "cli_command_executed",
		DistinctID: "test-machine-id",
		Properties: map[string]any{
			"command":        "trace status",
			"strategy":       "manual-commit",
			"agent":          "claude-code",
			"isTraceEnabled": true,
			"cli_version":    "1.0.0",
			"os":             "darwin",
			"arch":           "arm64",
		},
		Timestamp: time.Date(2026, 1, 28, 12, 0, 0, 0, time.UTC),
	}

	// Serialize
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Failed to marshal EventPayload: %v", err)
	}

	// Deserialize
	var decoded EventPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal EventPayload: %v", err)
	}

	// Verify fields
	if decoded.Event != payload.Event {
		t.Errorf("Event = %q, want %q", decoded.Event, payload.Event)
	}
	if decoded.DistinctID != payload.DistinctID {
		t.Errorf("DistinctID = %q, want %q", decoded.DistinctID, payload.DistinctID)
	}
	if !decoded.Timestamp.Equal(payload.Timestamp) {
		t.Errorf("Timestamp = %v, want %v", decoded.Timestamp, payload.Timestamp)
	}

	// Verify properties
	if cmd, ok := decoded.Properties["command"].(string); !ok || cmd != "trace status" {
		t.Errorf("Properties[command] = %v, want %q", decoded.Properties["command"], "trace status")
	}
}

func TestTrackCommandDetachedSkipsNilCommand(_ *testing.T) {
	// Should not panic with nil command
	TrackCommandDetached(nil, "claude-code", true, "1.0.0")
}

func TestTrackCommandDetachedSkipsHiddenCommands(_ *testing.T) {
	hiddenCmd := &cobra.Command{
		Use:    "__send_analytics",
		Hidden: true,
	}

	// Should not panic and should skip hidden commands
	TrackCommandDetached(hiddenCmd, "claude-code", true, "1.0.0")
}

func TestTrackCommandDetachedRespectsOptOut(t *testing.T) {
	t.Setenv("TRACE_TELEMETRY_OPTIN", "1")
	t.Setenv("TRACE_TELEMETRY_OPTOUT", "1")

	calls := spySendDetached(t)
	cmd := &cobra.Command{
		Use: "status",
	}

	// Opt-out must win over opt-in: nothing is dispatched.
	TrackCommandDetached(cmd, "claude-code", true, "1.0.0")
	if *calls != 0 {
		t.Errorf("expected 0 dispatches when opt-out is set, got %d", *calls)
	}
}

func TestTrackCommandDetachedDisabledByDefault(t *testing.T) {
	// Without TRACE_TELEMETRY_OPTIN, telemetry is off even with no opt-out.
	calls := spySendDetached(t)
	cmd := &cobra.Command{Use: "status"}

	TrackCommandDetached(cmd, "claude-code", true, "1.0.0")
	if *calls != 0 {
		t.Errorf("expected 0 dispatches by default (opt-in), got %d", *calls)
	}
}

func TestTrackCommandDetachedEnabledWithOptIn(t *testing.T) {
	t.Setenv("TRACE_TELEMETRY_OPTIN", "1")
	isolateConfigDir(t)

	calls := spySendDetached(t)
	cmd := &cobra.Command{Use: "status"}

	TrackCommandDetached(cmd, "claude-code", true, "1.0.0")
	if *calls != 1 {
		t.Errorf("expected 1 dispatch with opt-in, got %d", *calls)
	}
}

func TestTrackPluginDetachedDisabledByDefault(t *testing.T) {
	calls := spySendDetached(t)
	TrackPluginDetached("my-plugin", true, "1.0.0")
	if *calls != 0 {
		t.Errorf("expected 0 dispatches by default (opt-in), got %d", *calls)
	}
}

func TestTrackPluginDetachedEnabledWithOptIn(t *testing.T) {
	t.Setenv("TRACE_TELEMETRY_OPTIN", "1")
	isolateConfigDir(t)

	calls := spySendDetached(t)
	TrackPluginDetached("my-plugin", true, "1.0.0")
	if *calls != 1 {
		t.Errorf("expected 1 dispatch with opt-in, got %d", *calls)
	}
}

func TestBuildEventPayloadAgent(t *testing.T) {
	tests := []struct {
		name          string
		inputAgent    string
		expectedAgent string
	}{
		{"defaults empty to auto", "", "auto"},
		{"preserves explicit agent", "claude-code", "claude-code"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "test"}
			payload := BuildEventPayload(cmd, tt.inputAgent, true, "1.0.0")
			if payload == nil {
				t.Fatal("Expected non-nil payload")
				return
			}

			agent, ok := payload.Properties["agent"].(string)
			if !ok {
				t.Fatal("Expected agent property to be a string")
				return
			}
			if agent != tt.expectedAgent {
				t.Errorf("agent = %q, want %q", agent, tt.expectedAgent)
			}
		})
	}
}

func TestSendEventHandlesInvalidJSON(_ *testing.T) {
	// Should not panic with invalid JSON
	SendEvent("invalid json")
	SendEvent("")
	SendEvent("{}")
}
