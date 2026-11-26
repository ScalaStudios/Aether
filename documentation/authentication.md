# Authentication

Aether has two kinds of credentials, and they are deliberately kept separate:

1. **Web sign-in** for Aether Mail and the administration console. This is a local password or an OpenID Connect provider.
2. **Protocol credentials** for IMAP and SMTP clients. These are mailbox-scoped app passwords.

Signing in to the browser through OIDC does not let a desktop mail client in. Aether never accepts an ID token, or a user's web password, as an IMAP or SMTP credential. It does accept an OIDC access token issued to Aether, as [Access tokens instead of app passwords](#access-tokens-instead-of-app-passwords) describes.

## Halo

Halo publishes the following, which Aether uses:

- **OIDC discovery** at `<issuer>/.well-known/openid-configuration`.
- **The authorization code flow** with PKCE S256. PKCE is required for SPA and native clients and optional for web clients; Aether always sends it.
- **Claims:**
  - `sub` always.
  - `email` and `email_verified` with the `email` scope.
  - `name` and `preferred_username` with the `profile` scope.
  - `groups` (group names) with the `groups` scope. The application must also allow that scope.
- **RP-initiated logout** at `end_session_endpoint`.

Halo does not publish role or tenant claims, so Aether maps Halo groups to Aether roles.

### Register Aether in Halo

1. In Halo's administration, create an application with protocol **oidc** and type **web**. Web clients get a client secret.
2. Set these URIs:
   - **Redirect URI:** `https://<aether-host>/api/v1/auth/oidc/<slug>/callback`. The Aether wizard shows the exact value.
   - **Post-logout URI:** `https://<aether-host>/login`
3. Set the scopes to `openid profile email groups`.
4. Assign the users or groups who may use Aether. Halo refuses to issue tokens to users who are not assigned.

### Connect it in Aether

1. In Aether, go to **Administration → Identity providers → Connect Halo**.
2. Enter the issuer URL. Aether fetches the discovery document and shows whether PKCE S256 and the `groups` claim are available.
3. Enter the client ID and client secret. The secret is encrypted at rest.
4. Optionally configure provisioning:
   - **Auto-provision:** creates an Aether account on first sign-in. If the user's verified email belongs to a verified Aether domain, it also creates that mailbox and adds the user to the domain's organization. An address containing `*` or `?` is never provisioned; that sign-in fails with `not_provisioned`. The new mailbox gets the domain's or organization's default quota. If the organization is at its mailbox or storage limit, the sign-in fails with `limit_reached` and no account is created.
   - **Administrator group:** members of this Halo group become platform administrators.
   - **Group mappings:** members of a Halo group get the mapped role (any built-in role such as `owner`, `admin`, `domain_admin`, `helpdesk`, `list_admin`, `auditor` or `member`, or a custom organization role) with optional domain scopes in an organization. See [Roles and permissions](roles.md).

Group mappings only grant or upgrade roles, and combine domain scopes across matching mappings for the same role. They never remove a role. To revoke access, do it in Aether, or suspend the user in Halo.

### Account matching

On sign-in, Aether looks for the account in this order:

1. An existing link between this provider and the user's `sub`.
2. If the provider says the email is verified (`email_verified`), an existing account with the same email. The two are then linked.
3. If auto-provisioning is on, a new account.

Otherwise sign-in fails with `not_provisioned`. Aether never stores Halo passwords.

### Disabled and deprovisioned users

- Halo rejects suspended or deprovisioned users at its token and userinfo endpoints, so they cannot start a new Aether session.
- Aether sessions created through OIDC expire after 12 hours.
- Platform administrators can suspend or restore any account under **Administration → Members**. Organization owners, administrators and helpdesk can also suspend, restore and reset passwords for single-organization accounts within their role's authority (see [Roles and permissions](roles.md#security-boundaries)). Suspension ends every web session at once, blocks sign-in, makes Dovecot reject the account's app passwords, which stops IMAP and SMTP, ends its open IMAP sessions, including sessions opened for delegated mailboxes with an app password or an access token, and removes it from the shared mailboxes it belongs to until it is restored. Mail keeps arriving in the account's mailboxes. An account that was deprovisioned by converting its mailbox to a shared mailbox cannot be restored here: `PATCH /api/v1/admin/users/{id}` and `PATCH /api/v1/organizations/{orgId}/members/{userId}/status` answer `409 mailbox_converted`, and only converting the mailbox back restores it.

