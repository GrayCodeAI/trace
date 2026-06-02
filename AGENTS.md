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

## Naming Conventions

- **CLI commands use cobra**: `cmd/trace/` contains the CLI root and subcommands
- **Types are domain nouns**: `CodeGraphSnapshot`, `GraphStats`, `SymbolInfo`, `ModuleInfo`, `ComplexityMetrics`, `GraphDelta`
- **Snapshot store methods**: `Save()`, `Load()`, `CompareSnapshots()` — imperative verbs for operations
- **Error sentinels**: `ErrNoSnapshots` — `Err` prefix, package-level `var` with `errors.New()`
- **JSON tags on all exported fields**: `json:"timestamp"`, `json:"session_id"` — snake_case in JSON, CamelCase in Go
- **Time formatting**: use `time.RFC3339` for display, `"20060102_150405"` for filenames
- **Redaction package**: `redact/` handles secret detection — pattern names use `Pattern` suffix: `secretPattern`, `credentialedURIPattern`

## API Patterns

- **Git-native storage**: sessions stored as git objects on `trace/checkpoints/v1` orphan branch — never on the working branch
- **SnapshotStore pattern**: `NewSnapshotStore(path)` returns a store with `Save()` and `Load()` methods
- **CompareSnapshots()**: standalone function, not a method — takes two `*CodeGraphSnapshot` pointers, returns `*GraphDelta`
- **FormatSnapshot()**: standalone function returning markdown string — display formatting separated from data
- **Placeholder I/O functions**: `writeFile()`, `listFiles()`, `readFile()` are stubs — implement with actual file I/O
- **Config in `.trace/` directory**: `settings.json` (committed) + `settings.local.json` (gitignored)
- **Redaction is multi-layered**: entropy-based (`entropyThreshold = 4.5`), pattern-based (JWT, base64, DB URIs), keyword-based (password, secret)
- **Mise-based dev tooling**: `mise.toml` defines tasks — `mise run build`, `mise run test`, `mise run test:ci`

## Testing Patterns

- **Integration tests in `e2e/` directory**: separate from unit tests, test full agent session flows
- **Nilnil pattern**: agent launch functions return `(T, error)` where both can be nil — test with explicit nil checks
- **Git-dependent tests**: some tests need a real git repo — use `t.TempDir()` + `git init` for isolation
- **Redaction tests**: `redact/redact_test.go` tests secret detection patterns — cover entropy edge cases, placeholder exclusion
- **Bench tests**: `redact/redact_bench_test.go` — performance-critical path, benchmark before changing regex patterns
- **Snapshot tests**: test `Save()`/`Load()` roundtrip with `json.MarshalIndent` — verify timestamp formatting
- **Delta comparison tests**: create two snapshots, verify `CompareSnapshots()` produces correct `GraphDelta`
- **Perfsprint linter**: enforced in CI — `errors.New(fmt.Sprintf(...))` must be `fmt.Errorf(...)` instead

## Refactoring Guidelines

- **Safe to refactor**: `FormatSnapshot()` — display-only, no data contract
- **Safe to refactor**: `CompareSnapshots()` — internal comparison logic, extend with new delta fields
- **Safe to refactor**: redaction patterns in `redact/` — add new patterns, tune entropy threshold
- **Safe to refactor**: CLI commands in `cmd/trace/` — cobra command structure, add subcommands freely
- **Do not touch**: `CodeGraphSnapshot` struct field names/tags — JSON serialization contract for stored snapshots
- **Do not touch**: `trace/checkpoints/v1` branch name — hardcoded in consumers and git hooks
- **Do not touch**: `.trace/settings.json` schema — committed config, changing breaks existing repos
- **When adding agents**: update `cmd/trace/cli/agent/` — each agent has its own integration file
- **When adding redaction patterns**: add to `redact/redact.go` with corresponding test in `redact/redact_test.go`

## Key File Locations

| What | Where |
|---|---|
| Code graph snapshot types | `codegraph_snapshot.go` (`CodeGraphSnapshot`, `GraphStats`, `GraphDelta`) |
| CLI entry point | `cmd/trace/main.go` |
| CLI commands | `cmd/trace/cli/` (enable, disable, status, checkpoint, session, agent, doctor) |
| Secret redaction | `redact/redact.go` (entropy, patterns, DB URIs, JWT, base64) |
| Redaction pattern packs | `redact/packs.go` (vendor-specific patterns) |
| PII detection | `redact/pii.go` |
| Custom redaction rules | `redact/custom.go` |
| Agent launch detection | `internal/agentlaunch/` |
| Session capture engine | `capture.go` (if exists at root) |
| Git operations | `internal/` subdirectories |
| E2E test suite | `e2e/` directory |
| Perf benchmarks | `perf/` directory |
| Mise task definitions | `mise.toml` |
| Docs | `docs/` (investigate command, security, privacy) |
| Linter config | `.golangci.yaml` (very strict: 40+ linters including perfsprint, nilnil, wrapcheck, gosec) |
