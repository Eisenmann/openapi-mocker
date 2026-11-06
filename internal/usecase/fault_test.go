package usecase_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func serveFault(t *testing.T, rule *domain.MockRule) (usecase.MockResponse, *domain.RequestLog) {
	t.Helper()

	repo := newMockContractRepo()
	repo.AddVersion("p1", "yaml", "raw", "manual")

	rule.ProjectID, rule.Path, rule.Method = "p1", "/users", "GET"

	mocks := newMockMockRepo()
	mocks.CreateMock(rule)

	logs := newMockLogRepo()
	svc := usecase.NewMockServingService(repo, mocks, logs, &mockContractEngine{found: true, pathTemplate: "/users"})

	resp := svc.Serve("p1", "GET", "/users", "")

	entries := logs.ListLogs("p1", 1)
	if len(entries) == 0 {
		return resp, nil
	}

	return resp, entries[0]
}

func TestFault_DropAndTimeout(t *testing.T) {
	t.Parallel()

	resp, entry := serveFault(t, &domain.MockRule{StatusCode: 200, Body: `{}`, Fault: &domain.Fault{DropRatePct: 100}})
	if resp.Fault != usecase.FaultDrop || resp.Source != "chaos-drop" || resp.StatusCode != 0 {
		t.Errorf("drop: %+v", resp)
	}

	if entry == nil || entry.Fault != usecase.FaultDrop {
		t.Errorf("the request log must record the fault: %+v", entry)
	}

	resp, _ = serveFault(t, &domain.MockRule{StatusCode: 200, Body: `{}`, Fault: &domain.Fault{TimeoutRatePct: 100}})
	if resp.Fault != usecase.FaultTimeout || resp.HoldMs != domain.DefaultTimeoutMs {
		t.Errorf("timeout default hold: %+v", resp)
	}

	resp, _ = serveFault(t, &domain.MockRule{StatusCode: 200, Body: `{}`, Fault: &domain.Fault{TimeoutRatePct: 100, TimeoutMs: 750}})
	if resp.HoldMs != 750 {
		t.Errorf("timeout hold: got %d", resp.HoldMs)
	}
}

func TestFault_ErrorStatuses(t *testing.T) {
	t.Parallel()

	resp, _ := serveFault(t, &domain.MockRule{
		StatusCode: 200, Body: `{}`, FailRatePct: 100,
		Fault: &domain.Fault{ErrorStatuses: []int{429, 503}},
	})
	if !slices.Contains([]int{429, 503}, resp.StatusCode) || resp.Fault != usecase.FaultError || resp.Source != "chaos-injection" {
		t.Errorf("custom error status: %+v", resp)
	}

	resp, _ = serveFault(t, &domain.MockRule{StatusCode: 200, Body: `{}`, FailRatePct: 100})
	if resp.StatusCode < 500 || resp.StatusCode > 503 {
		t.Errorf("default error status: %d", resp.StatusCode)
	}
}

func TestFault_MalformedBodies(t *testing.T) {
	t.Parallel()

	const body = `{"name":"Ada","email":"ada@example.com"}`

	cases := []struct {
		mode      domain.MalformedMode
		fault     string
		wantBody  func([]byte) bool
		bodyLabel string
	}{
		{domain.MalformedGarbage, usecase.FaultGarbage, func(b []byte) bool { return len(b) > 0 && !json.Valid(b) }, "invalid JSON"},
		{"", usecase.FaultGarbage, func(b []byte) bool { return len(b) > 0 && !json.Valid(b) }, "invalid JSON (default mode)"},
		{domain.MalformedEmpty, usecase.FaultEmpty, func(b []byte) bool { return len(b) == 0 }, "empty"},
		{domain.MalformedTruncated, usecase.FaultTruncated, func(b []byte) bool { return string(b) == body }, "the full body (the adapter cuts it)"},
	}

	for _, tc := range cases {
		resp, _ := serveFault(t, &domain.MockRule{
			StatusCode: 201, Body: body,
			Fault: &domain.Fault{MalformedRatePct: 100, MalformedMode: tc.mode},
		})

		if resp.Fault != tc.fault || resp.Source != "chaos-malformed" || resp.StatusCode != 201 {
			t.Errorf("mode %q: %+v", tc.mode, resp)
		}

		if !tc.wantBody(resp.Body) {
			t.Errorf("mode %q: body must be %s, got %q", tc.mode, tc.bodyLabel, resp.Body)
		}
	}

	resp, _ := serveFault(t, &domain.MockRule{
		StatusCode: 200, Body: ``,
		Fault: &domain.Fault{MalformedRatePct: 100},
	})
	if json.Valid(resp.Body) || !bytes.Contains(resp.Body, []byte("malformed")) {
		t.Errorf("garbage of an empty body must still be invalid: %q", resp.Body)
	}
}