To have Halo suspensions take effect in Aether immediately, connect Halo's webhooks:

1. In Halo, create a webhook that points at the **Webhook URL** shown in Aether's identity provider form: `https://<aether-host>/api/v1/hooks/identity-providers/<slug>`.
2. Subscribe it to `user.suspend`, `user.restore`, `scim.user.suspend`, `scim.user.deprovision`, `scim.user.restore`, and `session.revoke`.
3. Paste the signing secret Halo shows into **Webhook signing secret** on the provider in Aether.

Aether checks each event's `Halo-Signature` header (HMAC-SHA256 with a timestamp, which must be within 5 minutes) and ignores repeated event IDs:

- A suspend or deprovision event suspends the linked Aether account.
- A restore event restores it, but only if Halo was what suspended it. A suspension made by an Aether administrator stays in place.
- `session.revoke` signs the account out everywhere.
- Events for an account whose mailbox was converted to a shared mailbox are accepted and change nothing.

Halo must be able to reach Aether over the network for this to work. A local development stack on `localhost` cannot receive these events.

### SCIM provisioning

Halo, or any provider that speaks SCIM 2.0, can create, update and deactivate Aether accounts instead of waiting for people to sign in:

1. In Aether, open **Administration → Identity providers**, choose the people icon on the provider, then **Turn on SCIM**. Copy the base URL, `https://<aether-host>/api/v1/scim/<slug>`, and the bearer token. Aether shows the token once and stores only its hash.
2. In Halo, open the Aether application's provisioning settings, paste the base URL and the token, and choose **Test**. Halo then provisions the people assigned to the application every 2 minutes.

What each request does:

| Request | Effect in Aether |
| --- | --- |
| `POST /Users` | Creates the account with the email address in `userName` (or the primary email). If the address is on a verified domain and no mailbox or alias uses it, creates the mailbox and adds the person to that domain's organization. `externalId` is required and is linked as the person's subject at this provider, so they can sign in at once. An existing account with the same address, or an `externalId` already linked to another account, returns `409`. An existing account that is not yet linked to this provider is linked when the person first signs in through it. A `userName` that is not an email address or contains `*` or `?` returns `400 invalidValue`. The mailbox gets the default quota. If the organization is at its mailbox or storage limit, nothing is created and the request returns `400 invalidValue` with the reason in `detail`. |
| `GET /Users?filter=userName eq "…"` | Finds an account by address. Only `userName eq` filters are supported. |
| `PUT /Users/{id}` | Updates the display name, replaces the linked `externalId`, and applies `active`. An `externalId` linked to another account returns `409`. The email address cannot change over SCIM, and a `userName` containing `*` or `?` returns `400 invalidValue` here too. |
| `PATCH /Users/{id}` | Applies `replace` operations on `active` and `displayName`. |
| `DELETE /Users/{id}` | Marks the account deprovisioned and ends its IMAP sessions, including sessions opened for delegated mailboxes with an app password or an access token. Its mail is kept. |
| `POST /Groups` | Creates a group with `displayName`, an optional `externalId`, and `members` given as Aether user ids from `/Users`. A `displayName` already used by this provider returns `409 uniqueness`, and a member that is not an account linked to this provider, or is a platform administrator, returns `400 invalidValue`. |
| `GET /Groups?filter=displayName eq "…"` | Finds a group by name. Only `displayName eq` filters are supported, and `excludedAttributes=members` leaves the members out. |
| `PUT /Groups/{id}` | Replaces the name, `externalId` and the whole member list. |
| `PATCH /Groups/{id}` | Applies `add`, `remove` and `replace` on `members`, `displayName` and `externalId`, including `remove` with a `members[value eq "…"]` path. `remove` on `members` without a value empties the group. |
| `DELETE /Groups/{id}` | Deletes the group and takes it off every distribution list that used it. |

