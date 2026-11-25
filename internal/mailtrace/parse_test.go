package mailtrace

import (
	"testing"
	"time"
)

var now = time.Date(2026, 11, 22, 20, 0, 0, 0, time.UTC)

func mustParse(t *testing.T, line string) record {
	t.Helper()
	r, ok := parse(line, now)
	if !ok {
		t.Fatalf("not parsed: %s", line)
	}
	return r
}

func TestParseInboundSession(t *testing.T) {
	c := mustParse(t, "Nov 22 19:47:20 mail postfix/smtpd[204]: connect from mx.example.org[203.0.113.7]")
	if c.kind != kindConnect || c.client != "mx.example.org" || c.ip != "203.0.113.7" || c.process != "postfix/smtpd[204]" {
		t.Fatalf("connect: %+v", c)
	}
	tls := mustParse(t, "Nov 22 19:47:20 mail postfix/smtpd[204]: Anonymous TLS connection established from mx.example.org[203.0.113.7]: TLSv1.3 with cipher TLS_AES_256_GCM_SHA384 (256/256 bits) key-exchange X25519 server-signature RSA-PSS (3072 bits) server-digest SHA256")
	if tls.kind != kindInboundTLS || tls.tls != "TLSv1.3 TLS_AES_256_GCM_SHA384" {
		t.Fatalf("tls: %+v", tls)
	}
	client := mustParse(t, "Nov 22 19:47:20 mail postfix/smtpd[204]: F3DA81644E53: client=mx.example.org[203.0.113.7]")
	if client.kind != kindClient || client.queueID != "F3DA81644E53" || client.ip != "203.0.113.7" || client.service != "smtpd" || client.sasl != "" {
		t.Fatalf("client: %+v", client)
	}
	if !client.at.Equal(time.Date(2026, 11, 22, 19, 47, 20, 0, time.UTC)) {
		t.Fatalf("time: %v", client.at)
	}
}

func TestParseSubmission(t *testing.T) {
	r := mustParse(t, "Nov 22 19:49:41 mail postfix/submission/smtpd[236]: 046FF1647A83: client=unknown[192.168.16.1], sasl_method=PLAIN, sasl_username=alice@northwind.test")
	if r.service != "submission" || r.sasl != "alice@northwind.test" || r.client != "unknown" {
		t.Fatalf("submission: %+v", r)
	}
	relay := mustParse(t, "Nov 22 19:49:42 mail postfix/sieve-relay/smtpd[244]: 392C61647A8E: client=aethertrace18-dovecot-1.aethertrace18_default[192.168.16.7], sasl_method=PLAIN, sasl_username=aether-sieve-relay")
	if relay.service != "sieve-relay" || relay.sasl != "aether-sieve-relay" {
		t.Fatalf("sieve relay: %+v", relay)
	}
}

func TestParseCleanupLines(t *testing.T) {
	subject := mustParse(t, "Nov 22 19:47:21 mail postfix/cleanup[209]: 6029E1644E53: info: header Subject: =?utf-8?b?R3LDvMOfZSBhdXMgWsO8cmljaCDigJM=?= Angebot from mail from x[1.2.3.4]; from=<SRS0=seSV=JL=example.org=ceo@mail.aether.test> to=<sales@northwind.test> proto=ESMTP helo=<mx.example.org>")
	if subject.kind != kindHeader || subject.header != "subject" || subject.recipient != "sales@northwind.test" || subject.stage != "info" {
		t.Fatalf("subject: %+v", subject)
	}
	if got := decodeSubject(subject.value); got != "Grüße aus Zürich – Angebot from mail" {
		t.Fatalf("decoded subject: %q", got)
	}
	spam := mustParse(t, "Nov 22 19:49:11 mail postfix/cleanup[227]: 466AB164378D: milter-header-info: header X-Spam-Status: Yes, score=4.97 from unknown[192.168.16.1]; from=<SRS0=f4/9=JL=example.org=promo@mail.aether.test> to=<bob@northwind.test> proto=ESMTP helo=<unknown>: ")
	if spam.header != "x-spam-status" || spam.stage != "milter-header-info" {
		t.Fatalf("spam header: %+v", spam)
	}
	if yes, score, ok := spamVerdict(spam.value); !ok || !yes || score != 4.97 {
		t.Fatalf("verdict: %v %v %v", yes, score, ok)
	}
	id := mustParse(t, "Nov 22 19:47:21 mail postfix/cleanup[209]: F3DA81644E53: message-id=<1795376840953987337@example.org>")
	if id.kind != kindMessageID || id.value != "1795376840953987337@example.org" {
		t.Fatalf("message-id: %+v", id)
	}
	if empty := mustParse(t, "Nov 22 19:49:11 mail postfix/cleanup[227]: 466AB164378D: message-id=<>"); empty.value != "" {
		t.Fatalf("empty message-id: %+v", empty)
	}
}

