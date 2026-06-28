package session_test

import (
	"testing"

	"github.com/GrayCodeAI/trace/cli/session"
)

func TestParsePipelinePhase_Known(t *testing.T) {
	cases := []struct {
		input string
		want  session.PipelinePhase
	}{
		{"localize", session.PipelinePhaseLocalize},
		{"repair", session.PipelinePhaseRepair},
		{"validate", session.PipelinePhaseValidate},
		{"review", session.PipelinePhaseReview},
	}
	for _, tc := range cases {
		got := session.ParsePipelinePhase(tc.input)
		if got != tc.want {
			t.Errorf("ParsePipelinePhase(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestParsePipelinePhase_Unknown(t *testing.T) {
	for _, s := range []string{"", "unknown", "LOCALIZE", "Repair"} {
		if got := session.ParsePipelinePhase(s); got != session.PipelinePhaseUnknown {
			t.Errorf("ParsePipelinePhase(%q) = %q, want PipelinePhaseUnknown", s, got)
		}
	}
}

func TestPipelinePhaseString(t *testing.T) {
	if string(session.PipelinePhaseReview) != "review" {
		t.Errorf("PipelinePhaseReview string = %q, want %q", session.PipelinePhaseReview, "review")
	}
}
