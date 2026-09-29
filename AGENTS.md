# AGENTS.md

Guidance for anyone, human or coding agent, changing this repository.
This file is the only agent-instruction file here: do not add `CLAUDE.md`,
`GEMINI.md`, `.cursorrules`, or similar files, and delete them if a tool
generates one. Put shared guidance in this file instead.

## What Trace is

Trace is a self-hosted Git forge for small teams with signed agent history,
part of the GrayCode tools (<https://github.com/GrayCodeAI>). It is pre-1.0
alpha software. One Go binary (`cmd/trace`, package `main`) serves the web
UI, the JSON API, Git smart HTTP and SSH, and runs the local CI runner.

## Layout

- `cmd/trace/*.go`: all code, grouped by feature (`actions.go`, `oidc.go`,
  `ssh.go`, and so on), with tests beside it in `*_test.go`.
- `cmd/trace/*.html`, `cmd/trace/assets/`: embedded templates and assets.
- Runtime state lives in a data directory (default `./data`, ignored by
  Git): bare repositories in `repos/OWNER/NAME.git`, JSON stores such as
  `users.json` and `issues.json`, the append-only `audit.jsonl`, and
  per-repository directories such as `lfs/OWNER/NAME`.
- README.md (operator guide), ARCHITECTURE.md (design and security
  boundary), FEATURES.md (capability matrix): keep them true.

## Commands

Run Go with `GOWORK=off` (the Makefile sets it).

```sh
make build       # bin/trace
make check       # gofmt check, go vet, build, race tests (the local gate)
make test        # tests without the race detector
make vulncheck   # govulncheck (needs network)
```

The tests start local listeners and run real `git` and `ssh`; they need no
network and write only to `t.TempDir()`.

## Code conventions

- Standard library first. The only direct dependency is
  `golang.org/x/crypto`; discuss any new dependency in an issue first.
- Format with gofmt. `go vet` must stay clean.
- JSON stores follow one pattern: take the store's `flock` on
  `data/.<name>.lock`, load, change, write to a temporary file, fsync,
  rename. Keep writes atomic and never rewrite `audit.jsonl`.
- Per-repository files live under `data/KIND/OWNER/NAME` so a transfer can
  move them and a delete can remove them.
- Run external programs with `exec.Command` and separate arguments; never
  build a shell command line from user or repository input. Anything
  written into a generated script (such as the pre-receive hook) must be
  validated and quoted.
- Treat repository content (workflow files, HTML, pointers) as untrusted.
  Authorization checks use `canRead`/`canWrite`/`canMaintain`, not raw
  role strings.
- Never read, print, log, or commit secrets: the data directory's
  `admin-token`, token hashes, `secrets.json`, `oidc.json`, or SSH and node
  keys. Tests create their own throwaway data directories.

## Changes and evidence

- One concern per commit, [Conventional Commits](https://www.conventionalcommits.org/)
  messages, branch from `main`, never force-push a shared branch.
- Every behaviour change needs a test; a bug fix needs a test that fails
  without the fix. Do not weaken, skip, or delete tests to get a green run.
- Update the docs in the same change when behaviour changes. Describe only
  what the code does and the tests verify; mark missing work as missing.
- Report verification exactly: the commands you ran and their real
  results. Do not claim a check passed if you did not run it, and say so
  when something could not be run (for example, a macOS-only path on
  Linux). When citing facts, separate what you observed in code or output
  from what a document states.
