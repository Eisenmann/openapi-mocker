package usecase

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/textproto"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// maxClosest is how many near-miss calls a failed verification reports.
const maxClosest = 3

// jsonNumberEpsilon is the tolerance when comparing JSON numbers.
const jsonNumberEpsilon = 1e-9

// maxVerifyMatches caps the calls returned with a verification.
const maxVerifyMatches = 100

// VerifyRequest asks "was this call made to the mock, and how often?". Every
// field that is set must hold for a call to count; empty fields match any.
type VerifyRequest struct {
	// Method is the HTTP method (case-insensitive).
	Method string `json:"method,omitempty"`
	// Path is the request path as the client sent it, relative to the mock
	// root ("/users/42"). Segments written "{name}" or "*" match anything,
	// so "/users/{id}" matches "/users/42".
	Path string `json:"path,omitempty"`
	// Endpoint is the contract operation that served the call: the path
	// template of a REST operation, "graphql", or the MCP method.
	Endpoint string `json:"endpoint,omitempty"`
	// Query lists query parameters that must be present with that value.
	Query map[string]string `json:"query,omitempty"`
	// Headers lists request headers that must be present with that value
	// (names are case-insensitive). Secret headers are redacted in the log
	// and can not be matched.
	Headers map[string]string `json:"headers,omitempty"`
	// Body constrains the request body.
	Body *BodyMatch `json:"body,omitempty"`
	// Status is the status code the mock answered with.
	Status *int `json:"status,omitempty"`

	// AtLeast, AtMost and Exactly bound how many calls match. Without any of
	// them the verification asks for at least one. Exactly 0 means "never".
	AtLeast *int `json:"atLeast,omitempty"`
	AtMost  *int `json:"atMost,omitempty"`
	Exactly *int `json:"exactly,omitempty"`

	// Since only considers calls made at or after this time.
	Since *time.Time `json:"since,omitempty"`
	// SinceID only considers calls made after the log entry with this id.
	SinceID string `json:"sinceId,omitempty"`
}

// BodyMatch constrains a request body; set exactly one field.
type BodyMatch struct {
	// Equals is the exact body text.
	Equals *string `json:"equals,omitempty"`
	// Contains is a substring of the body.
	Contains *string `json:"contains,omitempty"`
	// Regex is a Go regular expression the body must match.
	Regex *string `json:"regex,omitempty"`
	// JSON is a JSON value the body must contain: objects match when they
	// hold at least the given properties, arrays when they have the same
	// length and each element matches, scalars when they are equal.
	JSON json.RawMessage `json:"json,omitempty"`
}

// VerifyResult is the answer to a VerifyRequest.
type VerifyResult struct {
	// Passed says whether the number of matching calls satisfies the request.
	Passed bool `json:"passed"`
	// Count is the number of calls that matched.
	Count int `json:"count"`
	// Expected describes the wanted count, e.g. "at least 1".
	Expected string `json:"expected"`
	// Message is a one-line explanation, fit for a test failure message.
	Message string `json:"message"`
	// Matches are the matching calls with their captured bodies, newest
	// first (at most 100).
	Matches []*domain.RequestLog `json:"matches"`
	// Closest are the calls that came nearest, set when nothing matched.
	Closest []Candidate `json:"closest,omitempty"`
	// Examined is how many recorded calls were checked.
	Examined int `json:"examined"`
}

// Candidate is a call that nearly matched, with what differed.
type Candidate struct {
	Call       *domain.RequestLog `json:"call"`
	Mismatches []string           `json:"mismatches"`
}

// compiledVerify is a validated VerifyRequest.
type compiledVerify struct {
	req      *VerifyRequest
	path     string
	endpoint string
	method   string
	regex    *regexp.Regexp
	jsonWant any
	min, max int // max is -1 when unbounded.
	criteria int
}

// Verify checks the recorded calls of a project against req.
func (s *LogService) Verify(projectID string, req *VerifyRequest) (*VerifyResult, error) {
	if s.hub == nil {
		return nil, ErrObservabilityOff
	}

	cv, err := compileVerify(req)
	if err != nil {
		return nil, err
	}

	if (req.Body != nil || len(req.Headers) > 0) && !s.hub.Policy().Enabled {
		return nil, fmt.Errorf("%w: body and header matching need body capture (LOG_CAPTURE_BODIES is off)",
			ErrInvalidVerify)
	}

	calls, err := windowOf(s.hub.Journal(projectID), req)
	if err != nil {
		return nil, err
	}

	return cv.run(calls), nil
}

