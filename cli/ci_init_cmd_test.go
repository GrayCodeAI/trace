package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/settings"
)

// stubEnv builds a getenvFunc backed by a fixed map (parallel-safe; no
// process env mutation).
func stubEnv(m map[string]string) getenvFunc {
	return func(name string) string { return m[name] }
}

func TestDetectCIProvider(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		env      map[string]string
		want     string
		detected bool
	}{
		{"github actions", map[string]string{"GITHUB_ACTIONS": "true"}, "github-actions", true},
		{"gitlab ci", map[string]string{"GITLAB_CI": "true"}, "gitlab-ci", true},
		{"generic ci fallback", map[string]string{"CI": "true"}, "ci", true},
		{"github wins over generic", map[string]string{"CI": "true", "GITHUB_ACTIONS": "true"}, "github-actions", true},
		{"none", map[string]string{}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, ok := detectCIProvider(stubEnv(tc.env))
			if ok != tc.detected {
				t.Fatalf("detected = %v, want %v", ok, tc.detected)
			}
			if p.name != tc.want {
				t.Errorf("provider = %q, want %q", p.name, tc.want)
			}
		})
	}
}

func TestResolveCITags_GitHub(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"GITHUB_ACTIONS":    "true",
		"GITHUB_RUN_ID":     "12345",
		"GITHUB_REF_NAME":   "feature/x",
		"GITHUB_HEAD_REF":   "pr-branch",
		"GITHUB_SHA":        "abcdef",
		"GITHUB_REPOSITORY": "org/repo",
		// GITHUB_REF intentionally unset → must be dropped.
	}
	p, _ := detectCIProvider(stubEnv(env))
	tags := resolveCITags(p, stubEnv(env))

	want := map[string]string{
		"ci_run_id":  "12345",
		"branch":     "feature/x",
		"pr_ref":     "pr-branch",
		"sha":        "abcdef",
		"repository": "org/repo",
	}
	for k, v := range want {
		if tags[k] != v {
			t.Errorf("tag %q = %q, want %q", k, tags[k], v)
		}
	}
	if _, ok := tags["ref"]; ok {
		t.Errorf("empty GITHUB_REF should not produce a 'ref' tag, got %q", tags["ref"])
	}
}

func TestApplyCIInit_EnablesAutoCaptureAndProvider(t *testing.T) {
	t.Parallel()
	s := &settings.TraceSettings{Enabled: true}
	env := map[string]string{"GITLAB_CI": "true", "CI_PIPELINE_ID": "777", "CI_COMMIT_REF_NAME": "main"}

	provider, tags, detected := applyCIInit(s, stubEnv(env))

	if !detected {
		t.Fatal("expected CI detection")
	}
	if provider != "gitlab-ci" {
		t.Errorf("provider = %q, want gitlab-ci", provider)
	}
	if s.CI == nil || !s.CI.AutoCapture {
		t.Fatal("expected CI.AutoCapture to be true")
	}
	if s.CI.Provider != "gitlab-ci" {
		t.Errorf("CI.Provider = %q, want gitlab-ci", s.CI.Provider)
	}
	if tags["ci_run_id"] != "777" || tags["branch"] != "main" {
		t.Errorf("unexpected tags: %v", tags)
	}
}

func TestRunCIInit_WritesSettingsAndPrints(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	env := map[string]string{
		"GITHUB_ACTIONS":  "true",
		"GITHUB_RUN_ID":   "999",
		"GITHUB_REF_NAME": "release",
	}

	var out bytes.Buffer
	if err := runCIInit(context.Background(), &out, stubEnv(env)); err != nil {
		t.Fatalf("runCIInit error = %v", err)
	}

	// Settings should persist with CI auto-capture enabled.
	loaded, err := settings.Load(context.Background())
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if loaded.CI == nil || !loaded.CI.AutoCapture {
		t.Fatal("expected persisted CI.AutoCapture = true")
	}
	if loaded.CI.Provider != "github-actions" {
		t.Errorf("persisted provider = %q, want github-actions", loaded.CI.Provider)
	}

	// Output should report provider and the detected run tags.
	got := out.String()
	if !strings.Contains(got, "github-actions") {
		t.Errorf("output missing provider name: %q", got)
	}
	if !strings.Contains(got, "ci_run_id = 999") {
		t.Errorf("output missing run id tag: %q", got)
	}
	if !strings.Contains(got, "branch = release") {
		t.Errorf("output missing branch tag: %q", got)
	}
}

func TestRunCIInit_NoCIProvider(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	var out bytes.Buffer
	if err := runCIInit(context.Background(), &out, stubEnv(map[string]string{})); err != nil {
		t.Fatalf("runCIInit error = %v", err)
	}

	loaded, err := settings.Load(context.Background())
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	// Auto-capture is still enabled, but no provider recorded.
	if loaded.CI == nil || !loaded.CI.AutoCapture {
		t.Fatal("expected CI.AutoCapture = true even without provider")
	}
	if loaded.CI.Provider != "" {
		t.Errorf("provider should be empty, got %q", loaded.CI.Provider)
	}
	if !strings.Contains(out.String(), "No CI provider detected") {
		t.Errorf("expected 'No CI provider detected' message, got %q", out.String())
	}
}