func TestParseQueueManager(t *testing.T) {
	q := mustParse(t, "Nov 22 19:47:21 mail postfix/qmgr[202]: 528B61644E5A: from=<SRS0=seSV=JL=example.org=ceo@mail.aether.test>, size=651, nrcpt=2 (queue active)")
	if q.kind != kindQueued || q.sender != "ceo@example.org" || q.size != 651 || q.nrcpt != 2 {
		t.Fatalf("queued: %+v", q)
	}
	if r := mustParse(t, "Nov 22 19:47:21 mail postfix/qmgr[202]: 528B61644E5A: removed"); r.kind != kindRemoved {
		t.Fatalf("removed: %+v", r)
	}
	if r := mustParse(t, "Nov 27 03:10:00 mail postfix/qmgr[202]: 528B61644E5A: from=<a@b.test>, status=expired, returned to sender"); r.kind != kindExpired || r.at.Year() != 2025 {
		t.Fatalf("expired: %+v", r)
	}
}

func TestParseDeliveries(t *testing.T) {
	cases := []struct {
		line, kind, recipient, orig, detail string
	}{
		{"Nov 22 19:47:21 mail postfix/lmtp[211]: 6029E1644E53: to=<alice@northwind.test>, orig_to=<sales@northwind.test>, relay=dovecot[192.168.16.7]:24, delay=0.07, delays=0.05/0/0/0.02, dsn=2.0.0, status=sent (250 2.0.0 <alice@northwind.test> QOh7GslGA2sXAAAA48ZKtQ Saved)",
			"delivered", "alice@northwind.test", "sales@northwind.test", "250 2.0.0 <alice@northwind.test> QOh7GslGA2sXAAAA48ZKtQ Saved"},
		{"Nov 22 19:47:21 mail postfix/smtp[214]: 7A0CF1644E53: to=<someone@example.org>, orig_to=<partners@northwind.test>, relay=none, delay=0.21, delays=0.05/0.02/0.14/0, dsn=5.1.0, status=bounced (Domain example.org does not accept mail (nullMX))",
			"bounced", "someone@example.org", "partners@northwind.test", "Domain example.org does not accept mail (nullMX)"},
		{"Nov 22 19:47:21 mail postfix/smtp[214]: ADB3A164506D: to=<ceo@example.org>, orig_to=<SRS0=seSV=JL=example.org=ceo@mail.aether.test>, relay=none, delay=0, delays=0/0/0/0, dsn=5.1.0, status=bounced (Domain example.org does not accept mail (nullMX))",
			"bounced", "ceo@example.org", "", "Domain example.org does not accept mail (nullMX)"},
		{"Nov 22 20:01:00 mail postfix/smtp[301]: 8B2C1647A90: to=<partner@example.com>, relay=mx.example.com[198.51.100.25]:25, conn_use=2, delay=1.2, delays=0.1/0/0.6/0.5, dsn=2.0.0, status=sent (250 2.0.0 OK 1795377000 x7si12345)",
			"relayed", "partner@example.com", "", "250 2.0.0 OK 1795377000 x7si12345"},
		{"Nov 22 20:01:00 mail postfix/smtp[301]: 8B2C1647A91: to=<x@example.com>, orig_to=<partners@northwind.test>, relay=mx.example.com[198.51.100.25]:25, delay=1.2, delays=0.1/0/0.6/0.5, dsn=2.0.0, status=sent (250 2.0.0 OK)",
			"forwarded", "x@example.com", "partners@northwind.test", "250 2.0.0 OK"},
		{"Nov 22 20:02:00 mail postfix/lmtp[229]: 71D1A164301B: to=<alice@northwind.test>, relay=none, delay=30, delays=0.05/0/30/0, dsn=4.4.1, status=deferred (connect to dovecot[192.168.16.7]:24: Connection refused)",
			"deferred", "alice@northwind.test", "", "connect to dovecot[192.168.16.7]:24: Connection refused"},
	}
	for _, c := range cases {
		r := mustParse(t, c.line)
		if r.kind != kindDelivery || deliveryKind(r) != c.kind || r.recipient != c.recipient || r.origRecipient != c.orig || r.detail != c.detail {
			t.Errorf("%s\n got kind=%s %+v", c.line, deliveryKind(r), r)
		}
	}
	tls := mustParse(t, "Nov 22 20:01:00 mail postfix/smtp[301]: Verified TLS connection established to mx.example.com[198.51.100.25]:25: TLSv1.3 with cipher TLS_AES_256_GCM_SHA384 (256/256 bits) key-exchange X25519 server-signature ECDSA (P-256) server-digest SHA256")
	if tls.kind != kindOutboundTLS || tls.relay != "mx.example.com[198.51.100.25]:25" || tls.tls != "TLSv1.3 TLS_AES_256_GCM_SHA384" {
		t.Fatalf("outbound tls: %+v", tls)
	}
}

