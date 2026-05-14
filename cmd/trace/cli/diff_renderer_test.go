package cli

import (
	"strings"
	"testing"
)

func TestDiffRenderer_RenderDiff_Empty(t *testing.T) {
	t.Parallel()
	r := NewDiffRenderer()
	result := r.RenderDiff("")
	if result != "" {
		t.Errorf("expected empty string for empty diff, got %q", result)
	}
}

func TestDiffRenderer_RenderDiff_BasicAddDel(t *testing.T) {
	t.Parallel()
	r := NewDiffRenderer()

	diff := `diff --git a/file.go b/file.go
index abc123..def456 100644
--- a/file.go
+++ b/file.go
@@ -1,3 +1,4 @@
 package main

-func old() {}
+func new() {}
+func extra() {}`

	result := r.RenderDiff(diff)

	// Should contain ANSI codes for coloring
	if !strings.Contains(result, "\033[") {
		t.Error("expected ANSI color codes in output")
	}

	// Should contain the addition and deletion content (may have ANSI codes between words)
	if !strings.Contains(result, "new") {
		t.Error("expected addition content 'new' in output")
	}
	if !strings.Contains(result, "old") {
		t.Error("expected deletion content 'old' in output")
	}
}

func TestDiffRenderer_RenderDiff_LineNumbers(t *testing.T) {
	t.Parallel()
	r := NewDiffRenderer()

	diff := `--- a/file.txt
+++ b/file.txt
@@ -5,3 +5,3 @@
 context
-old line
+new line`

	result := r.RenderDiff(diff)

	// Should contain line number 5
	if !strings.Contains(result, "5") {
		t.Error("expected line number 5 in gutter")
	}
}

func TestDiffRenderer_RenderDiff_ContextCollapse(t *testing.T) {
	t.Parallel()
	r := NewDiffRenderer()
	r.CollapseThreshold = 4 // lower threshold for testing
	r.ContextLines = 1

	// Build a diff with many context lines
	var lines []string
	lines = append(lines, "--- a/file.txt")
	lines = append(lines, "+++ b/file.txt")
	lines = append(lines, "@@ -1,12 +1,12 @@")
	for range 10 {
		lines = append(lines, " unchanged line")
	}
	lines = append(lines, "-deleted")
	lines = append(lines, "+added")

	diff := strings.Join(lines, "\n")
	result := r.RenderDiff(diff)

	// Should contain collapse marker
	if !strings.Contains(result, "unchanged lines") {
		t.Error("expected context collapse marker in output")
	}
}

func TestDiffRenderer_RenderDiff_WordLevel(t *testing.T) {
	t.Parallel()
	r := NewDiffRenderer()

	diff := `--- a/file.txt
+++ b/file.txt
@@ -1,1 +1,1 @@
-hello world foo
+hello earth foo`

	result := r.RenderDiff(diff)

	// Should contain word-level highlight codes (background colors)
	if !strings.Contains(result, "\033[30;42m") && !strings.Contains(result, "\033[30;41m") {
		t.Error("expected word-level highlight ANSI codes")
	}
}

func TestDiffRenderer_RenderSideBySide_Empty(t *testing.T) {
	t.Parallel()
	r := NewDiffRenderer()
	result := r.RenderSideBySide("", 80)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestDiffRenderer_RenderSideBySide_Basic(t *testing.T) {
	t.Parallel()
	r := NewDiffRenderer()

	diff := `--- a/file.txt
+++ b/file.txt
@@ -1,3 +1,3 @@
 same
-old
+new`

	result := r.RenderSideBySide(diff, 80)

	// Should contain the separator
	if !strings.Contains(result, "│") {
		t.Error("expected side-by-side separator")
	}

	// Should contain both old and new content
	if !strings.Contains(result, "old") {
		t.Error("expected 'old' in left side")
	}
	if !strings.Contains(result, "new") {
		t.Error("expected 'new' in right side")
	}
}

func TestDiffRenderer_RenderSummary_Empty(t *testing.T) {
	t.Parallel()
	r := NewDiffRenderer()
	result := r.RenderSummary(nil)
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestDiffRenderer_RenderSummary_MultipleFiles(t *testing.T) {
	t.Parallel()
	r := NewDiffRenderer()

	diffs := []string{
		`--- a/foo.go
+++ b/foo.go
@@ -1,2 +1,4 @@
 package foo
+
+func A() {}
+func B() {}`,
		`--- a/bar.go
+++ b/bar.go
@@ -1,3 +1,2 @@
 package bar
-
-func old() {}`,
	}

	result := r.RenderSummary(diffs)

	// Should mention both files
	if !strings.Contains(result, "foo.go") {
		t.Error("expected foo.go in summary")
	}
	if !strings.Contains(result, "bar.go") {
		t.Error("expected bar.go in summary")
	}

	// Should show total
	if !strings.Contains(result, "2 files changed") {
		t.Error("expected '2 files changed' in summary")
	}

	// Should show insertions and deletions
	if !strings.Contains(result, "insertions(+)") {
		t.Error("expected insertions count")
	}
	if !strings.Contains(result, "deletions(-)") {
		t.Error("expected deletions count")
	}
}

func TestComputeLCS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		a, b     []string
		expected []string
	}{
		{
			name:     "identical",
			a:        []string{"a", "b", "c"},
			b:        []string{"a", "b", "c"},
			expected: []string{"a", "b", "c"},
		},
		{
			name:     "one change",
			a:        []string{"hello", " ", "world"},
			b:        []string{"hello", " ", "earth"},
			expected: []string{"hello", " "},
		},
		{
			name:     "empty a",
			a:        nil,
			b:        []string{"a", "b"},
			expected: nil,
		},
		{
			name:     "no common",
			a:        []string{"x", "y"},
			b:        []string{"a", "b"},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := computeLCS(tt.a, tt.b)
			if len(result) != len(tt.expected) {
				t.Errorf("expected LCS length %d, got %d", len(tt.expected), len(result))
				return
			}
			for i := range result {
				if result[i] != tt.expected[i] {
					t.Errorf("LCS[%d] = %q, want %q", i, result[i], tt.expected[i])
				}
			}
		})
	}
}

