# Trace

Trace is a self-hosted Git service for a small team. It keeps code in ordinary bare Git repositories, supports standard Git over HTTP(S), and can copy repositories to a read-only node on another VPS. The current design has one writable node per repository and manual failover.

## Team features

- Each person has a username and a generated personal token. User records keep token hashes in `data/users.json`; the bootstrap admin token also remains in the private `data/admin-token` file.
- Admins can create repositories and manage users and access from the web page or CLI.
- Repository grants are `read`, `write`, `maintain`, or `none`. Maintainers can merge reviewed pull requests and process the merge queue without becoming global administrators. Admins can access every repository.
- Repositories can be public or private. Public repositories allow anonymous web browsing and Git clone/fetch; pushes and all write operations still require authentication.
- Writers can push branches. Only admins can update `main` or tags. A server-side Git hook enforces this rule.
- The web page lists branches, recent commits, files, file contents, and a branch's changes relative to `main`. Admins review there and merge with standard Git commands.
- The public home page introduces Trace; the private dashboard has its own sign-in form and a session cookie. Git clients continue to use HTTP Basic authentication.
- Mirrors can serve clones but cannot accept pushes. Git history remains portable.

Trace includes a small pull-request, review, issue, release, webhook, audit, and notifications surface. It intentionally does not claim GitHub or GitLab parity.

Repositories can be tagged with up to 20 normalized topics for discovery:

```sh
./trace repo topics set -data ./data team/project go,git,agents
./trace repo topics list -data ./data team/project
```

Small teams can organize issues and pull requests on local project boards. The board API is also available through the API CLI:

```sh
./trace api project-create -url http://127.0.0.1:8787 -user admin -token-file data/admin-token -project Sprint team/project
./trace api project-card-add -url http://127.0.0.1:8787 -user admin -token-file data/admin-token -card-kind issue -card-number 7 -title "Fix release" team/project 1
./trace api project-card-move -url http://127.0.0.1:8787 -user admin -token-file data/admin-token -column Done team/project 1 1
```

For an entirely local operation, use the same board model without HTTP:

```sh
./trace project create -data ./data -name Sprint team/project
./trace project list -data ./data team/project
```

## Requirements

- Go 1.26 or newer to build.
- Git installed on each node.
- A TLS reverse proxy for use outside the machine. Trace listens on `127.0.0.1:8787` by default. Tokens must not cross the public internet without HTTPS.

## Start the main node

```sh
go build -o trace ./cmd/trace
./trace init -data ./data
./trace repo create -data ./data team/project
# or create a public repository
./trace repo create -data ./data -public team/project
# import an existing local working tree or bare repository
./trace repo import -data ./data /path/to/existing-repository team/project
./trace serve -data ./data
```

`repo import` is a local administrator operation. It clones Git objects into a new Trace bare repository, installs Trace branch protection, and refuses to overwrite an existing repository. Remote URL migration uses the signed `trace mirror sync` flow; the HTTP API never accepts arbitrary server filesystem paths.

`init` prints the first admin token once and stores it in `data/admin-token` with owner-only permissions. Open `http://127.0.0.1:8787/` locally for the public landing page, then choose **Open dashboard** to reach `/app`. Trace shows its own sign-in page at `/login`; sign in as `admin` with the token. Keep the data directory private and back it up. Browser sessions expire after 24 hours or when the token is rotated, and a server restart signs everyone out.

SSH transport is optional. Inspect or rotate the node host key locally with `trace ssh host-key` and `trace ssh rotate-host-key`. Rotation is atomic, but the running SSH listener must be restarted before it advertises the new fingerprint; publish the new fingerprint to operators before restarting clients.

Trace includes a local backup and restore utility. Backups contain the bare repositories and metadata, are written with owner-only permissions, and are verified before restore:

```sh
./trace backup create -data ./data -out /secure/backups/trace-20260920.tar.gz
./trace backup verify -out /secure/backups/trace-20260920.tar.gz
./trace backup restore -data ./restored-data -out /secure/backups/trace-20260920.tar.gz
```

Restore refuses to write into a non-empty directory unless `-force` is supplied. This is a single-node snapshot mechanism; schedule it and copy the archive to separate storage for disaster recovery.

To add a teammate from the CLI:

```sh
./trace user add -data ./data alice
./trace user grant -data ./data alice team/project maintain
./trace user list -data ./data
```

`user add` prints Alice's token once. Give it to her privately. The admin web page can also add users, rotate tokens, remove users, and change repository access. Use `trace user rotate`, `trace user remove`, or `trace user grant ... none` from the CLI to revoke access. Do not store tokens in Git remote URLs or shell history.

For shared access, create a team and grant its members a repository role:

```sh
./trace team create -data ./data platform
./trace team add -data ./data platform alice
./trace team grant -data ./data platform team/project write
./trace team list -data ./data
```

Teams are also manageable at **Team settings** in the dashboard and through `/api/v1/teams`. A member inherits the strongest role granted by any team. Removing a user from a team immediately removes that inherited access; direct user grants remain independent.

Developers use normal Git commands:

```sh
git clone https://git.example.com/git/team/project.git
cd project
git switch -c feature/my-change
# edit and commit
git push -u origin feature/my-change
```

When Git asks for credentials, enter the Trace username and personal token. A credential helper can remember them. Review the feature branch on the Trace web page. An admin can merge it with fast-forward, squash, or an explicit merge commit. Writers cannot push `main` directly.

Raw committed files can be consumed without the repository HTML shell:

```sh
curl https://git.example.com/raw/team/project/README.md?ref=main
./trace api raw -url https://git.example.com -user admin \
  -token-file ./data/admin-token -ref main -path README.md team/project
```

