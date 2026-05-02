//go:build e2e

package tests

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/GrayCodeAI/trace/e2e/agents"
	"github.com/GrayCodeAI/trace/e2e/trace"
	"github.com/GrayCodeAI/trace/e2e/testutil"
)

func TestMain(m *testing.M) {
	runDir := os.Getenv("E2E_ARTIFACT_DIR")
	if runDir == "" {
		_, file, _, _ := runtime.Caller(0)
		testutil.ArtifactRoot = filepath.Join(filepath.Dir(file), "..", "artifacts")
		runDir = testutil.ArtifactRunDir()
	}
	_ = os.MkdirAll(runDir, 0o755)
	testutil.SetRunDir(runDir)

	// Resolve the trace binary (set by mise run build via E2E_TRACE_BIN).
	traceBin := trace.BinPath()
	if err := ensureHookTraceBinary(traceBin); err != nil {
		fmt.Fprintf(os.Stderr, "preflight: prepare hook trace binary: %v\n", err)
		os.Exit(1)
	}

	// Prepend the binary's directory to PATH so that git hooks and agent
	// hooks (which call bare "trace") resolve to the same binary the test
	// harness uses, not a system-installed one.
	os.Setenv("PATH", filepath.Dir(traceBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Preflight: verify required dependencies before running any tests.
	// tmux is only required on Unix (interactive session tests are skipped on Windows).
	var missing []string
	requiredBins := []string{"git"}
	if runtime.GOOS != "windows" {
		requiredBins = append(requiredBins, "tmux")
	}
	for _, bin := range requiredBins {
		if _, err := exec.LookPath(bin); err != nil {
			missing = append(missing, bin)
		}
	}
	for _, a := range agents.All() {
		if _, err := exec.LookPath(a.Binary()); err != nil {
			missing = append(missing, fmt.Sprintf("%s (%s)", a.Binary(), a.Name()))
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "preflight: missing required binaries: %v\n", missing)
		os.Exit(1)
	}

	version := "unknown"
	if out, err := exec.Command(traceBin, "version").Output(); err == nil {
		version = string(out)
	}

	// Write preflight info to artifact dir only — gotestsum swallows both
	// stdout and stderr, so the test-e2e script cats this file at the end.
	preflight := fmt.Sprintf("trace binary:  %s\ntrace version: %s\n",
		traceBin, version)
	_ = os.WriteFile(filepath.Join(runDir, "trace-version.txt"), []byte(preflight), 0o644)

	// Don't look at user's Git config, ignore everything except the project-local Git settings.
	// This avoids oddball configs in ~/.gitconfig messing with our E2E tests.
	// We use an empty temp file instead of os.DevNull because git on Windows
	// cannot open NUL as a config file ("unable to access 'NUL': Invalid argument").
	emptyConfig := filepath.Join(runDir, "empty-gitconfig")
	_ = os.WriteFile(emptyConfig, nil, 0o644)
	os.Setenv("GIT_CONFIG_GLOBAL", emptyConfig)

	os.Exit(m.Run())
}

func ensureHookTraceBinary(traceBin string) error {
	dir := filepath.Dir(traceBin)
	hookName := "trace"
	if runtime.GOOS == "windows" {
		hookName = "trace.exe"
	}
	hookBin := filepath.Join(dir, hookName)
	if filepath.Clean(traceBin) == filepath.Clean(hookBin) {
		return nil
	}

	_ = os.Remove(hookBin)

	if runtime.GOOS == "windows" {
		data, err := os.ReadFile(traceBin)
		if err != nil {
			return err
		}
		return os.WriteFile(hookBin, data, 0o755)
	}

	return os.Symlink(traceBin, hookBin)
}
