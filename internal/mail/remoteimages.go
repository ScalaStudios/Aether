package mail

import (
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const maxProxiedImages = 40

type RemoteImageStats struct {
	Remote   int
	Trackers int
}

var imageBlockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

var trackerHosts = map[string]struct{}{
	"mailtrack.io":                  {},
	"t.yesware.com":                 {},
	"app.yesware.com":               {},
	"t.sidekickopen.com":            {},
	"t.sigopn01.com":                {},
	"t.sigopn02.com":                {},
	"t.sigopn03.com":                {},
	"t.sigopn04.com":                {},
	"t.sigopn05.com":                {},
	"bananatag.com":                 {},
	"bl-1.com":                      {},
	"t.contactmonkey.com":           {},
	"track.contactmonkey.com":       {},
	"email.mixmax.com":              {},
	"track.mixmax.com":              {},
	"t.streak.com":                  {},
	"mailfoogae.appspot.com":        {},
	"t.hubspotemail.net":            {},
	"track.hubspot.com":             {},
	"pstmrk.it":                     {},
	"spgo.io":                       {},
	"go.sparkpostmail.com":          {},
	"clicks.sparkpostmail.com":      {},
	"trk.klclick.com":               {},
	"trk.klaviyomail.com":           {},
	"ct.sendgrid.net":               {},
	"emltrk.com":                    {},
	"mltrk.io":                      {},
	"pixel.monitor1.returnpath.net": {},
	"returnpath.net":                {},
	"infusionmail.com":              {},
	"track.salesloft.com":           {},
	"salesloft-mail.com":            {},
	"track.outreach.io":             {},
	"toutapp.com":                   {},
	"tracking.cirrusinsight.com":    {},
	"sendibm1.com":                  {},
	"sendibm2.com":                  {},
	"sendib.com":                    {},
	"tinyletter.com":                {},
	"tinyletterapp.com":             {},
	"getnotify.com":                 {},
	"pixel.wp.com":                  {},
	"track.hubspot.net":             {},
}

var trackerPaths = regexp.MustCompile(`(?i)(?:^|/)(?:wf/open|track/(?:open|click)(?:\.php)?|email/open(?:\.gif)?|open\.php|pixel\.(?:gif|png)|beacon\.(?:gif|png)|spacer\.gif|(?:ss|mpss)/o)(?:$|/)`)

var trackingQueryKeys = map[string]struct{}{
	"utm_source": {}, "utm_medium": {}, "utm_campaign": {}, "utm_term": {}, "utm_content": {}, "utm_id": {}, "utm_name": {},
	"mc_eid": {}, "mc_cid": {},
	"fbclid": {}, "gclid": {}, "msclkid": {},
	"_hsenc": {}, "_hsmi": {},
	"mkt_tok": {},
	"vero_id": {}, "vero_conv": {},
	"ml_subscriber": {}, "ml_subscriber_hash": {},
	"oly_anon_id": {}, "oly_enc_id": {},
	"_ke": {},
}

var cssURL = regexp.MustCompile(`(?i)url\s*\([^)]*\)`)

func PresentRemoteImages(raw string, show bool, allow func(normalized string) (proxyURL string, ok bool)) (string, RemoteImageStats) {
	clean := SanitizeMessageHTML(raw)
	if strings.TrimSpace(clean) == "" {
		return clean, RemoteImageStats{}
	}
	ctx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(clean), ctx)
	if err != nil {
		return SanitizeHTML(raw), RemoteImageStats{}
	}
	var stats RemoteImageStats
	proxied := 0
	for _, n := range nodes {
		rewriteNode(n, show, allow, &stats, &proxied)
	}
	var buf strings.Builder
	for _, n := range nodes {
		if err := html.Render(&buf, n); err != nil {
			return SanitizeHTML(raw), RemoteImageStats{}
		}
	}
	return buf.String(), stats
}

func NormalizeImageURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	if len(raw) > 2048 || strings.ContainsAny(raw, "\r\n\x00") {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || u.Opaque != "" {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if !imageHostOK(host) {
		return "", false
	}
	port := u.Port()
	if port != "" && port != "80" && port != "443" {
		return "", false
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	q := u.Query()
	stripped := false
	for key := range q {
		if _, ok := trackingQueryKeys[strings.ToLower(key)]; ok {
			q.Del(key)
			stripped = true
		}
	}
	if stripped {
		u.RawQuery = q.Encode()
	}
	u.Fragment = ""
	out := u.String()
	if len(out) > 2048 {
		return "", false
	}
	return out, true
}

func IsTrackingImageURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return trackingHost(u.Hostname()) || trackerPaths.MatchString(u.EscapedPath())
}

func imageHostOK(host string) bool {
	switch host {
	case "", "localhost", "metadata.google.internal":
		return false
	}
	if strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".localdomain") {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return publicImageAddr(ip)
	}
	return true
}

func publicImageAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, p := range imageBlockedPrefixes {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

func trackingHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	for host != "" {
		if _, ok := trackerHosts[host]; ok {
			return true
		}
		_, rest, ok := strings.Cut(host, ".")
		if !ok {
			return false
		}
		host = rest
	}
	return false
}

func rewriteNode(n *html.Node, show bool, allow func(string) (string, bool), stats *RemoteImageStats, proxied *int) {
	if n.Type == html.ElementNode {
		scrubStyle(n)
		if n.Data == "img" {
			rewriteImg(n, show, allow, stats, proxied)
		} else {
			stripEmbeddedRemote(n)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		rewriteNode(c, show, allow, stats, proxied)
	}
}

func rewriteImg(n *html.Node, show bool, allow func(string) (string, bool), stats *RemoteImageStats, proxied *int) {
	removeAttr(n, "srcset")
	src := strings.TrimSpace(attr(n, "src"))
	if src == "" || localImageSrc(src) {
		return
	}
	normalized, ok := NormalizeImageURL(src)
	if !ok {
		removeAttr(n, "src")
		return
	}
	if IsTrackingImageURL(normalized) || tinyTrackingPixel(n, normalized) {
		removeAttr(n, "src")
		stats.Trackers++
		return
	}
	proxyURL, allowed := "", false
	if allow != nil {
		proxyURL, allowed = allow(normalized)
	}
	if !allowed {
		removeAttr(n, "src")
		return
	}
	stats.Remote++
	if !show || *proxied >= maxProxiedImages || proxyURL == "" || strings.ContainsAny(proxyURL, "\"' <>\r\n") {
		removeAttr(n, "src")
		return
	}
	setAttr(n, "src", proxyURL)
	*proxied++
}

func tinyTrackingPixel(n *html.Node, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return false
	}
	w, wok := attrDim(n, "width")
	h, hok := attrDim(n, "height")
	if wok && hok && w <= 1 && h <= 1 {
		return true
	}
	style := strings.ToLower(strings.ReplaceAll(attr(n, "style"), " ", ""))
	return strings.Contains(style, "width:1px") && strings.Contains(style, "height:1px")
}

func stripEmbeddedRemote(n *html.Node) {
	for _, key := range []string{"src", "srcset", "poster", "background"} {
		if val := attr(n, key); val != "" && !localImageSrc(val) {
			removeAttr(n, key)
		}
	}
	if n.Data == "object" || n.Data == "embed" {
		if val := attr(n, "data"); val != "" && !localImageSrc(val) {
			removeAttr(n, "data")
		}
	}
}

func scrubStyle(n *html.Node) {
	style := attr(n, "style")
	if style == "" || !strings.Contains(strings.ToLower(style), "url") {
		return
	}
	cleaned := strings.TrimSpace(cssURL.ReplaceAllString(style, ""))
	if cleaned == "" || cleaned == ";" {
		removeAttr(n, "style")
		return
	}
	setAttr(n, "style", cleaned)
}

func localImageSrc(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(lower, "data:image/") || strings.HasPrefix(lower, "cid:")
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func setAttr(n *html.Node, key, val string) {
	for i := range n.Attr {
		if strings.EqualFold(n.Attr[i].Key, key) {
			n.Attr[i].Key = key
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func removeAttr(n *html.Node, key string) {
	kept := n.Attr[:0]
	for _, a := range n.Attr {
		if !strings.EqualFold(a.Key, key) {
			kept = append(kept, a)
		}
	}
	n.Attr = kept
}

func attrDim(n *html.Node, key string) (int, bool) {
	v := strings.TrimSpace(strings.ToLower(attr(n, key)))
	v = strings.TrimSuffix(v, "px")
	if v == "" {
		return 0, false
	}
	i, err := strconv.Atoi(v)
	if err != nil || i < 0 {
		return 0, false
	}
	return i, true
}
