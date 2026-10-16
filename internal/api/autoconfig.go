package api

import (
	"context"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

func hostDomain(host string, prefixes ...string) (string, bool) {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	for _, prefix := range prefixes {
		if name, ok := strings.CutPrefix(host, prefix); ok {
			return name, true
		}
	}
	return "", false
}

func (s *Server) verifiedDomain(ctx context.Context, name string) bool {
	var found bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM domains WHERE name = $1 AND verified_at IS NOT NULL AND active)`, strings.ToLower(name)).Scan(&found)
	return err == nil && found
}

func addressDomain(address string) (string, bool) {
	a, err := mail.ParseAddress(address)
	if err != nil {
		return "", false
	}
	_, d, ok := strings.Cut(a.Address, "@")
	return strings.ToLower(d), ok && d != ""
}

type autoconfigServer struct {
	Type           string `xml:"type,attr"`
	Hostname       string `xml:"hostname"`
	Port           int    `xml:"port"`
	SocketType     string `xml:"socketType"`
	Authentication string `xml:"authentication"`
	Username       string `xml:"username"`
}

type autoconfigDoc struct {
	XMLName  xml.Name `xml:"clientConfig"`
	Version  string   `xml:"version,attr"`
	Provider struct {
		ID               string             `xml:"id,attr"`
		Domain           string             `xml:"domain"`
		DisplayName      string             `xml:"displayName"`
		DisplayShortName string             `xml:"displayShortName"`
		Incoming         []autoconfigServer `xml:"incomingServer"`
		Outgoing         []autoconfigServer `xml:"outgoingServer"`
	} `xml:"emailProvider"`
}

func (s *Server) mailAutoconfig(w http.ResponseWriter, r *http.Request) {
	name, ok := addressDomain(r.URL.Query().Get("emailaddress"))
	if !ok {
		name, ok = hostDomain(r.Host, "autoconfig.")
	}
	if !ok || !s.verifiedDomain(r.Context(), name) {
		http.NotFound(w, r)
		return
	}
	host := s.cfg.MailHostname
	var doc autoconfigDoc
	doc.Version = "1.1"
	doc.Provider.ID = name
	doc.Provider.Domain = name
	doc.Provider.DisplayName = name
	doc.Provider.DisplayShortName = name
	doc.Provider.Incoming = []autoconfigServer{
		{"imap", host, 993, "SSL", "password-cleartext", "%EMAILADDRESS%"},
		{"imap", host, 143, "STARTTLS", "password-cleartext", "%EMAILADDRESS%"},
	}
	doc.Provider.Outgoing = []autoconfigServer{
		{"smtp", host, 465, "SSL", "password-cleartext", "%EMAILADDRESS%"},
		{"smtp", host, 587, "STARTTLS", "password-cleartext", "%EMAILADDRESS%"},
	}
	writeXML(w, &doc)
}

type autodiscoverProtocol struct {
	Type           string `xml:"Type"`
	Server         string `xml:"Server"`
	Port           int    `xml:"Port"`
	LoginName      string `xml:"LoginName"`
	DomainRequired string `xml:"DomainRequired"`
	SPA            string `xml:"SPA"`
	SSL            string `xml:"SSL"`
	AuthRequired   string `xml:"AuthRequired"`
}

type autodiscoverDoc struct {
	XMLName  xml.Name `xml:"http://schemas.microsoft.com/exchange/autodiscover/responseschema/2006 Autodiscover"`
	Response struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/exchange/autodiscover/outlook/responseschema/2006a Response"`
		Account struct {
			AccountType string                 `xml:"AccountType"`
			Action      string                 `xml:"Action"`
			Protocols   []autodiscoverProtocol `xml:"Protocol"`
		} `xml:"Account"`
	}
}

func (s *Server) mailAutodiscover(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address string `xml:"Request>EMailAddress"`
	}
	if r.Method == http.MethodPost {
		xml.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req)
	} else {
		req.Address = r.URL.Query().Get("email")
	}
	name, ok := addressDomain(req.Address)
	if !ok || !s.verifiedDomain(r.Context(), name) {
		http.NotFound(w, r)
		return
	}
	login := strings.ToLower(strings.TrimSpace(req.Address))
	var doc autodiscoverDoc
	doc.Response.Account.AccountType = "email"
	doc.Response.Account.Action = "settings"
	doc.Response.Account.Protocols = []autodiscoverProtocol{
		{"IMAP", s.cfg.MailHostname, 993, login, "off", "off", "on", "on"},
		{"SMTP", s.cfg.MailHostname, 465, login, "off", "off", "on", "on"},
	}
	writeXML(w, &doc)
}

