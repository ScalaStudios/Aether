package mailtrace

import "slices"

var Severity = []string{"rejected", "bounced", "discarded", "deleted", "deferred", "held", "queued", "unknown", "junk", "delivered"}

type traceEvent struct {
	kind      string
	recipient string
	orig      string
}

func worst(a, b string) string {
	if a == "" || (b != "" && slices.Index(Severity, b) < slices.Index(Severity, a)) {
		return b
	}
	return a
}

func summarize(events []traceEvent, recipients []string, spam, removed bool, expected int) (string, map[string]string) {
	latest := map[string]string{}
	origins := map[string][]string{}
	final, held, expired := "", false, false
	for _, e := range events {
		switch {
		case e.kind == "held":
			held = true
		case e.kind == "released":
			held = false
		case e.kind == "expired":
			expired = true
		case e.kind == "notified":
		case e.recipient == "":
			final = e.kind
		default:
			latest[e.recipient] = e.kind
			if e.orig != "" && !slices.Contains(origins[e.orig], e.recipient) {
				origins[e.orig] = append(origins[e.orig], e.recipient)
			}
		}
	}
	status := func(k string) string {
		delivered := k == "delivered" || k == "relayed" || k == "forwarded"
		switch {
		case delivered && k == "delivered" && spam:
			return "junk"
		case delivered:
			return "delivered"
		case final != "":
			return final
		case k == "bounced" || k == "expired" || expired:
			return "bounced"
		case k == "rejected":
			return "rejected"
		case held:
			return "held"
		case k == "deferred":
			return "deferred"
		case removed:
			return "unknown"
		}
		return "queued"
	}
	per := map[string]string{}
	for _, a := range recipients {
		if targets, ok := origins[a]; ok && latest[a] == "" {
			for _, t := range targets {
				per[a] = worst(per[a], status(latest[t]))
			}
			continue
		}
		per[a] = status(latest[a])
	}
	overall := ""
	for _, s := range per {
		overall = worst(overall, s)
	}
	if !removed && len(latest) < expected {
		overall = worst(overall, status(""))
	}
	if overall == "" {
		overall = status("")
	}
	return overall, per
}

func deliveryKind(r record) string {
	switch r.status {
	case "sent":
		switch {
		case r.daemon == "lmtp" || r.daemon == "local" || r.daemon == "virtual":
			return "delivered"
		case r.origRecipient != "":
			return "forwarded"
		}
		return "relayed"
	case "deferred", "bounced", "expired":
		return r.status
	}
	return ""
}

var actionKinds = map[string]string{"reject": "rejected", "discard": "discarded", "hold": "held"}
