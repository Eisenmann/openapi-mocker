package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const serveSpecYAML = `openapi: 3.0.0
info: {title: T, version: "1"}
paths:
  /api/v1/ping:
    post:
      responses:
        '200': {description: ok, content: {application/json: {example: {ok: true}}}}
`

const serveSpecYAMLv2 = serveSpecYAML + `  /added:
    get:
      responses:
        '200': {description: ok, content: {application/json: {example: {added: true}}}}
`

func freePort(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	defer l.Close()

	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func tempSpec(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "spec.yaml")

	err := os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	return path
}

func TestParseServeArgs(t *testing.T) {
	t.Setenv("PORT", "")

	opts, err := parseServeArgs([]string{"spec.yaml", "--port=9000", "--validation", "warn", "--watch", "--data-dir", "d"})
	if err != nil {
		t.Fatal(err)
	}

	if opts.spec != "spec.yaml" || opts.port != "9000" || opts.validation != "warn" || !opts.watch || opts.dataDir != "d" {
		t.Errorf("opts = %+v", opts)
	}

	opts, err = parseServeArgs([]string{"--port", "9001", "spec.json"})
	if err != nil || opts.spec != "spec.json" || opts.port != "9001" {
		t.Errorf("flags before the file: %+v %v", opts, err)
	}

	t.Setenv("PORT", "7000")

	opts, err = parseServeArgs([]string{"s.yaml"})
	if err != nil || opts.port != "7000" {
		t.Errorf("PORT default: %+v %v", opts, err)
	}

	opts, err = parseServeArgs([]string{"--demo", "--port=9002"})
	if err != nil || !opts.demo || opts.spec != "" || opts.port != "9002" {
		t.Errorf("--demo: %+v %v", opts, err)
	}

	opts, err = parseServeArgs([]string{"--stateful", "s.yaml"})
	if err != nil || !opts.stateful || opts.spec != "s.yaml" {
		t.Errorf("--stateful: %+v %v", opts, err)
	}

	for _, bad := range [][]string{
		{},
		{"--stateful"},
		{"--demo", "s.yaml"},
		{"--demo", "--watch"},
		{"a", "b"},
		{"a", "--port"},
		{"a", "--port", "x"},
		{"a", "--port", "70000"},
		{"a", "--validation", "strict"},
		{"a", "--nope", "1"},
	} {
		_, err = parseServeArgs(bad)
		if err == nil {
			t.Errorf("%v must be rejected", bad)
		}
	}
}

func TestServeHandler_Routing(t *testing.T) {
	t.Parallel()

	var got string

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.URL.Path })
	h := serveHandler(next, "pid")

	for in, want := range map[string]string{
		"/users/1":                              "/mock/pid/users/1",
		"/":                                     "/mock/pid",
		"/api/v1/ping":                          "/mock/pid/api/v1/ping",
		"/healthz":                              "/healthz",
		"/__mocker/api/projects/default/verify": "/api/projects/pid/verify",
		"/__mocker/api/projects/default":        "/api/projects/pid",
		"/__mocker/api/projects/other/verify":   "/api/projects/other/verify",
		"/__mocker/metrics":                     "/metrics",
		"/__mocker/api/projects/default/logs/stream": "/api/projects/pid/logs/stream",
	} {
		got = ""

		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, in, nil))

		if got != want {
			t.Errorf("%s -> %q, want %q", in, got, want)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/__mocker", nil))

	if !strings.Contains(rec.Body.String(), `"projectId":"pid"`) {
		t.Errorf("info = %s", rec.Body.String())
	}
}

// startServe runs serveSpec in the background and waits until it is healthy.
func startServe(t *testing.T, opts *serveOptions) (base string, stderr *syncBuffer, stop func() int) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	stderr = &syncBuffer{}
	done := make(chan int, 1)

	go func() { done <- serveSpec(ctx, opts, stderr) }()

	base = "http://127.0.0.1:" + opts.port

	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/healthz") //nolint:noctx // test helper.
		if err == nil {
			resp.Body.Close()
			break
		}

		select {
		case code := <-done:
			t.Fatalf("server stopped early with %d: %s", code, stderr.String())
		case <-time.After(50 * time.Millisecond):
		}
	}

	return base, stderr, func() int {
		cancel()

		select {
		case code := <-done:
			return code
		case <-time.After(15 * time.Second):
			t.Fatal("server did not stop")

			return -1
		}
	}
}