// Calls lists the recorded calls of a project, newest first, with bodies.
func (s *LogService) Calls(projectID string, limit int) ([]*domain.RequestLog, error) {
	if s.hub == nil {
		return nil, ErrObservabilityOff
	}

	calls := s.hub.Journal(projectID)
	out := make([]*domain.RequestLog, 0, min(limit, len(calls)))

	for i := len(calls) - 1; i >= 0 && len(out) < limit; i-- {
		c := *calls[i]
		out = append(out, &c)
	}

	return out, nil
}

// ClearCalls forgets the recorded calls of a project, so the next test starts
// from an empty record. It returns how many calls were dropped.
func (s *LogService) ClearCalls(projectID string) (int, error) {
	if s.hub == nil {
		return 0, ErrObservabilityOff
	}

	return s.hub.ClearJournal(projectID), nil
}

func invalidVerify(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidVerify, fmt.Sprintf(format, args...))
}

func compileVerify(req *VerifyRequest) (*compiledVerify, error) {
	cv := &compiledVerify{
		req: req, path: normalizePath(req.Path), endpoint: req.Endpoint, method: strings.ToUpper(req.Method),
		regex: nil, jsonWant: nil, min: 1, max: -1, criteria: 0,
	}

	if req.Path == "" && req.Endpoint == "" {
		return nil, invalidVerify("set path or endpoint")
	}

	err := cv.compileBody()
	if err != nil {
		return nil, err
	}

	err = cv.compileCounts()
	if err != nil {
		return nil, err
	}

	cv.countCriteria()

	return cv, nil
}

func (cv *compiledVerify) compileBody() error {
	b := cv.req.Body
	if b == nil {
		return nil
	}

	if b.parts() != 1 {
		return invalidVerify("body needs exactly one of equals, contains, regex or json")
	}

	if b.Regex != nil {
		re, err := regexp.Compile(*b.Regex)
		if err != nil {
			return invalidVerify("body.regex: %v", err)
		}

		cv.regex = re
	}

	if len(b.JSON) > 0 {
		err := json.Unmarshal(b.JSON, &cv.jsonWant)
		if err != nil {
			return invalidVerify("body.json: %v", err)
		}
	}

	return nil
}

// parts counts how many of the body constraints are set.
func (b *BodyMatch) parts() int {
	n := 0

	for _, set := range []bool{b.Equals != nil, b.Contains != nil, b.Regex != nil, len(b.JSON) > 0} {
		if set {
			n++
		}
	}

	return n
}

func (cv *compiledVerify) compileCounts() error {
	r := cv.req

	for name, n := range map[string]*int{"atLeast": r.AtLeast, "atMost": r.AtMost, "exactly": r.Exactly} {
		if n != nil && *n < 0 {
			return invalidVerify("%s must not be negative", name)
		}
	}

	if r.Exactly != nil && (r.AtLeast != nil || r.AtMost != nil) {
		return invalidVerify("exactly can not be combined with atLeast or atMost")
	}

	cv.min, cv.max = bounds(r)
	if cv.max >= 0 && cv.min > cv.max {
		return invalidVerify("atLeast is greater than atMost")
	}

	return nil
}

// bounds turns the count fields into a range; max is -1 when unbounded.
func bounds(r *VerifyRequest) (lo, hi int) {
	switch {
	case r.Exactly != nil:
		return *r.Exactly, *r.Exactly
	case r.AtLeast != nil:
		lo = *r.AtLeast
	case r.AtMost != nil:
		lo = 0
	default:
		lo = 1
	}

	hi = -1
	if r.AtMost != nil {
		hi = *r.AtMost
	}

	return lo, hi
}

// countCriteria counts the constraints set, so near misses can be ranked.
func (cv *compiledVerify) countCriteria() {
	r := cv.req

	for _, set := range []bool{
		r.Method != "", r.Path != "", r.Endpoint != "", len(r.Query) > 0, len(r.Headers) > 0,
		r.Body != nil, r.Status != nil,
	} {
		if set {
			cv.criteria++
		}
	}
}

// windowOf applies Since and SinceID to calls (oldest first).
func windowOf(calls []*domain.RequestLog, req *VerifyRequest) ([]*domain.RequestLog, error) {
	if req.SinceID != "" {
		found := false

		for i, c := range calls {
			if c.ID == req.SinceID {
				calls, found = calls[i+1:], true
				break
			}
		}

		if !found {
			return nil, invalidVerify("sinceId %q is not among the recorded calls", req.SinceID)
		}
	}

	if req.Since == nil {
		return calls, nil
	}

	out := make([]*domain.RequestLog, 0, len(calls))

	for _, c := range calls {
		if !c.Timestamp.Before(*req.Since) {
			out = append(out, c)
		}
	}

	return out, nil
}

