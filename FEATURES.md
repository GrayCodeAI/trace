# Trace capability audit

This is the honest baseline for the current monorepo. “All features from the top 20 competitors” is not a finite implementation task: GitHub, GitLab, Forgejo, SourceHut, agent platforms, and CI vendors overlap only partially, and several features require separate infrastructure (runners, package registries, email, object storage, search, and an agent sandbox). Trace must ship a coherent product before it attempts parity.

## Current evidence

| Area | Status | Evidence in this repository |
| --- | --- | --- |
| Git smart HTTP clone/fetch/push | Implemented | `cmd/trace/main.go`, integration tests |
| Public/private repository visibility | Implemented | `trace repo create -public`, `trace visibility`, API visibility endpoint, anonymous public Git/web reads, authenticated writes |
| Local bare repositories and migration import | Implemented | `store.createRepo`, local `trace repo import`, Git integration tests; remote migration uses signed mirror sync |
| Personal tokens and repository grants | Implemented with read/write/maintain roles | `cmd/trace/users.go`, team inheritance, access tests, and admin API user lifecycle endpoints; maintainers can merge and process queues without global admin access |
| Teams and inherited repository roles | Implemented | Durable team membership and read/write grants via CLI, API, dashboard, Git HTTP, SSH, and browser authorization |
| Protected `main` and tags | Implemented | managed `pre-receive` hook and push test |
| Browser sign-in and session cookies | Implemented | `cmd/trace/session.go` |
| Browser repository/branch/file/diff view | Implemented | `cmd/trace/web.go` |
| Repository stars | Implemented | Durable `stars.json`, API/CLI star/unstar/list operations, and repository page toggle/count |
| Repository watchers | Implemented | Durable `watchers.json`, API/CLI watch/unwatch/list operations, repository page toggle/count, and inbox notifications for pushes, issues, pull requests, and releases |
| Repository topics | Implemented | Durable normalized topics with CLI, API, and maintainer web editing |
| Repository project boards | Implemented, local Kanban | Durable repository-scoped projects with Todo/In progress/Done columns, issue or pull-request cards, API, API CLI, and web board |
| Raw file delivery | Implemented | Public/private `/raw/OWNER/NAME/FILE`, authenticated API raw endpoint, CLI download, branch/path validation, and 8 MiB cap |
| Landing page | Implemented | `cmd/trace/landing.html` |
| Read-only mirror sync | Implemented, manual, signed when source is Trace | `trace mirror sync` verifies the source manifest by default, then fetches branches/tags; scheduling and failover remain external |
| Repository forks | Implemented | Local `repo fork` and API/CLI `fork` clone Git objects, install protection, and grant the target owner write access |
| Archive, restore, transfer, and delete | Implemented | Admin CLI, API, and repository web controls; transfers migrate Git plus repository metadata references; archived repositories remain readable but reject Git writes |
| Backup, verification, and restore | Implemented | Local gzip/tar snapshots with path validation and explicit non-empty restore protection via `trace backup` |
| Branch lifecycle API | Implemented | Create/delete branch commands and API endpoints with configurable protected branch patterns, archived/mirror checks, and receive-hook enforcement |
| Commit status checks | Implemented | Durable named contexts via API and API CLI; action runs remain the built-in CI source |
| JSON API for repositories, branches, commits | Implemented | `/api/v1/repos...`, `cmd/trace/api.go` |
| API repository creation | Implemented for admins | `POST /api/v1/repos` |
| Pull requests, drafts, labels, assignees, requested reviewers, review comments, approvals, code owners, merge queues, and merge strategies | Implemented with recursive CODEOWNERS path matching and an operator-triggered single-node queue | Durable local PR store; API, CLI, and browser review page; draft-to-ready lifecycle; labels/assignees/reviewer requests with inbox notifications; line comments anchor to a text file/line and commit; approval/check/code-owner policies are enforced; serialized queue rechecks policy before merging; merge supports fast-forward, squash, and explicit merge commits; unsupported CODEOWNERS syntax and distributed queue workers are not implemented |
| Issues, labels, milestones, notifications | Issues, labels, milestones, and local inbox implemented; email/push missing | Atomic `issues.json` store, notification inbox, API, CLI, and dedicated issue web page |
| CI runners and workflow files | Implemented, local, bounded queue; the operator's `trace serve -actions` policy (default `sandboxed`) decides whether workflows run and whether they must request macOS or Docker sandboxing | `.trace/workflow.json`, automatic push triggers, manual reruns, persisted runs, four-worker local queue, cancellation, logs, artifacts, repository-scoped secrets, opt-in persisted schedules, macOS `sandbox-exec`, and Docker isolation with no network/read-only root/capability drop; unsandboxed workflows run only under `-actions trusted`, and hosted runners are not implemented |
| Webhooks | Implemented, signed delivery with retries/history | Admin API/CLI configuration; HTTPS or loopback HTTP; HMAC-SHA256 signatures; three attempts and persisted delivery records |
| Releases, assets, archive downloads, and Pages | Implemented | Tag-backed releases support bounded 100 MiB asset upload/list/download, gzip archives use `git archive`, and Pages serves committed branch files with public/private access and path validation |
| Packages, registries, Git LFS | Generic artifacts, basic npm/PyPI interoperability, and basic Git LFS implemented | Authenticated package publish/list/download, one-attachment npm publish, basic PyPI multipart upload and simple-index reads, and LFS batch/upload/download; SHA-256 verification and 100 MiB object caps; no wheel metadata or distributed object store |
| SSH Git transport | Implemented, key-authenticated, throttled, discoverable, and rotatable | `trace user key`, persisted host key, `trace ssh host-key`, `trace ssh rotate-host-key`, admin API rotation, `/.well-known/trace/ssh-host-key`, `git-upload-pack`/`git-receive-pack`, and a shared 60-connections/minute per-host limiter; rotation requires a Trace restart to load the new signer; disabled by default |
| SSO, 2FA, SCIM, audit log | Optional OIDC authorization-code login with PKCE; returned RS256 ID tokens are checked against discovered JWKS, while userinfo-only providers remain supported; TOTP 2FA, SCIM users and groups mapped to teams, audit log, and shared file-backed rate limits implemented; SAML remains missing | `trace sso oidc`, `/login/oidc`, `/login/oidc/callback`, `/scim/v2/Users`, `/scim/v2/Groups`, durable team membership, append-only `audit.jsonl`, admin API, and `429` request throttling shared across local processes |
| Search and code indexing | Implemented, bounded persistent code index with live fallback; literal code, commit, and agent session search | Repository and workspace API/CLI/web queries return code, branch-scoped commit messages, and redacted session/checkpoint matches; commit results open a read-only diff page; each repository scans at most 1,000 recent commits and returns 100 matches; workspace search pages through ten authorized repositories at a time and respects team grants; code index rebuilds via API and refreshes on HTTP pushes; no semantic search |
| Agent session/checkpoint storage | Implemented, structured records with opt-in signed portability and narrow Codex/Claude Code/Gemini CLI/Cursor adapters | API, CLI, and browser page; sessions/checkpoints link to commits, redact common token/password/secret assignments, and can be exported/imported as Ed25519-signed bundles or explicitly published in private Git refs after expected node ID and target commit verification; supported completion notifications can queue metadata until the observed HEAD exists on the server; raw transcripts, automatic Git publication, and authorship verification are not implemented |
| Signed federation identity and peer sync | Implemented, configured peers, with explicit conflict decisions and source identity pinning | Persisted Ed25519 identity, signed full-ref manifests, first-use source node ID pinning with CLI/API/web inspection and explicit forget, exact probe ref comparison before an atomic mirror ref transaction, opt-in scheduling, non-fast-forward and changed-tag conflict detection, durable conflict records, and exact-object-ID source acceptance for branch, tag, or deletion conflicts; first contact still requires out-of-band verification, while network discovery and multi-writer reconciliation remain external |

