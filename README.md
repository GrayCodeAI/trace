<p align="center">
  <h1 align="center">Trace</h1>
  <p align="center">
    <strong>Git-native session capture for AI coding agents</strong>
  </p>
  <p align="center">
    <a href="#quick-start">Quick Start</a> &bull;
    <a href="#how-it-works">How It Works</a> &bull;
    <a href="#commands">Commands</a> &bull;
    <a href="#configuration">Configuration</a> &bull;
    <a href="CONTRIBUTING.md">Contributing</a>
  </p>
</p>

---

Trace hooks into your Git workflow to capture AI agent sessions as you work. Sessions are indexed alongside commits, creating a searchable record of *how* code was written — not just *what* changed.

## Ecosystem Boundaries

Trace is a Hawk support engine. Keep the dependency edge one-way:

- trace uses local-only types (trace/redaction event types are trace-scoped, not shared contracts)
- do not import `hawk/internal/*`
- do not import removed legacy path `hawk/shared/types`
- do not import other engines (`eyrie`, `yaad`, `tok`, `sight`, `inspect`) — engines are peers, not dependencies

### What you get

| Capability | Description |
|---|---|
| **Understand why code changed** | Full prompt/response transcripts, files touched, token usage |
| **Rewind instantly** | Go back to any checkpoint when an agent goes sideways |
| **Fork & A/B** | Branch a new independent session from any checkpoint to explore alternatives |
| **Resume seamlessly** | Pick up where you or a coworker left off on any branch |
| **Cost attribution** | USD cost broken down per session and per tool from recorded token usage |
| **Clean git history** | All session data lives on a separate branch — zero noise |
| **Audit & compliance** | Searchable, versioned record of every AI interaction |

### Supported Agents

| Agent | Status |
|---|---|
| Claude Code | Fully supported |
| Codex | Fully supported |
| Gemini CLI | Fully supported |
| OpenCode | Fully supported |
| Cursor | Supported (rewind unavailable) |
| Factory AI Droid | Fully supported |
| Copilot CLI | Fully supported |

---

## Quick Start

Trace is a **library**, not a standalone binary. Its full command tree is built by
`cli.NewRootCmd()` and surfaced inside the **Hawk** CLI as `hawk trace ...` (Hawk is in
development — no public install yet). There is no separate `trace` binary to install.

**Contributors — build/test the library from source:**

```bash
git clone https://github.com/GrayCodeAI/trace && cd trace
go build ./...
go test ./...
```

Once Hawk is available, use the commands under `hawk trace`:

```bash
# Enable in your project
cd your-project
hawk trace enable

# Check status
hawk trace status
```

That's it. Trace runs silently in the background via Git hooks.

---

## How It Works

```
Your Branch                    trace/checkpoints/v1
     |                                  |
     v                                  |
[Base Commit]                           |
     |                                  |
     |  +--- Agent works ---+           |
     |  |  Step 1           |           |
     |  |  Step 2           |           |
     |  |  Step 3           |           |
     |  +-------------------+           |
     |                                  |
     v                                  v
[Your Commit] ----------------------> [Session Metadata]
     |                           (transcript, prompts,
     v                            files touched, tokens)
```

**Key principles:**

- Zero commits on your active branch
- Session data stored on `trace/checkpoints/v1` orphan branch
- Checkpoints created automatically at each commit
- Non-destructive rewind — restores files without altering history
- Works on any branch (main, feature, etc.)

---

## Typical Workflow

### 1. Enable

```bash
trace enable                     # Interactive setup
trace enable --agent claude-code # Non-interactive
```

### 2. Work normally

Use your AI agent as before. Trace captures everything in the background.

```bash
trace status   # Check session anytime
```

### 3. Rewind if needed

```bash
trace checkpoint rewind   # Select a checkpoint to restore
```

### 4. Resume on another branch

```bash
trace session resume <branch>   # Restore session metadata & continue
```

### 5. Disable (optional)

```bash
trace disable   # Removes hooks, code untouched
```

---

## Commands

