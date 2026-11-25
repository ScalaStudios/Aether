package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strconv"
	"time"
)

type OTLPSink struct {
	endpoint    string
	headers     map[string]string
	serviceName string
	client      *http.Client
}

func NewOTLPSink(endpoint string, headers map[string]string, serviceName string) *OTLPSink {
	if serviceName == "" {
		serviceName = "aether-api"
	}
	var hdr map[string]string
	if len(headers) > 0 {
		hdr = maps.Clone(headers)
	}
	return &OTLPSink{
		endpoint:    endpoint,
		headers:     hdr,
		serviceName: serviceName,
		client:      &http.Client{Timeout: 10 * time.Second},
	}
}

type otlpExportLogsRequest struct {
	ResourceLogs []otlpResourceLogs `json:"resourceLogs"`
}

type otlpResourceLogs struct {
	Resource  otlpResource    `json:"resource"`
	ScopeLogs []otlpScopeLogs `json:"scopeLogs"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

type otlpScopeLogs struct {
	Scope      otlpScope       `json:"scope"`
	LogRecords []otlpLogRecord `json:"logRecords"`
}

type otlpScope struct {
	Name string `json:"name"`
}

type otlpLogRecord struct {
	TimeUnixNano         string         `json:"timeUnixNano"`
	ObservedTimeUnixNano string         `json:"observedTimeUnixNano"`
	SeverityNumber       int            `json:"severityNumber"`
	SeverityText         string         `json:"severityText"`
	Body                 otlpAnyValue   `json:"body"`
	Attributes           []otlpKeyValue `json:"attributes"`
	TraceID              string         `json:"traceId,omitempty"`
	SpanID               string         `json:"spanId,omitempty"`
}

type otlpKeyValue struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpAnyValue struct {
	StringValue *string `json:"stringValue,omitempty"`
	IntValue    *string `json:"intValue,omitempty"`
}

func otlpString(key, val string) otlpKeyValue {
	return otlpKeyValue{Key: key, Value: otlpAnyValue{StringValue: &val}}
}

func otlpInt(key string, val int64) otlpKeyValue {
	s := strconv.FormatInt(val, 10)
	return otlpKeyValue{Key: key, Value: otlpAnyValue{IntValue: &s}}
}

func (s *OTLPSink) FormatOTLP(ev Event) ([]byte, error) {
	sevNum := 9
	sevText := "INFO"
	if isWarningAction(ev.Action) {
		sevNum = 13
		sevText = "WARN"
	}
	tsNano := strconv.FormatInt(ev.CreatedAt.UTC().UnixNano(), 10)
	attrs := []otlpKeyValue{
		otlpInt("audit.id", ev.ID),
		otlpString("audit.action", ev.Action),
	}
	if ev.OrgID != nil && *ev.OrgID != "" {
		attrs = append(attrs, otlpString("audit.org_id", *ev.OrgID))
	}
	if ev.ActorUserID != nil && *ev.ActorUserID != "" {
		attrs = append(attrs, otlpString("audit.actor.user_id", *ev.ActorUserID))
	}
	if ev.ActorEmail != "" {
		attrs = append(attrs, otlpString("audit.actor.email", ev.ActorEmail))
	}
	if ev.TargetType != "" {
		attrs = append(attrs, otlpString("audit.target.type", ev.TargetType))
	}
	if ev.TargetID != "" {
		attrs = append(attrs, otlpString("audit.target.id", ev.TargetID))
	}
	if ev.IP != "" {
		attrs = append(attrs, otlpString("client.address", ev.IP))
	}
	if len(ev.Metadata) > 0 {
		if id, ok := ev.Metadata["apiKeyId"].(string); ok && id != "" {
			attrs = append(attrs, otlpString("audit.api_key.id", id))
		}
		if name, ok := ev.Metadata["apiKeyName"].(string); ok && name != "" {
			attrs = append(attrs, otlpString("audit.api_key.name", name))
		}
		if raw, err := json.Marshal(ev.Metadata); err == nil {
			attrs = append(attrs, otlpString("audit.metadata", string(raw)))
		}
	}
	bodyVal := ev.Action
	req := otlpExportLogsRequest{
		ResourceLogs: []otlpResourceLogs{
			{
				Resource: otlpResource{
					Attributes: []otlpKeyValue{
						otlpString("service.name", s.serviceName),
					},
				},
				ScopeLogs: []otlpScopeLogs{
					{
						Scope: otlpScope{Name: "aether.audit"},
						LogRecords: []otlpLogRecord{
							{
								TimeUnixNano:         tsNano,
								ObservedTimeUnixNano: tsNano,
								SeverityNumber:       sevNum,
								SeverityText:         sevText,
								Body:                 otlpAnyValue{StringValue: &bodyVal},
								Attributes:           attrs,
								TraceID:              ev.TraceID,
								SpanID:               ev.SpanID,
							},
						},
					},
				},
			},
		},
	}
	return json.Marshal(req)
}

func (s *OTLPSink) Send(ctx context.Context, ev Event) error {
	body, err := s.FormatOTLP(ev)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("otlp logs endpoint returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (s *OTLPSink) Close() error {
	s.client.CloseIdleConnections()
	return nil
}
