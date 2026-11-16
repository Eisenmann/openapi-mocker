package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const mcpTestSpec = `openapi: 3.0.3
info: {title: Pets, version: "1.0"}
paths:
  /pets:
    get:
      operationId: listPets
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {type: array, items: {type: string}}
`

type mcpSession struct {
	t   *testing.T
	in  io.Writer
	out *bufio.Reader
	id  int
}

func (s *mcpSession) call(method string, params any) map[string]any {
	s.t.Helper()

	s.id++

	req, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": s.id, "method": method, "params": params})
	if err != nil {
		s.t.Fatal(err)
	}

	_, err = s.in.Write(append(req, '\n'))
	if err != nil {
		s.t.Fatal(err)
	}

	line, err := s.out.ReadBytes('\n')
	if err != nil {
		s.t.Fatalf("read response: %v", err)
	}

	var res map[string]any

	err = json.Unmarshal(line, &res)
	if err != nil {
		s.t.Fatalf("response is not JSON: %v: %s", err, line)
	}

	return res
}

func (s *mcpSession) tool(name string, args map[string]any) map[string]any {
	s.t.Helper()

	res := s.call("tools/call", map[string]any{"name": name, "arguments": args})

	result, ok := res["result"].(map[string]any)
	if !ok {
		s.t.Fatalf("no result: %v", res)
	}

	return result
}

func TestMCPControl_EndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan int, 1)

	go func() {
		done <- serveMCP(ctx, &mcpOptions{port: "0", dataDir: ""}, inR, outW, io.Discard)
		_ = outW.Close()
	}()

	s := &mcpSession{t: t, in: inW, out: bufio.NewReader(outR), id: 0}

	init := s.call("initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "test", "version": "1"},
	})
	if init["error"] != nil {
		t.Fatalf("initialize: %v", init)
	}

	list := s.call("tools/list", map[string]any{})

	tools, _ := list["result"].(map[string]any)["tools"].([]any)
	if len(tools) < 15 {
		t.Fatalf("expected the control tools, got %d", len(tools))
	}

	created := s.tool("create_project", map[string]any{"name": "pets", "contract": mcpTestSpec})
	if created["isError"] == true {
		t.Fatalf("create_project failed: %v", created)
	}

	structured, _ := created["structuredContent"].(map[string]any)

	proj, _ := structured["project"].(map[string]any)
	mockURL, _ := proj["mock_url"].(string)
	if mockURL == "" {
		t.Fatalf("no mock_url in %v", structured)
	}

	rule := s.tool("add_mock_rule", map[string]any{
		"method": "get", "path": "/pets", "status_code": 200, "body": `["rex"]`,
	})
	if rule["isError"] == true {
		t.Fatalf("add_mock_rule failed: %v", rule)
	}

	resp, err := http.Get(mockURL + "/pets") //nolint:noctx,gosec // local test server.
	if err != nil {
		t.Fatal(err)
	}

	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if !strings.Contains(string(body), "rex") {
		t.Fatalf("mock did not serve the rule: %s", body)
	}

	verify := s.tool("verify_calls", map[string]any{"method": "GET", "path": "/pets", "exactly": 1})
	vs, _ := verify["structuredContent"].(map[string]any)

	if vs["passed"] != true {
		t.Fatalf("verify_calls should pass: %v", verify)
	}

	load := s.tool("run_load_test", map[string]any{"rate": 5, "ramp": "1s", "hold": "1s", "wait_seconds": 30})
	if load["isError"] == true {
		t.Fatalf("run_load_test failed: %v", load)
	}

	run, _ := load["structuredContent"].(map[string]any)
	if run["ended"] != true || run["totals"] == nil {
		t.Fatalf("the load run should have ended with totals: %v", run)
	}

	bad := s.tool("create_project", map[string]any{"name": "x", "bogus": 1})
	if bad["isError"] != true {
		t.Fatalf("unknown arguments must be an error: %v", bad)
	}

	_ = inW.Close()

	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("exit code %d", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("server did not stop when stdin closed")
	}
}

func TestParseMCPArgs(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"--port", "x"}, {"--port", "70000"}, {"extra"}, {"--nope"}} {
		_, err := parseMCPArgs(args)
		if err == nil {
			t.Errorf("%v: expected a usage error", args)
		}
	}

	opts, err := parseMCPArgs([]string{"--port", "8123", "--data-dir", "d"})
	if err != nil || opts.port != "8123" || opts.dataDir != "d" {
		t.Fatalf("got %+v, %v", opts, err)
	}
}
