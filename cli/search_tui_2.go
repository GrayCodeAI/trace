package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	glamour "charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	glamourstyles "charm.land/glamour/v2/styles"
	"github.com/GrayCodeAI/trace/cli/search"
	"github.com/GrayCodeAI/trace/cli/stringutil"
)

// computeColumns calculates column widths from terminal width.
func computeColumns(width int) columnLayout {
	const (
		ageWidth    = 10
		idWidth     = 12
		repoMin     = 10
		authorWidth = 14
		gaps        = 5 // spaces between columns
	)

	remaining := width - ageWidth - idWidth - authorWidth - gaps
	if remaining < 20 {
		remaining = 20
	}

	branchWidth := max(remaining*18/100, 8)
	repoWidth := max(remaining*18/100, repoMin)
	promptWidth := remaining - branchWidth - repoWidth
	if promptWidth < 12 {
		reclaim := 12 - promptWidth
		repoWidth = max(repoWidth-reclaim, repoMin)
		promptWidth = remaining - branchWidth - repoWidth
	}

	return columnLayout{
		age:    ageWidth,
		id:     idWidth,
		branch: branchWidth,
		repo:   repoWidth,
		prompt: promptWidth,
		author: authorWidth,
	}
}

// ─── Formatting Helpers ──────────────────────────────────────────────────────

// formatSearchAge parses an RFC3339 timestamp and returns a relative time string.
func formatSearchAge(createdAt string) string {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return createdAt
	}
	return timeAgo(t)
}

// formatCommit renders commit SHA + message, handling nil pointers.
func formatCommit(sha, message *string) string {
	s := derefStr(sha, "—")
	if sha != nil && len(*sha) > 7 {
		s = (*sha)[:7]
	}
	msg := derefStr(message, "")
	if msg != "" {
		s += "  " + msg
	}
	return s
}

// derefStr returns the dereferenced string pointer, or fallback if nil.
func derefStr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
}

// ─── Snippet Markdown ────────────────────────────────────────────────────────

// renderSnippetMarkdown renders a search snippet as markdown using glamour v2.
// It is used in the full-screen checkpoint detail view where the snippet has
// room to breathe; the inline detail card keeps plain word-wrapping. On any
// renderer error or impractically narrow widths it falls back to wrapText.
//
// dark must be detected before bubbletea owns the terminal — querying termenv
// inside the Update loop races against bubbletea's stdin reader and stalls.
//
// A fresh TermRenderer is built per call. *TermRenderer carries shared mutable
// state via ansi.RenderContext.blockStack, so caching the renderer would
// require serialising every Render call; construction is cheap (just goldmark
// + ANSI option setup, no chroma init unless a fenced code block forces it),
// so we just rebuild and avoid the concurrency hazard altogether.
func renderSnippetMarkdown(snippet string, width int, dark bool) string {
	if width < 20 {
		return wrapText(snippet, width)
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(snippetMarkdownStyles(dark)),
		glamour.WithWordWrap(width),
		glamour.WithPreservedNewLines(),
	)
	if err != nil {
		return wrapText(snippet, width)
	}
	rendered, err := renderer.Render(snippet)
	if err != nil {
		return wrapText(snippet, width)
	}
	return strings.TrimRight(rendered, "\n")
}

// snippetMarkdownStyles returns a glamour style config tailored for inline
// snippets. Foreground colours are nilled across every text-bearing element
// so the snippet inherits the terminal's default foreground colour. ANSI
// palette numbers like "234" embedded in glamour's stock styles get remapped
// by terminal themes and produce unreadable colours on cream / Solarized
// backgrounds — letting the terminal pick the colour avoids that entirely.
//
// IMPORTANT: this function copies a package-level glamourstyles var by value,
// then re-assigns its pointer fields. *Re-assigning* (`= nil`, `= &x`) is
// safe — it rebinds the local field. *Dereferencing* through the pointer
// (`*s.Document.Color = "x"`) would mutate the shared global and pollute
// every other glamour caller in the process. Don't do that.
func snippetMarkdownStyles(dark bool) ansi.StyleConfig {
	var s ansi.StyleConfig
	if dark {
		s = glamourstyles.DarkStyleConfig
	} else {
		s = glamourstyles.LightStyleConfig
	}
	zero := uint(0)
	s.Document.Margin = &zero
	s.Document.BlockPrefix = ""
	s.Document.BlockSuffix = ""

	// Null foreground on every primitive that contributes to flowing text so
	// nothing relies on theme-remappable ANSI palette numbers. Code/CodeBlock
	// keep their styling because BackgroundColor is enough to differentiate
	// them visually.
	s.Document.Color = nil
	s.Paragraph.Color = nil
	s.Text.Color = nil
	s.BlockQuote.Color = nil
	s.Strong.Color = nil
	s.Emph.Color = nil
	s.Strikethrough.Color = nil
	s.Heading.Color = nil
	s.H1.Color = nil
	s.H2.Color = nil
	s.H3.Color = nil
	s.H4.Color = nil
	s.H5.Color = nil
	s.H6.Color = nil
	s.Item.Color = nil
	s.Enumeration.Color = nil
	s.List.Color = nil

	// Links are the one place we *want* a colour: an underline alone is easy
	// to miss inline. Use an explicit hex so it survives theme remapping.
	linkColor := searchAccentBlue
	s.Link.Color = &linkColor
	s.LinkText.Color = &linkColor

	return s
}

// ─── Static Fallback ─────────────────────────────────────────────────────────

// renderSearchStatic writes a non-interactive table for accessible mode.
func renderSearchStatic(w io.Writer, results []search.Result, query string, total int, styles statusStyles) {
	fmt.Fprintf(w, "Found %d checkpoints matching %q\n\n", total, query)

	cols := computeColumns(styles.width)

	fmt.Fprintf(
		w, "%-*s %-*s %-*s %-*s %-*s %-*s\n",
		cols.age, "AGE",
		cols.id, "ID",
		cols.branch, "BRANCH",
		cols.repo, "REPO",
		cols.prompt, "PROMPT",
		cols.author, "AUTHOR",
	)

	for _, r := range results {
		age := formatSearchAge(r.Data.CreatedAt)
		id := stringutil.TruncateRunes(r.Data.ID, cols.id, "")
		branch := stringutil.TruncateRunes(r.Data.Branch, cols.branch, "...")
		repo := stringutil.TruncateRunes(r.Data.Org+"/"+r.Data.Repo, cols.repo, "...")
		prompt := stringutil.TruncateRunes(
			stringutil.CollapseWhitespace(r.Data.Prompt), cols.prompt, "...",
		)
		author := stringutil.TruncateRunes(derefStr(r.Data.AuthorUsername, r.Data.Author), cols.author, "...")

		fmt.Fprintf(
			w, "%-*s %-*s %-*s %-*s %-*s %-*s\n",
			cols.age, age,
			cols.id, id,
			cols.branch, branch,
			cols.repo, repo,
			cols.prompt, prompt,
			cols.author, author,
		)
	}
}
