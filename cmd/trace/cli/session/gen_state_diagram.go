//go:build ignore

// gen_mermaid generates the session phase state machine Mermaid diagram.
// Run via: go generate ./cmd/trace/cli/session/
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/session"
)

func main() {
	diagram := session.MermaidDiagram()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintln(os.Stderr, "runtime.Caller failed")
		os.Exit(1)
	}

	repoRoot := findModuleRoot(filepath.Dir(thisFile))
	outputDir := filepath.Join(repoRoot, "docs", "generated")

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create output directory: %v\n", err)
		os.Exit(1)
	}

	outputPath := filepath.Join(outputDir, "session-phase-state-machine.mmd")
	if err := os.WriteFile(outputPath, []byte(diagram), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write diagram: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Wrote %s\n", outputPath)
}

func findModuleRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			fmt.Fprintln(os.Stderr, "could not find go.mod")
			os.Exit(1)
		}
		dir = parent
	}
}
