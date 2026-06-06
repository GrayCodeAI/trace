package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/settings"
	"github.com/spf13/cobra"
)

// osGetenv is the production environment reader; tests inject a stub instead.
func osGetenv(name string) string { return os.Getenv(name) }

// ciProvider identifies a recognized CI provider and how to read its
// run-identifying environment variables. Detection reuses the allowlist
// semantics from plugin_env.go (GITHUB_ACTIONS / GITLAB_CI presence), but the
// run-id / PR / branch tags are provider-specific.
type ciProvider struct {
	// name is the stable provider identifier persisted to settings.CI.Provider.
	name string
	// detectVar is the env var whose presence (non-empty) marks this provider.
	detectVar string
	// tag maps a stable tag key to the env var that supplies its value. Empty
	// values are dropped when building the run-time tag set.
	tag map[string]string
}

// ciProviders is ordered: the first provider whose detectVar is set wins. CI
// (the generic flag) is intentionally absent here — it is only used as a
// fallback signal in detectCIProvider.
var ciProviders = []ciProvider{
	{
		name:      "github-actions",
		detectVar: "GITHUB_ACTIONS",
		tag: map[string]string{
			"ci_run_id":      "GITHUB_RUN_ID",
			"ci_run_attempt": "GITHUB_RUN_ATTEMPT",
			"branch":         "GITHUB_REF_NAME",
			"ref":            "GITHUB_REF",
			"pr_ref":         "GITHUB_HEAD_REF",
			"sha":            "GITHUB_SHA",
			"repository":     "GITHUB_REPOSITORY",
		},
	},
	{
		name:      "gitlab-ci",
		detectVar: "GITLAB_CI",
		tag: map[string]string{
			"ci_run_id":  "CI_PIPELINE_ID",
			"ci_job_id":  "CI_JOB_ID",
			"branch":     "CI_COMMIT_REF_NAME",
			"pr_number":  "CI_MERGE_REQUEST_IID",
			"sha":        "CI_COMMIT_SHA",
			"repository": "CI_PROJECT_PATH",
		},
	},
}

// getenvFunc abstracts os.Getenv so tests can inject a fixed environment
// without mutating process state (keeps tests parallel-safe).
type getenvFunc func(string) string

// detectCIProvider returns the recognized provider for the given environment,
// or (zero, false) when none matches. A bare CI=true with no recognized
// provider is reported as the generic "ci" provider so auto-capture can still
// be enabled with no provider-specific tags.
func detectCIProvider(getenv getenvFunc) (ciProvider, bool) {
	for _, p := range ciProviders {
		if getenv(p.detectVar) != "" {
			return p, true
		}
	}
	if getenv("CI") != "" {
		return ciProvider{name: "ci"}, true
	}
	return ciProvider{}, false
}

// resolveCITags reads the provider's env vars and returns the non-empty tag
// set for the current run. Returns an empty (non-nil) map when nothing is set.
func resolveCITags(p ciProvider, getenv getenvFunc) map[string]string {
	tags := make(map[string]string, len(p.tag))
	for key, envVar := range p.tag {
		if v := getenv(envVar); v != "" {
			tags[key] = v
		}
	}
	return tags
}

// applyCIInit builds (or updates) the CI configuration on the given settings
// from the environment. It returns the resolved provider, the run-time tags it
// derived (for printing), and whether a recognized CI environment was found.
// Settings are mutated in place; the caller persists them.
func applyCIInit(s *settings.TraceSettings, getenv getenvFunc) (provider string, runTags map[string]string, detected bool) {
	p, ok := detectCIProvider(getenv)
	if s.CI == nil {
		s.CI = &settings.CIConfig{}
	}
	s.CI.AutoCapture = true
	s.CI.Provider = p.name

	tags := resolveCITags(p, getenv)
	return p.name, tags, ok
}

func newCIInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ci-init",
		Short: "Configure Trace to auto-capture sessions in CI",
		Long: `Configure Trace to automatically capture agent sessions when running in CI.

Detects the CI provider from environment variables (GitHub Actions, GitLab CI)
and writes a "ci" block to .trace/settings.json enabling auto-capture. The run
id, PR/branch, and commit SHA are read from the environment on each run and
attached to captured sessions as tags.

Run this once as a step in your CI pipeline before invoking your agent.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCIInit(cmd.Context(), cmd.OutOrStdout(), osGetenv)
		},
	}
}

// runCIInit loads settings, applies CI config from the environment, persists,
// and prints what it configured.
func runCIInit(ctx context.Context, w io.Writer, getenv getenvFunc) error {
	s, err := settings.Load(ctx)
	if err != nil {
		return fmt.Errorf("loading settings: %w", err)
	}

	provider, tags, detected := applyCIInit(s, getenv)

	if err := settings.Save(ctx, s); err != nil {
		return fmt.Errorf("saving settings: %w", err)
	}

	if !detected {
		fmt.Fprintln(w, "No CI provider detected from the environment.")
		fmt.Fprintln(w, "Auto-capture has been enabled; run this command inside CI to record run tags.")
		return nil
	}

	fmt.Fprintf(w, "Configured Trace CI auto-capture for provider: %s\n", provider)
	if len(tags) == 0 {
		fmt.Fprintln(w, "No run tags found in the environment yet.")
		return nil
	}
	fmt.Fprintln(w, "Run tags detected:")
	for _, k := range sortedKeys(tags) {
		fmt.Fprintf(w, "  %s = %s\n", k, tags[k])
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
