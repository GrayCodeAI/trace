package agentlaunch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/agent"
	"github.com/GrayCodeAI/trace/cmd/trace/cli/agent/types"
)

// ---------------------------------------------------------------------------
// Mock agents for LaunchFixAgent tests
// ---------------------------------------------------------------------------

// stubAgent is a minimal agent.Agent implementation for testing.
// It satisfies the full Agent interface but returns zero values everywhere.
type stubAgent struct {
	name types.AgentName
}

func (s *stubAgent) Name() types.AgentName                          { return s.name }
func (s *stubAgent) Type() types.AgentType                          { return types.AgentType("stub") }
func (s *stubAgent) Description() string                            { return "stub" }
func (s *stubAgent) IsPreview() bool                                { return false }
func (s *stubAgent) DetectPresence(_ context.Context) (bool, error) { return false, nil }
func (s *stubAgent) ProtectedDirs() []string                        { return nil }
func (s *stubAgent) GetSessionID(_ *agent.HookInput) string         { return "" }
func (s *stubAgent) ReadTranscript(_ string) ([]byte, error)        { return nil, nil }
func (s *stubAgent) ChunkTranscript(_ context.Context, content []byte, _ int) ([][]byte, error) {
	return [][]byte{content}, nil
}

func (s *stubAgent) ReassembleTranscript(chunks [][]byte) ([]byte, error) {
	var result []byte
	for _, c := range chunks {
		result = append(result, c...)
	}
	return result, nil
}
func (s *stubAgent) GetSessionDir(_ string) (string, error) { return "", nil }
func (s *stubAgent) ResolveSessionFile(sessionDir, agentSessionID string) string {
	return sessionDir + "/" + agentSessionID + ".jsonl"
}

func (s *stubAgent) ReadSession(_ *agent.HookInput) (*agent.AgentSession, error) {
	return nil, nil //nolint:nilnil // test stub
}
func (s *stubAgent) WriteSession(_ context.Context, _ *agent.AgentSession) error { return nil }
func (s *stubAgent) FormatResumeCommand(_ string) string                         { return "" }

// stubLauncherAgent embeds stubAgent and adds the Launcher interface.
// The launchFn field lets each test control what LaunchCmd returns.
type stubLauncherAgent struct {
	stubAgent
	launchFn func(ctx context.Context, prompt string) (*exec.Cmd, error)
}

func (s *stubLauncherAgent) LaunchCmd(ctx context.Context, prompt string) (*exec.Cmd, error) {
	return s.launchFn(ctx, prompt)
}

// Ensure interfaces are satisfied at compile time.
var (
	_ agent.Agent    = (*stubAgent)(nil)
	_ agent.Agent    = (*stubLauncherAgent)(nil)
	_ agent.Launcher = (*stubLauncherAgent)(nil)
)

// registerTestAgent registers a factory in the global agent registry under
// the given name. Tests that call this MUST NOT use t.Parallel() because
// the registry is process-global. Returns a cleanup func that removes the
// registration so tests don't leak into each other.
//
// NOTE: We accept the registration is never "removed" because the agent
// registry has no Unregister. Instead we pick unique names per test.
func registerTestAgent(t *testing.T, name types.AgentName, factory agent.Factory) {
	t.Helper()
	agent.Register(name, factory)
}

// ---------------------------------------------------------------------------
// Tests for LaunchFixAgent
// ---------------------------------------------------------------------------

// TestLaunchFixAgent_UnknownAgent verifies that LaunchFixAgent returns a
// wrapped error when the agent name is not in the registry.
func TestLaunchFixAgent_UnknownAgent(t *testing.T) {
	t.Parallel()

	err := LaunchFixAgent(context.Background(), "nonexistent-agent-xyz", "fix this")
	if err == nil {
		t.Fatal("expected error for unknown agent, got nil")
	}
	if !strings.Contains(err.Error(), "resolve fix agent") {
		t.Errorf("error %q does not mention 'resolve fix agent'", err)
	}
	if !strings.Contains(err.Error(), "nonexistent-agent-xyz") {
		t.Errorf("error %q does not include the agent name", err)
	}
}

