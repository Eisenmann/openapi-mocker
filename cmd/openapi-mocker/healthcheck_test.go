package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunHealthcheck(t *testing.T) {
	t.Parallel()

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer healthy.Close()

	sick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer sick.Close()

	var out syncBuffer

	if code := runHealthcheck([]string{"--url", healthy.URL}, &out); code != exitOK {
		t.Errorf("healthy = %d: %s", code, out.String())
	}

	if code := runHealthcheck([]string{"--url", sick.URL, "--timeout", "1s"}, &out); code != exitIOFailure {
		t.Errorf("unhealthy = %d", code)
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL

	dead.Close()

	if code := runHealthcheck([]string{"--url", url}, &out); code != exitIOFailure {
		t.Errorf("unreachable = %d", code)
	}

	for _, bad := range [][]string{{"--url"}, {"--timeout", "soon"}, {"--timeout", "0s"}, {"--what", "x"}, {"--url", "ht tp://x"}} {
		if code := runHealthcheck(bad, &out); code != exitUsage {
			t.Errorf("%v = %d", bad, code)
		}
	}

	if !strings.Contains(out.String(), "Usage: openapi-mocker healthcheck") {
		t.Error("usage text missing")
	}
}

func TestHealthcheckFollowsPort(t *testing.T) {
	t.Setenv("PORT", "9191")

	opts, err := parseHealthArgs(nil)
	if err != nil || opts.url != "http://127.0.0.1:9191/healthz" {
		t.Errorf("opts = %+v %v", opts, err)
	}
}

func TestRunSubcommand(t *testing.T) {
	t.Parallel()

	if code, handled := runSubcommand("version", nil); !handled || code != exitOK {
		t.Errorf("version: %d %v", code, handled)
	}

	if _, handled := runSubcommand("unknown", nil); handled {
		t.Error("an unknown word is not a subcommand")
	}
}
