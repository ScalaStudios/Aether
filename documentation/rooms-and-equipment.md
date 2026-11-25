# Rooms and equipment

A room or a piece of equipment, such as a meeting room, a car or a projector, has its own address, like `board-room@example.com`. People invite it to an event the way they invite a person. Aether answers each invitation by the room's booking rules, so it is never booked twice.

| | Shared mailbox | Room or equipment |
| --- | --- | --- |
| Who signs in with the address | Nobody | Nobody |
| Who reads its mail | Its members | Nobody: Aether answers invitations itself |
| Who it answers | | Every invitation, by its booking rules |
| Approvals | | Optional approvers accept or decline requests |

## Booking a room

In Aether Calendar, open an event and choose **Add room or equipment**. The finder lists the rooms and equipment of your organizations with their location, seats and rules, and shows for the event's time whether each one is available, busy or outside its rules, with the reason. Search by name or location, and filter rooms from equipment.

When you save the event, every room on it answers at once:

| Answer | Shown as | Meaning |
| --- | --- | --- |
| Accepted | Booked | The room is yours for the event. |
| Tentative | Awaiting approval | The room needs an approver. You get another answer once they decide. |
| Declined | Declined | The room is busy, or the booking breaks one of its rules. The reason is shown after saving and sent by email. |

Moving the event asks the room again. If the new time is busy, the room declines and its old booking is released. Removing the room from the event, or deleting the event, releases the booking.

Rooms answer invitations from any calendar app too. Invite the room's address from Thunderbird, Apple Calendar or Outlook, signed in to Aether with an app password: the app sends the invitation by email, Aether reads it within a minute, books the room, and replies to the organizer by email. The reply also updates the event in the organizer's Aether calendar, so apps synced over CalDAV show the room as accepted.

Rooms appear in the directory with a **Room** or **Equipment** label, in address suggestions, and in the free and busy check of the event editor.

## Booking rules

Administrators manage rooms and equipment under **Administration → Rooms and equipment**.

| Rule | What it does |
| --- | --- |
| Requests | **Accept automatically**, or **Require approval** by one or more approvers |
| Who can book | Everyone in the organization, or only selected people. Approvers can always book |
| Book up to | How many days ahead a booking may start, 1 to 1,095. A repeating booking may continue past it |
| Longest booking | The longest a single booking or occurrence may last, from 30 minutes to 7 days, or no limit |
| Only during set hours | Bookings must start and end within the chosen hours, on the chosen days |
| Time zone | The zone for the set hours, for all-day bookings, and for times in the room's answers |

Conflicts are always declined, whichever rules are set. Each occurrence of a repeating booking is checked against existing bookings for the whole booking window, and at least one year ahead. A booking that is already in the past is declined.

People outside the organization are declined. An organizer counts as in the organization when their mailbox belongs to it or they are one of its members.

### Approvals

With **Require approval**, a booking that is free and within the rules waits for an approver, and the organizer gets a tentative answer. Each approver gets an email with a link to **Calendar → Requests**, which lists every waiting request for the rooms they approve, with a warning when another booking has since taken the time. **Approve** books the room and tells the organizer; **Decline** tells them, with an optional message. A request cannot be approved while it conflicts with a booking. Bookings made by an approver are accepted straight away. Changing only the title or description of an approved booking keeps it approved; moving it asks the approvers again.

Requests whose time has passed are removed. Turning a room off under **Bookable** stops new bookings and hides its requests until it is turned on again.

### Through the API

Rooms and equipment are mailboxes of the kind `resource`. An API key with `mailboxes:write` can create one with `POST /api/v1/organizations/{orgId}/mailboxes`, giving `kind: "resource"`, a `displayName` and a `resource` object with the rules, list them with `GET /api/v1/organizations/{orgId}/resources`, and change them with `PUT /api/v1/mailboxes/{mailboxId}/resource`. Approvers and the people who can book are given as user IDs of organization members. A resource cannot have delegates or app passwords, and cannot be converted to another kind.

For people, `GET /api/v1/calendar/resources?start=…&end=…` lists the bookable rooms and equipment with their availability, `GET /api/v1/calendar/booking-requests` lists the requests an approver can decide, and `POST /api/v1/calendar/booking-requests/{requestId}` takes `{"decision": "approve" | "decline", "comment": "…"}`. Decisions are recorded in the audit log as `resource.booking_approve` and `resource.booking_decline`, and rule changes as `resource.update`.

## Free and busy over CalDAV

Calendar apps can ask Aether when people and rooms are busy, through CalDAV scheduling ([RFC 6638](https://www.rfc-editor.org/rfc/rfc6638)). The principal of every account advertises a scheduling outbox, and a `POST` of a `VFREEBUSY` request to it answers with the busy times of each attendee in the same organizations: people, from all their calendars, and rooms and equipment, from their bookings. Busy times never include titles or other details. Thunderbird uses this for its scheduling view; addresses outside the organizations are answered as unknown.

Aether advertises the `calendar-schedule` capability for this, not `calendar-auto-schedule`, so apps still send invitations by email themselves. In Thunderbird, leave `calendar.caldav.sched.enabled` at its default of `false`: when it is `true`, Thunderbird expects the server to deliver invitations and stops sending them.

## How it works

Invitations from Aether Calendar to a room are answered while the event is saved, without email. Invitations from other apps arrive in the room's own inbox, which a background worker in every API process reads every 30 seconds. It trusts an invitation only when its organizer is the message's sender and the address the mail server received it from. Postfix asks the API about every message for a room during the SMTP session, on the same policy port as restricted distribution lists, and refuses mail that claims an Aether address without signing in, so nobody can book a room in someone else's name from outside.

A room's bookings live in its own calendar, which only Aether writes to. Deciding a booking locks the room, so two people asking for the same time at once cannot both get it. The room's answer is an iTIP `REPLY` with the reason in `COMMENT`, sent from the room's address.
