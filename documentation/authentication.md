# Authentication

Aether has two kinds of credentials, and they are deliberately kept separate:

1. **Web sign-in** for Aether Mail and the administration console. This is a local password or an OpenID Connect provider.
2. **Protocol credentials** for IMAP and SMTP clients. These are mailbox-scoped app passwords.

Signing in to the browser through OIDC does not let a desktop mail client in. Aether never accepts an ID token, or a user's web password, as an IMAP or SMTP credential.

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

The app passwords and the mail authentication path are covered by automated tests. Interoperability with these clients is expected, because they use standard IMAP and SMTP AUTH PLAIN/LOGIN over TLS, but it has not been tested against real installations of them:

- Thunderbird
- Apple Mail (macOS and iOS)
- Outlook for Windows and Mac (IMAP accounts)
- Android Gmail (IMAP account)
- K-9 Mail / Thunderbird for Android

Outlook "Exchange" or "Microsoft 365" account types are not supported.

Login through OAUTHBEARER or XOAUTH2 with Halo access tokens is not implemented. Halo issues JWT access tokens with an `aud` for registered API resources and offers token introspection, so a Dovecot `oauth2` passdb can be added later.

## Calendar and contacts apps

Calendar and contacts apps connect over CalDAV and CardDAV, using the same app passwords as mail apps.

| Setting | Value |
| --- | --- |
| Server | `https://<aether host>/dav/` (apps that support discovery can use just the host name) |
| Username | the full mailbox address |
| Password | an app password for that mailbox |

Discovery works through `/.well-known/caldav` and `/.well-known/carddav`. The server, its discovery, and calendar and contact create, read, query, and delete are covered by automated tests that use the `go-webdav` client. Apple Calendar and Contacts, Thunderbird, and DAVx5 use the same standard, so they are expected to work, but they have not yet been tested against Aether.

## Bootstrap administrator

On a standalone installation, create the first platform administrator from the CLI:

```sh
docker compose exec api aether bootstrap-admin --email you@example.com
```

The command prints a generated password once, to your terminal only; it is never written to logs. To supply your own password, use `--password-stdin`. Running the command again for the same email resets that password and restores platform administrator rights.

To disable password sign-in entirely after Halo is configured, set `AETHER_LOCAL_LOGIN=false`.
