package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/loadstore"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest/report"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const (
	loadPlanFile = `{"name":"api test","target":{"baseUrl":"mock://p1"},
"scenarios":{"s":{"weight":1,"steps":["GET /ping"]}},
"load":{"model":"iterations","iterations":1,"vus":1}}`
	loadSlowPlanFile = `{"name":"slow","target":{"baseUrl":"mock://p1"},
"scenarios":{"s":{"weight":1,"steps":["GET /ping"]}},
"load":{"model":"iterations","iterations":1,"vus":1}}`
	loadExternalPlanFile = `{"name":"ext","target":{"baseUrl":"https://staging.example.com","allowHosts":["staging.example.com"]},
"scenarios":{"s":{"weight":1,"steps":["GET /ping"]}},
"load":{"model":"iterations","iterations":1,"vus":1}}`
)

// loadFakeEngine finishes at once, or when cancelled for the plan named slow.
type loadFakeEngine struct{}

func (loadFakeEngine) Execute(ctx context.Context, in *usecase.LoadExecution) (*usecase.LoadOutcome, error) {
	if in.Plan.Name == "slow" {
		<-ctx.Done()
	}

	rec := new(domain.LoadRun)
	rec.ID = in.RunID
	rec.ProjectID = in.ProjectID
	rec.Plan = *in.Plan
	rec.Target = in.Target.BaseURL.String()

	rep := report.Build(&report.Input{Run: rec})

	summary, err := report.JSON(rep)
	if err != nil {
		return nil, err
	}

	series, err := report.CSV(rep)
	if err != nil {
		return nil, err
	}

	out := new(usecase.LoadOutcome)
	out.Totals = &domain.LoadTotals{Requests: 1}
	out.Artifacts = map[string][]byte{"summary.json": summary, "timeseries.csv": series}

	return out, nil
}

