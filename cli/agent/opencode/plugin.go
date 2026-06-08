package opencode

import _ "embed"

//go:embed trace_plugin.ts
var pluginTemplate string

// traceCmdPlaceholder is replaced with the actual command during installation.
const traceCmdPlaceholder = "__TRACE_CMD__"