func (cv *compiledVerify) run(calls []*domain.RequestLog) *VerifyResult {
	res := &VerifyResult{
		Passed: false, Count: 0, Expected: cv.expected(), Message: "", Matches: []*domain.RequestLog{},
		Closest: nil, Examined: len(calls),
	}

	var misses []Candidate

	// Newest first, so Matches and Closest lead with the latest calls.
	for i := len(calls) - 1; i >= 0; i-- {
		why := cv.mismatches(calls[i])
		if len(why) > 0 {
			misses = append(misses, Candidate{Call: calls[i], Mismatches: why})
			continue
		}

		res.Count++

		if len(res.Matches) < maxVerifyMatches {
			c := *calls[i]
			res.Matches = append(res.Matches, &c)
		}
	}

	res.Passed = res.Count >= cv.min && (cv.max < 0 || res.Count <= cv.max)
	if !res.Passed && res.Count == 0 {
		res.Closest = closest(misses)
	}

	res.Message = cv.message(res)

	return res
}

func (cv *compiledVerify) expected() string {
	switch {
	case cv.max == 0:
		return "never"
	case cv.max < 0:
		return fmt.Sprintf("at least %d", cv.min)
	case cv.min == cv.max:
		return fmt.Sprintf("exactly %d", cv.min)
	case cv.min == 0:
		return fmt.Sprintf("at most %d", cv.max)
	default:
		return fmt.Sprintf("between %d and %d", cv.min, cv.max)
	}
}

func (cv *compiledVerify) message(res *VerifyResult) string {
	r := cv.req

	target := strings.TrimSpace(r.Method + " " + firstNonEmpty(r.Path, r.Endpoint))
	verdict := "ok"

	if !res.Passed {
		verdict = "FAILED"
	}

	msg := fmt.Sprintf("%s: %s was called %d time(s), expected %s", verdict, target, res.Count, res.Expected)

	if len(res.Closest) > 0 {
		c := res.Closest[0]
		msg += fmt.Sprintf("; closest call: %s %s (%s)", c.Call.Method, c.Call.Path, strings.Join(c.Mismatches, "; "))
	} else if !res.Passed && res.Examined == 0 {
		msg += "; no calls are recorded for this project"
	}

	return msg
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}

// closest returns the near misses that matched the most constraints.
func closest(misses []Candidate) []Candidate {
	// Stable, so equally close calls stay newest first.
	sort.SliceStable(misses, func(i, j int) bool { return len(misses[i].Mismatches) < len(misses[j].Mismatches) })

	return misses[:min(len(misses), maxClosest)]
}

// mismatches lists why the call does not match; empty means it matches.
func (cv *compiledVerify) mismatches(l *domain.RequestLog) []string {
	r := cv.req
	why := cv.targetMismatches(l)

	if r.Status != nil && l.StatusCode != *r.Status {
		why = append(why, fmt.Sprintf("status is %d, expected %d", l.StatusCode, *r.Status))
	}

	why = append(why, queryMismatches(r.Query, l.Query)...)
	why = append(why, headerMismatches(r.Headers, l.Request)...)

	if r.Body != nil {
		why = append(why, cv.bodyMismatches(l.Request)...)
	}

	return why
}

// targetMismatches checks which operation was called: method, path, endpoint.
func (cv *compiledVerify) targetMismatches(l *domain.RequestLog) []string {
	var why []string

	r := cv.req

	if cv.method != "" && !strings.EqualFold(l.Method, cv.method) {
		why = append(why, fmt.Sprintf("method is %s, expected %s", l.Method, cv.method))
	}

	if r.Path != "" && !pathMatches(cv.path, l.Path) && !servedBy(l, cv.path) {
		why = append(why, fmt.Sprintf("path is %s, expected %s", l.Path, r.Path))
	}

	if r.Endpoint != "" && l.Endpoint != r.Endpoint {
		why = append(why, fmt.Sprintf("endpoint is %q, expected %q", l.Endpoint, r.Endpoint))
	}

	return why
}

// servedBy reports whether the call was served by the operation whose path
// template is path, so "/users/{id}" also matches by the contract's template.
func servedBy(l *domain.RequestLog, path string) bool {
	return l.Endpoint != "" && normalizePath(l.Endpoint) == path
}

func queryMismatches(want map[string]string, raw string) []string {
	if len(want) == 0 {
		return nil
	}

	got, _ := url.ParseQuery(raw)

	var why []string

	for _, name := range sortedKeys(want) {
		if !contains(got[name], want[name]) {
			why = append(why, fmt.Sprintf("query %s is %s, expected %q", name, describeValues(got[name]), want[name]))
		}
	}

	return why
}

