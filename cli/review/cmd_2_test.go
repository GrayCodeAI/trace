package review_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent/types"
	"github.com/GrayCodeAI/trace/cli/review"
	reviewtypes "github.com/GrayCodeAI/trace/cli/review/types"
	"github.com/GrayCodeAI/trace/cli/settings"
)

// TestComposeMultiAgentSinks exercises the sink-composition helper directly
// with explicit isTTY/canPrompt values, so we get real coverage of the TTY
// branch without depending on os.Stdout being a terminal during `go test`.
func TestComposeMultiAgentSinks(t *testing.T) {
	t.Parallel()

	provider := &stubCmdSynthesisProvider{}
	noopCancel := func() {}

	tests := []struct {
		name      string
		isTTY     bool
		canPrompt bool
		provider  review.SynthesisProvider
		wantTUI   bool
		wantDump  bool
		wantSynth bool
		wantTotal int
	}{
		{
			name:      "non-tty omits tui and synth",
			isTTY:     false,
			canPrompt: false,
			provider:  provider,
			wantDump:  true,
			wantTotal: 1,
		},
		{
			name:      "tty with provider and prompt appends synth",
			isTTY:     true,
			canPrompt: true,
			provider:  provider,
			wantTUI:   true,
			wantDump:  true,
			wantSynth: true,
			wantTotal: 3,
		},
		{
			name:      "tty without provider skips synth",
			isTTY:     true,
			canPrompt: true,
			provider:  nil,
			wantTUI:   true,
			wantDump:  true,
			wantTotal: 2,
		},
		{
			name:      "tty without prompt skips synth even with provider",
			isTTY:     true,
			canPrompt: false,
			provider:  provider,
			wantTUI:   true,
			wantDump:  true,
			wantTotal: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sinks := review.ExposedComposeMultiAgentSinks(review.SinkComposeInputs{
				Out:               &bytes.Buffer{},
				IsTTY:             tt.isTTY,
				CanPrompt:         tt.canPrompt,
				AgentNames:        []string{"a", "b"},
				CancelRun:         noopCancel,
				SynthesisProvider: tt.provider,
			})
			if got := len(sinks); got != tt.wantTotal {
				t.Fatalf("len(sinks)=%d, want %d", got, tt.wantTotal)
			}
			_, hasTUI := review.ExposedFindTUISink(sinks)
			if hasTUI != tt.wantTUI {
				t.Errorf("findTUISink found=%v, want %v", hasTUI, tt.wantTUI)
			}
			var hasDump, hasSynth bool
			for _, s := range sinks {
				switch s.(type) {
				case review.DumpSink:
					hasDump = true
				case review.SynthesisSink:
					hasSynth = true
				}
			}
			if hasDump != tt.wantDump {
				t.Errorf("DumpSink present=%v, want %v", hasDump, tt.wantDump)
			}
			if hasSynth != tt.wantSynth {
				t.Errorf("SynthesisSink present=%v, want %v", hasSynth, tt.wantSynth)
			}
		})
	}
}

func TestComposeSingleAgentSinks(t *testing.T) {
	t.Parallel()

	noopCancel := func() {}

	tests := []struct {
		name       string
		isTTY      bool
		canPrompt  bool
		wantTUI    bool
		wantDump   bool
		wantTotal  int
		wantOutput string
	}{
		{
			name:       "non-tty prints running line and uses dump only",
			wantDump:   true,
			wantTotal:  1,
			wantOutput: "Running review with agent-a...",
		},
		{
			name:      "tty uses tui and dump",
			isTTY:     true,
			canPrompt: true,
			wantTUI:   true,
			wantDump:  true,
			wantTotal: 2,
		},
		{
			name:       "tty without prompt falls back to running line",
			isTTY:      true,
			canPrompt:  false,
			wantDump:   true,
			wantTotal:  1,
			wantOutput: "Running review with agent-a...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out := &bytes.Buffer{}
			sinks := review.ExposedComposeSingleAgentSinks(review.SingleAgentSinkComposeInputs{
				Out:       out,
				IsTTY:     tt.isTTY,
				CanPrompt: tt.canPrompt,
				AgentName: "agent-a",
				CancelRun: noopCancel,
			})
			if got := len(sinks); got != tt.wantTotal {
				t.Fatalf("len(sinks)=%d, want %d", got, tt.wantTotal)
			}
			_, hasTUI := review.ExposedFindTUISink(sinks)
			if hasTUI != tt.wantTUI {
				t.Errorf("findTUISink found=%v, want %v", hasTUI, tt.wantTUI)
			}
			var hasDump, hasSynth bool
			for _, s := range sinks {
				switch s.(type) {
				case review.DumpSink:
					hasDump = true
				case review.SynthesisSink:
					hasSynth = true
				}
			}
			if hasDump != tt.wantDump {
				t.Errorf("DumpSink present=%v, want %v", hasDump, tt.wantDump)
			}
			if hasSynth {
				t.Error("SynthesisSink should not be present for single-agent reviews")
			}
			if tt.wantOutput != "" && !strings.Contains(out.String(), tt.wantOutput) {
				t.Errorf("output missing %q:\n%s", tt.wantOutput, out.String())
			}
			if tt.wantOutput == "" && out.Len() != 0 {
				t.Errorf("expected no pre-run output, got:\n%s", out.String())
			}
		})
	}
}

