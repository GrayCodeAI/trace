// Package provenance owns the env-var contract that lets the lifecycle hook
// recognize a spawned agent process as part of `entire review` or `entire
// investigate`. Both spawn families set their own TRACE_*_* vars on the
// child agent process; the UserPromptSubmit hook reads them to tag the
// in-flight session with the right Kind and provenance metadata.
//
// Single source of truth for the names — review, investigate, and
// agentlaunch (which strips both families before spawning a fix agent) all
// reference this package.
//
// These names are stable API; renaming any constant is a breaking change.
package provenance

import (
	"regexp"
	"strings"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/checkpoint/id"
)

const (
	ReviewSession     = "TRACE_REVIEW_SESSION"
	ReviewAgent       = "TRACE_REVIEW_AGENT"
	ReviewSkills      = "TRACE_REVIEW_SKILLS"
	ReviewPrompt      = "TRACE_REVIEW_PROMPT"
	ReviewStartingSHA = "TRACE_REVIEW_STARTING_SHA"

	InvestigateSession     = "TRACE_INVESTIGATE_SESSION"
	InvestigateAgent       = "TRACE_INVESTIGATE_AGENT"
	InvestigateRunID       = "TRACE_INVESTIGATE_RUN_ID"
	InvestigateTopic       = "TRACE_INVESTIGATE_TOPIC"
	InvestigateFindingsDoc = "TRACE_INVESTIGATE_FINDINGS_DOC"
	InvestigateStateDoc    = "TRACE_INVESTIGATE_STATE_DOC"
	InvestigateStartingSHA = "TRACE_INVESTIGATE_STARTING_SHA"
)

var reviewPrefixes = []string{
	ReviewSession + "=",
	ReviewAgent + "=",
	ReviewSkills + "=",
	ReviewPrompt + "=",
	ReviewStartingSHA + "=",
}

var investigatePrefixes = []string{
	InvestigateSession + "=",
	InvestigateAgent + "=",
	InvestigateRunID + "=",
	InvestigateTopic + "=",
	InvestigateFindingsDoc + "=",
	InvestigateStateDoc + "=",
	InvestigateStartingSHA + "=",
}

// IsReviewEntry reports whether kv is a "KEY=VALUE" entry whose key is one
// of the TRACE_REVIEW_* contract variables.
func IsReviewEntry(kv string) bool {
	return hasAnyPrefix(kv, reviewPrefixes)
}

// IsInvestigateEntry reports whether kv is a "KEY=VALUE" entry whose key is
// one of the TRACE_INVESTIGATE_* contract variables.
func IsInvestigateEntry(kv string) bool {
	return hasAnyPrefix(kv, investigatePrefixes)
}

// IsEntry reports whether kv is a "KEY=VALUE" entry from either family.
// agentlaunch uses this to strip provenance markers before spawning a fix
// session so the child is not tagged as review or investigate.
func IsEntry(kv string) bool {
	return IsReviewEntry(kv) || IsInvestigateEntry(kv)
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// runIDPattern matches a valid investigation run ID: exactly 12 lowercase
// hex characters. Re-uses checkpoint/id.Pattern so the format stays in
// lockstep with the checkpoint-ID format used elsewhere in the codebase.
var runIDPattern = regexp.MustCompile("^" + id.Pattern + "$")

// IsValidRunID reports whether runID is exactly 12 lowercase hex
// characters. Lives here (next to the InvestigateRunID env name) so the
// lifecycle hook can validate the env-supplied run ID without pulling in
// the heavier investigate package.
func IsValidRunID(runID string) bool {
	return runID != "" && runIDPattern.MatchString(runID)
}
