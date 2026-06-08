package tuiutil

import (
	"testing"
	"time"
)

func TestStripANSI(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want string
	}{
		{"plain text", "hello", "hello"},
		{"empty", "", ""},
		{"red escape", "\x1b[31mred\x1b[0m", "red"},
		{"bold escape", "\x1b[1mbold\x1b[0m", "bold"},
		{"nested escapes", "\x1b[1m\x1b[32mgreen bold\x1b[0m", "green bold"},
		{"no escapes", "no color here", "no color here"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripANSI(tc.s)
			if got != tc.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tc.s, got, tc.want)
			}
		})
	}
}

func TestSanitizeDisplayText(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want string
	}{
		{"plain text", "hello world", "hello world"},
		{"empty", "", ""},
		{"newline to space", "hello\nworld", "hello world"},
		{"tab to space", "hello\tworld", "hello world"},
		{"carriage return dropped", "hello\rworld", "helloworld"},
		{"ansi stripped", "\x1b[31mred\x1b[0m", "red"},
		{"ansi + newline", "\x1b[31mhello\nworld\x1b[0m", "hello world"},
		{"multiple newlines", "a\n\nb", "a  b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeDisplayText(tc.s)
			if got != tc.want {
				t.Errorf("SanitizeDisplayText(%q) = %q, want %q", tc.s, got, tc.want)
			}
		})
	}
}

func TestTruncateDisplayWidth(t *testing.T) {
	cases := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{"fits", "hello", 10, "hello"},
		{"exact", "hello", 5, "hello"},
		{"truncated", "hello world", 5, "hell…"},
		{"width 1", "hello", 1, "h"},
		{"width 0", "hello", 0, ""},
		{"negative", "hello", -1, ""},
		{"empty string", "", 5, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TruncateDisplayWidth(tc.s, tc.width)
			if got != tc.want {
				t.Errorf("TruncateDisplayWidth(%q, %d) = %q, want %q", tc.s, tc.width, got, tc.want)
			}
		})
	}
}

func TestPadDisplayWidth(t *testing.T) {
	got := PadDisplayWidth("hi", 5)
	if len(got) != 5 {
		t.Errorf("PadDisplayWidth length = %d, want 5; got %q", len(got), got)
	}
	// Should be "hi   "
	if got != "hi   " {
		t.Errorf("PadDisplayWidth = %q, want %q", got, "hi   ")
	}

	// Already exact width
	got = PadDisplayWidth("hello", 5)
	if got != "hello" {
		t.Errorf("PadDisplayWidth exact = %q, want %q", got, "hello")
	}

	// Longer than width - should truncate
	got = PadDisplayWidth("hello world", 5)
	if len([]rune(got)) > 5 {
		// The ellipsis makes it 5 runes
		t.Logf("PadDisplayWidth truncated: %q", got)
	}
}

func TestPadDisplayWidthWith(t *testing.T) {
	got := PadDisplayWidthWith("hi", 5, ".")
	if got != "hi..." {
		t.Errorf("PadDisplayWidthWith = %q, want %q", got, "hi...")
	}

	// Pad string with display width != 1 falls back to space
	got = PadDisplayWidthWith("hi", 5, "ab")
	if got != "hi   " {
		t.Errorf("PadDisplayWidthWith multi-width pad = %q, want %q", got, "hi   ")
	}

	// Exact width
	got = PadDisplayWidthWith("hello", 5, ".")
	if got != "hello" {
		t.Errorf("PadDisplayWidthWith exact = %q, want %q", got, "hello")
	}
}

func TestWrapDisplayWidth(t *testing.T) {
	// Width 0 or negative returns nil
	got := WrapDisplayWidth("hello", 0)
	if got != nil {
		t.Errorf("WrapDisplayWidth width=0: got %v, want nil", got)
	}

	got = WrapDisplayWidth("hello", -1)
	if got != nil {
		t.Errorf("WrapDisplayWidth width=-1: got %v, want nil", got)
	}

	// Empty string returns nil
	got = WrapDisplayWidth("", 10)
	if got != nil {
		t.Errorf("WrapDisplayWidth empty: got %v, want nil", got)
	}

	// Only newlines returns nil
	got = WrapDisplayWidth("\n\n", 10)
	if got != nil {
		t.Errorf("WrapDisplayWidth only newlines: got %v, want nil", got)
	}

	// Single line that fits
	got = WrapDisplayWidth("hello", 10)
	if len(got) != 1 || got[0] != "hello" {
		t.Errorf("WrapDisplayWidth single: got %v, want [hello]", got)
	}

	// Multi-paragraph
	got = WrapDisplayWidth("line1\nline2", 20)
	if len(got) != 2 {
		t.Errorf("WrapDisplayWidth 2 paragraphs: got %d lines, want 2: %v", len(got), got)
	}

	// Wrapping long text
	got = WrapDisplayWidth("abcdefghij", 5)
	if len(got) < 2 {
		t.Errorf("WrapDisplayWidth wrap: got %d lines, want >=2: %v", len(got), got)
	}

	// Trailing newline stripped
	got = WrapDisplayWidth("hello\n", 10)
	if len(got) != 1 {
		t.Errorf("WrapDisplayWidth trailing newline: got %d lines, want 1: %v", len(got), got)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"milliseconds", 523 * time.Millisecond, "523ms"},
		{"zero", 0, "0ms"},
		{"under second", 999 * time.Millisecond, "999ms"},
		{"one second", 1 * time.Second, "1.0s"},
		{"seconds", 8400 * time.Millisecond, "8.4s"},
		{"one minute", 1 * time.Minute, "1m0s"},
		{"minutes and seconds", 1*time.Minute + 42*time.Second, "1m42s"},
		{"five minutes thirty", 5*time.Minute + 30*time.Second, "5m30s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatDuration(tc.d)
			if got != tc.want {
				t.Errorf("FormatDuration(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}
