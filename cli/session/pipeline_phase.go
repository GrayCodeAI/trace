package session

// PipelinePhase identifies the step of the Agentless-style pipeline
// (localize → repair → validate) that a session turn belongs to.
//
// These string constants mirror hawk-core-contracts/sessions.Phase so that
// cost records emitted by trace can be correlated with hawk's tok.Tracker
// AggregateByPhase output without importing the contracts package
// (trace is a standalone CLI, not part of the hawk Go module graph).
type PipelinePhase string

const (
	// PipelinePhaseLocalize is the file/symbol identification phase.
	PipelinePhaseLocalize PipelinePhase = "localize"
	// PipelinePhaseRepair is the patch generation phase.
	PipelinePhaseRepair PipelinePhase = "repair"
	// PipelinePhaseValidate is the test/lint verification phase.
	PipelinePhaseValidate PipelinePhase = "validate"
	// PipelinePhaseReview is code review; the most token-intensive phase
	// at 59.4% of total spend (Tokenomics paper, arXiv 2601.14470).
	PipelinePhaseReview PipelinePhase = "review"
	// PipelinePhaseUnknown is the zero value for unattributed turns.
	PipelinePhaseUnknown PipelinePhase = ""
)

// ParsePipelinePhase returns the PipelinePhase matching s, or
// PipelinePhaseUnknown if unrecognised. Matching is case-sensitive to
// stay consistent with the contracts package and JSON encoding.
func ParsePipelinePhase(s string) PipelinePhase {
	switch PipelinePhase(s) {
	case PipelinePhaseLocalize, PipelinePhaseRepair,
		PipelinePhaseValidate, PipelinePhaseReview:
		return PipelinePhase(s)
	}
	return PipelinePhaseUnknown
}
