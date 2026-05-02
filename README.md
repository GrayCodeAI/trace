# Trace CLI

Trace hooks into your Git workflow to capture AI agent sessions as you work. Sessions are indexed alongside commits, creating a searchable record of how code was written in your repo.

With Trace, you can:

- **Understand why code changed** — see the full prompt/response transcript and files touched
- **Recover instantly** — rewind to a known-good checkpoint when an agent goes sideways and resume seamlessly
- **Keep Git history clean** — preserve agent context on a separate branch
- **Onboard faster** — show the path from prompt → change → commit
- **Maintain traceability** — support audit and compliance requirements when needed

## Why Trace

- **Understand why code changed, not just what** — Transcripts, prompts, files touched, token usage, tool calls, and more are captured alongside every commit.
- **Rewind and resume from any checkpoint** — Go back to any previous agent session and pick up exactly where you or a coworker left off.
- **Full context preserved and searchable** — A versioned record of every AI interaction tied to your git history, with nothing lost.
- **Zero context switching** — Git-native, two-step setup, works with Claude Code, Codex, Gemini, and more.

## Table of Contents

- [Why Trace](#why-trace)
- [Quick Start](#quick-start)
- [Typical Workflow](#typical-workflow)
- [Key Concepts](#key-concepts)
  - [How It Works](#how-it-works)
  - [Strategy](#strategy)
- [Local Device Auth Testing](#local-device-auth-testing)
- [Commands Reference](#commands-reference)
- [Configuration](#configuration)
- [Security & Privacy](#security--privacy)
- [Troubleshooting](#troubleshooting)
- [Development](#development)
- [Getting Help](#getting-help)
- [License](#license)

## Requirements

- Git
- macOS, Linux or Windows
- [Supported agent](#agent-hook-configuration) installed and authenticated

## Quick Start

```bash
# Install stable via Homebrew
brew tap GrayCodeAI/tap
brew install --cask trace

# Or install nightly via Homebrew
brew tap GrayCodeAI/tap
brew install --cask trace@nightly

# Or install stable via install.sh
curl -fsSL https://trace.io/install.sh | bash

# Or install nightly via install.sh
curl -fsSL https://trace.io/install.sh | bash -s -- --channel nightly

# Or install stable via Scoop (Windows)
scoop bucket add trace https://github.com/GrayCodeAI/scoop-bucket.git
scoop install trace/cli

# Or install via Go (development/manual setup)
go install github.com/GrayCodeAI/trace/cmd/trace@latest

# Linux: Add Go binaries to PATH (add to ~/.zshrc or ~/.bashrc if not already configured)
export PATH="$HOME/go/bin:$PATH"

# Enable in your project
cd your-project && trace enable

# Check status
trace status
```

After the initial setup, use `trace agent` to add or remove agents, `trace configure` to update non-agent settings, and `trace enable` / `trace disable` to toggle Trace on or off.

## Release Channels

Trace currently ships two release channels:

- `stable`: recommended for most users. Stable releases change less often and are the default for Homebrew, Scoop, and `install.sh`.
- `nightly`: prerelease builds for users who want the latest changes earlier. Nightlies are published more frequently and may include newer, less-proven changes than stable.

How to use each channel:

- Homebrew stable: `brew install --cask trace`
- Homebrew nightly: `brew install --cask trace@nightly`
- `install.sh` stable: `curl -fsSL https://trace.io/install.sh | bash`
- `install.sh` nightly: `curl -fsSL https://trace.io/install.sh | bash -s -- --channel nightly`
- Scoop: currently supports `stable` only via `scoop install trace/cli`

## Typical Workflow

### 1. Enable Trace in Your Repository

```
trace enable
```

On a repo that has not been enabled yet, `trace enable` runs the initial enable flow: it creates Trace settings, installs git hooks, and prompts you to choose which agent hooks to install. To enable a specific agent non-interactively, use `trace enable --agent <name>` (for example, `trace enable --agent cursor`).

After setup:

- Use `trace enable` to turn Trace back on if the repo is currently disabled.
- Use `trace agent` to add or remove agents.
- Use `trace configure` to update non-agent settings (telemetry, hooks, checkpoint remote, summary provider).

The hooks capture session data as you work. Checkpoints are created when you or the agent make a git commit. Your code commits stay clean, Trace never creates commits on your active branch. All session metadata is stored on a separate `trace/checkpoints/v1` branch.

### 2. Work with Your AI Agent

Just use one of your AI agents as before. Trace runs in the background, tracking your session:

```
trace status  # Check current session status anytime
```

### 3. Rewind to a Previous Checkpoint

If you want to undo some changes and go back to an earlier checkpoint:

```
trace checkpoint rewind
```

This shows all available checkpoints in the current session. Select one to restore your code to that exact state.

### 4. Resume a Previous Session

To restore the latest checkpointed session metadata for a branch:

```
trace session resume <branch>
```

Trace checks out the branch, restores the latest checkpointed session metadata (one or more sessions), and prints command(s) to continue.

### 5. Disable Trace (Optional)

```
trace disable
```

Removes the git hooks. Your code and commit history remain untouched.

## Key Concepts

### Sessions

A **session** represents a complete interaction with your AI agent, from start to finish. Each session captures all prompts, responses, files modified, and timestamps.

**Session ID format:** `YYYY-MM-DD-<UUID>` (e.g., `2026-01-08-abc123de-f456-7890-abcd-ef1234567890`)

Sessions are stored separately from your code commits on the `trace/checkpoints/v1` branch.

### Checkpoints

A **checkpoint** is a snapshot within a session that you can rewind to—a "save point" in your work.

Checkpoints are created when you or the agent make a git commit. **Checkpoint IDs** are 12-character hex strings (e.g., `a3b2c4d5e6f7`).

### How It Works

```
Your Branch                    trace/checkpoints/v1
     │                                  │
     ▼                                  │
[Base Commit]                           │
     │                                  │
     │  ┌─── Agent works ───┐           │
     │  │  Step 1           │           │
     │  │  Step 2           │           │
     │  │  Step 3           │           │
     │  └───────────────────┘           │
     │                                  │
     ▼                                  ▼
[Your Commit] ─────────────────► [Session Metadata]
     │                           (transcript, prompts,
     │                            files touched)
     ▼
```

Checkpoints are saved as you work. When you commit, session metadata is permanently stored on the `trace/checkpoints/v1` branch and linked to your commit.

### Strategy

Trace uses a manual-commit strategy that keeps your git history clean:

- **No commits on your branch** — Trace never creates commits on the active branch
- **Safe on any branch** — works on main, master, and feature branches alike
- **Non-destructive rewind** — restore files from any checkpoint without altering commit history
- **Metadata stored separately** — all session data lives on the `trace/checkpoints/v1` branch

### Git Worktrees

Trace works seamlessly with [git worktrees](https://git-scm.com/docs/git-worktree). Each worktree has independent session tracking, so you can run multiple AI sessions in different worktrees without conflicts.

### Concurrent Sessions

Multiple AI sessions can run on the same commit. If you start a second session while another has uncommitted work, Trace warns you and tracks them separately. Both sessions' checkpoints are preserved and can be rewound independently.

## Local Device Auth Testing

If you're working on the CLI device auth flow against a local `trace.io` checkout:

```bash
# In your app repo
cd ../trace.io-1
mise run dev

# In this repo, point the CLI at the local API
cd ../cli
export TRACE_API_BASE_URL=http://localhost:8787

# Run the smoke test
./scripts/local-device-auth-smoke.sh
```

Useful commands while developing:

```bash
# Run the login flow against a local server (prompts to press Enter before opening the browser)
go run ./cmd/trace login --insecure-http-auth

# Run the focused integration coverage for login
go test -tags=integration ./cmd/trace/cli/integration_test -run TestLogin
```

## Commands Reference

| Command          | Description                                                                                       |
| ---------------- | ------------------------------------------------------------------------------------------------- |
| `trace clean`   | Clean up session data and orphaned Trace data (use `--all` for repo-wide cleanup)                |
| `trace agent`   | Add, remove, or list agent integrations for the current repository                                |
| `trace configure` | Update non-agent settings (telemetry, git hook, strategy options, summary provider)            |
| `trace disable` | Remove Trace hooks from repository                                                               |
| `trace doctor`  | Fix or clean up stuck sessions                                                                    |
| `trace enable`  | Enable Trace in your repository                                                                  |
| `trace checkpoint`        | List, explain, rewind, and search checkpoints                                           |
| `trace checkpoint explain` | Explain a session, commit, or checkpoint                                               |
| `trace checkpoint rewind` | Rewind to a previous checkpoint                                                         |
| `trace login`   | Authenticate the CLI with Trace device auth                                                      |
| `trace session` | View and manage agent sessions tracked by Trace                                                  |
| `trace session resume`    | Switch to a branch, restore latest checkpointed session metadata, and show command(s) |
| `trace session attach`    | Attach to a previously detached session                                                |
| `trace status`  | Show current session info                                                                         |
| `trace doctor trace` | Show hook performance traces                                                                 |
| `trace version` | Show Trace CLI version                                                                           |

### `trace enable` Flags

| Flag                                        | Description                                                                                                       |
| ------------------------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| `--agent <name>`                            | AI agent to install hooks for: `claude-code`, `codex`, `gemini`, `opencode`, `cursor`, `factoryai-droid`, or `copilot-cli` |
| `--force`, `-f`                             | Force reinstall hooks (removes existing Trace hooks first)                                                       |
| `--local`                                   | Write settings to `settings.local.json` instead of `settings.json`                                                |
| `--project`                                 | Write settings to `settings.json` even if it already exists                                                       |
| `--skip-push-sessions`                      | Disable automatic pushing of session logs on git push                                                             |
| `--checkpoint-remote <provider:owner/repo>` | Push checkpoint branches to a separate repo (e.g., `github:org/checkpoints-repo`)                                 |
| `--telemetry=false`                         | Disable anonymous usage analytics                                                                                 |

**Examples:**

```
# First-time setup with a specific agent
trace enable --agent claude-code

# Re-enable a disabled repo
trace enable

# Re-enable and refresh hooks
trace enable --force

# Save settings locally (not committed to git)
trace enable --local
```

`trace enable` is primarily for turning Trace on. On an unconfigured repo it will also bootstrap setup. Use `trace agent` for adding or removing agents, and `trace configure` for non-agent settings.

### `trace configure`

Use `trace configure` to update non-agent settings on a repo that's already set up. Agent installation lives under `trace agent`.

Typical uses:

- Toggle telemetry
- Reinstall the Trace git hook (`--force`, `--absolute-git-hook-path`)
- Update strategy options such as `--checkpoint-remote` or `--skip-push-sessions`
- Pick a summary provider for `trace explain --generate`

**Examples:**

```bash
# Show help and the hint pointing to 'trace agent'
trace configure

# Opt out of telemetry
trace configure --telemetry=false

# Reinstall the Trace git hook with an absolute binary path
trace configure --absolute-git-hook-path

# Update strategy options on an existing repo
trace configure --checkpoint-remote github:myorg/checkpoints-private

# Add or remove an agent
trace agent add claude-code
trace agent remove claude-code
```

## Configuration

Trace uses two configuration files in the `.trace/` directory:

### settings.json (Project Settings)

Shared across the team, typically committed to git:

```json
{
  "enabled": true
}
```

### settings.local.json (Local Settings)

Personal overrides, gitignored by default:

```json
{
  "enabled": false,
  "log_level": "debug"
}
```

### Configuration Options

| Option                               | Values                                       | Description                                             |
| ------------------------------------ | -------------------------------------------- | ------------------------------------------------------- |
| `enabled`                            | `true`, `false`                              | Enable/disable Trace                                   |
| `log_level`                          | `debug`, `info`, `warn`, `error`             | Logging verbosity                                       |
| `strategy_options.push_sessions`     | `true`, `false`                              | Auto-push `trace/checkpoints/v1` branch on git push    |
| `strategy_options.checkpoint_remote` | `{"provider": "github", "repo": "org/repo"}` | Push checkpoint branches to a separate repo (see below) |
| `strategy_options.summarize.enabled` | `true`, `false`                              | Auto-generate AI summaries at commit time               |
| `telemetry`                          | `true`, `false`                              | Send anonymous usage statistics to Posthog              |

### Agent Hook Configuration

Each agent stores its hook configuration in its own directory. When you run `trace enable`, hooks are installed in the appropriate location for each selected agent:

| Agent            | Hook Location                 | Format            |
| ---------------- | ----------------------------- | ----------------- |
| Claude Code      | `.claude/settings.json`       | JSON hooks config |
| Codex            | `.codex/hooks.json`           | JSON hooks config |
| Copilot CLI      | `.github/hooks/trace.json`   | JSON hooks config |
| Cursor           | `.cursor/hooks.json`          | JSON hooks config |
| Factory AI Droid | `.factory/settings.json`      | JSON hooks config |
| Gemini CLI       | `.gemini/settings.json`       | JSON hooks config |
| OpenCode         | `.opencode/plugins/trace.ts` | TypeScript plugin |

You can enable multiple agents at the same time — each agent's hooks are independent. Trace detects which agents are active by checking for installed hooks, not by a setting in `settings.json`.

### Checkpoint Remote

By default, Trace pushes `trace/checkpoints/v1` to the same remote as your code. If you want to push checkpoint data to a separate repo (e.g., a private repo for public projects), configure `checkpoint_remote` with a structured provider and repo:

```json
{
  "strategy_options": {
    "checkpoint_remote": {
      "provider": "github",
      "repo": "myorg/checkpoints-private"
    }
  }
}
```

Or via the CLI:

```bash
trace enable --checkpoint-remote github:myorg/checkpoints-private
```

Trace derives the git URL automatically using the same protocol (SSH or HTTPS) as your push remote. It will:

- Fetch the checkpoint branch locally if it exists on the remote but not locally (one-time)
- Push `trace/checkpoints/v1` to the checkpoint repo instead of your default push remote
- Skip pushing if a fork is detected (push remote owner differs from checkpoint repo owner)
- If the remote is unreachable, warn and continue without blocking your main push

#### `TRACE_CHECKPOINT_TOKEN`

`TRACE_CHECKPOINT_TOKEN` allows you to provide a dedicated token for checkpoint repository operations, without modifying the credentials used for your primary repository.

When this environment variable is set, Trace behaves as follows:

- Injects the token into HTTPS Git operations used for checkpoint fetch and push
- If `checkpoint_remote` is configured:
  - Prefers an HTTPS URL for the checkpoint remote when a token is present, even if the repository’s `origin` uses SSH
- If `checkpoint_remote` is not configured:
  - Falls back to using the default `origin` remote
- If `checkpoint_remote` configuration cannot be loaded:
  - Falls back to `origin`
  - If `origin` is a valid SSH or HTTPS Git remote, Trace converts it to an HTTPS URL to enable token-based authentication

### Auto-Summarization

When enabled, Trace automatically generates AI summaries for checkpoints at commit time. Summaries capture intent, outcome, learnings, friction points, and open items from the session.

```json
{
  "strategy_options": {
    "summarize": {
      "enabled": true
    }
  }
}
```

**Requirements:**

- Claude CLI must be installed and authenticated (`claude` command available in PATH)
- Summary generation is non-blocking: failures are logged but don't prevent commits

**Note:** Currently uses Claude CLI for summary generation. Other AI backends may be supported in future versions.

### Settings Priority

Local settings override project settings field-by-field. When you run `trace status`, it shows both project and local (effective) settings.

### Agent-Specific Steps & Limitations

- When enabling Trace for Codex, the command will also create or update `.codex/config.toml` with `codex_hooks = true` to enable Codex hooks. If you configure Codex manually, make sure this flag is set in your `.codex/config.toml`. Or select Codex from the interactive agent picker when running `trace enable`.
- Trace supports Cursor IDE and Cursor Agent CLI tool, but `trace rewind` is not available at this time. Other commands (`doctor`, `status` etc.) work the same as all other agents.
- Trace supports Copilot CLI, but not Copilot in VS Code, in other IDEs, or on github.com.

## Security & Privacy

**Your session transcripts are stored in your git repository** on the `trace/checkpoints/v1` branch. If your repository is public, this data is visible to anyone.

Trace automatically redacts detected secrets (API keys, tokens, credentials) when writing to `trace/checkpoints/v1`, but redaction is best-effort. Temporary shadow branches used during a session may contain unredacted data and should not be pushed. See [docs/security-and-privacy.md](docs/security-and-privacy.md) for details.

## Troubleshooting

### Common Issues

| Issue                    | Solution                                                |
| ------------------------ | ------------------------------------------------------- |
| "Not a git repository"   | Navigate to a Git repository first                      |
| "Trace is disabled"     | Run `trace enable`                                     |
| "No rewind points found" | Work with your configured agent and commit your changes |
| "shadow branch conflict" | Run `trace clean --force`                              |

### SSH Authentication Errors

If you see an error like this when running `trace resume`:

```
Failed to fetch metadata: failed to fetch trace/checkpoints/v1 from origin: ssh: handshake failed: ssh: unable to authenticate, attempted methods [none publickey], no supported methods remain
```

This is a [known issue with go-git's SSH handling](https://github.com/go-git/go-git/issues/411). Fix it by adding GitHub's host keys to your known_hosts file:

```
ssh-keyscan -t rsa github.com >> ~/.ssh/known_hosts
ssh-keyscan -t ecdsa github.com >> ~/.ssh/known_hosts
```

### Debug Mode

```
# Via environment variable
TRACE_LOG_LEVEL=debug trace status

# Or via settings.local.json
{
  "log_level": "debug"
}
```

### Cleaning Up State

```
# Clean session data for current commit
trace clean --force

# Clean all orphaned data across the repository
trace clean --all --force

# Disable and re-enable
trace disable && trace enable --force
```

### Accessibility

For screen reader users, enable accessible mode:

```
export ACCESSIBLE=1
trace enable
```

This uses simpler text prompts instead of interactive TUI elements.

## Development

This project uses [mise](https://mise.jdx.dev/) for task automation and dependency management.

### Prerequisites

- [mise](https://mise.jdx.dev/) - Install with `curl https://mise.run | sh`

### Getting Started

```
# Clone the repository
git clone <repo-url>
cd cli

# Install dependencies (including Go)
mise install

# Trust the mise configuration (required on first setup)
mise trust

# Build the CLI
mise run build
```

### Dev Container

The repo includes a `.devcontainer/` configuration that installs the system packages used by local development and CI (`git`, `tmux`, `gnome-keyring`, etc) and then bootstraps the repo's `mise` toolchain.

Open the folder in a Dev Container, or start it from the `devcontainer` CLI as follows:

```bash
devcontainer up --workspace-folder .
devcontainer exec --workspace-folder . bash -lc '.devcontainer/run-with-keyring.sh'
```

The container's `postCreateCommand` runs `mise trust --yes && mise install`, so Go, `golangci-lint`, `gotestsum`, `shellcheck`, and the canary E2E helper binaries are ready after creation. Use `.devcontainer/run-with-keyring.sh <command>` for commands that touch the Linux keyring, including `mise run test:ci`.

If `TRACE_DEVCONTAINER_KEYRING_PASSWORD` is set in the environment, `.devcontainer/run-with-keyring.sh` uses that value to unlock the keyring non-interactively. If it is unset, the script generates a random password for the session automatically.

### Common Tasks

```
# Run tests
mise run test

# Run integration tests
mise run test:integration

# Run all tests (unit + integration, CI mode)
mise run test:ci

# Lint the code
mise run lint

# Format the code
mise run fmt
```

## Getting Help

```
trace --help              # General help
trace <command> --help    # Command-specific help
```

- **GitHub Issues:** Report bugs or request features at https://github.com/GrayCodeAI/trace/issues
- **Contributing:** See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines

## License

MIT License - see [LICENSE](LICENSE) for details.
