# Changelog

Notable changes to Trace are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions
follow [Semantic Versioning](https://semver.org/) (before 1.0, a minor
version may contain breaking changes).

## [Unreleased]

## [0.0.1] - not yet released

First public version of Trace at <https://github.com/GrayCodeAI/trace>. The
project was previously developed privately under the names MeshGit and
Refweave.

### Added

- Module path `github.com/GrayCodeAI/trace`, a `VERSION` file, and a
  `trace version` command.
- Makefile targets for building, testing, vetting, formatting checks, and
  vulnerability scanning.
- CI on Linux and macOS with a pinned Go toolchain, race tests, job
  timeouts, and `govulncheck`.
- A tag-triggered release workflow that publishes Linux and macOS
  binaries (amd64, arm64) with a SHA-256 `checksums.txt`. Binaries are not
  signed.
- SECURITY.md, CONTRIBUTING.md, CODE_OF_CONDUCT.md, and AGENTS.md.

### Security

- Upgraded `golang.org/x/crypto` to v0.56.0 (GO-2026-6354, GO-2026-6355:
  SSH channel denial of service).

### Documentation

- README, ARCHITECTURE, and FEATURES now match the code: pull-request
  merges by admins and maintainers in the browser, the basic npm and PyPI
  endpoints, the `maintain` grant in `trace user grant`, and a refreshed,
  dated competitor comparison.
