// Package settings provides configuration loading for Trace.
// This package is separate from cli to allow strategy package to import it
// without creating an import cycle (cli imports strategy).
package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/GrayCodeAI/trace/cli/paths"
)

const (
	// TraceSettingsFile is the path to the Trace settings file
	TraceSettingsFile = ".trace/settings.json"
	// TraceSettingsLocalFile is the path to the local settings override file (not committed)
	TraceSettingsLocalFile = ".trace/settings.local.json"
	// ClonePreferencesFile is the path inside the git common dir for clone-local preferences
	// (review migration state, etc.). Adapted from upstream "trace/preferences.json".
	ClonePreferencesFile = "trace/preferences.json"
	// defaultGenerationRetentionDays is the default retention window for archived
	// checkpoints v2 raw-transcript generations when no override is configured.
	defaultGenerationRetentionDays = 14
)

var checkpointsVersionWarningOnce sync.Once

// Commit linking mode constants.
const (
	// CommitLinkingAlways auto-links commits to sessions without prompting.
	CommitLinkingAlways = "always"
	// CommitLinkingPrompt prompts the user on each commit (default for existing users).
	CommitLinkingPrompt = "prompt"
)

// TraceSettings represents the .trace/settings.json configuration
type TraceSettings struct {
	// Enabled indicates whether Trace is active. When false, CLI commands
	// show a disabled message and hooks exit silently. Defaults to true.
	Enabled bool `json:"enabled"`

	// LocalDev indicates whether to use "go run" instead of the "trace" binary
	// This is used for development when the binary is not installed
	LocalDev bool `json:"local_dev,omitempty"`

	// LogLevel sets the logging verbosity (debug, info, warn, error).
	// Can be overridden by TRACE_LOG_LEVEL environment variable.
	// Defaults to "info".
	LogLevel string `json:"log_level,omitempty"`

	// StrategyOptions contains strategy-specific configuration
	StrategyOptions map[string]any `json:"strategy_options,omitempty"`

	// AbsoluteGitHookPath embeds the full binary path in git hooks instead of
	// bare "trace". This is needed for GUI git clients (Xcode, Tower, etc.)
	// that don't source shell profiles and can't find "trace" on PATH.
	AbsoluteGitHookPath bool `json:"absolute_git_hook_path,omitempty"`

	// Telemetry controls anonymous usage analytics.
	// nil = not asked yet (show prompt), true = opted in, false = opted out
	Telemetry *bool `json:"telemetry,omitempty"`

	// Redaction configures PII redaction behavior for transcripts and metadata.
	Redaction *RedactionSettings `json:"redaction,omitempty"`

	// Review maps agent name (e.g. "claude-code") to the review config for
	// that agent. When empty, `trace review` triggers the first-run picker.
	Review map[string]ReviewConfig `json:"review,omitempty"`

	// ReviewFixAgent is the default agent used when applying aggregate or
	// multi-agent review findings with `trace review --fix`.
	ReviewFixAgent string `json:"review_fix_agent,omitempty"`

	// CommitLinking controls how commits are linked to agent sessions.
	// "always" = auto-link without prompting, "prompt" = ask on each commit.
	// Defaults to "prompt" (preserves existing user behavior).
	CommitLinking string `json:"commit_linking,omitempty"`

	// ExternalAgents enables discovery and registration of external agent
	// plugins (trace-agent-* binaries on $PATH). Defaults to false.
	ExternalAgents bool `json:"external_agents,omitempty"`

	// SummaryGeneration stores provider preferences for explain --generate.
	// This is separate from strategy_options.summarize, which controls
	// checkpoint auto-summarize behavior.
	SummaryGeneration *SummaryGenerationSettings `json:"summary_generation,omitempty"`

	// Vercel indicates that the repository uses Vercel and the metadata branch
	// should include a vercel.json that disables deployments for Trace branches.
	Vercel bool `json:"vercel,omitempty"`

	// SummaryTimeoutSeconds is an optional hard deadline (in seconds) for
	// `trace explain --generate` summary generation. Zero or negative means
	// "unset" -- the caller picks the default. Not yet consumed by the
	// generate path; present so settings round-trip for a follow-up change
	// that wires it into the deadline selection.
	SummaryTimeoutSeconds int `json:"summary_timeout_seconds,omitempty"`

	// SignCheckpointCommits controls whether checkpoint commits are signed.
	// nil/true = sign (default), false = skip signing.
	SignCheckpointCommits *bool `json:"sign_checkpoint_commits,omitempty"`

	// Investigate holds configuration for `trace investigate`. Empty means
	// `trace investigate` triggers the first-run picker.
	Investigate *InvestigateConfig `json:"investigate,omitempty"`

	// Attribution controls how the agent identity is recorded on commits
	// Trace creates. Nil means defaults (co-authored-by trailer on, author
	// and committer overrides off — matching Aider's default behavior).
	Attribution *AttributionSettings `json:"attribution,omitempty"`

	// DirtyCommits controls whether Trace auto-commits a "work in progress"
	// snapshot of uncommitted changes at the start of an agent session,
	// before the agent makes any edits. nil/true = enabled (default, matching
	// Aider), false = disabled. Can be overridden per-invocation with
	// --no-dirty-commits.
	DirtyCommits *bool `json:"dirty_commits,omitempty"`

	// Webhooks configures best-effort HTTP notifications on session lifecycle
	// events (session_start, checkpoint_created, session_end, error). Empty
	// or nil disables notifications.
	Webhooks *WebhookConfig `json:"webhooks,omitempty"`

	// CI holds configuration written by `trace ci-init` to control session
	// auto-capture and tagging when running inside a CI provider. Nil means
	// no CI-specific configuration has been applied.
	CI *CIConfig `json:"ci,omitempty"`

	// Deprecated: no longer used. Exists to tolerate old settings files
	// that still contain "strategy": "auto-commit" or similar.
	Strategy string `json:"strategy,omitempty"`
}

