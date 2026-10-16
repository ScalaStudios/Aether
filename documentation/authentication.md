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
   - **Auto-provision:** creates an Aether account on first sign-in. If the user's verified email belongs to a verified Aether domain, it also creates that mailbox and adds the user to the domain's organization.
   - **Administrator group:** members of this Halo group become platform administrators.
   - **Group mappings:** members of a Halo group get the mapped role (owner, admin, or member) in an organization.

Group mappings only grant or upgrade roles. They never remove a role. To revoke access, do it in Aether, or suspend the user in Halo.

### Account matching

On sign-in, Aether looks for the account in this order:

1. An existing link between this provider and the user's `sub`.
2. If the provider says the email is verified (`email_verified`), an existing account with the same email. The two are then linked.
3. If auto-provisioning is on, a new account.

Otherwise sign-in fails with `not_provisioned`. Aether never stores Halo passwords.

### Disabled and deprovisioned users

- Halo rejects suspended or deprovisioned users at its token and userinfo endpoints, so they cannot start a new Aether session.
- Aether sessions created through OIDC expire after 12 hours.
- A platform administrator can suspend or restore any account under **Administration → Members**. Suspension ends every web session at once, blocks sign-in, and makes Dovecot reject the account's app passwords, which stops IMAP and SMTP. Mail keeps arriving in the account's mailboxes.

To have Halo suspensions take effect in Aether immediately, connect Halo's webhooks:

1. In Halo, create a webhook that points at the **Webhook URL** shown in Aether's identity provider form: `https://<aether-host>/api/v1/hooks/identity-providers/<slug>`.
2. Subscribe it to `user.suspend`, `user.restore`, `scim.user.suspend`, `scim.user.deprovision`, `scim.user.restore`, and `session.revoke`.
3. Paste the signing secret Halo shows into **Webhook signing secret** on the provider in Aether.

Aether checks each event's `Halo-Signature` header (HMAC-SHA256 with a timestamp, which must be within 5 minutes) and ignores repeated event IDs:

- A suspend or deprovision event suspends the linked Aether account.
- A restore event restores it, but only if Halo was what suspended it. A suspension made by an Aether administrator stays in place.
- `session.revoke` signs the account out everywhere.

Halo must be able to reach Aether over the network for this to work. A local development stack on `localhost` cannot receive these events.

### SCIM provisioning

Halo, or any provider that speaks SCIM 2.0, can create, update and deactivate Aether accounts instead of waiting for people to sign in:

1. In Aether, open **Administration → Identity providers**, choose the people icon on the provider, then **Turn on SCIM**. Copy the base URL, `https://<aether-host>/api/v1/scim/<slug>`, and the bearer token. Aether shows the token once and stores only its hash.
2. In Halo, open the Aether application's provisioning settings, paste the base URL and the token, and choose **Test**. Halo then provisions the people assigned to the application every 2 minutes.

What each request does:

| Request | Effect in Aether |
| --- | --- |
| `POST /Users` | Creates the account with the email address in `userName` (or the primary email). If the address is on a verified domain and no mailbox exists, creates the mailbox and adds the person to that domain's organization. `externalId` is linked as the person's subject at this provider, so they can sign in at once. An existing account with the same address returns `409`, and Halo then finds it with a filter and updates it. |
| `GET /Users?filter=userName eq "…"` | Finds an account by address. Only `userName eq` filters are supported. |
| `PUT /Users/{id}` | Updates the display name, links `externalId`, and applies `active`. The email address cannot change over SCIM. |
| `PATCH /Users/{id}` | Applies `replace` operations on `active` and `displayName`. |
| `DELETE /Users/{id}` | Marks the account deprovisioned. Its mail is kept. |

`active: false` suspends the account like a Halo suspension webhook: web sessions end and app passwords stop working, while mail keeps arriving. `active: true` restores the account only if this provider suspended it, so a suspension made by an Aether administrator stays in place. Groups are not provisioned; group mappings still apply when people sign in.

## Generic OIDC providers

Any provider works if it meets all of these:

- It supports discovery.
- It supports the authorization code flow with S256 PKCE.
- It signs ID tokens with an algorithm that go-oidc accepts.

Use **Add generic provider** in the same screen. Providers without a `groups` claim can still sign users in, but group mappings have no effect.

## Mail clients

Create an app password in **Settings → App passwords**. Each one is bound to a single mailbox, is shown only once, and can be revoked at any time.

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
- That account is active and owns the mailbox, or has full access to it as a delegate.

Dovecot hands these logins to the API, which caches a positive introspection result for up to 5 minutes, or until the token expires if that is sooner. Suspending an account or removing access takes effect at once, because the mailbox check is not cached.

Most desktop and phone mail apps only offer OAuth for a fixed list of large providers, so this is mainly for scripts and tools that already hold a token, such as one obtained from Halo with the device flow. App passwords remain the way to connect ordinary mail apps.

## Calendar and contacts apps

Calendar and contacts apps connect over CalDAV and CardDAV, using the same app passwords as mail apps.

| Setting | Value |
| --- | --- |
| Server | `https://<aether host>/dav/` (apps that support discovery can use just the host name) |
| Username | the full mailbox address |
| Password | an app password for that mailbox |

Discovery works through `/.well-known/caldav` and `/.well-known/carddav`. The server, its discovery, and calendar and contact create, read, query, and delete are covered by automated tests that use the `go-webdav` client, and have also been checked with the Python `caldav` client. Apps can create calendars with `MKCALENDAR`. Apple Calendar and Contacts on iOS have been tested against a production installation: add Calendar under **Settings → Apps → Calendar → Calendar Accounts → Add Account → Other → Add CalDAV Account** (and Contacts under **Settings → Apps → Contacts → Contacts Accounts → Add Account → Other → Add CardDAV Account**) with the mail host name and an app password. Thunderbird and DAVx5 use the same standard, so they are expected to work, but they have not yet been tested against Aether.

## Bootstrap administrator

On a standalone installation, create the first platform administrator from the CLI:

```sh
docker compose exec api aether bootstrap-admin --email you@example.com
```

The command prints a generated password once, to your terminal only; it is never written to logs. To supply your own password, use `--password-stdin`. Running the command again for the same email resets that password and restores platform administrator rights.

To disable password sign-in entirely after Halo is configured, set `AETHER_LOCAL_LOGIN=false`.
