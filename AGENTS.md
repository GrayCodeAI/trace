# AGENTS.md — Trace

Git-native session capture for AI coding agents. Hooks into your Git workflow to capture AI agent sessions as you work. Sessions are indexed alongside commits, creating a searchable record of how code was written.

## Design Principles

- **Git-native** — sessions stored as git objects, searchable with standard tools
- **Non-intrusive** — hooks into git without modifying your workflow
- **Replayable** — reconstruct any session from captured data

## Build & Test

```bash
go test ./...                    # Run all tests
go test -race ./...              # Race detector
go test -coverprofile=c.out ./... # Coverage
go vet ./...                     # Static analysis
gofumpt -w .                     # Format
```

## Architecture

- `capture.go` — Session capture engine (hooks into git)
- `session.go` — Session data model and storage
- `replay.go` — Session reconstruction and replay
- `search.go` — Full-text search across sessions
- `cmd/` — CLI commands (capture, list, search, replay, investigate)
- `internal/agentlaunch/` — Agent launch detection and tracking

## Conventions

- Go 1.26+, pure Go, no CGO
- Table-driven tests
- Conventional Commits: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`
- No `Co-authored-by:` trailers (auto-stripped by githook)
- `gofumpt` formatting enforced in CI
- Investigate command has its own docs in `docs/`

## Common Pitfalls

- Agent launch tests need careful nil handling (nilnil pattern)
- Perfsprint linter is strict — use `fmt.Errorf` not `errors.New(fmt.Sprintf(...))`
- Session storage uses git objects — don't modify `.git/` directly in tests
