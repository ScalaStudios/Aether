# Sender warnings

Aether Mail marks mail from outside your organization and warns about senders who pretend to be someone in it. Each message also shows its SPF, DKIM and DMARC results in plain words.

## What people see

| Shown | When |
| --- | --- |
| **External**, next to the sender | The message did not come from your organization (see [Internal or external](#internal-or-external)) |
| **Check this sender before you reply** | One of the impersonation checks below matched |
| **You don't often get email from …** | The sender is outside the organization and the mailbox has no other mail from them, and has never written to them |
| **Sender verification** | Always, under the recipients. Opens the SPF, DKIM and DMARC results |

The warning lists every check that matched:

| Check | Example |
| --- | --- |
| The sender's name is the name of someone in the organization, but the address is not theirs | `Grace Hopper <grace.private@gmail.com>` while Grace Hopper has a mailbox in the organization |
| The sender's name contains an address on one of the organization's domains | `"ceo@example.com" <boss@attacker.net>` |
| The sender's domain looks like one of the organization's domains | `examp1e.com`, `exarnple.com`, `exmaple.com`, `example.co`, `example.com.attacker.net`, or a domain with Cyrillic or Greek letters, for `example.com` |
| The message uses one of the organization's domains, but it was not sent from Aether and did not pass DMARC | `ceo@example.com` sent from someone else's server |

The warning and the External tag are shown in Aether Mail only. Other mail apps don't show them.

### Internal or external

A message is internal when its From address is on one of the organization's verified domains and one of these is true:

- it was sent by a signed-in account through Aether (webmail or SMTP submission),
- it passed DMARC for that domain, for example from a service the organization has authorized to send as its domain,
- it was forwarded by a mail rule on this server, or
- it has no verification result, because it arrived before this feature, was imported, or was added by a mail app.

Everything else is external, including mail that uses one of the organization's domains without being sent through Aether or passing DMARC. That case also gets the impersonation warning. Mail from another organization on the same Aether server is external.

### Name matching

Names are compared after removing accents, ignoring case, punctuation and word order, and treating look-alike letters and digits as the same (`0` and `o`, `1`, `I` and `l`, `rn` and `m`, Cyrillic and Greek look-alikes). `Lovelace, Ada` and `Аda Lovelace` with a Cyrillic А both match `Ada Lovelace`. Names shorter than three letters are not compared. The directory is the display names of the organization's user mailboxes and of its members; shared mailboxes are not included, because names such as "Support" are common outside too.

### Look-alike domains

A domain looks like one of the organization's domains when it is the same after replacing look-alike characters, when it differs by one added, removed, changed or swapped character, or when the organization's domain is a part of it (`example.com.attacker.net`). Subdomains of the organization's domains do not count. Single-character differences are only checked when the name before the top-level domain has at least five characters, so `acme.io` is not reported for `acme.co`.

### First contact

Aether looks for other mail from the sender in the message's folder, Inbox and Archive, and for mail to the sender in Sent. Up to the 20 newest matches in each folder are compared with the exact address. Mail that has been deleted, or filed into other folders by a rule, does not count. The tip is not shown for messages that already have a spoofing warning.

## Sender verification

| Result | Meaning |
| --- | --- |
| Pass | The check succeeded |
| Fail | The check failed |
| Soft fail | SPF only: the domain says the server is probably not allowed to send for it |
| Neutral, None | The domain publishes nothing to check against, or says nothing either way |
| Not checked, Error | A DNS lookup failed, or the domain's record or the signature is broken |

Mail sent by a signed-in account on this server says so instead, because SPF, DKIM and DMARC only apply to mail from other servers. Mail forwarded by a mail rule on this server says it could not be checked again. Mail without a verification result, such as imported mail, says it was not checked.

## Settings

Organization owners and administrators turn the three parts on or off under **Administration → Sender warnings**. All three are on until changed. Sender verification results are always shown.

| Setting | Controls |
| --- | --- |
| Tag external senders | The External tag |
| Point out new senders | The first contact tip |
| Warn about impersonation | The warning, for all four checks |

Each change is recorded in the audit log as `sender_warnings.update` with the settings that changed.

## How it works

The verdict is never taken from headers the sender wrote.

1. Postfix removes every `X-Aether-Authentication` header from incoming mail on every port, so a forged one is gone even when Rspamd is unavailable.
2. Rspamd adds exactly one `X-Aether-Authentication` header to every message it scans. It records whether a signed-in account sent it (`auth=pass`), the Sieve relay forwarded it (`auth=relay`), or neither (`auth=none`), with the SPF, DKIM and DMARC results and the checked domains, for example `auth=none spf=pass spf.domain=bounce.example.com dmarc=pass dkim=pass dkim.domain=example.com`.
3. Imports from other providers remove the header before storing each message.
4. When a message is opened, the API trusts the header only if there is exactly one and every value is valid. It then compares the From address with the organization's verified domains and directory, and searches the mailbox for earlier mail from the sender.

Mail that arrived while Rspamd was down has no header and is treated like imported mail. Outgoing mail also carries the header; receiving servers that run Aether replace it.

## API

`GET /api/v1/mailboxes/{mailboxId}/messages/{uid}` returns a `sender` object, or `null` for a message without a From address:

```json
{
  "organization": "Northwind Studio",
  "external": true,
  "firstContact": true,
  "warnings": [{ "kind": "name", "name": "Grace Hopper", "address": "grace@northwind.example" }],
  "authentication": {
    "submission": "none",
    "spf": { "result": "pass", "domain": "gmail.com" },
    "dkim": { "result": "pass", "domain": "gmail.com" },
    "dmarc": { "result": "pass" }
  }
}
```

`external` and `firstContact` are `false`, and `warnings` is empty, when the matching setting is off. Warning kinds are `name` (with `name` and `address` of the colleague), `address-in-name` (with the `address` shown in the name), `lookalike` (with `domain` and the organization domain it `resembles`) and `spoofed` (with `domain`). `authentication` is `null` when the message has no trusted result.

| Endpoint | Purpose |
| --- | --- |
| `GET /api/v1/organizations/{orgId}/sender-warnings` | Returns `externalTag`, `firstContact` and `impersonation` |
| `PUT /api/v1/organizations/{orgId}/sender-warnings` | Replaces all three. Owners and administrators only |

## Limitations

- Mail apps other than Aether Mail show neither the tag nor the warnings. There is no subject prefix or banner added to the message itself. Mail apps that can filter on headers can use `X-Aether-Authentication`.
- The External tag is shown on the open message, not in the message list.
- Name matching needs the whole name to match. A sender called `Grace H.` is not reported.
- Look-alike domains are checked against the organization's own domains only, not against other well-known brands.
- Mail forwarded by a mail rule on this server keeps no verification result from its first delivery.
