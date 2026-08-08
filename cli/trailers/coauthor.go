package trailers

import (
	"fmt"
	"regexp"
	"strings"
)

// CoAuthoredByTrailerKey identifies a co-author on a commit, following the
// git-conventional trailer format.
const CoAuthoredByTrailerKey = "Co-authored-by"

// coAuthoredByRegex matches a Co-authored-by trailer line, capturing the
// "Name <email>" identity. Case-insensitive on the key to match git's own
// case-insensitive trailer handling.
var coAuthoredByRegex = regexp.MustCompile(`(?i)^Co-authored-by:\s*(.+)$`)

// HasCoAuthoredBy reports whether the message already contains a
// Co-authored-by trailer for the given "Name <email>" identity. The key match
// is case-insensitive, the identity comparison is exact.
func HasCoAuthoredBy(message, identity string) bool {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return false
	}
	for _, line := range strings.Split(message, "\n") {
		m := coAuthoredByRegex.FindStringSubmatch(strings.TrimSpace(line))
		if len(m) > 1 && strings.TrimSpace(m[1]) == identity {
			return true
		}
	}
	return false
}

// AppendCoAuthoredBy appends a "Co-authored-by: <identity>" trailer in
// trailer-aware format. The call is idempotent for a given identity, and an
// empty identity returns the message unchanged.
func AppendCoAuthoredBy(message, identity string) string {
	identity = strings.TrimSpace(identity)
	if identity == "" || HasCoAuthoredBy(message, identity) {
		return message
	}
	return appendTrailerLine(message, fmt.Sprintf("%s: %s", CoAuthoredByTrailerKey, identity))
}
