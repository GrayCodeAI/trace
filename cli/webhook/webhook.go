// Package webhook delivers best-effort HTTP notifications on Trace session
// lifecycle events. Delivery is non-blocking and fail-open: a webhook that is
// slow, unreachable, or returns an error never propagates back to the caller
// and never fails a session. Failures are logged at WARN and dropped.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/GrayCodeAI/trace/cli/logging"
	"github.com/GrayCodeAI/trace/cli/settings"
)

// Lifecycle event names. These are the canonical strings used both in the
// delivered payload's "event" field and in WebhookConfig.Events filtering.
const (
	EventSessionStart      = "session_start"
	EventCheckpointCreated = "checkpoint_created"
	EventSessionEnd        = "session_end"
	EventError             = "error"
)

// defaultTimeout bounds each POST when the config does not specify one. Kept
// short so a hung endpoint cannot stall a lifecycle hook for long.
const defaultTimeout = 3 * time.Second

// httpClient is the interface satisfied by *http.Client, narrowed so tests can
// inject a stub without a live server.
type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Notifier delivers events to a set of configured webhook endpoints.
type Notifier struct {
	urls    []string
	events  []string // allowlist; empty means "all events"
	timeout time.Duration
	client  httpClient
}

// NewNotifier builds a Notifier from the given config. It returns nil when no
// endpoints are configured, so callers can treat a nil *Notifier as a no-op
// (every method is nil-safe).
func NewNotifier(cfg *settings.WebhookConfig) *Notifier {
	if cfg.IsZero() {
		return nil
	}
	timeout := defaultTimeout
	if cfg.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	return &Notifier{
		urls:    slices.Clone(cfg.URLs),
		events:  slices.Clone(cfg.Events),
		timeout: timeout,
		client:  &http.Client{Timeout: timeout},
	}
}

// LoadNotifier loads webhook config from Trace settings and returns a Notifier.
// Returns nil (a valid no-op) when settings cannot be loaded or no webhooks are
// configured — webhook delivery must never block on configuration errors.
func LoadNotifier(ctx context.Context) *Notifier {
	s, err := settings.Load(ctx)
	if err != nil {
		logging.Debug(logging.WithComponent(ctx, "webhook"),
			"could not load settings for webhooks", slog.String("error", err.Error()))
		return nil
	}
	return NewNotifier(s.Webhooks)
}

// wants reports whether the given event should be delivered under the
// configured event allowlist.
func (n *Notifier) wants(event string) bool {
	if len(n.events) == 0 {
		return true
	}
	return slices.Contains(n.events, event)
}

// Notify delivers an event to every configured endpoint. It is best-effort and
// never returns an error: failures are logged and dropped. The payload is the
// JSON object {event, session_id, timestamp, ...extra}. Calling on a nil
// Notifier is a no-op.
//
// Delivery runs synchronously against the per-POST timeout so the caller can
// reason about the upper bound. Callers on a hot path that cannot tolerate even
// the bounded wait should use NotifyAsync.
func (n *Notifier) Notify(ctx context.Context, event, sessionID string, extra map[string]any) {
	if n == nil || !n.wants(event) {
		return
	}

	payload := make(map[string]any, len(extra)+3)
	for k, v := range extra {
		payload[k] = v
	}
	// Reserved keys always win over caller-provided extras.
	payload["event"] = event
	payload["session_id"] = sessionID
	payload["timestamp"] = time.Now().UTC().Format(time.RFC3339)

	body, err := json.Marshal(payload)
	if err != nil {
		logging.Warn(logging.WithComponent(ctx, "webhook"),
			"failed to marshal webhook payload",
			slog.String("event", event), slog.String("error", err.Error()))
		return
	}

	logCtx := logging.WithComponent(ctx, "webhook")
	for _, url := range n.urls {
		n.post(logCtx, url, event, body)
	}
}

// NotifyAsync delivers an event on a detached goroutine and returns
// immediately, so even the bounded per-POST wait stays off the caller's path.
// It uses context.Background so a cancelled caller context does not abort
// in-flight delivery. Best-effort; failures are logged.
func (n *Notifier) NotifyAsync(event, sessionID string, extra map[string]any) {
	if n == nil || !n.wants(event) {
		return
	}
	go n.Notify(context.Background(), event, sessionID, extra)
}

func (n *Notifier) post(ctx context.Context, url, event string, body []byte) {
	reqCtx, cancel := context.WithTimeout(ctx, n.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		logging.Warn(ctx, "failed to build webhook request",
			slog.String("event", event), slog.String("url", url),
			slog.String("error", err.Error()))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "trace-webhook")

	resp, err := n.client.Do(req)
	if err != nil {
		logging.Warn(ctx, "webhook delivery failed",
			slog.String("event", event), slog.String("url", url),
			slog.String("error", err.Error()))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logging.Warn(ctx, "webhook endpoint returned non-2xx",
			slog.String("event", event), slog.String("url", url),
			slog.Int("status", resp.StatusCode))
		return
	}
	logging.Debug(ctx, "webhook delivered",
		slog.String("event", event), slog.String("url", url),
		slog.Int("status", resp.StatusCode))
}
