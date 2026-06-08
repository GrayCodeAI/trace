package cost

import (
	"github.com/GrayCodeAI/trace/cli/agent"
)

// Breakdown is the per-token-class dollar decomposition of a cost computation,
// all in USD.
type Breakdown struct {
	// Model is the model ID the cost was computed for.
	Model string `json:"model"`
	// Pricing is the rate table applied (USD per 1M tokens).
	Pricing ModelPricing `json:"pricing"`
	// PricingMatched reports whether the model matched a known table entry.
	// When false, fallback pricing was used.
	PricingMatched bool `json:"pricing_matched"`

	// Per-class dollar costs.
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheWrite float64 `json:"cache_write"`
	CacheRead  float64 `json:"cache_read"`

	// Subagents is the total dollar cost attributed to spawned subagents, if
	// any token usage recorded subagent tokens. It is already included in the
	// returned total.
	Subagents float64 `json:"subagents,omitempty"`

	// Total is the sum of all per-class costs (including subagents) in USD.
	Total float64 `json:"total"`
}

// ComputeCost computes the USD cost of a token usage record under the given
// model, using the built-in pricing table merged with any config-file
// overrides. Unknown models fall back to the fallback pricing. Subagent token
// usage (TokenUsage.SubagentTokens) is recursively included in the total and
// reported separately in the breakdown.
func ComputeCost(usage agent.TokenUsage, model string) (total float64, breakdown Breakdown) {
	table, err := LoadTable()
	if err != nil {
		// A malformed config file should not break cost reporting; fall back
		// to the built-in table.
		table = DefaultTable()
	}
	return ComputeCostWith(table, usage, model)
}

// ComputeCostWith is ComputeCost using an explicit pricing table. It performs
// no I/O, which makes it convenient for tests and for callers that have already
// loaded a table.
func ComputeCostWith(table *Table, usage agent.TokenUsage, model string) (total float64, breakdown Breakdown) {
	if table == nil {
		table = DefaultTable()
	}
	pricing, matched := table.PricingFor(model)

	b := Breakdown{
		Model:          model,
		Pricing:        pricing,
		PricingMatched: matched,
		Input:          float64(usage.InputTokens) * pricing.Input / tokensPerMillion,
		Output:         float64(usage.OutputTokens) * pricing.Output / tokensPerMillion,
		CacheWrite:     float64(usage.CacheCreationTokens) * pricing.CacheWrite / tokensPerMillion,
		CacheRead:      float64(usage.CacheReadTokens) * pricing.CacheRead / tokensPerMillion,
	}
	b.Total = b.Input + b.Output + b.CacheWrite + b.CacheRead

	// Recursively attribute subagent token usage under the same model pricing.
	if usage.SubagentTokens != nil {
		subTotal, _ := ComputeCostWith(table, *usage.SubagentTokens, model)
		b.Subagents = subTotal
		b.Total += subTotal
	}

	return b.Total, b
}