## Competitor-derived scope

The comparison set is GitHub, GitLab, Bitbucket, Gitea, Forgejo, Codeberg, SourceHut, OneDev, Gogs, Gitness, RhodeCode, Phorge, Gerrit, Azure DevOps, AWS CodeCommit, Entire, Graphite, GitLab Duo, GitHub Copilot coding agent, and Sourcegraph Cody. These products do not expose one common feature set. Trace will measure itself against the following product slices:

1. **Forge basics:** Git over HTTP and SSH, repository visibility, protected branches, pull requests, review comments, approvals, bounded CODEOWNERS rules, issues, labels, milestones, releases, webhooks, and an API.
2. **Team administration:** roles, teams, token lifecycle, 2FA, audit events, rate limits, backups, and repository transfer/deletion.
3. **Automation:** workflow definitions, isolated runners, logs, artifacts, schedules, secrets, and status checks.
4. **Distribution:** packages, releases, LFS, raw files, archive downloads, and Pages-style static publishing.
5. **Local-first and distributed operation:** portable on-disk data, signed manifests, peer sync, conflict-safe writer refs, restore verification, and explicit failover.
6. **Agent workflows:** opt-in session/checkpoint records linked to commits, agent identity, prompt/tool redaction, searchable context, and review gates.

## Remaining implementation order

