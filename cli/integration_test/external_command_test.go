//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/execx"
	"github.com/GrayCodeAI/trace/cli/testutil"
)

// Integration tests for external-command resolution in cmd/trace/main.go.
// They build and exec the real binary so the pre-Cobra routing (exit-code
// propagation, stdio passthrough, signal handling) is exercised end-to-end
// — unit tests in cli/plugin_test.go can't.

// writePluginScript writes a shell script that records argv and exits
// with exitCode. Skips the calling test on Windows.
func writePluginScript(t *testing.T, dir, binaryName, argFile string, exitCode int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("plugin shell-script harness only runs on Unix")
	}
	path := filepath.Join(dir, binaryName)
	body := fmt.Sprintf(
		"#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\n"+
			"echo \"plugin stdout\"\n"+
			"echo \"plugin stderr\" 1>&2\n"+
			"exit %d\n",
		argFile, exitCode,
	)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write plugin %s: %v", path, err)
	}
	return path
}

// pathWith returns os.Environ with dir prepended to PATH. Returning a
// fresh env slice (rather than t.Setenv) keeps tests parallel-safe.
func pathWith(dir string) []string {
	env := os.Environ()
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = "PATH=" + dir + string(os.PathListSeparator) + strings.TrimPrefix(kv, "PATH=")
			return env
		}
	}
	return append(env, "PATH="+dir)
}

func TestExternalCommand_HappyPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	argFile := filepath.Join(dir, "argv.txt")
	writePluginScript(t, dir, "trace-pgr", argFile, 0)

	cmd := execx.NonInteractive(context.Background(), getTestBinary(), "pgr", "hello", "--flag", "value")
	cmd.Env = pathWith(dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("trace pgr failed: %v\nstderr: %s", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "plugin stdout" {
		t.Errorf("stdout = %q, want %q", got, "plugin stdout")
	}
	if got := strings.TrimSpace(stderr.String()); got != "plugin stderr" {
		t.Errorf("stderr = %q, want %q", got, "plugin stderr")
	}
	argsBytes, err := os.ReadFile(argFile)
	if err != nil {
		t.Fatalf("read argv file: %v", err)
	}
	if got := strings.TrimSpace(string(argsBytes)); got != "hello\n--flag\nvalue" {
		t.Errorf("plugin argv = %q, want %q", got, "hello\n--flag\nvalue")
	}
}

func TestExternalCommand_ExitCodePropagation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writePluginScript(t, dir, "trace-failing", filepath.Join(dir, "argv.txt"), 42)

	cmd := execx.NonInteractive(context.Background(), getTestBinary(), "failing")
	cmd.Env = pathWith(dir)
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	if got := exitErr.ExitCode(); got != 42 {
		t.Errorf("exit code = %d, want 42", got)
	}
}

func TestExternalCommand_BuiltinWins(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// If the shadowing plugin ran, the parent's exit code would be 99
	// (writePluginScript bakes that in via the requested code).
	writePluginScript(t, dir, "trace-version", filepath.Join(dir, "argv.txt"), 99)

	cmd := execx.NonInteractive(context.Background(), getTestBinary(), "version")
	cmd.Env = pathWith(dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("trace version failed: %v\nstderr: %s", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "argv.txt")); err == nil {
		t.Errorf("trace-version plugin was invoked but built-in must take precedence\nstdout: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Trace CLI") {
		t.Errorf("expected built-in version output, got: %s", stdout.String())
	}
}

func TestExternalCommand_PluginNotFound(t *testing.T) {
	t.Parallel()
	// PATH deliberately points at an empty dir so no plugin can resolve.
	dir := t.TempDir()

	cmd := execx.NonInteractive(context.Background(), getTestBinary(), "definitely-not-a-real-plugin-or-builtin")
	cmd.Env = pathWith(dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected failure for unknown command")
	}
	// Cobra's normal error path should fire — the dispatcher must not have
	// swallowed the invocation.
	if !strings.Contains(stderr.String(), "unknown command") &&
		!strings.Contains(stderr.String(), "Invalid usage") {
		t.Errorf("expected Cobra unknown-command error, got stderr: %s", stderr.String())
	}
}

func TestExternalCommand_FlagAfterPluginNameNotEatenByCobra(t *testing.T) {
	t.Parallel()
	// Once we're routing to a plugin, flag-shaped args must reach the
	// child verbatim — Cobra's --help/--version handlers must not see them.
	dir := t.TempDir()
	argFile := filepath.Join(dir, "argv.txt")
	writePluginScript(t, dir, "trace-passthrough", argFile, 0)

	cmd := execx.NonInteractive(context.Background(), getTestBinary(), "passthrough", "--help", "--version", "subcmd")
	cmd.Env = pathWith(dir)
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		t.Fatalf("trace passthrough failed: %v", err)
	}

	argsBytes, err := os.ReadFile(argFile)
	if err != nil {
		t.Fatalf("read argv file: %v", err)
	}
	want := "--help\n--version\nsubcmd"
	if got := strings.TrimSpace(string(argsBytes)); got != want {
		t.Errorf("plugin argv = %q, want %q (Cobra ate flags)", got, want)
	}
}