Every `/Users` request reaches only accounts linked to this provider, and never a platform administrator; any other account id returns `404` and filters leave it out. `active: false` suspends the account like a Halo suspension webhook: web sessions end and app passwords stop working, while mail keeps arriving. `active: true` restores the account only if this provider suspended it, so a suspension made by an Aether administrator stays in place. Neither value changes an account whose mailbox was converted to a shared mailbox. Groups belong to the provider that pushed them, and deleting the provider deletes them. Administrators add them to distribution lists, whose members then follow the group (see [Distribution lists](architecture.md#members-from-groups-and-domains)). Organization roles still come from group mappings when people sign in, not from SCIM groups.

## Generic OIDC providers

Any provider works if it meets all of these:

- It supports discovery.
- It supports the authorization code flow with S256 PKCE.
- It signs ID tokens with an algorithm that go-oidc accepts.

Use **Add generic provider** in the same screen. Providers without a `groups` claim can still sign users in, but group mappings have no effect.

## Local accounts

People without an identity provider sign in with their email address and an Aether password. Everything below applies only to those password sign-ins; for accounts that sign in through Halo or another provider, the password, two-factor authentication and sign-out stay the provider's job.

### Password

Under **Settings → Security**, people change their own password by entering the current one and a new one of at least 12 characters. Changing it signs out every other browser; app passwords keep working.

A platform administrator can set a new password for someone under **Administration → Members → More actions → Reset password**, or with `PUT /api/v1/admin/users/{id}/password`. That signs the person out everywhere. Creating a mailbox for an existing account never changes its password.

### Two-factor authentication

Under **Settings → Security**, people can add:

- **An authenticator app.** Aether shows a QR code and a setup key for apps such as 1Password, Google Authenticator or Microsoft Authenticator (TOTP, 6 digits, 30 seconds). Each code is accepted once.
- **Security keys and passkeys.** Hardware keys, or passkeys on a computer or phone, through WebAuthn. Aether uses them as the second step after the password, not instead of it. They are bound to the host name in `AETHER_PUBLIC_URL`, so open Aether at that address to use them.
- **Recovery codes.** Adding the first method creates 10 single-use codes, shown once. **Generate new codes** replaces them.

With any method set up, signing in asks for the password, then for a code, a security key or a recovery code. The second step must follow within 5 minutes and allows 5 wrong answers before the password has to be entered again. Turning off a method, removing a key or generating new recovery codes needs the current password.

App passwords for mail, calendar and contacts apps are separate credentials and are not affected by two-factor authentication. Revoke an app password if a device is lost.

### Requiring two-factor authentication

Under **Administration → Security**, an organization's owners and administrators choose who must use two-factor authentication: nobody, owners and administrators, or everyone. Someone who must use it and has not set it up can still sign in with their password, but sees only the setup screen until they finish. The members list shows who has it turned on.

When someone has lost every method and their recovery codes, a platform administrator can remove their two-factor methods under **Administration → Members → More actions → Turn off two-factor**. If an organization requires it, the person is asked to set it up again at once.

### Sessions

**Settings → Where you are signed in** lists the browsers signed in to the account with their IP address and last activity, for every account, including those that sign in through a provider. People can sign out any of them, or every other one at once. Owners and administrators can do the same for a member under **Administration → Members → More actions → Sessions**; only owners can do it for an owner.

## Mail clients

Create an app password in **Settings → App passwords**. Each one is bound to a single mailbox, is shown only once, and can be revoked at any time. App passwords exist only for user mailboxes: shared mailboxes you belong to appear in the mail app under `Shared/<address>/` when you sign in as yourself (see [Shared mailboxes](shared-mailboxes.md#in-mail-apps)).

| Setting | Value |
| --- | --- |
| IMAP | `<mail hostname>`, port 993 (SSL/TLS) or 143 (STARTTLS) |
| SMTP | `<mail hostname>`, port 465 (SSL/TLS) or 587 (STARTTLS) |
| Username | the full mailbox address |
| Password | the app password |
| Authentication | normal password (PLAIN/LOGIN over TLS) |

The app passwords and the mail authentication path are covered by automated tests. iOS Mail has been tested against a production installation: add it under **Other → Add Mail Account** with the mail host name for both servers, and it picks the TLS ports itself. These clients use the same standard IMAP and SMTP AUTH PLAIN/LOGIN over TLS, so they are expected to work, but they have not been tested yet:

- Thunderbird
- Apple Mail on macOS
- Outlook for Windows and Mac (IMAP accounts)
- Android Gmail (IMAP account)
- K-9 Mail / Thunderbird for Android

Outlook "Exchange" or "Microsoft 365" account types are not supported.

### Access tokens instead of app passwords

IMAP and SMTP submission also accept an OpenID Connect access token through the `OAUTHBEARER` and `XOAUTH2` mechanisms, with the mailbox address as the username. Aether accepts a token when all of these hold:

- An enabled identity provider in Aether publishes an `introspection_endpoint` (RFC 7662) in its discovery document, and reports the token as active when Aether asks with its client ID and secret. Halo does.
- The token was issued to Aether's client ID, or its `aud` contains Aether's client ID or `AETHER_PUBLIC_URL`. A token issued to another application at the same provider is refused.
- The token's subject is linked to an Aether account, which means the person has signed in to Aether through that provider at least once.
- The mailbox has the protocol turned on: IMAP for IMAP logins, SMTP submission for SMTP logins (see [Protocol access](#protocol-access)).
- That account is active and owns the mailbox, or has full access and Send as on it as a delegate. A shared mailbox is never accepted as the username; its folders are reached through a member's own login, as [Shared mailboxes](shared-mailboxes.md#in-mail-apps) describes.

Dovecot hands these logins to the API, which caches a positive introspection result for up to 5 minutes, or until the token expires if that is sooner. Suspending an account or removing access takes effect at once, because the mailbox check is not cached.

Most desktop and phone mail apps only offer OAuth for a fixed list of large providers, so this is mainly for scripts and tools that already hold a token, such as one obtained from Halo with the device flow. App passwords remain the way to connect ordinary mail apps.

## JMAP

JMAP clients use the same app password, or the same OpenID Connect access token, against `/.well-known/jmap`. The username for Basic authentication is the mailbox address. A web password, a browser session and an API key are not accepted. Details, including what the server does not implement, are in [JMAP](jmap.md).

## Calendar and contacts apps

Calendar and contacts apps connect over CalDAV and CardDAV, using an app password created for **Mail, calendar and contacts** or for **Calendar and contacts**.

| Setting | Value |
| --- | --- |
| Server | `https://<aether host>/dav/` (apps that support discovery can use just the host name) |
| Username | the full mailbox address |
| Password | an app password for that mailbox |

Discovery works through `/.well-known/caldav` and `/.well-known/carddav`. The server, its discovery, and calendar, task (`VTODO`), and contact create, read, query, and delete are covered by automated tests that use the `go-webdav` client, and have also been checked with the Python `caldav` client. Apps can create calendars with `MKCALENDAR`, sync tasks (`VTODO`) on any calendar collection, and view subscribed external calendars as read-only collections. Apple Calendar and Contacts on iOS have been tested against a production installation: add Calendar under **Settings → Apps → Calendar → Calendar Accounts → Add Account → Other → Add CalDAV Account** (and Contacts under **Settings → Apps → Contacts → Contacts Accounts → Add Account → Other → Add CardDAV Account**) with the mail host name and an app password. Thunderbird and DAVx5 use the same standard, so they are expected to work, but they have not yet been tested against Aether.

## Protocol access

Each mailbox has five switches, under **Administration → Mailboxes → More actions → Protocol access**: Aether Mail, IMAP, SMTP submission, ManageSieve, and CalDAV and CardDAV. New mailboxes, including those created at single sign-on, start with the organization's defaults under **Protocols for new mailboxes**; changing the defaults leaves existing mailboxes alone. The API is `PATCH /api/v1/mailboxes/{mailboxId}` with `protocols`, and `PATCH /api/v1/organizations/{orgId}` with `mailboxProtocols`.

- Dovecot checks IMAP, SMTP submission (through Postfix's SASL) and ManageSieve for app passwords in the `mail_credentials` view, and the API checks them for access tokens. The API checks CalDAV and CardDAV.
- Turning IMAP or ManageSieve off ends the open sessions signed in as the address. Turning IMAP off for a shared mailbox also removes its members' rights from its folders and ends their sessions, and turning SMTP off stops members sending as it from their mail apps.
- With Aether Mail off, the mailbox disappears from Aether Mail for everyone, and scheduled messages from it fail instead of sending. Administrators still manage its rules, automatic replies and signatures in the console, and server-side filters and automatic replies keep running.
- App passwords are not revoked. They stop working for a protocol while it is off and work again when it is turned back on.

An authenticated SMTP connection that is already open when SMTP is turned off can keep sending until the client disconnects, because Postfix only checks the credentials at login.

Each app password also has a scope, chosen when it is created: **Mail** (IMAP and SMTP submission), **Calendar and contacts** (CalDAV and CardDAV), or **Mail, calendar and contacts**, which also allows ManageSieve. App passwords created before scopes existed, and those in Apple device profiles, have the last one.

## Blocking repeated failed sign-ins

Aether blocks an address that keeps failing to sign in, on IMAP, SMTP submission, ManageSieve, CalDAV and CardDAV, and the web sign-in. Dovecot asks the API before each IMAP, ManageSieve and SMTP login (Postfix authenticates through Dovecot) and reports the result afterwards, using Dovecot's authentication policy on `POST /internal/auth-policy`, authenticated with `AETHER_DOVECOT_API_KEY`.

- An address is blocked after failed sign-ins with `AETHER_AUTH_BLOCK_THRESHOLD` (default 10) different passwords or accounts within 10 minutes. A mail app retrying the same wrong saved password counts once, so one person with an outdated password does not block the rest of their office.
- The first block lasts 15 minutes. Each further block within a day of the previous one doubles, up to 24 hours.
- IPv4 addresses are blocked one by one; IPv6 addresses by their `/64` network.
- A blocked address cannot sign in at all, even with the right password. Mail apps see `Too many failed sign-ins from this address. Try again later.`; SMTP answers `535`, the web sign-in `403 address_blocked`.
- Addresses in `AETHER_AUTH_BLOCK_ALLOW_NETS` are never blocked. The default is loopback and the private ranges (`127.0.0.0/8, ::1/128, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, fc00::/7`), which covers Aether's own containers and clients on the local network. Add your offices' public addresses there, as a comma-separated list of addresses and networks. Adding an address does not lift a block already in place. `AETHER_AUTH_BLOCK_THRESHOLD=0` turns blocking off.
- When the API cannot be reached, Dovecot lets logins through rather than locking everyone out.

Dovecot's own per-address delay after failed logins is turned off, because it grew to 15 seconds for every login from the same address, correct ones included, and made Postfix answer `454` to everyone behind a shared address. Each failed attempt is still delayed by 2 seconds.

Platform administrators see current blocks under **Administration → Blocked addresses** and can unblock an address there, or with `GET /api/v1/admin/blocked-addresses` and `DELETE /api/v1/admin/blocked-addresses/{id}`. Unblocking also resets the address's count of failures and repeat blocks. Blocks and unblocks are recorded as `auth.address.blocked` and `auth.address.unblocked` audit events, and `aether_auth_address_blocks_total` counts blocks by protocol. `aether_auth_attempts_total` now also counts mail logins by protocol, with the result `success`, `failure` or `blocked`.

On Kubernetes, set the two variables through `api.extraEnv`.

Contact groups sync inside the address book, as group cards. An address book someone shared with you appears next to your own. A view-only share can be read from the phone, and the server refuses changes to it.

## Bootstrap administrator

On a standalone installation, create the first platform administrator from the CLI:

```sh
docker compose exec api aether bootstrap-admin --email you@example.com
```

The command prints a generated password once, to your terminal only; it is never written to logs. To supply your own password, use `--password-stdin`. Running the command again for the same email resets that password, signs that account out everywhere and restores platform administrator rights. Add `--reset-two-factor` to also remove its authenticator app, security keys and recovery codes, for an administrator who lost them.

To disable password sign-in entirely after Halo is configured, set `AETHER_LOCAL_LOGIN=false`.

## Account security notifications

Aether notifies people by email (delivered straight to their Inbox so mail rules cannot hide or forward the alert) and in **Settings → Security** when security-sensitive changes happen to their account or mailbox:

- **App password created:** an app password or Apple device profile is created for their mailbox.
- **External forwarding rule added:** an enabled mail rule begins forwarding messages to an address outside the organization's verified domains.
- **Delegate access granted:** someone is granted delegate access to their mailbox, or an existing delegate's permissions are increased.
- **New device or country sign-in:** their account signs in from a previously unseen browser/OS combination or a new country (country detection reads standard reverse-proxy GeoIP headers such as `CF-IPCountry` or `X-Country-Code` when `AETHER_TRUST_PROXY=true`).
- **Repeated failed sign-ins:** password sign-in fails at least the configured number of times within 15 minutes (default 5).

Each alert includes links and in-app actions to review or undo the change (revoking the app password, removing the external forwarding address, removing the delegate, or signing out the other session) and to report suspicious activity. Reporting an alert records `security.notification.report` in the organization audit log (which also delivers to webhooks) and emails the organization's owners and administrators.

Organization owners and administrators can choose which events send notifications, adjust the failed sign-in threshold (3 to 50), and opt in to receiving administrator email copies under **Administration → Security** (`GET` and `PUT /api/v1/organizations/{orgId}/security-notifications`).