// WebhookConfig configures outbound webhook notifications for session
// lifecycle events. Notifications are best-effort: delivery failures are
// logged but never propagated to the caller (a session is never failed
// because a webhook endpoint was unreachable).
type WebhookConfig struct {
	// URLs is the list of endpoints that receive a JSON POST for each event.
	// Empty disables webhook delivery.
	URLs []string `json:"urls,omitempty"`

	// Events optionally restricts which lifecycle events are delivered. When
	// empty, all events are sent. Valid values match the event constants in
	// the webhook package ("session_start", "checkpoint_created",
	// "session_end", "error").
	Events []string `json:"events,omitempty"`

	// TimeoutSeconds bounds each individual POST. Zero or negative means the
	// caller picks a short default.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// IsZero reports whether the config has no deliverable endpoints.
func (c *WebhookConfig) IsZero() bool {
	return c == nil || len(c.URLs) == 0
}

// CIConfig records the CI auto-capture configuration applied by
// `trace ci-init`. It is intentionally small: the run-time tags (run id, PR
// number, branch) are read from the environment on each invocation rather
// than persisted, so the committed config stays portable across runs.
type CIConfig struct {
	// AutoCapture indicates that sessions should be captured automatically
	// when running inside a recognized CI provider.
	AutoCapture bool `json:"auto_capture"`

	// Provider records which CI provider was detected at init time
	// (e.g. "github-actions", "gitlab-ci"). Empty when configured outside CI.
	Provider string `json:"provider,omitempty"`

	// Tags holds static key/value tags to attach to captured CI sessions, in
	// addition to the dynamic env-derived tags resolved at run time.
	Tags map[string]string `json:"tags,omitempty"`
}

// AttributionSettings holds the three independently-toggleable commit
// attribution flags. Each defaults to the Aider-compatible behavior: the
// co-authored-by trailer is on, while the author and committer identity
// overrides are off. A nil *bool for any individual field falls back to that
// default via the TraceSettings.Attribute* accessors.
type AttributionSettings struct {
	// AttributeAuthor, when true, sets the git author of Trace-created commits
	// to the agent identity instead of the human's git user. Default off.
	AttributeAuthor *bool `json:"attribute_author,omitempty"`

	// AttributeCommitter, when true, sets the git committer of Trace-created
	// commits to the agent identity instead of the human's git user.
	// Default off.
	AttributeCommitter *bool `json:"attribute_committer,omitempty"`

	// AttributeCoAuthoredBy, when true, appends a
	// "Co-authored-by: <agent> <email>" trailer to the commit message.
	// Default on.
	AttributeCoAuthoredBy *bool `json:"attribute_co_authored_by,omitempty"`
}

// AttributeAuthor reports whether the git author identity should be overridden
// with the agent identity. Defaults to false when unset.
func (s *TraceSettings) AttributeAuthor() bool {
	if s == nil || s.Attribution == nil || s.Attribution.AttributeAuthor == nil {
		return false
	}
	return *s.Attribution.AttributeAuthor
}

// AttributeCommitter reports whether the git committer identity should be
// overridden with the agent identity. Defaults to false when unset.
func (s *TraceSettings) AttributeCommitter() bool {
	if s == nil || s.Attribution == nil || s.Attribution.AttributeCommitter == nil {
		return false
	}
	return *s.Attribution.AttributeCommitter
}

// AttributeCoAuthoredBy reports whether a Co-authored-by trailer should be
// appended to commit messages. Defaults to true when unset (Aider-compatible).
func (s *TraceSettings) AttributeCoAuthoredBy() bool {
	if s == nil || s.Attribution == nil || s.Attribution.AttributeCoAuthoredBy == nil {
		return true
	}
	return *s.Attribution.AttributeCoAuthoredBy
}

// DirtyCommitsEnabled reports whether pre-session WIP auto-commits are enabled.
// Defaults to true when unset (Aider-compatible).
func (s *TraceSettings) DirtyCommitsEnabled() bool {
	if s == nil || s.DirtyCommits == nil {
		return true
	}
	return *s.DirtyCommits
}

// SummaryGenerationSettings configures provider selection for on-demand
// checkpoint summaries generated by explain --generate.
type SummaryGenerationSettings struct {
	// Provider is the selected summary provider agent name
	// (for example "claude-code", "codex", or "gemini").
	Provider string `json:"provider,omitempty"`

	// Model is an optional model hint passed to the selected provider.
	Model string `json:"model,omitempty"`
}

// Validate returns an error if the settings combination is semantically invalid.
// A model without a provider is meaningless: the model hint needs a provider to
// route to. The load path calls Validate() after merging, catching hand-edited
// files that land in this state.
func (s *SummaryGenerationSettings) Validate() error {
	if s == nil {
		return nil
	}
	if s.Model != "" && s.Provider == "" {
		return fmt.Errorf("summary_generation.model %q set without summary_generation.provider", s.Model)
	}
	return nil
}

// SetProvider updates the provider and optionally the model, clearing any stale
// model from the previous provider when switching without a replacement.
// An empty newProvider preserves the current provider; an empty newModel
// preserves the current model unless the provider is changing, in which case
// the old model is cleared to avoid passing (say) a Claude model to Codex.
func (s *SummaryGenerationSettings) SetProvider(newProvider, newModel string) {
	if s == nil {
		return
	}
	if newProvider != "" && s.Provider != "" && s.Provider != newProvider && newModel == "" {
		s.Model = ""
	}
	if newProvider != "" {
		s.Provider = newProvider
	}
	if newModel != "" {
		s.Model = newModel
	}
}

// ReviewConfig holds the per-agent review configuration. Both fields are
// optional; together they describe what `trace review` should ask the
// agent to do.
//
// Precedence when composing the review prompt sent to the agent:
//   - If Prompt is non-empty, it is used verbatim.
//   - Otherwise, Skills are composed into a default template
//     ("Please run these review skills in order: 1. /X 2. /Y").
//
// Skills are always recorded on the checkpoint metadata regardless of
// which path composed the prompt — they're the structured, queryable
// tag alongside ReviewPrompt (which is the ground truth).
type ReviewConfig struct {
	// Skills is the list of slash-prefixed skill invocations configured
	// for this agent. May be empty when Prompt carries the full request.
	Skills []string `json:"skills,omitempty"`

	// Prompt, when non-empty, carries saved review instructions. When
	// Skills is non-empty it is appended after the selected skills; when
	// Skills is empty it is the full prompt for prompt-only review configs.
	Prompt string `json:"prompt,omitempty"`
}

// IsZero reports whether the config is effectively unset.
func (c ReviewConfig) IsZero() bool {
	return len(c.Skills) == 0 && c.Prompt == ""
}

// ReviewConfigFor returns the configured review config for the given agent.
// Returns a zero-value config when the agent has no entry; callers should
// check IsZero (or the individual fields) to decide whether configuration
// is present.
func (s *TraceSettings) ReviewConfigFor(agentName string) ReviewConfig {
	if s == nil {
		return ReviewConfig{}
	}
	return s.Review[agentName]
}

// InvestigateConfig holds the configuration for `trace investigate`.
// Unlike ReviewConfig, investigate runs the same shared prompt across
// all configured agents, so the schema is a flat agent list with global
// loop knobs rather than per-agent skill lists.
type InvestigateConfig struct {
	// Agents is the ordered list of agent names to round-robin during the loop.
	Agents []string `json:"agents,omitempty"`

	// MaxTurns is the per-agent turn budget. Defaults to 2 when zero
	// (see investigate.defaultMaxTurns).
	MaxTurns int `json:"max_turns,omitempty"`

	// Quorum is the count of `approve` stances needed to terminate the loop.
	// Zero means "all agents must approve" (matches marvin's default).
	Quorum int `json:"quorum,omitempty"`

	// AlwaysPrompt is appended to every turn's composed prompt, parallel
	// to ReviewConfig.Prompt.
	AlwaysPrompt string `json:"always_prompt,omitempty"`
}

// IsZero reports whether the config is effectively unset.
func (c *InvestigateConfig) IsZero() bool {
	if c == nil {
		return true
	}
	return len(c.Agents) == 0 && c.MaxTurns == 0 && c.Quorum == 0 && c.AlwaysPrompt == ""
}

// InvestigateConfig returns the configured investigate config. Returns nil
// when no configuration is present; callers should check IsZero (or guard
// for nil) to decide whether configuration is present.
func (s *TraceSettings) InvestigateConfigMethod() *InvestigateConfig {
	if s == nil {
		return nil
	}
	return s.Investigate
}

// RedactionSettings configures redaction behavior beyond the default secret detection.
type RedactionSettings struct {
	PII *PIISettings `json:"pii,omitempty"`
}

// PIISettings configures PII detection categories.
// When Enabled is true, email and phone default to true; address defaults to false.
type PIISettings struct {
	Enabled        bool              `json:"enabled"`
	Email          *bool             `json:"email,omitempty"`
	Phone          *bool             `json:"phone,omitempty"`
	Address        *bool             `json:"address,omitempty"`
	CustomPatterns map[string]string `json:"custom_patterns,omitempty"`
}

// GetCommitLinking returns the effective commit linking mode.
// Returns the explicit value if set, otherwise defaults to "prompt"
// to preserve existing user behavior.
func (s *TraceSettings) GetCommitLinking() string {
	if s.CommitLinking != "" {
		return s.CommitLinking
	}
	return CommitLinkingPrompt
}

// SummaryTimeoutValue returns the configured hard deadline for
// `trace explain --generate` summary generation. Zero means "unset" --
// the caller picks the default. Negative values are treated as unset.
func (s *TraceSettings) SummaryTimeoutValue() time.Duration {
	if s.SummaryTimeoutSeconds < 1 {
		return 0
	}
	return time.Duration(s.SummaryTimeoutSeconds) * time.Second
}

// Load loads the Trace settings from .trace/settings.json,
// then applies any overrides from .trace/settings.local.json if it exists.
// Returns default settings if neither file exists.
// Works correctly from any subdirectory within the repository.
func Load(ctx context.Context) (*TraceSettings, error) {
	// Get absolute paths for settings files
	settingsFileAbs, err := paths.AbsPath(ctx, TraceSettingsFile)
	if err != nil {
		settingsFileAbs = TraceSettingsFile // Fallback to relative
	}
	localSettingsFileAbs, err := paths.AbsPath(ctx, TraceSettingsLocalFile)
	if err != nil {
		localSettingsFileAbs = TraceSettingsLocalFile // Fallback to relative
	}

	return loadMergedSettings(settingsFileAbs, localSettingsFileAbs)
}

func loadMergedSettings(settingsFileAbs, localSettingsFileAbs string) (*TraceSettings, error) {
	// Load base settings
	settings, err := loadFromFile(settingsFileAbs)
	if err != nil {
		return nil, fmt.Errorf("reading settings file: %w", err)
	}

	// Apply local overrides if they exist
	localData, err := os.ReadFile(localSettingsFileAbs) // #nosec G304 -- path is from AbsPath or constant, not external input
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading local settings file: %w", err)
		}
		// Local file doesn't exist, continue without overrides
	} else {
		if err := mergeJSON(settings, localData); err != nil {
			return nil, fmt.Errorf("merging local settings: %w", err)
		}
	}

	// Re-validate after merge. Individual files are validated by loadFromFile,
	// but mergeJSON patches fields independently and can produce combinations
	// (e.g. model without provider when the local override sets only a model
	// on top of a base with no provider) that neither file alone contained.
	if err := settings.SummaryGeneration.Validate(); err != nil {
		return nil, fmt.Errorf("merged settings invalid: %w", err)
	}

	return settings, nil
}

