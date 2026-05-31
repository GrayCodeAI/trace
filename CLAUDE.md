# trace

Git-native session capture and replay CLI. Records coding sessions as transcripts.

## Build & Test
- `go test ./... -count=1` — run all tests
- `go build ./cmd/trace` — build CLI binary

## Architecture
- `cmd/trace/cli/` — CLI command implementations
- `cmd/trace/cli/session/` — session state management, tags
- `cmd/trace/cli/transcript/compact/` — transcript compaction
- `otel_collector.go` — OpenTelemetry collector client with batching

## Key Patterns
- Session-based architecture with state persistence
- `TRACE_TAG_*` env vars for session metadata
- `gen_ai.*` OTel span conventions
- SSE stream parsing for real-time capture

## Recent Additions
- `TRACE_TAG_*` env var session metadata
- `gen_ai.*` OTel span aliases
- OTel collector client with batching and retry
