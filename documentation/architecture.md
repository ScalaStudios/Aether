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

Postfix and Dovecot do not keep their own user database. They read a small set of PostgreSQL views (`mail_domains`, `mail_users`, `mail_credentials`, `mail_aliases`, `mail_senders`, `mail_restricted_lists`) through a dedicated `aether_mail` role. That role can only `SELECT` from those views. It cannot see password hashes, OIDC secrets, or DKIM keys.

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

The trade-off is that we must maintain glue: SQL lookups, the master-user bridge, and DKIM key publication. Calendar and contacts (CalDAV and CardDAV) are not part of this stack. The Go API serves them, and stores their data in PostgreSQL.

Rspamd queries DNS blocklists through a bundled Unbound recursive resolver. Spamhaus and URIBL refuse queries that arrive through large public resolvers, so using Docker's resolver would silently disable those checks.

We chose Valkey over Redis because Valkey is BSD-licensed. It is needed because Rspamd stores greylisting, Bayes, outbound spam counts, and DKIM key data there, and the API counts sending limits there.

## Trust boundaries

| Boundary | Control |
| --- | --- |
| Internet → Postfix 25 | `mynetworks` is loopback only. Unauthenticated clients may relay only to Aether's own verified domains. Unknown recipients are rejected. |
| Internet → submission 587/465 | TLS is required before AUTH. SASL is checked by Dovecot against SHA-256 hashes of app passwords; Dovecot computes the hash, so the plaintext never reaches PostgreSQL. `reject_sender_login_mismatch` binds MAIL FROM to the authenticated mailbox, and an Rspamd rule rejects authenticated mail whose header From differs from MAIL FROM. |
| Internet → IMAP 143/993 | `ssl = required`, and plaintext auth is disabled without TLS. App passwords are accepted through PLAIN and LOGIN. OIDC access tokens are accepted through OAUTHBEARER and XOAUTH2: Dovecot's `checkpassword` passdb sends the address and token to the API's `/internal/mail-token`, which answers only callers on private networks, introspects the token with the provider, requires that it was issued to Aether, and checks mailbox access. Web passwords and ID tokens are not mail credentials. |
| Internet → JMAP | `/.well-known/jmap`, `/jmap`, upload, download and the event source accept an app password or an OIDC access token, and refuse session cookies and API keys. The API then opens the mailbox with the same master-user IMAP login as Aether Mail, and only for a mailbox that login may use. Calendars and contacts are the CalDAV rows, with the same read and write rights. |
| Browser → API | The `aether_session` cookie is HttpOnly and SameSite=Lax, and also Secure when the public URL uses https. The database stores only a SHA-256 of the session token. Every state-changing request needs an `X-CSRF-Token` header that is bound to the session. |
| API → Dovecot / Postfix | The API connects over TLS and verifies the mail certificate against `AETHER_MAIL_TLS_CA_FILE` or the system roots. It logs in as `<mailbox>*aether-webmail` with the Dovecot master password, and Dovecot accepts that login only from private networks. The API opens a mailbox only when the signed-in user owns it, or is a delegate or member of it, and refuses folder names under `Shared/` so that the master login never reaches another mailbox through the shared namespace. Shared mailbox rights are written over the same login by a reconciliation worker on every replica, under a two-minute lease on the mailbox row rather than inside a database transaction. Each IMAP command of that session is bounded to 30 seconds for its write and its reply, armed before the lease renewal that precedes every write, so a process whose lease was taken over, or that was paused past the limit, cannot write again and marks the mailbox for another pass. Handler-initiated rights work is bounded to 60 seconds; the worker finishes what a handler could not. |
| API → Dovecot doveadm | The doveadm HTTP API on port 8089, keyed with `AETHER_DOVECOT_API_KEY`, is used only to end IMAP sessions. Each kick is queued in `mail_kicks`, one row per address, in the transaction that revokes the access, and a kick worker on every API replica delivers it and retries until Dovecot confirms it. The addresses sent to doveadm are exact: `users.email` and `mail_kicks.identity` refuse `*` and `?`, which doveadm would treat as wildcards. Only the API and Dovecot hold the key; Postfix does not receive it. It is not published, and the Helm NetworkPolicy admits it from the API pods only. |
| Rspamd → API | Rspamd posts mail it would reject, or that scores 10 or more, to `/internal/quarantine` on the API. Like `/internal/mail-token`, the endpoint answers only callers on private networks, Caddy does not route `/internal/`, and the Helm NetworkPolicy admits Rspamd to port 8080. A message is dropped with `discard` only after the API has stored it; otherwise Rspamd keeps its own action. See [Quarantine](quarantine.md). |
| API → Postfix queue API | `aether-postfix-api` in the Postfix container, on port 8025, keyed with `AETHER_POSTFIX_API_KEY` as a bearer token compared in constant time. It runs `postqueue` and `postsuper` with fixed arguments and passes queue IDs, which must be 5 to 32 letters and digits, on standard input, never through a shell. Only platform administrators reach it through the API, which shows the envelope and never the message content, and every change is an audit event. Only the API and Postfix hold the key. It is not published, and the Helm NetworkPolicy admits it from the API pods only. |
| Message headers → sender warnings | Postfix removes every `X-Aether-Authentication` header from incoming mail on all ports, and Rspamd adds one with the submission, SPF, DKIM and DMARC results. Imports remove the header too. The API trusts it only when a message has exactly one valid copy; see [Sender warnings](sender-warnings.md#how-it-works). |
| API → PostgreSQL | All queries are parameterized (pgx). Tenant scope is checked on every resource lookup: a resource is loaded first, then its `org_id` is compared with the caller's memberships. Platform administrators manage tenants, but they cannot read a user's mail through the API. |
| Secrets at rest | OIDC client secrets and DKIM private keys are encrypted with AES-256-GCM using `AETHER_SECRET_KEY`. The encryption is bound to the row ID as additional data. |

## Rules, forwarding, and automatic replies

Mail rules and automatic replies are stored per mailbox in PostgreSQL. Whenever either changes, the API compiles both into one Sieve script and installs it over ManageSieve, on Dovecot's internal port 4190, using the same master-user login as webmail. Dovecot runs the script at delivery, after the mailbox's blocked and trusted senders script and the global spam-to-Junk script.

A rule has up to 20 conditions, matched with all or any, and at least one action. A condition tests From, To, Cc, Subject, the body, a named header, the size in kilobytes (over or under), or whether the mailbox's address is absent from To and Cc. The text conditions can be negated. The actions move the message to a folder, mark it read, flag it, add up to 20 labels, forward it to up to five addresses, keep a copy when forwarding, delete it (Sieve `discard`), and stop later rules. A rule that deletes cannot also move the message or keep a copy. Deleting a label that is a rule's only action is refused, so the rule is not left with nothing to do.

## Labels and saved searches

Labels live in `mailbox_labels`, up to 50 per mailbox. Each has a name, one of eight colors, and an IMAP keyword derived from the name the way Thunderbird derives a tag: lower case, with other characters folded to `_`. The keyword does not change when the label is renamed, so mail already tagged keeps the label. Applying a label stores that keyword on the message. Several labels can sit on one message, which stays in its folder.

Apple Mail reads a color from `$MailFlagBit0`, `$MailFlagBit1` and `$MailFlagBit2` rather than from the keyword. Aether sets those bits from the first label on the message, in label order, and leaves them alone on a message that has no Aether label. Pink has no Apple Mail color. Keywords set by other clients, including Thunderbird's `$label1` through `$label5`, are kept and shown, and Aether does not write those five itself.

A saved search lives in `saved_searches`, up to 50 per mailbox. It stores the search text, the unread, flagged and attachment filters, optional sender, recipient and dates, an optional label, and either one folder or every folder. Opening it runs that search. Sort order stays with the request. A search of one folder still groups conversations; a search of every folder does not, because a conversation can span folders and its cursor would not match a single-folder page. Renaming a folder rewrites saved searches that point at it, and deleting a folder removes the searches that point at that folder exactly.

Changing a label's color recompiles the mailbox Sieve script, so rules that add the label also set the new Apple Mail color, and rewrites the color bits on messages that already have the keyword. Deleting a label takes it off those messages, drops it from rules, and leaves saved searches in place with the label filter cleared.

Forwarding (`redirect`) and automatic replies (`vacation`) need Dovecot to send mail:

- **Sending path.** Dovecot hands the message to `msmtp`, which submits it to Postfix on internal port 10025. That port is never published, and it requires TLS and the `aether-sieve-relay` login. The relay account accepts logins only from private networks, has no mailbox, and authenticates with `AETHER_SIEVE_RELAY_PASSWORD`.
- **Forwarded mail** keeps the original headers, but its envelope sender is the forwarding mailbox. SPF therefore passes for the forwarding domain, the original sender's DKIM signature still satisfies DMARC, and delivery failures return to the forwarding mailbox.
- **Automatic replies** are sent with an empty envelope sender.
- **Header From check.** Rspamd's From-header rule exempts the relay account, because forwarded mail keeps someone else's From.
- **Aggregate reports.** The same relay carries DMARC aggregate reports and TLS reports. Rspamd signs them because the relay login is authenticated and the From domain is one of Aether's verified domains. After a collector accepts a report, or rejects it permanently, the API records that destination in `report_deliveries`, so another API process does not send it again.

## Spam filtering and learning

Rspamd scores every incoming message, and the global Sieve script that Dovecot runs before a mailbox's own rules files a message marked `X-Spam: Yes` into Junk. It leaves the message in the inbox when the sender is trusted and the message is not clearly malicious:

- **Trusted** means the From address is in the recipient's contacts, or the message replies (`In-Reply-To`) to a message in the recipient's Sent folder. Dovecot asks the API on `POST /internal/trusted-sender`, which reads the address book of the mailbox owner and searches the Sent folder over IMAP. Shared mailboxes have no owner, so only replies count for them.
- **Clearly malicious** means a score of 10 or more (Rspamd rejects at 15), or `dmarc=fail` in Rspamd's `Authentication-Results`, which is how a forged contact address shows up. Those always go to Junk, and so does everything when the API cannot be reached.

The filter learns from people's actions in every mail app through Dovecot's `imapsieve` plugin. Copying or moving a message into Junk runs `learn-spam.sieve`; moving it out of Junk to any folder except Trash runs `learn-ham.sieve`. Both pipe the message to the API on `POST /internal/spam-learn`, which posts it to Rspamd's controller on port 11334 (`/learnspam` or `/learnham`) with `AETHER_RSPAMD_CONTROLLER_PASSWORD`. Bayes statistics live in Valkey. **Report spam** and **Not spam** in Aether Mail move the message to Junk or to the Inbox, so they take the same path. Administrators turn learning off for an organization under **Administration → Spam filter**, and the API then counts the move without sending it to Rspamd.

Both internal endpoints answer only callers on private addresses, and the reverse proxy does not route `/internal/`.

### Forwarding to outside addresses

Each organization chooses whether mail rules may forward outside it, under **Administration → Forwarding** or with `GET` and `PUT /api/v1/organizations/{orgId}/forwarding` (`{"mode": "allowed" | "domains" | "blocked", "domains": [...]}`). Addresses on the organization's own domains are always allowed. In `domains` mode only exact matches of the listed domains are allowed; a subdomain needs its own entry. The default is `allowed`.

The policy is enforced in three places:

- **Saving rules.** The API refuses a rule that forwards to a blocked address with `422 forwarding_blocked`.
- **Sieve.** Blocked destinations are left out of the compiled script. Changing the policy recompiles the script of every active mailbox with a rule that forwards outside the organization. A rule that only forwarded to blocked addresses then keeps the message in the mailbox.
- **Postfix.** The sieve relay port asks the API's policy service (port 10040) about each recipient. Mail whose envelope sender is a hosted mailbox and whose recipient that mailbox's organization does not allow is rejected with `5.7.1`, so a stale script cannot forward either. Mail with an empty envelope sender, such as automatic replies, is not checked.

Every saved rule change records `rule.forwarding.add` or `rule.forwarding.remove` per destination in the audit log and webhooks, with `address`, `rule` and `external`. Policy changes record `forwarding.policy.update`. The Forwarding tab lists every rule that forwards outside the organization, and whether the policy lets it run.

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
- `domains`, with a verification token, and `dkim_keys`, with encrypted private keys and selector lifecycle state
- `mailboxes`, each with an owner, or with `kind` `shared` and no owner, plus the pending flag, generation, lease, attempt count and next-attempt time that drive the Dovecot rights reconciliation; its foreign keys restrict the deletion of its domain and organization; `mailbox_delegates` holds delegates and members with their access level, `send_as` and `send_on_behalf`; `mailbox_labels` and `saved_searches` hold the labels and saved searches for Aether Mail
- `aliases`, where an empty local part is the catch-all, and `kind` is `alias` or `list`; `alias_owners` holds the owners of lists, `alias_groups` the groups whose members a list includes, and `member_domains` the domains whose mailboxes it includes
- `directory_groups` and `directory_group_members`, groups pushed by an identity provider over SCIM and the accounts in them
- `app_passwords`
- `mail_kicks`, IMAP session kicks waiting for Dovecot to confirm them, one row per address with a generation that each new revocation increments
- `quarantine_messages`, one stored copy of each held message with its category, score and Rspamd symbols; `quarantine_items`, one row per mailbox it was held for, with its status and who requested or released it; `quarantine_policies` per organization; `quarantine_allowed_senders` per mailbox
- `retention_policies`, retention policies for an organization or one mailbox, and `retention_runs`, when each mailbox is next due and the lease of the replica applying them
- `oidc_providers`, `oidc_login_states`, `sessions`, `audit_events`
- `organizations.smime_policy`, `openpgp_policy` and `password_policy`, each `off`, `allow` or `require`
- `encryption_keys`, one wrapped private key and its public certificate or key per user, address and protocol; the private key is ciphertext produced in the browser
- `peer_keys`, public keys a user saved from a message, an Autocrypt header or a Web Key Directory lookup
- `protected_messages`, the ciphertext of a password-protected message, the salt, and the hashes of the access token and the reply proof

Mailbox storage paths use the mailbox UUID (`/var/vmail/<id>/Maildir`), so renaming an address never moves data.

## Request flow: webmail send

1. The browser posts JSON to `/api/v1/mailboxes/{id}/send`. The request carries the session cookie and the CSRF header.
2. The API checks that the caller owns the mailbox, or may send as it or on its behalf, and that the chosen From address is one they may use (see [Sending from aliases and lists](#sending-from-aliases-and-lists)). It builds a MIME message, sanitizing the HTML. On behalf, it adds a `Sender` header with the caller's own address. S/MIME and OpenPGP are already sealed in the browser; the API checks the organization's encryption policy and forwards that body without opening it. A password-protected message is also sealed in the browser. The API stores the ciphertext and mails a link. See [Encryption](encryption.md).
3. The API opens SMTP with STARTTLS to Postfix on port 587. It authenticates with SASL PLAIN as `alice@example.com*aether-webmail`, the mailbox the message is sent from, and gives the chosen From address, such as `support@example.com`, as MAIL FROM.
4. Dovecot reports the authenticated user as `alice@example.com`.
5. Postfix applies `smtpd_sender_login_maps`, then hands the message to the API's first milter, which runs the organization's [mail flow rules](mail-flow-rules.md), and then to Rspamd. Rspamd scores it, signs it with the domain's active RSA key and optional Ed25519 key from Valkey, and returns it. The API's second milter runs the rules that test the spam score.
6. Postfix delivers the message: through Dovecot LMTP for local recipients, or by direct MX delivery or the configured relay host for remote ones.
7. The API appends a copy to the mailbox's Sent folder over IMAP, or to the shared mailbox a list names for its sent copies.

## Live mail and reminders

Aether Mail keeps a server-sent event stream open at `GET /api/v1/events`. One API process holds the Valkey leader lock and runs IMAP IDLE for the folders people have asked to be told about, plus the folder they have open, at most eight folders per mailbox. It publishes each change on `aether:notify`, and every replica writes it to the streams connected to that process. New unread mail can also go out as Web Push. Calendar reminders are `VALARM`s on the event, delivered on the same loop. [Notifications](notifications.md) covers the settings, the keys, and what iOS Mail still needs.

## Performance targets

These are design targets for a single 2 vCPU / 4 GB host, not measured results:

- Message list: p95 under 300 ms for a 50-message page in a 100,000-message folder.
- Webmail send: p95 under 1 s to queue acceptance.
- Idle memory for the complete stack: under 1.5 GB.

There is no benchmark harness yet; it is part of Phase 4. In development, the integration suite completes in about 35 s. Most of that time is Dovecot's deliberate 2 s auth-failure delay in negative tests.

## Known limitations

- The API opens one IMAP connection per request, with no pooling. Measured in October 2026, a request that uses IMAP takes about 14 ms in total, so a pool would save little. Revisit this if the mail routes' 95th percentile latency on the Aether overview dashboard goes above 100 ms. Live mail adds up to eight long-lived IDLE connections per mailbox, counted inside Dovecot's per-user connection limit.
- Login, OIDC, and webmail send rate limits are fixed one-minute windows counted in Valkey, so they hold across API replicas. If Valkey is unreachable, the API allows the request and logs an error.

## Sender Rewriting Scheme

An alias can forward to an address at another provider. Postfix relays the forwarded copy with the original envelope sender, so the receiving server would see an SPF failure. To prevent this, Postfix rewrites envelope senders outside the hosted domains to SRS addresses at the mail hostname, using postsrsd. For example, `someone@gmail.com` becomes `SRS0=hash=tt=gmail.com=someone@mail.example.com`. Bounces sent to that address are checked against the hash and routed back to the original sender. Addresses with a forged or expired hash are refused.

Senders from hosted domains are never rewritten, so DMARC alignment of normal outgoing mail is unaffected. Postfix rewrites at the moment it accepts a message, before it knows where the message goes. Mail delivered to local mailboxes therefore also shows an SRS `Return-Path` for external senders. Spam and SPF checks run before the rewrite and see the original sender.

The SRS secret is generated on first start and kept in the Postfix spool volume.

## Distribution lists

A distribution list is an alias whose `kind` is `list`. It needs a local part, so a catch-all cannot be a list, and it has a name that appears in the organization directory. The two kinds differ in what they allow:

| | Alias | List |
| --- | --- | --- |
| Destinations | Up to 100 | Up to 5,000, including other lists and outside addresses, plus identity provider groups and whole domains |
| Who may send to it | Anyone | `acceptFrom`: `anyone`, `organization`, `members` or `allowlist` |
| Who may send as it | Every destination mailbox | The mailboxes in `sendAs`, which must belong to the organization |
| Who may change it | Organization administrators | Administrators, and the owners for the destinations only |

Owners must be members of the organization. `GET /api/v1/me/lists` returns the lists the signed-in user owns, and `PATCH /api/v1/aliases/{aliasId}` with `destinations`, `addDestinations` or `removeDestinations` is the one change an owner may make. The `mail_senders` view gives a list's `sendAs` mailboxes the right to send as it, and still gives that right to every destination mailbox of a plain alias.

Sender restrictions are enforced by Postfix during the SMTP session, on port 25 and on submission. `smtpd_recipient_restrictions` looks the recipient up in `mail_restricted_lists`, which holds only active lists whose `acceptFrom` is not `anyone` on active, verified domains; mail to any other address is not checked. For a restricted list, Postfix asks the API's policy service with `check_policy_service`. The API listens on `AETHER_POLICY_LISTEN` (default `:10040`), and Postfix connects to `AETHER_POLICY_SERVICE` (default `api:10040`). The decision uses the envelope sender, the recipient and the SASL login:

- `organization`: the session authenticated, and the sender's domain belongs to the list's organization.
- `members`: the sender is one of the list's destinations, or one of its members through a group or a domain.
- `allowlist`: the sender, or `@` followed by its domain, is in `allowedSenders`.

For `members` and `allowlist`, a sender on a domain that Aether hosts counts only when the session authenticated, so an outside server cannot claim a hosted address to reach a list. Mail with an empty envelope sender, such as a bounce, is rejected by every restricted list. The rejection is `5.7.1 <list> does not accept mail from this sender`, given while the sender is still connected. If the API cannot query the database, it asks Postfix to defer the message instead.

Authenticated submission also asks that policy service, with `policy_context=suppression`, before accepting a recipient. The API looks up the envelope sender's active verified domain and rejects the recipient with `550 5.7.1` when that organization's suppression list contains the address. Inbound mail on port 25 is not checked, so a suppressed address can still receive mail. The list and the delivery events are described in [API keys, webhooks, and suppressions](integrations.md).

### Members from groups and domains

Besides the addresses added by hand, a list's members can come from two places, both resolved when mail is delivered, so people join and leave with no change to the list:

- `groups`: groups that an identity provider keeps in Aether over SCIM (see [SCIM provisioning](authentication.md#scim-provisioning)). Each group member's personal mailboxes in the list's organization receive the mail. Deprovisioned accounts and inactive mailboxes are left out; a suspended account keeps receiving mail, as it does for its own address.
- `memberDomains`: domains of the list's organization. Every active personal mailbox on them receives the mail, including mailboxes created later.

A list needs at least one destination, group or domain, and only administrators can change its groups and domains. The `list_dynamic_members` view resolves both, and `mail_aliases` adds its addresses to the list's own destinations, so Postfix expands the list in one lookup. `GET /api/v1/aliases/{aliasId}/members` returns every address the list reaches right now, with how each one got there, to administrators and the list's owners; **Show members** on the list in **Administration → Aliases** displays it. `GET /api/v1/organizations/{orgId}/directory-groups` lists the groups an administrator can choose: those with a mailbox in the organization, or already used by one of its lists. API keys need `aliases:read` for both.

### Moderation

A list with `moderated` set holds messages until a moderator approves them. The moderators are the people in `moderators`, or the owners when that is empty, and a moderated list needs at least one of either. The moderators' and owners' own addresses and mailboxes, and the addresses and `@domain` entries in `moderationExempt`, skip moderation.

- **Routing.** For a moderated list, `mail_aliases` resolves the address to `list-<id>@aether-reports.internal` instead of its members, so Postfix hands every message for the list to the API's LMTP receiver (`AETHER_REPORT_LISTEN`, default `:2424`), the same receiver as DMARC reports. No Postfix setting changed. Migration `0039a` rebuilds the view after the one that adds members from groups and domains, so a moderated list routes to the API and leaves its group and domain members out of the view, and an unmoderated list keeps resolving them in Postfix.
- **Spoofing.** A moderated list is also in `mail_restricted_lists`, and the policy service rejects a sender on a hosted domain that did not authenticate. A moderator's address therefore cannot be claimed from outside to skip moderation. Senders outside Aether reach the API rewritten by SRS; the API reads the original address back from an `SRS0` address to check it against `moderationExempt`, and never treats a decoded address on a hosted domain as exempt.
- **Delivery.** An exempt message is stored in `held_messages` as already approved before the LMTP receiver answers, and an approved one stays there, so Postfix never has to retry it. The API then submits it on internal port 10025 with the `aether-sieve-relay` login and the original envelope sender to the list's members as they are at that moment: its own destinations plus everyone `list_dynamic_members` resolves from its groups and domains. Recipients go in batches of 500, one SMTP transaction each, and after every batch Postfix accepts, the accepted recipients, and those it refused with a 5xx, are added to the row's `delivered` list in the database. A batch that fails, or a recipient Postfix defers with a 4xx, is retried by the scheduler with the members that are not in `delivered` yet, after 5 minutes and up to an hour between attempts; after 30 attempts the message is dropped and the sender told. A row is claimed for 15 minutes while it is being delivered, so two API processes never deliver it at once. The API adds `X-Aether-List: <list>` and drops a message that already carries its own list's header, so lists that contain each other cannot loop. The API needs `AETHER_SIEVE_RELAY_PASSWORD` (and `AETHER_SIEVE_RELAY_ADDR`, default `postfix:10025`); without it, a list cannot be made moderated.
- **Holding.** Any other message is stored in `held_messages` with its sender, From, subject and a text preview, and the moderators get an email from the list's address with links to **Settings → Messages waiting for approval**. The links only open the page; approving or rejecting is a separate click, so a link scanner cannot decide for anyone.
- **Deciding.** `GET /api/v1/me/held-messages` lists what the signed-in person can moderate, `GET /api/v1/held-messages/{heldId}/raw` downloads the original, and `POST /api/v1/held-messages/{heldId}/approve` or `/reject` (with an optional `reason`) decides. The row is locked while the decision is recorded, so two moderators cannot both decide. Approving answers `204` once every member was reached, or `202` with `{"status": "queued"}` when part of the delivery is left to the scheduler. Each decision is an `alias.message_approve` or `alias.message_reject` audit event. A rejection tells the sender, with the reason.
- **Expiry.** A message nobody decides on is deleted five days after it arrived, and the sender is told. Notices go out with an empty envelope sender, and a held bounce, which has no sender, gets none.

When moderation is turned off, the list goes back to delivering directly; messages already held stay until someone decides or they expire.

Delivery is at least once per member, not exactly once. If the connection to Postfix drops after Postfix has queued a batch but before its `250` reply arrives, or the API stops between that reply and the database update, the batch is sent again on the next attempt and those members get the message twice. SMTP gives the client no way to ask whether a transaction it lost the reply for was queued.

## Allowed and blocked senders

Administrators keep one list of allowed and blocked senders for the organization, and optionally one per domain, under **Administration → Senders** or through `/api/v1/organizations/{orgId}/sender-lists` (API keys need the `senders` scope). An entry is an email address, a domain, which also matches its subdomains, or an IPv4 or IPv6 address or range, with an optional note and expiry date. People keep a personal list per mailbox under **Settings → Blocked and trusted senders**, from the Block button in the message view, or through `/api/v1/mailboxes/{mailboxId}/sender-lists` (API keys need `mailboxes`); personal entries are addresses and domains without expiry. Adding a value that is already listed in the same place replaces its entry. Every change is an audit event, `sender_list.add` or `sender_list.remove`, whose target is the organization or the mailbox.

Organization and domain lists apply to mail arriving on port 25 from servers that did not authenticate. A block anywhere wins over an allow:

- **During the SMTP session.** For each recipient, Postfix asks the API's policy service with `policy_context=senders`. The API compares the envelope sender and the client IP address with the entries for the organization that owns the recipient's domain and for that domain, and rejects a blocked sender with `5.7.1 Mail from this sender is blocked by the recipient's organization`. This check fails open: if the API or the database cannot answer, the message is accepted and filtered as usual.
- **After scanning.** An Rspamd postfilter sends the envelope sender, the header From, the client address and the recipients to the API's `/internal/sender-lists/check`, which answers only callers on private networks. A verdict applies only when every recipient domain agrees. A block, which now also covers the header From, rejects the message. An allow sets the action to `no action`, so the message skips the spam score and greylisting, but not virus scanning, which runs first. An allowed address or domain counts for the header From only when the message passes DMARC, and for the envelope sender only when it passes SPF, so a forged sender cannot use an allow; allowed IP addresses need neither. No allow counts when the sender's domain publishes a DMARC `reject` policy that the message fails. If the API cannot be reached, the message is filtered as usual.

Personal lists are compiled into a second Sieve script for the mailbox, `aether-senders`, installed over ManageSieve without being activated. Dovecot runs it first, from `sieve_before`, then the spam-to-Junk script, then the person's rules. A blocked sender's message is discarded and nothing else runs, so it gets no automatic reply. A trusted sender's message has its `X-Spam` header removed with the `editheader` extension, so the spam-to-Junk script leaves it in the inbox and the person's rules still run. Both tests compare the header From and the envelope sender. A personal list cannot rescue a message that Rspamd already rejected.

Spam thresholds per mailbox are not built.

## Retention

Organization administrators set retention policies under **Administration → Retention**, or with `/api/v1/organizations/{orgId}/retention-policies` and `/api/v1/retention-policies/{policyId}`. A policy applies to all mailboxes of the organization or to one mailbox, and is one of two kinds:

- **Delete after** removes messages older than a number of days from one folder, such as `INBOX`, `Sent`, `Archive` or `Clients/Acme`. Subfolders are not included. In Trash and Junk, age counts from when a message was moved there; in other folders, from when it arrived. A policy without days never deletes, which exempts a mailbox from a policy for all mailboxes.
- **Keep for** stops retention from deleting messages younger than a number of days, in every folder.

A mailbox's policy for a folder replaces the organization's policy for the same folder. Without either, Trash and Junk are emptied of messages older than 30 days, as Dovecot's `autoexpunge` used to do. When several keep policies apply, the longest counts. Every change is an audit event: `retention_policy.create`, `retention_policy.update` or `retention_policy.remove`. Aether Mail shows the policy for the open folder above the message list, from `GET /api/v1/mailboxes/{mailboxId}/retention`.

The API applies the policies once a day per active mailbox on an active, verified domain. Each replica claims one due mailbox at a time with a 30-minute lease in `retention_runs`, which never locks rows in `mailboxes`, and for each folder asks Dovecot's doveadm HTTP API to `expunge` with a search query: `savedbefore` for Trash and Junk, `before` for other folders, and `before` with the keep days when a keep policy applies. A folder that does not exist is skipped. If Dovecot cannot be reached, the mailbox is tried again an hour later. A change to a policy schedules the affected mailboxes straight away.

Keep for limits what retention deletes. It does not stop a person, or an IMAP client, from deleting a message themselves; those deletions are permanent until deleted mail can be recovered.

## Sending from aliases and lists

The From menu in Aether Mail offers each mailbox a person may send from, and with it every alias and list that `mail_senders` lets that mailbox send as: the aliases that forward to it and the lists that name it in `sendAs`. These appear only under mailboxes the person may sign in to with an app password, their own and user mailboxes where they have full access with Send as. Catch-all aliases are not offered. `GET /api/v1/me/mailboxes` lists them as `fromAddresses`, and `fromAddress` on a send, draft or scheduled message picks one; any other address answers `403 cannot_send`.

Each address has its own From name and signatures, kept per mailbox in `mailbox_signatures` and read and written with `fromAddress` on `/api/v1/mailboxes/{mailboxId}/signatures`. An empty From name falls back to the name an administrator gave the alias or list, or to the mailbox's display name for its own address. The organization signature template fills in the chosen address and name, and uses the template of that address's domain.

A reply starts from the address the message was delivered to, when the person may send from it, and otherwise from the first address in To, Cc or Bcc they may send from. Postfix delivers to Dovecot over the `mailbox-lmtp` transport, an `lmtp` service with `flags=O` and a recipient limit of one, so each copy carries an `X-Original-To` header with the address it was sent to before alias expansion. That finds the alias even when the sender put it in Bcc.

Sent copies go to the Sent folder of the mailbox the message was sent from. A list can instead name a shared mailbox of its organization (`sentMailboxId`), so that everyone who sends as the list sees one history. A scheduled message keeps its From address and is checked again when it goes out; if its sender can no longer use that address, it is not sent and shows an error.

## Sending limits and restricted senders

Postfix asks the API's policy service (the same one on port 10040 that checks distribution lists) about every recipient of mail sent through submission (ports 587 and 465), with `policy_context=sending`. Aether Mail and scheduled messages go through submission too, so they count. The limits belong to the account that signs in to send: a delegate who sends from a shared mailbox in a mail app counts against their own mailbox, while Aether Mail signs in as the shared mailbox and counts against it. Mail relayed by Sieve rules on port 10025 is not counted.

| Limit | Default | When exceeded |
| --- | --- | --- |
| `messagesPerHour` | 200 | The account is restricted |
| `messagesPerDay` | 1,000 | The account is restricted |
| `recipientsPerMessage` | 100 | Further recipients of that message get `452 4.5.3`; the account is not restricted |
| `recipientsPerDay` | 2,000 | The account is restricted |
| `newRecipientsPerDay` | off | The account is restricted |
| `spamPercent` | 50 | The account is restricted, once it has sent 10 messages that day |

Administrators set limits under **Administration → Sending** or with `PUT /api/v1/organizations/{orgId}/sending-limits`, for the organization, a domain or a mailbox. A mailbox uses its own value first, then its domain's, then the organization's, then the default. 0 means no limit. Hours and days are fixed UTC windows counted in Valkey. A message counts when Postfix asks about its first recipient, so a message refused later still counts. First-time recipients are addresses the mailbox has never sent to before, kept in `sending_recipients`. Every mailbox starts with none recorded, so the limit is off by default.

Rspamd counts, per signed-in account and day, outgoing messages whose verdict is `add header`, `rewrite subject` or `reject`, in `aether:sending:spam:<login>:<day>`. The policy service compares that with the day's message count when the next message starts. A message rejected because its From header does not match the sender is not counted. The GTUBE test string makes Rspamd skip every rule, including this count.

A restricted account gets `5.7.1` for every recipient until an administrator releases it from the console or with `DELETE /api/v1/mailboxes/{mailboxId}/sending-restriction`. Releasing it resets its counts for the current hour and day. The account can still sign in, receive and read mail. When an account is restricted, or passes 80% of a limit (once per hour or day window), the API records `mailbox.sending_restricted` or `mailbox.sending_limit_near` in the audit log, which also sends them to webhooks subscribed to those events. It also puts a notice in the inbox of each owner and administrator who has a mailbox. If Valkey or the database is unreachable, Postfix gets `DEFER_IF_PERMIT` and the client tries again later.
