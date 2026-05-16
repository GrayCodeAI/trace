package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Theme holds customizable ANSI color codes for diff rendering.
type Theme struct {
	Addition   string // ANSI code for added lines
	Deletion   string // ANSI code for deleted lines
	Context    string // ANSI code for context lines
	LineNumber string // ANSI code for line number gutter
	Header     string // ANSI code for diff headers
	WordAdd    string // ANSI code for word-level additions
	WordDel    string // ANSI code for word-level deletions
	Collapse   string // ANSI code for collapsed section marker
	Reset      string // ANSI reset code
	Bold       string // ANSI bold
	FilePath   string // ANSI code for file paths in summary
	StatAdd    string // ANSI code for +N in summary
	StatDel    string // ANSI code for -N in summary
}

// DefaultTheme returns a color theme inspired by delta/lazygit.
func DefaultTheme() Theme {
	return Theme{
		Addition:   "\033[32m",     // green
		Deletion:   "\033[31m",     // red
		Context:    "\033[0m",      // default
		LineNumber: "\033[38;5;8m", // gray
		Header:     "\033[1;36m",   // bold cyan
		WordAdd:    "\033[30;42m",  // black on green background
		WordDel:    "\033[30;41m",  // black on red background
		Collapse:   "\033[38;5;8m", // gray
		Reset:      "\033[0m",
		Bold:       "\033[1m",
		FilePath:   "\033[1;37m", // bold white
		StatAdd:    "\033[32m",   // green
		StatDel:    "\033[31m",   // red
	}
}

// DiffRenderer renders unified diffs with ANSI coloring,
// line numbers, word-level highlighting, and context collapse.
type DiffRenderer struct {
	Theme             Theme
	ContextLines      int // number of context lines to show before collapsing (default 3)
	CollapseThreshold int // minimum unchanged lines to collapse (default 8)
}

// NewDiffRenderer creates a DiffRenderer with default settings.
func NewDiffRenderer() *DiffRenderer {
	return &DiffRenderer{
		Theme:             DefaultTheme(),
		ContextLines:      3,
		CollapseThreshold: 8,
	}
}

// RenderDiff takes a unified diff string and returns ANSI-colored output with
// line numbers, syntax highlighting hints, green/red coloring, word-level diff
// highlighting, and context collapse.
func (r *DiffRenderer) RenderDiff(diff string) string {
	if diff == "" {
		return ""
	}

	lines := strings.Split(diff, "\n")
	var out strings.Builder

	var oldLine, newLine int
	var contextBlock []string
	inHunk := false

	for i, line := range lines {
		// Diff header lines
		if strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index ") {
			r.flushContext(&out, &contextBlock, oldLine, newLine)
			out.WriteString(r.Theme.Header + line + r.Theme.Reset + "\n")
			continue
		}
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			r.flushContext(&out, &contextBlock, oldLine, newLine)
			out.WriteString(r.Theme.Header + line + r.Theme.Reset + "\n")
			continue
		}

		// Hunk header: @@ -a,b +c,d @@
		if strings.HasPrefix(line, "@@") {
			r.flushContext(&out, &contextBlock, oldLine, newLine)
			oldLine, newLine = parseHunkHeader(line)
			inHunk = true
			out.WriteString(r.Theme.Header + line + r.Theme.Reset + "\n")
			continue
		}

		if !inHunk {
			out.WriteString(line + "\n")
			continue
		}

		// Addition
		if strings.HasPrefix(line, "+") {
			r.flushContext(&out, &contextBlock, oldLine, newLine)
			// Look for paired deletion for word-level diff
			rendered := r.renderAddition(line, lines, i)
			gutterNew := r.formatGutter("", newLine)
			out.WriteString(gutterNew + rendered + "\n")
			newLine++
			continue
		}

		// Deletion
		if strings.HasPrefix(line, "-") {
			r.flushContext(&out, &contextBlock, oldLine, newLine)
			// Look ahead for paired addition for word-level diff
			rendered := r.renderDeletion(line, lines, i)
			gutterOld := r.formatGutter(oldLine, "")
			out.WriteString(gutterOld + rendered + "\n")
			oldLine++
			continue
		}

		// Context line
		contextBlock = append(contextBlock, line)
	}

	// Flush remaining context
	r.flushContext(&out, &contextBlock, oldLine, newLine)

	return out.String()
}

