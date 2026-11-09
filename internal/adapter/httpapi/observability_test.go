package httpapi_test

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const obsSpec = `openapi: 3.0.0
info: {title: T, version: "1"}
paths:
  /echo/{id}:
    post:
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      requestBody:
        content:
          application/json:
            schema: {type: object}
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: {type: object, properties: {ok: {type: boolean}}}
              example: {ok: true}
`

type obsEnv struct {
	h     http.Handler
	hub   *usecase.LogHub
	store *jsonstore.Store
	id    string
}

func newObsEnv(t *testing.T, policy usecase.CapturePolicy) *obsEnv {
	t.Helper()

	store, err := jsonstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	engine := openapi.NewEngine()
	projects := usecase.NewProjectService(store)
	hub := usecase.NewLogHub(store, policy)

	h := httpapi.NewRouter(&httpapi.Services{
		Projects:    projects,
		Contracts:   usecase.NewContractService(store, store, engine, nil, usecase.FormatValidators{}),
		Mocks:       usecase.NewMockService(store, store, store, engine, nil),
		MockServing: usecase.NewMockServingService(store, store, hub, engine, usecase.WithProjects(store)),
		Logs:        usecase.NewLogService(hub),
		Access:      usecase.NewAccessService(store, store, store, usecase.WithAdminToken(adminEnvToken)),
	})

	p, err := projects.Create("obs", "")
	if err != nil {
		t.Fatal(err)
	}

	e := &obsEnv{h: h, hub: hub, store: store, id: p.ID}

	rec := doRequest(t, h, http.MethodPost, e.url("/contract"),
		strings.NewReader(`{"raw":`+jsonString(obsSpec)+`,"source":"manual"}`),
		map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}

	return e
}

func (e *obsEnv) url(rest string) string { return "/api/projects/" + e.id + rest }

func (e *obsEnv) get(t *testing.T, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	return doRequest(t, e.h, http.MethodGet, path, nil, headers)
}

func (e *obsEnv) hit(t *testing.T, id, body string) *httptest.ResponseRecorder {
	t.Helper()

	return doRequest(t, e.h, http.MethodPost, "/mock/"+e.id+"/echo/"+id, strings.NewReader(body),
		map[string]string{"Content-Type": "application/json", "Authorization": "Bearer client-secret"})
}

