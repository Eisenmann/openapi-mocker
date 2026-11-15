package loadtest_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

type sink struct {
	mu  sync.Mutex
	got []*loadtest.Result
}

func (s *sink) add(r *loadtest.Result) {
	s.mu.Lock()
	s.got = append(s.got, r)
	s.mu.Unlock()
}

type fakeValidator struct {
	viol    []usecase.ContractViolation
	checked bool
}

func (f fakeValidator) ValidateResponse(string, string, int, string, []byte) ([]usecase.ContractViolation, bool) {
	return f.viol, f.checked
}

func runWorker(t *testing.T, srvURL string, steps []domain.Step, val usecase.ResponseValidator, open bool) []*loadtest.Result {
	t.Helper()

	base, _ := url.Parse(srvURL)
	b, err := loadtest.NewBuilder(&loadtest.BuilderConfig{
		Target: base, ProjectID: "p1", Engine: usecase.NewTemplateEngine(), Operations: testOps(),
	})
	if err != nil {
		t.Fatal(err)
	}

	plan := new(domain.Plan)
	plan.Load.Kind = domain.LoadClosed
	if open {
		plan.Load.Kind = domain.LoadOpen
	}

	plan.Scenarios = []domain.Scenario{{Name: "sc", Weight: 1, Steps: steps}}

	s := new(sink)
	w := loadtest.NewWorker(&loadtest.WorkerConfig{
		Plan: plan, Client: &http.Client{Timeout: 300 * time.Millisecond}, Builder: b, Sink: s.add, Validator: val,
		SlowDetailAbove: 20 * time.Millisecond,
	})

	info := loadtest.IterationInfo{Intended: time.Now()}
	w.Iteration()(context.Background(), info)

	return s.got
}

func TestWorker_StatusClassesAndChecks(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":7,"tags":["a","b"],"name":"rex"}`))
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	steps := []domain.Step{
		{Method: "GET", Path: "/ok", Checks: []domain.Check{
			{Kind: domain.CheckStatus, Statuses: []int{200}},
			{Kind: domain.CheckJSONPath, Path: "id", Op: domain.CheckEquals, Value: 7},
			{Kind: domain.CheckJSONPath, Path: "tags", Op: domain.CheckContains, Value: "b"},
			{Kind: domain.CheckJSONPath, Path: "nope", Op: domain.CheckExists},
		}},
		{Method: "GET", Path: "/missing"},
		{Method: "GET", Path: "/missing", Checks: []domain.Check{{Kind: domain.CheckStatus, Statuses: []int{404}}}},
		{Method: "GET", Path: "/boom"},
	}

	res := runWorker(t, srv.URL, steps, nil, false)
	if len(res) != 4 {
		t.Fatalf("results = %d", len(res))
	}

	if res[0].ErrorClass != "" || res[0].ChecksPassed != 3 || res[0].ChecksFailed != 1 || res[0].Detail == nil {
		t.Errorf("ok step: %+v", res[0])
	}

	if res[1].ErrorClass != loadtest.ClassHTTP4xx || res[1].Status != 404 {
		t.Errorf("404 unexpected: %+v", res[1])
	}

	if res[2].ErrorClass != "" || res[2].ChecksPassed != 1 {
		t.Errorf("404 expected by check: %+v", res[2])
	}

	if res[3].ErrorClass != loadtest.ClassHTTP5xx || res[3].Detail == nil {
		t.Errorf("500: %+v", res[3])
	}

	// A fast loopback answer may measure 0 on a coarse clock (Windows).
	if res[0].Scenario != "sc" || res[0].BytesIn == 0 || res[0].Latency < 0 {
		t.Errorf("fields: %+v", res[0])
	}
}

func TestWorker_ExtractFeedsNextStep(t *testing.T) {
	t.Parallel()

	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/first" {
			_, _ = w.Write([]byte(`{"id":"abc"}`))

			return
		}

		gotPath = r.URL.Path
	}))
	defer srv.Close()

	steps := []domain.Step{
		{Method: "GET", Path: "/first", Extracts: []domain.Extract{{Var: "id", Path: "id"}, {Var: "x", Path: "gone"}}},
		{Method: "GET", Path: "/item/{{vars.id}}"},
	}

	res := runWorker(t, srv.URL, steps, nil, false)

	if gotPath != "/item/abc" {
		t.Errorf("second request path = %q", gotPath)
	}

	if res[0].ChecksFailed != 1 {
		t.Errorf("missing extract should count as a failed check: %+v", res[0])
	}
}

func TestWorker_Contract(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer srv.Close()

	steps := []domain.Step{{Method: "GET", Path: "/x", Checks: []domain.Check{{Kind: domain.CheckContract}}}}

	bad := fakeValidator{checked: true, viol: []usecase.ContractViolation{{Path: "/id", Message: "required"}}}

	res := runWorker(t, srv.URL, steps, bad, false)
	if res[0].ContractViolations != 1 || res[0].ChecksFailed != 1 || res[0].Detail == nil {
		t.Errorf("violation: %+v", res[0])
	}

	skipped := runWorker(t, srv.URL, steps, fakeValidator{}, false)
	if skipped[0].ChecksPassed != 0 || skipped[0].ChecksFailed != 0 {
		t.Errorf("skipped check must not count: %+v", skipped[0])
	}
}

