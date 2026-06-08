# Changelog

All notable changes to this project will be documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/). This project adheres to [Semantic Versioning](https://semver.org/).

---

## [Unreleased]

### Added — New features
- `trace fork` — clone a checkpoint into a new independent session for A/B testing from any point.
- Three-mode commit attribution — `Co-authored-by:` trailer (default on), plus opt-in `attribute_author` and `attribute_committer` to set the git author/committer to the agent.
- Pre-edit dirty-working-tree auto-commit — saves work-in-progress before an agent session (default on; `--no-dirty-commits` to skip).
- Per-session and per-tool USD cost attribution derived from recorded token usage and model pricing.
- Webhook notifications on session lifecycle events via the `webhooks` config (default off).
- `trace ci-init` — configure Trace to auto-capture sessions in CI.
- `trace annotate` — attach a comment to a session or checkpoint.
- asciinema export — `trace session export --format asciinema` renders a transcript as a playable v2 cast.

### Changed
- **Version re-baselined to `0.1.0`** in
  `cli/versioninfo/versioninfo.go`. Aligns trace with the rest
  of the hawk-eco ecosystem (`hawk`, `tok`, `eyrie`, `yaad`, `sight`,
  `inspect`).

### Added
- TRACE_TAG_* env var session metadata
- gen_ai.* OTel span aliases
- OpenTelemetry collector client with batching and retry

### Added — Production hygiene (top-50 OSS parity)
- `.gitattributes` — LF line-ending normalization, binary detection,
  GitHub linguist hints (collapse `go.sum` in PR diffs, mark `docs/**`
  and the symlinked `AGENTS.md` as documentation).
- `.editorconfig` — consistent formatting across editors (UTF-8, LF
  newlines, 2-space YAML, tabs for Go, final newline + trim trailing
  whitespace).
- `.github/PULL_REQUEST_TEMPLATE.md` — Summary / Changes / Privacy &
  redaction impact / Agent compatibility / Testing / Checklist. The
  privacy/redaction section is specific to trace because every change
  in `redact/` can leak PII or secrets if regressed.

## [0.1.0] - 2026-05-03

### Added

- Initial release of Trace CLI
- Git-native session capture for AI coding agents
- Support for Claude Code, Codex, Gemini CLI, OpenCode, Cursor, Factory AI Droid, and Copilot CLI
- Checkpoint rewind and session resume
- Auto-summarization with AI providers
- Checkpoint remote support (push session data to separate repos)
- `trace enable` / `trace disable` lifecycle management
- `trace checkpoint rewind` for restoring previous states
- `trace session resume` for cross-branch session recovery
- `trace doctor` for diagnostics and repair
- `trace clean` for state cleanup
- `trace explain` for checkpoint summaries
- Device auth login flow
- Git worktree support
- Concurrent session handling
- Automatic secret redaction in transcripts
- Accessible mode for screen readers
