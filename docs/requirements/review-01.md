# Review 01

> Status: pending-dev
> Date: 2026-05-22
> Reviewer: Code Review Agent
> Verdict: APPROVE (with minor suggestions)

## Scope

PR #14: "feat: concurrent session safety (CAS + per-shadow flock) + trail UX + supporting fixes"
11 commits, +4743 / -422 lines across 42 files.

---

## 1. Core Safety: `shadow_ref.go` + `temporary.go` CAS/Flock

### Correctness

The CAS + flock design is sound and correctly eliminates the race condition described in the PR.

**CAS loop** (`temporary.go:115-165`): The `for attempt := range shadowRefMaxRetries` loop correctly:
- Re-reads the parent hash via `getOrCreateShadowBranch` on each attempt (fresh state after CAS loss)
- Builds a new tree from disk each retry (not reusing a stale tree)
- Calls `tryDeleteLooseObject` on CAS failure to avoid leaking dangling commits
- Logs and returns a wrapped error on exhaustion

**`casUpdateShadowBranchRef`** (`shadow_ref.go:69-99`): Correct use of `git update-ref <ref> <new> <old>` with:
- Proper ZeroHash handling for first-checkpoint (uses `strings.Repeat("0", newHash.HexSize())` to match SHA-1/SHA-256 width)
- `LC_ALL=C` / `LANG=C` on the env to prevent locale-dependent stderr matching failures
- Error pattern matches both "cannot lock ref" and "but expected" strings

**`withShadowBranchFlock`** (`shadow_ref.go:140-151`): Correct `defer release()` placement. Lock files under `<git-common-dir>/trace-shadow-locks/` with slash-escaped branch names.

