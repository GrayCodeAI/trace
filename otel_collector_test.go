package trace

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultOTelCollectorConfig_SensibleValues(t *testing.T) {
	cfg := DefaultOTelCollectorConfig()

	assert.Equal(t, "localhost:4317", cfg.Endpoint, "default endpoint should be localhost:4317")
	assert.True(t, cfg.Insecure, "default insecure should be true for local dev")
	assert.Equal(t, 5*time.Second, cfg.Timeout, "default timeout should be 5s")
	assert.Equal(t, 100, cfg.BatchSize, "default batch size should be 100")
	assert.Equal(t, 10*time.Second, cfg.FlushInterval, "default flush interval should be 10s")

	// Retry config
	assert.Equal(t, 3, cfg.RetryConfig.MaxRetries, "default max retries should be 3")
	assert.Equal(t, 2.0, cfg.RetryConfig.BackoffMultiplier, "default backoff multiplier should be 2.0")
	assert.Equal(t, 500*time.Millisecond, cfg.RetryConfig.InitialBackoff, "default initial backoff should be 500ms")
}

func TestNewSpanBatch_CreatesWithNonEmptyID(t *testing.T) {
	batch := NewSpanBatch()

	require.NotNil(t, batch)
	assert.NotEmpty(t, batch.BatchID, "batch ID should be non-empty")
	assert.NotNil(t, batch.Spans, "Spans slice should be initialised")
	assert.Len(t, batch.Spans, 0, "new batch should have zero spans")
	assert.False(t, batch.CreatedAt.IsZero(), "CreatedAt should be set")
}

func TestAddSpan_IncrementsSize(t *testing.T) {
	batch := NewSpanBatch()
	assert.Equal(t, 0, batch.Size())

	span := OTelSpan{
		TraceID:   "trace-1",
		SpanID:    "span-1",
		Name:      "test-span",
		Status:    "OK",
		StartTime: time.Now(),
		EndTime:   time.Now().Add(time.Second),
	}
	batch.AddSpan(span)
	assert.Equal(t, 1, batch.Size())

	batch.AddSpan(OTelSpan{SpanID: "span-2", Name: "second"})
	assert.Equal(t, 2, batch.Size())
}

func TestIsFull_ReturnsTrueAtCapacity_FalseBelow(t *testing.T) {
	batch := NewSpanBatch()

	assert.False(t, batch.IsFull(3), "empty batch should not be full at capacity 3")

	batch.AddSpan(OTelSpan{SpanID: "s1"})
	assert.False(t, batch.IsFull(3), "batch with 1 span should not be full at capacity 3")

	batch.AddSpan(OTelSpan{SpanID: "s2"})
	assert.False(t, batch.IsFull(3), "batch with 2 spans should not be full at capacity 3")

	batch.AddSpan(OTelSpan{SpanID: "s3"})
	assert.True(t, batch.IsFull(3), "batch with 3 spans should be full at capacity 3")

	batch.AddSpan(OTelSpan{SpanID: "s4"})
	assert.True(t, batch.IsFull(3), "batch exceeding capacity should be full")
}

func TestToJSON_RoundTrip_PreservesSpanData(t *testing.T) {
	batch := NewSpanBatch()

	now := time.Now().Truncate(time.Microsecond)
	span := OTelSpan{
		TraceID:   "abc-123",
		SpanID:    "def-456",
		Name:      "llm.call",
		Status:    "OK",
		StartTime: now,
		EndTime:   now.Add(500 * time.Millisecond),
		Attributes: map[string]string{
			"model": "gpt-4",
		},
		Events: []OTelEvent{
			{
				Name:      "token_count",
				Timestamp: now.Add(250 * time.Millisecond),
				Attributes: map[string]string{
					"input_tokens": "100",
				},
			},
		},
	}
	batch.AddSpan(span)

	data, err := batch.ToJSON()
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var decoded SpanBatch
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, batch.BatchID, decoded.BatchID)
	assert.Len(t, decoded.Spans, 1)

	got := decoded.Spans[0]
	assert.Equal(t, "abc-123", got.TraceID)
	assert.Equal(t, "def-456", got.SpanID)
	assert.Equal(t, "llm.call", got.Name)
	assert.Equal(t, "OK", got.Status)
	assert.Equal(t, "gpt-4", got.Attributes["model"])
	require.Len(t, got.Events, 1)
	assert.Equal(t, "token_count", got.Events[0].Name)
}

func TestBatchSpans_SplitsCorrectly(t *testing.T) {
	spans := make([]OTelSpan, 7)
	for i := range spans {
		spans[i] = OTelSpan{SpanID: "s" + string(rune('0'+i))}
	}

	t.Run("exact fit", func(t *testing.T) {
		batches := BatchSpans(spans, 7)
		require.Len(t, batches, 1)
		assert.Len(t, batches[0], 7)
	})

	t.Run("partial last batch", func(t *testing.T) {
		batches := BatchSpans(spans, 3)
		require.Len(t, batches, 3)
		assert.Len(t, batches[0], 3)
		assert.Len(t, batches[1], 3)
		assert.Len(t, batches[2], 1)
	})

	t.Run("empty input", func(t *testing.T) {
		batches := BatchSpans(nil, 5)
		assert.Nil(t, batches)
	})

	t.Run("batch size one", func(t *testing.T) {
		batches := BatchSpans(spans[:2], 1)
		require.Len(t, batches, 2)
		assert.Len(t, batches[0], 1)
		assert.Len(t, batches[1], 1)
	})

	t.Run("batch size zero defaults to one", func(t *testing.T) {
		batches := BatchSpans(spans[:3], 0)
		require.Len(t, batches, 3)
	})
}

