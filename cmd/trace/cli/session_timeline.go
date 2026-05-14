package cli

import (
	"fmt"
	"strings"
	"time"
)

// CheckpointInfo holds metadata about a single checkpoint for timeline display.
type CheckpointInfo struct {
	ID           string
	Timestamp    time.Time
	Description  string
	FilesChanged int
	Additions    int
	Deletions    int
}

// TimelineEntry represents a rendered entry in the timeline.
type TimelineEntry struct {
	Checkpoint CheckpointInfo
	Label      string // formatted label for display
}

// Timeline represents a visual session timeline with entries and metadata.
type Timeline struct {
	SessionID string
	Entries   []TimelineEntry
	StartTime time.Time
	EndTime   time.Time
	Duration  time.Duration
}

// BuildTimeline creates a Timeline from a slice of CheckpointInfo,
// sorted by timestamp.
func BuildTimeline(checkpoints []CheckpointInfo) *Timeline {
	if len(checkpoints) == 0 {
		return &Timeline{}
	}

	// Sort checkpoints by timestamp (insertion sort for simplicity with stdlib only)
	sorted := make([]CheckpointInfo, len(checkpoints))
	copy(sorted, checkpoints)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Timestamp.Before(sorted[j-1].Timestamp); j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}

	entries := make([]TimelineEntry, len(sorted))
	for i, cp := range sorted {
		label := fmt.Sprintf("Checkpoint %d", i+1)
		entries[i] = TimelineEntry{
			Checkpoint: cp,
			Label:      label,
		}
	}

	t := &Timeline{
		Entries:   entries,
		StartTime: sorted[0].Timestamp,
		EndTime:   sorted[len(sorted)-1].Timestamp,
		Duration:  sorted[len(sorted)-1].Timestamp.Sub(sorted[0].Timestamp),
	}

	return t
}

// RenderTimeline renders an ASCII timeline of checkpoints within the given width.
// Output format:
//
//	--- Session abc123 -----------------------------------------
//	|  10:01  Checkpoint 1 * "Implement auth"
//	|         +-- 3 files changed (+45, -12)
//	|  10:05  Checkpoint 2 * "Fix token refresh"
//	|         +-- 1 file changed (+8, -3)
//	------------------------------------------------------------
func RenderTimeline(t *Timeline, width int) string {
	if t == nil || len(t.Entries) == 0 {
		return ""
	}

	if width < 40 {
		width = 40
	}

	var out strings.Builder

	// Header
	sessionLabel := "Session"
	if t.SessionID != "" {
		sessionLabel = "Session " + t.SessionID
	}
	header := renderTimelineRule(sessionLabel, width)
	out.WriteString(header + "\n")

	// Entries
	for _, entry := range t.Entries {
		cp := entry.Checkpoint
		timeStr := cp.Timestamp.Format("15:04")

		// Main line: │  HH:MM  Checkpoint N ● "Description"
		desc := ""
		if cp.Description != "" {
			desc = fmt.Sprintf(" %q", cp.Description)
		}
		mainLine := fmt.Sprintf("│  %s  %s ●%s", timeStr, entry.Label, desc)
		out.WriteString(mainLine + "\n")

		// Stats line: │         ├─ N files changed (+A, -D)
		filesWord := "files"
		if cp.FilesChanged == 1 {
			filesWord = "file"
		}
		statsLine := fmt.Sprintf("│         ├─ %d %s changed (+%d, -%d)",
			cp.FilesChanged, filesWord, cp.Additions, cp.Deletions)
		out.WriteString(statsLine + "\n")
	}

	// Footer
	footer := strings.Repeat("─", width)
	out.WriteString(footer + "\n")

	// Duration summary
	if t.Duration > 0 {
		fmt.Fprintf(&out, "  Duration: %s\n", FormatDuration(t.Duration))
	}

	return out.String()
}

// renderTimelineRule creates a header rule like: ─── Session abc123 ──────────────
func renderTimelineRule(label string, width int) string {
	prefix := "─── "
	suffix := " "
	contentLen := len([]rune(prefix)) + len([]rune(label)) + len([]rune(suffix))
	trailing := width - contentLen
	if trailing < 1 {
		trailing = 1
	}
	return prefix + label + suffix + strings.Repeat("─", trailing)
}

// FormatDuration formats a duration in a human-friendly way.
// Examples: "5s", "2m 30s", "1h 15m", "2h 0m"
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}

	if d < time.Second {
		return "0s"
	}

	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	if minutes > 0 {
		if seconds > 0 {
			return fmt.Sprintf("%dm %ds", minutes, seconds)
		}
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%ds", seconds)
}
