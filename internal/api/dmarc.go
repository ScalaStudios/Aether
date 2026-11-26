package api

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
	"github.com/jackc/pgx/v5"
)

const (
	maxReportMessage     = 20 << 20
	maxReportXML         = 50 << 20
	maxReportRecords     = 10000
	maxRecordAuthResults = 20
	maxReportXMLRun      = 64 << 10
	maxReportXMLDepth    = 64
	maxReportParts       = 10
	maxReportTime        = 253402300799
)

type dmarcFeedback struct {
	XMLName  xml.Name `xml:"feedback"`
	Version  string   `xml:"version,omitempty"`
	Metadata struct {
		OrgName   string `xml:"org_name"`
		Email     string `xml:"email,omitempty"`
		ReportID  string `xml:"report_id"`
		DateRange struct {
			Begin int64 `xml:"begin"`
			End   int64 `xml:"end"`
		} `xml:"date_range"`
	} `xml:"report_metadata"`
	Policy struct {
		Domain string `xml:"domain"`
		ADKIM  string `xml:"adkim,omitempty"`
		ASPF   string `xml:"aspf,omitempty"`
		P      string `xml:"p"`
		SP     string `xml:"sp,omitempty"`
		Pct    string `xml:"pct,omitempty"`
	} `xml:"policy_published"`
	Records []dmarcRecord `xml:"record"`
}

type dmarcRecord struct {
	Row struct {
		SourceIP  string `xml:"source_ip"`
		Count     int    `xml:"count"`
		Evaluated struct {
			Disposition string       `xml:"disposition"`
			DKIM        string       `xml:"dkim"`
			SPF         string       `xml:"spf"`
			Reason      *dmarcReason `xml:"reason,omitempty"`
		} `xml:"policy_evaluated"`
	} `xml:"row"`
	Identifiers struct {
		HeaderFrom string `xml:"header_from"`
	} `xml:"identifiers"`
	Auth struct {
		DKIM dmarcAuthList `xml:"dkim"`
		SPF  dmarcAuthList `xml:"spf"`
	} `xml:"auth_results"`
}

type dmarcReason struct {
	Type string `xml:"type,omitempty"`
}

type dmarcAuth struct {
	Domain string `xml:"domain"`
	Result string `xml:"result"`
}

type dmarcAuthList []dmarcAuth

func (l *dmarcAuthList) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if len(*l) == maxRecordAuthResults {
		return errors.New("record has too many authentication results")
	}
	var a dmarcAuth
	if err := d.DecodeElement(&a, &start); err != nil {
		return err
	}
	*l = append(*l, a)
	return nil
}

type xmlShapeReader struct {
	r     io.Reader
	run   int
	depth int
	open  bool
	prev  byte
}

func (x *xmlShapeReader) Read(p []byte) (int, error) {
	n, err := x.r.Read(p)
	for _, c := range p[:n] {
		x.run++
		switch {
		case x.open:
			x.open = false
			if c == '/' {
				x.depth--
			} else if c != '!' && c != '?' {
				x.depth++
			}
		case c == '<':
			x.open = true
		case c == '>':
			x.run = 0
			if x.prev == '/' {
				x.depth--
			}
		}
		x.prev = c
		if x.run > maxReportXMLRun || x.depth > maxReportXMLDepth {
			return 0, errors.New("report XML has an oversized tag or text, or is nested too deeply")
		}
	}
	return n, err
}

var reportSlots = make(chan struct{}, 2)

var reportSlotWait = 30 * time.Second

func parseDMARCMessage(r io.Reader, store func(*dmarcFeedback) error) error {
	mr, err := mail.CreateReader(r)
	if err != nil {
		return err
	}
	found := false
	for range maxReportParts {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		body, err := io.ReadAll(io.LimitReader(part.Body, maxReportMessage))
		if err != nil {
			return err
		}
		if fb, err := decodeDMARCReport(body); err == nil {
			found = true
			if err := store(fb); err != nil {
				return err
			}
		}
	}
	if !found {
		return errors.New("no DMARC aggregate report found in the message")
	}
	return nil
}