func TestSplitWords(t *testing.T) {
	t.Parallel()

	result := splitWords("hello world  foo")
	expected := []string{"hello", " ", "world", "  ", "foo"}
	if len(result) != len(expected) {
		t.Fatalf("expected %d tokens, got %d: %v", len(expected), len(result), result)
	}
	for i := range result {
		if result[i] != expected[i] {
			t.Errorf("token[%d] = %q, want %q", i, result[i], expected[i])
		}
	}
}

func TestParseHunkHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input       string
		wantOld     int
		wantNew     int
	}{
		{"@@ -1,3 +1,4 @@", 1, 1},
		{"@@ -10,5 +20,7 @@ func main()", 10, 20},
		{"@@ -100 +200 @@", 100, 200},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			old, newVal := parseHunkHeader(tt.input)
			if old != tt.wantOld {
				t.Errorf("oldStart = %d, want %d", old, tt.wantOld)
			}
			if newVal != tt.wantNew {
				t.Errorf("newStart = %d, want %d", newVal, tt.wantNew)
			}
		})
	}
}

func TestTruncateStr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"a very long string", 10, "a very ..."}, // s[:7] + "..." = 10
		{"abc", 3, "abc"},
		{"abcdef", 3, "abc"},
		{"", 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := truncateStr(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateStr(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestDiffRenderer_PadRight(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		width int
		want  string
	}{
		{"hi", 5, "hi   "},
		{"hello", 5, "hello"},
		{"longer", 3, "longer"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := padRight(tt.input, tt.width)
			if got != tt.want {
				t.Errorf("padRight(%q, %d) = %q, want %q", tt.input, tt.width, got, tt.want)
			}
		})
	}
}

func TestRenderChangeBar(t *testing.T) {
	t.Parallel()

	theme := DefaultTheme()

	// All additions
	bar := renderChangeBar(10, 0, 10, theme)
	if !strings.Contains(bar, "+") {
		t.Error("expected + characters for all-additions bar")
	}

	// All deletions
	bar = renderChangeBar(0, 10, 10, theme)
	if !strings.Contains(bar, "-") {
		t.Error("expected - characters for all-deletions bar")
	}

	// Mixed
	bar = renderChangeBar(5, 5, 10, theme)
	if !strings.Contains(bar, "+") || !strings.Contains(bar, "-") {
		t.Error("expected both + and - characters for mixed bar")
	}

	// Empty
	bar = renderChangeBar(0, 0, 10, theme)
	if bar != "" {
		t.Errorf("expected empty bar for zero changes, got %q", bar)
	}
}

func TestDefaultTheme(t *testing.T) {
	t.Parallel()

	theme := DefaultTheme()
	if theme.Reset == "" {
		t.Error("expected non-empty Reset code")
	}
	if theme.Addition == "" {
		t.Error("expected non-empty Addition color")
	}
	if theme.Deletion == "" {
		t.Error("expected non-empty Deletion color")
	}
}

func TestApplySyntaxHighlight(t *testing.T) {
	t.Parallel()

	theme := DefaultTheme()

	// Go keyword
	result := applySyntaxHighlight("func main() {", theme)
	if !strings.Contains(result, theme.Bold) {
		t.Error("expected bold highlight for 'func' keyword")
	}

	// Python keyword
	result = applySyntaxHighlight("def hello():", theme)
	if !strings.Contains(result, theme.Bold) {
		t.Error("expected bold highlight for 'def' keyword")
	}

	// No keywords
	result = applySyntaxHighlight("hello world", theme)
	if strings.Contains(result, theme.Bold) {
		t.Error("did not expect highlight for non-keyword text")
	}
}
