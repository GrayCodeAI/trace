# Architecture

## Goal

Make a code forge that one developer can run on a VPS, while allowing repositories and collaboration data to survive the loss of any one hosting provider. Git remains the source format for code. Operators can run independent nodes and choose which repositories to replicate.

## Current milestone

The Go process authenticates HTTP requests, creates bare repositories, renders a small admin page, and invokes `git http-backend` for Git smart HTTP. A second node can fetch branches and tags into a read-only mirror. The system has no central database. Each repository can be copied with standard Git tools.

This provides independent copies of code, but one writable node is still authoritative for each repository. A mirror must be synced on a schedule, and clients must choose another URL if the writable node fails.

## Next milestones

1. **Signed repository identity.** Generate a local Ed25519 node key. Assign each repository an ID from its owner public key and repository name. Publish a signed manifest containing repository ID, current refs, and the addresses of permitted replica nodes. Verify signatures before accepting replicated metadata.
2. **Peer synchronization.** Let nodes subscribe to repository IDs, discover their announced endpoints, compare signed ref manifests, and fetch missing Git objects. Record sync status and last successful sync on the admin page. No peer should need the owner's administrator token to fetch a public repository.
3. **Recovery and multiple writers.** Keep each writer's refs under a separate namespace, such as `refs/meshgit/writers/<key>/...`. A repository owner can promote a writer's branch after reviewing it. This avoids silently overwriting divergent branches.
4. **Portable collaboration records.** Store issues, patches, reviews, and comments as signed, versioned objects that peers can replicate. Keep the format documented and exportable without the web application.
5. **Optional agent history.** Attach AI session checkpoints to commits in a separate, access-controlled store. Make publication opt-in so prompts and tool output are not accidentally exposed with public code.
6. **Operations.** Add per-repository authorization, TLS deployment examples, token rotation, backup and restore verification, metrics, Git LFS support, and release builds.

## Trust rules

- A Git commit hash proves content identity; it does not prove who is authorized to update a project's published branch. Signed manifests will supply that authorization.
- A replica can serve objects when the writable node is unavailable. It must not claim a new authoritative ref without the owner's signature.
- Repository metadata should be exportable as files. Nodes must not require a company-controlled API to recover a repository.
- Private repositories need a separate design for encryption and key distribution; replicating ciphertext is not sufficient if keys are lost.

## Current security boundary

The first milestone uses one admin token for all repositories on a node. The token is checked before any web or Git request. The server binds to loopback by default, so a TLS reverse proxy is required for external access. The web form carries a random CSRF token. Mirrors disable HTTP push. Repository names and Git HTTP paths are restricted before requests reach Git.
