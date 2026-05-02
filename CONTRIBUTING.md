# Contributing

Thanks for your interest in Trace! We welcome contributions of all kinds.

---

## Before You Start

1. **Open an issue** describing your idea or the bug you found
2. **Wait for maintainer feedback** — we may have context that saves you time
3. **Get the green light**, then start coding

This avoids wasted effort on work that doesn't align with the project direction.

---

## Getting Started

### Prerequisites

- Go 1.26+
- [mise](https://mise.jdx.dev/) — `curl https://mise.run | sh`

### Setup

```bash
git clone https://github.com/GrayCodeAI/trace.git
cd trace
mise trust && mise install
go mod download
mise run build
mise run test
```

### Good First Issues

Look for issues labeled `good-first-issue`. Great starting points:

- Documentation improvements
- Test coverage
- Small bug fixes

---

## Development Workflow

```bash
# Create a branch
git checkout -b feature/your-feature

# Make changes, then verify
mise run fmt          # Format
mise run lint         # Lint
mise run test         # Test

# Commit
git commit -m "Add: description of your change"
```

### Code Style

- Standard Go idioms
- All errors handled explicitly
- `gofmt` and `golangci-lint` must pass
- See [CLAUDE.md](CLAUDE.md) for architecture patterns

### Testing

```bash
mise run test              # Unit tests
mise run test:integration  # Integration tests
mise run test:ci           # Full CI suite
```

---

## Pull Requests

### Checklist

- [ ] Related issue exists and is approved
- [ ] `mise run lint` passes
- [ ] `mise run test` passes
- [ ] New code has tests
- [ ] PR description explains *what* and *why*

### Process

1. Push your branch
2. Open a PR against `main`
3. Link the related issue
4. Address review feedback
5. Maintainer merges

---

## Security

Found a vulnerability? **Do not** open a public issue.

Email [security@graycode.ai](mailto:security@graycode.ai) instead. See [SECURITY.md](SECURITY.md).

---

## Community

- [GitHub Issues](https://github.com/GrayCodeAI/trace/issues) — bugs & features
- [Discord](https://discord.gg/jZJs3Tue4S) — questions & discussion

Please read our [Code of Conduct](CODE_OF_CONDUCT.md) before participating.

---

## Resources

| Document | Purpose |
|---|---|
| [README.md](README.md) | Setup & usage |
| [CLAUDE.md](CLAUDE.md) | Architecture & internals |
| [AGENTS.md](AGENTS.md) | Agent integration guide |
| [SECURITY.md](SECURITY.md) | Vulnerability reporting |

---

Thank you for helping make Trace better.
