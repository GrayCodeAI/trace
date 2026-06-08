package cli

import (
	"strings"
	"testing"
	"time"
)

func TestBuildTimeline_Empty(t *testing.T) {
	t.Parallel()

	tl := BuildTimeline(nil)
	if tl == nil {
		t.Fatal("expected non-nil timeline")
	}
	if len(tl.Entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(tl.Entries))
	}
}

func TestBuildTimeline_SortsCheckpoints(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	checkpoints := []CheckpointInfo{
		{ID: "c", Timestamp: base.Add(10 * time.Minute), Description: "Third"},
		{ID: "a", Timestamp: base, Description: "First"},
		{ID: "b", Timestamp: base.Add(5 * time.Minute), Description: "Second"},
	}

	tl := BuildTimeline(checkpoints)

	if len(tl.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(tl.Entries))
	}

	// Verify sorted order
	if tl.Entries[0].Checkpoint.ID != "a" {
		t.Errorf("first entry should be 'a', got %q", tl.Entries[0].Checkpoint.ID)
	}
	if tl.Entries[1].Checkpoint.ID != "b" {
		t.Errorf("second entry should be 'b', got %q", tl.Entries[1].Checkpoint.ID)
	}
	if tl.Entries[2].Checkpoint.ID != "c" {
		t.Errorf("third entry should be 'c', got %q", tl.Entries[2].Checkpoint.ID)
	}
}

func TestBuildTimeline_Duration(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	checkpoints := []CheckpointInfo{
		{ID: "a", Timestamp: base},
		{ID: "b", Timestamp: base.Add(15 * time.Minute)},
	}

	tl := BuildTimeline(checkpoints)

	if tl.Duration != 15*time.Minute {
		t.Errorf("expected duration 15m, got %v", tl.Duration)
	}
	if !tl.StartTime.Equal(base) {
		t.Errorf("expected start time %v, got %v", base, tl.StartTime)
	}
	if !tl.EndTime.Equal(base.Add(15 * time.Minute)) {
		t.Errorf("expected end time %v, got %v", base.Add(15*time.Minute), tl.EndTime)
	}
}

func TestBuildTimeline_Labels(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	checkpoints := []CheckpointInfo{
		{ID: "a", Timestamp: base, Description: "First"},
		{ID: "b", Timestamp: base.Add(5 * time.Minute), Description: "Second"},
	}

	tl := BuildTimeline(checkpoints)

	if tl.Entries[0].Label != "Checkpoint 1" {
		t.Errorf("expected label 'Checkpoint 1', got %q", tl.Entries[0].Label)
	}
	if tl.Entries[1].Label != "Checkpoint 2" {
		t.Errorf("expected label 'Checkpoint 2', got %q", tl.Entries[1].Label)
	}
}

func TestRenderTimeline_Nil(t *testing.T) {
	t.Parallel()
	result := RenderTimeline(nil, 60)
	if result != "" {
		t.Errorf("expected empty string for nil timeline, got %q", result)
	}
}

func TestRenderTimeline_EmptyEntries(t *testing.T) {
	t.Parallel()
	tl := &Timeline{}
	result := RenderTimeline(tl, 60)
	if result != "" {
		t.Errorf("expected empty string for empty timeline, got %q", result)
	}
}

