// Package trace provides distributed tracing and observability.
//
// The Version variable is sourced from the VERSION file at the repo root.
package trace

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var versionFile string

// Version of the trace library. Single source of truth: VERSION file.
var Version = strings.TrimSpace(versionFile)
