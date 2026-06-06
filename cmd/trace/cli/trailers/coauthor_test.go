package trailers

import (
	"strings"
	"testing"
)

func TestAppendCoAuthoredBy(t *testing.T) {
	t.Parallel()

	const identity = "Claude Code <claude-code@trace.noreply.graycode.ai>"

	tests := []struct {
		name string
		msg  string
		want string
	}{
		{
			name: "plain subject starts a new trailer block",
			msg:  "Add feature",
			want: "Add feature\n\nCo-authored-by: Claude Code <claude-code@trace.noreply.graycode.ai>\n",
		},
		{
			name: "appended to existing trailer block",
			msg:  "Add feature\n\nTrace-Checkpoint: a1b2c3d4e5f6\n",
			want: "Add feature\n\nTrace-Checkpoint: a1b2c3d4e5f6\nCo-authored-by: Claude Code <claude-code@trace.noreply.graycode.ai>\n",
		},
		{
			name: "placed above trailing git comments",
			msg:  "Add feature\n\n# comment line\n",
			want: "Add feature\n\nCo-authored-by: Claude Code <claude-code@trace.noreply.graycode.ai>\n\n# comment line\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := AppendCoAuthoredBy(tt.msg, identity)
			if !strings.Contains(got, "Co-authored-by: "+identity) {
				t.Errorf("result missing trailer.\n got: %q", got)
			}
		})
	}
}

func TestAppendCoAuthoredBy_Idempotent(t *testing.T) {
	t.Parallel()

	const identity = "Codex <codex@trace.noreply.graycode.ai>"
	once := AppendCoAuthoredBy("Fix bug", identity)
	twice := AppendCoAuthoredBy(once, identity)

	if once != twice {
		t.Errorf("AppendCoAuthoredBy not idempotent:\n once: %q\ntwice: %q", once, twice)
	}
	if n := strings.Count(twice, "Co-authored-by:"); n != 1 {
		t.Errorf("expected exactly 1 Co-authored-by trailer, got %d in %q", n, twice)
	}
}

func TestAppendCoAuthoredBy_EmptyIdentityNoOp(t *testing.T) {
	t.Parallel()
	if got := AppendCoAuthoredBy("Fix bug", ""); got != "Fix bug" {
		t.Errorf("empty identity should be a no-op, got %q", got)
	}
	if got := AppendCoAuthoredBy("Fix bug", "   "); got != "Fix bug" {
		t.Errorf("whitespace identity should be a no-op, got %q", got)
	}
}

func TestHasCoAuthoredBy(t *testing.T) {
	t.Parallel()
	const identity = "Gemini CLI <gemini-cli@trace.noreply.graycode.ai>"
	msg := "Subject\n\nCo-authored-by: " + identity + "\n"

	if !HasCoAuthoredBy(msg, identity) {
		t.Errorf("HasCoAuthoredBy = false, want true for present identity")
	}
	if HasCoAuthoredBy(msg, "Someone Else <x@y.z>") {
		t.Errorf("HasCoAuthoredBy = true, want false for absent identity")
	}
	// Key match must be case-insensitive (git treats trailer keys that way).
	mixed := "Subject\n\nco-authored-by: " + identity + "\n"
	if !HasCoAuthoredBy(mixed, identity) {
		t.Errorf("HasCoAuthoredBy should match case-insensitive key")
	}
}
