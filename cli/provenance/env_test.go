package provenance

import "testing"

func TestIsReviewEntry(t *testing.T) {
	cases := []struct {
		name string
		kv   string
		want bool
	}{
		{"review session", ReviewSession + "=abc123", true},
		{"review agent", ReviewAgent + "=my-agent", true},
		{"review skills", ReviewSkills + "=skill1,skill2", true},
		{"review prompt", ReviewPrompt + "=hello", true},
		{"review starting sha", ReviewStartingSHA + "=deadbeef", true},
		{"investigate session", InvestigateSession + "=abc", false},
		{"random string", "FOO=bar", false},
		{"empty", "", false},
		{"partial match no eq", ReviewSession, false},
		{"review session longer key", ReviewSession + "_EXTRA=val", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsReviewEntry(tc.kv)
			if got != tc.want {
				t.Errorf("IsReviewEntry(%q) = %v, want %v", tc.kv, got, tc.want)
			}
		})
	}
}

func TestIsInvestigateEntry(t *testing.T) {
	cases := []struct {
		name string
		kv   string
		want bool
	}{
		{"investigate session", InvestigateSession + "=abc123", true},
		{"investigate agent", InvestigateAgent + "=my-agent", true},
		{"investigate run id", InvestigateRunID + "=abcdef123456", true},
		{"investigate topic", InvestigateTopic + "=bug hunt", true},
		{"investigate findings", InvestigateFindingsDoc + "=doc.md", true},
		{"investigate state", InvestigateStateDoc + "=state.md", true},
		{"investigate starting sha", InvestigateStartingSHA + "=deadbeef", true},
		{"review session", ReviewSession + "=abc", false},
		{"random string", "FOO=bar", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsInvestigateEntry(tc.kv)
			if got != tc.want {
				t.Errorf("IsInvestigateEntry(%q) = %v, want %v", tc.kv, got, tc.want)
			}
		})
	}
}

func TestIsEntry(t *testing.T) {
	cases := []struct {
		name string
		kv   string
		want bool
	}{
		{"review session", ReviewSession + "=abc", true},
		{"investigate session", InvestigateSession + "=abc", true},
		{"investigate run id", InvestigateRunID + "=abc", true},
		{"random", "FOO=bar", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsEntry(tc.kv)
			if got != tc.want {
				t.Errorf("IsEntry(%q) = %v, want %v", tc.kv, got, tc.want)
			}
		})
	}
}

func TestIsValidRunID(t *testing.T) {
	cases := []struct {
		name  string
		runID string
		want  bool
	}{
		{"valid 12 hex lowercase", "abcdef123456", true},
		{"valid all zeros", "000000000000", true},
		{"valid all fs", "ffffffffffff", true},
		{"empty", "", false},
		{"too short", "abc123", false},
		{"too long", "abcdef1234567", false},
		{"uppercase", "ABCDEF123456", false},
		{"mixed case", "aBcDeF123456", false},
		{"non-hex", "gggggggggggg", false},
		{"contains spaces", "abcdef 12345", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsValidRunID(tc.runID)
			if got != tc.want {
				t.Errorf("IsValidRunID(%q) = %v, want %v", tc.runID, got, tc.want)
			}
		})
	}
}

func TestConstants(t *testing.T) {
	// Verify constant values are stable API
	if ReviewSession != "TRACE_REVIEW_SESSION" {
		t.Errorf("ReviewSession = %q, want TRACE_REVIEW_SESSION", ReviewSession)
	}
	if InvestigateSession != "TRACE_INVESTIGATE_SESSION" {
		t.Errorf("InvestigateSession = %q, want TRACE_INVESTIGATE_SESSION", InvestigateSession)
	}
	if InvestigateRunID != "TRACE_INVESTIGATE_RUN_ID" {
		t.Errorf("InvestigateRunID = %q, want TRACE_INVESTIGATE_RUN_ID", InvestigateRunID)
	}
}
