# Architecture

## Goal

Make a code forge that one developer can run on a VPS, while allowing repositories and collaboration data to survive the loss of any one hosting provider. Git remains the source format for code. Operators can run independent nodes and choose which repositories to replicate.

## Current milestone

The Go process serves a public landing page and a private team and code browser. People sign in through a web form that sets a short-lived, signed session cookie. Git clients use HTTP Basic authentication with the same personal token, or optional key-authenticated SSH Git transport and basic Git LFS. The server checks per-repository read/write grants, creates bare repositories, and invokes `git http-backend` for Git smart HTTP. Admins can update protected branches (`main` by default) and tags; writers can push other branches. A managed `pre-receive` hook enforces this on the Git server. The web page shows branches, diffs, pull requests, issues, tag-backed releases, and bounded live code search; pull requests can be approved and merged (fast-forward, squash, or an explicit merge commit) by an admin or maintainer when their immutable refs are unchanged and the repository's approval, check, and CODEOWNERS policy is met. Release archives are generated directly from the verified Git tag. A writer can manually trigger a local workflow stored in `.trace/workflow.json`; Trace persists run state, bounded logs, and artifacts.

User records are a small JSON file with hashed random tokens. The server reloads it on each request so rotation and revocation take effect immediately. Browser mutations authenticate through the signed session cookie and per-user CSRF token; script clients can use the JSON API with HTTP Basic personal tokens. Mutations append an owner-only audit event and can dispatch a signed webhook; webhook delivery is retried three times and recorded locally. A second node can fetch branches and tags into a read-only mirror. The system has no central database. Each repository can be copied with standard Git tools.

This provides independent copies of code, but one writable node is still authoritative for each repository. A mirror must still be configured and synced on a schedule; Trace verifies a signed source manifest, pins the source node identity at first contact, compares fetched refs with the signature, and publishes them in one ref transaction. Divergent branches, changed tags, and source-deleted refs stop sync until an administrator accepts the exact signed source object ID; the next signed sync checks the local and remote IDs again before applying that decision. Clients must choose another URL if the writable node fails. User records are local to each node and are not part of Git mirror sync.

## Next milestones

1. **Signed repository identity (implemented foundation).** Trace persists an Ed25519 node key and exposes authenticated signed ref manifests. The manifest proves which node signed the observed refs; it does not yet advertise peers or accept replicated metadata.
2. **Peer synchronization.** Let nodes subscribe to repository IDs, discover their announced endpoints, compare signed ref manifests, and fetch missing Git objects. Record sync status and last successful sync on the admin page. No peer should need the owner's administrator token to fetch a public repository.
3. **Recovery and multiple writers.** Keep each writer's refs under a separate namespace, such as `refs/trace/writers/<key>/...`. A repository owner can promote a writer's branch after reviewing it. This avoids silently overwriting divergent branches.
4. **Portable collaboration records.** Store issues, patches, reviews, and comments as signed, versioned objects that peers can replicate. Keep the format documented and exportable without the web application.
5. **Portable agent history.** Signed, versioned JSON bundles move structured checkpoints between nodes with an explicit expected source ID and target commit checks. An administrator can explicitly publish the same signed snapshot to a protected Git ref in a private repository. Automatic per-commit records, full run capture, and cross-node session sync remain future work; publication stays opt-in so summaries are not exposed with public code.
6. **Operations.** Backup verification, basic Git LFS, and checksummed release builds exist. Still missing: hardened isolated runners and distributed queues, TLS deployment examples, metrics, and LFS locking and garbage collection.

## Trust rules

- A Git commit hash proves content identity; it does not prove who is authorized to update a project's published branch. Signed manifests will supply that authorization.
- A replica can serve objects when the writable node is unavailable. It must not claim a new authoritative ref without the owner's signature.
- Repository metadata should be exportable as files. Nodes must not require a company-controlled API to recover a repository.
- Private repositories need a separate design for encryption and key distribution; replicating ciphertext is not sufficient if keys are lost.

## Current security boundary

Each user has a random personal token and may have authorized SSH public keys. Users may additionally require a TOTP code for browser sign-in; Git and API token authentication remains available for automation. The server checks the token hash at web sign-in and before every HTTP Git or API request, then checks repository access before passing a request to Git. Requests are throttled per client address in each process; the reverse proxy must provide shared limits for a multi-node deployment. SSH accepts only Git upload/receive commands and never opens a shell; its host key is persisted under the data directory. Web session cookies are signed, HTTP-only, and invalidated by token rotation or server restart. Only admins can create repositories, manage users, or update protected refs. The server binds to loopback by default, so a TLS reverse proxy is required for external access. Web forms carry a per-user CSRF token, and browser-session API writes require the same value in `X-Trace-CSRF`. Mirrors disable HTTP push. Repository names and Git paths are restricted before requests reach Git.