func TestWorker_TransportErrors(t *testing.T) {
	t.Parallel()

	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()

	if r := runWorker(t, slow.URL, []domain.Step{{Method: "GET", Path: "/"}}, nil, false); r[0].ErrorClass != loadtest.ClassTimeout {
		t.Errorf("timeout: %+v", r[0])
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	addr := dead.URL
	dead.Close()

	if r := runWorker(t, addr, []domain.Step{{Method: "GET", Path: "/"}}, nil, false); r[0].ErrorClass != loadtest.ClassRefused {
		t.Errorf("refused: %+v", r[0])
	}

	reset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		c, _, _ := w.(http.Hijacker).Hijack()
		_ = c.Close()
	}))
	defer reset.Close()

	if r := runWorker(t, reset.URL, []domain.Step{{Method: "GET", Path: "/"}}, nil, false); r[0].ErrorClass != loadtest.ClassReset {
		t.Errorf("reset: %+v", r[0])
	}

	tlsSrv := httptest.NewTLSServer(http.NotFoundHandler())
	defer tlsSrv.Close()

	if r := runWorker(t, tlsSrv.URL, []domain.Step{{Method: "GET", Path: "/"}}, nil, false); r[0].ErrorClass != loadtest.ClassTLS {
		t.Errorf("tls: %+v", r[0])
	}
}

func TestWorker_OpenModelMeasuresFromIntendedStart(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	base, _ := url.Parse(srv.URL)
	b, _ := loadtest.NewBuilder(&loadtest.BuilderConfig{Target: base, ProjectID: "p1", Engine: usecase.NewTemplateEngine()})

	plan := new(domain.Plan)
	plan.Load.Kind = domain.LoadOpen
	plan.Scenarios = []domain.Scenario{{Name: "sc", Weight: 1, Steps: []domain.Step{{Method: "GET", Path: "/"}}}}

	s := new(sink)
	w := loadtest.NewWorker(&loadtest.WorkerConfig{Plan: plan, Client: http.DefaultClient, Builder: b, Sink: s.add})
	w.Iteration()(context.Background(), loadtest.IterationInfo{Intended: time.Now().Add(-500 * time.Millisecond)})

	if s.got[0].Latency < 500*time.Millisecond {
		t.Errorf("latency %v must include the queueing delay", s.got[0].Latency)
	}
}

func TestWorker_CancelledRunRecordsNothing(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer srv.Close()

	base, _ := url.Parse(srv.URL)
	b, _ := loadtest.NewBuilder(&loadtest.BuilderConfig{Target: base, ProjectID: "p1", Engine: usecase.NewTemplateEngine()})

	plan := new(domain.Plan)
	plan.Load.Kind = domain.LoadClosed
	plan.Scenarios = []domain.Scenario{{Name: "sc", Weight: 1, Steps: []domain.Step{{Method: "GET", Path: "/"}}}}

	s := new(sink)
	w := loadtest.NewWorker(&loadtest.WorkerConfig{Plan: plan, Client: http.DefaultClient, Builder: b, Sink: s.add})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	w.Iteration()(ctx, loadtest.IterationInfo{Intended: time.Now()})

	if len(s.got) != 0 {
		t.Errorf("cancelled request recorded: %+v", s.got[0])
	}
}

func TestWorker_FeederExhaustedEndsIteration(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	base, _ := url.Parse(srv.URL)
	f, _ := loadtest.NewFeeder("u", domain.FeederUnique, []map[string]any{{"a": "1"}})
	b, _ := loadtest.NewBuilder(&loadtest.BuilderConfig{
		Target: base, ProjectID: "p1", Engine: usecase.NewTemplateEngine(), Feeders: map[string]*loadtest.Feeder{"u": f},
	})

	plan := new(domain.Plan)
	plan.Load.Kind = domain.LoadClosed
	plan.Scenarios = []domain.Scenario{{Name: "sc", Weight: 1, Steps: []domain.Step{{Method: "GET", Path: "/{{data.u.a}}"}}}}

	s := new(sink)
	w := loadtest.NewWorker(&loadtest.WorkerConfig{Plan: plan, Client: http.DefaultClient, Builder: b, Sink: s.add})

	for range 3 {
		w.Iteration()(context.Background(), loadtest.IterationInfo{Intended: time.Now()})
	}

	if len(s.got) != 1 || !w.FeederExhausted() {
		t.Errorf("results %d, exhausted %v", len(s.got), w.FeederExhausted())
	}
}

func TestWorker_Think(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	think := &domain.Think{Kind: domain.ThinkConstant, Duration: domain.Duration(80 * time.Millisecond)}
	steps := []domain.Step{{Method: "GET", Path: "/a", Think: think}, {Method: "GET", Path: "/b", Think: think}}

	start := time.Now()
	res := runWorker(t, srv.URL, steps, nil, false)

	if el := time.Since(start); el < 80*time.Millisecond || el > 160*time.Millisecond || len(res) != 2 {
		t.Errorf("think: elapsed %v (no pause after the last step), results %d", el, len(res))
	}
}