func TestObservability_StatsLogsAndBodies(t *testing.T) {
	t.Parallel()

	e := newObsEnv(t, usecase.DefaultCapturePolicy())

	if rec := e.hit(t, "7", `{"name":"ann"}`); rec.Code != http.StatusOK {
		t.Fatalf("mock: %d %s", rec.Code, rec.Body.String())
	}

	e.hit(t, "8", `{}`)

	doRequest(t, e.h, http.MethodGet, "/mock/"+e.id+"/nowhere", nil, nil)

	var stats struct {
		Total     int `json:"total"`
		Errors    int `json:"errors"`
		Endpoints []struct {
			Method, Endpoint string
			Hits             int
		} `json:"endpoints"`
		Capture struct {
			Enabled  bool `json:"enabled"`
			MaxBytes int  `json:"maxBytes"`
		} `json:"capture"`
	}

	rec := e.get(t, e.url("/stats"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("stats: %d %s", rec.Code, rec.Body.String())
	}

	decodeJSON(t, rec, &stats)

	if stats.Total != 3 || stats.Errors != 1 || len(stats.Endpoints) != 2 || !stats.Capture.Enabled {
		t.Fatalf("stats = %+v", stats)
	}

	if ep := stats.Endpoints[0]; ep.Endpoint != "/echo/{id}" || ep.Hits != 2 || ep.Method != "POST" {
		t.Errorf("top endpoint = %+v", ep)
	}

	// The list stays light; the detail has the bodies.
	var list []map[string]any

	decodeJSON(t, e.get(t, e.url("/logs?limit=2"), nil), &list)

	if len(list) != 2 || list[0]["request"] != nil {
		t.Fatalf("list = %+v", list)
	}

	var detail struct {
		Endpoint string `json:"endpoint"`
		Request  struct {
			Body    string              `json:"body"`
			Headers map[string][]string `json:"headers"`
		} `json:"request"`
		Response struct {
			Body string `json:"body"`
		} `json:"response"`
	}

	all := e.hub.ListLogs(e.id, 10)
	// newest first: the 404, then /echo/8, then /echo/7.
	rec = e.get(t, e.url("/logs/"+all[2].ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", rec.Code, rec.Body.String())
	}

	decodeJSON(t, rec, &detail)

	if detail.Endpoint != "/echo/{id}" || detail.Request.Body != `{"name":"ann"}` ||
		!strings.Contains(detail.Response.Body, `"ok"`) {
		t.Errorf("detail = %+v", detail)
	}

	if detail.Request.Headers["Authorization"][0] != "[redacted]" {
		t.Errorf("authorization header leaked: %v", detail.Request.Headers)
	}

	if e.get(t, e.url("/logs/nope"), nil).Code != http.StatusNotFound {
		t.Error("unknown log must be 404")
	}

	if e.get(t, e.url("/logs?limit=0"), nil).Code != http.StatusBadRequest {
		t.Error("bad limit must be 400")
	}

	del := doRequest(t, e.h, http.MethodDelete, e.url("/stats"), nil, nil)
	if del.Code != http.StatusNoContent {
		t.Fatalf("reset: %d", del.Code)
	}

	decodeJSON(t, e.get(t, e.url("/stats"), nil), &stats)

	if stats.Total != 0 {
		t.Errorf("after reset: %+v", stats)
	}
}

func TestObservability_CaptureOff(t *testing.T) {
	t.Parallel()

	e := newObsEnv(t, usecase.CapturePolicy{Enabled: false, MaxBytes: 0})
	e.hit(t, "1", `{"a":1}`)

	id := e.hub.ListLogs(e.id, 1)[0].ID

	var detail map[string]any

	decodeJSON(t, e.get(t, e.url("/logs/"+id), nil), &detail)

	if detail["request"] != nil || detail["captured"] != nil {
		t.Errorf("nothing may be captured: %+v", detail)
	}
}

func TestObservability_Metrics(t *testing.T) {
	t.Setenv("METRICS_TOKEN", "m-secret")

	e := newObsEnv(t, usecase.DefaultCapturePolicy())
	e.hit(t, "1", `{}`)

	if rec := e.get(t, "/metrics", nil); rec.Code != http.StatusUnauthorized ||
		rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("no token: %d", rec.Code)
	}

	if rec := e.get(t, "/metrics", map[string]string{"Authorization": "Bearer wrong"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", rec.Code)
	}

	rec := e.get(t, "/metrics", map[string]string{"Authorization": "Bearer m-secret"})
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("metrics: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}

	want := `openapimocker_endpoint_hits_total{project="` + e.id + `",method="POST",endpoint="/echo/{id}"} 1`
	if !strings.Contains(rec.Body.String(), want) {
		t.Errorf("missing %q in\n%s", want, rec.Body.String())
	}
}

func TestObservability_MetricsOpenWithoutToken(t *testing.T) {
	t.Parallel()

	e := newObsEnv(t, usecase.DefaultCapturePolicy())

	if rec := e.get(t, "/metrics", nil); rec.Code != http.StatusOK {
		t.Fatalf("metrics: %d", rec.Code)
	}
}

func TestObservability_RolesAndShareLinks(t *testing.T) {
	t.Parallel()

	e := newObsEnv(t, usecase.DefaultCapturePolicy())
	bearer := func(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

	post := func(path, body, tok string) *httptest.ResponseRecorder {
		h := map[string]string{"Content-Type": "application/json"}
		if tok != "" {
			h["Authorization"] = "Bearer " + tok
		}

		return doRequest(t, e.h, http.MethodPost, e.url(path), strings.NewReader(body), h)
	}

	var admin, tok created

	decodeJSON(t, post("/access/tokens", `{"name":"a","role":"admin"}`, ""), &admin)
	decodeJSON(t, post("/access/tokens", `{"name":"v","role":"viewer"}`, admin.Token), &tok)

	// Locked now: no token, no stats.
	if e.get(t, e.url("/stats"), nil).Code != http.StatusUnauthorized {
		t.Error("stats must need a token once locked")
	}

	if e.get(t, e.url("/stats"), bearer(tok.Token)).Code != http.StatusOK {
		t.Error("viewer may read stats")
	}

	reset := doRequest(t, e.h, http.MethodDelete, e.url("/stats"), nil, bearer(tok.Token))
	if reset.Code != http.StatusForbidden {
		t.Errorf("viewer must not reset stats: %d", reset.Code)
	}

	// Bodies can hold secrets: share links never read logs or stats.
	var link struct {
		Token string `json:"token"`
	}

	rec := post("/access/share-links", `{"label":"l"}`, admin.Token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("share link: %d %s", rec.Code, rec.Body.String())
	}

	decodeJSON(t, rec, &link)

	secret := link.Token

	for _, p := range []string{"/logs", "/logs/stream", "/stats"} {
		if got := e.get(t, e.url(p), map[string]string{"X-Share-Token": secret}); got.Code != http.StatusForbidden {
			t.Errorf("share link on %s: %d", p, got.Code)
		}
	}
}

func TestObservability_LiveStream(t *testing.T) {
	t.Parallel()

	e := newObsEnv(t, usecase.DefaultCapturePolicy())

	srv := httptest.NewServer(e.h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+e.url("/logs/stream"), nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	lines := make(chan string, 64)

	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}

		close(lines)
	}()

	// The headers arrive when the stream is open; now make a request.
	post, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/mock/"+e.id+"/echo/42",
		strings.NewReader(`{"live":true}`))
	if err != nil {
		t.Fatal(err)
	}

	post.Header.Set("Content-Type", "application/json")

	pr, err := http.DefaultClient.Do(post)
	if err != nil {
		t.Fatal(err)
	}

	_ = pr.Body.Close()

	var gotID, gotEvent, gotData string

	for gotData == "" {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("stream closed early")
			}

			switch {
			case strings.HasPrefix(l, "id: "):
				gotID = strings.TrimPrefix(l, "id: ")
			case strings.HasPrefix(l, "event: "):
				gotEvent = strings.TrimPrefix(l, "event: ")
			case strings.HasPrefix(l, "data: "):
				gotData = strings.TrimPrefix(l, "data: ")
			}
		case <-ctx.Done():
			t.Fatal("no event received")
		}
	}

	if gotEvent != "log" || gotID == "" || !strings.Contains(gotData, `"/echo/{id}"`) ||
		!strings.Contains(gotData, `{\"live\":true}`) {
		t.Errorf("event=%q id=%q data=%s", gotEvent, gotID, gotData)
	}
}

