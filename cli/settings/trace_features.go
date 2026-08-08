package settings

import (
	"context"
	"encoding/json"
	"fmt"
)

// TraceSettings is the trace-facing name for the settings type. Kept as an
// alias so trace-only callers written before the upstream rename keep working.
type TraceSettings = EntireSettings

// LoadTraceSettings loads settings using the trace-facing name.
func LoadTraceSettings(ctx context.Context) (*EntireSettings, error) {
	return Load(ctx)
}

// AttributionSettings controls git identity attribution for Trace-created
// commits.
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
func (s *EntireSettings) AttributeAuthor() bool {
	if s == nil || s.Attribution == nil || s.Attribution.AttributeAuthor == nil {
		return false
	}
	return *s.Attribution.AttributeAuthor
}

// AttributeCommitter reports whether the git committer identity should be
// overridden with the agent identity. Defaults to false when unset.
func (s *EntireSettings) AttributeCommitter() bool {
	if s == nil || s.Attribution == nil || s.Attribution.AttributeCommitter == nil {
		return false
	}
	return *s.Attribution.AttributeCommitter
}

// AttributeCoAuthoredBy reports whether a Co-authored-by trailer should be
// appended to commit messages. Defaults to true when unset (Aider-compatible).
func (s *EntireSettings) AttributeCoAuthoredBy() bool {
	if s == nil || s.Attribution == nil || s.Attribution.AttributeCoAuthoredBy == nil {
		return true
	}
	return *s.Attribution.AttributeCoAuthoredBy
}

// DirtyCommitsEnabled reports whether pre-session WIP auto-commits are enabled.
// Defaults to true when unset (Aider-compatible).
func (s *EntireSettings) DirtyCommitsEnabled() bool {
	if s == nil || s.DirtyCommits == nil {
		return true
	}
	return *s.DirtyCommits
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
}

// mergeAttribution merges per-field attribution overrides from raw JSON.
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

// mergeTraceExtensions merges the trace-only top-level sections (attribution,
// webhooks, ci) wholesale from raw JSON. These are small, self-contained
// structs with no per-field merge semantics.
func mergeTraceExtensions(settings *EntireSettings, raw map[string]json.RawMessage) error {
	if attrRaw, ok := raw["attribution"]; ok {
		if settings.Attribution == nil {
			settings.Attribution = &AttributionSettings{}
		}
		if err := mergeAttribution(settings.Attribution, attrRaw); err != nil {
			return err
		}
	}
	if err := mergeRawBoolPtr(raw, "dirty_commits", &settings.DirtyCommits); err != nil {
		return err
	}
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
