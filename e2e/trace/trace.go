package trace

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/execx"
)

// BinPath returns the path to the trace binary from E2E_TRACE_BIN.
// The mise test:e2e tasks set this automatically via `mise run build`.
func BinPath() string {
	p := os.Getenv("E2E_TRACE_BIN")
	if p == "" {
		log.Fatal("trace: E2E_TRACE_BIN not set — run tests via `mise run test:e2e`")
	}
	return p
}

// RewindPoint represents a single entry from `trace rewind --list`.
type RewindPoint struct {
	ID               string `json:"id"`
	Message          string `json:"message"`
	MetadataDir      string `json:"metadata_dir"`
	Date             string `json:"date"`
	IsTaskCheckpoint bool   `json:"is_task_checkpoint"`
	ToolUseID        string `json:"tool_use_id"`
	IsLogsOnly       bool   `json:"is_logs_only"`
	CondensationID   string `json:"condensation_id"`
	SessionID        string `json:"session_id"`
}

// Enable runs `trace enable` for the given agent with telemetry disabled.
func Enable(t *testing.T, dir, agent string) {
	t.Helper()
	run(t, dir, "enable", "--agent", agent, "--telemetry=false")
}

// Disable runs `trace disable` in the given directory.
func Disable(t *testing.T, dir string) {
	t.Helper()
	run(t, dir, "disable")
}

// Doctor runs `trace doctor --force` and returns the output.
func Doctor(t *testing.T, dir string) string {
	t.Helper()
	return run(t, dir, "doctor", "--force")
}

// CleanDryRun runs `trace clean --dry-run` and returns the output.
func CleanDryRun(t *testing.T, dir string) string {
	t.Helper()
	return run(t, dir, "clean", "--dry-run")
}

// CleanForce runs `trace clean --force` and returns the output.
func CleanForce(t *testing.T, dir string) string {
	t.Helper()
	return run(t, dir, "clean", "--force")
}

// RewindList runs `trace rewind --list` and parses the JSON output.
func RewindList(t *testing.T, dir string) []RewindPoint {
	t.Helper()
	out := run(t, dir, "checkpoint", "rewind", "--list")

	var points []RewindPoint
	if err := json.Unmarshal([]byte(out), &points); err != nil {
		t.Fatalf("parse rewind list: %v\nraw output: %s", err, out)
	}
	return points
}

// Rewind runs `trace rewind --to <id>`. Returns an error instead of
// failing the test, since callers may test failure cases.
func Rewind(t *testing.T, dir, id string) error {
	t.Helper()
	return runErr(dir, "checkpoint", "rewind", "--to", id)
}

// RewindLogsOnly runs `trace rewind --to <id> --logs-only`.
func RewindLogsOnly(t *testing.T, dir, id string) error {
	t.Helper()
	return runErr(dir, "checkpoint", "rewind", "--to", id, "--logs-only")
}

// run executes a `trace` subcommand in dir and fails the test on error.
func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := execx.NonInteractive(context.Background(), BinPath(), args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("trace %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// runErr executes a `trace` subcommand in dir and returns any error.
func runErr(dir string, args ...string) error {
	cmd := execx.NonInteractive(context.Background(), BinPath(), args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()

	out, err := cmd.CombinedOutput()
	if err != nil {
		return &ExecError{
			Args:   args,
			Err:    err,
			Output: string(out),
		}
	}
	return nil
}

// ExecError wraps an trace CLI execution failure with its output.
type ExecError struct {
	Args   []string
	Err    error
	Output string
}

func (e *ExecError) Error() string {
	return "trace " + strings.Join(e.Args, " ") + ": " + e.Err.Error() + "\n" + e.Output
}

func (e *ExecError) Unwrap() error {
	return e.Err
}

// Explain runs `trace explain --checkpoint <id>` and returns the output.
func Explain(t *testing.T, dir, checkpointID string) string {
	t.Helper()
	return run(t, dir, "checkpoint", "explain", "--checkpoint", checkpointID)
}

// ExplainGenerate runs `trace explain --checkpoint <id> --generate`.
// Returns (output, error) — doesn't fail test since callers may test failure cases.
func ExplainGenerate(dir, checkpointID string) (string, error) {
	return runOutput(dir, "checkpoint", "explain", "--checkpoint", checkpointID, "--generate")
}

// ExplainCommit runs `trace explain --commit <ref>`.
// Returns (output, error) — for testing failure cases.
func ExplainCommit(dir, ref string) (string, error) {
	return runOutput(dir, "checkpoint", "explain", "--commit", ref)
}

// AttachWithEnv runs `trace attach <session-id> --agent <agent> --force`
// with extra env vars.
func AttachWithEnv(dir string, extraEnv []string, sessionID, agent string) (string, error) {
	return runOutputEnv(dir, extraEnv, "session", "attach", sessionID, "--agent", agent, "--force")
}

// Resume runs `trace resume <branch> --force` and returns the output.
func Resume(dir, branch string) (string, error) {
	return runOutput(dir, "session", "resume", branch, "--force")
}

// ResumeWithEnv runs `trace resume <branch> --force` with extra env vars.
func ResumeWithEnv(dir, branch string, extraEnv []string) (string, error) {
	return runOutputEnv(dir, extraEnv, "session", "resume", branch, "--force")
}

// runOutput executes a `trace` subcommand and returns (output, error).
func runOutput(dir string, args ...string) (string, error) {
	return runOutputEnv(dir, nil, args...)
}

func runOutputEnv(dir string, extraEnv []string, args ...string) (string, error) {
	cmd := execx.NonInteractive(context.Background(), BinPath(), args...)
	cmd.Dir = dir
	cmd.Env = append(append([]string{}, os.Environ()...), extraEnv...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), &ExecError{
			Args:   args,
			Err:    err,
			Output: string(out),
		}
	}
	return strings.TrimSpace(string(out)), nil
}
