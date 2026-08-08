// hook_guard.go protects against cross-agent hook forwarding. Cursor IDE
// invokes any hook configured under .claude/settings.json or .cursor/hooks.json
// for the active session — when only one of those files is installed, the
// other agent's hook command receives the event. shouldSkipForwardedHook
// detects this by inspecting the transcript path: if it lives inside another
// registered agent's session directory, the firing agent is forwarded and
// must no-op so the session isn't claimed for the wrong agent (#1262).
package cli
