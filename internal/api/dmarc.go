package api

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
	"github.com/jackc/pgx/v5"
)

const (
	maxReportMessage = 20 << 20
	maxReportXML     = 50 << 20
	maxReportRecords = 10000
)

type dmarcFeedback struct {
	Metadata struct {
		OrgName   string `xml:"org_name"`
		ReportID  string `xml:"report_id"`
		DateRange struct {
			Begin int64 `xml:"begin"`
			End   int64 `xml:"end"`
		} `xml:"date_range"`
	} `xml:"report_metadata"`
	Policy struct {
		Domain string `xml:"domain"`
		P      string `xml:"p"`
	} `xml:"policy_published"`
	Records []struct {
		Row struct {
			SourceIP  string `xml:"source_ip"`
			Count     int    `xml:"count"`
			Evaluated struct {
				Disposition string `xml:"disposition"`
				DKIM        string `xml:"dkim"`
				SPF         string `xml:"spf"`
			} `xml:"policy_evaluated"`
		} `xml:"row"`
		Identifiers struct {
			HeaderFrom string `xml:"header_from"`
		} `xml:"identifiers"`
	} `xml:"record"`
}

func parseDMARCMessage(r io.Reader) ([]*dmarcFeedback, error) {
	mr, err := mail.CreateReader(r)
	if err != nil {
		return nil, err
	}
	var out []*dmarcFeedback
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return out, err
		}
		body, err := io.ReadAll(io.LimitReader(part.Body, maxReportMessage))
		if err != nil {
			return out, err
		}
		if fb, err := decodeDMARCReport(body); err == nil {
			out = append(out, fb)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no DMARC aggregate report found in the message")
	}
	return out, nil
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
	var fb dmarcFeedback
	if err := xml.NewDecoder(io.LimitReader(xmlData, maxReportXML)).Decode(&fb); err != nil {
		return nil, err
	}
	if fb.Metadata.ReportID == "" || fb.Policy.Domain == "" {
		return nil, errors.New("report is missing its ID or domain")
	}
	return &fb, nil
}

func (s *Server) storeDMARCReport(ctx context.Context, fb *dmarcFeedback) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var reportID string
		err := tx.QueryRow(ctx, `
			INSERT INTO dmarc_reports (domain_id, reporter, report_id, begins_at, ends_at, policy)
			SELECT id, $2, $3, to_timestamp($4), to_timestamp($5), $6 FROM domains WHERE name = $1 AND verified_at IS NOT NULL
			ON CONFLICT (domain_id, reporter, report_id) DO NOTHING RETURNING id`,
			strings.ToLower(strings.TrimSuffix(fb.Policy.Domain, ".")), truncate(fb.Metadata.OrgName, 200), truncate(fb.Metadata.ReportID, 200),
			fb.Metadata.DateRange.Begin, fb.Metadata.DateRange.End, truncate(fb.Policy.P, 20)).Scan(&reportID)
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
			rows = append(rows, []any{reportID, rec.Row.SourceIP, rec.Row.Count, truncate(rec.Row.Evaluated.Disposition, 20),
				rec.Row.Evaluated.DKIM == "pass", rec.Row.Evaluated.SPF == "pass", truncate(rec.Identifiers.HeaderFrom, 255)})
		}
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"dmarc_records"},
			[]string{"report_id", "source_ip", "count", "disposition", "dkim_aligned", "spf_aligned", "header_from"}, pgx.CopyFromRows(rows))
		return err
	})
}

type reportBackend struct{ s *Server }

func (b reportBackend) NewSession(*smtp.Conn) (smtp.Session, error) {
	return &reportSession{s: b.s}, nil
}

type reportSession struct{ s *Server }

func (r *reportSession) AuthMechanisms() []string { return nil }
func (r *reportSession) Auth(string) (sasl.Server, error) {
	return nil, smtp.ErrAuthUnsupported
}
func (r *reportSession) Mail(string, *smtp.MailOptions) error { return nil }
func (r *reportSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	if !strings.HasSuffix(strings.ToLower(to), "@aether-reports.internal") {
		return &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "Unknown recipient"}
	}
	return nil
}
func (r *reportSession) Reset()        {}
func (r *reportSession) Logout() error { return nil }

func (r *reportSession) Data(body io.Reader) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reports, err := parseDMARCMessage(io.LimitReader(body, maxReportMessage))
	if err != nil {
		r.s.log.Info("ignored a message sent to the DMARC report address", "err", err)
		io.Copy(io.Discard, body)
		return nil
	}
	for _, fb := range reports {
		if err := r.s.storeDMARCReport(ctx, fb); err != nil {
			r.s.log.Error("storing DMARC report failed", "reporter", fb.Metadata.OrgName, "err", err)
			return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "Try again later"}
		}
	}
	return nil
}

func (s *Server) ReportServer(addr string) *smtp.Server {
	srv := smtp.NewServer(reportBackend{s})
	srv.Addr = addr
	srv.LMTP = true
	srv.Network = "tcp"
	srv.Domain = s.cfg.MailHostname
	srv.MaxMessageBytes = maxReportMessage
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
	d, ok := s.domainFromPath(w, r, p)
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
