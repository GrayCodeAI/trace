package redact

import (
	"context"
	"os/exec"
	"sync"
	"testing"
)

func shCmd(ctx context.Context, script string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", script)
}

type fakeRuntime struct {
	mu         sync.Mutex
	spans      []Span
	err        error
	calls      int
	batchCalls int
}

func (f *fakeRuntime) Redact(_ context.Context, _ string, _ []string) ([]Span, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.spans, f.err
}

func (f *fakeRuntime) RedactBatch(_ context.Context, inputs []string, _ []string) ([][]Span, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batchCalls++
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]Span, len(inputs))
	for i := range inputs {
		out[i] = f.spans
	}
	return out, nil
}

func TestConfigurePrivacyFilter_StoresConfig(t *testing.T) {
	resetOPFConfig()
	t.Cleanup(resetOPFConfig)

	ConfigurePrivacyFilter(OPFConfig{
		Enabled:    true,
		Categories: map[string]bool{"private_person": true},
		Command:    "/usr/local/bin/opf",
		Timeout:    45,
	})

	got := getOPFConfig()
	if got == nil {
		t.Fatal("getOPFConfig returned nil")
	}
	if !got.Enabled || !got.Categories["private_person"] || got.Command != "/usr/local/bin/opf" || got.Timeout != 45 {
		t.Errorf("config not stored verbatim: %+v", got)
	}
	if got.runtime == nil {
		t.Error("runtime was not constructed")
	}
}

func TestConfigurePrivacyFilter_AppliesDefaults(t *testing.T) {
	resetOPFConfig()
	t.Cleanup(resetOPFConfig)

	ConfigurePrivacyFilter(OPFConfig{Enabled: true})
	got := getOPFConfig()
	if got.Command != "opf" {
		t.Errorf("default Command: want \"opf\", got %q", got.Command)
	}
	if got.Timeout != 30 {
		t.Errorf("default Timeout: want 30, got %d", got.Timeout)
	}
}

func TestIsKnownOPFCategory(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"private_person":  true,
		"private_email":   true,
		"secret":          true,
		"account_number":  true,
		"private_peerson": false,
		"":                false,
		"PII":             false,
	}
	for name, want := range cases {
		if got := IsKnownOPFCategory(name); got != want {
			t.Errorf("IsKnownOPFCategory(%q) = %v, want %v", name, got, want)
		}
	}
}