func writeXML(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "max-age=300")
	io.WriteString(w, xml.Header)
	xml.NewEncoder(w).Encode(v)
}

type plist []string

func (p *plist) key(k string, v any) {
	var b strings.Builder
	b.WriteString("<key>")
	xml.EscapeText(&b, []byte(k))
	b.WriteString("</key>")
	switch v := v.(type) {
	case string:
		b.WriteString("<string>")
		xml.EscapeText(&b, []byte(v))
		b.WriteString("</string>")
	case int:
		b.WriteString("<integer>" + strconv.Itoa(v) + "</integer>")
	case bool:
		if v {
			b.WriteString("<true/>")
		} else {
			b.WriteString("<false/>")
		}
	case []plist:
		b.WriteString("<array>")
		for _, d := range v {
			b.WriteString(d.dict())
		}
		b.WriteString("</array>")
	}
	*p = append(*p, b.String())
}

func (p plist) dict() string {
	return "<dict>" + strings.Join(p, "") + "</dict>"
}

func (s *Server) appleProfile(w http.ResponseWriter, r *http.Request, p *principal) {
	var in struct {
		MailboxID string `json:"mailboxId"`
	}
	if !decode(w, r, 8<<10, &in) {
		return
	}
	created, ok := s.issueAppPassword(w, r, p, in.MailboxID, "Apple device profile")
	if !ok {
		return
	}
	webHost := s.cfg.PublicURL.Hostname()
	address := created.MailboxAddress
	identifier := "aether." + webHost + "." + created.ID
	payload := func(kind, suffix, display string) plist {
		var d plist
		d.key("PayloadType", kind)
		d.key("PayloadVersion", 1)
		d.key("PayloadIdentifier", identifier+"."+suffix)
		d.key("PayloadUUID", strings.ToUpper(uuid.NewString()))
		d.key("PayloadDisplayName", display)
		return d
	}

	mailPayload := payload("com.apple.mail.managed", "mail", "Mail")
	mailPayload.key("EmailAccountDescription", address)
	mailPayload.key("EmailAccountName", p.User.DisplayName)
	mailPayload.key("EmailAccountType", "EmailTypeIMAP")
	mailPayload.key("EmailAddress", address)
	mailPayload.key("IncomingMailServerAuthentication", "EmailAuthPassword")
	mailPayload.key("IncomingMailServerHostName", s.cfg.MailHostname)
	mailPayload.key("IncomingMailServerPortNumber", 993)
	mailPayload.key("IncomingMailServerUseSSL", true)
	mailPayload.key("IncomingMailServerUsername", address)
	mailPayload.key("IncomingPassword", created.Password)
	mailPayload.key("OutgoingMailServerAuthentication", "EmailAuthPassword")
	mailPayload.key("OutgoingMailServerHostName", s.cfg.MailHostname)
	mailPayload.key("OutgoingMailServerPortNumber", 465)
	mailPayload.key("OutgoingMailServerUseSSL", true)
	mailPayload.key("OutgoingMailServerUsername", address)
	mailPayload.key("OutgoingPasswordSameAsIncomingPassword", true)

	calendar := payload("com.apple.caldav.account", "caldav", "Calendar")
	calendar.key("CalDAVAccountDescription", address)
	calendar.key("CalDAVHostName", webHost)
	calendar.key("CalDAVPort", 443)
	calendar.key("CalDAVUseSSL", true)
	calendar.key("CalDAVUsername", address)
	calendar.key("CalDAVPassword", created.Password)

	contacts := payload("com.apple.carddav.account", "carddav", "Contacts")
	contacts.key("CardDAVAccountDescription", address)
	contacts.key("CardDAVHostName", webHost)
	contacts.key("CardDAVPort", 443)
	contacts.key("CardDAVUseSSL", true)
	contacts.key("CardDAVUsername", address)
	contacts.key("CardDAVPassword", created.Password)

	var root plist
	root.key("PayloadContent", []plist{mailPayload, calendar, contacts})
	root.key("PayloadDisplayName", "Aether: "+address)
	root.key("PayloadDescription", "Mail, calendar and contacts for "+address+". Remove this profile to disconnect the device; the app password inside it can also be revoked in Aether.")
	root.key("PayloadIdentifier", identifier)
	root.key("PayloadType", "Configuration")
	root.key("PayloadUUID", strings.ToUpper(uuid.NewString()))
	root.key("PayloadVersion", 1)

	w.Header().Set("Content-Type", "application/x-apple-aspen-config")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(address, `"`, "")+`.mobileconfig"`)
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, xml.Header+`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">`+"\n"+`<plist version="1.0">`+root.dict()+"</plist>\n")
}
