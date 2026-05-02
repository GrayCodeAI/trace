package stringutil

import "testing"

func TestCollapseWhitespace(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no whitespace changes needed",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "newlines to space",
			input: "hello\nworld",
			want:  "hello world",
		},
		{
			name:  "multiple newlines",
			input: "hello\n\n\nworld",
			want:  "hello world",
		},
		{
			name:  "tabs to space",
			input: "hello\tworld",
			want:  "hello world",
		},
		{
			name:  "mixed whitespace",
			input: "hello\n\t  world",
			want:  "hello world",
		},
		{
			name:  "leading and trailing whitespace",
			input: "  hello world  ",
			want:  "hello world",
		},
		{
			name:  "multiline text",
			input: "Fix the bug\nin the login\npage",
			want:  "Fix the bug in the login page",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "only whitespace",
			input: "  \n\t  ",
			want:  "",
		},
		{
			name:  "carriage return",
			input: "hello\r\nworld",
			want:  "hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CollapseWhitespace(tt.input)
			if got != tt.want {
				t.Errorf("CollapseWhitespace(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxRunes int
		suffix   string
		want     string
	}{
		{
			name:     "ascii no truncation needed",
			input:    "hello",
			maxRunes: 10,
			suffix:   "...",
			want:     "hello",
		},
		{
			name:     "ascii truncation",
			input:    "hello world",
			maxRunes: 8,
			suffix:   "...",
			want:     "hello...",
		},
		{
			name:     "emoji no truncation needed",
			input:    "hello 🎉",
			maxRunes: 10,
			suffix:   "...",
			want:     "hello 🎉",
		},
		{
			name:     "emoji truncation preserves emoji",
			input:    "hello 🎉 world",
			maxRunes: 10,
			suffix:   "...",
			want:     "hello 🎉...",
		},
		{
			name:     "chinese characters",
			input:    "你好世界",
			maxRunes: 3,
			suffix:   "...",
			want:     "...",
		},
		{
			name:     "chinese characters longer",
			input:    "你好世界再见",
			maxRunes: 5,
			suffix:   "...",
			want:     "你好...",
		},
		{
			name:     "mixed unicode needs truncation",
			input:    "hello 世界 🎉 more",
			maxRunes: 10,
			suffix:   "...",
			want:     "hello 世...",
		},
		{
			name:     "empty string",
			input:    "",
			maxRunes: 10,
			suffix:   "...",
			want:     "",
		},
		{
			name:     "exact length",
			input:    "hello",
			maxRunes: 5,
			suffix:   "...",
			want:     "hello",
		},
		{
			name:     "no suffix",
			input:    "hello world",
			maxRunes: 5,
			suffix:   "",
			want:     "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateRunes(tt.input, tt.maxRunes, tt.suffix)
			if got != tt.want {
				t.Errorf("TruncateRunes(%q, %d, %q) = %q, want %q",
					tt.input, tt.maxRunes, tt.suffix, got, tt.want)
			}
		})
	}
}

func TestCapitalizeFirst(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "ascii lowercase",
			input: "hello",
			want:  "Hello",
		},
		{
			name:  "ascii already uppercase",
			input: "Hello",
			want:  "Hello",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "single char",
			input: "h",
			want:  "H",
		},
		{
			name:  "unicode lowercase",
			input: "über",
			want:  "Über",
		},
		{
			name:  "starts with emoji",
			input: "🎉party",
			want:  "🎉party",
		},
		{
			name:  "chinese character",
			input: "你好",
			want:  "你好", // Chinese doesn't have case
		},
		{
			name:  "greek lowercase",
			input: "αβγ",
			want:  "Αβγ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CapitalizeFirst(tt.input)
			if got != tt.want {
				t.Errorf("CapitalizeFirst(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