func decodeDMARCReport(body []byte) (*dmarcFeedback, error) {
	var xmlData io.Reader
	switch {
	case bytes.HasPrefix(body, []byte{0x1f, 0x8b}):
		gz, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		xmlData = gz
	case bytes.HasPrefix(body, []byte("PK")):
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if strings.HasSuffix(strings.ToLower(f.Name), ".xml") {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				xmlData = rc
				break
			}
		}
		if xmlData == nil {
			return nil, errors.New("zip has no XML file")
		}
	default:
		trimmed := bytes.TrimSpace(body)
		if !bytes.HasPrefix(trimmed, []byte("<?xml")) && !bytes.HasPrefix(trimmed, []byte("<feedback")) {
			return nil, errors.New("not a report")
		}
		xmlData = bytes.NewReader(trimmed)
	}
	dec := xml.NewDecoder(&xmlShapeReader{r: io.LimitReader(xmlData, maxReportXML)})
	var fb dmarcFeedback
	root := false
	for len(fb.Records) < maxReportRecords {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) && root {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if !root {
			if start.Name.Local != "feedback" {
				return nil, errors.New("not a report")
			}
			root = true
			continue
		}
		var v any
		switch start.Name.Local {
		case "version":
			v = &fb.Version
		case "report_metadata":
			v = &fb.Metadata
		case "policy_published":
			v = &fb.Policy
		case "record":
			fb.Records = append(fb.Records, dmarcRecord{})
			v = &fb.Records[len(fb.Records)-1]
		default:
			err = dec.Skip()
		}
		if v != nil {
			err = dec.DecodeElement(v, &start)
		}
		if err != nil {
			return nil, err
		}
	}
	if fb.Metadata.ReportID == "" || fb.Policy.Domain == "" {
		return nil, errors.New("report is missing its ID or domain")
	}
	return &fb, nil
}

func (s *Server) storeDMARCReport(ctx context.Context, fb *dmarcFeedback, recipientDomainIDs []string) error {
	if begin, end := fb.Metadata.DateRange.Begin, fb.Metadata.DateRange.End; begin < 0 || end < begin || end > maxReportTime {
		s.log.Info("ignored a DMARC report with an invalid date range", "reporter", fb.Metadata.OrgName, "begin", begin, "end", end)
		return nil
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var reportID string
		err := tx.QueryRow(ctx, `
			INSERT INTO dmarc_reports (domain_id, reporter, report_id, begins_at, ends_at, policy)
			SELECT id, $2, $3, to_timestamp($4), to_timestamp($5), $6 FROM domains WHERE name = $1 AND verified_at IS NOT NULL
			  AND org_id IN (SELECT org_id FROM domains WHERE id = ANY($7::uuid[]))
			ON CONFLICT (domain_id, reporter, report_id) DO NOTHING RETURNING id`,
			strings.ToLower(strings.TrimSuffix(fb.Policy.Domain, ".")), truncate(fb.Metadata.OrgName, 200), truncate(fb.Metadata.ReportID, 200),
			fb.Metadata.DateRange.Begin, fb.Metadata.DateRange.End, truncate(fb.Policy.P, 20), recipientDomainIDs).Scan(&reportID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var rows [][]any
		for i, rec := range fb.Records {
			if i == maxReportRecords {
				break
			}
			if !validDMARCRecord(rec) {
				continue
			}
			rows = append(rows, []any{reportID, rec.Row.SourceIP, rec.Row.Count, truncate(rec.Row.Evaluated.Disposition, 20),
				rec.Row.Evaluated.DKIM == "pass", rec.Row.Evaluated.SPF == "pass", truncate(rec.Identifiers.HeaderFrom, 255)})
		}
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"dmarc_records"},
			[]string{"report_id", "source_ip", "count", "disposition", "dkim_aligned", "spf_aligned", "header_from"}, pgx.CopyFromRows(rows))
		return err
	})
}

func validDMARCRecord(rec dmarcRecord) bool {
	ip, err := netip.ParseAddr(rec.Row.SourceIP)
	return err == nil && ip.Zone() == "" && rec.Row.Count >= 0 && rec.Row.Count <= math.MaxInt32
}

type reportBackend struct{ s *Server }

func (b reportBackend) NewSession(*smtp.Conn) (smtp.Session, error) {
	return &reportSession{s: b.s}, nil
}

type reportSession struct {
	s     *Server
	from  string
	rcpts []string
}

func (r *reportSession) AuthMechanisms() []string { return nil }
func (r *reportSession) Auth(string) (sasl.Server, error) {
	return nil, smtp.ErrAuthUnsupported
}
func (r *reportSession) Mail(from string, _ *smtp.MailOptions) error {
	r.from = from
	return nil
}
func (r *reportSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	if !strings.HasSuffix(strings.ToLower(to), "@aether-reports.internal") {
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "Unknown recipient"}
	}
	r.rcpts = append(r.rcpts, to)
	return nil
}
func (r *reportSession) Reset()        { r.from, r.rcpts = "", nil }
func (r *reportSession) Logout() error { return nil }

