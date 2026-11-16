package telemetry_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/telemetry"
)

func env(pairs ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = pairs[i+1]
	}

	return func(k string) string { return m[k] }
}

func TestLoad_DefaultIsOffAndUndecided(t *testing.T) {
	t.Parallel()

	s := telemetry.Load(env(), t.TempDir())

	if s.Enabled || s.Decided || s.InstallID != "" {
		t.Fatalf("expected off and undecided, got %+v", s)
	}
}

func TestEnableDisable_RoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	on, err := telemetry.Enable(dir)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}

	if !on.Enabled || len(on.InstallID) != 32 {
		t.Fatalf("expected enabled with a 32 character id, got %+v", on)
	}

	loaded := telemetry.Load(env(), dir)
	if !loaded.Enabled || loaded.InstallID != on.InstallID || loaded.Source != telemetry.SourceFile {
		t.Fatalf("expected the saved setting, got %+v", loaded)
	}

	again, err := telemetry.Enable(dir)
	if err != nil || again.InstallID != on.InstallID {
		t.Fatalf("enabling twice must keep the id: %+v %v", again, err)
	}

	err = telemetry.Disable(dir)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}

	off := telemetry.Load(env(), dir)
	if off.Enabled || !off.Decided || off.InstallID != "" {
		t.Fatalf("expected off, decided, no id, got %+v", off)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "telemetry.json"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}

	if string(raw) != "{\n  \"enabled\": false\n}\n" {
		t.Errorf("disabling must delete the install id, file is %q", raw)
	}

	fresh, err := telemetry.Enable(dir)
	if err != nil || fresh.InstallID == on.InstallID {
		t.Errorf("a new opt-in must get a new id: %+v %v", fresh, err)
	}
}

func TestLoad_DoNotTrackWinsOverEverything(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	_, err := telemetry.Enable(dir)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}

	s := telemetry.Load(env("DO_NOT_TRACK", "1", "OPENAPI_MOCKER_TELEMETRY", "1"), dir)
	if s.Enabled || s.Source != telemetry.SourceDoNotTrack {
		t.Fatalf("DO_NOT_TRACK must force telemetry off, got %+v", s)
	}
}

func TestLoad_EnvironmentSwitch(t *testing.T) {
	t.Parallel()

	on := telemetry.Load(env("OPENAPI_MOCKER_TELEMETRY", "true"), t.TempDir())
	if !on.Enabled || on.InstallID == "" || on.Source != telemetry.SourceEnvironment {
		t.Fatalf("expected enabled by the environment, got %+v", on)
	}

	dir := t.TempDir()

	_, err := telemetry.Enable(dir)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}

	off := telemetry.Load(env("OPENAPI_MOCKER_TELEMETRY", "0"), dir)
	if off.Enabled {
		t.Fatalf("expected the variable to turn a saved opt-in off, got %+v", off)
	}
}

func TestEnable_WithoutDirFails(t *testing.T) {
	t.Parallel()

	_, err := telemetry.Enable("")
	if err == nil {
		t.Fatal("expected an error when there is nowhere to save")
	}
}

func TestNewPayload_SortsAndDeduplicates(t *testing.T) {
	t.Parallel()

	p := telemetry.NewPayload("id", "1.2.3", telemetry.ModeServer, []string{"mcp", "openapi", "mcp"})

	if p.Schema != telemetry.SchemaVersion || len(p.Protocols) != 2 || p.Protocols[0] != "mcp" {
		t.Fatalf("unexpected payload %+v", p)
	}
}

func TestPayload_HasOnlyTheDocumentedFields(t *testing.T) {
	t.Parallel()

	b, err := json.Marshal(telemetry.NewPayload("id", "v", telemetry.ModeServe, []string{"openapi"}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]any

	err = json.Unmarshal(b, &got)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want := []string{"schema", "install_id", "version", "os", "arch", "mode", "protocols"}
	if len(got) != len(want) {
		t.Fatalf("payload has %d fields, documented %d: %s", len(got), len(want), b)
	}

	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing documented field %q", k)
		}
	}
}

func TestSend_PostsJSON(t *testing.T) {
	t.Parallel()

	var got telemetry.Payload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)

		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request %s %s", r.Method, r.Header.Get("Content-Type"))
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	want := telemetry.NewPayload("abc", "1.0.0", telemetry.ModeServe, []string{"graphql"})

	err := telemetry.Send(context.Background(), srv.URL, &want)
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	if got.InstallID != "abc" || got.Mode != telemetry.ModeServe || got.Protocols[0] != "graphql" {
		t.Fatalf("collector received %+v", got)
	}
}

func TestSend_RejectsPlainHTTPToARemoteHost(t *testing.T) {
	t.Parallel()

	err := telemetry.Send(context.Background(), "http://example.com/ping", &telemetry.Payload{})
	if err == nil {
		t.Fatal("expected http to a remote host to be refused")
	}
}

func TestSend_ErrorStatusAndRedirectsAreFailures(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32

	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer other.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer redirect.Close()

	err := telemetry.Send(context.Background(), redirect.URL, &telemetry.Payload{})
	if err == nil {
		t.Error("a redirect must not count as success")
	}

	if hits.Load() != 0 {
		t.Error("a redirect must not be followed")
	}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()

	err = telemetry.Send(context.Background(), broken.URL, &telemetry.Payload{})
	if err == nil {
		t.Error("a 500 must be reported")
	}
}

func TestEndpoint_EnvironmentOverridesBuiltin(t *testing.T) {
	t.Parallel()

	if got := telemetry.Endpoint(env(), "https://built.in"); got != "https://built.in" {
		t.Errorf("got %q", got)
	}

	if got := telemetry.Endpoint(env("OPENAPI_MOCKER_TELEMETRY_URL", "https://mine"), "https://built.in"); got != "https://mine" {
		t.Errorf("got %q", got)
	}

	if got := telemetry.Endpoint(env(), ""); got != "" {
		t.Errorf("no collector must give an empty endpoint, got %q", got)
	}
}

func TestRun_PingsAtStartAndStopsWithContext(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		telemetry.Run(ctx, srv.URL, 20*time.Millisecond,
			func() telemetry.Payload { return telemetry.Payload{} }, nil)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after the context ended")
	}

	if hits.Load() < 3 {
		t.Fatalf("expected a ping at the start and on the ticker, got %d", hits.Load())
	}
}