func TestComposeSinks_TUIWritersRunBeforePostRunWriters(t *testing.T) {
	t.Parallel()
	provider := &stubSynthesisProvider{}

	multi := review.ExposedComposeMultiAgentSinks(review.SinkComposeInputs{
		Out:               &bytes.Buffer{},
		IsTTY:             true,
		CanPrompt:         true,
		AgentNames:        []string{"a", "b"},
		CancelRun:         func() {},
		SynthesisProvider: provider,
	})
	if len(multi) != 3 {
		t.Fatalf("multi sinks len = %d, want 3", len(multi))
	}
	if _, ok := multi[0].(*review.TUISink); !ok {
		t.Fatalf("multi sink[0] = %T, want *TUISink", multi[0])
	}
	if _, ok := multi[1].(review.DumpSink); !ok {
		t.Fatalf("multi sink[1] = %T, want DumpSink", multi[1])
	}
	if _, ok := multi[2].(review.SynthesisSink); !ok {
		t.Fatalf("multi sink[2] = %T, want SynthesisSink", multi[2])
	}

	single := review.ExposedComposeSingleAgentSinks(review.SingleAgentSinkComposeInputs{
		Out:       &bytes.Buffer{},
		IsTTY:     true,
		CanPrompt: true,
		AgentName: "a",
		CancelRun: func() {},
	})
	if len(single) != 2 {
		t.Fatalf("single sinks len = %d, want 2", len(single))
	}
	if _, ok := single[0].(*review.TUISink); !ok {
		t.Fatalf("single sink[0] = %T, want *TUISink", single[0])
	}
	if _, ok := single[1].(review.DumpSink); !ok {
		t.Fatalf("single sink[1] = %T, want DumpSink", single[1])
	}
}

// TestFindTUISink_NoTUIInSlice covers the not-found path so the caller's
// `if tuiSink, ok := findTUISink(sinks); ok` branch is exercised in both
// directions.
func TestFindTUISink_NoTUIInSlice(t *testing.T) {
	t.Parallel()
	sinks := []reviewtypes.Sink{review.DumpSink{W: &bytes.Buffer{}}}
	if tui, ok := review.ExposedFindTUISink(sinks); ok || tui != nil {
		t.Errorf("findTUISink on dump-only slice returned (%v, %v); want (nil, false)", tui, ok)
	}
}

// TestDispatchFork_SynthesisSinkNilProviderNoComposition verifies that when
// deps.SynthesisProvider is nil, the command runs without panicking and does
// not attempt to synthesize (no synthesis output appears).
func TestDispatchFork_SynthesisSinkNilProviderNoComposition(t *testing.T) {
	setupCmdTestRepo(t)

	if err := review.SaveReviewConfig(context.Background(), map[string]settings.ReviewConfig{
		"agent-a": {Prompt: "review"},
		"agent-b": {Prompt: "review"},
	}); err != nil {
		t.Fatal(err)
	}

	multiPickerFn := func(_ context.Context, eligible []review.AgentChoice) (review.PickedAgents, error) {
		names := make([]string, 0, len(eligible))
		for _, e := range eligible {
			names = append(names, e.Name)
		}
		return review.PickedAgents{Names: names, PerRun: ""}, nil
	}

	installed := []types.AgentName{"agent-a", "agent-b"}
	deps := newDispatchTestDeps(t, installed, []string{"agent-a", "agent-b"}, multiPickerFn, nil)
	deps.SynthesisProvider = nil // explicitly nil — synthesis unavailable

	buf := &bytes.Buffer{}
	cmd := review.NewCommand(deps)
	cmd.SetOut(buf)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No synthesis output expected.
	if strings.Contains(buf.String(), "synthesis") {
		t.Errorf("no synthesis output expected when provider is nil, got: %s", buf.String())
	}
}

// TestDispatchFork_SingleAgentNoSynthesis verifies that the single-agent path
// never invokes synthesis (synthesis is multi-agent only). We set a provider
// but use a single launchable agent; the command should complete without
// calling the synthesis provider.
func TestDispatchFork_SingleAgentNoSynthesis(t *testing.T) {
	setupCmdTestRepo(t)
	installHooksForCmdTest(t, "cursor")

	if err := review.SaveReviewConfig(context.Background(), map[string]settings.ReviewConfig{
		"cursor": {Prompt: "review"},
	}); err != nil {
		t.Fatal(err)
	}

	provider := &stubCmdSynthesisProvider{}

	// cursor is installed but not launchable (ReviewerFor returns nil).
	installed := []types.AgentName{"cursor"}
	deps := newDispatchTestDeps(t, installed, nil /* no launchable */, nil, nil)
	deps.SynthesisProvider = provider

	buf := &bytes.Buffer{}
	cmd := review.NewCommand(deps)
	cmd.SetOut(buf)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if provider.called {
		t.Error("synthesis provider should NOT be called on single-agent path")
	}
}
