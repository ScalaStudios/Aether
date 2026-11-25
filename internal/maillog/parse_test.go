package maillog

import (
	"slices"
	"testing"
)

func types(events []Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
}

func TestParseOutgoingDelivery(t *testing.T) {
	p := NewParser()
	lines := []string{
		"Nov 23 10:00:00 mail postfix/submission/smtpd[11]: 4XyZ1k2mQRz9bc: client=unknown[10.0.0.5], sasl_method=PLAIN, sasl_username=app@example.com",
		"Nov 23 10:00:00 mail postfix/cleanup[12]: 4XyZ1k2mQRz9bc: message-id=<abc@example.com>",
		"Nov 23 10:00:00 mail postfix/qmgr[13]: 4XyZ1k2mQRz9bc: from=<App@Example.com>, size=1200, nrcpt=3 (queue active)",
		"Nov 23 10:00:01 mail postfix/smtp[14]: 4XyZ1k2mQRz9bc: to=<ok@remote.test>, relay=mx.remote.test[1.2.3.4]:25, delay=1, delays=0/0/0.5/0.5, dsn=2.0.0, status=sent (250 2.0.0 OK id=1)",
		"Nov 23 10:00:01 mail postfix/smtp[14]: 4XyZ1k2mQRz9bc: to=<gone@remote.test>, relay=mx.remote.test[1.2.3.4]:25, delay=1, delays=0/0/0.5/0.5, dsn=5.1.1, status=bounced (host mx.remote.test[1.2.3.4] said: 550 5.1.1 <gone@remote.test>: user unknown (in reply to RCPT TO command))",
		"Nov 23 10:00:01 mail postfix/smtp[14]: 4XyZ1k2mQRz9bc: to=<slow@remote.test>, relay=none, delay=1, delays=0/0/1/0, dsn=4.4.1, status=deferred (connect to mx.remote.test[1.2.3.4]:25: Connection timed out)",
		"Nov 23 10:00:02 mail postfix/lmtp[15]: 4XyZ1k2mQRz9bc: to=<user@example.com>, orig_to=<team@example.com>, relay=dovecot[172.18.0.4]:24, delay=0.1, delays=0/0/0/0.1, dsn=2.0.0, status=sent (250 2.0.0 <user@example.com> abc Saved)",
		"Nov 28 10:00:02 mail postfix/qmgr[13]: 4XyZ1k2mQRz9bc: from=<app@example.com>, status=expired, returned to sender",
		"Nov 28 10:00:02 mail postfix/qmgr[13]: 4XyZ1k2mQRz9bc: removed",
	}
	var events []Event
	for _, l := range lines {
		events = append(events, p.Parse(l)...)
	}
	want := []string{Delivered, Bounced, Deferred, Delivered, Received, Bounced}
	if got := types(events); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	b := events[1]
	if b.Sender != "app@example.com" || b.Recipient != "gone@remote.test" || b.DSN != "5.1.1" || b.MessageID != "abc@example.com" || b.Relay != "mx.remote.test[1.2.3.4]:25" {
		t.Fatalf("bounce = %+v", b)
	}
	if b.Reply != "host mx.remote.test[1.2.3.4] said: 550 5.1.1 <gone@remote.test>: user unknown (in reply to RCPT TO command)" {
		t.Fatalf("reply = %q", b.Reply)
	}
	if r := events[4]; r.OriginalRecipient != "team@example.com" || r.Recipient != "user@example.com" {
		t.Fatalf("received = %+v", r)
	}
	if e := events[5]; e.Recipient != "slow@remote.test" || e.DSN != "4.4.7" {
		t.Fatalf("expired = %+v", e)
	}
	if len(p.Queue) != 0 {
		t.Fatalf("queue not cleaned: %v", p.Queue)
	}
}

func TestParseInbound(t *testing.T) {
	p := NewParser()
	lines := []string{
		"2026-11-23T10:00:00.123456+00:00 mail postfix/smtpd[21]: 5AbC12: client=mx.sender.test[5.6.7.8]",
		"2026-11-23T10:00:00.200000+00:00 mail postfix/qmgr[13]: 5AbC12: from=<someone@sender.test>, size=900, nrcpt=1 (queue active)",
		"2026-11-23T10:00:01.000000+00:00 mail postfix/smtp[14]: 5AbC12: to=<far@elsewhere.test>, relay=mx.elsewhere.test[9.9.9.9]:25, delay=1, delays=0/0/0.5/0.5, dsn=2.0.0, status=sent (250 OK)",
		"2026-11-23T10:00:01.000000+00:00 mail postfix/lmtp[15]: 5AbC12: to=<user@example.com>, relay=dovecot[172.18.0.4]:24, delay=0.1, delays=0/0/0/0.1, dsn=2.0.0, status=sent (250 2.0.0 Saved)",
		"2026-11-23T10:00:02.000000+00:00 mail postfix/smtpd[21]: NOQUEUE: reject: RCPT from mx.sender.test[5.6.7.8]: 550 5.1.1 <nobody@example.com>: Recipient address rejected: User unknown in virtual mailbox table; from=<someone@sender.test> to=<nobody@example.com> proto=ESMTP helo=<mx.sender.test>",
		"2026-11-23T10:00:03.000000+00:00 mail postfix/cleanup[22]: 6DeF34: milter-reject: END-OF-MESSAGE from mx.sender.test[5.6.7.8]: 5.7.1 Spam message rejected; from=<spam@sender.test> to=<user@example.com> proto=ESMTP helo=<mx.sender.test>",
		"2026-11-23T10:00:04.000000+00:00 mail postfix/submission/smtpd[23]: NOQUEUE: reject: RCPT from unknown[10.0.0.5]: 550 5.7.1 <x@example.com>: Recipient address rejected: denied; from=<a@example.com> to=<x@example.com> proto=ESMTP helo=<h>",
		"2026-11-23T10:00:05.000000+00:00 mail postfix/smtpd[21]: warning: hostname mx.sender.test does not resolve to address 5.6.7.8",
	}
	var events []Event
	for _, l := range lines {
		events = append(events, p.Parse(l)...)
	}
	want := []string{Received, Rejected, Rejected}
	if got := types(events); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if r := events[1]; r.Recipient != "nobody@example.com" || r.Sender != "someone@sender.test" || r.Reply != "550 5.1.1 <nobody@example.com>: Recipient address rejected: User unknown in virtual mailbox table" {
		t.Fatalf("reject = %+v", r)
	}
	if r := events[2]; r.QueueID != "6DeF34" || r.Reply != "5.7.1 Spam message rejected" {
		t.Fatalf("milter reject = %+v", r)
	}
	if events[0].Time.Year() != 2026 || events[0].Time.Nanosecond() != 0 {
		t.Fatalf("time = %v", events[0].Time)
	}
}
