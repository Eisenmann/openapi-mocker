package mcpcontrol

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

var (
	errRuleNeeded   = errors.New("rule_id is required")
	errRuleNotFound = errors.New("no mock rule with that id")
	errMethodPath   = errors.New("method and path are required")
)

// faultArgs is the fault part of a rule, as a tool takes and returns it.
type faultArgs struct {
	JitterMs         int    `json:"jitter_ms,omitempty"`
	ErrorStatuses    []int  `json:"error_statuses,omitempty"`
	TimeoutRatePct   int    `json:"timeout_rate_pct,omitempty"`
	TimeoutMs        int    `json:"timeout_ms,omitempty"`
	DropRatePct      int    `json:"drop_rate_pct,omitempty"`
	MalformedRatePct int    `json:"malformed_rate_pct,omitempty"`
	MalformedMode    string `json:"malformed_mode,omitempty"`
}

func faultSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"description": "Fault injection. Each request rolls once: dropped connection, then timeout, then injected " +
			"error (the rule's fail_rate_pct), then malformed body; the rest is a normal response. The rates add up " +
			"to at most 100.",
		"properties": props{
			"jitter_ms":          number("Random 0..jitter_ms added on top of delay_ms."),
			"error_statuses":     numbers("Status codes for injected errors (400-599); default a random 500-503."),
			"timeout_rate_pct":   number("Percent of requests that are never answered."),
			"timeout_ms":         number("How long a timeout holds the connection (default 30000)."),
			"drop_rate_pct":      number("Percent of requests whose connection is reset with no response."),
			"malformed_rate_pct": number("Percent of requests answered with a broken body."),
			"malformed_mode": choice("How the body is broken: garbage (default), truncated or empty.",
				"garbage", "truncated", "empty"),
		},
	}
}

func (f *faultArgs) domain() *domain.Fault {
	return &domain.Fault{
		JitterMs: f.JitterMs, ErrorStatuses: f.ErrorStatuses, TimeoutRatePct: f.TimeoutRatePct,
		TimeoutMs: f.TimeoutMs, DropRatePct: f.DropRatePct, MalformedRatePct: f.MalformedRatePct,
		MalformedMode: domain.MalformedMode(f.MalformedMode),
	}
}

func faultView(f *domain.Fault) *faultArgs {
	if f.IsZero() {
		return nil
	}

	return &faultArgs{
		JitterMs: f.JitterMs, ErrorStatuses: f.ErrorStatuses, TimeoutRatePct: f.TimeoutRatePct,
		TimeoutMs: f.TimeoutMs, DropRatePct: f.DropRatePct, MalformedRatePct: f.MalformedRatePct,
		MalformedMode: string(f.MalformedMode),
	}
}

type matchArg struct {
	Source string `json:"source"`
	Name   string `json:"name"`
	Op     string `json:"op"`
	Value  string `json:"value,omitempty"`
}

