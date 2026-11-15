package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

type fakeLocal struct {
	mu   sync.Mutex
	logs []*domain.RequestLog
}

func (f *fakeLocal) Add(l *domain.RequestLog) {
	f.mu.Lock()
	defer f.mu.Unlock()

	l.ID = "local-id"
	f.logs = append(f.logs, l)
}

func (f *fakeLocal) ListLogs(string, int) []*domain.RequestLog {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.logs
}

func TestLogConfigFrom(t *testing.T) {
	t.Parallel()

	cfg, err := logConfigFrom(env(nil))
	if err != nil || cfg.mode != "local" || len(cfg.sinks) != 0 || cfg.exportBodies {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}

	cfg, err = logConfigFrom(env(map[string]string{"LOG_MODE": " Distributed "}))
	if err != nil || cfg.mode != "distributed" || len(cfg.sinks) != 1 || cfg.sinks[0] != "stdout" {
		t.Fatalf("distributed default sink: %+v %v", cfg, err)
	}

	cfg, err = logConfigFrom(env(map[string]string{
		"LOG_MODE": "local", "LOG_SINKS": "http, Elasticsearch,,", "LOG_EXPORT_BODIES": "TRUE",
		"LOG_HTTP_URL": "http://x", "LOG_ES_URL": "http://es", "LOG_ES_INDEX": "idx",
	}))
	if err != nil || len(cfg.sinks) != 2 || cfg.sinks[1] != "elasticsearch" || !cfg.exportBodies ||
		cfg.httpURL != "http://x" || cfg.esIndex != "idx" {
		t.Fatalf("explicit: %+v %v", cfg, err)
	}

	for _, bad := range []map[string]string{{"LOG_MODE": "cluster"}, {"LOG_SINKS": "syslog"}} {
		if _, err = logConfigFrom(env(bad)); err == nil {
			t.Errorf("%v must be rejected", bad)
		}
	}
}

func TestBuildLogPipeline_LocalKeepsTheStoreAndHasNoExporter(t *testing.T) {
	t.Parallel()

	local := &fakeLocal{}

	p, err := buildLogPipeline(&logConfig{mode: "local"}, local, &bytes.Buffer{})
	if err != nil || p.exporter != nil || p.repo != usecase.LogRepository(local) {
		t.Fatalf("pipeline = %+v err=%v", p, err)
	}

	hub := usecase.NewLogHub(p.repo, usecase.DefaultCapturePolicy(), p.options...)
	hub.Add(&domain.RequestLog{ProjectID: "p"})

	if len(local.logs) != 1 || hub.Info().Mode != "local" {
		t.Error("local mode must write to the data file store")
	}
}

func TestBuildLogPipeline_DistributedShipsToStdoutAndSkipsTheStore(t *testing.T) {
	t.Parallel()

	local := &fakeLocal{}

	var out syncBuffer

	p, err := buildLogPipeline(&logConfig{mode: "distributed", sinks: []string{"stdout"}}, local, &out)
	if err != nil || p.exporter == nil {
		t.Fatalf("pipeline = %+v err=%v", p, err)
	}

	hub := usecase.NewLogHub(p.repo, usecase.DefaultCapturePolicy(), p.options...)
	hub.Add(&domain.RequestLog{ProjectID: "p", Method: "GET", Path: "/a", StatusCode: 200, Timestamp: time.Now()})

	err = p.exporter.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(local.logs) != 0 {
		t.Error("distributed mode must not write logs to the data file")
	}

	if !strings.Contains(out.String(), `"url.path":"/a"`) || !strings.HasSuffix(out.String(), "\n") {
		t.Errorf("stdout = %q", out.String())
	}

	if got := hub.ListLogs("p", 10); len(got) != 1 || got[0].ID == "" {
		t.Errorf("the UI window must still work: %+v", got)
	}

	if info := hub.Info(); info.Mode != "distributed" || info.Sinks[0] != "stdout" {
		t.Errorf("info = %+v", info)
	}
}

func TestBuildLogPipeline_ConfiguresHTTPAndElasticsearch(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex

	paths := map[string]bool{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths[r.URL.Path] = true
		mu.Unlock()

		_, _ = w.Write([]byte(`{"errors":false}`))
	}))
	defer srv.Close()

	cfg := logConfig{
		mode: "distributed", sinks: []string{"http", "elasticsearch"},
		httpURL: srv.URL + "/ingest", esURL: srv.URL,
	}

	p, err := buildLogPipeline(&cfg, &fakeLocal{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}

	hub := usecase.NewLogHub(p.repo, usecase.DefaultCapturePolicy(), p.options...)
	hub.Add(&domain.RequestLog{ProjectID: "p", Timestamp: time.Now()})

	_ = p.exporter.Close(context.Background())

	mu.Lock()
	defer mu.Unlock()

	if !paths["/ingest"] || !paths["/_bulk"] {
		t.Errorf("paths hit = %v", paths)
	}
}

func TestBuildLogPipeline_MissingSinkSettingsAreErrors(t *testing.T) {
	t.Parallel()

	for _, sink := range []string{"http", "elasticsearch"} {
		_, err := buildLogPipeline(&logConfig{mode: "distributed", sinks: []string{sink}}, &fakeLocal{}, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), sink) {
			t.Errorf("sink %s: err = %v", sink, err)
		}
	}
}

func TestIsTrue(t *testing.T) {
	t.Parallel()

	for v, want := range map[string]bool{"1": true, "true": true, " YES ": true, "on": true, "": false, "0": false, "no": false} {
		if isTrue(v) != want {
			t.Errorf("isTrue(%q) != %v", v, want)
		}
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.b.String()
}
