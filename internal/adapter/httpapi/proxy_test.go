package httpapi_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/upstream"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const proxyOpenAPI = `openapi: 3.0.0
info: {title: Users, version: 1.0.0}
paths:
  /users/{id}:
    parameters:
      - {name: id, in: path, required: true, schema: {type: integer}}
    get:
      responses:
        '200':
          description: OK
          content:
            application/json:
              schema:
                type: object
                required: [id, name]
                properties: {id: {type: integer}, name: {type: string}}
  /users:
    post:
      requestBody:
        content: {application/json: {schema: {type: object}}}
      responses:
        '201': {description: Created, content: {application/json: {schema: {type: object}}}}
`

// proxyEnv is a mock server with a real store, a real upstream client and a
// fake upstream server that counts what reaches it.
type proxyEnv struct {
	h         http.Handler
	store     *jsonstore.Store
	serving   *usecase.MockServingService
	projectID string
	hits      atomic.Int32
	lastReq   atomic.Pointer[http.Request]
}

func newProxyEnv(t *testing.T, upstreamHandler http.HandlerFunc) *proxyEnv {
	t.Helper()

	env := &proxyEnv{}

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.hits.Add(1)
		env.lastReq.Store(r)
		upstreamHandler(w, r)
	}))
	t.Cleanup(up.Close)

	store, err := jsonstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	env.store = store
	p := store.Create("proxy", "")
	store.AddVersion(p.ID, "yaml", proxyOpenAPI, "manual")

	env.projectID = p.ID
	env.serving = usecase.NewMockServingService(
		store, store, store, openapi.NewEngine(),
		usecase.WithProjects(store), usecase.WithUpstream(upstream.New(5*time.Second), store),
	)
	env.h = httpapi.NewRouter(&httpapi.Services{
		Projects:    usecase.NewProjectService(store),
		Mocks:       usecase.NewMockService(store, store, nil, openapi.NewEngine(), nil),
		MockServing: env.serving,
		Drift:       usecase.NewDriftService(store),
		Logs:        usecase.NewLogService(store),
	})

	env.setProxy(t, up.URL+"/v1", domain.ProxyRecord, false)

	return env
}

func (e *proxyEnv) setProxy(t *testing.T, url string, mode domain.ProxyMode, drift bool) {
	t.Helper()

	_, err := usecase.NewProjectService(e.store).UpdateProxy(e.projectID, usecase.ProxyPatch{
		UpstreamURL: &url, Mode: &mode, DriftCheck: &drift,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (e *proxyEnv) get(t *testing.T, path string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()

	hdr := map[string]string{}
	for i := 0; i+1 < len(headers); i += 2 {
		hdr[headers[i]] = headers[i+1]
	}

	return doRequest(t, e.h, http.MethodGet, "/mock/"+e.projectID+path, nil, hdr)
}

func usersUpstream(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/users/7":
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "session=secret")
		w.Header().Set("X-Upstream", "yes")
		_, _ = io.WriteString(w, `{"id":7,"name":"Ada {{uuid}}","email":"ada@example.com"}`)
	case r.URL.Path == "/v1/users/500":
		http.Error(w, "boom", http.StatusInternalServerError)
	case r.URL.Path == "/v1/users" && r.Method == http.MethodPost:
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(b)
	default:
		http.NotFound(w, r)
	}
}

func TestProxy_ForwardPassesRequestAndSavesNothing(t *testing.T) {
	t.Parallel()

	env := newProxyEnv(t, usersUpstream)
	env.setProxy(t, strings.TrimSuffix(env.upstreamURL(t), ""), domain.ProxyForward, false)

	rec := env.get(t, "/users/7?verbose=1", "Authorization", "Bearer t0k", "X-Mock-Scenario", "fancy")
	if rec.Code != http.StatusOK || rec.Header().Get("X-Mock-Source") != usecase.SourceProxyForward {
		t.Fatalf("status %d source %q: %s", rec.Code, rec.Header().Get("X-Mock-Source"), rec.Body.String())
	}

	if rec.Header().Get("X-Upstream") != "yes" || rec.Header().Get("Set-Cookie") != "" {
		t.Errorf("headers: X-Upstream=%q Set-Cookie=%q", rec.Header().Get("X-Upstream"), rec.Header().Get("Set-Cookie"))
	}

	seen := env.lastReq.Load()
	if seen.URL.Query().Get("verbose") != "1" || seen.Header.Get("Authorization") != "Bearer t0k" {
		t.Errorf("query/auth not forwarded: %s %v", seen.URL, seen.Header)
	}

	if seen.Header.Get("X-Mock-Scenario") != "" || seen.Header.Get(usecase.HeaderForwarded) == "" {
		t.Errorf("X-Mock-* must be dropped and the loop marker added: %v", seen.Header)
	}

	env.get(t, "/users/7")

	if n := env.hits.Load(); n != 2 {
		t.Errorf("forward mode must hit the upstream every time, got %d", n)
	}

	if got := len(env.store.ListMocks(env.projectID)); got != 0 {
		t.Errorf("forward mode must not save mocks, got %d", got)
	}
}

func (e *proxyEnv) upstreamURL(t *testing.T) string {
	t.Helper()

	p, err := e.store.Get(e.projectID)
	if err != nil {
		t.Fatal(err)
	}

	return p.UpstreamURL
}

func TestProxy_RecordThenReplayVerbatim(t *testing.T) {
	t.Parallel()

	env := newProxyEnv(t, usersUpstream)

	first := env.get(t, "/users/7")
	if first.Header().Get("X-Mock-Source") != usecase.SourceProxyRecord || env.hits.Load() != 1 {
		t.Fatalf("first call must be proxied and recorded: %q hits=%d", first.Header().Get("X-Mock-Source"), env.hits.Load())
	}

	rules := env.store.ListMocks(env.projectID)
	if len(rules) != 1 || rules[0].Recording == nil || !rules[0].Raw || rules[0].Scenario != "recorded" {
		t.Fatalf("want one raw recorded rule, got %+v", rules)
	}

	if _, has := rules[0].Headers["Set-Cookie"]; has || rules[0].Headers["X-Upstream"] != "yes" {
		t.Errorf("recorded headers: %v", rules[0].Headers)
	}

	second := env.get(t, "/users/7")
	if second.Header().Get("X-Mock-Source") != "rule" || env.hits.Load() != 1 {
		t.Fatalf("replay must come from the rule without the upstream: %q hits=%d",
			second.Header().Get("X-Mock-Source"), env.hits.Load())
	}

	if second.Body.String() != first.Body.String() || !strings.Contains(second.Body.String(), "{{uuid}}") {
		t.Errorf("replay must be verbatim:\n%s\n%s", first.Body.String(), second.Body.String())
	}

	// A different query is a different request.
	env.get(t, "/users/7?x=1")

	if env.hits.Load() != 2 || len(env.store.ListMocks(env.projectID)) != 2 {
		t.Errorf("another query must be recorded separately: hits=%d", env.hits.Load())
	}
}

func TestProxy_ServerErrorsAndNotFoundsAreHandledSensibly(t *testing.T) {
	t.Parallel()

	env := newProxyEnv(t, usersUpstream)

	if rec := env.get(t, "/users/500"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("a 500 must be passed on, got %d", rec.Code)
	}

	if got := len(env.store.ListMocks(env.projectID)); got != 0 {
		t.Errorf("5xx answers must not be recorded, got %d rules", got)
	}

	if rec := env.get(t, "/users/8"); rec.Code != http.StatusNotFound {
		t.Fatalf("a 404 must be passed on, got %d", rec.Code)
	}

	if got := len(env.store.ListMocks(env.projectID)); got != 1 {
		t.Errorf("a 404 is a real answer and is recorded, got %d rules", got)
	}
}

func TestProxy_RecordsBodiesStructurally(t *testing.T) {
	t.Parallel()

	env := newProxyEnv(t, usersUpstream)
	post := func(body string) *httptest.ResponseRecorder {
		return doRequest(t, env.h, http.MethodPost, "/mock/"+env.projectID+"/users",
			strings.NewReader(body), map[string]string{"Content-Type": "application/json"})
	}

	post(`{"a":1,"b":2}`)
	rec := post("{ \"b\": 2,\n \"a\": 1 }")

	if rec.Header().Get("X-Mock-Source") != "rule" || env.hits.Load() != 1 {
		t.Errorf("the same JSON in another layout must hit the recording: %q hits=%d",
			rec.Header().Get("X-Mock-Source"), env.hits.Load())
	}

	post(`{"a":1,"b":3}`)

	if env.hits.Load() != 2 {
		t.Errorf("another body must reach the upstream, hits=%d", env.hits.Load())
	}
}

func TestProxy_LoopIsRefused(t *testing.T) {
	t.Parallel()

	env := newProxyEnv(t, usersUpstream)
	self := httptest.NewServer(env.h)
	t.Cleanup(self.Close)

	// The "upstream" is this very mock server.
	env.setProxy(t, self.URL+"/mock/"+env.projectID, domain.ProxyForward, false)

	done := make(chan *httptest.ResponseRecorder, 1)

	go func() { done <- env.get(t, "/users/7") }()

	select {
	case rec := <-done:
		if rec.Code != usecase.StatusLoopDetected {
			t.Fatalf("want 508, got %d: %s", rec.Code, rec.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request looped")
	}
}

func TestProxy_UnreachableUpstreamIs502(t *testing.T) {
	t.Parallel()

	env := newProxyEnv(t, usersUpstream)
	env.setProxy(t, "http://127.0.0.1:1", domain.ProxyForward, false)

	rec := env.get(t, "/users/7")
	if rec.Code != http.StatusBadGateway || rec.Header().Get("X-Mock-Source") != usecase.SourceProxyError {
		t.Errorf("want 502 proxy-error, got %d %q", rec.Code, rec.Header().Get("X-Mock-Source"))
	}
}

func TestProxy_RuleWinsOverUpstream(t *testing.T) {
	t.Parallel()

	env := newProxyEnv(t, usersUpstream)
	env.store.CreateMock(&domain.MockRule{
		ProjectID: env.projectID, Path: "/users/{id}", Method: "GET", StatusCode: 200,
		Body: `{"id":1,"name":"Mocked"}`,
	})

	rec := env.get(t, "/users/7")
	if !strings.Contains(rec.Body.String(), "Mocked") || env.hits.Load() != 0 {
		t.Errorf("a configured mock must answer without the upstream: %s hits=%d", rec.Body.String(), env.hits.Load())
	}
}

func TestDrift_ReportsStructuralDifferences(t *testing.T) {
	t.Parallel()

	env := newProxyEnv(t, usersUpstream)
	env.setProxy(t, env.upstreamURL(t), domain.ProxyOff, true)

	// The mock has an "age" and a numeric "id"...
	env.store.CreateMock(&domain.MockRule{
		ProjectID: env.projectID, Path: "/users/{id}", Method: "GET", StatusCode: 200,
		Body: `{"id":"7","name":"Ada","age":36}`,
	})

	rec := env.get(t, "/users/7")
	if rec.Header().Get("X-Mock-Source") != "rule" {
		t.Fatalf("the mock must answer: %q", rec.Header().Get("X-Mock-Source"))
	}

	env.serving.WaitDrift()

	reports := env.store.ListDrift(env.projectID, 10)
	if len(reports) != 1 || !reports[0].Drifted {
		t.Fatalf("want one drifted report, got %+v", reports)
	}

	kinds := map[string]string{}
	for _, d := range reports[0].Differences {
		kinds[d.Kind+":"+d.Path] = d.Mock + ">" + d.Live
	}

	for _, want := range []string{"type-mismatch:id", "missing-in-live:age", "extra-in-live:email"} {
		if _, ok := kinds[want]; !ok {
			t.Errorf("missing difference %s in %v", want, kinds)
		}
	}

	// ...and the same through the API.
	api := doRequest(t, env.h, http.MethodGet, "/api/projects/"+env.projectID+"/drift?limit=5", nil, nil)

	var listed []domain.DriftReport
	decodeJSON(t, api, &listed)

	if api.Code != http.StatusOK || len(listed) != 1 {
		t.Fatalf("drift API: %d %s", api.Code, api.Body.String())
	}

	if del := doRequest(t, env.h, http.MethodDelete, "/api/projects/"+env.projectID+"/drift", nil, nil); del.Code != http.StatusNoContent {
		t.Errorf("clear: %d", del.Code)
	}

	if left := env.store.ListDrift(env.projectID, 10); len(left) != 0 {
		t.Errorf("reports not cleared: %d", len(left))
	}
}

func TestDrift_MatchingShapeIsNotDriftAndUnsafeMethodsAreSkipped(t *testing.T) {
	t.Parallel()

	env := newProxyEnv(t, usersUpstream)
	env.setProxy(t, env.upstreamURL(t), domain.ProxyOff, true)
	env.store.CreateMock(&domain.MockRule{
		ProjectID: env.projectID, Path: "/users/{id}", Method: "GET", StatusCode: 200,
		Body: `{"id":1,"name":"Other","email":"x@y.z"}`,
	})
	env.store.CreateMock(&domain.MockRule{
		ProjectID: env.projectID, Path: "/users", Method: "POST", StatusCode: 201, Body: `{}`,
	})

	env.get(t, "/users/7")
	doRequest(t, env.h, http.MethodPost, "/mock/"+env.projectID+"/users", strings.NewReader(`{}`), nil)
	env.serving.WaitDrift()

	reports := env.store.ListDrift(env.projectID, 10)
	if len(reports) != 1 || reports[0].Drifted || reports[0].Method != http.MethodGet {
		t.Fatalf("want one clean GET report (POST is never replayed), got %+v", reports)
	}

	if n := env.hits.Load(); n != 1 {
		t.Errorf("only the GET may reach the upstream, got %d hits", n)
	}
}

func TestDrift_StatusAndContractAndUnreachable(t *testing.T) {
	t.Parallel()

	env := newProxyEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"nope"}`) // breaks the contract: id must be an integer, name is required.
	})
	env.setProxy(t, env.upstreamURL(t), domain.ProxyOff, true)
	env.store.CreateMock(&domain.MockRule{
		ProjectID: env.projectID, Path: "/users/{id}", Method: "GET", StatusCode: 200, Body: `{"id":1,"name":"A"}`,
	})

	env.get(t, "/users/7")
	env.serving.WaitDrift()

	diffs := env.store.ListDrift(env.projectID, 1)[0].Differences
	if !hasKind(diffs, domain.DriftContract) || !hasKind(diffs, domain.DriftType) || !hasKind(diffs, domain.DriftMissing) {
		t.Errorf("want contract, type-mismatch and missing differences, got %+v", diffs)
	}

	env.setProxy(t, "http://127.0.0.1:1", domain.ProxyOff, true)
	env.get(t, "/users/7")
	env.serving.WaitDrift()

	if diffs := env.store.ListDrift(env.projectID, 1)[0].Differences; !hasKind(diffs, domain.DriftUpstreamError) {
		t.Errorf("an unreachable upstream must be reported, got %+v", diffs)
	}
}

