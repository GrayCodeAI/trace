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
├── cmd/trace/                    🖥️ CLI entry point
│   ├── main.go                   ⚡ Root cobra command
│   └── cli/                      📂 Subcommand implementations
│       ├── trace_cmd.go          🔧 Core commands (enable, disable, status)
│       ├── hooks.go              🪝 Git hook management
│       ├── checkpoint/           💾 Checkpoint storage & retrieval
│       ├── recap/                📝 Session recap rendering
│       ├── settings/             ⚙️ Configuration management
│       └── agent/                🤖 Agent-specific integrations
├── codegraph_snapshot.go         📊 CodeGraphSnapshot, GraphStats, GraphDelta
├── strategy/                     📋 Checkpoint strategies
├── session/                      📂 Session state management
├── redact/
│   ├── redact.go                 🔒 Secret redaction (entropy + pattern + keyword)
│   ├── packs.go                  📦 Vendor-specific pattern packs
│   ├── pii.go                    🔐 PII detection
│   └── custom.go                 ⚙️ Custom redaction rules
├── internal/
│   └── agentlaunch/              🚀 Agent launch detection
├── e2e/                          🧪 End-to-end test suite
├── perf/                         📈 Performance benchmarks
└── docs/                         📖 Architecture and usage docs
```

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

| Command | Description |
|---------|-------------|
| `trace enable` | 🪝 Install git hooks |
| `trace disable` | 🧹 Remove hooks |
| `trace status` | 📊 Show capture status |
| `trace checkpoint "msg"` | 💾 Create checkpoint |
| `trace checkpoint rewind <id>` | ⏪ Rewind to checkpoint |
| `trace session resume <id>` | 🔄 Resume a session |
| `trace investigate <ref>` | 🔍 Investigate AI activity on a commit |
| `trace doctor` | 🩺 Run diagnostics |
| `trace agent <name>` | 🤖 Configure agent integration |

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

---

## 📊 Code Graph Snapshots

`CodeGraphSnapshot` captures project structure at checkpoint time:

| Type | Contains |
|------|----------|
| `SymbolInfo` | Functions, types, variables |
| `ModuleInfo` | Package structure, imports |
| `ComplexityMetrics` | Cyclomatic complexity, nesting |
| `GraphDelta` | Diff between two snapshots (`CompareSnapshots()`) |
