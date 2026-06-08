<div align="center">

# 📸 trace Architecture

**Git-Native Session Capture for AI Coding Agents**

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go)](https://go.dev/)
[![Type](https://img.shields.io/badge/Type-CLI-green)]()

</div>

---

## 🎯 Overview

trace hooks into your git workflow to capture AI agent sessions as you work. Sessions are indexed alongside commits, creating a **searchable record of how code was written**.

> 💡 Works with Claude Code, Codex, Gemini CLI, Cursor, Copilot CLI, and hawk.

---

## 🧱 Components

```
trace/
├── api/openapi.yaml              📜 CLI command surface reference
├── cli/                          📂 cobra command tree (package cli, NewRootCmd)
│   ├── trace_cmd.go              🔧 Core commands (enable, disable, status)
│   ├── hooks.go                  🪝 Git hook management
│   ├── checkpoint/               💾 Checkpoint storage & retrieval
│   ├── recap/                    📝 Session recap rendering
│   ├── settings/                 ⚙️ Configuration management
│   ├── strategy/                 📋 Checkpoint strategies
│   ├── session/                  📂 Session state management
│   └── agent/                    🤖 Agent-specific integrations
├── redact/
│   ├── redact.go                 🔒 Secret redaction (entropy + pattern + keyword)
│   ├── packs.go                  📦 Vendor-specific pattern packs
│   ├── pii.go                    🔐 PII detection
│   └── custom.go                 ⚙️ Custom redaction rules
├── internal/                     🔒 Private support packages (git ops, launch detection)
├── perf/                         📈 Performance benchmarks
└── docs/                         📖 Architecture and usage docs
```

> Trace is a **library** consumed by Hawk — there is no standalone binary. The `cli`
> package's command tree is mounted into Hawk and surfaced as `hawk trace ...`.

---

## 📂 Session Model

A **session** is a unit of work containing **checkpoints**:

| Type | Storage | Content |
|------|---------|---------|
| 💾 **Temporary** | Shadow branch (`.git/trace-sessions/`) | Full working tree state |
| 📋 **Committed** | `trace/checkpoints/v1` orphan branch | Metadata only |

Each checkpoint has a stable **12-hex-char ID** linking user commits to metadata.

---

## 🖥️ CLI Commands

Surfaced under `hawk trace ...`:

| Command | Description |
|---------|-------------|
| `hawk trace enable` | 🪝 Install git hooks |
| `hawk trace disable` | 🧹 Remove hooks |
| `hawk trace status` | 📊 Show capture status |
| `hawk trace checkpoint "msg"` | 💾 Create checkpoint |
| `hawk trace checkpoint rewind <id>` | ⏪ Rewind to checkpoint |
| `hawk trace session resume <id>` | 🔄 Resume a session |
| `hawk trace investigate <ref>` | 🔍 Investigate AI activity on a commit |
| `hawk trace doctor` | 🩺 Run diagnostics |
| `hawk trace agent <name>` | 🤖 Configure agent integration |

---

## 🌳 Git-Native Storage

Sessions are stored as **git objects** on the `trace/checkpoints/v1` orphan branch — never on the working branch.

> 💡 Standard git tools can inspect the data. No external database required.

---

## 🔒 Redaction

Multi-layer secret redaction before storing any session data:

| Layer | Strategy | Example |
|-------|----------|---------|
| 📊 **Entropy** | Shannon score > 4.5 | `AKIA...` (high-entropy strings) |
| 🔑 **Pattern** | Regex matching | JWTs, base64, DB URIs, API keys |
| 📝 **Keyword** | Key name detection | `password=`, `secret=` |
| 🔐 **PII** | Personal data | Emails, phone numbers, SSNs |