// flushContext writes accumulated context lines, collapsing large blocks.
func (r *DiffRenderer) flushContext(out *strings.Builder, block *[]string, oldStart, newStart int) {
	if len(*block) == 0 {
		return
	}

	threshold := r.CollapseThreshold
	if threshold <= 0 {
		threshold = 8
	}
	ctxLines := r.ContextLines
	if ctxLines <= 0 {
		ctxLines = 3
	}

	lines := *block
	if len(lines) >= threshold {
		// Show first ctxLines
		showBefore := ctxLines
		if showBefore > len(lines) {
			showBefore = len(lines)
		}
		showAfter := ctxLines
		if showAfter > len(lines)-showBefore {
			showAfter = len(lines) - showBefore
		}

		oldNum := oldStart - len(lines)
		newNum := newStart - len(lines)

		for j := range showBefore {
			gutter := r.formatGutter(oldNum+j, newNum+j)
			out.WriteString(gutter + r.Theme.Context + lines[j] + r.Theme.Reset + "\n")
		}

		collapsed := len(lines) - showBefore - showAfter
		if collapsed > 0 {
			marker := fmt.Sprintf("  ... %d unchanged lines ...", collapsed)
			out.WriteString(r.Theme.Collapse + marker + r.Theme.Reset + "\n")
		}

		for j := len(lines) - showAfter; j < len(lines); j++ {
			gutter := r.formatGutter(oldNum+j, newNum+j)
			out.WriteString(gutter + r.Theme.Context + lines[j] + r.Theme.Reset + "\n")
		}
	} else {
		oldNum := oldStart - len(lines)
		newNum := newStart - len(lines)
		for j, l := range lines {
			gutter := r.formatGutter(oldNum+j, newNum+j)
			out.WriteString(gutter + r.Theme.Context + l + r.Theme.Reset + "\n")
		}
	}

	*block = nil
}

// formatGutter formats the line number gutter. Arguments can be int or string.
func (r *DiffRenderer) formatGutter(old, newVal interface{}) string {
	oldStr := formatGutterNum(old)
	newStr := formatGutterNum(newVal)
	return fmt.Sprintf("%s%4s %4s │%s ", r.Theme.LineNumber, oldStr, newStr, r.Theme.Reset)
}

func formatGutterNum(v interface{}) string {
	switch n := v.(type) {
	case int:
		if n <= 0 {
			return ""
		}
		return strconv.Itoa(n)
	case string:
		return n
	default:
		return ""
	}
}

// renderAddition renders an addition line with word-level highlighting
// when a paired deletion is found nearby.
func (r *DiffRenderer) renderAddition(line string, allLines []string, idx int) string {
	// Look backward for a paired deletion
	content := line[1:] // strip leading +
	if idx > 0 && strings.HasPrefix(allLines[idx-1], "-") {
		delContent := allLines[idx-1][1:]
		highlighted := r.highlightWordChanges(delContent, content, true)
		return r.Theme.Addition + "+" + highlighted + r.Theme.Reset
	}
	return r.Theme.Addition + line + r.Theme.Reset
}

// renderDeletion renders a deletion line with word-level highlighting
// when a paired addition is found nearby.
func (r *DiffRenderer) renderDeletion(line string, allLines []string, idx int) string {
	content := line[1:] // strip leading -
	if idx+1 < len(allLines) && strings.HasPrefix(allLines[idx+1], "+") {
		addContent := allLines[idx+1][1:]
		highlighted := r.highlightWordChanges(content, addContent, false)
		return r.Theme.Deletion + "-" + highlighted + r.Theme.Reset
	}
	return r.Theme.Deletion + line + r.Theme.Reset
}

// highlightWordChanges compares old and new content word-by-word and highlights
// the changed words. If isAdd is true, highlights additions; otherwise deletions.
func (r *DiffRenderer) highlightWordChanges(oldContent, newContent string, isAdd bool) string {
	oldWords := splitWords(oldContent)
	newWords := splitWords(newContent)

	// Compute longest common subsequence
	lcs := computeLCS(oldWords, newWords)

	var result strings.Builder
	if isAdd {
		// Highlight words in newContent that are not in LCS
		lcsIdx := 0
		for _, w := range newWords {
			if lcsIdx < len(lcs) && w == lcs[lcsIdx] {
				result.WriteString(w)
				lcsIdx++
			} else {
				result.WriteString(r.Theme.WordAdd + w + r.Theme.Reset + r.Theme.Addition)
			}
		}
	} else {
		// Highlight words in oldContent that are not in LCS
		lcsIdx := 0
		for _, w := range oldWords {
			if lcsIdx < len(lcs) && w == lcs[lcsIdx] {
				result.WriteString(w)
				lcsIdx++
			} else {
				result.WriteString(r.Theme.WordDel + w + r.Theme.Reset + r.Theme.Deletion)
			}
		}
	}

	return result.String()
}

