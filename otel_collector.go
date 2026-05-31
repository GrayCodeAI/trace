package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// RetryConfig controls retry behaviour when exporting span batches.
type RetryConfig struct {
	MaxRetries        int           `json:"max_retries"`
	BackoffMultiplier float64       `json:"backoff_multiplier"`
	InitialBackoff    time.Duration `json:"initial_backoff"`
}

// OTelCollectorConfig holds configuration for the OpenTelemetry collector client.
type OTelCollectorConfig struct {
	Endpoint     string        `json:"endpoint"`
	Insecure     bool          `json:"insecure"`
	Timeout      time.Duration `json:"timeout"`
	BatchSize    int           `json:"batch_size"`
	FlushInterval time.Duration `json:"flush_interval"`
	RetryConfig  RetryConfig   `json:"retry_config"`
}

// DefaultOTelCollectorConfig returns a config with sensible production defaults:
// endpoint localhost:4317, 5 s timeout, batches of 100 spans, flush every 10 s.
func DefaultOTelCollectorConfig() OTelCollectorConfig {
	return OTelCollectorConfig{
		Endpoint:      "localhost:4317",
		Insecure:      true,
		Timeout:       5 * time.Second,
		BatchSize:     100,
		FlushInterval: 10 * time.Second,
		RetryConfig: RetryConfig{
			MaxRetries:        3,
			BackoffMultiplier: 2.0,
			InitialBackoff:    500 * time.Millisecond,
		},
	}
}

