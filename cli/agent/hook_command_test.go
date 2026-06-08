package agent

import (
	"strings"
	"testing"
)

func TestWrapProductionJSONWarningHookCommand(t *testing.T) {
	t.Parallel()

	command := WrapProductionJSONWarningHookCommand("trace hooks claude-code session-start", WarningFormatMultiLine)

	if command == "trace hooks claude-code session-start" {
		t.Fatal("expected wrapped command, got raw command")
	}
	if strings.Contains(command, `>&2`) {
		t.Fatalf("claude wrapper should not print warning to stderr, got %q", command)
	}
	if want := `systemMessage`; !strings.Contains(command, want) {
		t.Fatalf("claude wrapper missing systemMessage JSON, got %q", command)
	}
	if !strings.Contains(command, "Trace CLI") {
		t.Fatalf("claude wrapper missing warning text, got %q", command)
	}
	if want := "exec trace hooks claude-code session-start"; !strings.Contains(command, want) {
		t.Fatalf("claude wrapper missing exec target, got %q", command)
	}
}

func TestWrapProductionPlainTextWarningHookCommand(t *testing.T) {
	t.Parallel()

	command := WrapProductionPlainTextWarningHookCommand("trace hooks factoryai-droid session-start", WarningFormatSingleLine)

	if command == "trace hooks factoryai-droid session-start" {
		t.Fatal("expected wrapped command, got raw command")
	}
	if strings.Contains(command, `>&2`) {
		t.Fatalf("plain text wrapper should not print warning to stderr, got %q", command)
	}
	if !strings.Contains(command, "Trace CLI is enabled but not installed") {
		t.Fatalf("plain text wrapper missing warning text, got %q", command)
	}
	if want := "exec trace hooks factoryai-droid session-start"; !strings.Contains(command, want) {
		t.Fatalf("plain text wrapper missing exec target, got %q", command)
	}
}

func TestMissingTraceWarning(t *testing.T) {
	t.Parallel()

	if got := MissingTraceWarning(WarningFormatSingleLine); strings.Contains(got, "\n") {
		t.Fatalf("single-line warning should not contain newlines, got %q", got)
	}
	if got := MissingTraceWarning(WarningFormatMultiLine); !strings.Contains(got, "\n") {
		t.Fatalf("multiline warning should contain newlines, got %q", got)
	}
}

func TestIsManagedHookCommand_DirectPrefix(t *testing.T) {
	t.Parallel()

	prefixes := []string{"trace ", `go run "$(git rev-parse --show-toplevel)"/cmd/trace/main.go `}

	if !IsManagedHookCommand("trace hooks codex stop", prefixes) {
		t.Fatal("expected direct trace command to match")
	}
	if !IsManagedHookCommand(`go run "$(git rev-parse --show-toplevel)"/cmd/trace/main.go hooks codex stop`, prefixes) {
		t.Fatal("expected local-dev command to match")
	}
}

func TestIsManagedHookCommand_WrappedPrefix(t *testing.T) {
	t.Parallel()

	prefixes := []string{"trace "}

	if !IsManagedHookCommand(
		WrapProductionSilentHookCommand("trace hooks cursor stop"),
		prefixes,
	) {
		t.Fatal("expected wrapped silent command to match")
	}
	if !IsManagedHookCommand(
		WrapProductionJSONWarningHookCommand("trace hooks claude-code session-start", WarningFormatSingleLine),
		prefixes,
	) {
		t.Fatal("expected wrapped json warning command to match")
	}
	if !IsManagedHookCommand(
		WrapProductionPlainTextWarningHookCommand("trace hooks factoryai-droid stop", WarningFormatSingleLine),
		prefixes,
	) {
		t.Fatal("expected wrapped plain text warning command to match")
	}
}

func TestIsManagedHookCommand_DoesNotMatchSubstring(t *testing.T) {
	t.Parallel()

	prefixes := []string{"trace ", `go run "$(git rev-parse --show-toplevel)"/cmd/trace/main.go `}

	if IsManagedHookCommand(`echo "the trace workflow finished"`, prefixes) {
		t.Fatal("unexpected match for unrelated substring command")
	}
	if IsManagedHookCommand(`sh -c 'echo "the trace workflow finished"; exit 0'`, prefixes) {
		t.Fatal("unexpected match for unrelated wrapped shell command")
	}
	if IsManagedHookCommand(`sh -c 'if ! command -v trace >/dev/null 2>&1; then exit 0; fi; exec echo "the trace workflow finished"'`, prefixes) {
		t.Fatal("unexpected match for wrapper that does not exec an Trace hook")
	}
}
