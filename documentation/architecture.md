# Architecture

## Components

```text
            Internet
   25/587/465 │  143/993 │      80/443
              ▼          ▼          ▼
         ┌────────┐ ┌─────────┐ ┌───────┐
         │Postfix │ │ Dovecot │ │ Caddy │──/api──▶ ┌──────────┐
         └─┬──┬───┘ └──┬───┬──┘ └───┬───┘          │ Aether   │
   milter  │  │ LMTP   │   │ SQL    │ /            │ API (Go) │
           ▼  └───────▶│   ▼        ▼              └┬──┬──┬───┘
       ┌───────┐       │ ┌─────────────┐ ┌─────┐    │  │  │
       │Rspamd │◀──────┼─│ PostgreSQL  │◀┤ web │    │  │  │ IMAP (master user, TLS)
       └──┬────┘ SASL  │ └─────────────┘ └─────┘    │  │  └───────────▶ Dovecot
          │      ◀─────┘        ▲                   │  └─ SMTP submission (TLS) ▶ Postfix
          ▼                     └───────────────────┘
       ┌───────┐  DKIM keys written by the API
       │Valkey │◀───────────────────────────────────── API
       └───────┘
```

There is a single Go binary. It is the only component that writes application data, and it is the source of truth for:

- domains, mailboxes, aliases, and app passwords
- DKIM keys
- identity providers
- sessions
- the audit log

Postfix and Dovecot do not keep their own user database. They read a small set of PostgreSQL views (`mail_domains`, `mail_users`, `mail_credentials`, `mail_aliases`, `mail_senders`) through a dedicated `aether_mail` role. That role can only `SELECT` from those views. It cannot see password hashes, OIDC secrets, or DKIM keys.

## Why Postfix, Dovecot, and Rspamd

We evaluated two options: an integrated server such as Stalwart, and the Postfix, Dovecot, and Rspamd stack used by Mailcow. We chose the second, for these reasons:

- **Maturity.** Each component has run in production for 10 to 20 years. Thunderbird, Apple Mail, Outlook, iOS, and Android all interoperate well with them.
- **Licensing.** Every component is free software that runs as a separate process, and Aether talks to each one over a network protocol:

  | Component | License |
  | --- | --- |
  | Postfix | IPL-1.0 / EPL-2.0 |
  | Dovecot | MIT / LGPL-2.1 |
  | Rspamd | Apache-2.0 |
  | Valkey | BSD-3 |
  | PostgreSQL | PostgreSQL License |

  None of them is linked into the AGPL code.
- **No feature gating.** Some integrated servers keep features behind a separate enterprise licence. Every feature Aether needs from this stack is in the free edition.
- **Separation of concerns.** Mail transport, mailbox storage, and filtering are separate processes. Each can be upgraded, scaled, or replaced on its own.

The trade-off is that we must maintain glue: SQL lookups, the master-user bridge, and DKIM key publication. Calendar and contacts (CalDAV and CardDAV) are not part of this stack. Phase 3 will add them as a separate component.

Rspamd queries DNS blocklists through a bundled Unbound recursive resolver. Spamhaus and URIBL refuse queries that arrive through large public resolvers, so using Docker's resolver would silently disable those checks.

We chose Valkey over Redis because Valkey is BSD-licensed. It is needed because Rspamd stores greylisting, rate-limit, Bayes, and DKIM key data there.

## Trust boundaries

| Boundary | Control |
| --- | --- |
| Internet → Postfix 25 | `mynetworks` is loopback only. Unauthenticated clients may relay only to Aether's own verified domains. Unknown recipients are rejected. |
| Internet → submission 587/465 | TLS is required before AUTH. SASL is checked by Dovecot against SHA-256 hashes of app passwords; Dovecot computes the hash, so the plaintext never reaches PostgreSQL. `reject_sender_login_mismatch` binds MAIL FROM to the authenticated mailbox, and an Rspamd rule rejects authenticated mail whose header From differs from MAIL FROM. |
| Internet → IMAP 143/993 | `ssl = required`, and plaintext auth is disabled without TLS. Only app passwords are accepted. Web passwords and OIDC tokens are not mail credentials. |
| Browser → API | The `aether_session` cookie is HttpOnly and SameSite=Lax, and also Secure when the public URL uses https. The database stores only a SHA-256 of the session token. Every state-changing request needs an `X-CSRF-Token` header that is bound to the session. |
| API → Dovecot / Postfix | The API connects over TLS and verifies the mail certificate against `AETHER_MAIL_TLS_CA_FILE` or the system roots. It logs in as `<mailbox>*aether-webmail` with the Dovecot master password, and Dovecot accepts that login only from private networks. The API opens a mailbox only when the signed-in user owns it. |
| API → PostgreSQL | All queries are parameterized (pgx). Tenant scope is checked on every resource lookup: a resource is loaded first, then its `org_id` is compared with the caller's memberships. Platform administrators manage tenants, but they cannot read a user's mail through the API. |
| Secrets at rest | OIDC client secrets and DKIM private keys are encrypted with AES-256-GCM using `AETHER_SECRET_KEY`. The encryption is bound to the row ID as additional data. |

## Rules, forwarding, and automatic replies

Mail rules and automatic replies are stored per mailbox in PostgreSQL. Whenever either changes, the API compiles both into one Sieve script and installs it over ManageSieve, on Dovecot's internal port 4190, using the same master-user login as webmail. Dovecot runs the script at delivery, after the global spam-to-Junk script.

