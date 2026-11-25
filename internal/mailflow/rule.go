package mailflow

import (
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

type Condition struct {
	Field  string   `json:"field"`
	Scope  string   `json:"scope,omitempty"`
	Header string   `json:"header,omitempty"`
	Values []string `json:"values,omitempty"`
	Number float64  `json:"number,omitempty"`
}

type Disclaimer struct {
	Text     string `json:"text"`
	HTML     string `json:"html,omitempty"`
	Fallback string `json:"fallback"`
}

type HeaderValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Actions struct {
	Disclaimer    *Disclaimer   `json:"disclaimer,omitempty"`
	Bcc           []string      `json:"bcc,omitempty"`
	RedirectTo    []string      `json:"redirectTo,omitempty"`
	Reject        string        `json:"reject,omitempty"`
	AddHeaders    []HeaderValue `json:"addHeaders,omitempty"`
	RemoveHeaders []string      `json:"removeHeaders,omitempty"`
	SpamScore     *float64      `json:"spamScore,omitempty"`
	RequireTLS    bool          `json:"requireTls,omitempty"`
	Approvers     []string      `json:"approvers,omitempty"`
}

type Rule struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Mode       string      `json:"mode"`
	Conditions []Condition `json:"conditions"`
	Exceptions []Condition `json:"exceptions"`
	Actions    Actions     `json:"actions"`
	Stop       bool        `json:"stop"`
}

var (
	headerName    = regexp.MustCompile(`^[A-Za-z0-9-]{1,76}$`)
	domainPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	textFields    = []string{"subject", "body", "header", "attachmentName", "attachmentType"}
	scopes        = []string{"address", "domain", "inside", "outside", "memberOf"}
	directions    = []string{"incoming", "outgoing", "internal"}
	protected     = []string{"received", "from", "sender", "to", "cc", "bcc", "subject", "date", "message-id", "mime-version", "return-path", "dkim-signature", "arc-seal", "arc-message-signature", "arc-authentication-results", "authentication-results", "x-spam-status"}
)

func (r *Rule) SpamDependent() bool {
	for _, c := range slices.Concat(r.Conditions, r.Exceptions) {
		if c.Field == "spamScoreOver" {
			return true
		}
	}
	return false
}

func clean(values []string, lower bool) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if lower {
			v = strings.ToLower(v)
		}
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

func validAddress(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s && len(s) <= 254
}

func protectedHeader(name string) bool {
	name = strings.ToLower(name)
	return slices.Contains(protected, name) || strings.HasPrefix(name, "content-") || strings.HasPrefix(name, "x-aether-")
}

func (c *Condition) validate() error {
	switch c.Field {
	case "sender", "recipient":
		if !slices.Contains(scopes, c.Scope) {
			return errors.New("Choose how to match the sender or recipient.")
		}
		c.Values = clean(c.Values, true)
		switch c.Scope {
		case "inside", "outside":
			c.Values = nil
			return nil
		case "domain":
			for i, v := range c.Values {
				c.Values[i] = strings.TrimPrefix(v, "@")
				if !domainPattern.MatchString(c.Values[i]) {
					return fmt.Errorf("%s is not a domain name.", v)
				}
			}
		default:
			for _, v := range c.Values {
				if !validAddress(v) {
					return fmt.Errorf("%s is not an email address.", v)
				}
			}
		}
	case "direction":
		c.Values = clean(c.Values, true)
		for _, v := range c.Values {
			if !slices.Contains(directions, v) {
				return errors.New("The direction can be incoming, outgoing or internal.")
			}
		}
	case "sizeOver":
		if c.Number < 1 || c.Number > 1<<20 {
			return errors.New("Enter a size between 1 KB and 1 GB.")
		}
		c.Values = nil
		return nil
	case "spamScoreOver":
		if c.Number < -100 || c.Number > 100 {
			return errors.New("Enter a spam score between -100 and 100.")
		}
		c.Values = nil
		return nil
	default:
		if !slices.Contains(textFields, c.Field) {
			return errors.New("Conditions can check the sender, recipients, subject, body, a header, attachments, size, spam score or direction.")
		}
		if c.Field == "header" && !headerName.MatchString(c.Header) {
			return errors.New("Enter a header name such as List-Id, using letters, digits and hyphens.")
		}
		c.Values = clean(c.Values, false)
		for _, v := range c.Values {
			if len(v) > 500 || strings.ContainsAny(v, "\r\n\x00") {
				return errors.New("Keep each value to one line of up to 500 characters.")
			}
		}
	}
	if c.Field != "header" {
		c.Header = ""
	}
	if c.Field != "sender" && c.Field != "recipient" {
		c.Scope = ""
	}
	c.Number = 0
	if len(c.Values) == 0 || len(c.Values) > 100 {
		return errors.New("Each condition needs between 1 and 100 values.")
	}
	return nil
}

