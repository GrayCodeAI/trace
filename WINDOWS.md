# Windows Support

Trace is a library consumed by **Hawk**; its Windows behaviour is exercised through the
`hawk trace ...` commands. The trace packages are fully supported on Windows 10+ with no
external dependencies beyond Git. This document covers the Windows-specific platform code in
the library.

---

## Prerequisites

- **Windows 10 1809+** (ConPTY support)
- **Git for Windows** (provides `git.exe` + bundled bash for hooks)
- **Go 1.26+** (for building/testing from source)

---

## Hooks on Windows

| Hook type | Mechanism |
|---|---|
| Git hooks | `#!/bin/sh` via Git for Windows MSYS2 bash |
| Agent hooks | JSON config; agents invoke the hawk binary directly |

No batch file wrappers needed.

---

## Testing the library on Windows

```powershell
# Unit tests
go test ./...

# Integration tests
go test -tags=integration ./cli/integration_test/...
```

---

## Platform Architecture

| File | Purpose |
|---|---|
| `telemetry/detached_windows.go` | Process groups via `CREATE_NEW_PROCESS_GROUP` |
| `cli/integration_test/procattr_windows.go` | Test process detachment |

### Cross-platform patterns

- `os.Interrupt` only (no `SIGTERM`)
- `filepath.FromSlash()` for git output paths
- CRLF-safe parsing (`\r\n` -> `\n`)
- `os.DevNull` instead of `/dev/null`
- `runtime.GOOS` checks for pager selection

---

## Known Limitations

- Interactive PTY tests skipped (guarded with `//go:build unix`)
- File permissions (0o755) set but ignored by Windows ACLs
- Symlinks require Developer Mode or admin
- OpenCode plugin pending Windows Bun support

---

## Smoke Test

Via the `hawk` CLI on Windows, verify:

1. `hawk trace enable` in a git repo
2. Start agent session, make changes
3. `git commit -m "test"` — hooks fire
4. `hawk trace checkpoint rewind` — shows checkpoints
5. `hawk trace checkpoint explain` — pager works
