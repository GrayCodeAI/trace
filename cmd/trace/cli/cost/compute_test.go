package cost

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/agent"
)

const epsilon = 1e-9

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < epsilon
}

func TestComputeCost_KnownModel_HandComputed(t *testing.T) {
	// Isolate from any real ~/.hawk/trace-pricing.json.
	t.Setenv(PricingConfigEnv, filepath.Join(t.TempDir(), "missing.json"))

	usage := agent.TokenUsage{
		InputTokens:         1_000_000, // 1M @ $5.00  = $5.00
		OutputTokens:        500_000,   // 0.5M @ $25.00 = $12.50
		CacheCreationTokens: 200_000,   // 0.2M @ $6.25  = $1.25
		CacheReadTokens:     2_000_000, // 2M @ $0.50   = $1.00
	}

	total, b := ComputeCost(usage, "claude-opus-4-8")

	if !b.PricingMatched {
		t.Fatalf("expected claude-opus-4-8 to match a known model")
	}
	wantInput := 5.00
	wantOutput := 12.50
	wantCacheWrite := 1.25
	wantCacheRead := 1.00
	wantTotal := wantInput + wantOutput + wantCacheWrite + wantCacheRead // $19.75

	if !approxEqual(b.Input, wantInput) {
		t.Errorf("input cost = %v, want %v", b.Input, wantInput)
	}
	if !approxEqual(b.Output, wantOutput) {
		t.Errorf("output cost = %v, want %v", b.Output, wantOutput)
	}
	if !approxEqual(b.CacheWrite, wantCacheWrite) {
		t.Errorf("cache write cost = %v, want %v", b.CacheWrite, wantCacheWrite)
	}
	if !approxEqual(b.CacheRead, wantCacheRead) {
		t.Errorf("cache read cost = %v, want %v", b.CacheRead, wantCacheRead)
	}
	if !approxEqual(total, wantTotal) {
		t.Errorf("total = %v, want %v", total, wantTotal)
	}
	if !approxEqual(b.Total, total) {
		t.Errorf("breakdown total %v != returned total %v", b.Total, total)
	}
}

func TestComputeCost_UnknownModel_UsesFallback(t *testing.T) {
	t.Setenv(PricingConfigEnv, filepath.Join(t.TempDir(), "missing.json"))

	usage := agent.TokenUsage{
		InputTokens:  1_000_000, // 1M @ fallback $3.00 = $3.00
		OutputTokens: 1_000_000, // 1M @ fallback $15.00 = $15.00
	}

	total, b := ComputeCost(usage, "some-unknown-model-v9")

	if b.PricingMatched {
		t.Fatalf("expected unknown model to use fallback (matched=false)")
	}
	fallback := DefaultTable().prefixes[fallbackModelKey]
	if b.Pricing != fallback {
		t.Errorf("pricing = %+v, want fallback %+v", b.Pricing, fallback)
	}
	wantTotal := 3.00 + 15.00
	if !approxEqual(total, wantTotal) {
		t.Errorf("total = %v, want %v", total, wantTotal)
	}
}

func TestComputeCost_IncludesSubagents(t *testing.T) {
	t.Setenv(PricingConfigEnv, filepath.Join(t.TempDir(), "missing.json"))

	usage := agent.TokenUsage{
		InputTokens: 1_000_000, // 1M @ $1.00 (haiku) = $1.00
		SubagentTokens: &agent.TokenUsage{
			OutputTokens: 1_000_000, // 1M @ $5.00 (haiku) = $5.00
		},
	}

	total, b := ComputeCost(usage, "claude-haiku-4-5")

	if !approxEqual(b.Subagents, 5.00) {
		t.Errorf("subagent cost = %v, want 5.00", b.Subagents)
	}
	if !approxEqual(total, 6.00) {
		t.Errorf("total = %v, want 6.00 (1.00 main + 5.00 subagent)", total)
	}
}

func TestPricingFor_LongestPrefixWins(t *testing.T) {
	tbl := DefaultTable()

	p, matched := tbl.PricingFor("claude-opus-4-8")
	if !matched {
		t.Fatal("expected match")
	}
	// "claude-opus" ($5 input) must win over the broader "claude" ($3 input).
	if !approxEqual(p.Input, 5.00) {
		t.Errorf("claude-opus input = %v, want 5.00", p.Input)
	}

	p2, _ := tbl.PricingFor("claude-3-5-sonnet-unknown-suffix")
	// Falls back to the "claude" family default since no "claude-sonnet" prefix.
	if !approxEqual(p2.Input, 3.00) {
		t.Errorf("claude family input = %v, want 3.00", p2.Input)
	}
}

func TestLoadTable_ConfigOverride(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "trace-pricing.json")
	cfg := `{
	  "claude-opus": {"input": 1.0, "output": 2.0, "cache_write": 3.0, "cache_read": 4.0},
	  "my-custom-model": {"input": 10.0, "output": 20.0, "cache_write": 0, "cache_read": 0}
	}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(PricingConfigEnv, cfgPath)

	tbl, err := LoadTable()
	if err != nil {
		t.Fatalf("LoadTable: %v", err)
	}

	// Overridden built-in entry.
	p, matched := tbl.PricingFor("claude-opus-4-8")
	if !matched || !approxEqual(p.Input, 1.0) || !approxEqual(p.Output, 2.0) {
		t.Errorf("overridden claude-opus = %+v matched=%v, want input 1.0 output 2.0", p, matched)
	}

	// New entry added by config.
	pc, matched := tbl.PricingFor("my-custom-model-v1")
	if !matched || !approxEqual(pc.Input, 10.0) {
		t.Errorf("custom model = %+v matched=%v, want input 10.0", pc, matched)
	}

	// Untouched built-in entry still present.
	ph, _ := tbl.PricingFor("claude-haiku-4-5")
	if !approxEqual(ph.Input, 1.0) {
		t.Errorf("haiku input = %v, want 1.0 (unchanged built-in)", ph.Input)
	}
}

func TestLoadTable_MissingConfigIsNotError(t *testing.T) {
	t.Setenv(PricingConfigEnv, filepath.Join(t.TempDir(), "does-not-exist.json"))

	tbl, err := LoadTable()
	if err != nil {
		t.Fatalf("LoadTable with missing config should not error: %v", err)
	}
	if _, matched := tbl.PricingFor("claude-opus-4-8"); !matched {
		t.Error("built-in table should still be populated")
	}
}
