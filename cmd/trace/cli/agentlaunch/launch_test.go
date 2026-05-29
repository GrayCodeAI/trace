package agentlaunch

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// TestWithoutReviewOrInvestigateEnv pins the contract that the helper
// strips both TRACE_REVIEW_* and TRACE_INVESTIGATE_* entries from the
// supplied env slice while leaving unrelated entries untouched. This is
// the leak-prevention guarantee for fix-agent launches: a parent shell
// may have inherited stale provenance vars, and the fix session must not
// be tagged as a review or investigate session.
//
// The literal env names below mirror the constants in
// cmd/trace/cli/review/env.go and cmd/trace/cli/investigate/env.go.
// We use literals (not the exported constants) because importing review
// or investigate from this package would create a build cycle: review
// depends on agentlaunch.
func TestWithoutReviewOrInvestigateEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []string
		want     []string
		notWant  []string
		wantSize int
	}{
		{
			name: "strips review and investigate, keeps unrelated",
			input: []string{
				"PATH=/usr/bin",
				"HOME=/home/u",
				"TRACE_REVIEW_SESSION=1",
				"TRACE_REVIEW_AGENT=claude-code",
				"TRACE_REVIEW_SKILLS=[\"/x\"]",
				"TRACE_REVIEW_PROMPT=stale review prompt",
				"TRACE_REVIEW_STARTING_SHA=stale1",
				"TRACE_INVESTIGATE_SESSION=1",
				"TRACE_INVESTIGATE_AGENT=claude-code",
				"TRACE_INVESTIGATE_RUN_ID=abcdef012345",
				"TRACE_INVESTIGATE_TOPIC=topic",
				"TRACE_INVESTIGATE_FINDINGS_DOC=/tmp/f.md",
				"TRACE_INVESTIGATE_STATE_DOC=/tmp/state.json",
				"TRACE_INVESTIGATE_STARTING_SHA=stale2",
			},
			want: []string{
				"PATH=/usr/bin",
				"HOME=/home/u",
			},
			notWant: []string{
				"TRACE_REVIEW_SESSION=1",
				"TRACE_REVIEW_AGENT=claude-code",
				"TRACE_REVIEW_SKILLS=[\"/x\"]",
				"TRACE_REVIEW_PROMPT=stale review prompt",
				"TRACE_REVIEW_STARTING_SHA=stale1",
				"TRACE_INVESTIGATE_SESSION=1",
				"TRACE_INVESTIGATE_AGENT=claude-code",
				"TRACE_INVESTIGATE_RUN_ID=abcdef012345",
				"TRACE_INVESTIGATE_TOPIC=topic",
				"TRACE_INVESTIGATE_FINDINGS_DOC=/tmp/f.md",
				"TRACE_INVESTIGATE_STATE_DOC=/tmp/state.json",
				"TRACE_INVESTIGATE_STARTING_SHA=stale2",
			},
			wantSize: 2,
		},
		{
			name: "no provenance entries: passthrough",
			input: []string{
				"PATH=/usr/bin",
				"FOO=bar",
			},
			want: []string{
				"PATH=/usr/bin",
				"FOO=bar",
			},
			wantSize: 2,
		},
		{
			name:     "empty input: empty output",
			input:    nil,
			wantSize: 0,
		},
		{
			name: "only provenance entries: empty output",
			input: []string{
				"TRACE_REVIEW_SESSION=1",
				"TRACE_INVESTIGATE_SESSION=1",
			},
			notWant: []string{
				"TRACE_REVIEW_SESSION=1",
				"TRACE_INVESTIGATE_SESSION=1",
			},
			wantSize: 0,
		},
		{
			name: "look-alike non-provenance keys survive",
			input: []string{
				"NOT_TRACE_REVIEW_SESSION=1",
				"TRACE_REVIEW_OTHER=keep",      // not a known prefix
				"TRACE_INVESTIGATE_OTHER=keep", // not a known prefix
			},
			want: []string{
				"NOT_TRACE_REVIEW_SESSION=1",
				"TRACE_REVIEW_OTHER=keep",
				"TRACE_INVESTIGATE_OTHER=keep",
			},
			wantSize: 3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := withoutReviewOrInvestigateEnv(tc.input)
			if len(got) != tc.wantSize {
				t.Errorf("len = %d, want %d (got: %v)", len(got), tc.wantSize, got)
			}
			for _, kv := range tc.want {
				if !slices.Contains(got, kv) {
					t.Errorf("missing expected entry %q in %v", kv, got)
				}
			}
			for _, kv := range tc.notWant {
				if slices.Contains(got, kv) {
					t.Errorf("unexpected entry survived strip: %q", kv)
				}
			}
		})
	}
}

