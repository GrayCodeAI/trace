# Trace Examples

Trace captures AI coding sessions as git-native checkpoints.

> **Note:** Trace is surfaced through hawk as `hawk trace ...`. The commands
> below use `trace` for brevity; prefix with `hawk` when running inside hawk.

## Basic Usage

### Enable tracing

```bash
trace enable
# Work with your AI coding assistant — checkpoints are captured automatically
trace status
```

### View captured sessions

```bash
trace session list
trace session info <session-id>
```

### Investigate what happened

```bash
trace investigate <session-id>
```

## Advanced Examples

### Rewind to a checkpoint

```bash
trace checkpoint list <session-id>
trace checkpoint rewind <session-id> --checkpoint 3
```

### Resume a session

```bash
trace session resume <session-id>
```

### Export session data

```bash
trace session export <session-id> --format json
```

### Fork a session for A/B testing

```bash
trace fork <session-id> --checkpoint 3
```

### Replay a session

```bash
trace session replay <session-id>
```

## Integration with Agents

Trace works with:
- Claude Code
- Codex CLI
- Gemini CLI
- Cursor
- GitHub Copilot CLI
- Any MCP-compatible agent

See the [main README](../README.md) for full documentation.
