package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

type tokensProfileReport struct {
	Source                   string                        `json:"source"`
	UsageScope               string                        `json:"usage_scope"`
	CheckpointsAvailable     int                           `json:"checkpoints_available"`
	CheckpointsAnalyzed      int                           `json:"checkpoints_analyzed"`
	CheckpointsWithTokenData int                           `json:"checkpoints_with_token_data"`
	MissingTokenData         int                           `json:"missing_token_data"`
	MetadataReadWarnings     int                           `json:"metadata_read_warnings,omitempty"`
	Tokens                   *sessionTokensUsage           `json:"tokens,omitempty"`
	Signals                  []tokensProfileSignal         `json:"signals,omitempty"`
	Recommendations          []sessionTokensRecommendation `json:"recommendations,omitempty"`
	Limitations              []string                      `json:"limitations,omitempty"`
}

type tokensProfileSignal struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Count         int      `json:"count"`
	Percent       int      `json:"percent"`
	CheckpointIDs []string `json:"checkpoint_ids,omitempty"`
}

type tokensProfileSignalDefinition struct {
	id    string
	label string
}

var tokensProfileSignalDefinitions = []tokensProfileSignalDefinition{
	{id: "context-replay-hotspot", label: "Cache/context replay hotspot"},
	{id: "api-call-amplification", label: "API call amplification"},
	{id: "subagent-heavy", label: "Subagent-heavy sessions"},
	{id: "missing-token-data", label: "Missing token data"},
}

const tokensProfileUsageScopeCheckpointObserved = "checkpoint_observed"

func newTokensGroupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "tokens",
		Short:  "Analyze token usage across sessions and checkpoints",
		Hidden: true,
		Long: `Analyze token usage across sessions and checkpoints.

Commands:
  profile  Aggregate token usage across committed checkpoints`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newTokensProfileCmd())
	return cmd
}

func newTokensProfileCmd() *cobra.Command {
	var jsonFlag bool
	var limitFlag int
	var allFlag bool

	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Aggregate token usage and recommendations across checkpoint history",
		Long: `Aggregate token usage and recommendations across committed checkpoint history.

The profile reads committed checkpoint metadata only. It does not inspect
transcripts or source files, so it is deterministic and avoids adding token
cost while diagnosing token usage. By default it scans the latest 50 committed
checkpoints; use --limit or --all to change the scope.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			limit := limitFlag
			if allFlag {
				limit = 0
			} else if limit <= 0 {
				return errors.New("--limit must be positive unless --all is used")
			}
			return runTokensProfile(cmd.Context(), cmd, jsonFlag, limit)
		},
	}

	cmd.Flags().BoolVar(&jsonFlag, "json", false, "Output as JSON")
	cmd.Flags().IntVar(&limitFlag, "limit", 50, "Maximum committed checkpoints to analyze")
	cmd.Flags().BoolVar(&allFlag, "all", false, "Analyze all committed checkpoints")
	return cmd
}

func runTokensProfile(ctx context.Context, cmd *cobra.Command, jsonFlag bool, limit int) error {
	lookup, err := newExplainCheckpointLookup(ctx)
	if err != nil {
		return err
	}
	report, err := buildTokensProfileReport(ctx, lookup, limit)
	if err != nil {
		return err
	}
	if jsonFlag {
		return writeJSONPretty(cmd.OutOrStdout(), report)
	}
	renderTokensProfileText(cmd.OutOrStdout(), report)
	return nil
}

func buildTokensProfileReport(ctx context.Context, lookup *explainCheckpointLookup, limit int) (*tokensProfileReport, error) {
	infos, err := lookup.v1Store.ListCommitted(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing committed checkpoints: %w", err)
	}

	available := len(infos)
	scanCount := available
	if limit > 0 && limit < scanCount {
		scanCount = limit
	}

	report := &tokensProfileReport{
		Source:               "trace checkpoint metadata",
		UsageScope:           tokensProfileUsageScopeCheckpointObserved,
		CheckpointsAvailable: available,
		CheckpointsAnalyzed:  scanCount,
		Limitations: []string{
			"Token counts reflect observed checkpoint metadata only.",
			"Transcripts without token metadata contribute 0 tokens.",
		},
	}

	var combined sessionTokensUsage
	var missingCount int
	var warningsCount int
	var hotspotIDs []string

	for i := 0; i < scanCount; i++ {
		c := infos[i]
		meta, err := lookup.v1Store.ReadSessionMetadata(ctx, c.CheckpointID, 0)
		if err != nil {
			warningsCount++
			continue
		}
		if meta == nil || meta.TokenUsage == nil {
			missingCount++
			continue
		}

		report.CheckpointsWithTokenData++
		t := meta.TokenUsage
		combined.Input += t.InputTokens
		combined.Output += t.OutputTokens
		combined.CacheWrite += t.CacheCreationTokens
		combined.CacheRead += t.CacheReadTokens
		combined.Total += t.InputTokens + t.OutputTokens + t.CacheCreationTokens + t.CacheReadTokens

		if t.CacheCreationTokens > 0 && t.CacheCreationTokens >= t.InputTokens/2 {
			idStr := string(c.CheckpointID)
			if len(idStr) > 12 {
				idStr = idStr[:12]
			}
			hotspotIDs = append(hotspotIDs, idStr)
		}
	}

	report.MissingTokenData = missingCount
	report.MetadataReadWarnings = warningsCount
	if report.CheckpointsWithTokenData > 0 {
		report.Tokens = &combined
	}

	var signals []tokensProfileSignal
	if len(hotspotIDs) > 0 {
		percent := (len(hotspotIDs) * 100) / scanCount
		signals = append(signals, tokensProfileSignal{
			ID:            "context-replay-hotspot",
			Label:         "Cache/context replay hotspot",
			Count:         len(hotspotIDs),
			Percent:       percent,
			CheckpointIDs: hotspotIDs,
		})
	}
	if missingCount > 0 {
		percent := (missingCount * 100) / scanCount
		signals = append(signals, tokensProfileSignal{
			ID:      "missing-token-data",
			Label:   "Missing token data",
			Count:   missingCount,
			Percent: percent,
		})
	}
	report.Signals = signals

	return report, nil
}

func renderTokensProfileText(w io.Writer, report *tokensProfileReport) {
	fmt.Fprintf(w, "Token Profile (%s)\n", report.Source)
	fmt.Fprintf(w, "Checkpoints Analyzed: %d / %d available\n\n", report.CheckpointsAnalyzed, report.CheckpointsAvailable)

	if report.Tokens != nil {
		fmt.Fprintf(w, "Total Tokens: %d\n", report.Tokens.Total)
		fmt.Fprintf(w, "  Input Tokens: %d\n", report.Tokens.Input)
		fmt.Fprintf(w, "  Output Tokens: %d\n", report.Tokens.Output)
		if report.Tokens.CacheRead > 0 || report.Tokens.CacheWrite > 0 {
			fmt.Fprintf(w, "  Cache Read Tokens: %d\n", report.Tokens.CacheRead)
			fmt.Fprintf(w, "  Cache Creation Tokens: %d\n", report.Tokens.CacheWrite)
		}
	} else {
		fmt.Fprintln(w, "No token usage recorded in analyzed checkpoints.")
	}

	if len(report.Signals) > 0 {
		fmt.Fprintln(w, "\nSignals:")
		for _, sig := range report.Signals {
			fmt.Fprintf(w, "  - %s: %d checkpoints (%d%%)\n", sig.Label, sig.Count, sig.Percent)
		}
	}
}
