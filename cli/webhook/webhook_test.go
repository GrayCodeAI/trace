package webhook

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GrayCodeAI/trace/cli/settings"
)

func TestNotify_DeliversExpectedPayload(t *testing.T) {
	t.Parallel()

	type received struct {
		contentType string
		body        map[string]any
	}
	var (
		mu   sync.Mutex
		hits []received
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		mu.Lock()
		hits = append(hits, received{contentType: r.Header.Get("Content-Type"), body: body})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(&settings.WebhookConfig{URLs: []string{srv.URL}})
	if n == nil {
		t.Fatal("expected non-nil notifier for configured URL")
	}

	n.Notify(context.Background(), EventSessionStart, "sess-123", map[string]any{"agent": "claude-code"})

	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(hits))
	}
	got := hits[0]
	if got.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got.contentType)
	}
	if got.body["event"] != EventSessionStart {
		t.Errorf("event = %v, want %q", got.body["event"], EventSessionStart)
	}
	if got.body["session_id"] != "sess-123" {
		t.Errorf("session_id = %v, want sess-123", got.body["session_id"])
	}
	if got.body["agent"] != "claude-code" {
		t.Errorf("agent = %v, want claude-code", got.body["agent"])
	}
	ts, ok := got.body["timestamp"].(string)
	if !ok || ts == "" {
		t.Errorf("timestamp missing or not a string: %v", got.body["timestamp"])
	} else if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Errorf("timestamp %q not RFC3339: %v", ts, err)
	}
}

func TestNotify_ReservedKeysOverrideExtras(t *testing.T) {
	t.Parallel()

	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(&settings.WebhookConfig{URLs: []string{srv.URL}})
	// Caller tries to clobber the reserved keys; the notifier must win.
	n.Notify(context.Background(), EventSessionEnd, "real-id",
		map[string]any{"event": "spoofed", "session_id": "spoofed"})

	if body["event"] != EventSessionEnd {
		t.Errorf("event = %v, want %q (reserved key must win)", body["event"], EventSessionEnd)
	}
	if body["session_id"] != "real-id" {
		t.Errorf("session_id = %v, want real-id (reserved key must win)", body["session_id"])
	}
}

func TestNotify_UnreachableURLDoesNotError(t *testing.T) {
	t.Parallel()

	// Point at a server that is immediately closed → connection refused.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	n := NewNotifier(&settings.WebhookConfig{URLs: []string{url}, TimeoutSeconds: 1})

	done := make(chan struct{})
	go func() {
		// Must return without panicking or blocking; signature has no error.
		n.Notify(context.Background(), EventError, "sess", map[string]any{"reason": "boom"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Notify did not return for an unreachable URL")
	}
}

func TestNotify_Non2xxDoesNotError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := NewNotifier(&settings.WebhookConfig{URLs: []string{srv.URL}})
	// Should simply log and return; no panic, no hang.
	n.Notify(context.Background(), EventSessionStart, "sess", nil)
}

func TestNotify_EventFilter(t *testing.T) {
	t.Parallel()

	var count atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(&settings.WebhookConfig{
		URLs:   []string{srv.URL},
		Events: []string{EventSessionEnd},
	})

	n.Notify(context.Background(), EventSessionStart, "sess", nil) // filtered out
	n.Notify(context.Background(), EventSessionEnd, "sess", nil)   // allowed

	if got := count.Load(); got != 1 {
		t.Fatalf("expected 1 delivery (only session_end), got %d", got)
	}
}

func TestNotify_MultipleURLs(t *testing.T) {
	t.Parallel()

	var a, b atomic.Int64
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		a.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		b.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srvB.Close()

	n := NewNotifier(&settings.WebhookConfig{URLs: []string{srvA.URL, srvB.URL}})
	n.Notify(context.Background(), EventCheckpointCreated, "sess", nil)

	if a.Load() != 1 || b.Load() != 1 {
		t.Fatalf("expected both endpoints hit once, got a=%d b=%d", a.Load(), b.Load())
	}
}

func TestNewNotifier_NilForEmptyConfig(t *testing.T) {
	t.Parallel()

	if NewNotifier(nil) != nil {
		t.Error("nil config should yield nil notifier")
	}
	if NewNotifier(&settings.WebhookConfig{}) != nil {
		t.Error("config with no URLs should yield nil notifier")
	}
}

func TestNotify_NilNotifierIsNoOp(t *testing.T) {
	t.Parallel()
	var n *Notifier
	// Must not panic.
	n.Notify(context.Background(), EventSessionStart, "sess", nil)
	n.NotifyAsync(EventSessionStart, "sess", nil)
}
