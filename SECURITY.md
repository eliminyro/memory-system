# Security Policy

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

Report privately through GitHub's
[private vulnerability reporting](https://github.com/eliminyro/memory-system/security/advisories/new)
— the repository's **Security → Report a vulnerability** tab. Include:

- a description of the issue and its impact,
- steps to reproduce (or a proof of concept),
- the affected version (`/~/version` reports the running build), and
- any suggested remediation, if you have one.

You'll get an acknowledgement, and — once a fix ships — credit in the published
advisory if you'd like it.

## Supported versions

Security fixes target the latest released version. Please reproduce on a current
release before reporting.

## Scope

memory-system is an authenticated, multi-tenant service. Reports that concern the
authentication/authorization boundary, tenant isolation, secret handling, or the
HTTP edge (rate limiting, request limits, headers) are especially valued. Findings
that require an already-privileged admin credential, or that only apply to a
deliberately misconfigured deployment, are lower priority — but still worth a note.