Public repositories allow anonymous raw reads; private repositories require repository access. Raw paths are branch-scoped, reject traversal, and are capped at 8 MiB. Raw files are served as inert data: HTML, SVG, XML, and JavaScript are sent as `text/plain`, and every raw response carries a script-free `Content-Security-Policy: sandbox`.

Change visibility from the CLI when needed:

```sh
./trace visibility public -data ./data team/project
./trace visibility private -data ./data team/project
```

The API exposes the same state in repository responses and accepts an admin-only `POST /api/v1/repos/OWNER/NAME/visibility` body such as `{"public":true}`. Anonymous access is deliberately read-only; Git receive-pack and authenticated repository actions remain protected.

## Git over SSH

SSH is optional and disabled unless you pass `-ssh-listen`. Add an authorized public key to a Trace user, then start the listener:

```sh
./trace user key add -data ./data admin "$HOME/.ssh/id_ed25519.pub"
./trace serve -data ./data -ssh-listen 127.0.0.1:2222
git clone ssh://admin@127.0.0.1:2222/team/project.git
```

The SSH host key is persisted at `data/ssh/host_ed25519`. Trace accepts Git smart-SSH commands only; shell access is not provided. New SSH connections are throttled to 60 per minute per remote host. Operators can print the key and fingerprint with `./trace ssh host-key -data ./data`, or retrieve JSON from `/.well-known/trace/ssh-host-key` before provisioning `known_hosts`.

HTTP requests are throttled per client address across all paths: 300 requests per minute, plus a separate limit of 20 sign-in POSTs per minute; rejected requests return `429`, `Retry-After`, and `X-RateLimit-*` headers. IPv6 clients are grouped by their /64 prefix. Counters are kept in memory by each Trace process and reset when it restarts; they are not shared between processes or nodes, so use the reverse proxy's limiter when you run several.

Behind a reverse proxy every request arrives from the proxy's address, so tell Trace which peers are proxies; their `X-Forwarded-For` (rightmost untrusted entry) or `X-Real-IP` header then identifies the client. Headers from any other peer are ignored:

```sh
./trace serve -data ./data -trusted-proxy 127.0.0.1,::1
```

## OIDC single sign-on

OIDC is optional and disabled unless configured. The setup stores the client secret under `data/oidc.json` with owner-only permissions, uses discovery plus authorization-code PKCE, validates signed state, requires HTTPS except for loopback development, and obtains identity claims from the provider's userinfo endpoint. When the provider returns an ID token, Trace validates its RS256 signature (keys of at least 2048 bits from the discovered JWKS endpoint), issuer, audience, expiry, and the nonce sent with the authorization request. Every request to the provider has a 10-second timeout and does not follow redirects:

```sh
./trace sso oidc set -data ./data \
  -issuer https://login.example.com \
  -client-id trace \
  -client-secret "$TRACE_OIDC_SECRET" \
  -redirect-url https://trace.example.com/login/oidc/callback \
  -auto-provision
./trace sso oidc disable -data ./data
```

Trace binds each OIDC identity to one local account by the provider's issuer and subject (`sub`); usernames and email addresses from the provider are never used to pick an existing account. With `-auto-provision`, a first sign-in creates a new account named after `preferred_username` (or the email's local part) and bound to that subject; if a local account with that name already exists, sign-in is refused. To let an existing account sign in through the provider, an administrator links it explicitly (the refusal page shows the subject):

```sh
./trace sso oidc link -data ./data alice PROVIDER_SUBJECT
./trace sso oidc unlink -data ./data alice
```

Accounts created by auto-provisioning in earlier Trace versions are not bound and must be linked once. `-allowed-email-domains example.com,example.org` additionally requires a verified email (`email_verified`) in one of those domains. When the provider returns an ID token, its subject must equal the userinfo subject. Accounts with Trace TOTP enabled cannot sign in through OIDC; they use token sign-in with their code.

The login page shows the provider button only while configuration is present. Auto-provisioned accounts receive a local record for session continuity but no personal token is returned. Providers that return only userinfo are still accepted, so deploy behind a provider whose userinfo endpoint is trusted and use HTTPS. SAML and provider-specific group claims remain unimplemented.

## Two-factor authentication

Admins can enable TOTP for browser sign-in. Git and API personal tokens remain usable for automation; browser login additionally requires the current six-digit code:

```sh
./trace user 2fa enable -data ./data alice
./trace user 2fa status -data ./data alice
./trace user 2fa disable -data ./data alice
```

`enable` prints a provisioning URI once. Store the secret in an authenticator and keep the data directory private. Trace accepts a small clock skew and never stores generated one-time codes. SAML and provider-specific OIDC group-to-role mapping are not implemented.

## Code and agent context search

Search is available on a readable repository without cloning it:

```sh
./trace api search -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token -query 'TODO' -ref main team/project
```

The browser search page is `/repos/OWNER/NAME/search`. By default, it searches literal text in the selected branch's code and commit history plus structured agent sessions. Use `-scope code`, `-scope commits`, or `-scope sessions` in the CLI to narrow the search. Session-only search does not require the branch to exist. Commit search scans at most the newest 1,000 commits per repository and returns at most 100 matches; it matches full commit messages but displays only subjects, authors, and hashes. Code search is text-only and branch-scoped. A matching commit opens a read-only message and diff page. Administrators or repository writers can build a persistent code index with:

```sh
./trace api search-index -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token -ref main team/project
```

The index is stored under `data/search-index`, refreshed for changed branches after HTTP pushes, and invalidated automatically when the commit changes. Code search is capped at 100 matches and 512 KiB of output; indexing skips binary files, files over 2 MiB, and stops at 64 MiB of text. Session results are separately capped at 100 matches and 280 characters per excerpt. Without a current code index, Trace falls back to live Git search. This is literal search, not semantic or vector search.