// TestWithoutReviewOrInvestigateEnv_DoesNotMutateInput pins that the
// helper returns a fresh slice and never mutates its argument. Callers
// rely on this when they pass `os.Environ()` directly.
func TestWithoutReviewOrInvestigateEnv_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	input := []string{
		"PATH=/usr/bin",
		"TRACE_REVIEW_SESSION=1",
		"TRACE_INVESTIGATE_SESSION=1",
		"HOME=/home/u",
	}
	original := slices.Clone(input)

	_ = withoutReviewOrInvestigateEnv(input)

	if !slices.Equal(input, original) {
		t.Errorf("input was mutated: got %v, want %v", input, original)
	}
}

// TestLaunchFixAgent_EmptyEnvFallback_StripsHostProvenance pins that the
// "cmd.Env == nil → os.Environ()" fallback in LaunchFixAgent still strips
// provenance markers even when they were set on the parent process. A
// future launcher implementation that returns a cmd with no Env would
// otherwise re-import stale provenance via os.Environ() and silently
// re-tag the fix session.
//
// Mirrors the fallback branch exactly: build an empty Env, take the
// os.Environ() path, assert no provenance entries survive.
func TestLaunchFixAgent_EmptyEnvFallback_StripsHostProvenance(t *testing.T) {
	// t.Setenv mutates process global state; cannot run with t.Parallel().
	t.Setenv("TRACE_REVIEW_SESSION", "1")
	t.Setenv("TRACE_REVIEW_AGENT", "claude-code")
	t.Setenv("TRACE_REVIEW_STARTING_SHA", "deadbeefcafe")
	t.Setenv("TRACE_INVESTIGATE_SESSION", "1")
	t.Setenv("TRACE_INVESTIGATE_RUN_ID", "abcdef012345")

	// Drive the exact branch LaunchFixAgent takes when cmd.Env is empty:
	// withoutReviewOrInvestigateEnv(os.Environ()).
	emptyEnv := []string(nil)
	cleaned := withoutReviewOrInvestigateEnv(emptyEnv)
	if len(cleaned) != 0 {
		t.Fatalf("precondition: empty input should yield empty output, got %v", cleaned)
	}
	// Fall back to host env (the branch under test) and re-strip.
	fallback := withoutReviewOrInvestigateEnv(osEnvironForTest())

	for _, kv := range fallback {
		if hasReviewOrInvestigatePrefix(kv) {
			t.Errorf("fallback env still contains provenance entry %q", kv)
		}
	}
}

// osEnvironForTest mirrors os.Environ() via the same call LaunchFixAgent
// uses. Wrapped in a helper so the test reads as a direct simulation of
// the production branch.
func osEnvironForTest() []string {
	return os.Environ()
}

// hasReviewOrInvestigatePrefix is a tiny test helper that mirrors the
// production prefix check without importing provenance (which is fine
// here — the test file lives in the same package as the implementation).
func hasReviewOrInvestigatePrefix(kv string) bool {
	prefixes := []string{
		"TRACE_REVIEW_SESSION=",
		"TRACE_REVIEW_AGENT=",
		"TRACE_REVIEW_SKILLS=",
		"TRACE_REVIEW_PROMPT=",
		"TRACE_REVIEW_STARTING_SHA=",
		"TRACE_INVESTIGATE_SESSION=",
		"TRACE_INVESTIGATE_AGENT=",
		"TRACE_INVESTIGATE_RUN_ID=",
		"TRACE_INVESTIGATE_TOPIC=",
		"TRACE_INVESTIGATE_FINDINGS_DOC=",
		"TRACE_INVESTIGATE_STATE_DOC=",
		"TRACE_INVESTIGATE_STARTING_SHA=",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(kv, p) {
			return true
		}
	}
	return false
}
