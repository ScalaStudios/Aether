# JMAP

Aether speaks [JMAP](https://jmap.io/) for mail, calendars and contacts on the API. Dovecot 2.3 has no JMAP server, so mail methods read and write the same IMAP mailboxes as Aether Mail, using the master login. Calendars and contacts use the same PostgreSQL rows as CalDAV and CardDAV. No extra module is added: the JSON protocol is implemented in the API, on top of the IMAP, iCalendar and vCard libraries already in the build.

## Connecting

| | |
| --- | --- |
| Session | `GET /.well-known/jmap` |
| API | `POST /jmap` |
| Upload | `POST /jmap/upload/{accountId}` |
| Download | `GET /jmap/download/{accountId}/{blobId}/{name}` |
| Push | `GET /jmap/eventsource` |

Put the public URL in front of these paths the same way as `/api` and `/dav`. The session document's `apiUrl`, `uploadUrl`, `downloadUrl` and `eventSourceUrl` are built from `AETHER_PUBLIC_URL`.

Authentication is one of:

- HTTP Basic with the mailbox address and an app password. The password is checked with the same rule as IMAP: the mailbox is active, the domain is verified, and the account owns it or has full access and Send as. A web password is refused.
- `Authorization: Bearer` with an OpenID Connect access token, checked the same way as IMAP `OAUTHBEARER`. The token must have been issued to Aether, and the subject must already be linked to an account.

A browser session cookie and an `aether_` API key are not accepted. That keeps a page on another site from calling JMAP with the cookie. Cross-origin requests are allowed from any origin, without credentials, and must send the `Authorization` header.

Requests are limited to 600 a minute per address before sign-in and 180 a minute per account after. Sending is limited to 30 messages a minute per mailbox. A request may contain 16 method calls and 128 objects, and a body or upload may be 10 MB or 25 MB.

## Accounts

Each mailbox the person can open is a JMAP account, with the mailbox's id. Mail is available on all of them. Calendars and contacts are on the user mailbox whose address matches the account, or the first user mailbox they own. A login that owns no mailbox gets one account, with the user id, for calendars and contacts only. A read-only delegate can read mail and cannot change messages, folders or the seen flag. Send as and Send on behalf follow the mailbox rights. The envelope sender and the From header of a submission must be that account's address. Send on behalf adds a Sender header with the person's own address.

Platform administrators do not gain mail, calendars or contacts through JMAP.

## Mail

The capabilities are `urn:ietf:params:jmap:core` and `urn:ietf:params:jmap:mail`. Mail covers `Mailbox`, `Email`, `Thread`, `Identity`, `EmailSubmission` and `VacationResponse`.

Folder and message ids are stored in PostgreSQL (`jmap_mailboxes` and `jmap_messages`, migration `0062_jmap_messages.sql`) so they stay the same when a message moves or a folder is renamed. An email lives in one folder. Moving it is an `Email/set` patch of `mailboxIds`; copying into a second folder is refused. Destroying an email expunges it.

Keywords are `$seen`, `$draft`, `$flagged`, `$answered`, `$forwarded` and other `$` keywords of letters, digits and underscores. `Email/set` create accepts a structured message (From, To, Cc, Bcc, subject, text and HTML) or the blob id of an uploaded RFC 5322 message. From, when set, must be the account address. `EmailSubmission/set` sends that message. It does not file a copy in Sent; the client moves or copies it. `onSuccessUpdateEmail` and `onSuccessDestroyEmail` run after a successful send.

`VacationResponse` id `singleton` is the mailbox's automatic reply. Changing it installs the same Sieve script as the settings page and records `mailbox.rules.update`. The HTML body is reduced to text, and dates are whole days.

`Identity/get` returns one identity, whose id is the account id, when the login can send. `Identity/set` is refused.

`GET /jmap/eventsource` polls mailbox state about every 15 seconds and stays open for at most 30 minutes. Four streams per account are allowed. `closeafter=state` returns one state event and closes. This is not IMAP IDLE and not Web Push.

## Calendars and contacts

`urn:ietf:params:jmap:calendars` covers `Calendar` and `CalendarEvent`. `urn:ietf:params:jmap:contacts` covers `AddressBook` and `ContactCard`. An event id is `ev.` plus the iCalendar UID, so it survives a move between calendars. A contact id is `cd.` plus the vCard UID. The default Calendar collection and the Contacts address book cannot be deleted. A calendar shared read-only cannot be changed. Sharing itself stays on CalDAV and the REST API: `mayShare` is false.

Events store one calendar, a title, start, duration, location, status and a single recurrence rule. Recurrence is stored on the master event and is not expanded into instances. Updating an event patches that master so exception instances already in the file are kept. Contacts store a name, email addresses, phone numbers, one organization and one note.

## What is not in this server

- `Email/changes`, `Mailbox/changes` and `queryChanges`. Queries set `canCalculateChanges` to false. Clients poll with `get` and `query`, or use the event source.
- An email in more than one folder.
- A persistent thread index. Thread ids are derived from the root Message-ID when the message is read. A message with no Message-ID is its own thread.
- Creating a message with an arbitrary MIME `bodyStructure`. Structured create is text and HTML. Attachments go in through an uploaded raw message.
- Signatures, quotas, MDN, Sieve scripts other than the automatic reply, and scheduled send.
- Web Push (RFC 8887).
- Full JSCalendar and JSContact. The fields above are the ones that round-trip to iCalendar and vCard.
- Email query `before` and `after` finer than a calendar day, and a query across several folders that is sorted as one list. A query with `inMailbox` is sorted by the IMAP server.
- `anchor` and `anchorOffset`. Page with `position`.
