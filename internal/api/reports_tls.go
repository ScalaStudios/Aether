package api

import (
	"bufio"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type tlsReportPolicy struct {
	Policy struct {
		Type   string `json:"policy-type"`
		Domain string `json:"policy-domain"`
	} `json:"policy"`
	Summary struct {
		Successful int `json:"total-successful-session-count"`
		Failed     int `json:"total-failure-session-count"`
	} `json:"summary"`
	Failures []tlsReportFailure `json:"failure-details,omitempty"`
}

type tlsReportFailure struct {
	Type  string `json:"result-type"`
	MX    string `json:"receiving-mx-hostname,omitempty"`
	IP    string `json:"receiving-ip,omitempty"`
	Count int    `json:"failed-session-count"`
}

type tlsReport struct {
	Organization string `json:"organization-name"`
	DateRange    struct {
		Start time.Time `json:"start-datetime"`
		End   time.Time `json:"end-datetime"`
	} `json:"date-range"`
	Contact  string            `json:"contact-info"`
	ID       string            `json:"report-id"`
	Policies []tlsReportPolicy `json:"policies"`
}

var (
	postfixReportLine = regexp.MustCompile(`^(.{15}) [^ ]+ postfix/(smtp|qmgr)\[([0-9]+)\]: (.*)$`)
	postfixTLSSession = regexp.MustCompile(`^(Anonymous|Untrusted|Trusted|Verified) TLS connection established to ([^ ]+): TLSv`)
	postfixTLSFailure = regexp.MustCompile(`^([A-Za-z0-9]+): to=<([^>]*)>, .*relay=([^, ]+), .*status=(?:deferred|bounced) \((TLS is required, but was not offered by host|Cannot start TLS|Server certificate not trusted|Server certificate not verified|TLSA lookup error)(?:[ :)]|$)`)
	postfixSMTPResult = regexp.MustCompile(`^([A-Za-z0-9]+): to=<([^>]*)>, .*?relay=([^, ]+),`)
	postfixQueueFrom  = regexp.MustCompile(`^([A-Za-z0-9]+): from=<([^>]*)>,`)
	postfixRelay      = regexp.MustCompile(`^([^[]+)\[([^]]+)\]:[0-9]+$`)
)

type loggedTLSSession struct {
	relay, policy string
	at            time.Time
	counted       map[string]bool
}

func readTLSReportLog(path string, from string, day time.Time) (map[string][]tlsReportPolicy, error) {
	paths, err := filepath.Glob(path + ".*")
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	paths = append(paths, path)
	end := day.AddDate(0, 0, 1)
	policies := map[string]map[string]*tlsReportPolicy{}
	sessions := map[string]loggedTLSSession{}
	reportQueueIDs := map[string]bool{}
	for _, name := range paths {
		info, err := os.Stat(name)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if !info.Mode().IsRegular() || info.ModTime().Before(day.AddDate(0, 0, -1)) {
			continue
		}
		err = scanPostfixLog(name, func(line string) {
			match := postfixReportLine.FindStringSubmatch(line)
			if match == nil {
				return
			}
			at, err := time.Parse(time.Stamp, match[1])
			if err != nil {
				return
			}
			at = at.AddDate(day.Year(), 0, 0)
			if at.After(day.AddDate(0, 6, 0)) {
				at = at.AddDate(-1, 0, 0)
			}
			if match[2] == "qmgr" {
				if m := postfixQueueFrom.FindStringSubmatch(match[4]); m != nil {
					reportQueueIDs[m[1]] = strings.EqualFold(m[2], from) || m[2] == ""
				}
				return
			}
			pid, detail := match[3], match[4]
			if m := postfixTLSSession.FindStringSubmatch(detail); m != nil {
				policy := "no-policy-found"
				if m[1] == "Verified" {
					policy = "tlsa"
				}
				sessions[pid] = loggedTLSSession{m[2], policy, at, map[string]bool{}}
				return
			}
			if at.Before(day) || !at.Before(end) {
				return
			}
			result := postfixSMTPResult.FindStringSubmatch(detail)
			if result == nil || reportQueueIDs[result[1]] {
				return
			}
			domain, _ := addressDomain(result[2])
			if !domainPattern.MatchString(domain) {
				return
			}

			if m := postfixTLSFailure.FindStringSubmatch(detail); m != nil {
				delete(sessions, pid)
				failure := tlsReportFailure{Type: "validation-failure", Count: 1}
				policyKind := "no-policy-found"
				switch m[4] {
				case "TLS is required, but was not offered by host":
					failure.Type = "starttls-not-supported"
				case "Server certificate not trusted":
					failure.Type = "certificate-not-trusted"
				case "Server certificate not verified":
					failure.Type = "certificate-not-trusted"
					policyKind = "tlsa"
				case "TLSA lookup error":
					failure.Type = "tlsa-invalid"
					policyKind = "tlsa"
				}
				if relay := postfixRelay.FindStringSubmatch(m[3]); relay != nil {
					failure.MX = strings.ToLower(relay[1])
					if _, err := netip.ParseAddr(relay[2]); err == nil {
						failure.IP = relay[2]
					}
				}
				policy := tlsPolicy(policies, domain, policyKind)
				policy.Summary.Failed++
				for i := range policy.Failures {
					candidate := &policy.Failures[i]
					if candidate.Type == failure.Type && candidate.MX == failure.MX && candidate.IP == failure.IP {
						candidate.Count++
						return
					}
				}
				policy.Failures = append(policy.Failures, failure)
				return
			}
			if !strings.Contains(detail, "status=sent") {
				return
			}
			session, ok := sessions[pid]
			if ok && session.relay == result[3] && at.Sub(session.at) >= 0 &&
				at.Sub(session.at) <= 5*time.Minute && !session.counted[domain] {
				session.counted[domain] = true
				tlsPolicy(policies, domain, session.policy).Summary.Successful++
			}
		})
		if err != nil {
			return nil, err
		}
	}
	return flattenTLSPolicies(policies), nil
}

func scanPostfixLog(path string, consume func(string)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var source io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		reader, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer reader.Close()
		source = reader
	}
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		consume(scanner.Text())
	}
	return scanner.Err()
}