func TestObservability_StreamResumesAfterLastEventID(t *testing.T) {
	t.Parallel()

	e := newObsEnv(t, usecase.DefaultCapturePolicy())
	e.hit(t, "1", `{}`)
	seen := e.hub.ListLogs(e.id, 1)[0].ID
	e.hit(t, "2", `{}`)

	srv := httptest.NewServer(e.h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+e.url("/logs/stream"), nil)
	req.Header.Set("Last-Event-ID", seen)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: ") {
			if !strings.Contains(sc.Text(), "/echo/2") {
				t.Errorf("replayed the wrong entry: %s", sc.Text())
			}

			return
		}
	}

	t.Fatal("the missed entry was not replayed")
}

func TestObservability_NoServiceMeansNotFound(t *testing.T) {
	t.Parallel()

	store, err := jsonstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	h := httpapi.NewRouter(&httpapi.Services{
		Projects: usecase.NewProjectService(store),
		Logs:     usecase.NewLogService(store),
	})

	for _, p := range []string{"/api/projects/x/stats", "/api/projects/x/logs/stream", "/api/projects/x/logs/1", "/metrics"} {
		if rec := doRequest(t, h, http.MethodGet, p, nil, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
}

func TestObservability_StatsReportLogMode(t *testing.T) {
	t.Parallel()

	e := newObsEnv(t, usecase.DefaultCapturePolicy())

	var st struct {
		Logs struct {
			Mode  string   `json:"mode"`
			Sinks []string `json:"sinks"`
		} `json:"logs"`
	}

	decodeJSON(t, e.get(t, e.url("/stats"), nil), &st)

	if st.Logs.Mode != "local" || st.Logs.Sinks == nil || len(st.Logs.Sinks) != 0 {
		t.Errorf("logs = %+v", st.Logs)
	}
}

func TestObservability_StreamEndsWhenServerShutsDown(t *testing.T) {
	t.Parallel()

	e := newObsEnv(t, usecase.DefaultCapturePolicy())

	srv := httptest.NewServer(e.h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+e.url("/logs/stream"), nil)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Wait for the handler to subscribe, then close all streams.
	for e.hub.Subscribers() == 0 {
		time.Sleep(5 * time.Millisecond)
	}

	e.hub.CloseStreams()

	done := make(chan struct{})

	go func() {
		_, _ = io.Copy(io.Discard, resp.Body)

		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("the stream did not end")
	}

	if again := e.get(t, e.url("/logs/stream"), nil); again.Code != http.StatusServiceUnavailable {
		t.Errorf("new stream after close: %d", again.Code)
	}
}

func TestVerifyAPI_CalledWithBody(t *testing.T) {
	t.Parallel()

	e := newObsEnv(t, usecase.DefaultCapturePolicy())
	e.hit(t, "7", `{"name":"ann","role":"admin"}`)

	verify := func(body, query string) (int, map[string]any) {
		rec := doRequest(t, e.h, http.MethodPost, e.url("/verify"+query), strings.NewReader(body),
			map[string]string{"Content-Type": "application/json"})

		var out map[string]any

		decodeJSON(t, rec, &out)

		return rec.Code, out
	}

	code, out := verify(`{"method":"POST","path":"/echo/{id}","body":{"json":{"name":"ann"}}}`, "")
	if code != http.StatusOK || out["passed"] != true || out["count"] != float64(1) {
		t.Fatalf("pass: %d %+v", code, out)
	}

	code, out = verify(`{"method":"POST","path":"/echo/7","body":{"json":{"name":"bob"}}}`, "")
	if code != http.StatusOK || out["passed"] != false || out["closest"] == nil {
		t.Fatalf("fail: %d %+v", code, out)
	}

	code, _ = verify(`{"path":"/echo/9"}`, "?strict=true")
	if code != http.StatusConflict {
		t.Errorf("strict failure = %d", code)
	}

	code, _ = verify(`{"path":"/echo/7","exactly":1}`, "?strict=true")
	if code != http.StatusOK {
		t.Errorf("strict pass = %d", code)
	}

	code, _ = verify(`{}`, "")
	if code != http.StatusBadRequest {
		t.Errorf("empty = %d", code)
	}

	rec := e.get(t, e.url("/calls?limit=5"), nil)

	var calls []map[string]any

	decodeJSON(t, rec, &calls)

	if rec.Code != http.StatusOK || len(calls) != 1 || calls[0]["request"] == nil {
		t.Fatalf("calls: %d %+v", rec.Code, calls)
	}

	if rec := doRequest(t, e.h, http.MethodDelete, e.url("/calls"), nil, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("clear: %d", rec.Code)
	}

	_, out = verify(`{"path":"/echo/7","exactly":0}`, "")
	if out["passed"] != true {
		t.Errorf("after clear: %+v", out)
	}
}

func TestVerifyAPI_QueryIsRecorded(t *testing.T) {
	t.Parallel()

	e := newObsEnv(t, usecase.DefaultCapturePolicy())
	doRequest(t, e.h, http.MethodPost, "/mock/"+e.id+"/echo/1?b=2&a=1", strings.NewReader(`{}`),
		map[string]string{"Content-Type": "application/json"})

	rec := doRequest(t, e.h, http.MethodPost, e.url("/verify"),
		strings.NewReader(`{"path":"/echo/1","query":{"a":"1","b":"2"}}`), map[string]string{"Content-Type": "application/json"})

	var out struct{ Passed bool }

	decodeJSON(t, rec, &out)

	if !out.Passed {
		t.Errorf("body: %s", rec.Body.String())
	}
}

func TestVerifyAPI_ViewerMayVerifyButNotClear(t *testing.T) {
	t.Parallel()

	e := newObsEnv(t, usecase.DefaultCapturePolicy())
	e.hit(t, "1", `{}`)

	post := func(path, body, tok string) *httptest.ResponseRecorder {
		h := map[string]string{"Content-Type": "application/json"}
		if tok != "" {
			h["Authorization"] = "Bearer " + tok
		}

		return doRequest(t, e.h, http.MethodPost, e.url(path), strings.NewReader(body), h)
	}

	var admin, viewer created

	decodeJSON(t, post("/access/tokens", `{"name":"a","role":"admin"}`, ""), &admin)
	decodeJSON(t, post("/access/tokens", `{"name":"v","role":"viewer"}`, admin.Token), &viewer)

	if post("/verify", `{"path":"/echo/1"}`, "").Code != http.StatusUnauthorized {
		t.Error("verify must need a token once locked")
	}

	if post("/verify", `{"path":"/echo/1"}`, viewer.Token).Code != http.StatusOK {
		t.Error("a viewer may verify")
	}

	del := func(tok string) int {
		return doRequest(t, e.h, http.MethodDelete, e.url("/calls"), nil,
			map[string]string{"Authorization": "Bearer " + tok}).Code
	}

	if del(viewer.Token) != http.StatusForbidden {
		t.Error("a viewer must not clear calls")
	}

	if del(admin.Token) != http.StatusNoContent {
		t.Error("an admin may clear calls")
	}
}

func TestVerifyAPI_BadInput(t *testing.T) {
	t.Parallel()

	e := newObsEnv(t, usecase.DefaultCapturePolicy())

	for _, q := range []string{"?limit=0", "?limit=x"} {
		if e.get(t, e.url("/calls"+q), nil).Code != http.StatusBadRequest {
			t.Errorf("calls%s must be rejected", q)
		}
	}

	rec := doRequest(t, e.h, http.MethodPost, e.url("/verify"), strings.NewReader(`{not json`),
		map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body = %d", rec.Code)
	}
}