Workspace search covers every repository the signed-in user can read, ten repositories per page:

```sh
./trace api search-all -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token -query 'TODO' -ref main
```

The web page is `/search`. The API returns `next_after` when more repositories remain; pass that value as `-after OWNER/NAME` to the CLI for the next page. Repositories without the selected code branch contribute session matches only. Each repository retains its own 100-match cap for code and sessions; the response reports when a cap is reached. Search results only include repositories the authenticated user can read, including team grants.

## Agent sessions

Trace stores structured, local agent sessions and checkpoints linked to Git commits. It deliberately stores summaries and state, not raw prompts or tool transcripts:

```sh
./trace api agent-session-create -url http://127.0.0.1:8787 \
  -user admin -token-file ./data/admin-token -agent builder \
  -ref main -description 'Implement authentication flow' team/project
./trace api agent-session-checkpoint -url http://127.0.0.1:8787 \
  -user admin -token-file ./data/admin-token -ref main \
  -description 'Tests passing' team/project 1
./trace api agent-sessions -url http://127.0.0.1:8787 \
  -user admin -token-file ./data/admin-token team/project
```

The browser view is `/repos/OWNER/NAME/agents`. Session summaries are capped and redact common `token`, `password`, `secret`, and `authorization` assignments. This scanner is limited; inspect summaries before exporting them. Raw prompts and tool transcripts are not stored.

### Capture Codex turns after opt-in setup

Trace can receive Codex `agent-turn-complete` notifications and record the Git HEAD observed when each notification is processed. This **does not prove that Codex authored that commit**. By default Trace stores only a fixed completion label, a hashed Codex session/turn ID, and the commit SHA; it discards the notification's user messages and assistant message. To opt into saving the last assistant message as a session summary, add `-include-assistant-summary` during setup. Basic redaction cannot guarantee removal of every secret, so use that option only when the text is safe to share with repository readers.

Run this from the local worktree after creating the Trace repository and a personal token file:

```sh
./trace agent configure \
  -config "$PWD/.git/trace-agent-capture.json" \
  -url http://127.0.0.1:8787 -user admin \
  -token-file /absolute/path/to/data/admin-token \
  -repo team/project -worktree "$PWD"
```

Use absolute paths for the `trace` executable and the private config file in your **user-level** Codex `~/.codex/config.toml`:

```toml
notify = ["/absolute/path/to/trace", "agent", "codex-notify", "-config", "/absolute/path/to/worktree/.git/trace-agent-capture.json"]
```

Codex appends its JSON event as the final argument. Trace ignores notifications from other worktrees. The private config and outbox live in `.git`, outside committed files; the config holds a token-file path, never the token itself. A notification may arrive before its commit is pushed to Trace, so it remains queued. After `git push`, flush it with:

```sh
./trace agent sync -config "$PWD/.git/trace-agent-capture.json"
```

