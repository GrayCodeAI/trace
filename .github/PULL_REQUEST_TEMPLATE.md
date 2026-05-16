<!--
  Thanks for your contribution! Please fill out this template so reviewers can
  understand the change quickly. Anything that does not apply can be left in
  place; do not delete unanswered sections — write "n/a".
-->

## Summary

<!--
  One paragraph describing what this PR does and why. Link the related
  issue(s) with `Fixes #N` or `Refs #N` if applicable.
-->

## Changes

<!--
  Bullet list of what changed, grouped by area (cmd/trace, perf, redact,
  e2e, docs, CI). Reviewers should be able to skim this and know what to
  look at first.
-->

-

## Privacy & redaction impact

<!--
  Trace records developer sessions. Any change that touches `redact/`,
  `cmd/trace/cli/checkpoint/`, or transcript serialization can leak PII,
  secrets, or proprietary code if regressed.

  - Did you change `redact/redact.go`, `redact/pii.go`, the secret-pattern
    list, or any code that decides what gets persisted?
  - If yes: paste before/after `go test -count=1 ./redact/...` results
    and explicitly call out which redaction patterns were added, removed,
    or relaxed.
  - If no: write "n/a".
-->

## Agent compatibility

<!--
  trace integrates with Claude Code, Codex, Gemini CLI, OpenCode, Cursor,
  Factory AI Droid, Copilot CLI, etc. Did you change any agent-specific
  parsing or transcript shape?

  - Which agents did you test against? (`scripts/test-*-agent-integration.sh`,
    `e2e/agents/`, or manual.)
  - Note any agent that you could not test locally and why.
-->

## Testing

<!--
  Describe how you tested. Paste output of `mise run test` (or `go test
  -race -count=1 ./...`) and the lint task. If you added new tests,
  list them.
-->

```text
$ go test -race -count=1 ./...
...
$ golangci-lint run ./...
...
```

## Checklist

- [ ] Commits follow [Conventional Commits](https://www.conventionalcommits.org/)
      (`feat:`, `fix:`, `perf:`, `refactor:`, `docs:`, `test:`, etc.)
- [ ] `go build ./...` passes
- [ ] `golangci-lint run ./...` passes (no new lint findings, no
      `nolint:…` without justification)
- [ ] `go test -race -count=1 ./...` passes locally
- [ ] e2e impact considered (relevant `e2e/` test added, updated, or
      verified) — n/a if change is contained to non-runtime code
- [ ] Public APIs in `cmd/trace/cli/...` have godoc comments
- [ ] `CHANGELOG.md` updated under `## [Unreleased]` if user-visible
- [ ] No regression in `redact/` tests
- [ ] No secrets, tokens, or PII added to the repo (test fixtures use
      synthetic values only)
- [ ] No `Co-authored-by:` trailers (this is solo-developer work)