func TestExternalCommand_StdinPassthrough(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("plugin shell-script harness only runs on Unix")
	}
	dir := t.TempDir()
	outFile := filepath.Join(dir, "stdin.txt")
	body := fmt.Sprintf("#!/bin/sh\ncat > %q\nexit 0\n", outFile)
	if err := os.WriteFile(filepath.Join(dir, "trace-stdincat"), []byte(body), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write plugin: %v", err)
	}

	cmd := execx.NonInteractive(context.Background(), getTestBinary(), "stdincat")
	cmd.Env = pathWith(dir)
	cmd.Stdin = strings.NewReader("hello from parent stdin\n")
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		t.Fatalf("trace stdincat failed: %v", err)
	}

	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read stdin file: %v", err)
	}
	if want := "hello from parent stdin\n"; string(got) != want {
		t.Errorf("plugin stdin = %q, want %q", string(got), want)
	}
}

func TestExternalCommand_EnvVarsForwarded(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("plugin shell-script harness only runs on Unix")
	}
	// Spawn the parent CLI from inside a real git repo so it can resolve
	// the repo root and forward TRACE_REPO_ROOT. testutil.InitRepo
	// configures user.name/email and disables GPG signing.
	repoDir := t.TempDir()
	resolvedRepo, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	testutil.InitRepo(t, resolvedRepo)

	pluginDir := t.TempDir()
	envFile := filepath.Join(pluginDir, "env.txt")
	body := fmt.Sprintf(
		"#!/bin/sh\n{\n"+
			"  echo \"TRACE_CLI_VERSION=$TRACE_CLI_VERSION\"\n"+
			"  echo \"TRACE_REPO_ROOT=$TRACE_REPO_ROOT\"\n"+
			"  echo \"TRACE_PLUGIN_DATA_DIR=$TRACE_PLUGIN_DATA_DIR\"\n"+
			"} > %q\nexit 0\n",
		envFile,
	)
	if err := os.WriteFile(filepath.Join(pluginDir, "trace-envcheck"), []byte(body), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write plugin: %v", err)
	}

	// Pin the plugin parent dir so we can assert the per-plugin data path.
	pluginRoot := t.TempDir()
	cmd := execx.NonInteractive(context.Background(), getTestBinary(), "envcheck")
	cmd.Env = append(pathWith(pluginDir), "TRACE_PLUGIN_DIR="+pluginRoot)
	cmd.Dir = resolvedRepo
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		t.Fatalf("trace envcheck failed: %v", err)
	}

	got, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	envVars := parseEnvLines(t, string(got))

	// Value depends on build-time linker flags; just check it's non-empty.
	if v := envVars["TRACE_CLI_VERSION"]; v == "" {
		t.Errorf("TRACE_CLI_VERSION was empty")
	}
	if got, want := envVars["TRACE_REPO_ROOT"], resolvedRepo; got != want {
		t.Errorf("TRACE_REPO_ROOT = %q, want %q", got, want)
	}
	wantData := filepath.Join(pluginRoot, "data", "envcheck")
	if got := envVars["TRACE_PLUGIN_DATA_DIR"]; got != wantData {
		t.Errorf("TRACE_PLUGIN_DATA_DIR = %q, want %q", got, wantData)
	}
}

// writeEnvDumpPlugin creates an trace-envfilter plugin in its own dir
// that dumps the full child environment to env.txt. Each caller gets a
// fresh dir so parallel subtests don't trample each other's output.
func writeEnvDumpPlugin(t *testing.T) (pluginDir, envFile string) {
	t.Helper()
	pluginDir = t.TempDir()
	envFile = filepath.Join(pluginDir, "env.txt")
	body := fmt.Sprintf("#!/bin/sh\nenv > %q\nexit 0\n", envFile)
	if err := os.WriteFile(filepath.Join(pluginDir, "trace-envfilter"), []byte(body), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write plugin: %v", err)
	}
	return pluginDir, envFile
}

