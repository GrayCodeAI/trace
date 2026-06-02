# Trace Examples

Trace captures AI coding sessions as git-native checkpoints.

## Basic Usage

### Start a tracing session

```bash
trace start
# Work with your AI coding assistant
trace stop
```

### View captured sessions

```bash
trace list
trace show <session-id>
```

### Investigate what happened

```bash
trace investigate <session-id>
```

## Advanced Examples

### Rewind to a checkpoint

```bash
trace checkpoints <session-id>
trace rewind <session-id> --checkpoint 3
```

### Resume a session

```bash
trace resume <session-id>
```

### Export session data

```bash
trace export <session-id> --format json
```

## Integration with Agents

Trace works with:
- Claude Code
- Cursor
- GitHub Copilot
- Any MCP-compatible agent

See the [main README](../README.md) for full documentation.
