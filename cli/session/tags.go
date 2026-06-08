package session

import (
	"os"
	"strings"
)

// tagEnvPrefix is the environment variable prefix used for session tags.
const tagEnvPrefix = "TRACE_TAG_"

// CollectSessionTags scans os.Environ() for TRACE_TAG_* environment variables
// and returns a normalized map of key-value pairs.
//
// Normalization rules:
//   - The TRACE_TAG_ prefix is stripped from each key.
//   - Keys are lowercased.
//   - Hyphens in keys are replaced with underscores.
//
// Variables with empty values are excluded. If no TRACE_TAG_ variables are
// found, an empty (non-nil) map is returned.
//
// Examples:
//
//	TRACE_TAG_PROJECT=my-app     -> metadata["project"]="my-app"
//	TRACE_TAG_ENVIRONMENT=staging -> metadata["environment"]="staging"
//	TRACE_TAG_MY-TAG=value       -> metadata["my_tag"]="value"
//	TRACE_TAG_EMPTY=             -> (excluded)
func CollectSessionTags() map[string]string {
	return collectTagsFromEnv(os.Environ())
}

// collectTagsFromEnv is the testable core of CollectSessionTags, accepting
// an explicit environment list instead of reading os.Environ() directly.
func collectTagsFromEnv(environ []string) map[string]string {
	tags := make(map[string]string)
	for _, env := range environ {
		key, value, ok := cutEnvVar(env)
		if !ok {
			continue
		}
		if !strings.HasPrefix(key, tagEnvPrefix) {
			continue
		}
		if value == "" {
			continue
		}
		normalized := normalizeTagKey(key)
		if normalized != "" {
			tags[normalized] = value
		}
	}
	return tags
}

// cutEnvVar splits an environment variable string of the form "KEY=VALUE" into
// key and value. Returns ("", "", false) if the input does not contain '='.
// This is equivalent to strings.Cut but isolated for clarity and testability.
func cutEnvVar(env string) (key, value string, ok bool) {
	idx := strings.IndexByte(env, '=')
	if idx < 0 {
		return "", "", false
	}
	return env[:idx], env[idx+1:], true
}

// normalizeTagKey strips the TRACE_TAG_ prefix, lowercases the remainder,
// and replaces hyphens with underscores. Returns an empty string if the
// resulting key would be empty (e.g., the bare variable "TRACE_TAG_").
func normalizeTagKey(key string) string {
	// Strip prefix.
	normalized := strings.TrimPrefix(key, tagEnvPrefix)

	// Lowercase.
	normalized = strings.ToLower(normalized)

	// Replace hyphens with underscores.
	normalized = strings.ReplaceAll(normalized, "-", "_")

	return normalized
}