// LoadFromFile loads settings from a specific file path without merging local overrides.
// Returns default settings if the file doesn't exist.
// Use this when you need to display individual settings files separately.
func LoadFromFile(filePath string) (*TraceSettings, error) {
	return loadFromFile(filePath)
}

// LoadFromBytes parses settings from raw JSON bytes without merging local overrides.
// Use this when you have settings content from a non-file source (e.g., git show).
func LoadFromBytes(data []byte) (*TraceSettings, error) {
	s := &TraceSettings{Enabled: true}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(s); err != nil {
		return nil, fmt.Errorf("parsing settings: %w", err)
	}
	return s, nil
}

// loadFromFile loads settings from a specific file path.
// Returns default settings if the file doesn't exist.
func loadFromFile(filePath string) (*TraceSettings, error) {
	settings := &TraceSettings{
		Enabled: true, // Default to enabled
	}

	data, err := os.ReadFile(filePath) // #nosec G304 -- path is from AbsPath or constant, not external input
	if err != nil {
		if os.IsNotExist(err) {
			return settings, nil
		}
		return nil, fmt.Errorf("%w", err)
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(settings); err != nil {
		return nil, fmt.Errorf("parsing settings file: %w", err)
	}

	// Validate commit_linking if set
	if settings.CommitLinking != "" && settings.CommitLinking != CommitLinkingAlways && settings.CommitLinking != CommitLinkingPrompt {
		return nil, fmt.Errorf("invalid commit_linking value %q: must be %q or %q", settings.CommitLinking, CommitLinkingAlways, CommitLinkingPrompt)
	}

	// SummaryGeneration is NOT validated here — individual files may
	// legitimately contain only a model (provider comes from another file).
	// Validation happens after merge in Load().

	return settings, nil
}

// mergeJSON merges JSON data into existing settings.
// Only non-zero values from the JSON override existing settings.
func mergeJSON(settings *TraceSettings, data []byte) error {
	// Validate that there are no unknown keys using strict decoding.
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var temp TraceSettings
	if err := dec.Decode(&temp); err != nil {
		return fmt.Errorf("parsing JSON: %w", err)
	}

	// Parse into a map to check which fields are present.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing JSON: %w", err)
	}

	if err := mergeScalarFields(settings, raw); err != nil {
		return err
	}
	if err := mergeStrategyOptions(settings, raw); err != nil {
		return err
	}
	if err := mergeSummaryGeneration(settings, raw); err != nil {
		return err
	}
	if err := mergeCommitLinking(settings, raw); err != nil {
		return err
	}

	// Merge redaction sub-fields if present (field-level, not wholesale replace).
	if redactionRaw, ok := raw["redaction"]; ok {
		if settings.Redaction == nil {
			settings.Redaction = &RedactionSettings{}
		}
		if err := mergeRedaction(settings.Redaction, redactionRaw); err != nil {
			return fmt.Errorf("parsing redaction field: %w", err)
		}
	}

	// Merge investigate config if present (wholesale replace — the struct
	// is small and self-contained, so field-level merging adds complexity
	// without benefit).
	if investigateRaw, ok := raw["investigate"]; ok {
		if settings.Investigate == nil {
			settings.Investigate = &InvestigateConfig{}
		}
		if err := json.Unmarshal(investigateRaw, settings.Investigate); err != nil {
			return fmt.Errorf("parsing investigate field: %w", err)
		}
	}

	// Merge attribution sub-fields independently so a local override can flip
	// a single flag without resetting the other two to their defaults.
	if attrRaw, ok := raw["attribution"]; ok {
		if settings.Attribution == nil {
			settings.Attribution = &AttributionSettings{}
		}
		if err := mergeAttribution(settings.Attribution, attrRaw); err != nil {
			return fmt.Errorf("parsing attribution field: %w", err)
		}
	}

	// Webhooks and CI configs merge wholesale (small, self-contained structs).
	if webhooksRaw, ok := raw["webhooks"]; ok {
		if settings.Webhooks == nil {
			settings.Webhooks = &WebhookConfig{}
		}
		if err := json.Unmarshal(webhooksRaw, settings.Webhooks); err != nil {
			return fmt.Errorf("parsing webhooks field: %w", err)
		}
	}
	if ciRaw, ok := raw["ci"]; ok {
		if settings.CI == nil {
			settings.CI = &CIConfig{}
		}
		if err := json.Unmarshal(ciRaw, settings.CI); err != nil {
			return fmt.Errorf("parsing ci field: %w", err)
		}
	}

	return nil
}

