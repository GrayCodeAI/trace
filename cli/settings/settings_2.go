package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/GrayCodeAI/trace/cli/jsonutil"
	"github.com/GrayCodeAI/trace/cli/paths"
	"github.com/GrayCodeAI/trace/cli/session"
)

// mergePIISettings merges PII overrides into existing PIISettings.
// Only fields present in the override JSON are applied; missing fields
// are preserved from the base settings.
func mergePIISettings(dst *PIISettings, data json.RawMessage) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing pii: %w", err)
	}
	if v, ok := raw["enabled"]; ok {
		if err := json.Unmarshal(v, &dst.Enabled); err != nil {
			return fmt.Errorf("parsing pii.enabled: %w", err)
		}
	}
	if v, ok := raw["email"]; ok {
		var b bool
		if err := json.Unmarshal(v, &b); err != nil {
			return fmt.Errorf("parsing pii.email: %w", err)
		}
		dst.Email = &b
	}
	if v, ok := raw["phone"]; ok {
		var b bool
		if err := json.Unmarshal(v, &b); err != nil {
			return fmt.Errorf("parsing pii.phone: %w", err)
		}
		dst.Phone = &b
	}
	if v, ok := raw["address"]; ok {
		var b bool
		if err := json.Unmarshal(v, &b); err != nil {
			return fmt.Errorf("parsing pii.address: %w", err)
		}
		dst.Address = &b
	}
	if v, ok := raw["custom_patterns"]; ok {
		var cp map[string]string
		if err := json.Unmarshal(v, &cp); err != nil {
			return fmt.Errorf("parsing pii.custom_patterns: %w", err)
		}
		if dst.CustomPatterns == nil {
			dst.CustomPatterns = cp
		} else {
			for k, val := range cp {
				dst.CustomPatterns[k] = val
			}
		}
	}
	return nil
}

// IsSetUp returns true if Trace has been set up in the current repository.
// This checks if .trace/settings.json exists.
// Use this to avoid creating files/directories in repos where Trace was never enabled.
func IsSetUp(ctx context.Context) bool {
	settingsFileAbs, err := paths.AbsPath(ctx, TraceSettingsFile)
	if err != nil {
		return false
	}
	_, err = os.Stat(settingsFileAbs)
	return err == nil
}

// IsSetUpAny returns true if Trace has been set up in the current repository,
// checking both .trace/settings.json and .trace/settings.local.json.
// Use this to detect any prior setup, even if only local settings exist.
func IsSetUpAny(ctx context.Context) bool {
	if IsSetUp(ctx) {
		return true
	}
	localFileAbs, err := paths.AbsPath(ctx, TraceSettingsLocalFile)
	if err != nil {
		return false
	}
	_, err = os.Lstat(localFileAbs)
	return err == nil
}

// IsSetUpAndEnabled returns true if Trace is both set up and enabled.
// This checks if .trace/settings.json exists AND has enabled: true.
// Use this for hooks that should be no-ops when Trace is not active.
func IsSetUpAndEnabled(ctx context.Context) bool {
	if !IsSetUp(ctx) {
		return false
	}
	s, err := Load(ctx)
	if err != nil {
		return false
	}
	return s.Enabled
}

// IsCheckpointsV2Enabled checks if checkpoints v2 is enabled in settings.
// Returns false by default if settings cannot be loaded or the key is missing.
func IsCheckpointsV2Enabled(ctx context.Context) bool {
	settings, err := Load(ctx)
	if err != nil {
		return false
	}
	return settings.IsCheckpointsV2Enabled()
}

// CheckpointsVersion returns the configured checkpoints format version, or 1
// if settings cannot be loaded or the value is unset/invalid.
func CheckpointsVersion(ctx context.Context) int {
	s, err := Load(ctx)
	if err != nil {
		return 1
	}
	version := s.CheckpointsVersion()
	if s.StrategyOptions != nil {
		if configured, ok := s.StrategyOptions["checkpoints_version"]; ok {
			if _, supported := parseCheckpointsVersion(configured); !supported {
				checkpointsVersionWarningOnce.Do(func() {
					fmt.Fprintf(
						os.Stderr,
						"[trace] unsupported strategy_options.checkpoints_version %v detected in settings. Falling back to the default version (1).\n",
						configured,
					)
				})
			}
		}
	}
	return version
}