// splitWords splits a string into tokens preserving whitespace as separate tokens.
var wordSplitRe = regexp.MustCompile(`\S+|\s+`)

func splitWords(s string) []string {
	return wordSplitRe.FindAllString(s, -1)
}

// computeLCS returns the longest common subsequence of two string slices.
func computeLCS(a, b []string) []string {
	m, n := len(a), len(b)
	if m == 0 || n == 0 {
		return nil
	}

	// DP table
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			switch {
			case a[i-1] == b[j-1]:
				dp[i][j] = dp[i-1][j-1] + 1
			case dp[i-1][j] >= dp[i][j-1]:
				dp[i][j] = dp[i-1][j]
			default:
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	// Backtrack
	result := make([]string, dp[m][n])
	idx := dp[m][n] - 1
	i, j := m, n
	for i > 0 && j > 0 {
		switch {
		case a[i-1] == b[j-1]:
			result[idx] = a[i-1]
			idx--
			i--
			j--
		case dp[i-1][j] >= dp[i][j-1]:
			i--
		default:
			j--
		}
	}

	return result
}

// RenderSideBySide renders a unified diff in side-by-side view
// within the given terminal width.
func (r *DiffRenderer) RenderSideBySide(diff string, width int) string {
	if diff == "" {
		return ""
	}

	// Each side gets half the width minus separator
	sideWidth := (width - 3) / 2 // 3 for " │ " separator
	if sideWidth < 20 {
		sideWidth = 20
	}

	lines := strings.Split(diff, "\n")
	var out strings.Builder

	var oldLine, newLine int

	for _, line := range lines {
		// Header lines span full width
		if strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			out.WriteString(r.Theme.Header + line + r.Theme.Reset + "\n")
			continue
		}

		if strings.HasPrefix(line, "@@") {
			oldLine, newLine = parseHunkHeader(line)
			out.WriteString(r.Theme.Header + line + r.Theme.Reset + "\n")
			continue
		}

		switch {
		case strings.HasPrefix(line, "-"):
			content := truncateStr(line[1:], sideWidth-6)
			leftNum := fmt.Sprintf("%4d", oldLine)
			left := r.Theme.LineNumber + leftNum + r.Theme.Reset + " " +
				r.Theme.Deletion + padRight(content, sideWidth-5) + r.Theme.Reset
			right := strings.Repeat(" ", sideWidth)
			out.WriteString(left + " │ " + right + "\n")
			oldLine++
		case strings.HasPrefix(line, "+"):
			content := truncateStr(line[1:], sideWidth-6)
			left := strings.Repeat(" ", sideWidth)
			rightNum := fmt.Sprintf("%4d", newLine)
			right := r.Theme.LineNumber + rightNum + r.Theme.Reset + " " +
				r.Theme.Addition + padRight(content, sideWidth-5) + r.Theme.Reset
			out.WriteString(left + " │ " + right + "\n")
			newLine++
		default:
			// Context line
			content := line
			if len(content) > 0 && content[0] == ' ' {
				content = content[1:]
			}
			content = truncateStr(content, sideWidth-6)
			leftNum := fmt.Sprintf("%4d", oldLine)
			rightNum := fmt.Sprintf("%4d", newLine)
			left := r.Theme.LineNumber + leftNum + r.Theme.Reset + " " + padRight(content, sideWidth-5)
			right := r.Theme.LineNumber + rightNum + r.Theme.Reset + " " + padRight(content, sideWidth-5)
			out.WriteString(left + " │ " + right + "\n")
			oldLine++
			newLine++
		}
	}

	return out.String()
}

