package mcpcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const (
	defaultLogLimit = 20
	maxLogLimit     = 100
	// maxMatchesShown bounds the matching calls listed with a verification.
	maxMatchesShown = 10
)

var errBodyMatch = errors.New("give at most one of body_equals, body_contains, body_regex and body_json")

func (s *Server) observeTools() []*tool {
	return []*tool{
		newTool("get_logs", "Get the request log",
			"List the calls the mock received, newest first, with the status it answered and what served it "+
				"(a rule, the contract's example, request validation, an injected fault).", kindRead,
			object(props{
				"project": projectProp(),
				"limit":   number("How many calls (default 20, at most 100)."),
				"include_bodies": flag("Include the request and response bodies and headers " +
					"(cut at 2000 characters; secret headers are redacted)."),
			}), s.getLogs),
		newTool("verify_calls", "Verify calls to the mock",
			"Assert what the system under test sent to the mock: was a call made, with which query, headers and "+
				"body, and how many times. Every argument that is given must hold. The result says whether it "+
				"passed, and when nothing matched it shows the closest calls and what differed. Without a count "+
				"it asks for at least one call; exactly 0 means never.", kindRead,
			object(props{
				"project":       projectProp(),
				"method":        text("HTTP method."),
				"path":          text("Request path, for example /users/42. Segments written {name} or * match anything."),
				"endpoint":      text("The contract operation that served the call, for example /users/{id}."),
				"query":         stringMap("Query parameters that must be present with these values."),
				"headers":       stringMap("Request headers that must be present with these values."),
				"body_equals":   text("The request body must be exactly this text."),
				"body_contains": text("The request body must contain this text."),
				"body_regex":    text("The request body must match this Go regular expression."),
				"body_json": map[string]any{
					"description": "A JSON value the request body must contain: objects match when they hold at " +
						"least these properties, arrays when they have the same length and each element matches.",
				},
				"status":       number("The status the mock answered with."),
				"at_least":     number("At least this many matching calls."),
				"at_most":      number("At most this many matching calls."),
				"exactly":      number("Exactly this many matching calls (0 means never)."),
				"since":        text("Only calls at or after this time (RFC 3339)."),
				"since_log_id": text("Only calls after the log entry with this id."),
			}), s.verifyCalls),
		newTool("clear_calls", "Clear the recorded calls",
			"Forget the calls recorded so far, so the next verification starts from an empty record.", kindDestroy,
			object(props{"project": projectProp()}), s.clearCalls),
	}
}

