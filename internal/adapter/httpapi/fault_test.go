package httpapi_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// faultServer serves a GET /users rule over a real listener, because dropped
// connections and truncated bodies cannot be observed with a recorder.
func faultServer(t *testing.T, rule *domain.MockRule) (srv *httptest.Server, url string) {
	t.Helper()

	rule.Path, rule.Method = "/users", http.MethodGet
	if rule.StatusCode == 0 {
		rule.StatusCode = http.StatusOK
	}

	h, id := newTemplatingRouter(t, "yaml", stateOpenAPI, rule)
	srv = httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return srv, srv.URL + "/mock/" + id + "/users"
}

func TestFaultInjection_DroppedConnection(t *testing.T) {
	t.Parallel()

	_, url := faultServer(t, &domain.MockRule{Body: `[]`, Fault: &domain.Fault{DropRatePct: 100}})

	resp, err := http.Get(url) //nolint:gosec,noctx // test server URL.
	if err == nil {
		resp.Body.Close()
		t.Fatalf("a dropped connection must fail the request, got %d", resp.StatusCode)
	}
}

func TestFaultInjection_TimeoutHoldsThenDrops(t *testing.T) {
	t.Parallel()

	_, url := faultServer(t, &domain.MockRule{Body: `[]`, Fault: &domain.Fault{TimeoutRatePct: 100, TimeoutMs: 600}})

	// A client with a shorter timeout gives up first.
	client := &http.Client{Timeout: 150 * time.Millisecond}

	_, err := client.Get(url) //nolint:noctx,bodyclose // the request must fail.
	if err == nil {
		t.Fatal("the request must time out")
	}

	var netErr interface{ Timeout() bool }
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Errorf("want a client timeout, got %v", err)
	}

	// A patient client is held for TimeoutMs and then loses the connection.
	start := time.Now()

	resp, err := http.Get(url) //nolint:gosec,noctx // test server URL.
	if err == nil {
		resp.Body.Close()
		t.Fatal("after the hold the connection must be dropped")
	}

	if held := time.Since(start); held < 500*time.Millisecond {
		t.Errorf("request was held only %v", held)
	}
}

func TestFaultInjection_MalformedBodies(t *testing.T) {
	t.Parallel()

	const body = `{"name":"Ada","email":"ada@example.com"}`

	t.Run("garbage", func(t *testing.T) {
		t.Parallel()

		_, url := faultServer(t, &domain.MockRule{Body: body, Fault: &domain.Fault{MalformedRatePct: 100}})

		resp, err := http.Get(url) //nolint:gosec,noctx // test server URL.
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		got, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || json.Valid(got) || resp.Header.Get("X-Mock-Source") != "chaos-malformed" {
			t.Errorf("garbage body: %d %q %v", resp.StatusCode, got, resp.Header)
		}
	})

	t.Run("empty", func(t *testing.T) {
		t.Parallel()

		_, url := faultServer(t, &domain.MockRule{Body: body, Fault: &domain.Fault{MalformedRatePct: 100, MalformedMode: domain.MalformedEmpty}})

		resp, err := http.Get(url) //nolint:gosec,noctx // test server URL.
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		got, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || len(got) != 0 {
			t.Errorf("empty body: %d %q", resp.StatusCode, got)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		t.Parallel()

		_, url := faultServer(t, &domain.MockRule{Body: body, Fault: &domain.Fault{MalformedRatePct: 100, MalformedMode: domain.MalformedTruncated}})

		resp, err := http.Get(url) //nolint:gosec,noctx // test server URL.
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		// The error text depends on the OS (unexpected EOF, connection reset,
		// "forcibly closed" on Windows); that the read fails is what matters.
		got, err := io.ReadAll(resp.Body)
		if err == nil {
			t.Errorf("a truncated body must end in an error, got %q", got)
		}

		if len(got) >= len(body) {
			t.Errorf("only part of the body may arrive, got %d of %d bytes", len(got), len(body))
		}
	})
}

func TestFaultInjection_LatencyAndErrors(t *testing.T) {
	t.Parallel()

	_, url := faultServer(t, &domain.MockRule{Body: `[]`, DelayMs: 200, Fault: &domain.Fault{JitterMs: 50}})

	start := time.Now()

	resp, err := http.Get(url) //nolint:gosec,noctx // test server URL.
	if err != nil {
		t.Fatal(err)
	}

	resp.Body.Close()

	if took := time.Since(start); took < 190*time.Millisecond {
		t.Errorf("latency must be at least the base delay, took %v", took)
	}

	_, url = faultServer(t, &domain.MockRule{Body: `[]`, FailRatePct: 100, Fault: &domain.Fault{ErrorStatuses: []int{429}}})

	resp, err = http.Get(url) //nolint:gosec,noctx // test server URL.
	if err != nil {
		t.Fatal(err)
	}

	resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("injected error status: got %d", resp.StatusCode)
	}
}

