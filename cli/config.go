package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/GrayCodeAI/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/settings"
	"github.com/GrayCodeAI/trace/cli/strategy"

	// Import agents to register them
	_ "github.com/GrayCodeAI/trace/cli/agent/claudecode"
	_ "github.com/GrayCodeAI/trace/cli/agent/codex"
	_ "github.com/GrayCodeAI/trace/cli/agent/factoryaidroid"
)

// Package-level aliases to avoid shadowing the settings package with local variables named "settings".
const (
	TraceSettingsFile      = settings.TraceSettingsFile
	TraceSettingsLocalFile = settings.TraceSettingsLocalFile
)

// TraceSettings is an alias for settings.TraceSettings.
type TraceSettings = settings.TraceSettings

// LoadTraceSettings loads the Trace settings from .trace/settings.json,
// then applies any overrides from .trace/settings.local.json if it exists.
// Returns default settings if neither file exists.
// Works correctly from any subdirectory within the repository.
func LoadTraceSettings(ctx context.Context) (*settings.TraceSettings, error) {
	s, err := settings.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading settings: %w", err)
	}
	return s, nil
}

// SaveTraceSettings saves the Trace settings to .trace/settings.json.
func SaveTraceSettings(ctx context.Context, s *settings.TraceSettings) error {
	if err := settings.Save(ctx, s); err != nil {
		return fmt.Errorf("saving settings: %w", err)
	}
	return nil
}

// SaveTraceSettingsLocal saves the Trace settings to .trace/settings.local.json.
func SaveTraceSettingsLocal(ctx context.Context, s *settings.TraceSettings) error {
	if err := settings.SaveLocal(ctx, s); err != nil {
		return fmt.Errorf("saving local settings: %w", err)
	}
	return nil
}

// IsEnabled returns whether Trace is currently enabled.
// Returns true by default if settings cannot be loaded.
func IsEnabled(ctx context.Context) (bool, error) {
	s, err := settings.Load(ctx)
	if err != nil {
		return true, err //nolint:wrapcheck // already present in codebase
	}
	return s.Enabled, nil
}

// GetStrategy returns the manual-commit strategy instance with blob fetching
// enabled so that checkpoint reads work after treeless fetches.
func GetStrategy(_ context.Context) *strategy.ManualCommitStrategy {
	s := strategy.NewManualCommitStrategy()
	s.SetBlobFetcher(FetchBlobsByHash)
	return s
}

// GetLogLevel returns the configured log level from settings.
// Returns empty string if not configured (caller should use default).
// Note: TRACE_LOG_LEVEL env var takes precedence; check it first.
func GetLogLevel() string {
	s, err := settings.Load(context.TODO()) //nolint:contextcheck // Called as a callback via SetLogLevelGetter, no ctx available
	if err != nil {
		return ""
	}
	return s.LogLevel
}

// GetAgentsWithHooksInstalled returns names of agents that have hooks installed.
func GetAgentsWithHooksInstalled(ctx context.Context) []types.AgentName {
	var installed []types.AgentName
	for _, name := range agent.List() {
		ag, err := agent.Get(name)
		if err != nil {
			continue
		}
		if hs, ok := agent.AsHookSupport(ag); ok && hs.AreHooksInstalled(ctx) {
			installed = append(installed, name)
		}
	}
	return installed
}

// InstalledAgentDisplayNames returns user-facing display names for agents with hooks installed.
func InstalledAgentDisplayNames(ctx context.Context) []string {
	installedNames := GetAgentsWithHooksInstalled(ctx)
	displayNames := make([]string, 0, len(installedNames))
	for _, name := range installedNames {
		if ag, err := agent.Get(name); err == nil {
			displayNames = append(displayNames, string(ag.Type()))
		}
	}
	return displayNames
}

// JoinAgentNames joins agent names into a comma-separated string.
func JoinAgentNames(names []types.AgentName) string {
	strs := make([]string, len(names))
	for i, n := range names {
		strs[i] = string(n)
	}
	return strings.Join(strs, ",")
}