func (r *reportSession) LMTPData(body io.Reader, status smtp.StatusCollector) error {
	raw, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	var reportErr error
	reported := false
	for _, rcpt := range r.rcpts {
		if id, ok := listRecipient(rcpt); ok {
			status.SetStatus(rcpt, r.s.receiveListMessage(id, r.from, raw))
			continue
		}
		if !reported {
			reportErr, reported = r.Data(bytes.NewReader(raw)), true
		}
		status.SetStatus(rcpt, reportErr)
	}
	return nil
}

func reportRecipientDomain(rcpt string) (string, bool) {
	local, domain, _ := strings.Cut(strings.ToLower(rcpt), "@")
	id, ok := strings.CutPrefix(local, "reports+")
	id, _, _ = strings.Cut(id, "+")
	return id, ok && domain == "aether-reports.internal" && uuidPattern.MatchString(id)
}

func (r *reportSession) Data(body io.Reader) error {
	wait, cancelWait := context.WithTimeout(context.Background(), reportSlotWait)
	defer cancelWait()
	select {
	case reportSlots <- struct{}{}:
		defer func() { <-reportSlots }()
	case <-wait.Done():
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Try again later"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var domainIDs []string
	for _, rcpt := range r.rcpts {
		if id, ok := reportRecipientDomain(rcpt); ok {
			domainIDs = append(domainIDs, id)
		}
	}
	var storeErr error
	err := parseDMARCMessage(io.LimitReader(body, maxReportMessage), func(fb *dmarcFeedback) error {
		if storeErr = r.s.storeDMARCReport(ctx, fb, domainIDs); storeErr != nil {
			r.s.log.Error("storing DMARC report failed", "reporter", fb.Metadata.OrgName, "err", storeErr)
		}
		return storeErr
	})
	if storeErr != nil {
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Try again later"}
	}
	if err != nil {
		r.s.log.Info("ignored a message sent to the DMARC report address", "err", err)
		io.Copy(io.Discard, body)
	}
	return nil
}

func (s *Server) ReportServer(addr string) *smtp.Server {
	srv := smtp.NewServer(reportBackend{s})
	srv.Addr = addr
	srv.LMTP = true
	srv.Network = "tcp"
	srv.Domain = s.cfg.MailHostname
	srv.MaxMessageBytes = maxListMessage
	srv.ReadTimeout = time.Minute
	srv.WriteTimeout = time.Minute
	return srv
}

type dmarcSource struct {
	SourceIP    string   `json:"sourceIp"`
	Messages    int64    `json:"messages"`
	DKIMAligned int64    `json:"dkimAligned"`
	SPFAligned  int64    `json:"spfAligned"`
	Quarantined int64    `json:"quarantined"`
	Rejected    int64    `json:"rejected"`
	Reporters   []string `json:"reporters"`
	HeaderFroms []string `json:"headerFroms"`
}

func (s *Server) domainDMARC(w http.ResponseWriter, r *http.Request, p *principal) {
	d, ok := s.domainFromPath(w, r, p, permDomainsRead)
	if !ok {
		return
	}
	days, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || days < 1 || days > 366 {
		days = 30
	}
	var reports int64
	if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM dmarc_reports WHERE domain_id = $1 AND ends_at > now() - make_interval(days => $2)`, d.ID, days).Scan(&reports); err != nil {
		s.internalError(w, r, err)
		return
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT host(rec.source_ip), sum(rec.count),
		       coalesce(sum(rec.count) FILTER (WHERE rec.dkim_aligned), 0), coalesce(sum(rec.count) FILTER (WHERE rec.spf_aligned), 0),
		       coalesce(sum(rec.count) FILTER (WHERE rec.disposition = 'quarantine'), 0), coalesce(sum(rec.count) FILTER (WHERE rec.disposition = 'reject'), 0),
		       array_agg(DISTINCT rep.reporter), array_agg(DISTINCT rec.header_from)
		FROM dmarc_records rec JOIN dmarc_reports rep ON rep.id = rec.report_id
		WHERE rep.domain_id = $1 AND rep.ends_at > now() - make_interval(days => $2)
		GROUP BY rec.source_ip ORDER BY sum(rec.count) DESC LIMIT 200`, d.ID, days)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	sources, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (dmarcSource, error) {
		var src dmarcSource
		return src, row.Scan(&src.SourceIP, &src.Messages, &src.DKIMAligned, &src.SPFAligned, &src.Quarantined, &src.Rejected, &src.Reporters, &src.HeaderFroms)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days, "reports": reports, "sources": nonNil(sources)})
}
