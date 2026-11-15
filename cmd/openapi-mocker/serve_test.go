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

	for _, bad := range [][]string{
		{},
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
	opts := &serveOptions{spec: tempSpec(t, serveSpecYAML), port: freePort(t), dataDir: "", validation: "", watch: false}
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
	opts := &serveOptions{spec: spec, port: freePort(t), dataDir: "", validation: "", watch: true}
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
		opts := &serveOptions{spec: spec, port: freePort(t), dataDir: dir, validation: "warn", watch: false}
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

	opts := &serveOptions{spec: filepath.Join(t.TempDir(), "missing.yaml"), port: "1", dataDir: "", validation: "", watch: false}
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
