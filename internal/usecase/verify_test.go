package usecase_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func rawJSON(s string) []byte { return []byte(s) }

func ptr[T any](v T) *T { return &v }

func verifyHub(t *testing.T, policy usecase.CapturePolicy) (*usecase.LogHub, *usecase.LogService) {
	t.Helper()

	hub := usecase.NewLogHub(&idRepo{}, policy)

	return hub, usecase.NewLogService(hub)
}

func addCall(hub *usecase.LogHub, method, path, query, endpoint, body string, status int) {
	hub.Add(&domain.RequestLog{
		ID: "", ProjectID: "p1", Method: method, Path: path, Query: query, StatusCode: status,
		MatchedRule: "", Matched: true, Timestamp: time.Now().UTC(), DurationMs: 0, Violations: nil, Fault: "",
		Source: "", Endpoint: endpoint, Captured: false,
		Request:  &domain.HTTPCapture{Headers: map[string][]string{"X-Trace": {"t-1"}, "Authorization": {"Bearer s"}}, Body: body, Size: len(body), Truncated: false, Binary: false},
		Response: nil,
	})
}

func TestVerify_BodyModesAndCounts(t *testing.T) {
	t.Parallel()

	hub, svc := verifyHub(t, usecase.DefaultCapturePolicy())
	addCall(hub, http.MethodPost, "/users", "", "/users", `{"name":"ann","tags":["a","b"],"age":30}`, 201)
	addCall(hub, http.MethodPost, "/users", "", "/users", `{"name":"bob"}`, 201)
	addCall(hub, http.MethodGet, "/users/42", "expand=1&x=2", "/users/{id}", "", 200)

	cases := []struct {
		name  string
		req   usecase.VerifyRequest
		pass  bool
		count int
	}{
		{"json subset", usecase.VerifyRequest{Method: "post", Path: "/users", Body: &usecase.BodyMatch{JSON: rawJSON(`{"name":"ann","age":30}`)}}, true, 1},
		{"json array same length", usecase.VerifyRequest{Path: "/users", Body: &usecase.BodyMatch{JSON: rawJSON(`{"tags":["a","b"]}`)}}, true, 1},
		{"json array different", usecase.VerifyRequest{Path: "/users", Body: &usecase.BodyMatch{JSON: rawJSON(`{"tags":["a"]}`)}}, false, 0},
		{"contains", usecase.VerifyRequest{Path: "/users", Body: &usecase.BodyMatch{Contains: ptr("bob")}}, true, 1},
		{"equals", usecase.VerifyRequest{Path: "/users", Body: &usecase.BodyMatch{Equals: ptr(`{"name":"bob"}`)}}, true, 1},
		{"regex", usecase.VerifyRequest{Path: "/users", Body: &usecase.BodyMatch{Regex: ptr(`"name":"(ann|bob)"`)}}, true, 2},
		{"template path", usecase.VerifyRequest{Method: "GET", Path: "/users/{id}"}, true, 1},
		{"endpoint", usecase.VerifyRequest{Endpoint: "/users/{id}"}, true, 1},
		{"query", usecase.VerifyRequest{Path: "/users/42", Query: map[string]string{"expand": "1"}}, true, 1},
		{"query wrong", usecase.VerifyRequest{Path: "/users/42", Query: map[string]string{"expand": "2"}}, false, 0},
		{"header", usecase.VerifyRequest{Path: "/users", Headers: map[string]string{"x-trace": "t-1"}}, true, 2},
		{"status", usecase.VerifyRequest{Path: "/users", Status: ptr(404)}, false, 0},
		{"exactly 2", usecase.VerifyRequest{Path: "/users", Exactly: ptr(2)}, true, 2},
		{"exactly 1 fails", usecase.VerifyRequest{Path: "/users", Exactly: ptr(1)}, false, 2},
		{"at most 1 fails", usecase.VerifyRequest{Path: "/users", AtMost: ptr(1)}, false, 2},
		{"at least 3 fails", usecase.VerifyRequest{Path: "/users", AtLeast: ptr(3)}, false, 2},
		{"never passes", usecase.VerifyRequest{Path: "/orders", Exactly: ptr(0)}, true, 0},
		{"never fails", usecase.VerifyRequest{Path: "/users", Exactly: ptr(0)}, false, 2},
		{"at most 0 on nothing", usecase.VerifyRequest{Path: "/orders", AtMost: ptr(0)}, true, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res, err := svc.Verify("p1", &tc.req)
			if err != nil {
				t.Fatal(err)
			}

			if res.Passed != tc.pass || res.Count != tc.count {
				t.Errorf("passed=%v count=%d, want %v/%d: %s", res.Passed, res.Count, tc.pass, tc.count, res.Message)
			}
		})
	}
}

