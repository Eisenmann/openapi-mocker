package usecase_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

type fakeSink struct {
	name string

	mu       sync.Mutex
	batches  [][]*domain.RequestLog
	failures int // fail this many sends first
	block    chan struct{}
	calls    int
}

func (f *fakeSink) Name() string { return f.name }

func (f *fakeSink) Send(_ context.Context, batch []*domain.RequestLog) error {
	if f.block != nil {
		<-f.block
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++

	if f.failures > 0 {
		f.failures--

		return errors.New("sink down")
	}

	f.batches = append(f.batches, append([]*domain.RequestLog(nil), batch...))

	return nil
}

func (f *fakeSink) entries() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	n := 0
	for _, b := range f.batches {
		n += len(b)
	}

	return n
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func TestLogExporter_BatchesAndFlushesOnClose(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{name: "a"}
	exp := usecase.NewLogExporter([]usecase.LogSink{sink},
		usecase.WithExportBatch(3), usecase.WithExportInterval(time.Hour))

	for i := range 7 {
		exp.Enqueue(&domain.RequestLog{Path: fmt.Sprint("/", i)})
	}

	// Two full batches go out by themselves; the last entry waits for Close.
	waitFor(t, "two full batches", func() bool { return sink.entries() == 6 })

	err := exp.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if sink.entries() != 7 {
		t.Errorf("entries after close = %d", sink.entries())
	}

	if err = exp.Close(context.Background()); !errors.Is(err, usecase.ErrExporterClosed) {
		t.Errorf("second close = %v", err)
	}

	stats, queued := exp.Stats()
	if stats[0].Sent != 7 || stats[0].Failed != 0 || queued != 0 {
		t.Errorf("stats = %+v queued=%d", stats, queued)
	}
}

func TestLogExporter_FlushesPartialBatchOnInterval(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{name: "a"}
	exp := usecase.NewLogExporter([]usecase.LogSink{sink}, usecase.WithExportInterval(10*time.Millisecond))

	defer func() { _ = exp.Close(context.Background()) }()

	exp.Enqueue(&domain.RequestLog{Path: "/x"})
	waitFor(t, "interval flush", func() bool { return sink.entries() == 1 })
}

func TestLogExporter_RetriesThenSucceeds(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{name: "a", failures: 2}
	exp := usecase.NewLogExporter([]usecase.LogSink{sink},
		usecase.WithExportInterval(5*time.Millisecond), usecase.WithExportRetryDelay(time.Millisecond))

	exp.Enqueue(&domain.RequestLog{})

	waitFor(t, "retried delivery", func() bool { return sink.entries() == 1 })

	_ = exp.Close(context.Background())

	if stats, _ := exp.Stats(); stats[0].Sent != 1 || stats[0].Failed != 0 {
		t.Errorf("stats = %+v", stats)
	}
}

func TestLogExporter_CountsFailureAndKeepsOtherSinksWorking(t *testing.T) {
	t.Parallel()

	down := &fakeSink{name: "down", failures: 100}
	up := &fakeSink{name: "up"}
	exp := usecase.NewLogExporter([]usecase.LogSink{down, up},
		usecase.WithExportInterval(5*time.Millisecond), usecase.WithExportRetryDelay(0))

	exp.Enqueue(&domain.RequestLog{})
	exp.Enqueue(&domain.RequestLog{})

	waitFor(t, "healthy sink delivery", func() bool { return up.entries() == 2 })
	_ = exp.Close(context.Background())

	stats, _ := exp.Stats()
	if stats[0].Sink != "down" || stats[0].Failed != 2 || stats[0].Sent != 0 || stats[1].Sent != 2 {
		t.Errorf("stats = %+v", stats)
	}

	names := exp.SinkNames()
	if len(names) != 2 || names[0] != "down" || names[1] != "up" {
		t.Errorf("names = %v", names)
	}
}

func TestLogExporter_DropsWhenQueueIsFullInsteadOfBlocking(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{name: "slow", block: make(chan struct{})}
	exp := usecase.NewLogExporter([]usecase.LogSink{sink},
		usecase.WithExportQueue(2), usecase.WithExportBatch(1), usecase.WithExportInterval(time.Millisecond))

	done := make(chan struct{})

	go func() {
		for range 50 {
			exp.Enqueue(&domain.RequestLog{})
		}

		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue blocked on a slow sink")
	}

	if stats, _ := exp.Stats(); stats[0].Dropped == 0 {
		t.Error("dropped entries must be counted")
	}

	close(sink.block)

	_ = exp.Close(context.Background())

	// After Close, new entries are dropped too.
	before, _ := exp.Stats()
	exp.Enqueue(&domain.RequestLog{})

	after, _ := exp.Stats()
	if after[0].Dropped != before[0].Dropped+1 {
		t.Error("an entry after Close must be dropped")
	}
}

func TestLogExporter_CloseHonoursContext(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{name: "stuck", block: make(chan struct{})}
	exp := usecase.NewLogExporter([]usecase.LogSink{sink}, usecase.WithExportInterval(time.Millisecond))
	exp.Enqueue(&domain.RequestLog{})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := exp.Close(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("close = %v", err)
	}

	close(sink.block)
}

func TestLogHub_ExportsEntriesAndStripsBodiesByDefault(t *testing.T) {
	t.Parallel()

	for _, withBodies := range []bool{false, true} {
		sink := &fakeSink{name: "s"}
		exp := usecase.NewLogExporter([]usecase.LogSink{sink}, usecase.WithExportInterval(5*time.Millisecond))
		hub := usecase.NewLogHub(&idRepo{}, usecase.DefaultCapturePolicy(),
			usecase.WithLogExport(exp, withBodies), usecase.WithLogMode("distributed"))

		l := entry("p", http.MethodPost, "/a", 200, 1)
		l.Request = capt(`{"k":"v"}`, http.Header{"Authorization": {"Bearer x"}})
		hub.Add(l)

		waitFor(t, "export", func() bool { return sink.entries() == 1 })

		got := sink.batches[0][0]
		if got.ID == "" {
			t.Error("the exported entry must carry its id")
		}

		if withBodies != (got.Request != nil) {
			t.Errorf("exportBodies=%v but request=%v", withBodies, got.Request)
		}

		if withBodies && got.Request.Headers["Authorization"][0] != "[redacted]" {
			t.Errorf("exported headers must be redacted: %v", got.Request.Headers)
		}

		info := hub.Info()
		if info.Mode != "distributed" || len(info.Sinks) != 1 || info.Sinks[0] != "s" || info.ExportBodies != withBodies {
			t.Errorf("info = %+v", info)
		}

		_ = exp.Close(context.Background())
	}
}

func TestLogHub_InfoDefaultsToLocal(t *testing.T) {
	t.Parallel()

	hub := usecase.NewLogHub(&idRepo{}, usecase.DefaultCapturePolicy())
	if info := hub.Info(); info.Mode != "local" || info.Sinks == nil || len(info.Sinks) != 0 {
		t.Errorf("info = %+v", info)
	}

	if info := usecase.NewLogService(&idRepo{}).Info(); info.Mode != "local" {
		t.Errorf("info without hub = %+v", info)
	}
}

func TestLogHub_ExportMetrics(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{name: "elasticsearch"}
	exp := usecase.NewLogExporter([]usecase.LogSink{sink}, usecase.WithExportInterval(5*time.Millisecond))
	hub := usecase.NewLogHub(&idRepo{}, usecase.DefaultCapturePolicy(), usecase.WithLogExport(exp, false))

	hub.Add(entry("p", http.MethodGet, "/a", 200, 1))
	waitFor(t, "export", func() bool { return sink.entries() == 1 })

	var buf strings.Builder

	if err := hub.WritePrometheus(&buf); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		`openapimocker_log_export_events_total{sink="elasticsearch",result="sent"} 1`,
		`openapimocker_log_export_events_total{sink="elasticsearch",result="failed"} 0`,
		`openapimocker_log_export_events_total{sink="elasticsearch",result="dropped"} 0`,
		"openapimocker_log_export_queue_length 0",
	} {
		if !slices.Contains(strings.Split(buf.String(), "\n"), want) {
			t.Errorf("missing %q in\n%s", want, buf.String())
		}
	}

	_ = exp.Close(context.Background())
}
