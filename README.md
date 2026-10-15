# Aether

Aether is a self-hosted email platform from Scala Studios. It combines a standard mail server (Postfix, Dovecot, Rspamd) with a Go control plane, a web administration console, and a webmail client. It runs on a single Docker host, and you sign in with a local account, [Halo](https://github.com/scalastudios/halo), or any OpenID Connect provider.

Aether is licensed under AGPL-3.0-or-later. There are no paid tiers, no telemetry, and no cloud activation.

## Status

The project is at **milestone 1**: an administrator can configure a domain, create mailboxes, and sign in. Mail then flows over SMTP, and users read it in both IMAP clients and Aether Mail. The tables below list what is implemented and what is planned.

### Implemented and covered by automated tests

| Area | What exists |
| --- | --- |
| Mail transport | **Postfix 3.7**:<br>• Inbound SMTP on port 25.<br>• Submission on 587 (STARTTLS) and 465 (implicit TLS), with SASL authentication through Dovecot.<br>• Recipient validation, aliases, and catch-all, all driven by PostgreSQL.<br>• Sender-login enforcement: a mailbox can only send as itself and as aliases that point to it, in both the envelope and the From header.<br>• SRS for mail that aliases forward to external addresses, so the forwarded copy passes SPF. |
| Mailbox access | **Dovecot 2.3**:<br>• IMAP on 143 (STARTTLS) and 993 (implicit TLS).<br>• Maildir storage, special-use folders, quotas, and LMTP delivery.<br>• A Sieve rule that files spam into Junk. |
| Filtering and signing | **Rspamd**:<br>• A milter that scores and filters spam, with DNS blocklists queried through a bundled Unbound resolver.<br>• DKIM signing with per-domain RSA-2048 keys that Aether generates.<br>• ARC signing and outbound rate limiting.<br>• Optional ClamAV virus scanning. |
| Control plane | **Go API** (`cmd/aether`):<br>• Organizations, domains, mailboxes, aliases, members, and the audit log.<br>• DNS record guidance and a live DNS check, including MTA-STS policies and TLS reporting.<br>• DMARC aggregate reports received and summarized per sending IP address.<br>• Domain ownership verification.<br>• Organization API keys with scopes, and signed webhooks for every audit event. |
| Authentication | • Local accounts with Argon2id password hashing.<br>• Account suspension from the console, and automatically from signed Halo webhooks.<br>• OpenID Connect sign-in using the authorization code flow with PKCE S256, nonce checking, and state bound to the browser.<br>• Halo and generic OIDC providers, with group-to-role mapping and optional auto-provisioning.<br>• Mailbox-scoped app passwords for IMAP and SMTP. |
| Webmail | **Aether Mail** (Next.js):<br>• Folders, paged message list, and server-side search.<br>• Sanitized HTML rendering with remote content blocked.<br>• Attachment download, and attachments in compose (upload or drag and drop, 20 MB per message); forwarding keeps the original attachments.<br>• Compose, reply, reply all, and forward, with drafts autosaved to the IMAP Drafts folder.<br>• Conversation view within a folder, using the server's THREAD=REFERENCES.<br>• Mail rules (move, mark read, flag, forward) and automatic replies, compiled to Sieve and run on the server for every client.<br>• Scheduled send and snooze.<br>• Shared mailboxes: administrators can give organization members full access to a mailbox, including sending as it and IMAP app passwords. Aliases with several destinations work as distribution lists.<br>• Flags, move, delete, and keyboard shortcuts. |
| Calendar and contacts | • CalDAV and CardDAV built into the Go server, at `/dav/` with `/.well-known` discovery. Apps sign in with mailbox app passwords.<br>• Calendars with repeating events, ICS import and export, and meeting invitations that work with Gmail, Outlook, and Apple Calendar.<br>• Calendars shared with people (view or edit) or with a whole organization, in the web app and over CalDAV.<br>• A personal address book.<br>• The organization directory.<br>• Recipient suggestions in the composer.<br>• Data lives in PostgreSQL, so it is part of every backup. |
| Administration | **Admin console**:<br>• Domains with DNS status, mailboxes, aliases, members, and the audit log.<br>• Identity providers, including a Halo setup wizard. |
| Monitoring | • Prometheus metrics for the API, Postfix, Dovecot, and Rspamd, and a Grafana dashboard.<br>• OpenTelemetry tracing for the API. |
| Deployment | • Docker Compose stack with health checks and persistent volumes.<br>• Generated secrets and self-signed TLS for development.<br>• Backups to disk and to S3-compatible storage with restic, and a restore script. |

### Planned and not implemented yet

- **Mail, next steps:** conversations that span folders (Inbox plus Sent), and separate read-only and send-as permissions for delegates.
- **Calendar and contacts, next steps:** free/busy when scheduling.
- **Files and office:** file storage and office suite integration.
- **Token-based mail login:** SASL OAUTHBEARER or XOAUTH2 for IMAP and SMTP, using Halo access tokens. App passwords are the protocol credential today.
- **SCIM provisioning:** Halo can push users to Aether over SCIM. Suspensions already arrive through Halo webhooks.
- **Operations:**
  - Kubernetes: the Helm chart validates in CI but has not yet run on a real cluster (see [documentation/kubernetes.md](documentation/kubernetes.md)).
- **API platform:** generated SDKs. The OpenAPI 3.1 specification can already be used with standard generators.

Exchange ActiveSync, EWS, and MAPI are not implemented. Do not expect Outlook to connect through Exchange protocols. Outlook works over IMAP and SMTP.

## Quick start (local development)

You need Docker with Compose v2.

```sh
./scripts/setup.sh dev
docker compose up -d --build
echo 'choose-a-long-password' | docker compose exec -T api aether bootstrap-admin --email admin@aether.test --password-stdin
```

Open http://localhost:8080 and sign in as the administrator. Then:

1. In **Administration**, create an organization.
2. Add the domain `aether.test`. Domains added by a platform administrator are verified immediately.
3. Create a mailbox with a password.
4. Sign out and sign back in as the mailbox owner to use Aether Mail.
5. In **Settings → App passwords**, create a credential for Thunderbird or another IMAP client.

The development profile uses these host ports:

| Service | Host port |
| --- | --- |
| Web | 8080 |
| SMTP | 2525 |
| Submission | 5870 (STARTTLS) and 4650 (TLS) |
| IMAP | 1430 (STARTTLS) and 9930 (TLS) |

The mail certificate is self-signed for `mail.aether.test`.

### Run the tests

```sh
go test ./...
AETHER_TEST_ADMIN_EMAIL=admin@aether.test AETHER_TEST_ADMIN_PASSWORD=choose-a-long-password \
  docker compose --profile test run --rm --no-deps integration-tests
cd apps/web && npm ci && npm run lint && npm run build
```

The integration suite runs inside the Compose network against the real Postfix, Dovecot, Rspamd, PostgreSQL, and API containers. It covers:

- Submission and DKIM signing
- Inbound delivery
- Webmail send, read, and reply
- GTUBE spam filed into Junk
- Open-relay and spoofing refusal
- App password scoping and revocation
- Tenant isolation
- CSRF
- OIDC with PKCE against a test issuer

## Production deployment

See [documentation/installation.md](documentation/installation.md). In short:

1. Point DNS at the host.
2. Run `./scripts/setup.sh` and set `AETHER_PUBLIC_URL`, `AETHER_SITE_ADDRESS`, and `AETHER_MAIL_HOSTNAME`.
3. Provide a trusted certificate for the mail hostname.
4. Run `docker compose up -d --build`.

Caddy obtains the web certificate automatically.

## Documentation

- [Architecture and decisions](documentation/architecture.md)
- [Installation](documentation/installation.md)
- [Authentication, Halo, and mail clients](documentation/authentication.md)
- [API keys and webhooks](documentation/integrations.md)
- [Third-party components and licenses](documentation/third-party.md)
- [API specification (OpenAPI 3.1)](api/openapi.yaml)

## Repository layout

```text
cmd/aether/            API server and CLI (serve, migrate, bootstrap-admin, healthcheck)
internal/              Go packages: api, auth, config, db, mail, secrets
migrations/            PostgreSQL schema, embedded in the binary
apps/web/              Next.js web app (Aether Mail, Settings, Administration)
infrastructure/docker/ Images and configuration for Postfix, Dovecot, Rspamd, Caddy, and PostgreSQL
tests/integration/     End-to-end tests against the running stack
api/openapi.yaml       REST API contract
brand/                 Scala Studios brand assets and fonts (OFL)
```

A Scala Studios project.
