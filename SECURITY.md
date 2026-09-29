# Security policy

Trace is a self-hosted Git forge. It stores source code, access grants,
personal-token hashes, CI secrets, and an administrator token, so we treat
vulnerability reports as a priority.

## Supported versions

Trace is pre-1.0 alpha software. Only the latest release (and `main`)
receives security fixes. The current version is in [`VERSION`](VERSION).

## Reporting a vulnerability

Please do not open a public issue, pull request, or discussion for a
security problem. Report it privately through either channel:

- GitHub private vulnerability reporting:
  <https://github.com/GrayCodeAI/trace/security/advisories/new>
- Email: `security@graycodeai.com`

Include what you can of:

- the affected component or route and the Trace version or commit;
- steps to reproduce, ideally a minimal proof of concept;
- the impact you expect (for example, which role is needed and what it
  gains);
- any suggested fix.

We aim to acknowledge a report within 3 business days and to agree on a
disclosure timeline with you once we have assessed it. Reporters are
credited in the advisory unless they prefer otherwise. Please give us a
reasonable chance to release a fix before disclosing publicly; we will not
pursue legal action against good-faith research that follows this policy.

## Scope

In scope: the `trace` binary and everything in this repository, including
the web UI, the JSON API, Git smart HTTP and SSH transports, the CI runner,
OIDC, SCIM, webhooks, federation, and the release workflow.

Out of scope: vulnerabilities in Git, Docker, the operating system, or a
reverse proxy in front of Trace; findings that require an administrator to
attack their own node; and denial of service that needs more traffic than
the documented rate limits allow.

## Operating Trace safely

The README describes the security model. In short: keep the data directory
private (it holds `data/admin-token`, token hashes, and CI secrets), put a
TLS reverse proxy in front of Trace, and run untrusted repositories' CI only
with sandboxing.
