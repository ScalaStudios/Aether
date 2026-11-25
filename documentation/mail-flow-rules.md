# Mail flow rules

Mail flow rules apply to every message an organization sends or receives, whatever app sent it. Owners and administrators manage them under **Administration → Mail flow**, or through `/api/v1/organizations/{orgId}/mail-flow/rules` with an API key that has the `mailflow` scope. They replace Exchange transport rules and Google Workspace content compliance rules.

Mail rules under **Settings → Mail rules** are different: they belong to one mailbox and only sort incoming mail.

## How a rule is built

A rule has conditions, exceptions, actions and a mode.

- **Conditions** must all match. A rule without conditions applies to every message.
- **Exceptions** skip the rule when any of them matches.
- **Actions** run on the messages the rule matches.
- **Mode** is `enforce`, `test` or `off`. A rule in test mode changes nothing: it only records the messages it would have handled under **Recent matches**, so a rule can be checked against real mail before it is turned on.

Rules run in the order of the list. **Stop processing more rules** skips the organization's later rules for a matching message. Text is matched without regard to letter case, and a text condition matches when the field contains any of its values.

### Conditions

| Condition | Matches when |
| --- | --- |
| Sender | The envelope sender is one of the addresses, is at one of the domains, is inside or outside the organization, or is a member of a distribution list |
| A recipient | Any envelope recipient matches, in the same ways |
| Subject, body | The subject or the text of the message contains a value. Attachments are not searched |
| Header | A named header contains a value |
| Attachment name, attachment type | An attachment's file name or content type contains a value, such as `.zip` or `application/x-msdownload` |
| Size | The message is larger than a number of kilobytes |
| Spam score | Rspamd's score is above a number |
| Direction | The message is incoming, outgoing or internal |

A sender counts as inside the organization only when it signed in to send. Mail that arrives on port 25 claiming an address on one of the organization's domains is incoming mail from outside. For each organization, a message is:

| Direction | When |
| --- | --- |
| Outgoing | Sent by a signed-in member, to at least one address outside the organization |
| Internal | Sent by a signed-in member, only to addresses inside the organization |
| Incoming | Anything else that reaches one of the organization's addresses |

Membership of a list means the list address itself or one of its direct destinations. Lists inside lists are not expanded.

### Actions

| Action | What it does |
| --- | --- |
| Append a disclaimer | Adds text below the message, to the plain text and the HTML versions. HTML is optional and sanitized like mail; without it the text is used in HTML too |
| Send a copy to | Adds hidden recipients, such as a compliance mailbox |
| Redirect to | Replaces the recipients. For incoming mail, only the organization's own recipients are replaced |
| Reject | Refuses the message during the SMTP session with `550 5.7.1` and your text, so the sender's server returns it. It cannot be combined with other actions |
| Add or remove headers | Sets custom headers, such as `X-Department`, or removes them. Headers that carry the sender, recipients, date, MIME structure, signatures or authentication results cannot be added or removed |
| Change the spam score | Adds a number between -50 and 50 to Rspamd's score, which can move a message to Junk, greylist it or reject it |
| Require TLS | Delivers over mandatory TLS. If the receiving server does not offer TLS, the message stays queued and is eventually returned |
| Hold for approval | Keeps the message until one of the approvers approves it |

When several rules match, their actions add up: every disclaimer is added in order, copies and headers accumulate, and the last redirect wins. A rejection stops everything.

### Disclaimers on signed and encrypted mail

Changing the text of an S/MIME or OpenPGP signed message breaks the signature, and an encrypted message cannot be changed at all. Each disclaimer chooses what happens to such messages:

- **Attach the disclaimer as a separate part** wraps the message in a new `multipart/mixed` message with the disclaimer as a second, plain text part. The signature stays valid.
- **Send without the disclaimer** leaves the message alone.
- **Reject the message** refuses it during the SMTP session.

Messages whose body is not text, such as a message that is only a PDF, also get the disclaimer as a separate part.

### Holding messages for approval

Approvers must be active members of the organization. Each gets an email with the sender, recipients, subject and the start of the text, and approves or rejects the message under **Settings → Messages waiting for approval**. Owners and administrators can also decide under **Administration → Mail flow**. Any one approval sends the message on to its recipients as it was held, with the other actions of the matching rules already applied. A rejected message, or one nobody approves within 7 days, is discarded without telling the sender.

The sender's server is told the message was accepted. Approved messages are passed back to Postfix through the Sieve relay port, so the API needs `AETHER_SIEVE_RELAY_PASSWORD`; without it, rules that hold messages cannot be saved.

## Auditing

Each change to a rule is one audit event, `mail_flow_rule.create`, `mail_flow_rule.update` or `mail_flow_rule.delete`, with the old and new rule in `before` and `after`. Reordering is `mail_flow_rule.reorder`, and decisions on held messages are `mail_flow.held.approve` and `mail_flow.held.reject`. Like every audit event, they are sent to the organization's webhooks.

**Recent matches** lists the last 200 messages a rule matched, kept for 30 days, with the rule, whether it ran in test mode, the direction, the envelope and the subject. The API also logs every match.

## How they run

The API serves two milters, on `AETHER_MILTER_LISTEN` (default `:10045`) and `AETHER_MILTER_AFTER_LISTEN` (default `:10046`). Postfix calls them around Rspamd for mail on ports 25, 587 and 465:

1. The first milter removes `X-Aether-*` and `X-Spam-Status` headers a sender may have added, then runs every rule that does not test the spam score. Postfix shows each milter the message as changed by the ones before it, so Rspamd scores the final message and signs it with DKIM after the disclaimer is added.
2. Rspamd scores the message and signs it. A spam score change reaches Rspamd as an `X-Aether-Flow-Score` header, which the `AETHER_FLOW_SCORE` symbol adds to the score.
3. The second milter reads the score from `X-Spam-Status`, removes the internal headers and runs the rules that test the spam score. These rules run after all others and cannot add a disclaimer or change the score.

Require TLS adds a header that Postfix's `milter_header_checks` turns into a `FILTER` to the `smtp-tls` transport, which uses `smtp_tls_security_level = encrypt`, through the relay host when one is set. A message with recipients on this server is rejected instead, because the filter would send their copies out to the internet too.

Mail passed on through the Sieve relay port, which carries forwards from mail rules and approved messages, does not run through the rules again.

Both milters use `default_action = tempfail`: when the API cannot be reached, Postfix answers `451` and senders retry later, instead of letting mail through without its disclaimers, copies or rejections. When the API fails to load the rules, it answers `451 4.3.0` as well.

## Limits

- 200 rules per organization, 20 conditions and 20 exceptions per rule, 100 values per condition, 20 addresses per action and 10 headers to add or remove.
- A rejection or a hold applies to the whole message, including recipients in other organizations on the same installation.
- Body conditions read the first 1 MB of each text part.
- Disclaimers are not added to calendar invitations that are sent as a separate `text/calendar` part.