1. Richer status reporting, CODEOWNERS pattern semantics, merge queues, and code-review ownership UX.
2. Distributed rate limiting, stronger account security, SSH host-key rotation/distribution, and isolated CI execution.
3. Hosted or sandboxed CI runners, language-complete package registries, and distributed artifact storage.
4. Multi-writer federation, automatic peer discovery, richer conflict reconciliation, and portable collaboration records.
5. Automatic Git-native agent checkpoints and sync, more agent adapters, richer redaction/search controls, and provider-complete SSO.

Trace is **not feature-complete** against this matrix today. The implemented baseline is intentionally smaller and testable; each missing row needs its own design, tests, and operational story before it can be called done.

## Entire comparison (reviewed 2026-09-20)

This is a capability comparison with [Entire's product page](https://entire.io/), not a claim of performance parity. Trace's landing page uses an original visual and its own product copy.

| Entire product area | Trace today | Gap |
| --- | --- | --- |
| Git hosting and regional mirrors | Standard Git HTTP and SSH on one writable node; signed, read-only peer mirrors | No measured speed claim, managed regional network, or automatic failover |
| CLI and browser UI | Both exist for core workflows | CLI parity across every web action and a consistently polished application UI are unfinished |
| Agent sessions and checkpoints | Structured records linked to commits, portable opt-in signed JSON bundles, private Git refs with signed snapshots, and opt-in Codex/Claude Code/Gemini CLI/Cursor completion capture | No full conversation capture, verified agent authorship, automatic per-commit checkpoints, or automatic cross-node session sync |
| Local privacy controls | Selected secret fields are redacted from stored session data | No general transcript scanner or configurable redaction policy |
| Semantic graph | Not implemented | No graph construction, semantic retrieval, or verified token-saving measurement |
| Agentic search | One literal query across code, selected-branch commits, and structured session summaries | No semantic ranking, raw-transcript search, or graph-based context retrieval |
| Agent integrations | Codex, Claude Code, Gemini CLI, and Cursor completion notifications can queue observed HEAD metadata with idempotent replay | No full run capture, verified authorship, or other agent hooks; Cursor's adapter has been tested against its documented schema but not through a live Cursor session |

Do not market Trace as an Entire equivalent until these gaps have implementations and independent verification. The near-term product goal is a reliable self-hosted Git workspace for a small team, with explicit agent context and safe read-only replication.
