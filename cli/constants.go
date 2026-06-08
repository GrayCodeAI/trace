package cli

import "github.com/GrayCodeAI/trace/cli/paths"

// Note: Tool name constants (ToolWrite, ToolEdit, etc.) and FileModificationTools
// have been moved to the agent/claudecode package.

// Directory paths - re-exported from paths package for convenience
const (
	TraceDir         = paths.TraceDir
	TraceTmpDir      = paths.TraceTmpDir
	TraceMetadataDir = paths.TraceMetadataDir
)
