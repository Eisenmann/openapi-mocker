package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest/report"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const (
	maxLoadBody = 1 << 20

	// Names of the run artifacts the engine writes (see loadtest/engine).
	artifactSummary = "summary.json"
	artifactSeries  = "timeseries.csv"

	reportJSON  = "json"
	reportHTML  = "html"
	reportCSV   = "csv"
	reportJUnit = "junit"

	// A report page is self-contained: inline style and script, no requests.
	reportCSP = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data:"
)

var (
	errLoadNotEnabled = errors.New("load testing is not enabled")
	errLoadRunRequest = errors.New("a run request needs a plan, or the planId of a stored plan")
	errReportFormat   = errors.New("format must be html, json, csv or junit")
	errNoReportYet    = errors.New("the run has no report yet")
	errLoadVersion    = errors.New("version must be a positive number")
)

func (a *api) registerLoadRoutes(mux *http.ServeMux) {
	const plans = "/api/projects/{id}/load/plans"

	const runs = "/api/projects/{id}/load/runs"

	mux.HandleFunc("GET "+plans, a.listLoadPlans)
	mux.HandleFunc("POST "+plans, a.createLoadPlan)
	mux.HandleFunc("GET "+plans+"/{planId}", a.getLoadPlan)
	mux.HandleFunc("PUT "+plans+"/{planId}", a.updateLoadPlan)
	mux.HandleFunc("DELETE "+plans+"/{planId}", a.deleteLoadPlan)
	mux.HandleFunc("GET "+plans+"/{planId}/versions", a.listLoadPlanVersions)

	mux.HandleFunc("GET "+runs, a.listLoadRuns)
	mux.HandleFunc("POST "+runs, a.startLoadRun)
	mux.HandleFunc("GET "+runs+"/{runId}", a.getLoadRun)
	mux.HandleFunc("DELETE "+runs+"/{runId}", a.deleteLoadRun)
	mux.HandleFunc("POST "+runs+"/{runId}/abort", a.abortLoadRun)
	mux.HandleFunc("GET "+runs+"/{runId}/report", a.loadRunReport)
}

// loadReady answers 404 and returns false when load testing is not wired.
func (a *api) loadReady(w http.ResponseWriter) bool {
	if a.s.LoadPlans == nil || a.s.LoadRuns == nil {
		writeError(w, http.StatusNotFound, errLoadNotEnabled)

		return false
	}

	return true
}

// writeLoadError maps the errors of the load services to status codes.
func writeLoadError(w http.ResponseWriter, err error) {
	status := http.StatusUnprocessableEntity

	switch {
	case errors.Is(err, usecase.ErrLoadTargetRole), errors.Is(err, usecase.ErrLoadTargetHost),
		errors.Is(err, usecase.ErrLoadTargetServerHost), errors.Is(err, usecase.ErrLoadTargetInternal):
		status = http.StatusForbidden
	case errors.Is(err, domain.ErrLoadRunActive), errors.Is(err, domain.ErrLoadRunNotLive):
		status = http.StatusConflict
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrLoadRunNotFound):
		status = http.StatusNotFound
	case errors.Is(err, domain.ErrLoadPlanID):
		status = http.StatusBadRequest
	}

	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// loadActor is who made the request, for plan and run records.
func loadActor(r *http.Request) string {
	p := principalOf(r)
	if p == nil {
		return ""
	}

	if p.Actor.Name != "" {
		return p.Actor.Name
	}

	return p.Actor.Kind
}

// loadRole is the caller's role; without access control a caller may run
// against the project's own mock but not against external targets.
func loadRole(r *http.Request) domain.Role {
	p := principalOf(r)
	if p == nil {
		return domain.RoleEditor
	}

	return p.Role
}

func readLimitedBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	defer r.Body.Close()

	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLoadBody))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}

	return data, nil
}

// --- plans ---.

// planBody reads a plan file (YAML or JSON) from the request body.
func planBody(w http.ResponseWriter, r *http.Request) (*domain.Plan, int, error) {
	data, err := readLimitedBody(w, r)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}

	plan, err := usecase.ParsePlan(data)
	if err != nil {
		return nil, http.StatusUnprocessableEntity, err
	}

	return plan, 0, nil
}

func (a *api) createLoadPlan(w http.ResponseWriter, r *http.Request) {
	if !a.loadReady(w) {
		return
	}

	plan, status, err := planBody(w, r)
	if err != nil {
		writeError(w, status, err)

		return
	}

	p, err := a.s.LoadPlans.Create(r.Context(), r.PathValue("id"), plan, loadActor(r))
	if err != nil {
		writeLoadError(w, err)

		return
	}

	setAuditNote(r, fmt.Sprintf("%s version %d", p.ID, p.Version))
	writeJSON(w, http.StatusCreated, p)
}