func addresses(list []string, what string) ([]string, error) {
	list = clean(list, true)
	if len(list) > 20 {
		return nil, fmt.Errorf("%s takes up to 20 addresses.", what)
	}
	for _, a := range list {
		if !validAddress(a) {
			return nil, fmt.Errorf("%s is not an email address.", a)
		}
	}
	return list, nil
}

func (a *Actions) validate() error {
	var err error
	if a.Bcc, err = addresses(a.Bcc, "Copy to"); err != nil {
		return err
	}
	if a.RedirectTo, err = addresses(a.RedirectTo, "Redirect to"); err != nil {
		return err
	}
	if a.Approvers, err = addresses(a.Approvers, "Approval"); err != nil {
		return err
	}
	a.Reject = strings.TrimSpace(a.Reject)
	if a.Reject != "" {
		if utf8.RuneCountInString(a.Reject) > 200 || strings.ContainsAny(a.Reject, "\r\n\x00") || !isASCII(a.Reject) {
			return errors.New("Write the rejection message in plain ASCII, on one line of up to 200 characters.")
		}
		if a.Disclaimer != nil || len(a.Bcc) > 0 || len(a.RedirectTo) > 0 || len(a.AddHeaders) > 0 || len(a.RemoveHeaders) > 0 || a.SpamScore != nil || a.RequireTLS || len(a.Approvers) > 0 {
			return errors.New("A rule that rejects messages cannot have other actions.")
		}
		return nil
	}
	if d := a.Disclaimer; d != nil {
		d.Text, d.HTML = strings.TrimSpace(d.Text), strings.TrimSpace(d.HTML)
		switch {
		case d.Text == "" || len(d.Text) > 10000:
			return errors.New("Write the disclaimer text, up to 10,000 characters.")
		case len(d.HTML) > 32<<10:
			return errors.New("Keep the disclaimer HTML under 32 KB.")
		case d.Fallback != "wrap" && d.Fallback != "skip" && d.Fallback != "reject":
			return errors.New("Choose what happens to signed or encrypted messages: wrap, skip or reject.")
		}
	}
	if len(a.AddHeaders) > 10 || len(a.RemoveHeaders) > 10 {
		return errors.New("Add or remove up to 10 headers.")
	}
	for i := range a.AddHeaders {
		h := &a.AddHeaders[i]
		h.Name, h.Value = strings.TrimSpace(h.Name), strings.TrimSpace(h.Value)
		if !headerName.MatchString(h.Name) || protectedHeader(h.Name) {
			return fmt.Errorf("%q cannot be added. Use a custom header name such as X-Department.", h.Name)
		}
		if h.Value == "" || len(h.Value) > 500 || strings.ContainsAny(h.Value, "\r\n\x00") || !isASCII(h.Value) {
			return errors.New("Give each added header a value in plain ASCII, on one line of up to 500 characters.")
		}
	}
	a.RemoveHeaders = clean(a.RemoveHeaders, false)
	for _, h := range a.RemoveHeaders {
		if !headerName.MatchString(h) || protectedHeader(h) {
			return fmt.Errorf("%q cannot be removed.", h)
		}
	}
	if a.SpamScore != nil && (*a.SpamScore < -50 || *a.SpamScore > 50 || *a.SpamScore == 0) {
		return errors.New("Change the spam score by an amount between -50 and 50.")
	}
	if a.Disclaimer == nil && len(a.Bcc) == 0 && len(a.RedirectTo) == 0 && len(a.AddHeaders) == 0 && len(a.RemoveHeaders) == 0 && a.SpamScore == nil && !a.RequireTLS && len(a.Approvers) == 0 {
		return errors.New("Choose at least one action.")
	}
	return nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func (r *Rule) Validate() error {
	r.Name = strings.TrimSpace(r.Name)
	switch {
	case r.Name == "" || utf8.RuneCountInString(r.Name) > 100:
		return errors.New("Give the rule a name of up to 100 characters.")
	case r.Mode != "enforce" && r.Mode != "test" && r.Mode != "off":
		return errors.New("The mode must be enforce, test or off.")
	case len(r.Conditions) > 20 || len(r.Exceptions) > 20:
		return errors.New("A rule can have up to 20 conditions and 20 exceptions.")
	}
	r.Conditions, r.Exceptions = nonNil(r.Conditions), nonNil(r.Exceptions)
	for _, list := range [][]Condition{r.Conditions, r.Exceptions} {
		for i := range list {
			if err := list[i].validate(); err != nil {
				return err
			}
		}
	}
	if err := r.Actions.validate(); err != nil {
		return err
	}
	if r.SpamDependent() && (r.Actions.Disclaimer != nil || r.Actions.SpamScore != nil) {
		return errors.New("Rules that test the spam score run after spam filtering, so they cannot add a disclaimer or change the score.")
	}
	return nil
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}