func TestParseRejections(t *testing.T) {
	noqueue := mustParse(t, "Nov 22 19:47:21 mail postfix/smtpd[204]: NOQUEUE: reject: RCPT from mx.example.org[203.0.113.7]: 550 5.1.1 <nobody@northwind.test>: Recipient address rejected: User unknown in virtual mailbox table; from=<ceo@example.org> to=<nobody@northwind.test> proto=ESMTP helo=<mx.example.org>")
	if noqueue.kind != kindAction || noqueue.queueID != "" || noqueue.stage != "RCPT" || noqueue.dsn != "5.1.1" || noqueue.ip != "203.0.113.7" ||
		noqueue.sender != "ceo@example.org" || noqueue.recipient != "nobody@northwind.test" ||
		noqueue.detail != "550 5.1.1 <nobody@northwind.test>: Recipient address rejected: User unknown in virtual mailbox table" {
		t.Fatalf("noqueue: %+v", noqueue)
	}
	milter := mustParse(t, "Nov 22 19:47:21 mail postfix/cleanup[209]: 6E0741644EF5: milter-reject: END-OF-MESSAGE from unknown[192.168.16.1]: 5.7.1 Gtube pattern; from=<SRS0=SAUa=JL=example.org=spammer@mail.aether.test> to=<bob@northwind.test> proto=ESMTP helo=<mx.example.org>")
	if milter.status != "reject" || milter.stage != "END-OF-MESSAGE" || milter.detail != "5.7.1 Gtube pattern" || milter.sender != "spammer@example.org" || milter.dsn != "5.7.1" {
		t.Fatalf("milter: %+v", milter)
	}
	header := mustParse(t, "Nov 22 20:05:00 mail postfix/cleanup[209]: 1A2B3C4D5E: reject: header Subject: buy now from unknown[192.168.16.1]; from=<a@example.org> to=<bob@northwind.test> proto=ESMTP helo=<x>: 5.7.1 Message content rejected")
	if header.stage != "" || header.detail != "5.7.1 Message content rejected" {
		t.Fatalf("header reject: %+v", header)
	}
	hold := mustParse(t, "Nov 22 20:05:00 mail postfix/cleanup[209]: 1A2B3C4D5F: milter-hold: END-OF-MESSAGE from unknown[192.168.16.1]: milter triggers HOLD action; from=<a@example.org> to=<bob@northwind.test> proto=ESMTP helo=<x>")
	if hold.status != "hold" || actionKinds[hold.status] != "held" {
		t.Fatalf("hold: %+v", hold)
	}
}

func TestParseIgnoresNoise(t *testing.T) {
	for _, line := range []string{
		"Nov 22 19:46:25 mail postfix/master[1]: daemon started -- version 3.7.11, configuration /etc/postfix",
		"Nov 22 19:47:23 mail postfix/submission/smtpd[215]: warning: unknown[192.168.16.1]: SASL PLAIN authentication failed: (reason unavailable), sasl_username=alice@northwind.test",
		"Nov 22 19:47:21 mail postfix/cleanup[209]: F3DA81644E53: warning: header Subject: x from y[1.2.3.4]; from=<a@b> to=<c@d>",
		"Nov 22 19:47:21 mail dovecot: lmtp(alice@northwind.test): saved mail to INBOX",
		"garbage",
	} {
		if r, ok := parse(line, now); ok {
			t.Errorf("parsed noise %q as %+v", line, r)
		}
	}
}

func TestAddressDecodesSRS(t *testing.T) {
	cases := map[string]string{
		"SRS0=seSV=JL=example.org=ceo@mail.aether.test":               "ceo@example.org",
		"SRS1=Xa1b=forwarder.test==HHHH=TT=origin.test=jo@relay.test": "jo@origin.test",
		"Alice@Northwind.Test":                                        "alice@northwind.test",
		"":                                                            "",
		"srs0@example.org":                                            "srs0@example.org",
	}
	for in, want := range cases {
		if got := address(in); got != want {
			t.Errorf("address(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeSubjectUnfoldsLoggedFolds(t *testing.T) {
	logged := "A very long subject line that keeps going and going A very long? subject line that keeps going and going A very long subject line that keeps? going and going "
	want := "A very long subject line that keeps going and going A very long subject line that keeps going and going A very long subject line that keeps going and going"
	if got := decodeSubject(logged); got != want {
		t.Fatalf("got %q", got)
	}
	if got := decodeSubject("Are you there? Call me"); got != "Are you there? Call me" {
		t.Fatalf("short subject changed: %q", got)
	}
	if got := decodeSubject("=?utf-8?q?Caf=C3=A9_au_lait_et_croissants_pour_toute_l=27=C3=A9quipe_du?=? =?utf-8?q?_bureau?="); got != "Café au lait et croissants pour toute l'équipe du bureau" {
		t.Fatalf("folded encoded words: %q", got)
	}
}