func TestRenderTimeline_Basic(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 12, 10, 1, 0, 0, time.UTC)
	checkpoints := []CheckpointInfo{
		{
			ID:           "abc123",
			Timestamp:    base,
			Description:  "Implement auth",
			FilesChanged: 3,
			Additions:    45,
			Deletions:    12,
		},
		{
			ID:           "def456",
			Timestamp:    base.Add(4 * time.Minute),
			Description:  "Fix token refresh",
			FilesChanged: 1,
			Additions:    8,
			Deletions:    3,
		},
		{
			ID:           "ghi789",
			Timestamp:    base.Add(11 * time.Minute),
			Description:  "Add tests",
			FilesChanged: 2,
			Additions:    89,
			Deletions:    0,
		},
	}

	tl := BuildTimeline(checkpoints)
	tl.SessionID = "abc123"
	result := RenderTimeline(tl, 60)

	// Should contain session header
	if !strings.Contains(result, "Session abc123") {
		t.Error("expected session header")
	}

	// Should contain timestamps
	if !strings.Contains(result, "10:01") {
		t.Error("expected timestamp 10:01")
	}
	if !strings.Contains(result, "10:05") {
		t.Error("expected timestamp 10:05")
	}
	if !strings.Contains(result, "10:12") {
		t.Error("expected timestamp 10:12")
	}

	// Should contain descriptions
	if !strings.Contains(result, "Implement auth") {
		t.Error("expected description 'Implement auth'")
	}
	if !strings.Contains(result, "Fix token refresh") {
		t.Error("expected description 'Fix token refresh'")
	}
	if !strings.Contains(result, "Add tests") {
		t.Error("expected description 'Add tests'")
	}

	// Should contain file stats
	if !strings.Contains(result, "3 files changed (+45, -12)") {
		t.Error("expected '3 files changed (+45, -12)'")
	}
	if !strings.Contains(result, "1 file changed (+8, -3)") {
		t.Error("expected '1 file changed (+8, -3)'")
	}
	if !strings.Contains(result, "2 files changed (+89, -0)") {
		t.Error("expected '2 files changed (+89, -0)'")
	}

	// Should contain timeline markers
	if !strings.Contains(result, "│") {
		t.Error("expected vertical line markers")
	}
	if !strings.Contains(result, "●") {
		t.Error("expected bullet markers")
	}
	if !strings.Contains(result, "├─") {
		t.Error("expected tree branch markers")
	}

	// Should contain duration
	if !strings.Contains(result, "Duration:") {
		t.Error("expected duration line")
	}
}

func TestRenderTimeline_SingleFile(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 12, 14, 30, 0, 0, time.UTC)
	checkpoints := []CheckpointInfo{
		{
			ID:           "x1",
			Timestamp:    base,
			Description:  "One file",
			FilesChanged: 1,
			Additions:    5,
			Deletions:    2,
		},
	}

	tl := BuildTimeline(checkpoints)
	result := RenderTimeline(tl, 50)

	// "file" singular
	if !strings.Contains(result, "1 file changed") {
		t.Error("expected '1 file changed' (singular)")
	}
}

func TestRenderTimeline_MinWidth(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC)
	checkpoints := []CheckpointInfo{
		{ID: "a", Timestamp: base, Description: "Test", FilesChanged: 1, Additions: 1, Deletions: 0},
	}
	tl := BuildTimeline(checkpoints)

	// Very small width should be clamped to 40
	result := RenderTimeline(tl, 10)
	if result == "" {
		t.Error("expected non-empty output even with small width")
	}
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    time.Duration
		expected string
	}{
		{0, "0s"},
		{500 * time.Millisecond, "0s"},
		{5 * time.Second, "5s"},
		{30 * time.Second, "30s"},
		{time.Minute, "1m"},
		{2*time.Minute + 30*time.Second, "2m 30s"},
		{time.Hour, "1h 0m"},
		{time.Hour + 15*time.Minute, "1h 15m"},
		{2*time.Hour + 45*time.Minute, "2h 45m"},
		{-5 * time.Second, "5s"}, // negative durations become positive
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			t.Parallel()
			got := FormatDuration(tt.input)
			if got != tt.expected {
				t.Errorf("FormatDuration(%v) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestRenderTimelineRule(t *testing.T) {
	t.Parallel()

	result := renderTimelineRule("Session abc", 40)

	if !strings.HasPrefix(result, "─── ") {
		t.Error("expected rule prefix")
	}
	if !strings.Contains(result, "Session abc") {
		t.Error("expected label in rule")
	}
	// Should be exactly 40 runes wide
	runes := []rune(result)
	if len(runes) != 40 {
		t.Errorf("expected width 40, got %d", len(runes))
	}
}

func TestCheckpointInfo_Fields(t *testing.T) {
	t.Parallel()

	cp := CheckpointInfo{
		ID:           "test-id",
		Timestamp:    time.Now(),
		Description:  "test description",
		FilesChanged: 5,
		Additions:    100,
		Deletions:    50,
	}

	if cp.ID != "test-id" {
		t.Error("unexpected ID")
	}
	if cp.FilesChanged != 5 {
		t.Error("unexpected FilesChanged")
	}
	if cp.Additions != 100 {
		t.Error("unexpected Additions")
	}
	if cp.Deletions != 50 {
		t.Error("unexpected Deletions")
	}
}
