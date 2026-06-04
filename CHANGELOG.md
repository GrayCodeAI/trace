# Changelog

All notable changes to this project will be documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/). This project adheres to [Semantic Versioning](https://semver.org/).

---

## [Unreleased]

### Changed
- **Version re-baselined to `0.1.0`** in
  `cmd/trace/cli/versioninfo/versioninfo.go`. Aligns trace with the rest
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