// OTelEvent represents a timed annotation attached to a span.
type OTelEvent struct {
	Name       string            `json:"name"`
	Timestamp  time.Time         `json:"timestamp"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// OTelSpan is the in-process representation of an OpenTelemetry span.
type OTelSpan struct {
	TraceID      string            `json:"trace_id"`
	SpanID       string            `json:"span_id"`
	ParentSpanID string            `json:"parent_span_id,omitempty"`
	Name         string            `json:"name"`
	Status       string            `json:"status"`
	StartTime    time.Time         `json:"start_time"`
	EndTime      time.Time         `json:"end_time"`
	Attributes   map[string]string `json:"attributes,omitempty"`
	Events       []OTelEvent       `json:"events,omitempty"`
}

// SpanBatch groups spans for bulk export.
type SpanBatch struct {
	Spans     []OTelSpan `json:"spans"`
	BatchID   string     `json:"batch_id"`
	CreatedAt time.Time  `json:"created_at"`
}

// NewSpanBatch creates a new, empty batch with a unique ID.
func NewSpanBatch() *SpanBatch {
	return &SpanBatch{
		Spans:     make([]OTelSpan, 0),
		BatchID:   uuid.New().String(),
		CreatedAt: time.Now(),
	}
}

// AddSpan appends a span to the batch.
func (sb *SpanBatch) AddSpan(span OTelSpan) {
	sb.Spans = append(sb.Spans, span)
}

// IsFull reports whether the batch has reached maxSize spans.
func (sb *SpanBatch) IsFull(maxSize int) bool {
	return len(sb.Spans) >= maxSize
}

// ToJSON serialises the batch to JSON bytes suitable for export.
func (sb *SpanBatch) ToJSON() ([]byte, error) {
	return json.Marshal(sb)
}

// Size returns the number of spans currently in the batch.
func (sb *SpanBatch) Size() int {
	return len(sb.Spans)
}

// ConvertTranscriptToOTelSpan converts a generic transcript entry (as stored in
// full.jsonl or transcript.jsonl) into an OTelSpan. It recognises common field
// names used across agent transcript formats.
func ConvertTranscriptToOTelSpan(entry map[string]interface{}) OTelSpan {
	span := OTelSpan{
		Attributes: make(map[string]string),
	}

	// Trace / span identifiers.
	span.TraceID = stringField(entry, "trace_id", "traceId")
	span.SpanID = stringField(entry, "span_id", "spanId")
	span.ParentSpanID = stringField(entry, "parent_span_id", "parentSpanId")

	// Name: prefer explicit "name", fall back to "type" or "operation".
	span.Name = stringField(entry, "name", "type", "operation")

	// Status: prefer explicit "status", fall back to "level" or derive from "is_error".
	span.Status = stringField(entry, "status", "level")
	if span.Status == "" {
		if isErr, ok := entry["is_error"]; ok {
			if b, ok := isErr.(bool); ok && b {
				span.Status = "ERROR"
			}
		}
	}

	// Timestamps.
	span.StartTime = timeField(entry, "start_time", "startTime", "timestamp", "ts")
	span.EndTime = timeField(entry, "end_time", "endTime", "end_timestamp")

	// Spread well-known fields into attributes.
	knownScalarKeys := []string{
		"model", "provider", "agent", "cli_version",
		"input_tokens", "output_tokens", "id", "message_id",
		"tool_name", "tool_use_id", "file_path",
	}
	for _, k := range knownScalarKeys {
		if v := stringField(entry, k); v != "" {
			span.Attributes[k] = v
		}
	}

	// Carry gen_ai.* semantic convention attributes if present.
	for k, v := range entry {
		if strings.HasPrefix(k, "gen_ai.") {
			span.Attributes[k] = fmt.Sprintf("%v", v)
		}
	}

	return span
}

// stringField returns the first non-empty string value found among the given
// keys in the map. Returns "" if none match or the value is not a string.
func stringField(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// timeField returns the first parseable time value found among the given keys.
// Recognises both time.Time values and RFC 3339 / RFC 3339Nano strings.
// Returns the zero time if nothing matches.
func timeField(m map[string]interface{}, keys ...string) time.Time {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case time.Time:
			return t
		case string:
			if parsed, err := time.Parse(time.RFC3339Nano, t); err == nil {
				return parsed
			}
			if parsed, err := time.Parse(time.RFC3339, t); err == nil {
				return parsed
			}
		}
	}
	return time.Time{}
}

// BatchSpans splits a flat slice of spans into sub-slices of at most batchSize
// elements each. The final sub-slice may be smaller. A batchSize <= 0 is treated
// as 1.
func BatchSpans(spans []OTelSpan, batchSize int) [][]OTelSpan {
	if batchSize <= 0 {
		batchSize = 1
	}
	if len(spans) == 0 {
		return nil
	}
	var batches [][]OTelSpan
	for i := 0; i < len(spans); i += batchSize {
		end := i + batchSize
		if end > len(spans) {
			end = len(spans)
		}
		batches = append(batches, spans[i:end])
	}
	return batches
}

// OTelCollector is a stub client for exporting span batches to an OpenTelemetry
// collector via OTLP/gRPC or OTLP/HTTP. The actual transport is out of scope;
// SendBatch validates the batch shape and returns nil on success so callers can
// code against the interface without a live collector.
type OTelCollector struct {
	config OTelCollectorConfig
}

// NewOTelCollector creates a collector client with the given config.
func NewOTelCollector(cfg OTelCollectorConfig) *OTelCollector {
	return &OTelCollector{config: cfg}
}

// SendBatch validates that the batch is non-nil and non-empty, then returns nil.
// In a production implementation this would marshal the batch to OTLP protobuf
// and POST to the configured endpoint, honouring config.Timeout and
// config.RetryConfig.
func (c *OTelCollector) SendBatch(ctx context.Context, batch *SpanBatch) error {
	if batch == nil {
		return fmt.Errorf("otel collector: batch must not be nil")
	}
	if len(batch.Spans) == 0 {
		return fmt.Errorf("otel collector: batch %s contains no spans", batch.BatchID)
	}
	if c.config.Endpoint == "" {
		return fmt.Errorf("otel collector: endpoint is not configured")
	}

	// Stub: in production this would perform the actual OTLP export with
	// retry logic driven by c.config.RetryConfig.
	return nil
}