// IsPushV2RefsEnabled checks if pushing v2 refs is enabled in settings.
// Returns false by default if settings cannot be loaded or flags are missing.
func IsPushV2RefsEnabled(ctx context.Context) bool {
	s, err := Load(ctx)
	if err != nil {
		return false
	}
	return s.IsPushV2RefsEnabled()
}

// IsFilteredFetchesEnabled checks if filtered fetches should be used.
// When enabled, filtered fetches always resolve remote names to URLs first so
// git does not persist promisor settings onto named remotes in local config.
// Returns false by default.
func IsFilteredFetchesEnabled(ctx context.Context) bool {
	s, err := Load(ctx)
	if err != nil {
		return false
	}
	return s.IsFilteredFetchesEnabled()
}

// IsSummarizeEnabled checks if auto-summarize is enabled in settings.
// Returns false by default if settings cannot be loaded or the key is missing.
func IsSummarizeEnabled(ctx context.Context) bool {
	settings, err := Load(ctx)
	if err != nil {
		return false
	}
	return settings.IsSummarizeEnabled()
}

// IsSummarizeEnabled checks if auto-summarize is enabled in this settings instance.
func (s *TraceSettings) IsSummarizeEnabled() bool {
	if s.StrategyOptions == nil {
		return false
	}
	summarizeOpts, ok := s.StrategyOptions["summarize"].(map[string]any)
	if !ok {
		return false
	}
	enabled, ok := summarizeOpts["enabled"].(bool)
	if !ok {
		return false
	}
	return enabled
}

// CheckpointRemoteConfig holds the structured checkpoint remote configuration.
// Stored in strategy_options.checkpoint_remote as {"provider": "github", "repo": "org/repo"}.
type CheckpointRemoteConfig struct {
	Provider string // e.g., "github"
	Repo     string // e.g., "org/checkpoints-repo"
}

// Owner returns the owner portion of the repo field (before the slash).
// Returns empty string if the repo field doesn't contain a slash.
func (c *CheckpointRemoteConfig) Owner() string {
	parts := strings.SplitN(c.Repo, "/", 2)
	if len(parts) < 2 {
		return ""
	}
	return parts[0]
}

// GetCheckpointRemote returns the configured checkpoint remote.
// Expects a structured object: {"provider": "github", "repo": "org/repo"}.
// Returns nil if not configured, wrong type, or missing required fields.
func (s *TraceSettings) GetCheckpointRemote() *CheckpointRemoteConfig {
	if s.StrategyOptions == nil {
		return nil
	}
	val, ok := s.StrategyOptions["checkpoint_remote"]
	if !ok {
		return nil
	}
	m, ok := val.(map[string]any)
	if !ok {
		return nil
	}
	provider, providerOK := m["provider"].(string)
	repo, repoOK := m["repo"].(string)
	if !providerOK || !repoOK || provider == "" || repo == "" {
		return nil
	}
	if !strings.Contains(repo, "/") {
		return nil
	}
	return &CheckpointRemoteConfig{Provider: provider, Repo: repo}
}

// IsCheckpointsV2Enabled checks if checkpoints v2 is enabled.
// Returns true when either checkpoints_v2 is set or checkpoints_version is 2.
func (s *TraceSettings) IsCheckpointsV2Enabled() bool {
	if s.CheckpointsVersion() == 2 {
		return true
	}
	if s.StrategyOptions == nil {
		return false
	}
	val, ok := s.StrategyOptions["checkpoints_v2"].(bool)
	return ok && val
}

// CheckpointsVersion returns the configured checkpoints format version from
// strategy_options.checkpoints_version. Returns 1 when unset, invalid, or
// unsupported. The currently supported versions are 1 and 2.
func (s *TraceSettings) CheckpointsVersion() int {
	if s.StrategyOptions == nil {
		return 1
	}
	val, ok := s.StrategyOptions["checkpoints_version"]
	if !ok {
		return 1
	}
	version, ok := parseCheckpointsVersion(val)
	if ok {
		return version
	}
	return 1
}

func parseCheckpointsVersion(val any) (int, bool) {
	v, ok := val.(int)
	if ok && (v == 1 || v == 2) {
		return v, true
	}
	floatV, ok := val.(float64)
	if ok && (floatV == 1 || floatV == 2) {
		return int(floatV), true
	}
	stringV, ok := val.(string)
	if ok {
		parsed, err := strconv.Atoi(stringV)
		if err == nil && (parsed == 1 || parsed == 2) {
			return parsed, true
		}
	}
	return 1, false
}