// TestExternalCommand_EnvFiltered_CredentialsDropped asserts that
// credential-shaped variables in the parent environment do NOT reach
// the plugin, while allowlisted OS-plumbing variables do.
func TestExternalCommand_EnvFiltered_CredentialsDropped(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("plugin shell-script harness only runs on Unix")
	}
	pluginDir, envFile := writeEnvDumpPlugin(t)

	cmd := execx.NonInteractive(context.Background(), getTestBinary(), "envfilter")
	cmd.Env = append(
		pathWith(pluginDir),
		"GITHUB_TOKEN=must-not-leak",
		"AWS_ACCESS_KEY_ID=must-not-leak",
		"NO_COLOR=1",
	)
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		t.Fatalf("trace envfilter failed: %v", err)
	}
	got, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	envVars := parseEnvLines(t, string(got))
	if _, ok := envVars["GITHUB_TOKEN"]; ok {
		t.Error("GITHUB_TOKEN must be filtered out of plugin env")
	}
	if _, ok := envVars["AWS_ACCESS_KEY_ID"]; ok {
		t.Error("AWS_ACCESS_KEY_ID must be filtered out of plugin env")
	}
	if got := envVars["NO_COLOR"]; got != "1" {
		t.Errorf("NO_COLOR = %q, want %q", got, "1")
	}
}

// TestExternalCommand_EnvFiltered_OverrideWildcard asserts that
// TRACE_PLUGIN_ENV opens names back up via wildcard, but does not
// disable filtering for everything else.
func TestExternalCommand_EnvFiltered_OverrideWildcard(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("plugin shell-script harness only runs on Unix")
	}
	pluginDir, envFile := writeEnvDumpPlugin(t)

	cmd := execx.NonInteractive(context.Background(), getTestBinary(), "envfilter")
	cmd.Env = append(
		pathWith(pluginDir),
		"TRACE_PLUGIN_ENV=AWS_*",
		"AWS_PROFILE=dev",
		"AWS_REGION=us-east-1",
		"GITHUB_TOKEN=still-must-not-leak",
	)
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		t.Fatalf("trace envfilter failed: %v", err)
	}
	got, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	envVars := parseEnvLines(t, string(got))
	if got := envVars["AWS_PROFILE"]; got != "dev" {
		t.Errorf("AWS_PROFILE = %q, want %q (override should admit it)", got, "dev")
	}
	if got := envVars["AWS_REGION"]; got != "us-east-1" {
		t.Errorf("AWS_REGION = %q, want %q (override should admit it)", got, "us-east-1")
	}
	if _, ok := envVars["GITHUB_TOKEN"]; ok {
		t.Error("GITHUB_TOKEN must remain filtered even with override")
	}
}

// parseEnvLines splits "KEY=value" lines into a map. Missing keys map
// to empty strings.
func parseEnvLines(t *testing.T, contents string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(contents, "\n"), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Errorf("malformed env line: %q", line)
			continue
		}
		m[k] = v
	}
	return m
}

func TestExternalCommand_NonExecutableReportsLaunchError(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("executable bit semantics tested on Unix only")
	}
	dir := t.TempDir()
	// Mode 0o644 — file exists on PATH but cannot be exec'd. The dispatcher
	// must report a launch failure rather than silently falling through to
	// Cobra's generic unknown-command path.
	if err := os.WriteFile(filepath.Join(dir, "trace-noexec"), []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write plugin: %v", err)
	}

	cmd := execx.NonInteractive(context.Background(), getTestBinary(), "noexec")
	cmd.Env = pathWith(dir)
	var stderr bytes.Buffer
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit for non-executable plugin")
	}
	if !strings.Contains(stderr.String(), "Failed to run plugin trace-noexec") {
		t.Errorf("expected launch-failure message in stderr, got: %s", stderr.String())
	}
}

func TestExternalCommand_AgentProtocolBinarySkipped(t *testing.T) {
	t.Parallel()
	// `trace-agent-*` is reserved for the protocol — never dispatched as
	// a passthrough plugin even when present on PATH.
	dir := t.TempDir()
	writePluginScript(t, dir, "trace-agent-foo", filepath.Join(dir, "argv.txt"), 0)

	cmd := execx.NonInteractive(context.Background(), getTestBinary(), "agent-foo")
	cmd.Env = pathWith(dir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err == nil {
		t.Fatal("expected failure — trace-agent-* must not be dispatched as a plugin")
	}
	if _, err := os.Stat(filepath.Join(dir, "argv.txt")); err == nil {
		t.Error("trace-agent-foo was invoked but must have been skipped")
	}
	// Should fall through to Cobra's unknown-command path, not be eaten silently.
	if !strings.Contains(stderr.String(), "unknown command") &&
		!strings.Contains(stderr.String(), "Invalid usage") {
		t.Errorf("expected Cobra unknown-command error, got stderr: %s", stderr.String())
	}
}