// RenderSummary renders a summary view showing files changed with +/- stats.
func (r *DiffRenderer) RenderSummary(diffs []string) string {
	if len(diffs) == 0 {
		return ""
	}

	type fileStat struct {
		path      string
		additions int
		deletions int
	}

	var stats []fileStat
	totalAdd := 0
	totalDel := 0

	for _, diff := range diffs {
		lines := strings.Split(diff, "\n")
		var currentFile string
		var adds, dels int

		for _, line := range lines {
			switch {
			case strings.HasPrefix(line, "+++ b/"):
				if currentFile != "" {
					stats = append(stats, fileStat{currentFile, adds, dels})
					totalAdd += adds
					totalDel += dels
				}
				currentFile = line[6:]
				adds = 0
				dels = 0
			case strings.HasPrefix(line, "+++ ") && !strings.HasPrefix(line, "+++ a/"):
				if currentFile != "" {
					stats = append(stats, fileStat{currentFile, adds, dels})
					totalAdd += adds
					totalDel += dels
				}
				currentFile = strings.TrimPrefix(line, "+++ ")
				adds = 0
				dels = 0
			case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") && currentFile != "":
				adds++
			case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") && currentFile != "":
				dels++
			}
		}
		if currentFile != "" {
			stats = append(stats, fileStat{currentFile, adds, dels})
			totalAdd += adds
			totalDel += dels
		}
	}

	var out strings.Builder

	// Find longest file path for alignment
	maxPath := 0
	for _, s := range stats {
		if len(s.path) > maxPath {
			maxPath = len(s.path)
		}
	}
	if maxPath > 50 {
		maxPath = 50
	}

	for _, s := range stats {
		path := s.path
		if len(path) > maxPath {
			path = "..." + path[len(path)-maxPath+3:]
		}
		bar := renderChangeBar(s.additions, s.deletions, 20, r.Theme)
		fmt.Fprintf(
			&out, " %s%-*s%s %s%+4d%s %s%+4d%s %s\n",
			r.Theme.FilePath, maxPath, path, r.Theme.Reset,
			r.Theme.StatAdd, s.additions, r.Theme.Reset,
			r.Theme.StatDel, -s.deletions, r.Theme.Reset,
			bar,
		)
	}

	// Summary line
	fmt.Fprintf(
		&out, "\n %s%d files changed%s, %s%d insertions(+)%s, %s%d deletions(-)%s\n",
		r.Theme.Bold, len(stats), r.Theme.Reset,
		r.Theme.StatAdd, totalAdd, r.Theme.Reset,
		r.Theme.StatDel, totalDel, r.Theme.Reset,
	)

	return out.String()
}

// renderChangeBar creates a visual bar showing the ratio of additions to deletions.
func renderChangeBar(adds, dels, width int, theme Theme) string {
	total := adds + dels
	if total == 0 {
		return ""
	}

	addWidth := (adds * width) / total
	delWidth := width - addWidth

	if adds > 0 && addWidth == 0 {
		addWidth = 1
		delWidth = width - 1
	}
	if dels > 0 && delWidth == 0 {
		delWidth = 1
		addWidth = width - 1
	}

	bar := theme.StatAdd + strings.Repeat("+", addWidth) + theme.Reset +
		theme.StatDel + strings.Repeat("-", delWidth) + theme.Reset

	return bar
}

// parseHunkHeader extracts start line numbers from a unified diff hunk header.
// Format: @@ -old,count +new,count @@
func parseHunkHeader(line string) (oldStart, newStart int) {
	re := regexp.MustCompile(`@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
	matches := re.FindStringSubmatch(line)
	if len(matches) >= 3 {
		oldStart, _ = strconv.Atoi(matches[1]) //nolint:errcheck // regex guarantees digits
		newStart, _ = strconv.Atoi(matches[2]) //nolint:errcheck // regex guarantees digits
	}
	return
}

// truncateStr truncates a string to fit within maxLen characters.
func truncateStr(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// applySyntaxHighlight applies basic syntax highlighting for common languages.
// It detects keywords for Go, Python, JavaScript, and TypeScript.
func applySyntaxHighlight(line string, theme Theme) string {
	// Go keywords
	goKeywords := []string{
		"func", "package", "import", "var", "const", "type", "struct",
		"interface", "map", "chan", "go", "defer", "return", "if", "else",
		"for", "range", "switch", "case", "select", "break", "continue",
	}

	// Python keywords
	pyKeywords := []string{
		"def", "class", "import", "from", "return", "if", "elif", "else",
		"for", "while", "try", "except", "finally", "with", "as", "yield",
		"lambda", "pass", "raise", "None", "True", "False",
	}

	// JS/TS keywords
	jsKeywords := []string{
		"function", "const", "let", "var", "class", "export", "import",
		"from", "return", "if", "else", "for", "while", "switch", "case",
		"async", "await", "try", "catch", "finally", "throw", "new",
		"interface", "type", "enum",
	}

	allKeywords := make(map[string]bool)
	for _, k := range goKeywords {
		allKeywords[k] = true
	}
	for _, k := range pyKeywords {
		allKeywords[k] = true
	}
	for _, k := range jsKeywords {
		allKeywords[k] = true
	}

	words := strings.Fields(line)
	for i, w := range words {
		// Strip punctuation for lookup
		clean := strings.TrimRight(w, "({[;:,")
		if allKeywords[clean] {
			words[i] = theme.Bold + w + theme.Reset
		}
	}

	return strings.Join(words, " ")
}
