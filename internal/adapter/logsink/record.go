// Package logsink ships request-log entries to external systems: JSON lines on
// stdout for a log collector (Filebeat, Fluent Bit, Vector, Promtail), an
// HTTP endpoint that takes newline-delimited JSON (Logstash, Vector), and the
// Elasticsearch/OpenSearch bulk API.
package logsink

import (
	"fmt"
	"net/http"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// ServiceName is the value of service.name in every record.
const ServiceName = "openapi-mocker"

// Record is the JSON form of one log entry. The field names follow the
// Elastic Common Schema where one exists, so dashboards built for ECS data
// work, and anything else lives under "mocker.".
type Record struct {
	Timestamp   time.Time           `json:"@timestamp"`
	Message     string              `json:"message"`
	Level       string              `json:"log.level"`
	Service     string              `json:"service.name"`
	Dataset     string              `json:"event.dataset"`
	DurationNs  int64               `json:"event.duration"`
	Method      string              `json:"http.request.method"`
	Path        string              `json:"url.path"`
	Status      int                 `json:"http.response.status_code"`
	LogID       string              `json:"mocker.log_id"`
	ProjectID   string              `json:"mocker.project_id"`
	Endpoint    string              `json:"mocker.endpoint,omitempty"`
	Matched     bool                `json:"mocker.matched"`
	MatchedRule string              `json:"mocker.matched_rule,omitempty"`
	Source      string              `json:"mocker.source,omitempty"`
	Fault       string              `json:"mocker.fault,omitempty"`
	Violations  []string            `json:"mocker.violations,omitempty"`
	ReqHeaders  map[string][]string `json:"http.request.headers,omitempty"`
	ReqBody     *string             `json:"http.request.body.content,omitempty"`
	ReqTrunc    bool                `json:"http.request.body.truncated,omitempty"`
	RespHeaders map[string][]string `json:"http.response.headers,omitempty"`
	RespBody    *string             `json:"http.response.body.content,omitempty"`
	RespTrunc   bool                `json:"http.response.body.truncated,omitempty"`
}

// ToRecord converts an entry; request and response parts appear only when the
// entry carries them.
func ToRecord(l *domain.RequestLog) Record {
	r := Record{
		Timestamp: l.Timestamp.UTC(), Message: fmt.Sprintf("%s %s -> %d", l.Method, l.Path, l.StatusCode),
		Level: levelFor(l.StatusCode), Service: ServiceName, Dataset: ServiceName + ".requests",
		DurationNs: l.DurationMs * int64(time.Millisecond), Method: l.Method, Path: l.Path, Status: l.StatusCode,
		LogID: l.ID, ProjectID: l.ProjectID, Endpoint: l.Endpoint, Matched: l.Matched, MatchedRule: l.MatchedRule,
		Source: l.Source, Fault: l.Fault, Violations: l.Violations,
		ReqHeaders: nil, ReqBody: nil, ReqTrunc: false, RespHeaders: nil, RespBody: nil, RespTrunc: false,
	}

	if c := l.Request; c != nil {
		r.ReqHeaders, r.ReqBody, r.ReqTrunc = c.Headers, bodyOf(c), c.Truncated
	}

	if c := l.Response; c != nil {
		r.RespHeaders, r.RespBody, r.RespTrunc = c.Headers, bodyOf(c), c.Truncated
	}

	return r
}

func bodyOf(c *domain.HTTPCapture) *string {
	if c.Binary || c.Body == "" {
		return nil
	}

	return &c.Body
}

func levelFor(status int) string {
	switch {
	case status >= http.StatusInternalServerError:
		return "error"
	case status >= http.StatusBadRequest:
		return "warn"
	default:
		return "info"
	}
}
