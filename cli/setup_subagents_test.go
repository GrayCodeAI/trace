package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GrayCodeAI/trace/cli/agent/claudecode"
	"github.com/GrayCodeAI/trace/cli/agent/codex"
	"github.com/GrayCodeAI/trace/cli/agent/geminicli"
)

func TestScaffoldSearchSubagent_CreatesManagedFiles(t *testing.T) {
	testCases := []struct {
		name        string
		scaffoldFn  func() (searchSubagentScaffoldResult, error)
		relPath     string
		wantSnippet string
	}{
		{
			name: "claude",
			scaffoldFn: func() (searchSubagentScaffoldResult, error) {
				return scaffoldSearchSubagent(context.Background(), claudecode.NewClaudeCodeAgent())
			},
			relPath:     filepath.Join(".claude", "agents", "trace-search.md"),
			wantSnippet: "tools: Bash",
		},
		{
			name: "codex",
			scaffoldFn: func() (searchSubagentScaffoldResult, error) {
				return scaffoldSearchSubagent(context.Background(), codex.NewCodexAgent())
			},
			relPath:     filepath.Join(".codex", "agents", "trace-search.toml"),
			wantSnippet: `sandbox_mode = "read-only"`,
		},
		{
			name: "gemini",
			scaffoldFn: func() (searchSubagentScaffoldResult, error) {
				return scaffoldSearchSubagent(context.Background(), geminicli.NewGeminiCLIAgent())
			},
			relPath:     filepath.Join(".gemini", "agents", "trace-search.md"),
			wantSnippet: "- run_shell_command",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := setupTestDir(t)

			result, err := tc.scaffoldFn()
			if err != nil {
				t.Fatalf("scaffoldSearchSubagent() error = %v", err)
			}
			if result.Status != searchSubagentCreated {
				t.Fatalf("scaffoldSearchSubagent() status = %q, want %q", result.Status, searchSubagentCreated)
			}
			if result.RelPath != tc.relPath {
				t.Fatalf("scaffoldSearchSubagent() relPath = %q, want %q", result.RelPath, tc.relPath)
			}

			data, err := os.ReadFile(filepath.Join(tmpDir, tc.relPath))
			if err != nil {
				t.Fatalf("failed to read scaffolded file: %v", err)
			}
			content := string(data)
			if !strings.Contains(content, traceManagedSearchSubagentMarker) {
				t.Fatal("scaffolded file should contain Trace-managed marker")
			}
			assertStrictJSONSearchInstructions(t, content)
			if !strings.Contains(content, tc.wantSnippet) {
				t.Fatalf("scaffolded file missing expected snippet %q", tc.wantSnippet)
			}
		})
	}
}

func TestScaffoldSearchSubagent_IdempotentManagedFile(t *testing.T) {
	setupTestDir(t)

	ag := claudecode.NewClaudeCodeAgent()
	if _, err := scaffoldSearchSubagent(context.Background(), ag); err != nil {
		t.Fatalf("first scaffoldSearchSubagent() error = %v", err)
	}

	result, err := scaffoldSearchSubagent(context.Background(), ag)
	if err != nil {
		t.Fatalf("second scaffoldSearchSubagent() error = %v", err)
	}
	if result.Status != searchSubagentUnchanged {
		t.Fatalf("second scaffoldSearchSubagent() status = %q, want %q", result.Status, searchSubagentUnchanged)
	}
}

func TestScaffoldSearchSubagent_UpdatesManagedFile(t *testing.T) {
	tmpDir := setupTestDir(t)

	ag := claudecode.NewClaudeCodeAgent()
	relPath, _, ok := searchSubagentTemplate(ag.Name())
	if !ok {
		t.Fatal("searchSubagentTemplate() unexpectedly unsupported for claude")
	}

	targetPath := filepath.Join(tmpDir, relPath)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("failed to create target dir: %v", err)
	}
	oldContent := "<!-- " + traceManagedSearchSubagentMarker + " -->\noutdated\n"
	if err := os.WriteFile(targetPath, []byte(oldContent), 0o644); err != nil {
		t.Fatalf("failed to write old managed content: %v", err)
	}

	result, err := scaffoldSearchSubagent(context.Background(), ag)
	if err != nil {
		t.Fatalf("scaffoldSearchSubagent() error = %v", err)
	}
	if result.Status != searchSubagentUpdated {
		t.Fatalf("scaffoldSearchSubagent() status = %q, want %q", result.Status, searchSubagentUpdated)
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("failed to read updated content: %v", err)
	}
	if !strings.Contains(string(data), "tools: Bash") {
		t.Fatal("updated managed file should contain the current template")
	}
	assertStrictJSONSearchInstructions(t, string(data))
}

func TestScaffoldSearchSubagent_PreservesUserOwnedFile(t *testing.T) {
	tmpDir := setupTestDir(t)

	ag := claudecode.NewClaudeCodeAgent()
	relPath, _, ok := searchSubagentTemplate(ag.Name())
	if !ok {
		t.Fatal("searchSubagentTemplate() unexpectedly unsupported for claude")
	}

	targetPath := filepath.Join(tmpDir, relPath)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("failed to create target dir: %v", err)
	}
	userContent := "user-owned search agent\n"
	if err := os.WriteFile(targetPath, []byte(userContent), 0o644); err != nil {
		t.Fatalf("failed to write user-owned file: %v", err)
	}

	result, err := scaffoldSearchSubagent(context.Background(), ag)
	if err != nil {
		t.Fatalf("scaffoldSearchSubagent() error = %v", err)
	}
	if result.Status != searchSubagentSkippedConflict {
		t.Fatalf("scaffoldSearchSubagent() status = %q, want %q", result.Status, searchSubagentSkippedConflict)
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("failed to read preserved file: %v", err)
	}
	if string(data) != userContent {
		t.Fatal("user-owned file should not be overwritten")
	}
}

func assertStrictJSONSearchInstructions(t *testing.T, content string) {
	t.Helper()

	if !strings.Contains(content, "trace search --json") {
		t.Fatal("scaffolded file should instruct use of `trace search --json`")
	}
	if !strings.Contains(content, "Never run `trace search` without `--json`; it opens an interactive TUI.") {
		t.Fatal("scaffolded file should explicitly forbid plain `trace search`")
	}
	if strings.Contains(content, "Your only history-search mechanism is the `trace search` command.") {
		t.Fatal("scaffolded file should not present plain `trace search` as the required command")
	}
}
