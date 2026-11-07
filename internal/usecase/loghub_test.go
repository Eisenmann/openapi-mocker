package usecase_test

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// idRepo is a LogRepository that assigns ids like the real store does.
type idRepo struct {
	mu   sync.Mutex
	logs []*domain.RequestLog
}

func (r *idRepo) Add(l *domain.RequestLog) {
	r.mu.Lock()
	defer r.mu.Unlock()

	l.ID = fmt.Sprintf("id-%d", len(r.logs)+1)
	r.logs = append(r.logs, l)
}

func (r *idRepo) ListLogs(projectID string, limit int) []*domain.RequestLog {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []*domain.RequestLog

	for i := len(r.logs) - 1; i >= 0 && len(out) < limit; i-- {
		if r.logs[i].ProjectID == projectID {
			out = append(out, r.logs[i])
		}
	}

	return out
}

func capt(body string, header http.Header) *domain.HTTPCapture {
	return &domain.HTTPCapture{Headers: header, Body: body, Size: len(body)}
}

func entry(project, method, endpoint string, status int, ms int64) *domain.RequestLog {
	return &domain.RequestLog{
		ProjectID: project, Method: method, Path: "/x", Endpoint: endpoint,
		StatusCode: status, Matched: endpoint != "", DurationMs: ms, Timestamp: time.Now(),
	}
}

func TestLogHub_CapturesRedactsAndKeepsBodiesOutOfTheStore(t *testing.T) {
	t.Parallel()

	repo := &idRepo{}
	hub := usecase.NewLogHub(repo, usecase.CapturePolicy{Enabled: true, MaxBytes: 8})

	l := entry("p1", http.MethodPost, "/users", 201, 3)
	l.Request = capt("0123456789", http.Header{
		"Authorization": {"Bearer secret"}, "X-Trace": {"t1"}, "Cookie": {"a=b"},
	})
	l.Response = capt("héllo wörld", nil)
	hub.Add(l)

	if repo.logs[0].Request != nil || repo.logs[0].Response != nil || !repo.logs[0].Captured {
		t.Fatalf("store must hold the flag but not the bodies: %+v", repo.logs[0])
	}

	got, ok := hub.Get("p1", l.ID)
	if !ok || got.Request == nil {
		t.Fatal("captured entry not found")
	}

	if got.Request.Body != "01234567" || !got.Request.Truncated || got.Request.Size != 10 {
		t.Errorf("request body = %+v", got.Request)
	}

	if got.Request.Headers["Authorization"][0] != "[redacted]" || got.Request.Headers["Cookie"][0] != "[redacted]" ||
		got.Request.Headers["X-Trace"][0] != "t1" {
		t.Errorf("headers = %v", got.Request.Headers)
	}

	// "héllo wörld" is cut at 8 bytes, which is "héllo w".
	if got.Response.Body != "héllo w" || !got.Response.Truncated {
		t.Errorf("response body = %+v", got.Response)
	}

	if _, ok := hub.Get("other", l.ID); ok {
		t.Error("entry must not be visible in another project")
	}

	if len(hub.ListLogs("p1", 10)) != 1 || hub.ListLogs("p1", 10)[0].Request != nil {
		t.Error("list must come from the store, without bodies")
	}
}

func TestLogHub_PolicyDisabledAndBinary(t *testing.T) {
	t.Parallel()

	off := usecase.NewLogHub(&idRepo{}, usecase.CapturePolicy{Enabled: false, MaxBytes: 100})
	l := entry("p", http.MethodGet, "/a", 200, 1)
	l.Request = capt("x", nil)
	off.Add(l)

	if got, _ := off.Get("p", l.ID); got.Request != nil || got.Captured {
		t.Errorf("nothing may be captured when disabled: %+v", got)
	}

	on := usecase.NewLogHub(&idRepo{}, usecase.DefaultCapturePolicy())
	b := entry("p", http.MethodPost, "/a", 200, 1)
	b.Request = capt(string([]byte{0xff, 0xfe, 0x00}), nil)
	on.Add(b)

	got, _ := on.Get("p", b.ID)
	if !got.Request.Binary || got.Request.Body != "" {
		t.Errorf("binary body must be flagged and left out: %+v", got.Request)
	}
}

func TestLogHub_GetFallsBackToStore(t *testing.T) {
	t.Parallel()

	repo := &idRepo{}
	repo.Add(&domain.RequestLog{ProjectID: "p", Path: "/old"})

	hub := usecase.NewLogHub(repo, usecase.DefaultCapturePolicy())

	got, ok := hub.Get("p", "id-1")
	if !ok || got.Path != "/old" {
		t.Fatalf("got %+v %v", got, ok)
	}

	if _, ok := hub.Get("p", "missing"); ok {
		t.Error("unknown id must not be found")
	}
}