type ruleView struct {
	ID          string            `json:"id"`
	Project     string            `json:"project"`
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	Scenario    string            `json:"scenario"`
	Match       []matchArg        `json:"match,omitempty"`
	StatusCode  int               `json:"status_code"`
	ContentType string            `json:"content_type,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        string            `json:"body,omitempty"`
	DelayMs     int               `json:"delay_ms,omitempty"`
	FailRatePct int               `json:"fail_rate_pct,omitempty"`
	Fault       *faultArgs        `json:"fault,omitempty"`
}

func viewRule(m *domain.MockRule) ruleView {
	v := ruleView{
		ID: m.ID, Project: m.ProjectID, Method: m.Method, Path: m.Path, Scenario: m.Scenario,
		Match: nil, StatusCode: m.StatusCode, ContentType: m.ContentType, Headers: m.Headers,
		Body: clip(m.Body), DelayMs: m.DelayMs, FailRatePct: m.FailRatePct, Fault: faultView(m.Fault),
	}

	for _, c := range m.Match {
		v.Match = append(v.Match, matchArg{Source: string(c.Source), Name: c.Name, Op: string(c.Op), Value: c.Value})
	}

	return v
}

func (s *Server) ruleTools() []*tool {
	return []*tool{
		newTool("list_mock_rules", "List mock rules",
			"List a project's mock rules: the fixed responses that win over the contract's examples.", kindRead,
			object(props{"project": projectProp()}), s.listRules),
		newTool("add_mock_rule", "Add a mock rule",
			"Make the mock answer a method and path with a fixed response. The path may use {params} like the "+
				"contract (/users/{id}). A rule with a scenario is used only when the request names it with the "+
				"X-Mock-Scenario header; a rule with match conditions is used when they all hold. Delay and "+
				"faults make the response slow or broken on purpose.", kindWrite,
			object(props{
				"project":      projectProp(),
				"method":       text("HTTP method, for example GET."),
				"path":         text("Path, for example /users/{id}."),
				"status_code":  number("Response status (default 200)."),
				"body":         text("Response body. It may use {{placeholders}} for generated data."),
				"content_type": text("Content type (default application/json when there is a body)."),
				"headers":      stringMap("Response headers."),
				"scenario":     text("Scenario name; leave out for the default."),
				"match": map[string]any{
					"type": "array", "description": "Request conditions that must all hold.",
					"items": object(props{
						"source": choice("Where to look.", "body", "query", "header", "path"),
						"name":   text("Parameter or header name, or the JSON path in the body (user.role)."),
						"op": choice("Comparison.", "equals", "not_equals", "contains", "regex", "exists",
							"not_exists"),
						"value": text("The value to compare with (not used by exists and not_exists)."),
					}, "source", "name", "op"),
				},
				"delay_ms":      number("Latency added to every response, in milliseconds."),
				"fail_rate_pct": number("Percent of requests answered with an injected error instead."),
				"fault":         faultSchema(),
			}, "method", "path"), s.addRule),
		newTool("set_fault", "Set or clear faults on a rule",
			"Change the latency and failures of an existing mock rule. Give fault to replace its fault settings, "+
				"delay_ms and fail_rate_pct to change those, or clear to remove all of them.", kindWrite,
			object(props{
				"rule_id":       text("The rule id from list_mock_rules or add_mock_rule."),
				"delay_ms":      number("Latency added to every response, in milliseconds."),
				"fail_rate_pct": number("Percent of requests answered with an injected error."),
				"fault":         faultSchema(),
				"clear":         flag("Remove the delay, the error rate and the fault settings."),
			}, "rule_id"), s.setFault),
		newTool("delete_mock_rule", "Delete a mock rule",
			"Delete a mock rule; the contract's examples answer again.", kindDestroy,
			object(props{"rule_id": text("The rule id.")}, "rule_id"), s.deleteRule),
	}
}

func (s *Server) listRules(_ context.Context, a *projectArgs) (any, error) {
	p, err := s.project(a.Project)
	if err != nil {
		return nil, err
	}

	rules := s.deps.Mocks.List(p.ID)
	out := make([]ruleView, 0, len(rules))

	for _, r := range rules {
		out = append(out, viewRule(r))
	}

	return map[string]any{"project": p.ID, "rules": out}, nil
}

type addRuleArgs struct {
	Project     string            `json:"project"`
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	StatusCode  int               `json:"status_code"`
	Body        string            `json:"body"`
	ContentType string            `json:"content_type"`
	Headers     map[string]string `json:"headers"`
	Scenario    string            `json:"scenario"`
	Match       []matchArg        `json:"match"`
	DelayMs     int               `json:"delay_ms"`
	FailRatePct int               `json:"fail_rate_pct"`
	Fault       *faultArgs        `json:"fault"`
}

// rule builds the rule the arguments describe, with defaults filled in.
func (a *addRuleArgs) rule(projectID string) *domain.MockRule {
	rule := &domain.MockRule{ //nolint:exhaustruct // the optional fields stay unset.
		ProjectID: projectID, Method: strings.ToUpper(strings.TrimSpace(a.Method)), Path: a.Path, Scenario: a.Scenario,
		StatusCode: a.StatusCode, ContentType: a.ContentType, Headers: a.Headers, Body: a.Body,
		DelayMs: a.DelayMs, FailRatePct: a.FailRatePct,
	}

	if rule.StatusCode == 0 {
		rule.StatusCode = http.StatusOK
	}

	if rule.ContentType == "" && rule.Body != "" {
		rule.ContentType = "application/json"
	}

	for _, c := range a.Match {
		rule.Match = append(rule.Match, domain.MatchCondition{
			Source: domain.MatchSource(c.Source), Name: c.Name, Op: domain.MatchOp(c.Op), Value: c.Value,
		})
	}

	if a.Fault != nil {
		rule.Fault = a.Fault.domain()
	}

	return rule
}

func (s *Server) addRule(_ context.Context, a *addRuleArgs) (any, error) {
	if strings.TrimSpace(a.Method) == "" || strings.TrimSpace(a.Path) == "" {
		return nil, errMethodPath
	}

	p, err := s.project(a.Project)
	if err != nil {
		return nil, err
	}

	rule := a.rule(p.ID)

	err = rule.ValidateFault()
	if err != nil {
		return nil, fmt.Errorf("fault settings: %w", err)
	}

	created, err := s.deps.Mocks.Create(rule)
	if err != nil {
		return nil, fmt.Errorf("add the rule: %w", err)
	}

	return map[string]any{"rule": viewRule(created), "url": s.mockURL(p.ID) + rule.Path}, nil
}

type setFaultArgs struct {
	RuleID      string     `json:"rule_id"`
	DelayMs     *int       `json:"delay_ms"`
	FailRatePct *int       `json:"fail_rate_pct"`
	Fault       *faultArgs `json:"fault"`
	Clear       bool       `json:"clear"`
}

// findRule looks a rule up in every project, since a rule id is unique.
func (s *Server) findRule(id string) (*domain.MockRule, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errRuleNeeded
	}

	for _, p := range s.deps.Projects.List() {
		for _, r := range s.deps.Mocks.List(p.ID) {
			if r.ID == id {
				c := *r

				return &c, nil
			}
		}
	}

	return nil, fmt.Errorf("%w: %s", errRuleNotFound, id)
}

// applyTo changes the fault settings of rule: clear removes them all, the
// other arguments replace one setting each.
func (a *setFaultArgs) applyTo(rule *domain.MockRule) {
	if a.Clear {
		rule.DelayMs, rule.FailRatePct, rule.Fault = 0, 0, nil

		return
	}

	if a.DelayMs != nil {
		rule.DelayMs = *a.DelayMs
	}

	if a.FailRatePct != nil {
		rule.FailRatePct = *a.FailRatePct
	}

	if a.Fault != nil {
		rule.Fault = a.Fault.domain()
	}
}

func (s *Server) setFault(_ context.Context, a *setFaultArgs) (any, error) {
	rule, err := s.findRule(a.RuleID)
	if err != nil {
		return nil, err
	}

	if !a.Clear && a.DelayMs == nil && a.FailRatePct == nil && a.Fault == nil {
		return nil, errNoFaultChange
	}

	a.applyTo(rule)

	err = rule.ValidateFault()
	if err != nil {
		return nil, fmt.Errorf("fault settings: %w", err)
	}

	err = s.deps.Mocks.Update(rule)
	if err != nil {
		return nil, fmt.Errorf("update the rule: %w", err)
	}

	return map[string]any{"rule": viewRule(rule)}, nil
}

var errNoFaultChange = errors.New("give delay_ms, fail_rate_pct, fault or clear: there is nothing to change")

type ruleIDArgs struct {
	RuleID string `json:"rule_id"`
}

func (s *Server) deleteRule(_ context.Context, a *ruleIDArgs) (any, error) {
	rule, err := s.findRule(a.RuleID)
	if err != nil {
		return nil, err
	}

	err = s.deps.Mocks.Delete(rule.ID)
	if err != nil {
		return nil, fmt.Errorf("delete the rule: %w", err)
	}

	return map[string]any{"deleted": rule.ID}, nil
}