Forwarding (`redirect`) and automatic replies (`vacation`) need Dovecot to send mail:

- **Sending path.** Dovecot hands the message to `msmtp`, which submits it to Postfix on internal port 10025. That port is never published, and it requires TLS and the `aether-sieve-relay` login. The relay account accepts logins only from private networks, has no mailbox, and authenticates with `AETHER_SIEVE_RELAY_PASSWORD`.
- **Forwarded mail** keeps the original headers, but its envelope sender is the forwarding mailbox. SPF therefore passes for the forwarding domain, the original sender's DKIM signature still satisfies DMARC, and delivery failures return to the forwarding mailbox.
- **Automatic replies** are sent with an empty envelope sender.
- **Header From check.** Rspamd's From-header rule exempts the relay account, because forwarded mail keeps someone else's From.

## Calendar and contacts

Aether serves CalDAV and CardDAV itself, using emersion's `go-webdav` library. Calendars and address books are rows in `dav_collections`. Events and cards are stored as iCalendar and vCard text in `dav_objects`, together with a few extracted columns: UID, ETag, summary, email addresses, start and end time, and whether the event repeats. Those columns serve the web app's JSON API and recipient suggestions without parsing every object. Sharing is a row in `calendar_shares` that grants a user or every member of an organization read or write access to one collection. A shared calendar appears in the other person's calendar home as `shared-<collection id>`, and writes to a read-only share are refused with 403.

- **Login:** DAV requests sign in with a mailbox address and an app password.
- **Ownership:** the user ID is part of every DAV path, and requests for another user's path are refused with 403.
- **Writes:** conditional updates use `If-Match` with the stored ETag.
- **Repeating events:** the JSON API expands them with `rrule-go` for the requested range.

We chose this over running Radicale or Baïkal next to Aether. Doing it in-process keeps one sign-in system and one set of tenant checks, and puts calendars and contacts in the same database backups as everything else. The trade-off is that `go-webdav`'s server is younger than those projects, which is why client compatibility is documented as untested until each client has been tried.

## Data model

The data model is defined in `migrations/0001_init.sql`:

- `organizations`, `organization_members` with roles owner, admin, and member
- `users`, `user_identities`, linking an OIDC issuer and subject to a user
- `domains`, with a verification token and an encrypted DKIM key
- `mailboxes`, each with an owner
- `aliases`, where an empty local part is the catch-all
- `app_passwords`
- `oidc_providers`, `oidc_login_states`, `sessions`, `audit_events`

Mailbox storage paths use the mailbox UUID (`/var/vmail/<id>/Maildir`), so renaming an address never moves data.

## Request flow: webmail send

1. The browser posts JSON to `/api/v1/mailboxes/{id}/send`. The request carries the session cookie and the CSRF header.
2. The API checks that the caller owns the mailbox and builds a MIME message, sanitizing the HTML.
3. The API opens SMTP with STARTTLS to Postfix on port 587. It authenticates with SASL PLAIN as `alice@example.com*aether-webmail`.
4. Dovecot reports the authenticated user as `alice@example.com`.
5. Postfix applies `smtpd_sender_login_maps`, then hands the message to Rspamd. Rspamd scores it, signs it with the domain's DKIM key from Valkey, and returns it.
6. Postfix delivers the message: through Dovecot LMTP for local recipients, or by direct MX delivery or the configured relay host for remote ones.
7. The API appends a copy to the mailbox's Sent folder over IMAP.

## Performance targets

These are design targets for a single 2 vCPU / 4 GB host, not measured results:

- Message list: p95 under 300 ms for a 50-message page in a 100,000-message folder.
- Webmail send: p95 under 1 s to queue acceptance.
- Idle memory for the complete stack: under 1.5 GB.

There is no benchmark harness yet; it is part of Phase 4. In development, the integration suite completes in about 35 s. Most of that time is Dovecot's deliberate 2 s auth-failure delay in negative tests.

## Known limitations

- The API opens one IMAP connection per request, with no pooling. Measured in October 2026, a request that uses IMAP takes about 14 ms in total, so a pool would save little. Revisit this if the mail routes' 95th percentile latency on the Aether overview dashboard goes above 100 ms.
- Login, OIDC, and webmail send rate limits are fixed one-minute windows counted in Valkey, so they hold across API replicas. If Valkey is unreachable, the API allows the request and logs an error.

## Sender Rewriting Scheme

An alias can forward to an address at another provider. Postfix relays the forwarded copy with the original envelope sender, so the receiving server would see an SPF failure. To prevent this, Postfix rewrites envelope senders outside the hosted domains to SRS addresses at the mail hostname, using postsrsd. For example, `someone@gmail.com` becomes `SRS0=hash=tt=gmail.com=someone@mail.example.com`. Bounces sent to that address are checked against the hash and routed back to the original sender. Addresses with a forged or expired hash are refused.

Senders from hosted domains are never rewritten, so DMARC alignment of normal outgoing mail is unaffected. Postfix rewrites at the moment it accepts a message, before it knows where the message goes. Mail delivered to local mailboxes therefore also shows an SRS `Return-Path` for external senders. Spam and SPF checks run before the rewrite and see the original sender.

The SRS secret is generated on first start and kept in the Postfix spool volume.
