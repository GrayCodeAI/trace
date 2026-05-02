# Exploratory E2E Tests

Tests in this directory are **not run by CI**. They exist for:

- Validating bug fixes before the fix is merged (reference the Linear issue)
- Prototyping new test scenarios before promoting to `tests/`
- Reproducing reported issues

## Running

    go test -tags=e2e -count=1 -timeout=30m ./exploratory/...

## Promoting to blessed

Move the file to `tests/` — no other changes needed.