func hasKind(diffs []domain.DriftDifference, kind string) bool {
	for _, d := range diffs {
		if d.Kind == kind {
			return true
		}
	}

	return false
}

func TestProxy_SettingsAreValidated(t *testing.T) {
	t.Parallel()

	env := newProxyEnv(t, usersUpstream)
	patch := func(body string) *httptest.ResponseRecorder {
		return call(t, env.h, http.MethodPatch, "/api/projects/"+env.projectID, body)
	}

	for _, bad := range []string{
		`{"proxyMode":"mirror"}`,
		`{"upstreamUrl":"ftp://x.example"}`,
		`{"upstreamUrl":"http://user:pw@x.example"}`,
		`{"upstreamUrl":"","proxyMode":"record"}`,
	} {
		if rec := patch(bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", bad, rec.Code)
		}
	}

	rec := patch(`{"upstreamUrl":"https://api.example.com/v2","proxyMode":"forward","driftCheck":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid settings: %d %s", rec.Code, rec.Body.String())
	}

	var p map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}

	if p["upstreamUrl"] != "https://api.example.com/v2" || p["proxyMode"] != "forward" || p["driftCheck"] != true {
		t.Errorf("settings not saved: %v", p)
	}
}

func TestProxy_NonJSONRecordingsReplayAndStayEditable(t *testing.T) {
	t.Parallel()

	env := newProxyEnv(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "plain {{text}} not json")
	})

	env.get(t, "/users/7")

	replay := env.get(t, "/users/7")
	if replay.Header().Get("X-Mock-Source") != "rule" || replay.Body.String() != "plain {{text}} not json" ||
		!strings.HasPrefix(replay.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("replay: %q %q %q", replay.Header().Get("X-Mock-Source"), replay.Header().Get("Content-Type"), replay.Body.String())
	}

	// The UI saves a recorded rule back with PUT; that must keep working.
	rule := env.store.ListMocks(env.projectID)[0]
	rule.Body = "edited {{text}}"

	payload, err := json.Marshal(rule)
	if err != nil {
		t.Fatal(err)
	}

	if rec := call(t, env.h, http.MethodPut, "/api/mocks/"+rule.ID, string(payload)); rec.Code != http.StatusOK {
		t.Fatalf("editing a recorded rule: %d %s", rec.Code, rec.Body.String())
	}

	if got := env.get(t, "/users/7").Body.String(); got != "edited {{text}}" {
		t.Errorf("edit not served: %q", got)
	}

	// A hand-written (non-raw) rule must still be JSON.
	rule.Raw = false

	payload, _ = json.Marshal(rule)

	if rec := call(t, env.h, http.MethodPut, "/api/mocks/"+rule.ID, string(payload)); rec.Code == http.StatusOK {
		t.Errorf("a non-raw non-JSON body must be rejected, got %d", rec.Code)
	}
}