func TestFault_LatencyWithJitter(t *testing.T) {
	t.Parallel()

	for range 50 {
		resp, _ := serveFault(t, &domain.MockRule{StatusCode: 200, Body: `{}`, DelayMs: 100, Fault: &domain.Fault{JitterMs: 40}})
		if resp.DelayMs < 100 || resp.DelayMs > 140 {
			t.Fatalf("delay must be 100..140, got %d", resp.DelayMs)
		}
	}

	resp, _ := serveFault(t, &domain.MockRule{StatusCode: 200, Body: `{}`, DelayMs: 100})
	if resp.DelayMs != 100 {
		t.Errorf("no jitter: got %d", resp.DelayMs)
	}

	// Latency also applies to faults that replace the response.
	resp, _ = serveFault(t, &domain.MockRule{StatusCode: 200, Body: `{}`, DelayMs: 80, FailRatePct: 100})
	if resp.DelayMs != 80 {
		t.Errorf("error fault keeps the delay: got %d", resp.DelayMs)
	}
}

func TestFault_RatesAreExclusiveShares(t *testing.T) {
	t.Parallel()

	rule := &domain.MockRule{
		StatusCode: 200, Body: `{}`, FailRatePct: 30,
		Fault: &domain.Fault{DropRatePct: 20, TimeoutRatePct: 10, TimeoutMs: 1, MalformedRatePct: 40, MalformedMode: domain.MalformedEmpty},
	}

	seen := map[string]int{}

	for range 2000 {
		resp, _ := serveFault(t, rule)
		seen[resp.Fault]++
	}

	// 100% in total: a normal response never happens, every outcome does.
	if seen[""] != 0 {
		t.Errorf("rates add up to 100, got %d normal responses", seen[""])
	}

	for _, f := range []string{usecase.FaultDrop, usecase.FaultTimeout, usecase.FaultError, usecase.FaultEmpty} {
		if seen[f] == 0 {
			t.Errorf("fault %q never happened: %v", f, seen)
		}
	}
}

func TestMockService_ValidatesFault(t *testing.T) {
	t.Parallel()

	svc := usecase.NewMockService(newMockMockRepo(), newMockContractRepo(), newMockProviderRepo(),
		&mockContractEngine{}, &mockLLMGateway{})

	bad := &domain.MockRule{ProjectID: "p", Path: "/x", Method: "GET", FailRatePct: 70, Fault: &domain.Fault{DropRatePct: 40}}
	if _, err := svc.Create(bad); !errors.Is(err, usecase.ErrInvalidFault) {
		t.Errorf("want ErrInvalidFault, got %v", err)
	}

	if usecase.StatusFromError(usecase.ErrInvalidFault) != usecase.StatusBadRequest {
		t.Error("invalid fault must map to 400")
	}

	good := &domain.MockRule{ProjectID: "p", Path: "/x", Method: "GET", FailRatePct: 10, Fault: &domain.Fault{DropRatePct: 40}}
	if _, err := svc.Create(good); err != nil {
		t.Errorf("valid fault: %v", err)
	}
}

// faultyMCPEngine answers every request with a fixed body after rolling the
// fault of `calls` tool calls, like the real engine does for a batch.
type faultyMCPEngine struct {
	mockMCPEngine

	body   []byte
	faults []usecase.MCPFault
}

