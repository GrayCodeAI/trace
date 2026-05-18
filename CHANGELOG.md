# Changelog

All notable changes to this project will be documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/). This project adheres to [Semantic Versioning](https://semver.org/).

---

## [0.2.1](https://github.com/GrayCodeAI/trace/compare/v0.2.0...v0.2.1) (2026-05-18)


### Bug Fixes

* gofumpt formatting + go mod tidy ([b0c7ecb](https://github.com/GrayCodeAI/trace/commit/b0c7ecb2bfee6815fba3a0bf5ddb2422eaff9ca2))
* gofumpt formatting + go mod tidy ([945eac1](https://github.com/GrayCodeAI/trace/commit/945eac1609c6de4bd7e23b82582c59be9aeb6927))
* upgrade Go from 1.26.1 to 1.26.3 to patch stdlib vulnerabilities ([6480766](https://github.com/GrayCodeAI/trace/commit/648076644f70601ce63fbe95cefe9dd13d31a334))

## [Unreleased]

### Changed
- **Version re-baselined to `0.2.0`** in
  `cmd/trace/cli/versioninfo/versioninfo.go`. Aligns trace with the rest
  of the hawk-eco ecosystem (`hawk`, `tok`, `eyrie`, `yaad`, `sight`,
  `inspect`).

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