func (a *api) updateLoadPlan(w http.ResponseWriter, r *http.Request) {
	if !a.loadReady(w) {
		return
	}

	plan, status, err := planBody(w, r)
	if err != nil {
		writeError(w, status, err)

		return
	}

	p, err := a.s.LoadPlans.Update(r.Context(), r.PathValue("id"), r.PathValue("planId"), plan, loadActor(r))
	if err != nil {
		writeLoadError(w, err)

		return
	}

	setAuditNote(r, fmt.Sprintf("version %d", p.Version))
	writeJSON(w, http.StatusOK, p)
}

func (a *api) listLoadPlans(w http.ResponseWriter, r *http.Request) {
	if !a.loadReady(w) {
		return
	}

	plans, err := a.s.LoadPlans.List(r.Context(), r.PathValue("id"))
	if err != nil {
		writeLoadError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, plans)
}

// getLoadPlan returns a plan as JSON, or with ?format=yaml (or json-file) as
// the plan file that "load run" and the editor read.
func (a *api) getLoadPlan(w http.ResponseWriter, r *http.Request) {
	if !a.loadReady(w) {
		return
	}

	version := 0

	if v := r.URL.Query().Get("version"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, errLoadVersion)

			return
		}

		version = n
	}

	p, err := a.s.LoadPlans.Get(r.Context(), r.PathValue("id"), r.PathValue("planId"), version)
	if err != nil {
		writeLoadError(w, err)

		return
	}

	if r.URL.Query().Get("format") == "yaml" {
		writePlanFile(w, p)

		return
	}

	writeJSON(w, http.StatusOK, p)
}

func writePlanFile(w http.ResponseWriter, p *domain.StoredPlan) {
	data, err := usecase.FormatPlanYAML(&p.Plan)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)

		return
	}

	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (a *api) listLoadPlanVersions(w http.ResponseWriter, r *http.Request) {
	if !a.loadReady(w) {
		return
	}

	vs, err := a.s.LoadPlans.Versions(r.Context(), r.PathValue("id"), r.PathValue("planId"))
	if err != nil {
		writeLoadError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, vs)
}

