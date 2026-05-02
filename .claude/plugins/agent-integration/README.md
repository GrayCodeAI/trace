# Agent Integration Plugin

Local plugin providing individual commands for the agent integration workflow.

## Commands

| Command | Description |
|---------|-------------|
| `/agent-integration:research` | Assess a new agent's hook/lifecycle compatibility |
| `/agent-integration:write-tests` | Generate E2E test suite for the agent |
| `/agent-integration:implement` | Build the Go agent package via TDD |

## Related

- Orchestrator skill: `.claude/skills/agent-integration/SKILL.md` (`/agent-integration` — runs all 3 phases)