func (e *faultyMCPEngine) ExecuteWith(_, _ []byte, opts *usecase.MCPExecOptions) ([]byte, []string, error) {
	for i := range e.faults {
		if opts.Fault != nil {
			opts.Fault(&e.faults[i])
		}
	}

	return e.body, nil, nil
}

func serveMCPFault(t *testing.T, body string, faults ...usecase.MCPFault) (usecase.MCPResponse, *domain.RequestLog) {
	t.Helper()

	repo := newMockContractRepo()
	repo.AddVersion("p1", usecase.FormatMCP, "{}", "manual")

	logs := newMockLogRepo()
	engine := &faultyMCPEngine{body: []byte(body), faults: faults}
	svc := usecase.NewMCPServingService(repo, logs, engine)

	resp, err := svc.Handle("p1", []byte(`{}`), "")
	if err != nil {
		t.Fatalf("handle: %v", err)
	}

	entries := logs.ListLogs("p1", 1)
	if len(entries) == 0 {
		t.Fatal("no request log")
	}

	return resp, entries[0]
}

func TestMCPFault_Outcomes(t *testing.T) {
	t.Parallel()

	const rpc = `{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`

	resp, entry := serveMCPFault(t, rpc, usecase.MCPFault{DelayMs: 120, Settings: &domain.Fault{DropRatePct: 100}})
	if resp.Fault != usecase.FaultDrop || resp.Status != 0 || resp.DelayMs != 120 || entry.Fault != usecase.FaultDrop {
		t.Errorf("drop: %+v / %+v", resp, entry)
	}

	resp, _ = serveMCPFault(t, rpc, usecase.MCPFault{Settings: &domain.Fault{TimeoutRatePct: 100, TimeoutMs: 400}})
	if resp.Fault != usecase.FaultTimeout || resp.HoldMs != 400 {
		t.Errorf("timeout: %+v", resp)
	}

	resp, entry = serveMCPFault(t, rpc, usecase.MCPFault{ErrorRatePct: 100, Settings: &domain.Fault{ErrorStatuses: []int{503}}})
	if resp.Fault != usecase.FaultError || resp.Status != 503 || entry.StatusCode != 503 {
		t.Errorf("error status: %+v / %+v", resp, entry)
	}

	resp, _ = serveMCPFault(t, rpc, usecase.MCPFault{Settings: &domain.Fault{MalformedRatePct: 100}})
	if resp.Fault != usecase.FaultGarbage || json.Valid(resp.Body) || resp.Status != 200 {
		t.Errorf("garbage: %+v", resp)
	}

	resp, _ = serveMCPFault(t, rpc, usecase.MCPFault{Settings: &domain.Fault{MalformedRatePct: 100, MalformedMode: domain.MalformedEmpty}})
	if resp.Fault != usecase.FaultEmpty || resp.Body == nil || len(resp.Body) != 0 {
		t.Errorf("empty must be a non-nil empty body: %+v", resp)
	}

	resp, _ = serveMCPFault(t, rpc, usecase.MCPFault{Settings: &domain.Fault{MalformedRatePct: 100, MalformedMode: domain.MalformedTruncated}})
	if resp.Fault != usecase.FaultTruncated || string(resp.Body) != rpc || resp.Status != 200 {
		t.Errorf("truncated keeps the body for the adapter to cut: %+v", resp)
	}

	resp, _ = serveMCPFault(t, rpc, usecase.MCPFault{Settings: &domain.Fault{}})
	if resp.Fault != "" || resp.Status != 200 || string(resp.Body) != rpc {
		t.Errorf("no fault rolled: %+v", resp)
	}
}

func TestMCPFault_BatchUsesLargestDelayAndFirstFault(t *testing.T) {
	t.Parallel()

	resp, _ := serveMCPFault(t, `[]`,
		usecase.MCPFault{DelayMs: 50, Settings: &domain.Fault{}},
		usecase.MCPFault{DelayMs: 300, Settings: &domain.Fault{DropRatePct: 100}},
		usecase.MCPFault{DelayMs: 100, ErrorRatePct: 100, Settings: &domain.Fault{}},
	)

	if resp.DelayMs != 300 || resp.Fault != usecase.FaultDrop {
		t.Errorf("batch: largest delay and the first fault, got %+v", resp)
	}
}
