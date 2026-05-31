package session

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectSessionTags(t *testing.T) {
	t.Run("collects_TRACE_TAG_prefix", func(t *testing.T) {
		t.Setenv("TRACE_TAG_PROJECT", "my-app")
		t.Setenv("TRACE_TAG_ENVIRONMENT", "staging")
		t.Setenv("NOT_A_TAG", "ignored")

		tags := CollectSessionTags()
		assert.Equal(t, "my-app", tags["project"])
		assert.Equal(t, "staging", tags["environment"])
		assert.Len(t, tags, 2)
	})

	t.Run("strips_prefix", func(t *testing.T) {
		t.Setenv("TRACE_TAG_REGION", "us-east-1")

		tags := CollectSessionTags()
		assert.Equal(t, "us-east-1", tags["region"])
		assert.NotContains(t, tags, "TRACE_TAG_REGION")
	})

	t.Run("lowercases_keys", func(t *testing.T) {
		t.Setenv("TRACE_TAG_PROJECT_NAME", "test")

		tags := CollectSessionTags()
		assert.Equal(t, "test", tags["project_name"])
	})

	t.Run("replaces_hyphens_with_underscores", func(t *testing.T) {
		t.Setenv("TRACE_TAG_MY-TAG", "value")

		tags := CollectSessionTags()
		assert.Equal(t, "value", tags["my_tag"])
		assert.NotContains(t, tags, "my-tag")
	})

	t.Run("skips_empty_values", func(t *testing.T) {
		t.Setenv("TRACE_TAG_EMPTY", "")
		t.Setenv("TRACE_TAG_SET", "yes")

		tags := CollectSessionTags()
		assert.NotContains(t, tags, "empty")
		assert.Equal(t, "yes", tags["set"])
	})

	t.Run("bare_TRACE_TAG_is_excluded", func(t *testing.T) {
		t.Setenv("TRACE_TAG_", "value")

		tags := CollectSessionTags()
		// After normalization, the key would be "" which is rejected.
		assert.NotContains(t, tags, "")
	})

	t.Run("returns_empty_map_when_no_tags", func(t *testing.T) {
		// Use an isolated environment so no TRACE_TAG_ vars leak in.
		tags := collectTagsFromEnv([]string{
			"HOME=/home/user",
			"PATH=/usr/bin",
		})
		assert.NotNil(t, tags, "should return non-nil map")
		assert.Len(t, tags, 0)
	})

	t.Run("multiple_tags_preserved", func(t *testing.T) {
		tags := collectTagsFromEnv([]string{
			"TRACE_TAG_PROJECT=webapp",
			"TRACE_TAG_TEAM=platform",
			"TRACE_TAG_ENV=prod",
		})
		assert.Len(t, tags, 3)
		assert.Equal(t, "webapp", tags["project"])
		assert.Equal(t, "platform", tags["team"])
		assert.Equal(t, "prod", tags["env"])
	})
}

func TestCollectTagsFromEnv(t *testing.T) {
	t.Run("handles_no_equals", func(t *testing.T) {
		tags := collectTagsFromEnv([]string{
			"TRACE_TAG_INVALID_NO_EQUALS",
			"TRACE_TAG_VALID=yes",
		})
		assert.Len(t, tags, 1)
		assert.Equal(t, "yes", tags["valid"])
	})

	t.Run("handles_value_with_equals", func(t *testing.T) {
		tags := collectTagsFromEnv([]string{
			"TRACE_TAG_URL=https://example.com?foo=bar",
		})
		assert.Equal(t, "https://example.com?foo=bar", tags["url"])
	})

	t.Run("handles_empty_env_list", func(t *testing.T) {
		tags := collectTagsFromEnv(nil)
		assert.NotNil(t, tags)
		assert.Len(t, tags, 0)
	})

	t.Run("case_insensitive_prefix_match", func(t *testing.T) {
		tags := collectTagsFromEnv([]string{
			"trace_tag_lower=value", // lowercase prefix does not match
			"TRACE_TAG_UPPER=value", // uppercase prefix matches
		})
		assert.NotContains(t, tags, "trace_tag_lower", "lowercase prefix should not match")
		assert.Contains(t, tags, "upper")
	})

	t.Run("key_normalization_is_idempotent", func(t *testing.T) {
		tags := collectTagsFromEnv([]string{
			"TRACE_TAG_MY_COOL_TAG=v1",
			"TRACE_TAG_MY-COOL-TAG=v2",
		})
		// Both normalize to "my_cool_tag", last one wins (map iteration order).
		assert.Contains(t, tags, "my_cool_tag")
	})
}

func TestNormalizeTagKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"TRACE_TAG_PROJECT", "project"},
		{"TRACE_TAG_", ""},
		{"TRACE_TAG_MY-TAG", "my_tag"},
		{"TRACE_TAG_MULTI-PART-KEY", "multi_part_key"},
		{"TRACE_TAG_ALLCAPS", "allcaps"},
		{"TRACE_TAG_mixed_Case", "mixed_case"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeTagKey(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCutEnvVar(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantKey string
		wantVal string
		wantOK  bool
	}{
		{"simple", "KEY=VALUE", "KEY", "VALUE", true},
		{"empty_value", "KEY=", "KEY", "", true},
		{"value_with_equals", "KEY=a=b", "KEY", "a=b", true},
		{"no_equals", "KEYVALUE", "", "", false},
		{"empty_string", "", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, val, ok := cutEnvVar(tt.input)
			assert.Equal(t, tt.wantKey, key)
			assert.Equal(t, tt.wantVal, val)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}

func TestState_Metadata_Serialization(t *testing.T) {
	t.Run("metadata_round_trips_through_json", func(t *testing.T) {
		t.Parallel()
		state := &State{
			SessionID: "test-session",
			StartedAt: time.Now(),
			Metadata: map[string]string{
				"project":     "my-app",
				"environment": "staging",
			},
		}

		dir := t.TempDir()
		store := NewStateStoreWithDir(dir)
		require.NoError(t, store.Save(t.Context(), state))

		loaded, err := store.Load(t.Context(), "test-session")
		require.NoError(t, err)
		require.NotNil(t, loaded)
		assert.Equal(t, "my-app", loaded.Metadata["project"])
		assert.Equal(t, "staging", loaded.Metadata["environment"])
		assert.Len(t, loaded.Metadata, 2)
	})

	t.Run("nil_metadata_round_trips_as_nil", func(t *testing.T) {
		t.Parallel()
		state := &State{
			SessionID: "test-nil-metadata",
			StartedAt: time.Now(),
		}

		dir := t.TempDir()
		store := NewStateStoreWithDir(dir)
		require.NoError(t, store.Save(t.Context(), state))

		loaded, err := store.Load(t.Context(), "test-nil-metadata")
		require.NoError(t, err)
		require.NotNil(t, loaded)
		assert.Nil(t, loaded.Metadata)
	})
}

func TestCollectSessionTags_Integration(t *testing.T) {
	// Verify that CollectSessionTags can be used to populate Metadata on State.
	tags := collectTagsFromEnv([]string{
		"TRACE_TAG_PROJECT=my-app",
		"TRACE_TAG_TEAM=platform",
	})

	state := &State{
		SessionID: "integration-test",
		Metadata:  tags,
	}

	assert.Equal(t, "my-app", state.Metadata["project"])
	assert.Equal(t, "platform", state.Metadata["team"])
}

// Verify that os.Setenv-based tag collection works end-to-end.
func TestCollectSessionTags_WithOsSetenv(t *testing.T) {
	// Capture original state so we can restore after the test.
	origVars := map[string]string{
		"TRACE_TAG_TEST_OS_SETENV":     "",
		"TRACE_TAG_TEST_OS_SETENV_TWO": "",
	}
	for k := range origVars {
		origVars[k] = os.Getenv(k)
	}
	t.Cleanup(func() {
		for k, v := range origVars {
			if v == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, v)
			}
		}
	})

	os.Setenv("TRACE_TAG_TEST_OS_SETENV", "from-setenv")
	os.Setenv("TRACE_TAG_TEST_OS_SETENV_TWO", "second")

	tags := CollectSessionTags()
	assert.Equal(t, "from-setenv", tags["test_os_setenv"])
	assert.Equal(t, "second", tags["test_os_setenv_two"])
}
