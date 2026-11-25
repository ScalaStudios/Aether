# Organization features

Every feature is on for a new organization. Owners can turn off the ones the organization does not use, and platform administrators can also turn off the features that reach outside the server. Turning a feature off keeps its settings, so turning it back on restores them as they were.

| Feature | Key | Who can change it | While it is off |
| --- | --- | --- | --- |
| Calendar and contacts | `calendar` | Owners | `/api/v1/calendar/*`, `/api/v1/contacts/*`, invitation replies and the mailbox calendar and contacts imports answer `403`; messages no longer show invitation details |
| CalDAV and CardDAV | `dav` | Owners | `/dav/` answers `404` after sign-in |
| Mail import | `mailImport` | Platform administrators | `/api/v1/mailboxes/{mailboxId}/imports` and starting import schedules answer `403`, running imports for the organization's mailboxes are cancelled, and scheduled imports wait until the feature is on again |
| App passwords | `appPasswords` | Owners | No new app passwords; existing ones stop working for IMAP, SMTP and DAV, and open mail sessions of mailboxes with app passwords are ended. Listing and revoking them still works |
| Forwarding outside the organization | `externalForwarding` | Platform administrators | Mail rules no longer forward to addresses outside the organization's domains; the rules are kept and forward again when the feature is turned back on. Saving rules with an outside address answers `403`. The organization's [forwarding policy](architecture.md#rules-forwarding-and-automatic-replies) shows `blocked` and cannot be changed until the feature is on again |
| Personal aliases | `personalAliases` | Owners | Creating personal aliases and setting the personal alias key answer `403`, and Settings hides personal aliases; existing personal aliases keep receiving mail |
| Webhooks | `webhooks` | Platform administrators | Webhook endpoints answer `403`, no new deliveries are queued, and pending deliveries wait until the feature is on again |
| API keys | `apiKeys` | Owners | API key endpoints answer `403`, and requests made with the organization's API keys answer `403` |

A request refused because a feature is off answers with the code `feature_disabled`.

Calendars, contacts and DAV belong to a person rather than to one organization. They are off for a person when any organization the person belongs to, or owns a mailbox in, has turned them off. DAV also needs `calendar` on, and app passwords on in the organization of the mailbox used to sign in.

## In the web app

Owners and platform administrators change the switches under **Administration → Features**. Each switch says what turning it off does. An owner sees the three platform administrator switches locked, and an organization administrator sees every switch but cannot change any. A switch changes straight away and goes back if the server refuses the change.

If the mail server cannot be reached while **Forwarding outside the organization** changes, the switch stays as set and a notice under it offers **Try again**, which installs the mail rules again.

A feature that is off is also hidden:

| Feature | Hidden while it is off |
| --- | --- |
| Calendar and contacts | The Calendar and Contacts pages and their navigation entries, the calendar and contacts apps instructions in **Settings**, and the calendar and contacts imports under **Administration → Mailboxes**. Opening `/calendar` or `/contacts` shows that the feature is turned off |
| CalDAV and CardDAV | The calendar and contacts apps instructions in **Settings** |
| Mail import | **Import mail** in **Settings** |
| App passwords | Creating app passwords and Apple device profiles, and the calendar and contacts apps instructions in **Settings**. Existing app passwords stay listed so that they can be revoked |
| Forwarding outside the organization | Nothing. Mail rules say that they only forward to addresses on the organization's domains |
| Webhooks | The Webhooks section under **Administration → Integrations** |
| API keys | The API keys section under **Administration → Integrations** |

The navigation, the Calendar and Contacts pages and **Settings** follow the features that are off in any of the person's organizations, so someone who belongs to several organizations loses a page as soon as one of them turns its feature off. **Administration** follows the organization selected there.

## API

`GET /api/v1/organizations/{orgId}/features` returns the state of every feature to any member:

```json
{
  "features": { "calendar": true, "dav": false, "mailImport": true, "appPasswords": true, "externalForwarding": true, "personalAliases": true, "webhooks": true, "apiKeys": true },
  "platformAdminOnly": ["externalForwarding", "mailImport", "webhooks"]
}
```

`PUT` on the same path takes the features to change, such as `{"dav": false}`, and returns the new state. Features left out keep their state, and sending a feature with its current value is allowed, so the whole `features` object can be sent back. Each change is one audit event, `feature.enable` or `feature.disable`, with target type `feature` and the feature as target id.

When the body contains `externalForwarding`, Aether installs the mail rules of the organization's forwarding mailboxes again. A `502` means the switch was saved but some mailboxes could not be updated on the mail server; sending the same request again retries them.

`GET /api/v1/session` lists `disabledFeatures` for each membership and, at the top level, every feature that is off in any of the person's organizations.
