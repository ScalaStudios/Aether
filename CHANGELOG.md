# Changelog

All notable changes to Aether are recorded in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Before 1.0, a minor version may change the API or the configuration; the notes for that version say how.

## Unreleased

### Added

- IMAP and SMTP submission accept OpenID Connect access tokens through `OAUTHBEARER` and `XOAUTH2`. The API checks each token with the provider's introspection endpoint, requires that it was issued to Aether, and checks mailbox access on every login. Dovecot's image now includes `curl`, and `AETHER_API_INTERNAL_URL` (default `http://api:8080`) tells Dovecot where the API is.
- Import mail from another provider: **Settings → Import mail** signs in to any IMAP account and copies its folders into a mailbox in the background, keeping flags and dates, mapping Sent, Drafts, Junk, Trash and Archive to their Aether folders, and skipping messages already present. Imports from servers on private addresses are blocked unless `AETHER_WEBHOOK_ALLOW_PRIVATE` is `true`.

## 0.1.0 - 2026-10-16

The first release. Container images are published to GitHub Container Registry as `ghcr.io/scalastudios/aether-<component>:0.1.0` for `api`, `web`, `postfix`, `dovecot`, `rspamd` and `unbound`.

### Added

- A mail server on one Docker host: Postfix for inbound SMTP on port 25 and submission on 465 and 587, Dovecot for IMAP on 143 and 993, and Rspamd as a milter, all configured from PostgreSQL by the Go API.
- Domains, mailboxes, aliases, catch-all addresses, distribution lists and quotas, managed per organization, with domain ownership verification.
- Sender-login enforcement on both the envelope and the From header, SRS for aliases that forward outside, and refusal of open relaying.
- Spam filtering with DNS blocklists through a bundled Unbound resolver, spam filed into Junk, DKIM signing with a 2048-bit RSA key per domain, ARC signing, outbound rate limits, and optional ClamAV scanning.
- DNS records for every domain with a live check, MTA-STS policies, TLS reporting, DMARC aggregate reports summarized per sending address, and DANE checks on outbound mail.
- Mail app setup: Thunderbird autoconfig, Autodiscover XML, RFC 6186 SRV records, and an Apple configuration profile with mail, calendar and contacts accounts.
- Aether Mail: folders, conversations, server-side search, rich-text compose with a formatting toolbar, signatures with an organization template and inline images, attachments, drafts, scheduled send, snooze, keyboard shortcuts, and HTML mail rendered with remote content blocked.
- Mail rules and automatic replies compiled to Sieve, and shared mailboxes with full or read-only access.
- Calendar and contacts in the web app and over CalDAV and CardDAV: repeating events, ICS import and export, meeting invitations, shared and organization calendars, free/busy, a personal address book and the organization directory.
- Sign-in with local accounts (Argon2id) or any OpenID Connect provider with PKCE, a Halo setup wizard, group-to-role mapping, accounts created on first sign-in, suspension through signed Halo webhooks, and profile pictures imported from the identity provider.
- Mailbox-scoped app passwords for IMAP, SMTP, CalDAV and CardDAV.
- An admin console with organizations, domains, mailboxes, aliases, members, identity providers, DMARC reports, API keys, signed webhooks and an audit log.
- A REST API described by an OpenAPI 3.1 document, and organization API keys with scopes.
- Prometheus metrics for the API, Postfix, Dovecot and Rspamd with a Grafana dashboard, and OpenTelemetry tracing for the API.
- Backup and restore scripts, with encrypted off-site copies to S3-compatible storage through restic.
- A Helm chart that validates in CI.
