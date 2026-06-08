# Design Doc: Trace Visual Session Replay + Collaboration UI

**Status:** Draft / Proposed
**Owner:** trace team
**Last updated:** 2026-06-06
**Scope:** Multi-month effort. This document is an execution plan, not a code drop.

---

## 1. Overview & Competitive Context

`trace` today captures AI coding sessions git-natively: transcripts and full-state checkpoints are
written to git **orphan/shadow branches** rather than a database. Permanent checkpoints land on the
`trace/checkpoints/v1` orphan branch (`cli/checkpoint/committed.go:51`,
`committed.go:1594-1647`), while in-flight, intra-session rewind state lives on per-commit shadow
branches keyed `trace/<commit-hash>` (`cli/checkpoint/checkpoint.go:55-86`,
`cli/checkpoint/shadow_ref.go`). The OpenAPI contract is explicit that
**trace ships no HTTP server today** (`api/openapi.yaml`: *"trace is a CLI tool — no HTTP server is
exposed"*).

Replay today is a **terminal experience**: `trace session replay [session-id]` walks the recorded
transcript step-by-step in the CLI (`cli/session_replay.go:27-90`). It reads the raw
transcript via the live path in session state, parses it with the shared transcript parser
(`cli/transcript/parse.go`), and renders `replayEntry{Index,Type,Content,ToolName,Timestamp}`
rows (`session_replay.go:18-25`, `buildReplayEntries`). There is no timeline, no diff view, no
sharing, and nothing a non-terminal user (PM, reviewer, teammate) can open.

**Where the Top-20 ship this (from `TOP20_COMPARISON.md`):**

| Capability (this doc) | Top-20 repos that ship it | Comparison doc ref |
|---|---|---|
| Visual session replay UI (web) | OpenHands (browser session replay), Browser Use, LangSmith (visual trace viewer) | line 194 |
| Session search / filter UI | OpenHands, LangSmith (trace search w/ filters) | line 200 |
| Agent decision-tree flamegraph / waterfall spans | LangSmith (flamegraph trace view), OpenHands (decision tree), Arize Phoenix (waterfall spans) | line 201 |
| Shared session viewing for teams | OpenHands Cloud, Cursor, Windsurf | line 202 |
| Public session sharing via permalink | OpenHands Cloud, Langfuse (public trace sharing) | line 217 |
| rrweb browser-session recording/replay | OpenHands (rrweb DOM-mutation streams + rrweb-player) | trace P1 table, line ~205 |
| Export to video / GIF / asciinema | Browser Use (video export), asciinema | line 215 |
| Telemetry/observability export (OTel) | LangSmith, Langfuse, Arize Phoenix | line 60 |

The comparison doc already points at the prior sketch `.hermes/specs/trace-visual-replay.md` (line
194). That spec is a useful wishlist but is **not grounded** in trace's current architecture — it
assumes a generic REST + SQLite-FTS5 backend and ignores the orphan-branch storage model, the
existing redact package, and the cost/attribution work already in-tree. This document supersedes it
with a plan specific to *this* codebase.

The executive summary (line 21) names "no web UI" and "no observability integrations" among the five
ecosystem-wide gaps, and lists trace's missing "conversation forking" and "three-mode commit
attribution." This effort closes the surface-area gap for trace specifically and gives the whole
ecosystem its first shareable, visual artifact — which doubles as the demo surface for the rest of
the stack.

---

## 2. Goals / Non-Goals

### Goals
1. A **web-based, read-mostly session replay viewer** (React) that renders transcript events
   (user / assistant / tool-call / tool-result) on a **scrubbable timeline**, replacing the
   terminal-only replay as the primary review surface.
2. An **agent decision visualization** — flamegraph/waterfall of the captured tool-call sequence,
   with per-span duration, token usage, and cost.
3. **Shared session viewing for teams** — a teammate can open a session another person captured,
   from a shared trace remote, without re-running the agent.
4. **Public permalink sharing with mandatory secret redaction**, reusing the existing
   `trace/redact` package so no unredacted bytes ever leave the machine.
5. **Static export** of a session to a self-contained artifact (HTML, GIF/MP4, asciinema cast)
   for docs and demos.
6. Reuse, not reinvent: build on the existing transcript parser, checkpoint store, cost compute,
   annotate, and redact packages.

### Non-Goals
- **Not** a cloud SaaS / multi-tenant control plane. No hosted accounts, billing, or org RBAC
  (those are separate P0 ecosystem gaps for hawk/eyrie, lines ~+RBAC). Sharing here is
  artifact-based (push to a git remote / publish a static bundle), not server-tenancy.
- **Not** live two-way collaboration (no simultaneous cursors / editing). "Collaboration" here =
  asynchronous shared *viewing* of already-captured sessions.
- **Not** a replacement for the TUI replay's interactive *stepping* use-case offline; the TUI stays.
- **rrweb browser recording is explicitly conditional**: it ships only *if/when* hawk gains agentic
  browser control. Until then it is a forward-compatible schema slot, not built code.
- **Not** changing the on-disk storage model. Checkpoints remain on
  `trace/checkpoints/v1` and shadow branches; we add a read API over them, not a new datastore.

---

## 3. Architecture

### 3.1 Component overview

```
┌──────────────────────────────────────────────────────────────────────────┐
│  trace serve   (NEW, local-first HTTP server; off by default)              │
│  ┌────────────────────────────────────────────────────────────────────┐  │
│  │ REST + SSE handlers (read-mostly)                                    │  │
│  │   GET /v1/sessions, /v1/sessions/:id, /v1/checkpoints/:id, ...       │  │
│  │   GET /v1/sessions/:id/spans      ← decision-tree/waterfall model    │  │
│  │   GET /v1/sessions/:id/cost       ← cost attribution                 │  │
│  │   POST /v1/sessions/:id/publish   ← redacted static export          │  │
│  └────────────────────────────────────────────────────────────────────┘  │
│         │ reads via existing packages (no new datastore)                   │
│         ▼                                                                   │
│  ┌──────────────┐  ┌────────────────────┐  ┌───────────────┐ ┌─────────┐  │
│  │ checkpoint   │  │ transcript/parse.go │  │ agent/token_  │ │ redact  │  │
│  │ store        │  │ (Line events)       │  │ usage + cost/ │ │ pkg     │  │
│  │ (orphan/     │  │                     │  │ compute       │ │         │  │
│  │  shadow refs)│  │                     │  │               │ │         │  │
│  └──────────────┘  └────────────────────┘  └───────────────┘ └─────────┘  │
└──────────────────────────────────────────────────────────────────────────┘
                              ▲
                              │ fetch + SSE
                  ┌───────────┴───────────────┐
                  │  React Replay SPA          │
                  │  (go:embed static bundle)  │
                  │  Timeline · Transcript ·   │
                  │  Waterfall · Diff · Cost   │
                  └────────────────────────────┘

  trace publish ──► self-contained static bundle (HTML/JSON, fully redacted)
                    └─► optional push to git remote / GitHub Pages branch
```

Two delivery modes, sharing one read core:
- **`trace serve`** — local HTTP server (binds `127.0.0.1` by default) for interactive viewing of
  *this machine's* sessions. The React bundle is `go:embed`-ed into the trace binary so there is no
  separate deploy step (mirrors the hawk daemon's embedded-frontend approach in the comparison doc,
  hawk P0).
- **`trace publish` / `trace export`** — no server; produces a frozen, redacted, self-contained
  artifact. This is the team-sharing and demo path and works without any running process.

### 3.2 Data model

Nothing new is persisted on disk. The viewer consumes a derived **read model** assembled at request
time from the existing artifacts.

**Source events** — parsed from the transcript with the existing parser. The on-the-wire transcript
line is intentionally small (`cli/transcript/types.go:22`):

```go
type Line struct {
    Type    string          `json:"type"`           // "user" | "assistant" | "tool" (normalized)
    Role    string          `json:"role,omitempty"`
    UUID    string          `json:"uuid"`
    Message json.RawMessage `json:"message"`         // content blocks: text / tool_use / tool_result
}
```

Content-block constants already exist (`types.go:10-16`: `TypeAssistant`, `ContentTypeToolUse`,
`tool_result`). `normalizeLineType` (`parse.go:128-132`) handles the format variants, so the read
model gets a single normalized event stream regardless of agent.

**Derived read model (API DTOs, computed, not stored):**

```
SessionSummary    { id, agentType, model, provider, branch, startedAt, endedAt,
                    checkpointCount, eventCount, tokenUsage, estimatedCost, status }
SessionDetail     { SessionSummary, checkpoints[], annotations[] }
Checkpoint        { id, kind: "committed"|"shadow", parentId, createdAt,
                    transcriptStart, filesTouched[], author }
TimelineEvent     { index, uuid, type, role, ts, contentBlocks[], parentCheckpointId }
Span              { id, parentId, name(toolName), startTs, durationMs,
                    inputTokens, outputTokens, cacheRead, cacheCreate, costUsd,
                    depth, status }              // for waterfall/flamegraph
CostBreakdown     { total, byCheckpoint[], byAgent[], byModel[] }
```

- `TokenUsage` is read directly from the existing struct
  (`cli/agent/types.go:48`): `InputTokens`, `CacheCreationTokens`, `CacheReadTokens`,
  `OutputTokens`, `APICallCount`, and crucially `SubagentTokens *TokenUsage` — the recursive
  subagent field is exactly what feeds nested spans in the waterfall.
- `Span` is computed by walking the normalized event stream: each `tool_use` block opens a span,
  the matching `tool_result` (correlated by `tool_use_id`) closes it. Subagent token rollups
  (`SubagentTokens`) and any subagent transcript dirs become child spans. Per-span cost comes from
  `cost.ComputeCost` / `ComputeCostWith` (`cli/cost/compute.go:38-51`) against the active
  model's pricing (`cli/cost/pricing.go`).

**Checkpoint mapping for the timeline:** committed checkpoints (`trace/checkpoints/v1`) are the
permanent timeline nodes; shadow-branch checkpoints (`trace/<commit-hash>`) are the rewind/scrub
granularity within a node. `CheckpointTranscriptStart` (referenced throughout
`strategy/manual_commit_condensation.go`) maps each checkpoint to its slice of the transcript, so the
timeline scrubber can jump to "the transcript as it was at checkpoint N."

### 3.3 API surface (`trace serve`, all read-mostly)

```
GET  /v1/sessions                         list + filter (agent, branch, date, model, status, text)
GET  /v1/sessions/:id                     SessionDetail (checkpoints + annotations)
GET  /v1/sessions/:id/events              normalized TimelineEvent[] (paginated, ?from, ?to)
GET  /v1/sessions/:id/spans               Span[] (waterfall/flamegraph model)
GET  /v1/sessions/:id/cost                CostBreakdown
GET  /v1/checkpoints/:id                  Checkpoint detail (transcript slice, files)
GET  /v1/checkpoints/:id/diff             unified diff vs parent (from shadow/committed tree)
GET  /v1/sessions/:id/annotations         from annotate state (see §4)
SSE  /v1/sessions/:id/live                live tail for an in-progress session
POST /v1/sessions/:id/publish             produce redacted static bundle, return path/URL
GET  /                                     embedded React SPA
```

Design constraints:
- **Bind to loopback by default.** A `--listen` flag is required to expose beyond `127.0.0.1`, and
  doing so prints a redaction/exposure warning (§7).
- **No write endpoints that mutate git history** in P0/P1. (`rewind` already exists as a CLI verb;
  exposing it over HTTP is deferred — the prior spec's `POST /rewind` is out of scope here.)
- Handlers live in a new `cli/serve/` package and reuse the existing internal API client
  conventions in `cli/api/` (`client.go`, `base_url.go`, `auth_tokens.go`).

### 3.4 Key flows (sequences)

**A. Open a session in the web viewer**
```
user> trace serve
  → starts loopback HTTP server, opens browser at http://127.0.0.1:<port>
SPA → GET /v1/sessions
  server: strategy.* enumerates sessions (reuses FindMostRecentSession path / session state listing)
SPA → GET /v1/sessions/:id/events
  server: read transcript (live path or shadow-branch copy) → transcript.ParseFromBytes
         → normalizeLineType → TimelineEvent[]
SPA → GET /v1/sessions/:id/spans
  server: walk events, correlate tool_use/tool_result, fold SubagentTokens,
          cost.ComputeCost per span → Span[]
SPA renders Timeline + TranscriptViewer + Waterfall + CostChart
```

**B. Scrub the timeline to checkpoint N**
```
SPA: user drags scrubber to checkpoint node N
SPA → GET /v1/checkpoints/:id  (id = N)
  server: checkpoint store reads tree at trace/checkpoints/v1 (or shadow ref) for N
SPA → GET /v1/checkpoints/:id/diff
  server: git diff N vs parent → unified diff
SPA: TranscriptViewer truncates to CheckpointTranscriptStart(N); DiffViewer shows N vs parent
```

**C. Publish a public permalink (redaction is mandatory and on the hot path)**
```
user> trace publish <session-id>
  server/CLI: assemble read model (events, spans, cost, diffs)
  → redact.JSONLContent(transcript)          // structured JSONL redaction
  → redact.String(...) on free-text fields    // annotations, prompts, file paths
  → redact.LoadPacks(custom-packs-dir)        // org/custom secret patterns
  → verify pass (redact/verify.go) asserts no residual secrets
  → render static HTML + frozen JSON bundle (no live endpoints baked in)
  → [optional] push bundle to a publish branch / GitHub Pages, return permalink
```

**D. Team viewing of a colleague's session**
```
colleague captured session on their machine → checkpoints pushed to shared git remote
  (trace/checkpoints/v1 is an orphan branch and is pushable like any ref)
teammate> git fetch <remote> 'refs/heads/trace/checkpoints/v1:...'   (helper: trace pull)
teammate> trace serve   → GET /v1/sessions lists the fetched committed checkpoints
  server reads from the fetched committed branch (origin/trace/checkpoints/v1 fallback already
  exists: committed.go:1648) → renders read-only
```
Note: shadow branches are local/intra-session and are *not* the team-sharing vehicle; the
**committed** branch is. This is why team viewing is artifact/git-based, not server-tenancy.

---

## 4. Integration With Existing hawk-eco Code

Everything below already exists in-tree and is reusable today. Citations are `path:line` where useful.

| Need | Reuse | Notes |
|---|---|---|
| Parse transcript events | `cli/transcript/parse.go` (`ParseFromBytes`, `ParseFromFileAtLine`, `normalizeLineType`) and `transcript/types.go:10-26` | Already handles gzip, arbitrarily long lines, multiple agent formats, and incremental parsing from a start line (perfect for SSE live tail and checkpoint slicing). The web read model is a thin adapter over this. |
| Walk replay entries | `cli/session_replay.go` (`replayEntry`, `buildReplayEntries`, `parseTranscriptToReplayEntries`) | The TUI replay already converts `Line` → display rows. Extract the agnostic conversion into a shared function consumed by *both* the TUI and the HTTP handlers so there's one event-normalization codepath. |
| Token usage per session/subagent | `cli/agent/token_usage.go` (`CalculateTokenUsage`, subagent-aware path) + `agent/types.go:48` (`TokenUsage` incl. `SubagentTokens`) | Directly feeds span token annotations and the cost chart. Subagent recursion already modeled. |
| Cost attribution | `cli/cost/compute.go:38-51` (`ComputeCost`, `ComputeCostWith`) + `cost/pricing.go` (`ModelPricing`, `Table`, `LoadTable`, `PricingFor`) | This is the "new cost-attribution work" the task says to reuse. Per-span, per-checkpoint, per-agent, per-model breakdowns all derive from these. No new pricing logic. |
| Annotations / collaboration metadata | `cli/annotate_cmd.go` (`trace annotate --comment/--checkpoint/--list`, persisted via `MutateSessionState`) | Annotations are already per-session and optionally per-checkpoint. Surface them on the timeline (pins on checkpoint nodes). Reuse the "annotate work" the task references — the web UI is a read view over the same session-state field. |
| Secret redaction | `trace/redact/` — `redact.go:165` `String`, `:474` `Bytes`, `:485` `JSONLBytes`, `:506` `JSONLContent`; `packs.go:184` `LoadPacks`; `redact/verify.go` | Mandatory on every publish/export path. JSONL-aware redaction matches the transcript format exactly. `LoadPacks` lets orgs add custom patterns. `verify.go` gives a post-redaction assertion. |
| Checkpoint storage / refs | `cli/checkpoint/` — `checkpoint.go:55-86` (shadow vs committed model), `committed.go:1594-1648` (`trace/checkpoints/v1` orphan branch + origin fallback), `shadow_ref.go` (CAS-safe ref updates) | The read API reads trees from these refs. The `origin/trace/checkpoints/v1` fallback (`committed.go:1648`) is precisely what makes §3.4-D team viewing work without new infra. |
| Diff vs parent | checkpoint trees + go-git (`github.com/go-git/go-git/v6`, already a dep) | Diff is a tree-vs-tree git diff between checkpoint commits; no new diff engine. |
| OTel export (observability tie-in) | `otel_collector.go` (`OTelCollectorConfig`, span batching/export) | Spans we compute for the waterfall are conceptually the same spans OTel export ships (comparison line 60). Unify the span model so the UI waterfall and the Langfuse/Phoenix export read one `Span` type. |
| HTTP/CLI plumbing | `cli/api/` (`client.go`, `base_url.go`, `auth_tokens.go`), cobra command pattern (`session_replay.go:27`, `annotate_cmd.go:20`) | New `trace serve` / `trace publish` / `trace export` commands follow the existing cobra `newXCmd()` + `runX()` pattern. |
| TUI rendering stack | `charm.land/bubbletea/v2` (go.mod:7), `charmbracelet/x/ansi` | Used by the asciinema/GIF export renderer to "play" the transcript in a headless terminal buffer (§6). |

**What is genuinely new (must be built):**
- `cli/serve/` — HTTP handlers, read-model assembly, SSE live tail.
- `cli/serve/spans.go` — tool_use/tool_result correlation → `Span[]` (shared with OTel).
- React SPA under `ui/` (`go:embed`-ed) — Timeline, TranscriptViewer, Waterfall, DiffViewer,
  CostChart, AnnotationPins.
- `trace publish` / `trace export` static-bundle renderers.
- Headless terminal-replay renderer for GIF/MP4/asciinema.

---

## 5. Phased Rollout

### P0 — Foundations + viewer (the demo-able core)
Milestones:
1. **Extract a shared event-normalization function** from `session_replay.go` so TUI and HTTP share
   one transcript→event path. (No behavior change; pure refactor + tests.)
2. **`trace serve`** loopback HTTP server with `go:embed` static hosting; endpoints `GET /v1/sessions`,
   `/v1/sessions/:id`, `/v1/sessions/:id/events`, `/v1/checkpoints/:id`, `/v1/checkpoints/:id/diff`.
3. **React Replay SPA v1**: Session list → Session detail → **timeline scrubbing** over checkpoints
   + TranscriptViewer (syntax-highlighted) + DiffViewer.
4. **Cost panel** wired to `cost.ComputeCost` + `agent.CalculateTokenUsage`.
5. **Annotations on the timeline** (read view over annotate state).
Exit criteria: a user runs `trace serve`, opens a recent session in the browser, scrubs the timeline,
sees transcript + diff + cost. Loopback-only.

### P1 — Decision visualization + sharing
Milestones:
1. **Span model** (`spans.go`): tool_use/tool_result correlation, subagent rollups, per-span cost.
2. **Waterfall + flamegraph view** in the SPA (depth from subagent nesting; width = duration;
   color = cost/token).
3. **`trace publish`**: redacted static bundle (HTML + frozen JSON), `redact.JSONLContent` +
   `redact.String` + `LoadPacks` + `verify` on the hot path; emits a permalink-able artifact.
4. **Team viewing**: `trace pull`/`trace push` helpers for `trace/checkpoints/v1`; serve renders
   fetched committed checkpoints (reuses `origin/trace/checkpoints/v1` fallback).
5. **Session search/filter** (comparison line 200): server-side filter over enumerated sessions
   (agent, branch, date, model, status); text search over transcript content. (Index strategy in
   Open Questions — start with linear scan + cache, add an index only if it doesn't meet latency.)
6. **Unify OTel `Span`** so waterfall + Langfuse/Phoenix export share one type (line 60).
Exit criteria: publish a redacted permalink a teammate opens with no secrets; waterfall renders for
a multi-tool, multi-subagent session.

### P2 — Export polish + conditional browser replay
Milestones:
1. **`trace export --format asciinema|gif|mp4`**: headless bubbletea render of the transcript →
   asciinema cast → optional ffmpeg to GIF/MP4 (comparison line 215). asciinema first (no ffmpeg
   dep), GIF/MP4 behind an ffmpeg-present check.
2. **Embed mode** (`<iframe>`/embeddable HTML) for docs.
3. **rrweb integration — conditional** (line ~205): *only if hawk adds agentic browser control.*
   Define the rrweb event-batch schema slot in the read model now; implement capture (CDP/Playwright
   shim) + rrweb-player view only when browser control lands. No build work until then.
4. **Live SSE polish** for in-progress sessions; webhook notifications on session events
   (comparison line 203) as an adjacent deliverable.
Exit criteria: a session exports to a shareable GIF/asciinema; rrweb schema is reserved and
documented but dormant.

---

## 6. Build-vs-Buy & Dependencies

| Concern | Decision | Rationale / licensing |
|---|---|---|
| Web framework | **Build thin** on Go stdlib `net/http` + existing `cli/api/` conventions | Avoids a heavy server dep; trace is a static-binary tool. SSE is trivial over stdlib. |
| Frontend framework | **React + Vite + TypeScript**, `go:embed` the built bundle | Matches the prior spec and the hawk daemon's embedded-frontend pattern. MIT-licensed toolchain. Single binary preserved. |
| Diff rendering | **Buy (vendor JS): Monaco diff editor** OR a lighter diff lib | Monaco = MIT. Heavy; consider a lighter MIT diff renderer if bundle size matters. Diff *computation* stays server-side via go-git. |
| Charts (cost/token) | **Buy: Recharts or uPlot** (MIT) | Standard, well-licensed. |
| Flamegraph/waterfall | **Build** a small SVG/Canvas renderer over our `Span[]` | Generic flamegraph libs don't fit our nested-subagent + cost model cleanly; the renderer is small. |
| Terminal replay → cast | **Reuse bubbletea** (go.mod:7) to render headless; **asciicast v2** format (open spec) | No new dep for asciinema; bubbletea already vendored. |
| GIF/MP4 | **Buy: ffmpeg as optional external tool** (not linked, shelled out) | ffmpeg is LGPL/GPL depending on build — *shelling out* avoids linking/licensing entanglement. Feature gated on ffmpeg presence. |
| rrweb | **Buy: rrweb + rrweb-player** (MIT) | Only pulled in if/when browser control ships (P2 conditional). |
| Redaction | **Reuse `trace/redact`** | Already in-tree, already battle-tested (extensive `redact/*_test.go`). No new dep. |
| Cost/pricing | **Reuse `cli/cost`** | No new pricing source. |
| Search index | **Defer**: start linear-scan + in-memory cache; add SQLite FTS5 (CGO-free via modernc) only if needed | Avoid CGO (trace ships a static binary). The prior spec's FTS5 assumption needs validation against the no-CGO constraint. |

**Static-binary constraint is load-bearing:** trace is distributed as a Go static binary. Any
dependency that requires CGO (e.g. mattn/go-sqlite3 for FTS5) breaks that. This is why search starts
index-free and any later index must be CGO-free.

---

## 7. Security / Privacy Considerations

These repos are privacy-first; the viewer must not weaken that.

1. **Loopback by default.** `trace serve` binds `127.0.0.1`. Exposing it (`--listen 0.0.0.0`)
   requires an explicit flag and prints a warning. No remote endpoint is opened implicitly.
2. **Redaction is mandatory and verified on every export/publish.** Every byte that can leave the
   machine passes through `redact.JSONLContent` (structured transcript) + `redact.String`
   (free text) + org `LoadPacks` patterns, followed by a `redact/verify.go` assertion. **Fail
   closed**: if verify finds residual secrets, publish aborts. Unredacted bytes never reach a
   permalink/bundle.
3. **Local serve does NOT redact by default** (it's your own machine, loopback) — but the redaction
   pipeline is reused identically for any path that produces a shareable artifact, so there is no
   second, weaker codepath to audit.
4. **Published bundles are frozen and inert.** A `trace publish` artifact contains no live endpoints,
   no API tokens, no machine paths beyond what survives redaction. It is static HTML + frozen JSON.
5. **No new auth/identity system.** Team viewing rides existing git transport auth (you can already
   fetch the orphan branch or you can't). We deliberately avoid building a tenancy/credential layer
   here (that's a separate hawk/eyrie SaaS concern).
6. **Shadow branches stay local.** Intra-session rewind state (`trace/<commit-hash>`) is never the
   sharing vehicle; only the curated `trace/checkpoints/v1` committed branch is pushed/shared,
   limiting blast radius of accidental exposure.
7. **Annotations may contain sensitive notes.** They go through the same redaction on publish and are
   excluded from public bundles unless explicitly opted in.
8. **Cost/pricing data is non-sensitive** but model names can leak provider choice; included by
   default, suppressible.

---

## 8. Open Questions

1. **Session enumeration source of truth.** Is there a single canonical "list all sessions" path, or
   must we walk `trace/checkpoints/v1` trees + local session-state files? (`FindMostRecentSession`
   exists; bulk listing performance at 1000+ sessions is unproven.)
2. **Search index under the no-CGO constraint.** Is linear-scan acceptable at target corpus sizes, or
   do we need a CGO-free index (Bleve? modernc-sqlite FTS5)? Needs a benchmark before committing.
3. **Diff for shadow vs committed checkpoints.** Confirm both checkpoint kinds expose a stable tree to
   diff against a parent uniformly, including first-checkpoint (orphan) cases.
4. **Live SSE for in-progress sessions** — does the capture path flush the transcript incrementally
   enough that `ParseFromFileAtLine` tailing is smooth, or do we need a notify hook?
5. **Permalink hosting model.** GitHub Pages via a publish branch, a static bundle the user hosts, or
   both? Affects redaction guarantees and the "permalink" UX.
6. **Span timing fidelity.** Do transcripts carry per-tool-call timestamps/durations reliably across
   all agents, or must some waterfall widths be estimated? (`Line` has UUIDs but timing lives in the
   message blocks per-agent.)
7. **OTel span unification scope.** How much of `otel_collector.go`'s existing span shape can become
   the shared `Span` type without breaking the export contract?
8. **rrweb trigger.** What's the committed timeline for hawk browser control, and should we ship the
   dormant schema now or wait?

---

## 9. Effort Estimate (rough, eng-weeks)

Assumes 1–2 engineers, includes tests + docs. Backend = Go; frontend = React/TS.

| Phase | Workstream | Eng-weeks |
|---|---|---|
| P0 | Shared event-normalization refactor (TUI/HTTP) | 1.0 |
| P0 | `trace serve` + read endpoints (sessions/events/checkpoints/diff) | 2.5 |
| P0 | React SPA v1 (list, detail, timeline scrub, transcript, diff) | 4.0 |
| P0 | Cost + annotation panels | 1.5 |
| **P0 subtotal** | | **~9** |
| P1 | Span model + tool/subagent correlation | 2.0 |
| P1 | Waterfall/flamegraph renderer (SVG/Canvas) | 2.5 |
| P1 | `trace publish` + redaction/verify pipeline + static bundle | 2.5 |
| P1 | Team viewing (push/pull helpers, fetched-branch serve) | 1.5 |
| P1 | Search/filter (index-free first) | 2.0 |
| P1 | OTel `Span` unification | 1.0 |
| **P1 subtotal** | | **~11.5** |
| P2 | asciinema/GIF/MP4 export (headless bubbletea + ffmpeg) | 3.0 |
| P2 | Embed mode + live SSE polish + webhooks | 2.0 |
| P2 | rrweb (conditional, only if browser control ships) | 3.0 (deferred) |
| **P2 subtotal** | | **~5 (+3 deferred)** |
| **Total (excl. deferred rrweb)** | | **~25–26 eng-weeks** |

This is a multi-month effort (~6 calendar months at 1 engineer, ~3 at two), consistent with the
comparison doc classifying web-UI/visual-replay as a major surface-area gap rather than a quick win.