func fetch(t *testing.T, method, url, body string) (int, string) {
	t.Helper()

	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, string(data)
}

func TestServeSpec_ServesAndVerifies(t *testing.T) {
	opts := &serveOptions{spec: tempSpec(t, serveSpecYAML), port: freePort(t), dataDir: "", validation: "", watch: false, stateful: false, demo: false}
	base, stderr, stop := startServe(t, opts)

	code, body := fetch(t, http.MethodPost, base+"/api/v1/ping", `{"a":1}`)
	if code != http.StatusOK || !strings.Contains(body, `"ok"`) {
		t.Fatalf("mock: %d %s", code, body)
	}

	code, body = fetch(t, http.MethodPost, base+"/__mocker/api/projects/default/verify?strict=true",
		`{"method":"POST","path":"/api/v1/ping","body":{"json":{"a":1}},"exactly":1}`)
	if code != http.StatusOK || !strings.Contains(body, `"passed":true`) {
		t.Errorf("verify: %d %s", code, body)
	}

	code, _ = fetch(t, http.MethodPost, base+"/__mocker/api/projects/default/verify?strict=true",
		`{"method":"GET","path":"/api/v1/ping"}`)
	if code != http.StatusConflict {
		t.Errorf("failed verify = %d", code)
	}

	// The UI is not served: the root is the mock.
	code, _ = fetch(t, http.MethodGet, base+"/", "")
	if code == http.StatusOK {
		t.Error("the root must be the mock, not the web UI")
	}

	if exit := stop(); exit != exitOK {
		t.Errorf("exit = %d: %s", exit, stderr.String())
	}

	if !strings.Contains(stderr.String(), "POST    /api/v1/ping") {
		t.Errorf("banner = %s", stderr.String())
	}
}