func headerMismatches(want map[string]string, c *domain.HTTPCapture) []string {
	if len(want) == 0 {
		return nil
	}

	var why []string

	for _, name := range sortedKeys(want) {
		var got []string

		if c != nil {
			got = c.Headers[textproto.CanonicalMIMEHeaderKey(name)]
			if got == nil {
				got = headerIgnoreCase(c.Headers, name)
			}
		}

		switch {
		case contains(got, want[name]):
		case contains(got, redactedValue):
			why = append(why, fmt.Sprintf("header %s is redacted in the log and can not be matched", name))
		default:
			why = append(why, fmt.Sprintf("header %s is %s, expected %q", name, describeValues(got), want[name]))
		}
	}

	return why
}

func headerIgnoreCase(h map[string][]string, name string) []string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v
		}
	}

	return nil
}

func (cv *compiledVerify) bodyMismatches(c *domain.HTTPCapture) []string {
	if c == nil {
		return []string{"request body was not captured"}
	}

	if c.Binary {
		return []string{"request body is binary and was not captured"}
	}

	if cv.req.Body.JSON != nil {
		return jsonMismatches(cv.jsonWant, c)
	}

	return cv.textMismatches(c)
}

func (cv *compiledVerify) textMismatches(c *domain.HTTPCapture) []string {
	b := cv.req.Body

	switch {
	case b.Equals != nil && c.Body != *b.Equals:
		return []string{"body differs from the expected text" + truncatedNote(c)}
	case b.Contains != nil && !strings.Contains(c.Body, *b.Contains):
		return []string{fmt.Sprintf("body does not contain %q%s", *b.Contains, truncatedNote(c))}
	case cv.regex != nil && !cv.regex.MatchString(c.Body):
		return []string{fmt.Sprintf("body does not match /%s/%s", cv.regex.String(), truncatedNote(c))}
	default:
		return nil
	}
}

func truncatedNote(c *domain.HTTPCapture) string {
	if !c.Truncated {
		return ""
	}

	return fmt.Sprintf(" (body was cut to %d of %d bytes)", len(c.Body), c.Size)
}

func jsonMismatches(want any, c *domain.HTTPCapture) []string {
	var got any

	dec := json.NewDecoder(bytes.NewReader([]byte(c.Body)))

	err := dec.Decode(&got)
	if err != nil {
		return []string{"body is not valid JSON" + truncatedNote(c)}
	}

	if where := jsonDiff(want, got, "$"); where != "" {
		return []string{"body json differs at " + where}
	}

	return nil
}

// jsonDiff returns where got does not contain want, or "" when it does.
func jsonDiff(want, got any, at string) string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return at + " (expected an object)"
		}

		for _, k := range sortedKeys(w) {
			gv, present := g[k]
			if !present {
				return at + "." + k + " (missing)"
			}

			if d := jsonDiff(w[k], gv, at+"."+k); d != "" {
				return d
			}
		}

		return ""
	case []any:
		return arrayDiff(w, got, at)
	default:
		if scalarEqual(want, got) {
			return ""
		}

		return fmt.Sprintf("%s (expected %v, got %v)", at, want, got)
	}
}

func arrayDiff(want []any, got any, at string) string {
	g, ok := got.([]any)
	if !ok {
		return at + " (expected an array)"
	}

	if len(g) != len(want) {
		return fmt.Sprintf("%s (expected %d element(s), got %d)", at, len(want), len(g))
	}

	for i := range want {
		if d := jsonDiff(want[i], g[i], fmt.Sprintf("%s[%d]", at, i)); d != "" {
			return d
		}
	}

	return ""
}

func scalarEqual(a, b any) bool {
	af, aok := a.(float64)
	bf, bok := b.(float64)

	if aok && bok {
		return math.Abs(af-bf) < jsonNumberEpsilon
	}

	return a == b
}

// normalizePath gives a path a leading slash and no trailing one.
func normalizePath(p string) string {
	if p == "" {
		return ""
	}

	p = "/" + strings.Trim(p, "/")

	return p
}

// pathMatches compares a path template with a path, segment by segment.
func pathMatches(template, path string) bool {
	if template == "" {
		return true
	}

	want := strings.Split(template, "/")
	got := strings.Split(normalizePath(path), "/")

	if len(want) != len(got) {
		return false
	}

	for i := range want {
		wild := want[i] == "*" || (strings.HasPrefix(want[i], "{") && strings.HasSuffix(want[i], "}"))
		if !wild && want[i] != got[i] {
			return false
		}
	}

	return true
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}

	return false
}

func describeValues(values []string) string {
	switch len(values) {
	case 0:
		return "missing"
	case 1:
		return fmt.Sprintf("%q", values[0])
	default:
		return fmt.Sprintf("%q", values)
	}
}