func TestConvertTranscriptToOTelSpan_ValidInput(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	entry := map[string]interface{}{
		"trace_id":   "t-001",
		"span_id":    "sp-001",
		"name":       "agent.think",
		"status":     "OK",
		"start_time": now.Format(time.RFC3339),
		"end_time":   now.Add(2 * time.Second).Format(time.RFC3339),
		"model":      "claude-sonnet-4-20250514",
		"provider":   "anthropic",
	}

	span := ConvertTranscriptToOTelSpan(entry)

	assert.Equal(t, "t-001", span.TraceID)
	assert.Equal(t, "sp-001", span.SpanID)
	assert.Equal(t, "agent.think", span.Name)
	assert.Equal(t, "OK", span.Status)
	assert.Equal(t, now, span.StartTime)
	assert.Equal(t, now.Add(2*time.Second), span.EndTime)
	assert.Equal(t, "claude-sonnet-4-20250514", span.Attributes["model"])
	assert.Equal(t, "anthropic", span.Attributes["provider"])
}

func TestConvertTranscriptToOTelSpan_AlternateFieldNames(t *testing.T) {
	entry := map[string]interface{}{
		"traceId":  "t-alt",
		"spanId":   "sp-alt",
		"type":     "tool.call",
		"level":    "ERROR",
		"ts":       time.Now().Format(time.RFC3339Nano),
		"is_error": true,
	}

	span := ConvertTranscriptToOTelSpan(entry)

	assert.Equal(t, "t-alt", span.TraceID)
	assert.Equal(t, "sp-alt", span.SpanID)
	assert.Equal(t, "tool.call", span.Name)
	assert.Equal(t, "ERROR", span.Status)
}

func TestConvertTranscriptToOTelSpan_DerivesStatusFromIsError(t *testing.T) {
	entry := map[string]interface{}{
		"name":     "failing_step",
		"is_error": true,
	}

	span := ConvertTranscriptToOTelSpan(entry)

	assert.Equal(t, "ERROR", span.Status, "status should be derived from is_error=true")
}

func TestConvertTranscriptToOTelSpan_GenAIAttributes(t *testing.T) {
	entry := map[string]interface{}{
		"name":            "llm.request",
		"gen_ai.model":    "gpt-4",
		"gen_ai.tokens":   42,
		"gen_ai.provider": "openai",
	}

	span := ConvertTranscriptToOTelSpan(entry)

	assert.Equal(t, "gpt-4", span.Attributes["gen_ai.model"])
	assert.Equal(t, "42", span.Attributes["gen_ai.tokens"])
	assert.Equal(t, "openai", span.Attributes["gen_ai.provider"])
}

func TestSpanBatch_EmptyBatchSerialization(t *testing.T) {
	batch := NewSpanBatch()

	data, err := batch.ToJSON()
	require.NoError(t, err)

	var decoded SpanBatch
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, batch.BatchID, decoded.BatchID)
	assert.Empty(t, decoded.Spans, "empty batch should serialise with zero spans")
}

func TestSpanBatch_MultipleSpansWithDifferentStatuses(t *testing.T) {
	batch := NewSpanBatch()

	statuses := []string{"OK", "ERROR", "UNSET", "TIMEOUT"}
	for i, st := range statuses {
		batch.AddSpan(OTelSpan{
			TraceID:   "trace-multi",
			SpanID:    "span-" + string(rune('A'+i)),
			Name:      "step." + st,
			Status:    st,
			StartTime: time.Now(),
			EndTime:   time.Now().Add(time.Duration(i+1) * time.Second),
		})
	}

	assert.Equal(t, 4, batch.Size())

	data, err := batch.ToJSON()
	require.NoError(t, err)

	var decoded SpanBatch
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	require.Len(t, decoded.Spans, 4)
	for i, st := range statuses {
		assert.Equal(t, st, decoded.Spans[i].Status, "span %d should preserve status %s", i, st)
	}
}

func TestOTelCollector_SendBatch_ValidBatch_ReturnsNil(t *testing.T) {
	cfg := DefaultOTelCollectorConfig()
	collector := NewOTelCollector(cfg)

	batch := NewSpanBatch()
	batch.AddSpan(OTelSpan{
		TraceID:   "trace-send",
		SpanID:    "span-send",
		Name:      "test.send",
		Status:    "OK",
		StartTime: time.Now(),
		EndTime:   time.Now().Add(time.Second),
	})

	err := collector.SendBatch(context.Background(), batch)
	assert.NoError(t, err, "SendBatch with a valid non-empty batch should return nil")
}

func TestOTelCollector_SendBatch_NilBatch_ReturnsError(t *testing.T) {
	cfg := DefaultOTelCollectorConfig()
	collector := NewOTelCollector(cfg)

	err := collector.SendBatch(context.Background(), nil)
	assert.Error(t, err, "SendBatch with nil batch should return an error")
	assert.Contains(t, err.Error(), "must not be nil")
}

func TestOTelCollector_SendBatch_EmptyBatch_ReturnsError(t *testing.T) {
	cfg := DefaultOTelCollectorConfig()
	collector := NewOTelCollector(cfg)

	batch := NewSpanBatch()
	err := collector.SendBatch(context.Background(), batch)
	assert.Error(t, err, "SendBatch with empty batch should return an error")
	assert.Contains(t, err.Error(), "contains no spans")
}
