// Package cost computes USD cost attribution for agent token usage.
//
// Pricing is expressed in USD per 1,000,000 tokens, broken out by token class
// (input, output, cache write, cache read). A built-in table covers common
// model families (claude-*, gpt-*, gemini-*) with a conservative fallback for
// unknown models. The table can be overridden or extended from a JSON config
// file at ~/.hawk/trace-pricing.json.
package cost

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ModelPricing holds the USD price per 1,000,000 tokens for each token class.
type ModelPricing struct {
	// Input is the price per 1M fresh (non-cached) input tokens.
	Input float64 `json:"input"`
	// Output is the price per 1M generated output tokens.
	Output float64 `json:"output"`
	// CacheWrite is the price per 1M tokens written to the prompt cache.
	CacheWrite float64 `json:"cache_write"`
	// CacheRead is the price per 1M tokens read from the prompt cache.
	CacheRead float64 `json:"cache_read"`
}

// tokensPerMillion is the divisor that converts a raw token count and a
// per-1M-token price into a dollar amount.
const tokensPerMillion = 1_000_000.0

// PricingConfigEnv overrides the pricing config file path when set. Used in
// tests to avoid touching the real home directory.
const PricingConfigEnv = "TRACE_PRICING_CONFIG"

// fallbackModelKey is the table key whose pricing is used for any model that
// does not match a known prefix.
const fallbackModelKey = "fallback"

// defaultPricing is the built-in price table, keyed by a lowercase model-ID
// prefix. Prefixes are matched longest-first so more specific entries (e.g.
// "claude-opus") win over family defaults (e.g. "claude"). Prices are USD per
// 1,000,000 tokens.
//
// Claude pricing is sourced from the published Anthropic price list; gpt-* and
// gemini-* entries are representative defaults intended to be overridden via
// the config file for exact billing.
func defaultPricing() map[string]ModelPricing {
	return map[string]ModelPricing{
		// Anthropic Claude.
		"claude-opus":   {Input: 5.00, Output: 25.00, CacheWrite: 6.25, CacheRead: 0.50},
		"claude-sonnet": {Input: 3.00, Output: 15.00, CacheWrite: 3.75, CacheRead: 0.30},
		"claude-haiku":  {Input: 1.00, Output: 5.00, CacheWrite: 1.25, CacheRead: 0.10},
		"claude":        {Input: 3.00, Output: 15.00, CacheWrite: 3.75, CacheRead: 0.30},

		// OpenAI GPT (representative; override via config for exact rates).
		"gpt-4o-mini": {Input: 0.15, Output: 0.60, CacheWrite: 0.15, CacheRead: 0.075},
		"gpt-4o":      {Input: 2.50, Output: 10.00, CacheWrite: 2.50, CacheRead: 1.25},
		"gpt-4":       {Input: 30.00, Output: 60.00, CacheWrite: 30.00, CacheRead: 30.00},
		"gpt":         {Input: 2.50, Output: 10.00, CacheWrite: 2.50, CacheRead: 1.25},

		// Google Gemini (representative; override via config for exact rates).
		"gemini-1.5-pro":   {Input: 1.25, Output: 5.00, CacheWrite: 1.25, CacheRead: 0.3125},
		"gemini-1.5-flash": {Input: 0.075, Output: 0.30, CacheWrite: 0.075, CacheRead: 0.01875},
		"gemini":           {Input: 1.25, Output: 5.00, CacheWrite: 1.25, CacheRead: 0.3125},

		// Conservative fallback for unknown models.
		fallbackModelKey: {Input: 3.00, Output: 15.00, CacheWrite: 3.75, CacheRead: 0.30},
	}
}

// Table maps model-ID prefixes to pricing. Look up rates with PricingFor.
type Table struct {
	prefixes map[string]ModelPricing
}

// DefaultTable returns the built-in pricing table.
func DefaultTable() *Table {
	return &Table{prefixes: defaultPricing()}
}

// LoadTable returns the built-in pricing table with any overrides from the
// config file at ~/.hawk/trace-pricing.json (or the path named by
// TRACE_PRICING_CONFIG) merged in. A missing config file is not an error and
// yields the built-in table unchanged. Entries in the file replace built-in
// entries with the same key and add new keys.
func LoadTable() (*Table, error) {
	t := DefaultTable()
	path, err := configPath()
	if err != nil {
		return t, nil //nolint:nilerr // missing home dir falls back to defaults
	}

	data, err := os.ReadFile(path) // #nosec G304 -- path is either a fixed ~/.hawk/trace-pricing.json location or the TRACE_PRICING_CONFIG env var set by the operator running the CLI, not remote/untrusted input
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return t, nil
		}
		return t, err
	}

	var overrides map[string]ModelPricing
	if err := json.Unmarshal(data, &overrides); err != nil {
		return t, err
	}
	for k, v := range overrides {
		t.prefixes[strings.ToLower(k)] = v
	}
	return t, nil
}

// configPath returns the pricing config file location.
func configPath() (string, error) {
	if override := os.Getenv(PricingConfigEnv); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".hawk", "trace-pricing.json"), nil
}

// PricingFor returns the pricing for a model, matching the longest model-ID
// prefix in the table. Matching is case-insensitive. If no prefix matches, the
// fallback pricing is returned along with matched=false.
func (t *Table) PricingFor(model string) (pricing ModelPricing, matched bool) {
	m := strings.ToLower(strings.TrimSpace(model))

	bestLen := -1
	var best ModelPricing
	for prefix, p := range t.prefixes {
		if prefix == fallbackModelKey {
			continue
		}
		if strings.HasPrefix(m, prefix) && len(prefix) > bestLen {
			bestLen = len(prefix)
			best = p
		}
	}
	if bestLen >= 0 {
		return best, true
	}
	return t.prefixes[fallbackModelKey], false
}
