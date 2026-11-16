package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/examples"
)

// isolateTelemetry points the settings at an empty directory and clears the
// variables that would change them. Tests that call it cannot run in parallel.
func isolateTelemetry(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	t.Setenv("OPENAPI_MOCKER_CONFIG_DIR", dir)
	t.Setenv("OPENAPI_MOCKER_TELEMETRY", "")
	t.Setenv("OPENAPI_MOCKER_TELEMETRY_URL", "")
	t.Setenv("DO_NOT_TRACK", "")

	return dir
}

func runTelemetryArgs(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code = runTelemetry(args, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

func TestTelemetryCommand_StatusEnableDisable(t *testing.T) {
	isolateTelemetry(t)

	code, out, _ := runTelemetryArgs(t, "status")
	if code != exitOK || !strings.Contains(out, "Usage ping: off") {
		t.Fatalf("expected off by default, got %d %q", code, out)
	}

	code, out, _ = runTelemetryArgs(t, "enable")
	if code != exitOK || !strings.Contains(out, "Install id: ") {
		t.Fatalf("enable: %d %q", code, out)
	}

	_, out, _ = runTelemetryArgs(t, "status")
	if !strings.Contains(out, "Usage ping: on") {
		t.Fatalf("expected on after enable, got %q", out)
	}

	code, _, _ = runTelemetryArgs(t, "disable")
	if code != exitOK {
		t.Fatalf("disable returned %d", code)
	}

	_, out, _ = runTelemetryArgs(t, "status")
	if !strings.Contains(out, "Usage ping: off") || strings.Contains(out, "Install id") {
		t.Fatalf("expected off without an id after disable, got %q", out)
	}
}

func TestTelemetryCommand_ShowPrintsTheWholePing(t *testing.T) {
	isolateTelemetry(t)

	code, out, _ := runTelemetryArgs(t, "show")
	if code != exitOK {
		t.Fatalf("show returned %d", code)
	}

	for _, field := range []string{`"schema"`, `"install_id"`, `"version"`, `"os"`, `"arch"`, `"mode"`, `"protocols"`} {
		if !strings.Contains(out, field) {
			t.Errorf("show output lacks %s:\n%s", field, out)
		}
	}
}

func TestTelemetryCommand_UsageErrors(t *testing.T) {
	isolateTelemetry(t)

	for _, args := range [][]string{{}, {"nope"}, {"status", "extra"}} {
		code, _, errOut := runTelemetryArgs(t, args...)
		if code != exitUsage || !strings.Contains(errOut, "Usage: openapi-mocker telemetry") {
			t.Errorf("args %v: expected a usage error, got %d %q", args, code, errOut)
		}
	}
}

func TestProtocolOf(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		examples.Petstore:                      protocolOpenAPI,
		"type Query { a: String }":             protocolGraphQL,
		`{"mcpServer": {"name": "x"}}`:         protocolMCP,
		"syntax = \"proto3\";\nmessage A {}\n": protocolGRPC,
	}

	for raw, want := range cases {
		if got := protocolOf(raw); got != want {
			t.Errorf("protocolOf(%.30q) = %q, want %q", raw, got, want)
		}
	}
}

func TestStartTelemetry_SendsOnlyWhenOptedIn(t *testing.T) {
	isolateTelemetry(t)

	var hits atomic.Int32

	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer collector.Close()

	t.Setenv("OPENAPI_MOCKER_TELEMETRY_URL", collector.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startTelemetry(ctx, "serve", "", func() []string { return []string{protocolOpenAPI} })
	time.Sleep(200 * time.Millisecond)

	if hits.Load() != 0 {
		t.Fatalf("a ping was sent without an opt-in")
	}

	t.Setenv("OPENAPI_MOCKER_TELEMETRY", "1")
	startTelemetry(ctx, "serve", "", func() []string { return []string{protocolOpenAPI} })

	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if hits.Load() == 0 {
		t.Fatal("expected a ping after the opt-in")
	}

	t.Setenv("DO_NOT_TRACK", "1")

	before := hits.Load()

	startTelemetry(ctx, "serve", "", func() []string { return []string{protocolOpenAPI} })
	time.Sleep(200 * time.Millisecond)

	if hits.Load() != before {
		t.Fatal("DO_NOT_TRACK must stop the ping")
	}
}

func TestStartTelemetry_NoCollectorSendsNothing(t *testing.T) {
	isolateTelemetry(t)
	t.Setenv("OPENAPI_MOCKER_TELEMETRY", "1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// With no endpoint this must return at once and start nothing.
	startTelemetry(ctx, "serve", "", func() []string { return nil })
}

func TestStoreProtocols_ListsActiveContractKinds(t *testing.T) {
	t.Parallel()

	a, err := buildApp(t.TempDir(), true)
	if err != nil {
		t.Fatalf("build app: %v", err)
	}

	if got := storeProtocols(a.store); len(got) != 0 {
		t.Fatalf("an empty store serves no protocol, got %v", got)
	}

	_, err = bootstrapSpec(a, examples.Petstore, "")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	got := storeProtocols(a.store)
	if len(got) != 1 || got[0] != protocolOpenAPI {
		t.Fatalf("expected [openapi], got %v", got)
	}
}

func TestTelemetryHint_SilentWhenNotATerminal(t *testing.T) {
	isolateTelemetry(t)
	t.Setenv("OPENAPI_MOCKER_TELEMETRY_URL", "https://collector.example/ping")

	var out bytes.Buffer

	telemetryHint(&out, "")

	if out.Len() != 0 {
		t.Fatalf("the hint must not appear in piped output, got %q", out.String())
	}
}