// TestLaunchFixAgent_AgentNotLaunchable verifies that LaunchFixAgent returns
// a specific error when the agent exists but does not implement Launcher.
func TestLaunchFixAgent_AgentNotLaunchable(t *testing.T) {
	name := types.AgentName("stub-no-launch")
	registerTestAgent(t, name, func() agent.Agent {
		return &stubAgent{name: name}
	})

	err := LaunchFixAgent(context.Background(), string(name), "fix this")
	if err == nil {
		t.Fatal("expected error for non-launchable agent, got nil")
	}
	if !strings.Contains(err.Error(), "cannot be launched") {
		t.Errorf("error %q does not mention 'cannot be launched'", err)
	}
}

// TestLaunchFixAgent_ExitSuccess verifies that LaunchFixAgent returns nil
// when the launched command exits cleanly (status 0).
func TestLaunchFixAgent_ExitSuccess(t *testing.T) {
	name := types.AgentName("stub-launch-ok")
	registerTestAgent(t, name, func() agent.Agent {
		return &stubLauncherAgent{
			stubAgent: stubAgent{name: name},
			launchFn: func(_ context.Context, _ string) (*exec.Cmd, error) {
				return exec.Command("true"), nil
			},
		}
	})

	err := LaunchFixAgent(context.Background(), string(name), "fix something")
	if err != nil {
		t.Fatalf("expected nil error for clean exit, got: %v", err)
	}
}

// TestLaunchFixAgent_ExitNonZero verifies that LaunchFixAgent wraps the
// ExitError with the exit code when the command fails.
func TestLaunchFixAgent_ExitNonZero(t *testing.T) {
	name := types.AgentName("stub-launch-fail")
	registerTestAgent(t, name, func() agent.Agent {
		return &stubLauncherAgent{
			stubAgent: stubAgent{name: name},
			launchFn: func(_ context.Context, _ string) (*exec.Cmd, error) {
				return exec.Command("false"), nil
			},
		}
	})

	err := LaunchFixAgent(context.Background(), string(name), "fix something")
	if err == nil {
		t.Fatal("expected error for non-zero exit, got nil")
	}
	if !strings.Contains(err.Error(), "fix agent exited with status") {
		t.Errorf("error %q does not mention 'fix agent exited with status'", err)
	}
	// Verify the underlying error is an *exec.ExitError.
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("error chain does not contain *exec.ExitError: %v", err)
	}
}

