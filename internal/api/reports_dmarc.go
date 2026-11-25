package api

import (
	"cmp"
	"context"
	"encoding/xml"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

func (s *Server) sendDMARCReports(ctx context.Context, rep reporter, day time.Time) {
	date := day.Format("20060102")
	keys, err := s.kv.SMembers(ctx, "dmarc_idx;"+date).Result()
	if err != nil {
		s.log.Warn("loading DMARC report index failed", "err", err)
		return
	}
	domains := make(map[string][]string)
	for _, key := range keys {
		if domain, ok := dmarcReportDomain(key, date); ok {
			domains[domain] = append(domains[domain], key)
		}
	}
	for domain, keys := range domains {
		if err := s.sendDMARCDomain(ctx, rep, day, domain, keys); err != nil {
			s.log.Warn("building DMARC report failed", "domain", domain, "err", err)
		}
	}
}

func dmarcReportDomain(key, date string) (string, bool) {
	parts := strings.Split(key, ";")
	if len(parts) < 4 || parts[0] != "dmarc_rpt" || parts[len(parts)-1] != date {
		return "", false
	}
	domain := strings.ToLower(parts[1])
	if !domainPattern.MatchString(domain) {
		return "", false
	}
	return domain, true
}

func publishedDMARC(tags map[string]string) (p, sp, adkim, aspf, pct string, ok bool) {
	if tags == nil {
		return
	}
	p = strings.ToLower(tags["p"])
	if p == "" {
		p = "none"
	}
	if p != "none" && p != "quarantine" && p != "reject" {
		return
	}
	sp = strings.ToLower(tags["sp"])
	if sp == "" {
		sp = p
	}
	if sp != "none" && sp != "quarantine" && sp != "reject" {
		return
	}
	adkim = strings.ToLower(cmp.Or(tags["adkim"], "r"))
	aspf = strings.ToLower(cmp.Or(tags["aspf"], "r"))
	if (adkim != "r" && adkim != "s") || (aspf != "r" && aspf != "s") {
		return
	}
	pct = tags["pct"]
	if pct == "" {
		pct = "100"
	}
	n, err := strconv.Atoi(pct)
	if err != nil || n < 0 || n > 100 {
		pct = "100"
	}
	return p, sp, adkim, aspf, pct, true
}

func (s *Server) sendDMARCDomain(ctx context.Context, rep reporter, day time.Time, domain string, keys []string) error {
	tags, err := s.reportTags(ctx, "_dmarc."+domain, "DMARC1")
	if err != nil {
		return err
	}
	p, sp, adkim, aspf, pct, ok := publishedDMARC(tags)
	if !ok || tags["rua"] == "" {
		return nil
	}
	var feedback dmarcFeedback
	feedback.Version = "1.0"
	feedback.Policy.Domain = domain
	feedback.Policy.P = p
	feedback.Policy.SP = sp
	feedback.Policy.ADKIM = adkim
	feedback.Policy.ASPF = aspf
	feedback.Policy.Pct = pct
	counts := map[string]int{}
	for _, key := range keys {
		rows, err := s.kv.ZRangeWithScores(ctx, key, 0, -1).Result()
		if err != nil {
			return err
		}
		for _, row := range rows {
			if data, ok := row.Member.(string); ok {
				counts[data] += int(row.Score)
			}
		}
	}
	for data, count := range counts {
		if record, ok := rspamdDMARCRecord(data, count); ok {
			feedback.Records = append(feedback.Records, record)
		}
	}
	if len(feedback.Records) == 0 {
		return nil
	}
	slices.SortFunc(feedback.Records, func(a, b dmarcRecord) int {
		return cmp.Or(cmp.Compare(a.Row.SourceIP, b.Row.SourceIP), cmp.Compare(a.Identifiers.HeaderFrom, b.Identifiers.HeaderFrom), cmp.Compare(a.Row.Count, b.Row.Count))
	})
	end := day.AddDate(0, 0, 1)
	id := domain + "." + day.Format("20060102") + "@" + rep.submitter
	feedback.Metadata.OrgName = rep.org
	feedback.Metadata.Email = rep.from
	feedback.Metadata.ReportID = id
	feedback.Metadata.DateRange.Begin = day.Unix()
	feedback.Metadata.DateRange.End = end.Unix() - 1
	payload, err := xml.Marshal(feedback)
	if err != nil {
		return err
	}
	body := append([]byte(xml.Header), payload...)
	message := reportMessage{
		subject:     fmt.Sprintf("Report Domain: %s Submitter: %s Report-ID: <%s>", domain, rep.submitter, id),
		text:        "This message contains a DMARC aggregate report.",
		filename:    fmt.Sprintf("%s!%s!%d!%d.xml.gz", rep.submitter, domain, day.Unix(), end.Unix()-1),
		contentType: "application/gzip", data: gzipBytes(body),
	}
	for _, uri := range strings.Split(tags["rua"], ",") {
		dest, limit, err := s.dmarcDestination(ctx, domain, strings.TrimSpace(uri))
		if err != nil {
			s.log.Warn("DMARC report destination lookup failed", "domain", domain, "err", err)
			continue
		}
		if dest == "" {
			continue
		}
		if limit != 0 && int64(len(message.data)) > limit {
			s.log.Warn("DMARC report exceeds the published size limit", "domain", domain, "destination", dest, "bytes", len(message.data), "limit", limit)
			continue
		}
		s.deliverReport(ctx, rep, "dmarc", domain, day, dest, message, nil)
	}
	return nil
}

func rspamdDMARCRecord(data string, count int) (dmarcRecord, bool) {
	var record dmarcRecord
	fields := strings.Split(data, ",")
	if len(fields) != 12 || net.ParseIP(fields[0]) == nil || count <= 0 || !domainPattern.MatchString(fields[5]) {
		return record, false
	}
	if (fields[1] != "pass" && fields[1] != "fail") || (fields[2] != "pass" && fields[2] != "fail") {
		return record, false
	}
	switch fields[3] {
	case "none", "quarantine", "reject":
	default:
		return record, false
	}
	switch fields[4] {
	case "", "forwarded", "sampled_out", "trusted_forwarder", "mailing_list", "local_policy", "other":
	default:
		return record, false
	}
	record.Row.SourceIP = fields[0]
	record.Row.Count = count
	record.Row.Evaluated.SPF = fields[1]
	record.Row.Evaluated.DKIM = fields[2]
	record.Row.Evaluated.Disposition = fields[3]
	if fields[4] != "" {
		record.Row.Evaluated.Reason = &dmarcReason{Type: fields[4]}
	}
	record.Identifiers.HeaderFrom = fields[5]
	for i, result := range []string{"pass", "fail", "temperror", "permerror"} {
		for _, domain := range strings.Split(fields[i+6], "|") {
			if domainPattern.MatchString(domain) {
				record.Auth.DKIM = append(record.Auth.DKIM, dmarcAuth{domain, result})
			}
		}
	}
	switch fields[11] {
	case "none", "pass", "fail", "softfail", "neutral", "temperror", "permerror":
		if fields[10] != "" && domainPattern.MatchString(fields[10]) {
			record.Auth.SPF = []dmarcAuth{{fields[10], fields[11]}}
		}
	}
	return record, true
}

func sameReportDomain(policyDomain, host string) bool {
	if policyDomain == host {
		return true
	}
	left, leftErr := publicsuffix.EffectiveTLDPlusOne(policyDomain)
	right, rightErr := publicsuffix.EffectiveTLDPlusOne(host)
	return leftErr == nil && rightErr == nil && left == right
}

func dmarcMailto(uri string) (string, int64) {
	var limit int64
	if address, size, ok := strings.Cut(uri, "!"); ok {
		power := 0
		size = strings.ToLower(size)
		if len(size) > 0 {
			power = strings.IndexByte("kmgt", size[len(size)-1]) + 1
			if power > 0 {
				size = size[:len(size)-1]
			}
		}
		n, err := strconv.ParseInt(size, 10, 64)
		if err != nil || n < 0 || n > (1<<63-1)>>(power*10) {
			return "", 0
		}
		limit, uri = n<<(power*10), address
		if limit == 0 {
			return "", 0
		}
	}
	address, _ := mailtoAddress(uri)
	return address, limit
}

func (s *Server) dmarcDestination(ctx context.Context, domain, uri string) (string, int64, error) {
	address, limit := dmarcMailto(uri)
	host, _ := addressDomain(address)
	if address == "" {
		return "", 0, nil
	}
	if sameReportDomain(domain, host) {
		return address, limit, nil
	}
	tags, err := s.reportTags(ctx, domain+"._report._dmarc."+host, "DMARC1")
	if err != nil || tags == nil || tags["rua"] == "" {
		return "", 0, err
	}
	var matched string
	var maxSize int64
	for _, part := range strings.Split(tags["rua"], ",") {
		candidate, size := dmarcMailto(strings.TrimSpace(part))
		candidateHost, _ := addressDomain(candidate)
		if candidate != "" && candidateHost == host && strings.EqualFold(candidate, address) {
			matched, maxSize = candidate, size
			break
		}
	}
	if matched == "" {
		return "", 0, nil
	}
	if maxSize != 0 && (limit == 0 || maxSize < limit) {
		limit = maxSize
	}
	return matched, limit, nil
}