func TestVerify_FailureShowsClosestCall(t *testing.T) {
	t.Parallel()

	hub, svc := verifyHub(t, usecase.DefaultCapturePolicy())
	addCall(hub, http.MethodPost, "/users", "", "/users", `{"name":"ann"}`, 201)
	addCall(hub, http.MethodGet, "/health", "", "/health", "", 200)

	res, err := svc.Verify("p1", &usecase.VerifyRequest{
		Method: "POST", Path: "/users", Body: &usecase.BodyMatch{JSON: rawJSON(`{"name":"zed"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}

	if res.Passed || len(res.Closest) == 0 || res.Closest[0].Call.Path != "/users" {
		t.Fatalf("result = %+v", res)
	}

	if !strings.Contains(res.Message, "$.name") || !strings.Contains(res.Message, "FAILED") {
		t.Errorf("message = %q", res.Message)
	}
}

func TestVerify_RedactedHeaderIsExplained(t *testing.T) {
	t.Parallel()

	hub, svc := verifyHub(t, usecase.DefaultCapturePolicy())
	addCall(hub, http.MethodGet, "/a", "", "", "", 200)

	res, err := svc.Verify("p1", &usecase.VerifyRequest{Path: "/a", Headers: map[string]string{"Authorization": "Bearer s"}})
	if err != nil {
		t.Fatal(err)
	}

	if res.Passed || !strings.Contains(res.Message, "redacted") {
		t.Errorf("result = %+v", res)
	}
}

func TestVerify_WindowAndClear(t *testing.T) {
	t.Parallel()

	hub, svc := verifyHub(t, usecase.DefaultCapturePolicy())
	addCall(hub, http.MethodGet, "/a", "", "", "", 200)

	first := hub.ListLogs("p1", 1)[0].ID

	addCall(hub, http.MethodGet, "/a", "", "", "", 200)

	res, err := svc.Verify("p1", &usecase.VerifyRequest{Path: "/a", SinceID: first})
	if err != nil || res.Count != 1 {
		t.Fatalf("sinceId: %+v %v", res, err)
	}

	future := time.Now().Add(time.Hour)

	res, err = svc.Verify("p1", &usecase.VerifyRequest{Path: "/a", Since: &future})
	if err != nil || res.Count != 0 || res.Passed {
		t.Fatalf("since: %+v %v", res, err)
	}

	_, err = svc.Verify("p1", &usecase.VerifyRequest{Path: "/a", SinceID: "nope"})
	if !errors.Is(err, usecase.ErrInvalidVerify) {
		t.Errorf("unknown sinceId: %v", err)
	}

	n, err := svc.ClearCalls("p1")
	if err != nil || n != 2 {
		t.Fatalf("clear: %d %v", n, err)
	}

	res, _ = svc.Verify("p1", &usecase.VerifyRequest{Path: "/a", Exactly: ptr(0)})
	if !res.Passed || res.Examined != 0 {
		t.Errorf("after clear: %+v", res)
	}

	// The persistent log is untouched.
	if len(svc.List("p1", 10)) != 2 {
		t.Error("clearing calls must not clear the log")
	}
}

func TestVerify_Validation(t *testing.T) {
	t.Parallel()

	_, svc := verifyHub(t, usecase.DefaultCapturePolicy())

	bad := []usecase.VerifyRequest{
		{},
		{Path: "/a", Body: &usecase.BodyMatch{}},
		{Path: "/a", Body: &usecase.BodyMatch{Contains: ptr("a"), Equals: ptr("a")}},
		{Path: "/a", Body: &usecase.BodyMatch{Regex: ptr("(")}},
		{Path: "/a", Body: &usecase.BodyMatch{JSON: rawJSON("{")}},
		{Path: "/a", Exactly: ptr(1), AtLeast: ptr(1)},
		{Path: "/a", AtLeast: ptr(3), AtMost: ptr(1)},
		{Path: "/a", AtLeast: ptr(-1)},
	}

	for i := range bad {
		_, err := svc.Verify("p1", &bad[i])
		if !errors.Is(err, usecase.ErrInvalidVerify) {
			t.Errorf("case %d: err = %v", i, err)
		}
	}
}

func TestVerify_BodyNeedsCapture(t *testing.T) {
	t.Parallel()

	_, svc := verifyHub(t, usecase.CapturePolicy{Enabled: false, MaxBytes: 0})

	_, err := svc.Verify("p1", &usecase.VerifyRequest{Path: "/a", Body: &usecase.BodyMatch{Contains: ptr("x")}})
	if !errors.Is(err, usecase.ErrInvalidVerify) {
		t.Errorf("err = %v", err)
	}

	// Without body or header constraints capture is not needed.
	res, err := svc.Verify("p1", &usecase.VerifyRequest{Path: "/a", Exactly: ptr(0)})
	if err != nil || !res.Passed {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

func TestVerify_NeedsHub(t *testing.T) {
	t.Parallel()

	svc := usecase.NewLogService(&idRepo{})

	_, err := svc.Verify("p1", &usecase.VerifyRequest{Path: "/a"})
	if !errors.Is(err, usecase.ErrObservabilityOff) {
		t.Errorf("err = %v", err)
	}
}

func TestVerify_JournalKeepsBodiesPastTheRing(t *testing.T) {
	t.Parallel()

	hub, svc := verifyHub(t, usecase.DefaultCapturePolicy())

	for range 700 {
		addCall(hub, http.MethodPost, "/n", "", "", `{"k":1}`, 200)
	}

	res, err := svc.Verify("p1", &usecase.VerifyRequest{
		Path: "/n", Body: &usecase.BodyMatch{JSON: rawJSON(`{"k":1}`)}, Exactly: ptr(700),
	})
	if err != nil || !res.Passed || len(res.Matches) != 100 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestVerify_UncapturedBodiesAreExplained(t *testing.T) {
	t.Parallel()

	hub, svc := verifyHub(t, usecase.CapturePolicy{Enabled: true, MaxBytes: 8})
	addCall(hub, http.MethodPost, "/big", "", "", `{"name":"a-long-value"}`, 200)
	addCall(hub, http.MethodPost, "/bin", "", "", "\xff\xfe", 200)
	hub.Add(&domain.RequestLog{
		ID: "", ProjectID: "p1", Method: http.MethodPost, Path: "/none", Query: "", StatusCode: 200,
		MatchedRule: "", Matched: true, Timestamp: time.Now().UTC(), DurationMs: 0, Violations: nil, Fault: "",
		Source: "", Endpoint: "", Captured: false, Request: nil, Response: nil,
	})

	for path, want := range map[string]string{"/big": "cut to", "/bin": "binary", "/none": "not captured"} {
		res, err := svc.Verify("p1", &usecase.VerifyRequest{Path: path, Body: &usecase.BodyMatch{Contains: ptr("zzz")}})
		if err != nil || res.Passed || len(res.Closest) == 0 ||
			!strings.Contains(strings.Join(res.Closest[0].Mismatches, ";"), want) {
			t.Errorf("%s: res=%+v err=%v", path, res, err)
		}
	}

	res, _ := svc.Verify("p1", &usecase.VerifyRequest{Path: "/big", Body: &usecase.BodyMatch{JSON: rawJSON(`{}`)}})
	if res.Passed || !strings.Contains(res.Message, "not valid JSON") {
		t.Errorf("truncated json: %s", res.Message)
	}
}

func TestVerify_JSONMismatchKinds(t *testing.T) {
	t.Parallel()

	hub, svc := verifyHub(t, usecase.DefaultCapturePolicy())
	addCall(hub, http.MethodPost, "/j", "", "", `{"a":{"b":[1,2]},"s":"x"}`, 200)

	for _, want := range []string{`{"a":[1]}`, `{"a":{"b":{}}}`, `{"a":{"b":[1]}}`, `{"a":{"b":[1,3]}}`, `{"z":1}`, `{"s":1}`} {
		res, err := svc.Verify("p1", &usecase.VerifyRequest{Path: "/j", Body: &usecase.BodyMatch{JSON: rawJSON(want)}})
		if err != nil || res.Passed {
			t.Errorf("%s must not match: %+v %v", want, res, err)
		}
	}

	res, _ := svc.Verify("p1", &usecase.VerifyRequest{Path: "/j", Body: &usecase.BodyMatch{JSON: rawJSON(`{"a":{"b":[1,2]}}`)}})
	if !res.Passed {
		t.Error("subset must match")
	}
}

func TestVerify_CountDescriptionsAndCalls(t *testing.T) {
	t.Parallel()

	hub, svc := verifyHub(t, usecase.DefaultCapturePolicy())
	addCall(hub, http.MethodGet, "/a", "", "", "", 200)
	addCall(hub, http.MethodGet, "/a", "", "", "", 200)

	for req, want := range map[*usecase.VerifyRequest]string{
		{Path: "/a", AtLeast: ptr(1), AtMost: ptr(3)}: "between 1 and 3",
		{Path: "/a", AtMost: ptr(5)}:                  "at most 5",
		{Path: "/a", Exactly: ptr(0)}:                 "never",
		{Endpoint: "/a"}:                              "at least 1",
	} {
		res, err := svc.Verify("p1", req)
		if err != nil || res.Expected != want {
			t.Errorf("expected %q, got %+v %v", want, res, err)
		}
	}

	calls, err := svc.Calls("p1", 1)
	if err != nil || len(calls) != 1 {
		t.Fatalf("calls: %v %v", calls, err)
	}

	off := usecase.NewLogService(&idRepo{})

	_, err = off.Calls("p1", 1)
	if !errors.Is(err, usecase.ErrObservabilityOff) {
		t.Errorf("calls without hub: %v", err)
	}

	_, err = off.ClearCalls("p1")
	if !errors.Is(err, usecase.ErrObservabilityOff) {
		t.Errorf("clear without hub: %v", err)
	}
}

func TestVerify_HeaderNameIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	hub := usecase.NewLogHub(&idRepo{}, usecase.DefaultCapturePolicy())
	hub.Add(&domain.RequestLog{
		ID: "", ProjectID: "p1", Method: http.MethodGet, Path: "/h", Query: "", StatusCode: 200,
		MatchedRule: "", Matched: true, Timestamp: time.Now().UTC(), DurationMs: 0, Violations: nil, Fault: "",
		Source: "", Endpoint: "", Captured: false,
		Request:  &domain.HTTPCapture{Headers: map[string][]string{"x-odd": {"1"}}, Body: "", Size: 0, Truncated: false, Binary: false},
		Response: nil,
	})

	res, err := usecase.NewLogService(hub).Verify("p1", &usecase.VerifyRequest{Path: "/h", Headers: map[string]string{"X-Odd": "1"}})
	if err != nil || !res.Passed {
		t.Errorf("res=%+v err=%v", res, err)
	}
}