func newLoadHandler(t *testing.T) http.Handler {
	t.Helper()

	store, err := loadstore.New(t.TempDir(), &loadstore.Options{})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runs, err := usecase.NewLoadRunService(ctx, &usecase.LoadRunDeps{
		Engine: loadFakeEngine{}, Store: store, MockBase: "http://127.0.0.1:1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Runs save their result after they end; wait for that before the temp dirs go.
	t.Cleanup(func() { _ = runs.Shutdown(context.Background()) })

	return httpapi.NewRouter(&httpapi.Services{LoadPlans: usecase.NewLoadPlanService(store), LoadRuns: runs})
}

func loadDo(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func loadJSON(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()

	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

const loadBase = "/api/projects/p1/load"

func TestLoadAPI_PlanLifecycle(t *testing.T) {
	t.Parallel()

	h := newLoadHandler(t)

	rec := loadDo(t, h, http.MethodPost, loadBase+"/plans", loadPlanFile)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	var p domain.StoredPlan

	loadJSON(t, rec, &p)

	if p.Version != 1 || p.ID == "" || p.Name != "api test" {
		t.Fatalf("created plan: %+v", p)
	}

	url := loadBase + "/plans/" + p.ID

	if rec := loadDo(t, h, http.MethodPut, url, loadPlanFile); rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}

	var latest domain.StoredPlan

	loadJSON(t, loadDo(t, h, http.MethodGet, url, ""), &latest)

	if latest.Version != 2 || latest.Latest != 2 {
		t.Errorf("latest: %+v", latest)
	}

	var first domain.StoredPlan

	loadJSON(t, loadDo(t, h, http.MethodGet, url+"?version=1", ""), &first)

	if first.Version != 1 || first.Latest != 2 {
		t.Errorf("version 1: %+v", first)
	}

	if rec := loadDo(t, h, http.MethodGet, url+"?version=x", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad version: %d", rec.Code)
	}

	if rec := loadDo(t, h, http.MethodGet, url+"?format=yaml", ""); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "api test") {
		t.Errorf("plan file: %d %s", rec.Code, rec.Body.String())
	}

	var versions []domain.StoredPlan

	loadJSON(t, loadDo(t, h, http.MethodGet, url+"/versions", ""), &versions)

	if len(versions) != 2 {
		t.Errorf("versions: %d", len(versions))
	}

	var list []domain.StoredPlan

	loadJSON(t, loadDo(t, h, http.MethodGet, loadBase+"/plans", ""), &list)

	if len(list) != 1 {
		t.Errorf("list: %d", len(list))
	}

	if rec := loadDo(t, h, http.MethodDelete, url, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}

	if rec := loadDo(t, h, http.MethodGet, url, ""); rec.Code != http.StatusNotFound {
		t.Errorf("deleted plan: %d", rec.Code)
	}
}

func TestLoadAPI_InvalidPlanIsRefused(t *testing.T) {
	t.Parallel()

	h := newLoadHandler(t)

	for _, body := range []string{`{"name":"x"}`, `not a plan: [`, ``} {
		rec := loadDo(t, h, http.MethodPost, loadBase+"/plans", body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%q: %d %s", body, rec.Code, rec.Body.String())
		}
	}
}

func waitLoadRun(t *testing.T, h http.Handler, url string) domain.LoadRun {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		var run domain.LoadRun

		loadJSON(t, loadDo(t, h, http.MethodGet, url, ""), &run)

		if run.State != domain.LoadRunPending && run.State != domain.LoadRunRunning {
			return run
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("the run did not end")

	return domain.LoadRun{}
}

func TestLoadAPI_RunAndReports(t *testing.T) {
	t.Parallel()

	h := newLoadHandler(t)

	var plan domain.StoredPlan

	loadJSON(t, loadDo(t, h, http.MethodPost, loadBase+"/plans", loadPlanFile), &plan)

	rec := loadDo(t, h, http.MethodPost, loadBase+"/runs", `{"planId":"`+plan.ID+`"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}

	var started domain.LoadRun

	loadJSON(t, rec, &started)

	url := loadBase + "/runs/" + started.ID
	if rec.Header().Get("Location") != "/api/projects/p1"+"/load/runs/"+started.ID || started.PlanID != plan.ID ||
		started.PlanVersion != 1 {
		t.Errorf("accepted run: %v %+v", rec.Header(), started)
	}

	run := waitLoadRun(t, h, url)
	if run.State != domain.LoadRunFinished || run.Verdict != domain.VerdictPass {
		t.Fatalf("run: %+v", run)
	}

	for format, want := range map[string]string{
		"": "application/json", "json": "application/json", "html": "text/html",
		"junit": "application/xml", "csv": "text/csv",
	} {
		rec := loadDo(t, h, http.MethodGet, url+"/report?format="+format, "")
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), want) {
			t.Errorf("report %q: %d %s", format, rec.Code, rec.Header().Get("Content-Type"))
		}
	}

	html := loadDo(t, h, http.MethodGet, url+"/report?format=html", "")
	if !strings.Contains(html.Body.String(), "<html") || !strings.Contains(html.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("html report must be a page with a restrictive CSP")
	}

	if rec := loadDo(t, h, http.MethodGet, url+"/report?format=pdf", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown format: %d", rec.Code)
	}

	var list []domain.LoadRun

	loadJSON(t, loadDo(t, h, http.MethodGet, loadBase+"/runs", ""), &list)

	if len(list) != 1 {
		t.Errorf("run list: %d", len(list))
	}

	if rec := loadDo(t, h, http.MethodDelete, url, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete run: %d", rec.Code)
	}

	if rec := loadDo(t, h, http.MethodGet, url, ""); rec.Code != http.StatusNotFound {
		t.Errorf("deleted run: %d", rec.Code)
	}

	if rec := loadDo(t, h, http.MethodGet, url+"/report", ""); rec.Code != http.StatusNotFound {
		t.Errorf("report of a deleted run: %d", rec.Code)
	}
}

func TestLoadAPI_InlinePlanForms(t *testing.T) {
	t.Parallel()

	h := newLoadHandler(t)

	quoted, _ := json.Marshal(loadPlanFile)

	for name, body := range map[string]string{
		"plan file":         loadPlanFile,
		"plan as a string":  `{"plan":` + string(quoted) + `}`,
		"plan as an object": `{"plan":` + loadPlanFile + `}`,
	} {
		rec := loadDo(t, h, http.MethodPost, loadBase+"/runs", body)
		if rec.Code != http.StatusAccepted {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())

			continue
		}

		var run domain.LoadRun

		loadJSON(t, rec, &run)
		waitLoadRun(t, h, loadBase+"/runs/"+run.ID)
	}

	if rec := loadDo(t, h, http.MethodPost, loadBase+"/runs", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("empty body: %d", rec.Code)
	}

	if rec := loadDo(t, h, http.MethodPost, loadBase+"/runs", `{"planId":"plan_none"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown plan: %d", rec.Code)
	}
}

func TestLoadAPI_ExternalTargetNeedsTheRole(t *testing.T) {
	t.Parallel()

	h := newLoadHandler(t)

	// Without access control the caller has the editor role: mock targets only.
	rec := loadDo(t, h, http.MethodPost, loadBase+"/runs", loadExternalPlanFile)
	if rec.Code != http.StatusForbidden {
		t.Errorf("external target as editor: %d %s", rec.Code, rec.Body.String())
	}

	// A mock target of another project is refused.
	other := strings.Replace(loadPlanFile, "mock://p1", "mock://p2", 1)
	if rec := loadDo(t, h, http.MethodPost, loadBase+"/runs", other); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("foreign mock target: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLoadAPI_AbortAndActiveRules(t *testing.T) {
	t.Parallel()

	h := newLoadHandler(t)

	rec := loadDo(t, h, http.MethodPost, loadBase+"/runs", loadSlowPlanFile)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}

	var run domain.LoadRun

	loadJSON(t, rec, &run)

	url := loadBase + "/runs/" + run.ID

	if rec := loadDo(t, h, http.MethodPost, loadBase+"/runs", loadSlowPlanFile); rec.Code != http.StatusConflict {
		t.Errorf("a second run of the project: %d", rec.Code)
	}

	if rec := loadDo(t, h, http.MethodDelete, url, ""); rec.Code != http.StatusConflict {
		t.Errorf("delete of an active run: %d", rec.Code)
	}

	if rec := loadDo(t, h, http.MethodPost, url+"/abort", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("abort: %d %s", rec.Code, rec.Body.String())
	}

	ended := waitLoadRun(t, h, url)
	if ended.State != domain.LoadRunAborted || ended.Verdict != domain.VerdictFail || ended.AbortReason == "" {
		t.Errorf("aborted run: %+v", ended)
	}

	if rec := loadDo(t, h, http.MethodPost, url+"/abort", ""); rec.Code != http.StatusConflict {
		t.Errorf("abort of an ended run: %d", rec.Code)
	}

	if rec := loadDo(t, h, http.MethodPost, loadBase+"/runs/run_none/abort", ""); rec.Code != http.StatusNotFound {
		t.Errorf("abort of an unknown run: %d", rec.Code)
	}
}

func TestLoadAPI_NotEnabled(t *testing.T) {
	t.Parallel()

	h := httpapi.NewRouter(&httpapi.Services{})

	for _, path := range []string{"/plans", "/runs"} {
		if rec := loadDo(t, h, http.MethodGet, loadBase+path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
}
