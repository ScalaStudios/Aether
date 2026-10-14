# Security policy

## Reporting a vulnerability

Do not open a public issue. Use GitHub's private vulnerability reporting on this repository.

Include:

- the affected version or commit
- steps to reproduce
- the impact

You will get an acknowledgement within 3 working days. Each report gets a fix or a documented mitigation before it is disclosed.

## Supported versions

Aether has not reached 1.0. Only the latest commit on `main` receives security fixes.

## Scope notes

These areas are in scope and are of particular interest:

- **Cross-tenant access:** reading or changing another organization's domains, mailboxes, or mail.
- **Relay abuse:** sending mail as an address the authenticated mailbox does not own, or relaying without authentication.
- **Authentication bypass:** in the OIDC flow (state, nonce, PKCE), in sessions, or in the CSRF checks.
- **Unsafe email rendering:** script execution or remote content loading from a received message in Aether Mail.
- **Credential exposure:** secrets, tokens, or app passwords appearing in logs, API responses, or the database in plaintext.

The threat model and trust boundaries are described in `documentation/architecture.md`.