func TestFaultInjection_ClientGivingUpDuringDelayIsHarmless(t *testing.T) {
	t.Parallel()

	_, url := faultServer(t, &domain.MockRule{Body: `[]`, DelayMs: 2000})

	client := &http.Client{Timeout: 100 * time.Millisecond}

	_, err := client.Get(url) //nolint:noctx,bodyclose // the request must fail.
	if err == nil {
		t.Fatal("the request must time out")
	}
}

func TestRouter_FaultValidationEndToEnd(t *testing.T) {
	t.Parallel()

	h, id := newTemplatingRouter(t, "yaml", stateOpenAPI)
	rules := "/api/projects/" + id + "/mocks"

	bad := `{"path":"/users","method":"GET","statusCode":200,"body":"[]","failRatePct":80,"fault":{"dropRatePct":30}}`
	if c := call(t, h, http.MethodPost, rules, bad).Code; c != http.StatusBadRequest {
		t.Errorf("rates above 100%% in total must be 400, got %d", c)
	}

	good := `{"path":"/users","method":"GET","statusCode":200,"body":"[]","failRatePct":20,` +
		`"fault":{"dropRatePct":30,"jitterMs":25,"errorStatuses":[502],"malformedMode":"empty","malformedRatePct":10}}`

	rec := call(t, h, http.MethodPost, rules, good)
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid fault: %d %s", rec.Code, rec.Body.String())
	}

	var created domain.MockRule

	decodeJSON(t, rec, &created)

	if created.Fault == nil || created.Fault.DropRatePct != 30 || created.Fault.MalformedMode != domain.MalformedEmpty {
		t.Errorf("fault must round-trip: %+v", created.Fault)
	}

	update := `{"path":"/users","method":"GET","statusCode":200,"body":"[]","fault":{"timeoutMs":999999}}`
	if c := call(t, h, http.MethodPut, "/api/mocks/"+created.ID, update).Code; c != http.StatusBadRequest {
		t.Errorf("invalid fault on update must be 400, got %d", c)
	}
}

// mcpFaultURL serves a one-tool MCP manifest whose tool has the given fault.
func mcpFaultURL(t *testing.T, fault string) string {
	t.Helper()

	manifest := `{"mcpServer":{"name":"demo","tools":[{"name":"weather","fault":` + fault +
		`,"mockResponses":{"default":{"content":[{"type":"text","text":"sunny"}]}}}]}}`

	h, id := newTemplatingRouter(t, "mcp", manifest)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return srv.URL + "/mock/" + id + "/mcp"
}

const mcpWeatherCall = `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"weather","arguments":{}}}`

func postMCP(url string, client *http.Client) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(mcpWeatherCall)) //nolint:noctx // test.
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")

	return client.Do(req) //nolint:gosec // test server URL.
}

func TestMCPFaultInjection_OverRealConnections(t *testing.T) {
	t.Parallel()

	t.Run("dropped connection", func(t *testing.T) {
		t.Parallel()

		resp, err := postMCP(mcpFaultURL(t, `{"dropRatePct":100}`), http.DefaultClient)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("a dropped connection must fail the request, got %d", resp.StatusCode)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		t.Parallel()

		_, err := postMCP(mcpFaultURL(t, `{"timeoutRatePct":100,"timeoutMs":600}`), &http.Client{Timeout: 150 * time.Millisecond}) //nolint:bodyclose // must fail.
		if err == nil {
			t.Fatal("the request must time out")
		}
	})

	t.Run("error status with JSON-RPC error", func(t *testing.T) {
		t.Parallel()

		resp, err := postMCP(mcpFaultURL(t, `{"errorRatePct":100,"errorStatuses":[503]}`), http.DefaultClient)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		got, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(got), `"error"`) ||
			!strings.Contains(string(got), `"id":7`) || resp.Header.Get("X-Mock-Fault") != "error" {
			t.Errorf("injected error: %d %s %v", resp.StatusCode, got, resp.Header)
		}
	})

	t.Run("garbage and truncated bodies", func(t *testing.T) {
		t.Parallel()

		resp, err := postMCP(mcpFaultURL(t, `{"malformedRatePct":100}`), http.DefaultClient)
		if err != nil {
			t.Fatal(err)
		}

		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK || json.Valid(got) {
			t.Errorf("garbage: %d %q", resp.StatusCode, got)
		}

		resp, err = postMCP(mcpFaultURL(t, `{"malformedRatePct":100,"malformedMode":"truncated"}`), http.DefaultClient)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		_, err = io.ReadAll(resp.Body)
		if err == nil {
			t.Error("a truncated body must end in an error")
		}
	})

	t.Run("latency and no fault", func(t *testing.T) {
		t.Parallel()

		start := time.Now()

		resp, err := postMCP(mcpFaultURL(t, `{"delayMs":200}`), http.DefaultClient)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		got, _ := io.ReadAll(resp.Body)
		if time.Since(start) < 190*time.Millisecond || !strings.Contains(string(got), "sunny") {
			t.Errorf("delayed answer: %v %s", time.Since(start), got)
		}
	})
}