func TestLogHub_StreamAndBacklog(t *testing.T) {
	t.Parallel()

	hub := usecase.NewLogHub(&idRepo{}, usecase.DefaultCapturePolicy())

	first := entry("p", http.MethodGet, "/a", 200, 1)
	hub.Add(first)

	sub, backlog, err := hub.Subscribe("p", "")
	if err != nil || len(backlog) != 0 {
		t.Fatalf("err=%v backlog=%d", err, len(backlog))
	}

	defer sub.Close()

	hub.Add(entry("other", http.MethodGet, "/a", 200, 1))
	hub.Add(entry("p", http.MethodGet, "/b", 404, 1))

	select {
	case l := <-sub.Entries():
		if l.Endpoint != "/b" {
			t.Errorf("got %+v", l)
		}
	case <-time.After(time.Second):
		t.Fatal("no entry delivered")
	}

	select {
	case l := <-sub.Entries():
		t.Fatalf("entry of another project leaked: %+v", l)
	default:
	}

	// A reconnecting client gets what it missed after the id it saw.
	again, missed, err := hub.Subscribe("p", first.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()

	if len(missed) != 1 || missed[0].Endpoint != "/b" {
		t.Errorf("backlog = %+v", missed)
	}

	if hub.Subscribers() != 2 {
		t.Errorf("subscribers = %d", hub.Subscribers())
	}

	sub.Close()

	if hub.Subscribers() != 1 {
		t.Errorf("subscribers after close = %d", hub.Subscribers())
	}
}

func TestLogHub_SlowReaderLosesEntriesNotTheServer(t *testing.T) {
	t.Parallel()

	hub := usecase.NewLogHub(&idRepo{}, usecase.DefaultCapturePolicy())

	sub, _, err := hub.Subscribe("p", "")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	for range 400 {
		hub.Add(entry("p", http.MethodGet, "/a", 200, 1))
	}

	if sub.TakeDropped() == 0 {
		t.Error("a full queue must count dropped entries")
	}

	if sub.TakeDropped() != 0 {
		t.Error("TakeDropped must reset the counter")
	}
}

func TestLogHub_StreamLimit(t *testing.T) {
	t.Parallel()

	hub := usecase.NewLogHub(&idRepo{}, usecase.DefaultCapturePolicy())

	for range usecase.MaxStreamSubscribers {
		_, _, err := hub.Subscribe("p", "")
		if err != nil {
			t.Fatal(err)
		}
	}

	_, _, err := hub.Subscribe("p", "")
	if err == nil {
		t.Fatal("the limit must be enforced")
	}

	if usecase.StatusFromError(err) != usecase.StatusServiceUnavailable {
		t.Errorf("status = %d", usecase.StatusFromError(err))
	}
}

func TestLogHub_StatsAndReset(t *testing.T) {
	t.Parallel()

	hub := usecase.NewLogHub(&idRepo{}, usecase.DefaultCapturePolicy())

	hub.Add(entry("p", http.MethodGet, "/users/{id}", 200, 10))
	hub.Add(entry("p", http.MethodGet, "/users/{id}", 500, 30))
	hub.Add(entry("p", http.MethodPost, "/users", 201, 5))
	hub.Add(entry("p", "BREW", "", 404, 1))
	hub.Add(entry("q", http.MethodGet, "/x", 200, 1))

	st := hub.Stats("p")
	if st.Total != 4 || st.Errors != 2 || len(st.Endpoints) != 3 {
		t.Fatalf("stats = %+v", st)
	}

	top := st.Endpoints[0]
	if top.Endpoint != "/users/{id}" || top.Hits != 2 || top.Errors != 1 || top.AvgMs != 20 || top.MaxMs != 30 {
		t.Errorf("top = %+v", top)
	}

	last := st.Endpoints[len(st.Endpoints)-1]
	if last.Endpoint != "unmatched" || last.Method != "OTHER" {
		t.Errorf("unmatched/unknown method must be bucketed: %+v", last)
	}

	hub.ResetStats("p")

	if got := hub.Stats("p"); got.Total != 0 || len(got.Endpoints) != 0 {
		t.Errorf("after reset: %+v", got)
	}

	if hub.Stats("q").Total != 1 {
		t.Error("reset must only touch its project")
	}
}

func TestLogHub_Metrics(t *testing.T) {
	t.Parallel()

	hub := usecase.NewLogHub(&idRepo{}, usecase.DefaultCapturePolicy())

	a := entry("p1", http.MethodGet, "/users/{id}", 200, 30)
	a.Fault = "error"
	a.Violations = []string{"v1", "v2"}
	hub.Add(a)
	hub.Add(entry("p1", http.MethodGet, "/users/{id}", 200, 2000))
	hub.Add(entry(`we"ird`, http.MethodGet, "", 404, 1))

	var buf bytes.Buffer

	err := hub.WritePrometheus(&buf)
	if err != nil {
		t.Fatal(err)
	}

	out := buf.String()

	for _, want := range []string{
		"# TYPE openapimocker_requests_total counter",
		`openapimocker_requests_total{project="p1",method="GET",code="200"} 2`,
		`openapimocker_endpoint_hits_total{project="p1",method="GET",endpoint="/users/{id}"} 2`,
		`openapimocker_endpoint_hits_total{project="we\"ird",method="GET",endpoint="unmatched"} 1`,
		`openapimocker_request_duration_seconds_bucket{project="p1",le="0.05"} 1`,
		`openapimocker_request_duration_seconds_bucket{project="p1",le="2.5"} 2`,
		`openapimocker_request_duration_seconds_bucket{project="p1",le="+Inf"} 2`,
		`openapimocker_request_duration_seconds_count{project="p1"} 2`,
		`openapimocker_faults_total{project="p1",fault="error"} 1`,
		`openapimocker_request_violations_total{project="p1"} 2`,
		"openapimocker_log_stream_subscribers 0",
		"openapimocker_dropped_series_total 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestLogHub_SeriesAreBounded(t *testing.T) {
	t.Parallel()

	hub := usecase.NewLogHub(&idRepo{}, usecase.DefaultCapturePolicy())

	// A client choosing project ids must not be able to grow memory forever.
	for i := range 6000 {
		hub.Add(entry(fmt.Sprintf("p%d", i), http.MethodGet, "", 404, 1))
	}

	var buf bytes.Buffer

	_ = hub.WritePrometheus(&buf)

	if strings.Count(buf.String(), "openapimocker_requests_total{") > 5000 {
		t.Error("request series are not bounded")
	}

	if strings.Contains(buf.String(), "openapimocker_dropped_series_total 0") {
		t.Error("dropped series must be counted")
	}
}

func TestLogService_WithoutHub(t *testing.T) {
	t.Parallel()

	svc := usecase.NewLogService(&idRepo{})

	_, err := svc.Detail("p", "x")
	if err == nil || usecase.StatusFromError(err) != usecase.StatusNotFound {
		t.Errorf("err = %v", err)
	}

	if _, _, err = svc.Subscribe("p", ""); err == nil {
		t.Error("subscribe needs a hub")
	}

	if _, err = svc.Stats("p"); err == nil {
		t.Error("stats need a hub")
	}

	if svc.ResetStats("p") == nil || svc.WriteMetrics(&bytes.Buffer{}) == nil {
		t.Error("reset and metrics need a hub")
	}

	if svc.CapturePolicy().Enabled {
		t.Error("no capture without a hub")
	}
}

func TestLogService_DetailNotFound(t *testing.T) {
	t.Parallel()

	svc := usecase.NewLogService(usecase.NewLogHub(&idRepo{}, usecase.DefaultCapturePolicy()))

	_, err := svc.Detail("p", "nope")
	if err == nil || usecase.StatusFromError(err) != usecase.StatusNotFound {
		t.Errorf("err = %v", err)
	}
}

func TestLogHub_CloseStreamsEndsStreamsAndRefusesNewOnes(t *testing.T) {
	t.Parallel()

	hub := usecase.NewLogHub(&idRepo{}, usecase.DefaultCapturePolicy())

	sub, _, err := hub.Subscribe("p", "")
	if err != nil {
		t.Fatal(err)
	}

	hub.CloseStreams()

	select {
	case _, open := <-sub.Entries():
		if open {
			t.Error("the channel must be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("stream not closed")
	}

	// Adding an entry after the close must not panic on the closed channel.
	hub.Add(entry("p", http.MethodGet, "/a", 200, 1))
	sub.Close()

	_, _, err = hub.Subscribe("p", "")
	if err == nil || usecase.StatusFromError(err) != usecase.StatusServiceUnavailable {
		t.Errorf("err = %v", err)
	}

	usecase.NewLogService(&idRepo{}).CloseStreams() // no hub: a no-op.
}
