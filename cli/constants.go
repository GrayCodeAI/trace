package cli

import "github.com/GrayCodeAI/trace/cli/paths"

// Note: Tool name constants (ToolWrite, ToolEdit, etc.) and FileModificationTools
// have been moved to the agent/claudecode package.

// Directory paths - re-exported from paths package for convenience
const (
	EntireDir         = paths.EntireDir
	EntireTmpDir      = paths.EntireTmpDir
	EntireMetadataDir = paths.EntireMetadataDir
)
