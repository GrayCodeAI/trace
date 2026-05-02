# Security & Privacy

Trace stores AI session transcripts and metadata in your git repository. This document explains what data is stored, how sensitive content is protected, and how to configure additional safeguards.

## Transcript Storage & Git History

### Where data is stored

When you use Trace with an AI agent (Claude Code, Codex, Gemini CLI, OpenCode, Cursor, Factory AI Droid, Copilot CLI), session transcripts, user prompts, and checkpoint metadata are committed to a dedicated branch in your git repository (`trace/checkpoints/v1`). This branch is separate from your working branches, your code commits stay clean, but it lives in the same repository.

Trace also creates temporary local branches (e.g., `trace/<short-hash>`) as working storage during a session. These shadow branches store file snapshots and transcripts **without redaction**. They are cleaned up when session data is condensed (with redaction) into `trace/checkpoints/v1` at commit time. Shadow branches are **not** pushed by Trace — do not push them manually, as unredacted content would be visible on the remote.

Anyone with access to your repository can view the transcript data on the `trace/checkpoints/v1` branch. This includes the full prompt/response history and session metadata. Note that transcripts capture all tool interactions — including file contents, MCP server calls, and other data exchanged during the session.

If your repository is **public**, this data is visible to the trace internet.

### What Trace redacts automatically

Trace automatically scans transcript and metadata content before writing it to the `trace/checkpoints/v1` branch. Five secret detection methods run during condensation:

1. **Entropy scoring** — Identifies high-entropy strings (Shannon entropy > 4.5) that look like randomly generated secrets, even if they don't match a known pattern.
2. **Pattern matching** — Uses [Betterleaks](https://github.com/betterleaks/betterleaks) built-in rules to detect known secret formats.
3. **Credentialed URI detection** — Redacts URLs with embedded passwords, such as `scheme://user:password@host`.
4. **Database connection-string detection** — Redacts JDBC, Postgres keyword DSN, SQL Server, and ODBC-style connection strings containing passwords.
5. **Bounded credential value detection** — Redacts password-like config values such as `DB_PASSWORD=...` and `PGPASSWORD=...` while preserving the surrounding key.

Detected secrets are replaced with `REDACTED` before the data is ever written to a git object. This is **always on** and cannot be disabled.

### Recommendations

If your AI sessions will touch sensitive data:

- **Use a private repository.** This is the simplest and most complete protection. Transcripts on `trace/checkpoints/v1` are only visible to collaborators.
- **Avoid passing sensitive files to your agent.** Content that never enters the agent conversation never appears in transcripts.
- **Review before pushing.** You can inspect the `trace/checkpoints/v1` branch locally before pushing it to a remote.

## What Gets Redacted

### Secrets (always on)

Betterleaks pattern matching covers cloud providers (AWS, GCP, Azure), version control platforms (GitHub, GitLab, Bitbucket), payment processors (Stripe, Square), communication tools (Slack, Discord, Twilio), private key blocks (RSA, DSA, EC, PGP), and generic credentials (bearer tokens, basic auth, JWTs). Dedicated credentialed URI detection covers URLs that embed passwords. Additional database connection-string detection covers DB DSNs and query-parameter passwords not reliably covered by generic secret rules. Entropy scoring catches secrets that don't match any known pattern.

All detected secrets are replaced with `REDACTED`.

To reduce over-redaction, Trace preserves structural transcript fields such as IDs and paths, ignores common placeholder values, and redacts only credential values for bounded key/value forms. When a connection string contains a real (non-placeholder) password, it is redacted as a unit because partial fragments can still expose sensitive material; connection strings whose passwords are placeholders (e.g. `${DB_PASSWORD}`) are left intact.

## Limitations

- **Best-effort.** Novel or low-entropy secrets (short passwords, predictable tokens) may not be caught.
- **Filenames and binary data.** Secrets in filenames, binary files, or deeply nested structures may not be detected.
- **JSONL skip rules.** Trace skips scanning fields named `signature` or ending in `id`/`ids`, and objects whose `type` starts with `image` or equals `base64`, to avoid false positives.
- **Users are ultimately responsible** for reviewing what they commit and push. Redaction is a safety net, not a guarantee.
