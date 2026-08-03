package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/posthog/posthog-go"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var (
	// PostHogAPIKey is set at build time for production.
	// Empty by default to prevent telemetry during IDE builds and local development.
	PostHogAPIKey = ""
	// PostHogEndpoint is set at build time for production
	PostHogEndpoint = "https://eu.i.posthog.com"
)

// EventPayload represents the data passed to the detached subprocess.
// Note: APIKey and Endpoint are intentionally excluded to avoid exposing
// them in process listings (ps/top). SendEvent reads them from package-level vars.
type EventPayload struct {
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Properties map[string]any `json:"properties"`
	Timestamp  time.Time      `json:"timestamp"`
}

// userConfigDir returns the base user config directory. It is a var so
// tests can isolate the anonymous ID file from the real user config dir.
var userConfigDir = os.UserConfigDir

// anonymousID returns a stable per-install anonymous identifier. The ID is a
// random 128-bit value generated on first use and cached in the user config
// dir. A random ID is used instead of a hardware-derived machine ID so
// telemetry events cannot be correlated to a physical machine.
func anonymousID() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "trace")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "telemetry-id")
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

// telemetryEnabled reports whether telemetry is permitted. Telemetry is
// opt-in: nothing is sent unless TRACE_TELEMETRY_OPTIN=1 is set. The legacy
// TRACE_TELEMETRY_OPTOUT variable continues to force-disable even when
// opt-in is enabled.
func telemetryEnabled() bool {
	if os.Getenv("TRACE_TELEMETRY_OPTOUT") != "" {
		return false
	}
	return os.Getenv("TRACE_TELEMETRY_OPTIN") == "1"
}

// sendDetached dispatches a payload to the detached analytics subprocess.
// It is a var so tests can spy on whether telemetry was dispatched.
var sendDetached = spawnDetachedAnalytics

// silentLogger suppresses PostHog log output - expected for CLI best-effort telemetry
type silentLogger struct{}

func (silentLogger) Logf(_ string, _ ...interface{})   {}
func (silentLogger) Debugf(_ string, _ ...interface{}) {}
func (silentLogger) Warnf(_ string, _ ...interface{})  {}
func (silentLogger) Errorf(_ string, _ ...interface{}) {}

// BuildEventPayload constructs the event payload for tracking.
// Exported for testing. Returns nil if the payload cannot be built.
func BuildEventPayload(cmd *cobra.Command, agent string, isTraceEnabled bool, version string) *EventPayload {
	if cmd == nil {
		return nil
	}

	// Get an anonymous per-install ID for distinct_id
	machineID, err := anonymousID()
	if err != nil {
		return nil
	}

	// Collect flag names (not values) for privacy
	var flags []string
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		flags = append(flags, flag.Name)
	})

	selectedAgent := agent
	if selectedAgent == "" {
		selectedAgent = "auto"
	}

	properties := map[string]any{
		"command":        cmd.CommandPath(),
		"agent":          selectedAgent,
		"isTraceEnabled": isTraceEnabled,
		"cli_version":    version,
		"os":             runtime.GOOS,
		"arch":           runtime.GOARCH,
	}

	if len(flags) > 0 {
		properties["flags"] = strings.Join(flags, ",")
	}

	return &EventPayload{
		Event:      "cli_command_executed",
		DistinctID: machineID,
		Properties: properties,
		Timestamp:  time.Now(),
	}
}

// TrackCommandDetached tracks a command execution by spawning a detached subprocess.
// This returns immediately without blocking the CLI.
//
// Telemetry is opt-in: it is only sent when TRACE_TELEMETRY_OPTIN=1 is set.
// The legacy TRACE_TELEMETRY_OPTOUT environment variable (any non-empty
// value) force-disables telemetry regardless.
func TrackCommandDetached(cmd *cobra.Command, agent string, isTraceEnabled bool, version string) {
	// Opt-in gate: nothing is sent unless explicitly enabled.
	if !telemetryEnabled() {
		return
	}

	if cmd == nil {
		return
	}

	if cmd.Hidden {
		return
	}

	payload := BuildEventPayload(cmd, agent, isTraceEnabled, version)
	if payload == nil {
		return
	}

	if payloadJSON, err := json.Marshal(payload); err == nil {
		sendDetached(string(payloadJSON))
	}
}

// TrackPluginDetached tracks a plugin invocation by spawning a detached subprocess.
// This returns immediately without blocking the CLI.
func TrackPluginDetached(pluginName string, isTraceEnabled bool, version string) {
	if !telemetryEnabled() {
		return
	}

	payload := BuildPluginEventPayload(pluginName, isTraceEnabled, version)
	if payload == nil {
		return
	}

	if payloadJSON, err := json.Marshal(payload); err == nil {
		sendDetached(string(payloadJSON))
	}
}

// BuildPluginEventPayload creates a telemetry payload for a plugin invocation.
func BuildPluginEventPayload(pluginName string, isTraceEnabled bool, version string) *EventPayload {
	if pluginName == "" {
		return nil
	}
	return &EventPayload{
		Event: "plugin_invocation",
		Properties: map[string]interface{}{
			"plugin_name":   pluginName,
			"trace_enabled": isTraceEnabled,
			"cli_version":   version,
			"$lib":          "trace-cli",
			"$lib_version":  version,
		},
	}
}

// SendEvent processes an event payload in the detached subprocess.
// This is called by the hidden __send_analytics command.
func SendEvent(payloadJSON string) {
	var payload EventPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return
	}

	// Create PostHog client - no need for fast timeouts since we're detached
	// Read API key and endpoint from package-level vars (not passed via argv for security)
	client, err := posthog.NewWithConfig(PostHogAPIKey, posthog.Config{
		Endpoint:     PostHogEndpoint,
		Logger:       silentLogger{},
		DisableGeoIP: posthog.Ptr(true),
	})
	if err != nil {
		return
	}
	defer func() {
		_ = client.Close()
	}()

	// Build properties
	props := posthog.NewProperties()
	for k, v := range payload.Properties {
		props.Set(k, v)
	}

	//nolint:errcheck // Best effort telemetry - don't block on result
	_ = client.Enqueue(posthog.Capture{
		DistinctId: payload.DistinctID,
		Event:      payload.Event,
		Properties: props,
		Timestamp:  payload.Timestamp,
	})
}
