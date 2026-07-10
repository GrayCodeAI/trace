---
description: trace — session capture build and test conventions.
globs: "*.go"
alwaysApply: false
---

# trace Conventions

Git-native session capture for AI coding agents.

## Build & Test

```bash
# With mise (recommended)
mise install && mise trust
mise run build
mise run test
mise run test:ci
mise run fmt
mise run lint

# Or with Go directly
go build ./...
go test ./...
```

## Architecture

- Library + CLI (surfaced via `cli.NewRootCmd()` in Hawk as `hawk trace ...`)
- No standalone `trace` binary
- Session data stored on `trace/checkpoints/v1` orphan branch

## Ecosystem Boundaries

- Uses local-only types; do not import `hawk/internal/*` or legacy paths
- Do not import other engines (`eyrie`, `yaad`, `tok`, `sight`, `inspect`)
- Hawk embeds trace through `cli.NewRootCmd()` only

For full hawk-eco extension guidelines, see [hawk/AGENTS.md](https://github.com/GrayCodeAI/hawk/blob/main/AGENTS.md).
