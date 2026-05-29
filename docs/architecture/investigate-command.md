# `trace investigate` Command

## Overview

`trace investigate` runs a multi-agent investigation loop. Multiple AI coding agents take turns appending findings, evidence, and analysis to a shared findings document until they reach quorum on confirming (or rejecting) the investigation.

The command is experimental (gated behind `trace labs`). We are actively refining it based on user feedback.

## Quick Start

```bash
# First run — opens the agent picker to configure which agents participate
trace investigate

# Run with a seed topic
trace investigate "Why is the checkout flow failing intermittently?"

# Run from a GitHub issue
trace investigate --issue-link https://github.com/org/repo/issues/123

# Resume a previous run
trace investigate --continue <run-id>

# Edit configuration
trace investigate --edit

# Browse past investigations
trace investigate --findings
```

## How It Works

### Configuration

On first run, `trace investigate` opens a picker to select which agents participate. The selection is saved to `.trace/settings.local.json` (not committed):

```json
{
  "investigate": {
    "agents": ["claude-code", "codex"],
    "max_turns": 2,
    "quorum": 0
  }
}
```

Configuration fields:
- **agents** — ordered list of agent names to round-robin during the loop
- **max_turns** — per-agent turn budget (default 2; 0 uses default)
- **quorum** — count of `approve` stances needed to terminate (0 = all agents must approve)
- **always_prompt** — appended to every turn's composed prompt

### Investigation Loop

1. **Bootstrap** — creates a findings document and state file under `<git-common-dir>/trace-investigations/<run-id>/`
2. **Round-robin** — each configured agent takes turns reading the current findings and appending their analysis
3. **Stance** — each agent declares `approve`, `reject`, or `continue`
4. **Quorum** — when enough agents approve (or one rejects with explanation), the loop terminates
5. **Manifest** — a summary manifest is written for later browsing

### Inputs

Three mutually exclusive input modes:

| Input | Description |
|-------|-------------|
| `[seed-doc]` | Positional path to a starting findings file |
| `--issue-link <url>` | GitHub issue or PR URL (resolved via `gh`) |
| Investigation prompt | Collected by the spawn-time multipicker when no seed/issue is supplied |

### Subcommands

#### `trace investigate fix [run-id]`

Launches a coding agent with the investigation's findings as grounded context. The agent receives the full findings document and can propose code changes.

```bash
trace investigate fix                # fix the most recent investigation
trace investigate fix <run-id>       # fix a specific investigation
```

#### `trace investigate show [run-id]`

Prints a saved investigation's summary and findings.

```bash
trace investigate show               # show the most recent investigation
trace investigate show <run-id>      # show a specific investigation
```

#### `trace investigate --findings`

Browse local investigation manifests in a TUI picker.

## Storage

Investigation data lives under `<git-common-dir>/trace-investigations/`:

```
.git/
  trace-investigations/
    manifests/
      <run-id>.json          # summary manifest
    <run-id>/
      findings.md            # shared findings document
      state.json             # loop state (current agent, turn count, stances)
```

This directory is inside `.git/` (not the worktree) so investigation artifacts are never committed to the repository.

## Agent Integration

Agents participate via the `Spawner` interface. Each agent's spawner builds a command that:
1. Receives the composed prompt via argv or stdin
2. Has `TRACE_INVESTIGATE_*` environment variables set (findings path, state path, run ID)
3. Writes its findings to the shared findings document
4. Exits with a stance (approve/reject/continue) via the state file

Currently supported agents: Claude Code, Codex, Gemini CLI, OpenCode, Cursor, Copilot CLI, Factory AI Droid, Pi.

## Environment Variables

| Variable | Description |
|----------|-------------|
| `TRACE_INVESTIGATE_FINDINGS_DOC` | Absolute path to the findings document |
| `TRACE_INVESTIGATE_STATE_DOC` | Absolute path to the state file |
| `TRACE_INVESTIGATE_RUN_ID` | 12-hex-char run identifier |
| `TRACE_INVESTIGATE_TIMELINE_DOC` | Path to the timeline document |

## Examples

### Investigate a flaky test

```bash
trace investigate "The CI test TestUserLogin is flaky — it passes locally but fails ~30% of the time in CI. Investigate root cause."
```

### Investigate from a GitHub issue

```bash
trace investigate --issue-link https://github.com/myorg/myrepo/issues/456
```

### Multi-agent with custom config

```bash
# Override agents and turn budget
trace investigate --agents claude-code,codex --max-turns 3 --quorum 2 "Why does the build take 20 minutes?"
```

### Resume and fix

```bash
# Resume a paused investigation
trace investigate --continue abc123def456

# Launch a coding agent with the findings
trace investigate fix abc123def456
```

## Relationship to `trace review`

| Aspect | `trace review` | `trace investigate` |
|--------|---------------|-------------------|
| Purpose | Code review on a branch | Open-ended code investigation |
| Agents | Configured per review skill | Round-robin from settings |
| Output | Review findings on checkpoint | Findings document + manifest |
| Quorum | N/A (single pass) | Multi-turn with approval |
| Storage | Checkpoint metadata | `.git/trace-investigations/` |
