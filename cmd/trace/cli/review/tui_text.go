// Package review — see env.go for package-level rationale.
package review

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

func stripANSI(s string) string {
	return ansi.Strip(s)
}

func sanitizeDisplayText(s string) string {
	stripped := stripANSI(s)
	return strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\t':
			return ' '
		case '\r':
			return -1
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, stripped)
}

// wrapDisplayWidth (ported from upstream for tui_text_test).
func wrapDisplayWidth(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	paragraphs := strings.Split(s, "\n")
	out := make([]string, 0, len(paragraphs))
	for _, p := range paragraphs {
		clean := sanitizeDisplayText(p)
		if clean == "" {
			out = append(out, "")
			continue
		}
		words := strings.Fields(clean)
		line := ""
		for _, w := range words {
			if len(line)+len(w)+1 > width && line != "" {
				out = append(out, line)
				line = w
			} else {
				if line != "" {
					line += " "
				}
				line += w
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func padDisplayWidth(s string, width int) string {
	return padDisplayWidthWith(s, width, " ")
}

func padDisplayWidthWith(s string, width int, pad string) string {
	s = truncateDisplayWidth(s, width)
	remaining := width - ansi.StringWidth(s)
	if remaining <= 0 {
		return s
	}
	if ansi.StringWidth(pad) != 1 {
		return s + strings.Repeat(" ", remaining)
	}
	return s + strings.Repeat(pad, remaining)
}

func truncateDisplayWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	if width == 1 {
		return ansi.Truncate(s, width, "")
	}
	return ansi.Truncate(s, width, "…")
}
