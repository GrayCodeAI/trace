# MeshGit

MeshGit is a small Git host built for one developer and multiple independently operated nodes. It serves standard Git smart HTTP, keeps repositories as ordinary bare Git repositories, and can pull a read-only copy onto another node. This is the first working milestone of a decentralized code host, not yet a peer-to-peer forge.

## What works

- Create repositories from the CLI or a small web page.
- Clone, fetch, and push with standard Git over HTTP(S).
- Require one admin token for every request, including reads.
- Pull a read-only mirror from another MeshGit node over HTTPS.
- Run with the Go standard library and the system `git` binary. No database is needed.

## Requirements

- Go 1.26 or newer to build.
- Git installed on each node.
- A TLS reverse proxy for use outside the machine. MeshGit listens on `127.0.0.1:8787` by default and uses HTTP Basic authentication; do not expose plain HTTP on the internet.

## Start one node

```sh
go build -o meshgit ./cmd/meshgit
./meshgit init -data ./data
./meshgit repo create -data ./data me/project
./meshgit serve -data ./data
```

`init` prints a random admin token once and stores it in `data/admin-token` with owner-only permissions. Keep the data directory private and back it up. The web page is at `http://127.0.0.1:8787/`; authenticate with username `git` and that token.

After your TLS proxy points `https://git.example.com` to `127.0.0.1:8787`, use ordinary Git:

```sh
git remote add origin https://git.example.com/git/me/project.git
git push -u origin main
git clone https://git.example.com/git/me/project.git
```

When Git asks for credentials, use username `git` and the admin token as the password. Configure your operating system's Git credential helper if you do not want to enter it on each push. Do not put the token in a remote URL, shell history, or repository config.

## Add a second node

Build and initialize MeshGit on a second VPS. Copy the **first node's** admin token into a private file on the second node, for example `/etc/meshgit/source-token` with mode `0600`. Then run:

```sh
./meshgit mirror sync -data ./data \
  -from https://git.example.com/git/me/project.git \
  -token-file /etc/meshgit/source-token me/project
```

The command creates `me/project` as a read-only repository on the second node if needed, then fetches its branches and tags. Run it periodically with a timer or cron. You can clone from either node. If the first node is lost, a clone or mirror has the Git history; you can create a new writable repository and push the branches to it.

Only the source URL and token file are inputs to `mirror sync`. It refuses to overwrite a normal writable repository. Remote HTTP is accepted only for a loopback source, to support local development.

## Architecture and limits

```text
developer Git client ──push──> writable MeshGit node
                               │
                               └──fetch──> read-only MeshGit mirror
                                             (independent VPS)
```

Each node stores bare Git repositories under `data/repos/OWNER/NAME.git`. MeshGit authenticates requests and delegates Git wire protocol handling to `git http-backend`. There is no custom Git object format or central database.

This milestone has a single writable node per repository. Mirrors are read-only, sync is scheduled externally, and failover is manual. It does not yet include peer discovery, multi-writer conflict resolution, accounts, repository-level permissions, Git LFS, issues, pull requests, or CI. A mirror copies Git branches and tags, not files outside Git.

## Develop

```sh
go test ./...
```

The integration test starts local HTTP servers and verifies authenticated push, clone, mirror sync, and refusal of pushes to a mirror.