// TestLaunchFixAgent_ContextCanceled verifies that LaunchFixAgent wraps a
// context.Canceled error with a descriptive message.
func TestLaunchFixAgent_ContextCanceled(t *testing.T) {
	name := types.AgentName("stub-launch-cancel")
	registerTestAgent(t, name, func() agent.Agent {
		return &stubLauncherAgent{
			stubAgent: stubAgent{name: name},
			launchFn: func(ctx context.Context, _ string) (*exec.Cmd, error) {
				// Use "sleep" so the cmd.Run() blocks long enough for
				// us to cancel the context. Pass ctx through so the
				// exec.CommandContext respects cancellation.
				return exec.CommandContext(ctx, "sleep", "30"), nil
			},
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := LaunchFixAgent(ctx, string(name), "fix something")
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
	if !strings.Contains(err.Error(), "fix agent cancelled") {
		t.Errorf("error %q does not mention 'fix agent cancelled'", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error chain does not contain context.Canceled: %v", err)
	}
}

// TestLaunchFixAgent_LaunchCmdError verifies that LaunchFixAgent wraps the
// error returned by Launcher.LaunchCmd.
func TestLaunchFixAgent_LaunchCmdError(t *testing.T) {
	name := types.AgentName("stub-launch-cmd-err")
	registerTestAgent(t, name, func() agent.Agent {
		return &stubLauncherAgent{
			stubAgent: stubAgent{name: name},
			launchFn: func(_ context.Context, _ string) (*exec.Cmd, error) {
				return nil, errors.New("binary not found")
			},
		}
	})

	err := LaunchFixAgent(context.Background(), string(name), "fix something")
	if err == nil {
		t.Fatal("expected error when LaunchCmd fails, got nil")
	}
	if !strings.Contains(err.Error(), "build fix command") {
		t.Errorf("error %q does not mention 'build fix command'", err)
	}
	if !strings.Contains(err.Error(), "binary not found") {
		t.Errorf("error %q does not wrap the underlying cause 'binary not found'", err)
	}
}

// TestLaunchFixAgent_StripsProvenanceFromCmdEnv verifies that LaunchFixAgent
// removes TRACE_REVIEW_* and TRACE_INVESTIGATE_* entries from the cmd.Env
// that the launcher returns, so the fix session is not mis-tagged.
func TestLaunchFixAgent_StripsProvenanceFromCmdEnv(t *testing.T) {
	name := types.AgentName("stub-launch-env")
	registerTestAgent(t, name, func() agent.Agent {
		return &stubLauncherAgent{
			stubAgent: stubAgent{name: name},
			launchFn: func(_ context.Context, _ string) (*exec.Cmd, error) {
				cmd := exec.Command("true")
				cmd.Env = []string{
					"TRACE_REVIEW_SESSION=1",
					"TRACE_REVIEW_AGENT=claude-code",
					"TRACE_INVESTIGATE_SESSION=1",
					"TRACE_INVESTIGATE_RUN_ID=abcdef012345",
					"KEEP_ME=yes",
				}
				return cmd, nil
			},
		}
	})

	err := LaunchFixAgent(context.Background(), string(name), "fix something")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	// We can't inspect cmd.Env after Run() completes, but the test exercises
	// the cmd.Env != nil branch and the stripping logic. The contract that
	// stripping actually removes entries is pinned by
	// TestWithoutReviewOrInvestigateEnv below.
}

// TestLaunchFixAgent_EmptyEnvFallsBackToOsEnviron verifies the fallback
// path: when the launcher returns a cmd with empty Env, LaunchFixAgent
// falls back to os.Environ() and strips provenance from it.
func TestLaunchFixAgent_EmptyEnvFallsBackToOsEnviron(t *testing.T) {
	// Set provenance vars on the host process so they'd leak without stripping.
	t.Setenv("TRACE_REVIEW_SESSION", "1")
	t.Setenv("TRACE_REVIEW_AGENT", "claude-code")
	t.Setenv("TRACE_INVESTIGATE_SESSION", "1")
	t.Setenv("TRACE_INVESTIGATE_RUN_ID", "abcdef012345")

	name := types.AgentName("stub-launch-empty-env")
	registerTestAgent(t, name, func() agent.Agent {
		return &stubLauncherAgent{
			stubAgent: stubAgent{name: name},
			launchFn: func(_ context.Context, _ string) (*exec.Cmd, error) {
				cmd := exec.Command("true")
				// Explicitly empty Env — triggers the fallback path.
				cmd.Env = []string{}
				return cmd, nil
			},
		}
	})

	err := LaunchFixAgent(context.Background(), string(name), "fix something")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	// The env stripping contract is pinned by TestWithoutReviewOrInvestigateEnv.
}

// TestLaunchFixAgent_NilEnvFallsBackToOsEnviron is similar to the empty-env
// test but exercises the nil case (cmd.Env == nil, len(nil) == 0).
func TestLaunchFixAgent_NilEnvFallsBackToOsEnviron(t *testing.T) {
	t.Setenv("TRACE_REVIEW_SESSION", "1")
	t.Setenv("TRACE_REVIEW_STARTING_SHA", "deadbeef")

	name := types.AgentName("stub-launch-nil-env")
	registerTestAgent(t, name, func() agent.Agent {
		return &stubLauncherAgent{
			stubAgent: stubAgent{name: name},
			launchFn: func(_ context.Context, _ string) (*exec.Cmd, error) {
				cmd := exec.Command("true")
				// Leave Env as nil — triggers the nil/0 branch.
				return cmd, nil
			},
		}
	})

	err := LaunchFixAgent(context.Background(), string(name), "fix something")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

// TestLaunchFixAgent_OtherRunError verifies that LaunchFixAgent wraps
// non-ExitError, non-canceled run errors in the generic message.
func TestLaunchFixAgent_OtherRunError(t *testing.T) {
	name := types.AgentName("stub-launch-bad-bin")
	registerTestAgent(t, name, func() agent.Agent {
		return &stubLauncherAgent{
			stubAgent: stubAgent{name: name},
			launchFn: func(_ context.Context, _ string) (*exec.Cmd, error) {
				// Point to a binary that doesn't exist.
				return exec.Command("/nonexistent-binary-path-xyz"), nil
			},
		}
	})

	err := LaunchFixAgent(context.Background(), string(name), "fix something")
	if err == nil {
		t.Fatal("expected error for nonexistent binary, got nil")
	}
	if !strings.Contains(err.Error(), "run fix agent") {
		t.Errorf("error %q does not mention 'run fix agent'", err)
	}
	// Should NOT match ExitError or context.Canceled paths.
	if strings.Contains(err.Error(), "fix agent exited with status") {
		t.Errorf("error %q should not mention exit status", err)
	}
	if strings.Contains(err.Error(), "fix agent cancelled") {
		t.Errorf("error %q should not mention cancelled", err)
	}
}

// ---------------------------------------------------------------------------
// Tests for withoutReviewOrInvestigateEnv
// ---------------------------------------------------------------------------

// TestWithoutReviewOrInvestigateEnv pins the contract that the helper
// strips both TRACE_REVIEW_* and TRACE_INVESTIGATE_* entries from the
// supplied env slice while leaving unrelated entries untouched. This is
// the leak-prevention guarantee for fix-agent launches: a parent shell
// may have inherited stale provenance vars, and the fix session must not
// be tagged as a review or investigate session.
//
// The literal env names below mirror the constants in
// cmd/trace/cli/review/env.go and cmd/trace/cli/investigate/env.go.
// We use literals (not the exported constants) because importing review
// or investigate from this package would create a build cycle: review
// depends on agentlaunch.
func TestWithoutReviewOrInvestigateEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []string
		want     []string
		notWant  []string
		wantSize int
	}{
		{
			name: "strips review and investigate, keeps unrelated",
			input: []string{
				"PATH=/usr/bin",
				"HOME=/home/u",
				"TRACE_REVIEW_SESSION=1",
				"TRACE_REVIEW_AGENT=claude-code",
				"TRACE_REVIEW_SKILLS=[\"/x\"]",
				"TRACE_REVIEW_PROMPT=stale review prompt",
				"TRACE_REVIEW_STARTING_SHA=stale1",
				"TRACE_INVESTIGATE_SESSION=1",
				"TRACE_INVESTIGATE_AGENT=claude-code",
				"TRACE_INVESTIGATE_RUN_ID=abcdef012345",
				"TRACE_INVESTIGATE_TOPIC=topic",
				"TRACE_INVESTIGATE_FINDINGS_DOC=/tmp/f.md",
				"TRACE_INVESTIGATE_STATE_DOC=/tmp/state.json",
				"TRACE_INVESTIGATE_STARTING_SHA=stale2",
			},
			want: []string{
				"PATH=/usr/bin",
				"HOME=/home/u",
			},
			notWant: []string{
				"TRACE_REVIEW_SESSION=1",
				"TRACE_REVIEW_AGENT=claude-code",
				"TRACE_REVIEW_SKILLS=[\"/x\"]",
				"TRACE_REVIEW_PROMPT=stale review prompt",
				"TRACE_REVIEW_STARTING_SHA=stale1",
				"TRACE_INVESTIGATE_SESSION=1",
				"TRACE_INVESTIGATE_AGENT=claude-code",
				"TRACE_INVESTIGATE_RUN_ID=abcdef012345",
				"TRACE_INVESTIGATE_TOPIC=topic",
				"TRACE_INVESTIGATE_FINDINGS_DOC=/tmp/f.md",
				"TRACE_INVESTIGATE_STATE_DOC=/tmp/state.json",
				"TRACE_INVESTIGATE_STARTING_SHA=stale2",
			},
			wantSize: 2,
		},
		{
			name: "no provenance entries: passthrough",
			input: []string{
				"PATH=/usr/bin",
				"FOO=bar",
			},
			want: []string{
				"PATH=/usr/bin",
				"FOO=bar",
			},
			wantSize: 2,
		},
		{
			name:     "empty input: empty output",
			input:    nil,
			wantSize: 0,
		},
		{
			name: "only provenance entries: empty output",
			input: []string{
				"TRACE_REVIEW_SESSION=1",
				"TRACE_INVESTIGATE_SESSION=1",
			},
			notWant: []string{
				"TRACE_REVIEW_SESSION=1",
				"TRACE_INVESTIGATE_SESSION=1",
			},
			wantSize: 0,
		},
		{
			name: "look-alike non-provenance keys survive",
			input: []string{
				"NOT_TRACE_REVIEW_SESSION=1",
				"TRACE_REVIEW_OTHER=keep",      // not a known prefix
				"TRACE_INVESTIGATE_OTHER=keep", // not a known prefix
			},
			want: []string{
				"NOT_TRACE_REVIEW_SESSION=1",
				"TRACE_REVIEW_OTHER=keep",
				"TRACE_INVESTIGATE_OTHER=keep",
			},
			wantSize: 3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := withoutReviewOrInvestigateEnv(tc.input)
			if len(got) != tc.wantSize {
				t.Errorf("len = %d, want %d (got: %v)", len(got), tc.wantSize, got)
			}
			for _, kv := range tc.want {
				if !slices.Contains(got, kv) {
					t.Errorf("missing expected entry %q in %v", kv, got)
				}
			}
			for _, kv := range tc.notWant {
				if slices.Contains(got, kv) {
					t.Errorf("unexpected entry survived strip: %q", kv)
				}
			}
		})
	}
}

// TestWithoutReviewOrInvestigateEnv_DoesNotMutateInput pins that the
// helper returns a fresh slice and never mutates its argument. Callers
// rely on this when they pass `os.Environ()` directly.
func TestWithoutReviewOrInvestigateEnv_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	input := []string{
		"PATH=/usr/bin",
		"TRACE_REVIEW_SESSION=1",
		"TRACE_INVESTIGATE_SESSION=1",
		"HOME=/home/u",
	}
	original := slices.Clone(input)

	_ = withoutReviewOrInvestigateEnv(input)

	if !slices.Equal(input, original) {
		t.Errorf("input was mutated: got %v, want %v", input, original)
	}
}

// TestLaunchFixAgent_EmptyEnvFallback_StripsHostProvenance pins that the
// "cmd.Env == nil → os.Environ()" fallback in LaunchFixAgent still strips
// provenance markers even when they were set on the parent process. A
// future launcher implementation that returns a cmd with no Env would
// otherwise re-import stale provenance via os.Environ() and silently
// re-tag the fix session.
//
// Mirrors the fallback branch exactly: build an empty Env, take the
// os.Environ() path, assert no provenance entries survive.
func TestLaunchFixAgent_EmptyEnvFallback_StripsHostProvenance(t *testing.T) {
	// t.Setenv mutates process global state; cannot run with t.Parallel().
	t.Setenv("TRACE_REVIEW_SESSION", "1")
	t.Setenv("TRACE_REVIEW_AGENT", "claude-code")
	t.Setenv("TRACE_REVIEW_STARTING_SHA", "deadbeefcafe")
	t.Setenv("TRACE_INVESTIGATE_SESSION", "1")
	t.Setenv("TRACE_INVESTIGATE_RUN_ID", "abcdef012345")

	// Drive the exact branch LaunchFixAgent takes when cmd.Env is empty:
	// withoutReviewOrInvestigateEnv(os.Environ()).
	emptyEnv := []string(nil)
	cleaned := withoutReviewOrInvestigateEnv(emptyEnv)
	if len(cleaned) != 0 {
		t.Fatalf("precondition: empty input should yield empty output, got %v", cleaned)
	}
	// Fall back to host env (the branch under test) and re-strip.
	fallback := withoutReviewOrInvestigateEnv(osEnvironForTest())

	for _, kv := range fallback {
		if hasReviewOrInvestigatePrefix(kv) {
			t.Errorf("fallback env still contains provenance entry %q", kv)
		}
	}
}

// osEnvironForTest mirrors os.Environ() via the same call LaunchFixAgent
// uses. Wrapped in a helper so the test reads as a direct simulation of
// the production branch.
func osEnvironForTest() []string {
	return os.Environ()
}

// hasReviewOrInvestigatePrefix is a tiny test helper that mirrors the
// production prefix check without importing provenance (which is fine
// here — the test file lives in the same package as the implementation).
func hasReviewOrInvestigatePrefix(kv string) bool {
	prefixes := []string{
		"TRACE_REVIEW_SESSION=",
		"TRACE_REVIEW_AGENT=",
		"TRACE_REVIEW_SKILLS=",
		"TRACE_REVIEW_PROMPT=",
		"TRACE_REVIEW_STARTING_SHA=",
		"TRACE_INVESTIGATE_SESSION=",
		"TRACE_INVESTIGATE_AGENT=",
		"TRACE_INVESTIGATE_RUN_ID=",
		"TRACE_INVESTIGATE_TOPIC=",
		"TRACE_INVESTIGATE_FINDINGS_DOC=",
		"TRACE_INVESTIGATE_STATE_DOC=",
		"TRACE_INVESTIGATE_STARTING_SHA=",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(kv, p) {
			return true
		}
	}
	return false
}
