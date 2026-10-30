# Changelog

All notable changes to Aether are recorded in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Before 1.0, a minor version may change the API or the configuration; the notes for that version say how.

## Unreleased

### Added

- SCIM 2.0 provisioning for users: an identity provider such as Halo can create, update, deactivate and deprovision Aether accounts at `/api/v1/scim/<slug>`. New accounts get a mailbox when their address is on a verified domain and can sign in through the provider straight away. Turn it on under **Administration → Identity providers**.
- IMAP and SMTP submission accept OpenID Connect access tokens through `OAUTHBEARER` and `XOAUTH2`. The API checks each token with the provider's introspection endpoint, requires that it was issued to Aether, and checks mailbox access on every login. Dovecot's image now includes `curl`, and `AETHER_API_INTERNAL_URL` (default `http://api:8080`) tells Dovecot where the API is.
- Import mail from another provider: **Settings → Import mail** signs in to any IMAP account and copies its folders into a mailbox in the background, keeping flags and dates, mapping Sent, Drafts, Junk, Trash and Archive to their Aether folders, and skipping messages already present. Imports from servers on private addresses are blocked unless `AETHER_WEBHOOK_ALLOW_PRIVATE` is `true`.
- Distribution lists: an alias created with `kind` set to `list` takes up to 5,000 destinations, including other lists and outside addresses, and has a name that appears in the organization directory. `acceptFrom` limits who may send to it: anyone, people signed in to the organization, the list's own members, or an allow-list of addresses and `@domain` entries. Postfix checks restricted lists during the SMTP session by asking the API on a new internal port, 10040 (`AETHER_POLICY_LISTEN`; Postfix connects to `AETHER_POLICY_SERVICE`, default `api:10040`), and rejects other senders with `5.7.1`. `sendAs` names the mailboxes that may send as the list, and owners may change its destinations without being administrators. Moderation and members taken from identity provider groups are not built.
- Aliases and lists can be changed in place with `PATCH /api/v1/aliases/{aliasId}`: destinations, replaced or added and removed one at a time, the active state, kind, name, sender restrictions, send-as and owners. Each change is one `alias.update` audit event with the old and new values.
- Mail rules can test the body, any named header, the size in kilobytes, and whether the mailbox's address is missing from To and Cc. Text conditions can be negated, and a rule can delete a message instead of moving it.
- Organization administrators and API keys with `mailboxes:write` can manage delegates, mail rules, automatic replies and signatures for any mailbox in the organization, and import an iCalendar or vCard file into the mailbox owner's calendar or address book with `POST /api/v1/mailboxes/{mailboxId}/calendar/import` and `/contacts/import`.
- `DELETE /api/v1/organizations/{orgId}/members/{userId}` removes a member, together with their delegated mailbox access and list ownership in that organization. The last owner cannot be removed. API keys need `members:write`.
- Folder management in Aether Mail and the API: create, rename, move, delete (with an option to move the messages elsewhere first), empty and mark all as read, for the owner and full delegates. Special-use folders cannot be renamed or deleted, and renaming a folder updates the mail rules that file into it.
- Sorting and filtering the message list by date, sent date, sender, recipient, subject or size, in either direction, with filters for unread, flagged, attachments, sender, recipient and a date range. Aether Mail remembers the choice per folder.
- Moving and copying messages between a person's own mailbox and the mailboxes they have full access to, keeping headers, the original date and flags. Moves out of or into another mailbox are recorded in the audit log.
- Branding per organization under **Administration → Branding**: name, logos for light and dark themes, favicon, sign-in message, theme colours and custom CSS. The sign-in page uses the branding of the organization that owns the domain it is served on. `AETHER_CUSTOM_CSS=false` turns custom CSS off.
- Signature templates per organization and per domain under **Administration → Signatures**, with placeholders for name, job title, phone, organization, domain and address, the organization's links, and its own logo. Job title and phone can be set under **Settings → Profile**.

### Changed

- **Breaking:** the message list pages with an opaque `cursor` instead of `before`, and `nextCursor` is now a string. Clients that page with `before` must pass the returned `nextCursor` as `cursor`.
- The organization signature template no longer contains the Scala Studios logo. Each organization sets its own.

- The SPF check evaluates the record as a receiving server would, for every address of the mail host, following `include`, `redirect`, `a`, `mx`, `ip4`, `ip6` and `exists` within the limit of 10 DNS lookups. A record that authorizes the server through an `include` or an IP address now shows **OK**. Softfail, neutral and records that cannot be evaluated show **Not strict**, and only a `fail` shows **Mismatch**.

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