func tlsPolicy(policies map[string]map[string]*tlsReportPolicy, domain, kind string) *tlsReportPolicy {
	if policies[domain] == nil {
		policies[domain] = map[string]*tlsReportPolicy{}
	}
	if policies[domain][kind] == nil {
		policy := &tlsReportPolicy{}
		policy.Policy.Domain = domain
		policy.Policy.Type = kind
		policies[domain][kind] = policy
	}
	return policies[domain][kind]
}

func flattenTLSPolicies(policies map[string]map[string]*tlsReportPolicy) map[string][]tlsReportPolicy {
	out := make(map[string][]tlsReportPolicy, len(policies))
	for domain, kinds := range policies {
		names := make([]string, 0, len(kinds))
		for name := range kinds {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			out[domain] = append(out[domain], *kinds[name])
		}
	}
	return out
}

func (s *Server) sendTLSReports(ctx context.Context, rep reporter, day time.Time, https *http.Client) {
	if s.cfg.MailLog == "" || s.cfg.ReportRelayHost != "" {
		return
	}
	policies, err := readTLSReportLog(s.cfg.MailLog, rep.from, day)
	if err != nil {
		s.log.Warn("reading Postfix TLS report data failed", "err", err)
		return
	}
	for domain, domainPolicies := range policies {
		tags, err := s.reportTags(ctx, "_smtp._tls."+domain, "TLSRPTv1")
		if err != nil {
			s.log.Warn("TLS report policy lookup failed", "domain", domain, "err", err)
			continue
		}
		if tags["rua"] == "" {
			continue
		}
		end := day.AddDate(0, 0, 1).Add(-time.Second)
		report := tlsReport{Organization: rep.org, Contact: rep.from, Policies: domainPolicies}
		report.ID = domain + "." + day.Format("20060102") + "@" + rep.submitter
		report.DateRange.Start, report.DateRange.End = day, end
		body, err := json.Marshal(report)
		if err != nil {
			s.log.Warn("encoding TLS report failed", "domain", domain, "err", err)
			continue
		}
		message := reportMessage{
			subject:    fmt.Sprintf("Report Domain: %s Submitter: %s Report-ID: <%s>", domain, rep.submitter, report.ID),
			headers:    [][2]string{{"TLS-Report-Domain", domain}, {"TLS-Report-Submitter", rep.submitter}},
			reportType: "tlsrpt", text: "This message contains an SMTP TLS aggregate report.",
			filename:    fmt.Sprintf("%s!%s!%d!%d.json.gz", rep.submitter, domain, day.Unix(), end.Unix()),
			contentType: "application/tlsrpt+gzip", data: gzipBytes(body),
		}
		for _, uri := range strings.Split(tags["rua"], ",") {
			if dest := tlsReportDestination(strings.TrimSpace(uri)); dest != "" {
				s.deliverReport(ctx, rep, "tls", domain, day, dest, message, https)
			}
		}
	}
}

func tlsReportDestination(uri string) string {
	if addr, ok := mailtoAddress(uri); ok {
		return addr
	}
	dest, err := url.Parse(uri)
	if err != nil || dest.Scheme != "https" || dest.User != nil || dest.Fragment != "" ||
		!domainPattern.MatchString(strings.ToLower(dest.Hostname())) || cmp.Or(dest.Port(), "443") != "443" {
		return ""
	}
	return dest.String()
}