| Command | Description |
|---|---|
| `trace enable` | Enable Trace in your repository |
| `trace disable` | Remove hooks from repository |
| `trace status` | Show current session info |
| `trace agent` | Add, remove, or list agent integrations |
| `trace configure` | Update non-agent settings |
| `trace checkpoint` | List, explain, rewind, search checkpoints |
| `trace checkpoint rewind` | Rewind to a previous checkpoint |
| `trace checkpoint explain` | Explain a session or checkpoint |
| `trace fork` | Clone a checkpoint into a new independent session for A/B testing |
| `trace annotate` | Attach a comment to a session or checkpoint |
| `trace ci-init` | Configure Trace to auto-capture sessions in CI |
| `trace session` | View and manage sessions |
| `trace session resume` | Restore session on a branch |
| `trace session attach` | Attach to a detached session |
| `trace session export` | Export a session (JSON envelope or asciinema cast) |
| `trace clean` | Clean up orphaned session data |
| `trace doctor` | Diagnose and fix issues |
| `trace login` | Authenticate with Trace |
| `trace version` | Show CLI version |

Run `trace <command> --help` for detailed usage.

---

## Configuration

Trace stores config in `.trace/` at the repo root.

### Project settings (`.trace/settings.json`)

Shared with the team, committed to git:

```json
{
  "enabled": true,
  "strategy_options": {
    "push_sessions": true,
    "summarize": { "enabled": true }
  }
}
```

### Local overrides (`.trace/settings.local.json`)

Personal, gitignored:

```json
{
  "log_level": "debug"
}
```

### All options

| Option | Values | Description |
|---|---|---|
| `enabled` | `true` / `false` | Toggle Trace |
| `log_level` | `debug`, `info`, `warn`, `error` | Logging verbosity |
| `strategy_options.push_sessions` | `true` / `false` | Auto-push checkpoints on git push |
| `strategy_options.checkpoint_remote` | `{"provider": "github", "repo": "..."}` | Push checkpoints to separate repo |
| `strategy_options.summarize.enabled` | `true` / `false` | AI summaries at commit time |
| `attribution.attribute_co_authored_by` | `true` / `false` | Append `Co-authored-by: <agent>` trailer (default on) |
| `attribution.attribute_author` | `true` / `false` | Set the git author to the agent (default off) |
| `attribution.attribute_committer` | `true` / `false` | Set the git committer to the agent (default off) |
| `dirty_commits` | `true` / `false` | Auto-commit a dirty working tree before an agent session (default on; `--no-dirty-commits` to skip) |
| `webhooks` | `{"urls": ["..."], "events": ["..."]}` | POST a JSON notification on session lifecycle events (default off) |
| `telemetry` | `true` / `false` | Anonymous usage analytics |

### Checkpoint Remote

Push session data to a separate private repo:

```bash
trace enable --checkpoint-remote github:myorg/checkpoints-private
```

---

## Security & Privacy

- Session transcripts live on `trace/checkpoints/v1` in your repo
- Secrets are automatically redacted (API keys, tokens, credentials) — best-effort
- Shadow branches used during sessions are local-only and never pushed
- See [docs/security-and-privacy.md](docs/security-and-privacy.md) for details

---

## Troubleshooting

| Issue | Fix |
|---|---|
| "Not a git repository" | `cd` into a git repo first |
| "Trace is disabled" | `trace enable` |
| "No rewind points" | Work with your agent, then commit |
| Shadow branch conflict | `trace clean --force` |

**Debug mode:**

```bash
TRACE_LOG_LEVEL=debug trace status
```

**Reset everything:**

```bash
trace clean --all --force
```

**Accessibility:**

```bash
export ACCESSIBLE=1   # Screen reader friendly mode
```

---

## Development

```bash
# Prerequisites: mise (https://mise.jdx.dev/)
git clone https://github.com/GrayCodeAI/trace.git
cd trace && mise install && mise trust

# Build
mise run build

# Test
mise run test          # Unit tests
mise run test:ci       # Full suite (unit + integration)

# Lint & format
mise run fmt && mise run lint
```

See [CLAUDE.md](CLAUDE.md) for architecture details.

---

## License

MIT &mdash; see [LICENSE](LICENSE)

---

<p align="center">
  Built by <a href="https://github.com/GrayCodeAI">GrayCode AI</a>
</p>