func (a *api) deleteLoadPlan(w http.ResponseWriter, r *http.Request) {
	if !a.loadReady(w) {
		return
	}

	err := a.s.LoadPlans.Delete(r.Context(), r.PathValue("id"), r.PathValue("planId"))
	if err != nil {
		writeLoadError(w, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- runs ---.

// runRequest is a start request: a stored plan by id, or a plan inline (as a
// plan file in a string, or as a JSON plan object).
type runRequest struct {
	PlanID  string          `json:"planId"`
	Version int             `json:"version"`
	Plan    json.RawMessage `json:"plan"`
}

// runPlan is the plan a start request refers to.
type runPlan struct {
	plan    *domain.Plan
	planID  string
	version int
}

// planForRun resolves a start request. The body is either {"planId": ...,
// "version": ...}, {"plan": <plan file as a string or object>}, or a plan
// file itself.
func (a *api) planForRun(w http.ResponseWriter, r *http.Request, projectID string) (*runPlan, int, error) {
	data, err := readLimitedBody(w, r)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}

	var req runRequest

	_ = json.Unmarshal(data, &req) // a YAML plan file is not JSON; it is read below.

	switch {
	case req.PlanID != "":
		p, getErr := a.s.LoadPlans.Get(r.Context(), projectID, req.PlanID, req.Version)
		if getErr != nil {
			return nil, statusOfLoadError(getErr), getErr
		}

		return &runPlan{plan: &p.Plan, planID: p.ID, version: p.Version}, 0, nil
	case len(req.Plan) > 0:
		return parseInlinePlan(req.Plan)
	case strings.TrimSpace(string(data)) == "":
		return nil, http.StatusBadRequest, errLoadRunRequest
	default:
		return parseInlinePlan(data)
	}
}

// parseInlinePlan reads a plan file given as a JSON string or as raw bytes.
func parseInlinePlan(raw []byte) (*runPlan, int, error) {
	text := raw

	var s string
	if json.Unmarshal(raw, &s) == nil {
		text = []byte(s)
	}

	plan, err := usecase.ParsePlan(text)
	if err != nil {
		return nil, http.StatusUnprocessableEntity, err
	}

	return &runPlan{plan: plan, planID: "", version: 0}, 0, nil
}

func statusOfLoadError(err error) int {
	if errors.Is(err, domain.ErrNotFound) {
		return http.StatusNotFound
	}

	return http.StatusUnprocessableEntity
}

func (a *api) startLoadRun(w http.ResponseWriter, r *http.Request) {
	if !a.loadReady(w) {
		return
	}

	projectID := r.PathValue("id")

	rp, status, err := a.planForRun(w, r, projectID)
	if err != nil {
		writeError(w, status, err)

		return
	}

	run, err := a.s.LoadRuns.Start(r.Context(), &usecase.StartLoadRun{
		ProjectID: projectID, Plan: rp.plan, PlanID: rp.planID, PlanVersion: rp.version,
		Role: loadRole(r), Actor: loadActor(r),
	})
	if err != nil {
		writeLoadError(w, err)

		return
	}

	setAuditNote(r, fmt.Sprintf("run %s of %q against %s", run.ID, run.Plan.Name, run.Target))
	w.Header().Set("Location", "/api/projects/"+projectID+"/load/runs/"+run.ID)
	writeJSON(w, http.StatusAccepted, run)
}

// loadRunView is a run with its live progress while it is active.
type loadRunView struct {
	domain.LoadRun

	Progress *usecase.LoadProgress `json:"progress,omitempty"`
}

func (a *api) getLoadRun(w http.ResponseWriter, r *http.Request) {
	if !a.loadReady(w) {
		return
	}

	run, err := a.s.LoadRuns.Get(r.Context(), r.PathValue("id"), r.PathValue("runId"))
	if err != nil {
		writeLoadError(w, err)

		return
	}

	view := loadRunView{LoadRun: *run, Progress: nil}

	if p, live := a.s.LoadRuns.Progress(run.ID); live {
		view.Progress = &p
	}

	writeJSON(w, http.StatusOK, view)
}

func (a *api) listLoadRuns(w http.ResponseWriter, r *http.Request) {
	if !a.loadReady(w) {
		return
	}

	runs, err := a.s.LoadRuns.List(r.Context(), r.PathValue("id"))
	if err != nil {
		writeLoadError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, runs)
}

func (a *api) abortLoadRun(w http.ResponseWriter, r *http.Request) {
	if !a.loadReady(w) {
		return
	}

	err := a.s.LoadRuns.Abort(r.Context(), r.PathValue("id"), r.PathValue("runId"))
	if err != nil {
		writeLoadError(w, err)

		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func (a *api) deleteLoadRun(w http.ResponseWriter, r *http.Request) {
	if !a.loadReady(w) {
		return
	}

	err := a.s.LoadRuns.Delete(r.Context(), r.PathValue("id"), r.PathValue("runId"))
	if err != nil {
		writeLoadError(w, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- reports ---.

// loadRunReport serves the report of a finished run: json (default), html,
// csv (the time series) or junit.
func (a *api) loadRunReport(w http.ResponseWriter, r *http.Request) {
	if !a.loadReady(w) {
		return
	}

	format, name, err := reportSource(r.URL.Query().Get("format"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)

		return
	}

	data, err := a.s.LoadRuns.Artifact(r.Context(), r.PathValue("id"), r.PathValue("runId"), name)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": errNoReportYet.Error()})

			return
		}

		writeLoadError(w, err)

		return
	}

	contentType, body, err := renderLoadReport(format, data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)

		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if format == reportHTML {
		w.Header().Set("Content-Security-Policy", reportCSP)
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// reportSource validates the requested format and names the artifact it is
// rendered from.
func reportSource(format string) (resolved, artifact string, err error) {
	switch format {
	case "", reportJSON:
		return reportJSON, artifactSummary, nil
	case reportHTML, reportJUnit:
		return format, artifactSummary, nil
	case reportCSV:
		return format, artifactSeries, nil
	default:
		return "", "", errReportFormat
	}
}

// renderLoadReport turns the stored summary into the requested format.
func renderLoadReport(format string, stored []byte) (contentType string, body []byte, err error) {
	switch format {
	case reportJSON:
		return "application/json", stored, nil
	case reportCSV:
		return "text/csv; charset=utf-8", stored, nil
	}

	rep := new(report.Report)

	err = json.Unmarshal(stored, rep)
	if err != nil {
		return "", nil, fmt.Errorf("read report: %w", err)
	}

	if format == reportJUnit {
		body, err = report.JUnit(rep)

		return "application/xml", body, err //nolint:wrapcheck // the report package words its errors.
	}

	body, err = report.HTML(rep)

	return "text/html; charset=utf-8", body, err //nolint:wrapcheck // the report package words its errors.
}
