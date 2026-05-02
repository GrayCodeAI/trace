# Windows Support

Trace is fully supported on Windows 10+ with no external dependencies beyond Git.

---

## Installation

### Scoop (recommended)

```powershell
scoop bucket add trace https://github.com/GrayCodeAI/scoop-bucket.git
scoop install trace
```

### Go install

```powershell
go install github.com/GrayCodeAI/trace/cmd/trace@latest
```

### Build from source

```powershell
go build -o trace.exe ./cmd/trace/
```

Or cross-compile from macOS/Linux:

```bash
mise run build:windows        # amd64
mise run build:windows-arm64  # arm64
```

---

## Prerequisites

- **Windows 10 1809+** (ConPTY support)
- **Git for Windows** (provides `git.exe` + bundled bash for hooks)
- **Go 1.26+** (for building from source)

---

## Usage

All commands work identically to macOS/Linux:

```powershell
cd your-project
trace enable
trace status
```

---

## How Hooks Work

| Hook type | Mechanism |
|---|---|
| Git hooks | `#!/bin/sh` via Git for Windows MSYS2 bash |
| Agent hooks | JSON config, agents call `trace.exe` directly |

No batch file wrappers needed.

---

## Testing

```powershell
# Unit tests
go test ./...

# Integration tests
go test -tags=integration ./cmd/trace/cli/integration_test/...

# E2E tests
set E2E_TRACE_BIN=trace.exe
set E2E_AGENT=claude-code
go test -tags=e2e -count=1 -timeout=30m ./e2e/tests/...
```

E2E uses native ConPTY — no tmux required.

---

## Platform Architecture

| File | Purpose |
|---|---|
| `telemetry/detached_windows.go` | Process groups via `CREATE_NEW_PROCESS_GROUP` |
| `integration_test/procattr_windows.go` | Test process detachment |
| `e2e/agents/pty_session_windows.go` | Interactive sessions via ConPTY |

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

After building, verify:

1. `trace.exe version`
2. `trace.exe enable` in a git repo
3. Start agent session, make changes
4. `git commit -m "test"` — hooks fire
5. `trace checkpoint rewind` — shows checkpoints
6. `trace checkpoint explain` — pager works
