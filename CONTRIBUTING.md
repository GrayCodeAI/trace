# Contributing to Trace

Thanks for helping. Trace is a small, self-hosted Git forge for teams, part
of the [GrayCode](https://graycodeai.com) tools. It is pre-1.0 alpha
software, so behaviour and data formats can still change.

## Before you start

- For a security problem, follow [SECURITY.md](SECURITY.md) instead of
  opening an issue.
- For anything larger than a small fix, open an issue first so we can agree
  on the approach.
- Read [AGENTS.md](AGENTS.md): it describes the conventions for both people
  and coding agents working in this repository.

## Development setup

You need Go (the version in `go.mod` or newer; CI uses the version pinned
in `.github/workflows/ci.yml`), Git, and an `ssh` client for the transport
tests. Trace builds for Linux and macOS.

```sh
git clone https://github.com/GrayCodeAI/trace.git
cd trace
make build        # bin/trace
make check        # gofmt check, go vet, build, race tests
```

`make help` lists all targets. The test suite starts local listeners and
runs real `git` and `ssh` commands, so it takes a few minutes.

## Pull requests

- Branch from `main` and keep each pull request to one topic.
- Use [Conventional Commits](https://www.conventionalcommits.org/) for
  commit messages and pull request titles (`fix(ssh): ...`,
  `feat(actions): ...`, `docs: ...`). Mark breaking changes with `!` and a
  `BREAKING CHANGE:` footer.
- Add or update tests for every behaviour change; a bug fix should come
  with a test that fails without it.
- Update README.md, FEATURES.md, or ARCHITECTURE.md in the same pull
  request when behaviour changes. Do not claim a capability that is not
  implemented and tested.
- Run `make check` before pushing. CI runs the same checks plus tests on
  macOS and `govulncheck`.

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE).