// mergeAttribution merges the three attribution flags field-by-field so that
// each may be overridden independently by a local settings file.
func mergeAttribution(attr *AttributionSettings, data json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("parsing attribution: %w", err)
	}
	if err := mergeRawBoolPtr(fields, "attribute_author", &attr.AttributeAuthor); err != nil {
		return err
	}
	if err := mergeRawBoolPtr(fields, "attribute_committer", &attr.AttributeCommitter); err != nil {
		return err
	}
	if err := mergeRawBoolPtr(fields, "attribute_co_authored_by", &attr.AttributeCoAuthoredBy); err != nil {
		return err
	}
	return nil
}

// mergeScalarFields merges simple bool, *bool, string, and int fields from raw JSON.
func mergeScalarFields(settings *TraceSettings, raw map[string]json.RawMessage) error {
	if err := mergeRawBool(raw, "enabled", &settings.Enabled); err != nil {
		return err
	}
	if err := mergeRawBool(raw, "local_dev", &settings.LocalDev); err != nil {
		return err
	}
	if err := mergeRawBool(raw, "absolute_git_hook_path", &settings.AbsoluteGitHookPath); err != nil {
		return err
	}
	if err := mergeRawBool(raw, "external_agents", &settings.ExternalAgents); err != nil {
		return err
	}
	if err := mergeRawBool(raw, "vercel", &settings.Vercel); err != nil {
		return err
	}
	if err := mergeRawBoolPtr(raw, "telemetry", &settings.Telemetry); err != nil {
		return err
	}
	if err := mergeRawBoolPtr(raw, "sign_checkpoint_commits", &settings.SignCheckpointCommits); err != nil {
		return err
	}
	if err := mergeRawBoolPtr(raw, "dirty_commits", &settings.DirtyCommits); err != nil {
		return err
	}
	if err := mergeRawStringNonEmpty(raw, "log_level", &settings.LogLevel); err != nil {
		return err
	}
	if err := mergeRawInt(raw, "summary_timeout_seconds", &settings.SummaryTimeoutSeconds); err != nil {
		return err
	}
	return nil
}