**Layering**: Per-session gate (`sessionMutationGate`) serializes same-goroutine reentrant calls. Per-shadow flock serializes all writers to the same shadow branch. CAS is the final safety net against rare external `git update-ref` callers. This three-layer defense is correct and the layers compose without deadlock risk because:
- The gate is per-session-ID, the flock is per-shadow-branch (same session always maps to same shadow)
- Lock acquisition order is always gate-then-flock, never reversed
- Reentrant gate calls skip the flock (nested calls reuse outer's state pointer)

### Suggestion (Consider)

**`shadow_ref.go:32` -- jitter bound is small**: `shadowRefMaxJitter = 8ms` with the `attempt > 4` doubling gives a worst-case sleep of ~16ms. Under 8 concurrent goroutines all hitting the same shadow branch, the flock serializes them so only one CAS attempt runs at a time anyway. The jitter is purely a safety net against external writers. This is fine as-is, but worth noting: if an external `git update-ref` is the persistent contention source, 16 retries x 16ms max = ~256ms total budget before giving up. That seems adequate for a CLI tool.

---

## 2. Concurrent Test: `manual_commit_concurrent_test.go`

The test is well-designed and exercises the exact failure scenario.

**Strengths**:
- 8 goroutines x 4 checkpoints = 32 total checkpoints on one shadow branch
- Each goroutine owns its own strategy + repo handle (mirrors production where each hook is a fresh process)
- Barrier pattern (`close(start)`) ensures all goroutines start simultaneously to maximize contention
- Uses `writeFileForRaceTest` instead of `testutil.WriteFile` (avoids `t.Fatalf` from sub-goroutines)
- Three assertion layers: (1) no errors, (2) per-session StepCount matches, (3) recursive tree walk for internal consistency
- Single-branch assertion verifies all sessions hash to the same shadow branch

**One nit**: `walkShadowBranchAssertConsistent` walks `commit.ParentHashes[0]` only. If a merge commit ever appeared on the shadow branch (unlikely but possible with an external writer), it would miss the second parent. This is acceptable given shadow branches never have merges in practice.

---

## 3. Trail UX: `trail_cmd.go` + `trail_cmd_test.go`

**`trail_cmd.go`**: Well-structured command with:
- `trailListOptions` struct avoids positional arg sprawl
- `--all` / `-a` flag maps to `trailListStatusAny` cleanly
- `filterTrailsByAuthor` is case-insensitive (correct for GitHub logins)
- `printTrailList` groups by status when multiple statuses are present, with an "Other" bucket for unknown server-side statuses
- `checkTrailResponse` appends a `trace login` hint on 401/403 (good UX)

**`trail_cmd_test.go`**: Good coverage of the display/filter helpers:
- Author filtering (case-insensitive)
- Status parsing (comma-separated, invalid, "any" sentinel)
- Display shape tests (singular/plural, grouped/flat, author shown/hidden)
- `fetchCurrentUserLogin` with fake runner (error wrapping, empty login rejection)

**`pushBranchToOrigin`** (`trail_cmd.go:999-1007`): Uses `--no-verify` to skip pre-push hooks during trail creation. Reasonable -- trail creation should not trigger user hooks.

---

## 4. Supporting Code

### Codex PostToolUse (`lifecycle.go`, `codex/lifecycle.go`)

**`handleLifecycleToolUse`** correctly:
- No-ops on empty SessionID or zero file count
- Delegates to `RecordFilesTouched` which uses `MutateSessionState` (proper locking)

**`parseApplyPatchFiles`**: Clean line-by-line parser for Codex's `*** Add/Update/Delete File:` markers. Tests cover all operations, empty patch, only-adds, and no-markers.

### Session State (`session_state.go`)

**`MutateSessionState`** reentrancy via goroutine ID:
- `goroutineID()` uses `runtime.Stack` -- not officially stable but standard Go practice
- `ErrMutationSkip` sentinel avoids unnecessary saves (good optimization)
- `ErrStateNotFound` properly surfaces when state file is missing

**`RecordFilesTouched`**: Returns nil on `ErrStateNotFound` (silent no-op when session not yet initialized). Correct behavior for mid-turn hooks that fire before `TurnStart`.

### Flock (`flock_unix.go`, `flock_windows.go`)

Minimal and correct. Unix uses `syscall.Flock(LOCK_EX)`, Windows uses `LockFileEx(LOCKFILE_EXCLUSIVE_LOCK)`. Both properly close the file descriptor on release.

### Settings (`settings.go`)

New `LoadProjectRaw`, `LoadLocalRaw`, `Load/SaveClonePreferences` helpers. Standard file I/O with validation. No concerns.

### Redact (`custom.go`, `packs.go`)

Custom redaction rules and YAML rule packs. Well-structured with proper validation. Tests are comprehensive.

---

## Findings

### Important (Should Fix)

**`temporary.go:116` / `shadow_ref.go` -- go-git ref read may serve stale cached data after CAS update**:

Inside the CAS retry loop, `getOrCreateShadowBranch` calls `s.repo.Reference(refName, true)`. The `true` argument forces dereference (symbolic -> resolved), but go-git's `Storer` may still return a cached hash from a previous read in the same `GitStore` instance. Within the flock, only one goroutine's CAS attempt runs at a time, so the ref can only move forward due to an external writer. If go-git caches the old ref hash, the next retry would read the same stale parent, build a new tree, create a new commit, and the CAS would fail again (correctly). This wastes one retry but does not cause data loss. **Impact: low** -- the 16-retry budget absorbs it. But if you want belt-and-suspenders, call `s.repo.Storer.SetReference()` to invalidate the cached ref after a CAS failure, or shell out to `git rev-parse refs/heads/<branch>` to get a guaranteed-fresh hash.

**`shadow_ref.go:129` -- branch name sanitization is minimal**:

`strings.ReplaceAll(branchName, "/", "_")` handles the `trace/<hash>` convention, but if `branchName` ever contains other filesystem-special characters (`:`, `\` on Windows, null bytes), the lock file path could be problematic. Shadow branch names are currently always `trace/<hex>-<hex>` so this is safe in practice, but a `strings.Map` that strips all non-alphanumeric/dash/underscore characters would be more defensive.

### Suggestions (Consider)

**`temporary.go:111` -- `withShadowBranchFlock` takes a `func() error` but `WriteTemporaryTask` could benefit from returning a value via closure**:

The current pattern captures `result` / `resultHash` via closure variables, which works fine. But if you ever need the flock helper to return a value directly, a generic version `withShadowBranchFlock[T]` would be cleaner. Low priority -- the current pattern is idiomatic Go.

**`trail_cmd.go:897-931` -- `findTrail` scans the full list to find one trail**:

For repos with many trails, `findTrailByBranch` and `findTrailByNumber` both fetch the full list and linear-scan. If the API ever supports server-side filtering by branch/number, that would be more efficient. Current implementation is fine for expected trail counts.

**`session_state.go:411-429` -- `goroutineID()` uses `runtime.Stack` parsing**:

This is a well-known Go pattern but depends on the `goroutine N [running]:` format, which is not part of Go's stability guarantee. If Go ever changes this format, the gate would silently fall back to returning -1, which would cause the outer call to always be treated as non-reentrant (safe but loses the reentrancy optimization). Consider adding a comment noting this fragility.

### Praise

- **Defense in depth**: The three-layer locking (per-session gate, per-shadow flock, CAS) is thoughtfully designed and the layers compose cleanly without deadlock risk.
- **Fail-closed security**: `filterGitIgnoredFiles` returns nil on any error, preventing gitignored secrets from leaking into shadow branches.
- **Test quality**: The concurrent test is production-grade -- barrier synchronization, per-goroutine repo handles, three assertion layers, and proper goroutine-safe error reporting.
- **Clean rebrand**: No stray "trace-*" strings in the new code.
- **`LC_ALL=C` on git commands**: Preventing locale-dependent error matching is a subtle but important detail.

---

## Summary

**Verdict: APPROVE**

This PR correctly solves the concurrent-session race condition with a well-layered locking strategy. The CAS + flock pattern is the canonical approach for cross-process git ref safety, and the implementation handles edge cases (dangling objects, locale-dependent errors, hash width) properly.

The concurrent test is strong and exercises the exact failure scenario. Trail UX and supporting code are clean and well-tested.

The one important finding (go-git stale ref read after CAS failure) has low practical impact because the 16-retry budget absorbs wasted attempts. The branch name sanitization suggestion is defensive-only given current naming conventions.

**Estimated effort to address suggestions**: ~30 minutes for the defensive sanitization; the stale-ref issue can be deferred since it's a performance concern, not a correctness bug.