type callView struct {
	ID         string    `json:"id"`
	Time       time.Time `json:"time"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Query      string    `json:"query,omitempty"`
	Status     int       `json:"status"`
	Endpoint   string    `json:"endpoint,omitempty"`
	Matched    bool      `json:"matched"`
	Source     string    `json:"source,omitempty"`
	Rule       string    `json:"rule,omitempty"`
	Fault      string    `json:"fault,omitempty"`
	DurationMs int64     `json:"duration_ms"`
	Violations []string  `json:"violations,omitempty"`

	RequestHeaders  map[string][]string `json:"request_headers,omitempty"`
	RequestBody     string              `json:"request_body,omitempty"`
	ResponseHeaders map[string][]string `json:"response_headers,omitempty"`
	ResponseBody    string              `json:"response_body,omitempty"`
}

func viewCall(l *domain.RequestLog, bodies bool) callView {
	v := callView{
		ID: l.ID, Time: l.Timestamp, Method: l.Method, Path: l.Path, Query: l.Query, Status: l.StatusCode,
		Endpoint: l.Endpoint, Matched: l.Matched, Source: l.Source, Rule: l.MatchedRule, Fault: l.Fault,
		DurationMs: l.DurationMs, Violations: l.Violations,
		RequestHeaders: nil, RequestBody: "", ResponseHeaders: nil, ResponseBody: "",
	}

	if !bodies {
		return v
	}

	if l.Request != nil {
		v.RequestHeaders, v.RequestBody = l.Request.Headers, clip(l.Request.Body)
	}

	if l.Response != nil {
		v.ResponseHeaders, v.ResponseBody = l.Response.Headers, clip(l.Response.Body)
	}

	return v
}

type getLogsArgs struct {
	Project       string `json:"project"`
	Limit         int    `json:"limit"`
	IncludeBodies bool   `json:"include_bodies"`
}

func (s *Server) getLogs(_ context.Context, a *getLogsArgs) (any, error) {
	p, err := s.project(a.Project)
	if err != nil {
		return nil, err
	}

	limit := a.Limit
	if limit <= 0 {
		limit = defaultLogLimit
	}

	limit = min(limit, maxLogLimit)

	calls, err := s.deps.Logs.Calls(p.ID, limit)
	if err != nil {
		return nil, fmt.Errorf("read the log: %w", err)
	}

	out := make([]callView, 0, len(calls))
	for _, c := range calls {
		out = append(out, viewCall(c, a.IncludeBodies))
	}

	return map[string]any{"project": p.ID, "calls": out}, nil
}

type verifyArgs struct {
	Project      string            `json:"project"`
	Method       string            `json:"method"`
	Path         string            `json:"path"`
	Endpoint     string            `json:"endpoint"`
	Query        map[string]string `json:"query"`
	Headers      map[string]string `json:"headers"`
	BodyEquals   *string           `json:"body_equals"`
	BodyContains *string           `json:"body_contains"`
	BodyRegex    *string           `json:"body_regex"`
	BodyJSON     json.RawMessage   `json:"body_json"`
	Status       *int              `json:"status"`
	AtLeast      *int              `json:"at_least"`
	AtMost       *int              `json:"at_most"`
	Exactly      *int              `json:"exactly"`
	Since        string            `json:"since"`
	SinceLogID   string            `json:"since_log_id"`
}

func (a *verifyArgs) request() (*usecase.VerifyRequest, error) {
	req := &usecase.VerifyRequest{
		Method: a.Method, Path: a.Path, Endpoint: a.Endpoint, Query: a.Query, Headers: a.Headers,
		Status: a.Status, AtLeast: a.AtLeast, AtMost: a.AtMost, Exactly: a.Exactly, SinceID: a.SinceLogID,
		Body: nil, Since: nil,
	}

	given := 0

	for _, set := range []bool{a.BodyEquals != nil, a.BodyContains != nil, a.BodyRegex != nil, len(a.BodyJSON) > 0} {
		if set {
			given++
		}
	}

	if given > 1 {
		return nil, errBodyMatch
	}

	if given == 1 {
		req.Body = &usecase.BodyMatch{Equals: a.BodyEquals, Contains: a.BodyContains, Regex: a.BodyRegex, JSON: a.BodyJSON}
	}

	if a.Since != "" {
		t, err := time.Parse(time.RFC3339, a.Since)
		if err != nil {
			return nil, fmt.Errorf("since must be an RFC 3339 time such as 2026-01-02T15:04:05Z: %w", err)
		}

		req.Since = &t
	}

	return req, nil
}

func (s *Server) verifyCalls(_ context.Context, a *verifyArgs) (any, error) {
	p, err := s.project(a.Project)
	if err != nil {
		return nil, err
	}

	req, err := a.request()
	if err != nil {
		return nil, err
	}

	res, err := s.deps.Logs.Verify(p.ID, req)
	if err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}

	out := map[string]any{
		"passed": res.Passed, "count": res.Count, "expected": res.Expected, "message": res.Message,
		"examined": res.Examined,
	}

	matches := make([]callView, 0, min(len(res.Matches), maxMatchesShown))
	for i, m := range res.Matches {
		if i == maxMatchesShown {
			break
		}

		matches = append(matches, viewCall(m, true))
	}

	out["matches"] = matches

	if len(res.Closest) > 0 {
		closest := make([]map[string]any, 0, len(res.Closest))
		for _, c := range res.Closest {
			closest = append(closest, map[string]any{"call": viewCall(c.Call, true), "differences": c.Mismatches})
		}

		out["closest"] = closest
	}

	return out, nil
}

func (s *Server) clearCalls(_ context.Context, a *projectArgs) (any, error) {
	p, err := s.project(a.Project)
	if err != nil {
		return nil, err
	}

	n, err := s.deps.Logs.ClearCalls(p.ID)
	if err != nil {
		return nil, fmt.Errorf("clear the calls: %w", err)
	}

	return map[string]any{"project": p.ID, "cleared": n}, nil
}
