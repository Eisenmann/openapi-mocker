package logsink_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/logsink"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

func sample(withBodies bool) *domain.RequestLog {
	l := &domain.RequestLog{
		ID: "log1", ProjectID: "p1", Method: "POST", Path: "/users/7", StatusCode: 404,
		Endpoint: "/users/{id}", Matched: true, MatchedRule: "rule-a", Source: "rule", Fault: "error",
		Violations: []string{"v"}, DurationMs: 12, Timestamp: time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC),
	}

	if withBodies {
		l.Request = &domain.HTTPCapture{Headers: map[string][]string{"X": {"1"}}, Body: `{"a":1}`, Size: 7}
		l.Response = &domain.HTTPCapture{Body: "oops", Size: 4, Truncated: true}
	}

	return l
}

func decodeLines(t *testing.T, raw []byte) []map[string]any {
	t.Helper()

	var out []map[string]any

	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		var m map[string]any

		err := json.Unmarshal(sc.Bytes(), &m)
		if err != nil {
			t.Fatalf("bad JSON line %q: %v", sc.Text(), err)
		}

		out = append(out, m)
	}

	return out
}

func TestToRecord_FieldsAndLevels(t *testing.T) {
	t.Parallel()

	r := logsink.ToRecord(sample(true))
	if r.Message != "POST /users/7 -> 404" || r.Level != "warn" || r.DurationNs != 12_000_000 ||
		r.Service != "openapi-mocker" || r.ProjectID != "p1" || r.Endpoint != "/users/{id}" {
		t.Errorf("record = %+v", r)
	}

	if r.ReqBody == nil || *r.ReqBody != `{"a":1}` || r.RespBody == nil || !r.RespTrunc {
		t.Errorf("bodies = %+v", r)
	}

	for status, want := range map[int]string{200: "info", 399: "info", 400: "warn", 500: "error"} {
		l := sample(false)
		l.StatusCode = status

		if got := logsink.ToRecord(l).Level; got != want {
			t.Errorf("status %d: level %q, want %q", status, got, want)
		}
	}

	bin := sample(false)
	bin.Request = &domain.HTTPCapture{Binary: true, Size: 3}

	if logsink.ToRecord(bin).ReqBody != nil {
		t.Error("binary bodies must be left out")
	}
}

func TestStdout_WritesOneJSONLinePerEntry(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	s := logsink.NewStdout(&buf)

	err := s.Send(context.Background(), []*domain.RequestLog{sample(false), sample(true)})
	if err != nil {
		t.Fatal(err)
	}

	lines := decodeLines(t, buf.Bytes())
	if len(lines) != 2 || s.Name() != "stdout" {
		t.Fatalf("lines = %d", len(lines))
	}

	if strings.Contains(buf.String(), `\u003e`) {
		t.Error("the message arrow must not be HTML-escaped")
	}

	first := lines[0]
	if first["@timestamp"] != "2026-11-07T10:00:00Z" || first["http.response.status_code"] != float64(404) ||
		first["mocker.project_id"] != "p1" {
		t.Errorf("first = %v", first)
	}

	if _, has := first["http.request.body.content"]; has {
		t.Error("an entry without bodies must not have body fields")
	}

	if lines[1]["http.request.body.content"] != `{"a":1}` {
		t.Errorf("second = %v", lines[1])
	}
}

func TestHTTP_PostsNDJSONWithBearer(t *testing.T) {
	t.Parallel()

	var gotAuth, gotType string

	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotType = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	s, err := logsink.NewHTTP(srv.URL, "tok", srv.Client())
	if err != nil {
		t.Fatal(err)
	}

	err = s.Send(context.Background(), []*domain.RequestLog{sample(false), sample(false)})
	if err != nil {
		t.Fatal(err)
	}

	if gotAuth != "Bearer tok" || gotType != "application/x-ndjson" || len(decodeLines(t, gotBody)) != 2 {
		t.Errorf("auth=%q type=%q body=%q", gotAuth, gotType, gotBody)
	}
}