func mergeRawBool(raw map[string]json.RawMessage, key string, dst *bool) error {
	v, ok := raw[key]
	if !ok {
		return nil
	}
	return unmarshalField(key, v, dst)
}

func mergeRawBoolPtr(raw map[string]json.RawMessage, key string, dst **bool) error {
	v, ok := raw[key]
	if !ok {
		return nil
	}
	var b bool
	if err := unmarshalField(key, v, &b); err != nil {
		return err
	}
	*dst = &b
	return nil
}

func mergeRawStringNonEmpty(raw map[string]json.RawMessage, key string, dst *string) error {
	v, ok := raw[key]
	if !ok {
		return nil
	}
	var s string
	if err := unmarshalField(key, v, &s); err != nil {
		return err
	}
	if s != "" {
		*dst = s
	}
	return nil
}

func mergeRawInt(raw map[string]json.RawMessage, key string, dst *int) error {
	v, ok := raw[key]
	if !ok {
		return nil
	}
	return unmarshalField(key, v, dst)
}

func unmarshalField(key string, data json.RawMessage, dst any) error {
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("parsing %s field: %w", key, err)
	}
	return nil
}

func mergeStrategyOptions(settings *TraceSettings, raw map[string]json.RawMessage) error {
	optionsRaw, ok := raw["strategy_options"]
	if !ok {
		return nil
	}
	var opts map[string]any
	if err := unmarshalField("strategy_options", optionsRaw, &opts); err != nil {
		return err
	}
	if settings.StrategyOptions == nil {
		settings.StrategyOptions = opts
	} else {
		for k, v := range opts {
			settings.StrategyOptions[k] = v
		}
	}
	return nil
}

