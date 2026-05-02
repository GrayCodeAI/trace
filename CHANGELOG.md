# Changelog

All notable changes to this project will be documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/). This project adheres to [Semantic Versioning](https://semver.org/).

---

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