// IsPushV2RefsEnabled checks if pushing v2 refs is enabled.
// checkpoints_version: 2 forces v2 ref pushes on, regardless of push_v2_refs.
func (s *TraceSettings) IsPushV2RefsEnabled() bool {
	if s.CheckpointsVersion() == 2 {
		return true
	}
	if !s.IsCheckpointsV2Enabled() {
		return false
	}
	if s.StrategyOptions == nil {
		return false
	}
	val, ok := s.StrategyOptions["push_v2_refs"].(bool)
	return ok && val
}

// GetFullTranscriptGenerationRetentionDays returns the retention window for
// archived checkpoints v2 /full/* generations. Invalid, missing, or
// non-positive values fall back to the documented default.
func (s *TraceSettings) GetFullTranscriptGenerationRetentionDays() int {
	if s.StrategyOptions == nil {
		return defaultGenerationRetentionDays
	}

	val, ok := s.StrategyOptions["full_transcript_generation_retention_days"]
	if !ok {
		return defaultGenerationRetentionDays
	}

	switch days := val.(type) {
	case int:
		if days > 0 {
			return days
		}
	case float64:
		intDays := int(days)
		if intDays > 0 && days == float64(intDays) {
			return intDays
		}
	}

	return defaultGenerationRetentionDays
}

// IsFilteredFetchesEnabled checks if fetches should use --filter=blob:none.
// When enabled, filtered fetches always use resolved URLs rather than remote
// names to avoid persisting promisor settings onto named remotes.
func (s *TraceSettings) IsFilteredFetchesEnabled() bool {
	if s.StrategyOptions == nil {
		return false
	}
	val, ok := s.StrategyOptions["filtered_fetches"].(bool)
	return ok && val
}

// IsPushSessionsDisabled checks if push_sessions is disabled in settings.
// Returns true if push_sessions is explicitly set to false.
func (s *TraceSettings) IsPushSessionsDisabled() bool {
	if s.StrategyOptions == nil {
		return false
	}
	val, exists := s.StrategyOptions["push_sessions"]
	if !exists {
		return false
	}
	if boolVal, ok := val.(bool); ok {
		return !boolVal // disabled = !push_sessions
	}
	return false
}

// IsExternalAgentsEnabled checks if external agent discovery is enabled in settings.
// Returns false by default if settings cannot be loaded or the key is missing.
func IsExternalAgentsEnabled(ctx context.Context) bool {
	s, err := Load(ctx)
	if err != nil {
		return false
	}
	return s.ExternalAgents
}

// IsSignCheckpointCommitsEnabled returns true if checkpoint commits should be signed.
// Defaults to true when the setting is not explicitly set.
func (s *TraceSettings) IsSignCheckpointCommitsEnabled() bool {
	return s.SignCheckpointCommits == nil || *s.SignCheckpointCommits
}

// IsSignCheckpointCommitsEnabled checks if checkpoint commit signing is enabled in settings.
// Returns true by default if settings cannot be loaded or the key is missing.
func IsSignCheckpointCommitsEnabled(ctx context.Context) bool {
	s, err := Load(ctx)
	if err != nil {
		return true
	}
	return s.IsSignCheckpointCommitsEnabled()
}

// Save saves the settings to .trace/settings.json.
func Save(ctx context.Context, settings *TraceSettings) error {
	return saveToFile(ctx, settings, TraceSettingsFile)
}

// SaveLocal saves the settings to .trace/settings.local.json.
func SaveLocal(ctx context.Context, settings *TraceSettings) error {
	return saveToFile(ctx, settings, TraceSettingsLocalFile)
}

// saveToFile saves settings to the specified file path.
func saveToFile(ctx context.Context, settings *TraceSettings, filePath string) error {
	// Get absolute path for the file
	filePathAbs, err := paths.AbsPath(ctx, filePath)
	if err != nil {
		filePathAbs = filePath // Fallback to relative
	}

	// Ensure directory exists
	dir := filepath.Dir(filePathAbs)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating settings directory: %w", err)
	}

	data, err := jsonutil.MarshalIndentWithNewline(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling settings: %w", err)
	}

	//nolint:gosec // G306: settings file is config, not secrets; 0o644 is appropriate
	if err := os.WriteFile(filePathAbs, data, 0o644); err != nil {
		return fmt.Errorf("writing settings file: %w", err)
	}
	return nil
}

// --- Clone-local preferences and raw settings helpers (ported from upstream for review migration) ---