func mergeSummaryGeneration(settings *TraceSettings, raw map[string]json.RawMessage) error {
	summaryRaw, ok := raw["summary_generation"]
	if !ok {
		return nil
	}
	if settings.SummaryGeneration == nil {
		settings.SummaryGeneration = &SummaryGenerationSettings{}
	}

	var summaryFields map[string]json.RawMessage
	if err := unmarshalField("summary_generation", summaryRaw, &summaryFields); err != nil {
		return err
	}

	_, modelInOverride := summaryFields["model"]

	if providerRaw, ok := summaryFields["provider"]; ok {
		var provider string
		if err := unmarshalField("summary_generation.provider", providerRaw, &provider); err != nil {
			return err
		}
		// If the override switches providers without also setting a model,
		// the base's model was tuned to the old provider and would likely
		// cause a runtime failure when handed to the new one (e.g. codex
		// rejecting "sonnet"). Clear it so the new provider falls back to
		// its own default.
		if provider != settings.SummaryGeneration.Provider && !modelInOverride {
			settings.SummaryGeneration.Model = ""
		}
		settings.SummaryGeneration.Provider = provider
	}

	if modelRaw, ok := summaryFields["model"]; ok {
		var model string
		if err := unmarshalField("summary_generation.model", modelRaw, &model); err != nil {
			return err
		}
		settings.SummaryGeneration.Model = model
	}
	return nil
}

func mergeCommitLinking(settings *TraceSettings, raw map[string]json.RawMessage) error {
	commitLinkingRaw, ok := raw["commit_linking"]
	if !ok {
		return nil
	}
	var cl string
	if err := unmarshalField("commit_linking", commitLinkingRaw, &cl); err != nil {
		return err
	}
	if cl == "" {
		return nil
	}
	switch cl {
	case CommitLinkingAlways, CommitLinkingPrompt:
		settings.CommitLinking = cl
	default:
		return fmt.Errorf("invalid commit_linking value %q: must be %q or %q", cl, CommitLinkingAlways, CommitLinkingPrompt)
	}
	return nil
}

// mergeRedaction merges redaction overrides into existing RedactionSettings.
// Only fields present in the override JSON are applied.
func mergeRedaction(dst *RedactionSettings, data json.RawMessage) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing redaction: %w", err)
	}
	if piiRaw, ok := raw["pii"]; ok {
		if dst.PII == nil {
			dst.PII = &PIISettings{}
		}
		if err := mergePIISettings(dst.PII, piiRaw); err != nil {
			return err
		}
	}
	return nil
}