func TestHTTP_ErrorsOnBadStatusAndMissingURL(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer srv.Close()

	s, _ := logsink.NewHTTP(srv.URL, "", nil)

	err := s.Send(context.Background(), []*domain.RequestLog{sample(false)})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("err = %v", err)
	}

	if _, err = logsink.NewHTTP("", "", nil); err == nil {
		t.Error("an empty URL must be rejected")
	}

	if _, err = logsink.NewElasticsearch(&logsink.ESConfig{}); err == nil {
		t.Error("an empty URL must be rejected")
	}
}

func TestElasticsearch_BulkRequest(t *testing.T) {
	t.Parallel()

	var path, auth string

	var body []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"errors":false,"items":[]}`))
	}))
	defer srv.Close()

	s, err := logsink.NewElasticsearch(&logsink.ESConfig{
		URL: srv.URL + "/", Index: "mocker-{date}", APIKey: "key123", Username: "ignored", Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	err = s.Send(context.Background(), []*domain.RequestLog{sample(false)})
	if err != nil {
		t.Fatal(err)
	}

	lines := decodeLines(t, body)
	if path != "/_bulk" || auth != "ApiKey key123" || len(lines) != 2 {
		t.Fatalf("path=%q auth=%q lines=%d", path, auth, len(lines))
	}

	action := lines[0]["create"].(map[string]any)
	if action["_index"] != "mocker-2026.11.07" || action["_id"] != "log1" {
		t.Errorf("action = %v", action)
	}

	if lines[1]["mocker.project_id"] != "p1" || s.Name() != "elasticsearch" {
		t.Errorf("document = %v", lines[1])
	}
}

func TestElasticsearch_BasicAuthAndDefaultIndex(t *testing.T) {
	t.Parallel()

	var user, pass string

	var body []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ = r.BasicAuth()
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"errors":false}`))
	}))
	defer srv.Close()

	s, _ := logsink.NewElasticsearch(&logsink.ESConfig{URL: srv.URL, Username: "u", Password: "p", Client: srv.Client()})

	err := s.Send(context.Background(), []*domain.RequestLog{sample(false)})
	if err != nil || user != "u" || pass != "p" {
		t.Fatalf("err=%v user=%q pass=%q", err, user, pass)
	}

	if !strings.Contains(string(body), `"_index":"openapi-mocker-logs"`) {
		t.Errorf("default index missing: %s", body)
	}
}

func TestElasticsearch_ItemErrors(t *testing.T) {
	t.Parallel()

	respond := func(payload string, status int) *logsink.Elasticsearch {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(payload))
		}))
		t.Cleanup(srv.Close)

		s, _ := logsink.NewElasticsearch(&logsink.ESConfig{URL: srv.URL, Client: srv.Client()})

		return s
	}

	batch := []*domain.RequestLog{sample(false)}

	// A duplicate (409) is a retry of something already stored: success.
	dup := `{"errors":true,"items":[{"create":{"status":409,"error":{"type":"version_conflict","reason":"x"}}}]}`
	if err := respond(dup, 200).Send(context.Background(), batch); err != nil {
		t.Errorf("409 must be accepted: %v", err)
	}

	bad := `{"errors":true,"items":[{"create":{"status":400,"error":{"type":"mapper_parsing_exception","reason":"boom"}}}]}`

	err := respond(bad, 200).Send(context.Background(), batch)
	if err == nil || !strings.Contains(err.Error(), "mapper_parsing_exception") {
		t.Errorf("a rejected item must fail the batch: %v", err)
	}

	if err = respond("unavailable", 503).Send(context.Background(), batch); err == nil {
		t.Error("a 5xx must fail the batch")
	}

	if err = respond("not json", 200).Send(context.Background(), batch); err != nil {
		t.Errorf("an unreadable 2xx body is not an error: %v", err)
	}
}