func TestServeSpec_WatchReloads(t *testing.T) {
	spec := tempSpec(t, serveSpecYAML)
	opts := &serveOptions{spec: spec, port: freePort(t), dataDir: "", validation: "", watch: true, stateful: false, demo: false}
	base, stderr, stop := startServe(t, opts)

	if code, _ := fetch(t, http.MethodGet, base+"/added", ""); code == http.StatusOK {
		t.Fatal("/added must not exist yet")
	}

	err := os.WriteFile(spec, []byte(serveSpecYAMLv2), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	waitFor(t, func() bool { return strings.Contains(stderr.String(), "reloaded") })

	if code, body := fetch(t, http.MethodGet, base+"/added", ""); code != http.StatusOK {
		t.Errorf("after reload: %d %s", code, body)
	}

	// A broken file is reported and the previous spec keeps being served.
	err = os.WriteFile(spec, []byte("openapi: [broken"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	waitFor(t, func() bool { return strings.Contains(stderr.String(), "reload failed") })

	if code, _ := fetch(t, http.MethodGet, base+"/added", ""); code != http.StatusOK {
		t.Errorf("a broken reload must keep the old spec: %d", code)
	}

	stop()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("condition not met in time")
}

func TestServeSpec_KeepsDataDir(t *testing.T) {
	dir := t.TempDir()
	spec := tempSpec(t, serveSpecYAML)

	for range 2 {
		opts := &serveOptions{spec: spec, port: freePort(t), dataDir: dir, validation: "warn", watch: false, stateful: false, demo: false}
		_, _, stop := startServe(t, opts)

		if exit := stop(); exit != exitOK {
			t.Fatalf("exit = %d", exit)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "db.json")); err != nil {
		t.Errorf("data dir was not used: %v", err)
	}
}

func TestServeSpec_BadInputs(t *testing.T) {
	t.Parallel()

	var out syncBuffer

	opts := &serveOptions{spec: filepath.Join(t.TempDir(), "missing.yaml"), port: "1", dataDir: "", validation: "", watch: false, stateful: false, demo: false}
	if code := serveSpec(context.Background(), opts, &out); code != exitUsage {
		t.Errorf("missing file = %d", code)
	}

	opts.spec = tempSpec(t, "this is not a contract")
	if code := serveSpec(context.Background(), opts, &out); code != exitUsage || !strings.Contains(out.String(), "invalid spec") {
		t.Errorf("invalid spec = %d: %s", code, out.String())
	}
}

func TestRunServe_UsageError(t *testing.T) {
	t.Parallel()

	var out syncBuffer

	if code := runServe(nil, &out); code != exitUsage || !strings.Contains(out.String(), "Usage: openapi-mocker serve") {
		t.Errorf("code=%d out=%s", code, out.String())
	}
}

func TestServeSpec_DemoPetstore(t *testing.T) {
	opts := &serveOptions{
		spec: "", port: freePort(t), dataDir: "", validation: "", watch: false, stateful: false, demo: true,
	}
	base, stderr, stop := startServe(t, opts)

	code, body := fetch(t, http.MethodGet, base+"/pets", "")
	if code != http.StatusOK || strings.Count(body, `"name"`) != 3 || !strings.Contains(body, `"Rex"`) {
		t.Fatalf("seeded pets: %d %s", code, body)
	}

	code, body = fetch(t, http.MethodPost, base+"/pets", `{"name":"Bella","tag":"dog"}`)
	if code != http.StatusCreated || !strings.Contains(body, `"id":4`) {
		t.Fatalf("add a pet: %d %s", code, body)
	}

	code, body = fetch(t, http.MethodGet, base+"/pets/4", "")
	if code != http.StatusOK || !strings.Contains(body, `"Bella"`) {
		t.Errorf("get the new pet: %d %s", code, body)
	}

	code, _ = fetch(t, http.MethodDelete, base+"/pets/1", "")
	if code != http.StatusNoContent {
		t.Errorf("remove a pet = %d", code)
	}

	code, _ = fetch(t, http.MethodGet, base+"/pets/1", "")
	if code != http.StatusNotFound {
		t.Errorf("removed pet = %d", code)
	}

	if exit := stop(); exit != exitOK {
		t.Errorf("exit = %d: %s", exit, stderr.String())
	}

	out := stderr.String()
	tryIt := "Try it:  curl http://localhost:" + opts.port + "/pets"

	for _, want := range []string{"serving the built-in Petstore", "stateful:", tryIt, "Press Ctrl+C"} {
		if !strings.Contains(out, want) {
			t.Errorf("banner lacks %q: %s", want, out)
		}
	}
}

func TestServeSpec_StatefulFlag(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "..", "examples", "petstore.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	opts := &serveOptions{
		spec: tempSpec(t, string(spec)), port: freePort(t), dataDir: "", validation: "", watch: false,
		stateful: true, demo: false,
	}
	base, _, stop := startServe(t, opts)

	code, body := fetch(t, http.MethodGet, base+"/pets", "")
	if code != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Fatalf("a stateful mock starts empty: %d %s", code, body)
	}

	fetch(t, http.MethodPost, base+"/pets", `{"name":"Rex"}`)

	_, body = fetch(t, http.MethodGet, base+"/pets", "")
	if !strings.Contains(body, `"Rex"`) {
		t.Errorf("list after create = %s", body)
	}

	stop()
}

func TestServeSpec_WithoutStatefulStaysStatic(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "..", "examples", "petstore.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	opts := &serveOptions{
		spec: tempSpec(t, string(spec)), port: freePort(t), dataDir: "", validation: "", watch: false,
		stateful: false, demo: false,
	}
	base, _, stop := startServe(t, opts)

	fetch(t, http.MethodPost, base+"/pets", `{"name":"Zed"}`)

	_, body := fetch(t, http.MethodGet, base+"/pets", "")
	if strings.Contains(body, `"Zed"`) {
		t.Errorf("a stateless mock must not remember the pet: %s", body)
	}

	stop()
}

func TestTryItCommand(t *testing.T) {
	endpoints := []usecase.Endpoint{
		{Method: http.MethodPost, Path: "/pets"},
		{Method: http.MethodGet, Path: "/pets/{petId}"},
		{Method: http.MethodGet, Path: "/pets"},
	}

	if got := tryItCommand(endpoints, "http://localhost:8080"); got != "curl http://localhost:8080/pets" {
		t.Errorf("tryItCommand = %q", got)
	}

	if got := tryItCommand(endpoints[:2], "http://localhost:8080"); got != "" {
		t.Errorf("no parameterless GET: %q", got)
	}
}
