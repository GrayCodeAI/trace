# E2E Plugin

Local plugin providing individual commands for E2E test triage and debugging.

## Commands

| Command | Description |
|---------|-------------|
| `/e2e:triage-ci` | Run failing tests locally, classify flaky vs real-bug, present findings report |
| `/e2e:debug` | Deep-dive artifact analysis for root cause diagnosis |
| `/e2e:implement` | Apply fixes from triage/debug findings, verify with E2E tests |

## Related

- Orchestrator skill: `.claude/skills/e2e/SKILL.md` (`/e2e` — runs triage-ci then implement)
