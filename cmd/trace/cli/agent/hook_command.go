package agent

import (
	"fmt"
	"strings"

	"github.com/GrayCodeAI/trace/cmd/trace/cli/jsonutil"
)

type WarningFormat int

const (
	WarningFormatSingleLine WarningFormat = iota + 1
	WarningFormatMultiLine
)

func MissingTraceWarning(format WarningFormat) string {
	switch format {
	case WarningFormatSingleLine:
		return "Trace CLI is enabled but not installed or not on PATH. Installation guide: https://docs.trace.io/cli/installation#installation-methods"
	case WarningFormatMultiLine:
		return "\n\nTrace CLI is enabled but not installed or not on PATH.\nInstallation guide: https://docs.trace.io/cli/installation#installation-methods"
	default:
		return MissingTraceWarning(WarningFormatSingleLine)
	}
}

// WrapProductionSilentHookCommand exits successfully without output when the
// Trace CLI is missing from PATH.
func WrapProductionSilentHookCommand(command string) string {
	return fmt.Sprintf(
		`sh -c 'if ! command -v trace >/dev/null 2>&1; then exit 0; fi; exec %s'`,
		command,
	)
}

// WrapProductionJSONWarningHookCommand emits a JSON hook response with a
// systemMessage field on stdout when the Trace CLI is missing from PATH.
func WrapProductionJSONWarningHookCommand(command string, format WarningFormat) string {
	payload, err := jsonutil.MarshalWithNoHTMLEscape(struct {
		SystemMessage string `json:"systemMessage,omitempty"`
	}{
		SystemMessage: MissingTraceWarning(format),
	})
	if err != nil {
		// Fallback to plain text on stdout if JSON payload construction somehow fails.
		return WrapProductionPlainTextWarningHookCommand(command, format)
	}

	return fmt.Sprintf(
		`sh -c 'if ! command -v trace >/dev/null 2>&1; then printf "%%s\n" %q; exit 0; fi; exec %s'`,
		string(payload),
		command,
	)
}

// WrapProductionPlainTextWarningHookCommand emits the warning as plain
// text to stdout when the Trace CLI is missing from PATH.
func WrapProductionPlainTextWarningHookCommand(command string, format WarningFormat) string {
	return fmt.Sprintf(
		`sh -c 'if ! command -v trace >/dev/null 2>&1; then printf "%%s\n" %q; exit 0; fi; exec %s'`,
		MissingTraceWarning(format),
		command,
	)
}

const productionHookWrapperPrefix = `sh -c 'if ! command -v trace >/dev/null 2>&1; then `

// IsManagedHookCommand reports whether command is either a direct Trace hook
// command or one of Trace's production wrapper forms that exec that command.
func IsManagedHookCommand(command string, prefixes []string) bool {
	if hasManagedHookPrefix(command, prefixes) {
		return true
	}
	if !strings.HasPrefix(command, productionHookWrapperPrefix) {
		return false
	}

	_, wrappedCommand, ok := strings.Cut(command, "; fi; exec ")
	if !ok {
		return false
	}

	return hasManagedHookPrefix(wrappedCommand, prefixes)
}

func hasManagedHookPrefix(command string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(command, prefix) {
			return true
		}
	}
	return false
}