// ClonePreferences holds per-clone (non-committed) preferences, primarily for
// the review feature (e.g. which agent to use for review fixes, migration dismissal state).
type ClonePreferences struct {
	Review         map[string]ReviewConfig `json:"review,omitempty"`
	ReviewFixAgent string                  `json:"review_fix_agent,omitempty"`

	// ReviewMigrationDismissed records that the user declined the one-shot
	// migration of review keys from project settings to clone-local prefs.
	// Once true, `trace review` stops prompting on every invocation.
	ReviewMigrationDismissed bool `json:"review_migration_dismissed,omitempty"`
}

// LoadProjectRaw reads .trace/settings.json as a generic JSON object.
// Used by review migration to move keys without loading the full typed struct.
func LoadProjectRaw(ctx context.Context) (path string, raw map[string]json.RawMessage, exists bool, err error) {
	path, err = paths.AbsPath(ctx, TraceSettingsFile)
	if err != nil {
		path = TraceSettingsFile
	}
	data, readErr := os.ReadFile(path) //nolint:gosec
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return path, map[string]json.RawMessage{}, false, nil
		}
		return path, nil, false, fmt.Errorf("reading project settings: %w", readErr)
	}
	raw = map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return path, nil, true, fmt.Errorf("parsing project settings: %w", err)
	}
	return path, raw, true, nil
}

// LoadLocalRaw reads .trace/settings.local.json as a generic JSON object.
func LoadLocalRaw(ctx context.Context) (path string, raw map[string]json.RawMessage, exists bool, err error) {
	path, err = paths.AbsPath(ctx, TraceSettingsLocalFile)
	if err != nil {
		path = TraceSettingsLocalFile
	}
	data, readErr := os.ReadFile(path) //nolint:gosec
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return path, map[string]json.RawMessage{}, false, nil
		}
		return path, nil, false, fmt.Errorf("reading local settings: %w", readErr)
	}
	raw = map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return path, nil, true, fmt.Errorf("parsing local settings: %w", err)
	}
	return path, raw, true, nil
}

// SaveProjectRaw writes a generic JSON object back to .trace/settings.json atomically.
func SaveProjectRaw(path string, raw map[string]json.RawMessage) error {
	data, err := jsonutil.MarshalIndentWithNewline(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal project settings: %w", err)
	}
	if err := jsonutil.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("writing project settings: %w", err)
	}
	return nil
}

// ClonePreferencesPath returns the path to trace/preferences.json inside the git common dir.
func ClonePreferencesPath(ctx context.Context) (string, error) {
	commonDir, err := session.GetGitCommonDir(ctx)
	if err != nil {
		return "", fmt.Errorf("get git common dir: %w", err)
	}
	return filepath.Join(commonDir, ClonePreferencesFile), nil
}

// LoadClonePreferences loads clone-local preferences from the git common dir.
func LoadClonePreferences(ctx context.Context) (*ClonePreferences, error) {
	path, err := ClonePreferencesPath(ctx)
	if err != nil {
		return nil, err
	}
	return loadClonePreferencesFromFile(path)
}

// SaveClonePreferences saves clone-local preferences to the git common dir.
func SaveClonePreferences(ctx context.Context, prefs *ClonePreferences) error {
	path, err := ClonePreferencesPath(ctx)
	if err != nil {
		return err
	}
	return saveClonePreferencesToFile(prefs, path)
}

func loadClonePreferencesFromFile(filePath string) (*ClonePreferences, error) {
	prefs := &ClonePreferences{}
	data, err := os.ReadFile(filePath) //nolint:gosec
	if err != nil {
		if os.IsNotExist(err) {
			return prefs, nil
		}
		return nil, fmt.Errorf("%w", err)
	}
	// Lenient decode (unknown fields are ignored) — same rationale as upstream.
	if err := json.Unmarshal(data, prefs); err != nil {
		return nil, fmt.Errorf("parsing preferences file: %w", err)
	}
	return prefs, nil
}

func saveClonePreferencesToFile(prefs *ClonePreferences, filePath string) error {
	if prefs == nil {
		prefs = &ClonePreferences{}
	}
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating preferences directory: %w", err)
	}
	data, err := jsonutil.MarshalIndentWithNewline(prefs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling preferences: %w", err)
	}
	if err := jsonutil.WriteFileAtomic(filePath, data, 0o644); err != nil {
		return fmt.Errorf("writing preferences file: %w", err)
	}
	return nil
}