Later notifications also retry queued events. The capture endpoint accepts each session/turn event once, so retrying after a network failure does not duplicate checkpoints. This adapter captures completion metadata and an observed commit; it does not capture prompts, tools, edits, or a verified commit authorship trail. Codex currently documents `notify` as supporting `agent-turn-complete` events with `thread-id`, `turn-id`, and `cwd`: [Codex advanced configuration](https://developers.openai.com/codex/config-advanced#notifications).

### Capture Claude Code turns

The same private capture config also works with Claude Code. Add a `Stop` command hook to the worktree’s uncommitted `.claude/settings.local.json`, using absolute paths:

```json
{
  "hooks": {
    "Stop": [{
      "hooks": [{
        "type": "command",
        "command": "/absolute/path/to/trace",
        "args": ["agent", "claude-stop", "-config", "/absolute/path/to/worktree/.git/trace-agent-capture.json"],
        "async": true
      }]
    }]
  }
}
```

The hook reads Claude Code's JSON from stdin and records only a fixed completion label and the observed HEAD by default. `-include-assistant-summary` in the private Trace config opts into storing the final assistant message after basic redaction. When Claude Code includes `prompt_id`, Trace uses it to deduplicate repeated Stop notifications with the same prompt, commit, and stored summary. Older versions without `prompt_id` get a fresh event ID per Stop invocation; outbox retries still remain idempotent, but a provider-repeated invocation can appear twice. The async hook does not delay the Claude Code response; Claude Code may cancel an unfinished async hook when a non-interactive `claude -p` session exits, so use `trace agent sync` to flush anything that was queued. See the [Claude Code hook reference](https://code.claude.com/docs/en/hooks) for the `Stop` input and async command behavior.

If an unpushed commit keeps the queue blocked, inspect exact event IDs and SHAs with `trace agent pending -config FILE`. After deciding that a specific event should be discarded, run `trace agent drop -config FILE -event-id ID`. Dropping removes only the local queued event; it does not delete a server record that was already accepted.

### Capture Gemini CLI turns

Gemini CLI's `AfterAgent` hook can use the same private Trace capture config. Merge this example into your private `~/.gemini/settings.json`, replacing both absolute paths and quoting them for your shell if they contain spaces:

```json
{
  "hooks": {
    "AfterAgent": [{
      "hooks": [{
        "name": "trace-capture",
        "type": "command",
        "command": "'/absolute/path/to/trace' agent gemini-after-agent -config '/absolute/path/to/worktree/.git/trace-agent-capture.json'"
      }]
    }]
  }
}
```

Trace reads the hook's JSON from stdin and replies with `{}` on stdout, as Gemini expects. It discards the user prompt and final response by default; `-include-assistant-summary` in the private Trace config opts into storing a capped final response with basic redaction. Trace uses Gemini's session ID and event timestamp for replay-safe identity and records only the HEAD observed at hook time. The hook does not prove authorship or capture tools. See the [Gemini CLI hook reference](https://github.com/google-gemini/gemini-cli/blob/main/docs/hooks/reference.md). The adapter is tested with the documented JSON contract and the installed Gemini CLI package schema, but has not been exercised through a live Gemini model turn.

### Capture Cursor agent turns

Cursor's `stop` command hook can use the same private Trace capture config. Add this to your **user-level** `~/.cursor/hooks.json`, replacing both absolute paths. The command runs locally and reads Cursor's hook JSON from stdin:

```json
{
  "version": 1,
  "hooks": {
    "stop": [
      {"command": "/absolute/path/to/trace agent cursor-stop -config /absolute/path/to/private-capture.json"}
    ]
  }
}
```

Trace accepts only completed turns whose `workspace_roots` resolve to the configured Git worktree. It hashes Cursor's conversation and generation IDs, queues the observed HEAD until it exists on the Trace server, and returns `{}` to the hook. It stores no transcript, email, prompt, or model output from this hook, even when `-include-assistant-summary` is enabled, because the documented `stop` payload does not contain the final response. The event proves only that a hook observed HEAD at that time; it does not prove Cursor authored the commit. The adapter is tested against [Cursor's documented hook schema](https://cursor.com/docs/hooks), but has not been exercised in a live Cursor session.

### Move agent history between Trace nodes

An administrator can export native sessions as a signed JSON bundle and import them into a second node that already has the referenced Git commits:

```sh
./trace agent bundle export -data ./source-data \
  -out ./team-project-agents.json team/project
./trace agent bundle import -data ./mirror-data \
  -file ./team-project-agents.json \
  -expected-node-id VERIFIED_SOURCE_NODE_ID team/project
```

The source node ID must be checked through a trusted channel. Import rejects a changed signature, a different repository or node ID, missing commits, and changed previously imported checkpoints. Reimporting the same bundle is safe; a later bundle can add checkpoints. Imported sessions display their source node and source session ID and cannot be edited locally. Exports omit sessions imported from another node, so a node cannot silently re-sign another node's history as its own. The bundle contains summaries and contributor names: review it before sharing it outside the team.

The API exposes `GET` and admin `POST /api/v1/repos/OWNER/NAME/agent-sessions/bundle`. `POST` takes `{"expected_node_id":"...","bundle":{...}}`. The agent page has a download link and an admin upload form. `trace api agent-bundle-export -output NEW_FILE` and `trace api agent-bundle-import -bundle-file FILE -expected-node-id ID` provide remote CLI access.

### Keep signed agent history in Git

An administrator can publish the same signed, redacted native-session snapshot to a protected, per-node Git ref in a **private** repository:

```sh
./trace agent git publish -data ./source-data team/project
```

The command prints the exact ref and source node ID. Repository readers with Git access can fetch that ref. On another Trace node that already has the referenced code commits, fetch the printed ref into its bare repository, verify the node ID through a trusted channel, then import:

```sh
git -C /path/to/target-data/repos/team/project.git fetch \
  https://source.example/git/team/project.git \
  +PUBLISHED_REF:PUBLISHED_REF
./trace agent git import -data ./target-data \
  -expected-node-id VERIFIED_SOURCE_NODE_ID team/project
```

Publishing again adds a new Git snapshot commit only when native sessions changed. A later fetch and import adds new checkpoints without rewriting imported history. The browser agent page shows the current ref and lets an administrator publish it; the API exposes `GET` and admin `POST /api/v1/repos/OWNER/NAME/agent-sessions/git-bundle`. The local CLI can remove its own ref with `trace agent git unpublish -expected-node-id OWN_NODE_ID`, and the admin API accepts `DELETE` with `{"expected_node_id":"..."}`. Removing a ref does **not** erase old Git objects or copies another reader fetched. Trace refuses Git publication in public, mirrored, or archived repositories, and refuses to make a repository public while a published agent ref remains. Review summaries before publication: basic redaction cannot guarantee that every secret was removed. This is portable structured history, not full prompts or tool transcripts.

## Git LFS

Trace exposes the basic authenticated Git LFS batch protocol under `/lfs/OWNER/NAME.git/info/lfs`. Install `git-lfs`, point the Git remote at Trace, and use normal `git lfs track`, `git add`, and `git push` commands. Trace verifies each uploaded object's SHA-256 OID and size, stores objects under `data/lfs`, and caps individual objects at 100 MiB. LFS locking, object garbage collection, and distributed object storage are not implemented.

## Package artifacts

Trace also provides a generic immutable artifact registry. It is intentionally protocol-neutral; it does not claim npm, PyPI, or Maven compatibility yet:

```sh
./trace package publish -data ./data team/project lib 1.0.0 lib-1.0.0.tgz
./trace package list -data ./data team/project
./trace package get -data ./data team/project lib 1.0.0 lib-1.0.0.tgz /tmp/lib-1.0.0.tgz
```

The HTTP artifact route is `/packages/OWNER/NAME.git/PACKAGE/VERSION/FILENAME`; uploads require write access, downloads require read access, and each artifact is verified and capped at 100 MiB. The browser listing is `/repos/OWNER/NAME/packages`.

Trace can publish a committed branch as a Pages-style static site. Enable it locally or through the API:

```sh
./trace pages enable -data ./data team/site main
./trace api pages -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token -pages-branch main team/site
```

The site is served at `/pages/OWNER/NAME/`. An `index.html` is used for directory requests, and an optional root such as `dist` can be configured. Public repositories have anonymous Pages reads; private repositories require repository access. Files are read from Git, capped at 8 MiB, and path traversal is rejected. Pages are served on Trace's origin inside a CSP sandbox without `allow-same-origin`: site scripts run with an opaque origin and cannot read Trace pages, cookies, or form tokens as the signed-in user. Because of that sandbox, browsers do not send the Trace session cookie with the site's own sub-resource requests, so a private Pages site should inline its CSS, scripts, and images. Disable with `trace pages disable` or `trace api pages-disable`.

For npm-style consumers, Trace exposes package metadata and tarball routes at `/npm/OWNER/REPO/PACKAGE` and `/npm/OWNER/REPO/PACKAGE/-/FILENAME`. A basic npm `PUT` payload with one `_attachments` tarball is accepted and stored immutably. Scoped package names, dist-tag mutation, npm auth token negotiation, and dependency proxying are not implemented.

Python tooling can browse the same artifacts through the authenticated simple index at `/pypi/OWNER/REPO/simple/` and `/pypi/OWNER/REPO/simple/PACKAGE/`. A basic PyPI multipart upload is accepted at `/pypi/OWNER/REPO/` with `name`, `version`, and `content`; Trace does not generate wheel metadata or implement repository-wide PyPI search.

## Federation manifests

Each node has a persisted Ed25519 identity under `data/node/identity_ed25519`. Read a signed repository ref manifest with:

```sh
./trace api manifest -url http://127.0.0.1:8787 -user admin -token-file ./data/admin-token team/project
```

The manifest lets another node verify which Trace identity signed the observed refs. It does not discover peers, copy Git objects, or resolve concurrent writers; mirror synchronization remains explicitly configured and scheduled.

## Local CI actions

A repository can define a manually triggered local workflow at `.trace/workflow.json`:

```json
{"name":"checks","sandbox":true,"sandbox_runtime":"docker","sandbox_image":"golang:1.26-alpine","jobs":[{"name":"test","run":["go test ./..."],"artifacts":["coverage.out"]}]}
```

Trigger and inspect runs through the API or CLI:

```sh
./trace api action-run -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token team/project
./trace api actions -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token team/project
```

Pushing a branch through Trace automatically queues the workflow for that changed branch when `.trace/workflow.json` is present. Manual triggering remains available for reruns and diagnostics.

The operator, not the workflow file, decides what may run. `trace serve -actions MODE` accepts:

- `sandboxed` (the default): only workflows that set `"sandbox":true` run; any other workflow is refused when it is pushed, triggered, or scheduled.
- `off`: no workflow runs on this node.
- `trusted`: workflows without `"sandbox":true` also run, as the Trace service account. Use this only when every repository writer on the node is trusted.

```sh
./trace serve -data ./data -actions off
```

Queued or running jobs can be cancelled with `POST /api/v1/repos/OWNER/NAME/actions/runs/ID/cancel` or:

```sh
./trace api action-cancel -url http://127.0.0.1:8787 -user admin -token-file ./data/admin-token OWNER/NAME ID
```

Cancellation and the 15-minute step limit stop the whole step and record the result. Local and `sandbox-exec` steps run in their own process group, which Trace kills on cancellation, on timeout, and when the step exits, so background processes do not outlive it (a process that deliberately leaves the group with `setsid` is not tracked). Docker steps run in a container named `trace-run-RUN-JOB-STEP`, which Trace stops with `docker kill`. Each step's captured output is capped at the 1 MiB log limit. The runner is still local process execution, not a hardened container or VM sandbox.

The runner checks out the requested commit, limits each command to 15 minutes and each log to 1 MiB, stores job logs under `data/action-logs`, and stores matching artifacts under `data/artifacts`. Each repository keeps its 200 most recent finished runs; older runs are pruned together with their logs and artifacts, so required checks must pass on a run that is still retained. The run list API returns summaries; `GET /api/v1/repos/OWNER/NAME/actions/runs/ID` returns one run with its logs, and the actions page shows the 20 most recent runs. A node runs at most four jobs concurrently; additional runs remain queued and can be cancelled. Workflows without `"sandbox":true` run only under `-actions trusted` and then execute with the Trace service account. On macOS, `"sandbox":true` uses `sandbox-exec` with a restricted filesystem and no network access. For portable isolation, set `"sandbox_runtime":"docker"` and an explicit `"sandbox_image"`; Trace runs Docker with no network, a read-only root, dropped capabilities, no-new-privileges, a process limit, and only the checked-out workspace (`/workspace`) and a per-run scratch directory (`/tmp`) writable; `TRACE_REPOSITORY`, `TRACE_COMMIT`, `CI`, and `TRACE_SECRET_*` are passed into the container by name. Trace refuses to fall back to unsandboxed execution when sandboxing is requested. Docker still depends on the host daemon and image supply chain.

Repository-scoped CI secrets can be managed through the dashboard, API, or CLI. Secret values are stored in `data/secrets.json` with owner-only permissions and are injected only into jobs as `TRACE_SECRET_<NAME>` environment variables; list operations return names, never values. A job's environment contains only `PATH`, `HOME` (the checkout), `TMPDIR` (a per-run scratch directory), `LANG`/`LC_ALL` when set, `TRACE_REPOSITORY`, `TRACE_COMMIT`, `CI=true`, and the repository's `TRACE_SECRET_*` values; the Trace service environment is never passed to jobs:

```sh
./trace api secret-set -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token -secret-file ./data/npm-token OWNER/NAME
./trace api secret-list -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token OWNER/NAME
```

The local runner is still not an isolation boundary. Do not run untrusted workflow commands on a shared node.

Workflows may opt into a recurring schedule with a duration between one minute and 24 hours:

```json
{"name":"nightly","schedule":"6h","sandbox":true,"sandbox_runtime":"docker","sandbox_image":"golang:1.26-alpine","jobs":[{"name":"test","run":["go test ./..."]}]}
```

Start the scheduler explicitly with the server (it is disabled by default):

```sh
./trace serve -data ./data -actions-interval 1m
```

Scheduled runs target `main`, are persisted in the Actions database, and do not overlap another queued or running scheduled run for the same repository. Push and manual runs are accepted into the same bounded worker queue. This is a single-node scheduler; it has no hosted runner pool or distributed locking.

## Add a read-only mirror

Create a dedicated read-only account on the source node:

```sh
./trace user add -data ./data mirror
./trace user grant -data ./data mirror team/project read
```

Build and initialize Trace on a second VPS. Copy the new `mirror` token into a private file there, for example `/etc/trace/source-token` with mode `0600`. Then run:

```sh
./trace mirror sync -data ./data \
  -from https://git.example.com/git/team/project.git \
  -user mirror \
  -token-file /etc/trace/source-token team/project
```

Mirror sync verifies the source node's signed ref manifest by default before accepting branches and tags. Use `-verify-manifest=false` only when interoperating with a non-Trace Git source that cannot expose the federation manifest endpoint.

For repeatable multi-repository replication, persist peer definitions locally:

```sh
./trace federation peer add -data ./data \
  -from https://trace.example/git/team/project.git \
  -user mirror -token-file ./data/source-token team/project
./trace federation peer list -data ./data
./trace federation peer sync -data ./data
```

Peer definitions store the source URL, username, and token-file path; tokens themselves are not copied into the peer registry. Sync still needs an external scheduler such as cron or a system timer.

Signed mirror sync uses full Git ref names in its manifest, compares the downloaded probe refs to the signed values, and updates mirror refs in one Git transaction. A rewind, divergent branch, changed tag, or locally present source-deleted ref is recorded in `data/federation-conflicts.json` and stops synchronization instead of choosing a side. Inspect conflicts with:

```sh
./trace federation conflicts -data ./data
```

Administrators can also read them from `GET /api/v1/federation/conflicts` or `/settings/federation`. To accept the signed source for one conflict, copy the exact source URL, full ref name, local object ID, and remote object ID from that record:

```sh
./trace federation resolve -data ./data \
  -source https://trace.example/git/team/project.git \
  -ref refs/heads/main -local LOCAL_OID -remote SIGNED_SOURCE_OID \
  team/project
./trace federation peer sync -data ./data
```

For a source-deleted ref, replace `-remote SIGNED_SOURCE_OID` with `-delete`. The admin API exposes `POST /api/v1/federation/conflicts/resolve`, and the federation settings page offers the same approval. Approval alone does not move or delete a ref. A later signed sync must still verify the pinned source identity, fetch exactly the signed refs, and find the same local and remote object IDs. An approved deletion is included in the atomic ref transaction. The conflict record then gains `applied_at`; stale records gain `superseded_at`, and changed IDs require a new decision.

The first verified sync pins the source node's Ed25519 ID in `data/federation-trust.json`. Later signed manifests from a different key are rejected before mirror refs change. This is trust on first use: verify the first node ID with the source operator through a trusted channel. A valid signature alone does not prove that the first responder was the intended node.

```sh
./trace federation trust list -data ./data
./trace federation trust forget -data ./data -expected PINNED_NODE_ID \
  team/project https://source.example/git/team/project.git
```

Forgetting a pin requires the currently pinned ID. Verify a replacement ID before the next sync, which will pin it. Administrators can also inspect or forget pins in `/settings/federation`, or use `GET` and `DELETE /api/v1/federation/trust` through the API. Source identity rotation is an operator action; it is not automatic.

Trace can also run the scheduler itself:

```sh
./trace serve -data ./data -federation-interval 5m
```

The interval is disabled by default. Each cycle logs failures and continues serving HTTP. Configured peer sync requires signed refs and never replaces a writable repository. The separate manual mirror command permits unsigned Git interoperability only when `-verify-manifest=false` is explicitly set.

The same registry is available to remote admins through `/api/v1/federation/peers` and `/api/v1/federation/peers/sync`, or through `trace api federation-peers`, `federation-peer-add`, `federation-peer-remove`, and `federation-sync`.

Schedule this command with a timer or cron. It copies branches and tags, not user accounts or other files. The mirror has its own administrator token; create team accounts on the mirror if teammates need to clone from it. If the source `mirror` token is rotated, update the mirror's source-token file. Remote HTTP is allowed only for a loopback source.

## Data and limits

Repositories live under `data/repos/OWNER/NAME.git`; access records live in `data/users.json`. Existing single-user data directories retain their original token under the username `admin`. The old `git` username is no longer accepted. Starting the server installs the managed branch-protection hook in existing repositories and refuses to replace a custom `pre-receive` hook.

Repositories can be forked locally or through the API:

```sh
./trace repo fork -data ./data team/source admin/fork
./trace api fork -url http://127.0.0.1:8787 -user admin -token-file ./data/admin-token team/source admin/fork
```

The fork copies Git objects and refs, installs Trace branch protection, and grants the target owner write access. It is a full independent repository; later synchronization is not automatic.

The repository page also includes the same fork form for authenticated users.

Administrators can move or permanently remove a repository:

```sh
./trace repo transfer -data ./data team/project team/platform
./trace repo delete -data ./data team/platform
```

The authenticated API provides `POST /api/v1/repos/OWNER/NAME/transfer` with `{"name":"NEW_OWNER/NAME"}` and `DELETE /api/v1/repos/OWNER/NAME`. The dashboard exposes the same controls. Deletion removes the bare repository and is irreversible; transfer keeps Git configuration and rewrites repository references in Trace metadata.

Administrators can archive and restore repositories:

```sh
./trace repo archive -data ./data team/old-project
./trace repo restore -data ./data team/old-project
```

Archived repositories remain readable and clonable, but Git pushes and write operations are rejected until restoration.

Trace exposes administrator-authenticated SCIM 2.0 endpoints at `/scim/v2/Users` and `/scim/v2/Groups`. User provisioning supports list, create, get, deactivate/reactivate (including `path: "active"` patches), and delete; group resources map directly to durable Trace teams, and group patches add, replace, and remove members (including `members[value eq "USER"]` paths). Lists support `filter=userName eq "NAME"` (users) or `filter=displayName eq "NAME"` (groups) and `startIndex`/`count` paging. Trace refuses SCIM `password` values: credentials are personal tokens issued by an administrator, or OIDC sign-in. New SCIM users are therefore created disabled until an administrator issues a token (`trace user rotate`) and the account is reactivated. Full OIDC/SAML SSO and SCIM role mapping beyond team repository grants are not implemented.

Branches can also be managed without raw Git plumbing:

```sh
./trace branch create -data ./data team/project feature -from main
./trace branch delete -data ./data team/project feature
```

The API exposes the same operations at `POST /api/v1/repos/OWNER/NAME/branches` and `DELETE /api/v1/repos/OWNER/NAME/branches/BRANCH`. The `main` branch cannot be deleted.

The repository page includes matching create and delete branch controls for users with write access.

External systems can publish durable commit statuses with `POST /api/v1/repos/OWNER/NAME/statuses/COMMIT` and read them with `GET` on the same path. States are `error`, `failure`, `pending`, or `success`, and each context is updated in place.

The repository page shows statuses for the selected branch commit under **Commit checks**.

The mirror is read-only, synchronization is scheduled externally, and failover is manual. Automatic peer discovery, multi-writer conflict resolution, language-specific package protocols, LFS locking/GC, distributed object storage, and isolated CI are not implemented. File previews and web diffs are size-limited; clone the repository for a full review.

## Develop

```sh
go test ./...
go vet ./...
```

The integration tests verify authenticated Git push and clone, repository permissions, protected `main`, branch review, token rotation, mirror behavior, pull-request and issue lifecycles, webhook signing, release archives, and SSH key-authenticated Git transport.

The web UI bundles the Inter typeface under the [SIL Open Font License](cmd/trace/assets/OFL-Inter.txt); it does not request fonts from a third-party server.

## API

The authenticated JSON API is intentionally small and stable enough for scripts and future CLI commands. Use a personal token with HTTP Basic authentication; never put a token in a URL.

```sh
curl --user "admin:$TRACE_TOKEN" http://127.0.0.1:8787/api/v1/repos
curl --user "admin:$TRACE_TOKEN" http://127.0.0.1:8787/api/v1/repos/team/project/branches
curl --user "admin:$TRACE_TOKEN" http://127.0.0.1:8787/api/v1/repos/team/project/commits?limit=20
curl --user "admin:$TRACE_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"team/new-project"}' \
  http://127.0.0.1:8787/api/v1/repos
# Admin user lifecycle: the token is returned only on create/rotate.
curl --user "admin:$TRACE_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"alice","admin":false}' \
  http://127.0.0.1:8787/api/v1/users
curl --user "admin:$TRACE_TOKEN" http://127.0.0.1:8787/api/v1/users
curl --user "admin:$TRACE_TOKEN" -X PATCH \
  -H 'Content-Type: application/json' -d '{"disabled":true}' \
  http://127.0.0.1:8787/api/v1/users/alice
curl --user "admin:$TRACE_TOKEN" -X POST http://127.0.0.1:8787/api/v1/users/alice/rotate
```

The same read operations are available from the bundled CLI. The token is read from a file so it does not appear in shell history:

```sh
./trace api repos -url http://127.0.0.1:8787 -user admin -token-file ./data/admin-token
./trace api branches -url http://127.0.0.1:8787 -user admin -token-file ./data/admin-token team/project
./trace api commits -url http://127.0.0.1:8787 -user admin -token-file ./data/admin-token team/project
```

The browser session can read the API. A browser-session write must also send `X-Trace-CSRF` equal to the session user's CSRF value; only requests whose Basic credentials authenticate the same user are exempt from that browser-only header.

Admins can inspect the append-only audit ledger:

```sh
./trace api audit -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token -limit 100
```

Events are stored as JSON Lines in `data/audit.jsonl` with owner-only permissions. Token values and request bodies are never written to the ledger.

## Webhooks

Admins can configure signed JSON webhooks for repository events. HTTPS is required for remote endpoints; plain HTTP is accepted only for loopback development endpoints.

```sh
./trace api webhook-create -url http://127.0.0.1:9000 -user admin \
  -token-file ./data/admin-token -webhook-url http://127.0.0.1:9000/events \
  -webhook-secret 'use-a-random-secret-at-least-16-chars' \
  -events repo.created,pull_request.merged,issue.created team/project
./trace api webhooks -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token team/project
```

Each delivery includes `X-Trace-Event` and `X-Trace-Signature: sha256=...`. Trace attempts delivery up to three times with a short backoff and stores the result in `data/webhook-deliveries.json`. Admins can inspect it with `GET /api/v1/repos/OWNER/NAME/webhooks/ID/deliveries`; this is a bounded local history, not a durable distributed queue.

## Repository stars

Authenticated users can star repositories they can read. The repository page shows the current count and toggle; scripts can use the API or CLI:

```sh
./trace api star -url http://127.0.0.1:8787 -user admin -token-file ./data/admin-token team/project
./trace api stars -url http://127.0.0.1:8787 -user admin -token-file ./data/admin-token team/project
./trace api unstar -url http://127.0.0.1:8787 -user admin -token-file ./data/admin-token team/project
./trace api watch -url http://127.0.0.1:8787 -user admin -token-file ./data/admin-token team/project
./trace api watchers -url http://127.0.0.1:8787 -user admin -token-file ./data/admin-token team/project
./trace api unwatch -url http://127.0.0.1:8787 -user admin -token-file ./data/admin-token team/project
```

Watching a repository adds push, issue, pull-request, and release activity to the existing notification inbox; the actor does not receive a duplicate notification for their own event.

## Releases

Releases are metadata attached to existing Git tags. Trace verifies the tag before storing the release and can stream a gzip archive directly from the bare repository:

```sh
./trace api release-create -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token -tag v1.0.0 -title 'First release' team/project
./trace api releases -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token team/project
curl --user "admin:$TRACE_TOKEN" \
  -o project-v1.0.0.tar.gz \
  http://127.0.0.1:8787/api/v1/repos/team/project/releases/archive/v1.0.0
# Release assets are stored outside the Git object database and capped at 100 MiB.
./trace api release-asset-upload -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token -asset-name project-macos.tar.gz \
  -asset-file ./dist/project-macos.tar.gz team/project 1
./trace api release-assets -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token team/project 1
```

## Issues

Issues and labels are stored in `data/issues.json` and are available from the dedicated repository issues page, the JSON API, and the CLI:

```sh
./trace api issue-create -url http://127.0.0.1:8787 -user alice \
  -token-file ./alice.token -title 'Track a bug' -labels bug,priority team/project
./trace api issues -url http://127.0.0.1:8787 -user alice \
  -token-file ./alice.token team/project
./trace api issue-comment -url http://127.0.0.1:8787 -user reviewer \
  -token-file ./reviewer.token -body 'Confirmed on main' team/project 1
./trace api issue-close -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token team/project 1
```

## Pull requests

Pull requests are stored in `data/pull-requests.json`, with an atomic lock and immutable source/target commit snapshots. They support labels and assignees through the API, CLI, and browser create form. Draft pull requests can be marked ready by their author or an admin; merge and merge-queue processing reject drafts. An admin or maintainer can fast-forward the target branch only when the source and target refs have not changed, the configured approval count is met, and required action jobs passed for the same head commit. Review comments may include `file`, `line`, and `side: "new"` to anchor them to a text file in the pull request head commit. Trace validates that the file and line exist and stores the head commit with the comment, so later branch changes do not silently move the anchor. The browser review page is `/repos/OWNER/NAME/pulls/ID`.

Admins can configure merge requirements through the API or CLI. The default requires one approval from someone other than the author:

```sh
./trace api policy-set -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token -required-approvals 2 \
  -required-checks test,lint -protected-branches main,release/* \
  -require-codeowners team/project
./trace api policy -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token team/project
```

Required checks match successful action job names for the pull-request head commit. A run from a different commit does not satisfy the policy. Protected branch patterns are enforced by the Git receive hook for non-admin pushes; `*` is supported as a trailing wildcard, for example `release/*`. Patterns may contain only letters, digits, `.`, `_`, `-`, and `/`, must start with a letter or digit, and are quoted when Trace writes the hook. Administrators can still perform emergency updates.

When `-require-codeowners` is enabled, Trace reads `CODEOWNERS` from the pull-request head (`CODEOWNERS`, `.github/CODEOWNERS`, `.gitlab/CODEOWNERS`, or `docs/CODEOWNERS`) and requires an approval from a matching user or team member for every changed file covered by the last matching rule. Pattern matching supports repository-relative paths, filename patterns, trailing directory patterns, and recursive `**` path segments. CODEOWNERS syntax outside this subset is ignored.

Admins can also edit the same settings in the browser at `/repos/OWNER/NAME/settings/policy`.

Approved pull requests can be serialized through the durable merge queue. Enqueue from the browser pull-request page or use the API CLI; processing rechecks approvals, action jobs, CODEOWNERS, and branch freshness before changing the target ref:

```sh
./trace api merge-queue-enqueue -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token -pull-request 12 -strategy squash team/project
./trace api merge-queue -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token team/project
./trace api merge-queue-process -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token team/project
```

Queue processing is operator-triggered and single-node. It records `queued`, `processing`, `merged`, or `blocked` entries in `data/merge-queue.json`; it is not a hosted worker or a distributed consensus queue.

```sh
./trace api pull-create -url http://127.0.0.1:8787 -user alice \
  -token-file ./alice.token -title 'Ship feature' -source feature team/project
./trace api pulls -url http://127.0.0.1:8787 -user alice \
  -token-file ./alice.token team/project
./trace api pull-approve -url http://127.0.0.1:8787 -user reviewer \
  -token-file ./reviewer.token team/project 1
./trace api pull-merge -url http://127.0.0.1:8787 -user admin \
  -token-file ./data/admin-token team/project 1
```

See [FEATURES.md](FEATURES.md) for the verified capability matrix and the missing work required before claiming parity with larger forges or agent platforms.

Milestones are managed through the same API and CLI (`trace api milestones` and `trace api milestone-create`). Issue updates can assign a milestone by ID. Activity notifications are stored locally per user and can be read or marked complete with `trace api notifications` and `trace api notification-read`; Trace does not send email or push notifications yet.
