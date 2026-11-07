package usecase

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

const (
	defaultExportQueue    = 4096
	defaultExportBatch    = 200
	defaultExportInterval = time.Second
	defaultExportTimeout  = 10 * time.Second
	exportAttempts        = 3
	defaultRetryDelay     = 500 * time.Millisecond
)

// LogSink ships batches of request-log entries to an external system (a log
// file collector, Logstash, Elasticsearch/OpenSearch, ...).
type LogSink interface {
	// Name identifies the sink in metrics and in the UI.
	Name() string
	// Send delivers one batch. An error makes the exporter retry the batch.
	Send(ctx context.Context, batch []*domain.RequestLog) error
}

// ErrExporterClosed is returned by Close when the exporter is already closed.
var ErrExporterClosed = errors.New("log exporter is closed")

// LogExporter delivers log entries to sinks in the background. Enqueue never
// blocks the request that produced the entry: when the queue is full the entry
// is dropped and counted.
type LogExporter struct {
	sinks    []*sinkState
	queue    chan *domain.RequestLog
	batch    int
	interval time.Duration
	timeout  time.Duration
	retry    time.Duration

	closeOnce sync.Once
	closed    atomic.Bool
	stop      chan struct{}
	done      chan struct{}
	// enqueueMu keeps Enqueue and Close from racing on the queue channel.
	enqueueMu sync.RWMutex
}

type sinkState struct {
	sink    LogSink
	sent    atomic.Uint64
	failed  atomic.Uint64
	dropped atomic.Uint64
}

// ExporterOption configures a LogExporter.
type ExporterOption func(*LogExporter)

// WithExportQueue sets how many entries may wait for delivery.
func WithExportQueue(n int) ExporterOption {
	return func(e *LogExporter) {
		if n > 0 {
			e.queue = make(chan *domain.RequestLog, n)
		}
	}
}

// WithExportBatch sets the largest batch handed to a sink.
func WithExportBatch(n int) ExporterOption {
	return func(e *LogExporter) {
		if n > 0 {
			e.batch = n
		}
	}
}

// WithExportInterval sets how long a partial batch waits before it is sent.
func WithExportInterval(d time.Duration) ExporterOption {
	return func(e *LogExporter) {
		if d > 0 {
			e.interval = d
		}
	}
}

// WithExportRetryDelay sets the pause before the first retry (it doubles).
func WithExportRetryDelay(d time.Duration) ExporterOption {
	return func(e *LogExporter) {
		if d >= 0 {
			e.retry = d
		}
	}
}

// NewLogExporter starts an exporter that feeds the given sinks.
func NewLogExporter(sinks []LogSink, opts ...ExporterOption) *LogExporter {
	e := &LogExporter{
		sinks: make([]*sinkState, 0, len(sinks)), queue: make(chan *domain.RequestLog, defaultExportQueue),
		batch: defaultExportBatch, interval: defaultExportInterval, timeout: defaultExportTimeout,
		retry: defaultRetryDelay, closeOnce: sync.Once{}, closed: atomic.Bool{},
		stop: make(chan struct{}), done: make(chan struct{}), enqueueMu: sync.RWMutex{},
	}

	for _, s := range sinks {
		e.sinks = append(e.sinks, &sinkState{
			sink: s, sent: atomic.Uint64{}, failed: atomic.Uint64{}, dropped: atomic.Uint64{},
		})
	}

	for _, opt := range opts {
		opt(e)
	}

	go e.run()

	return e
}

// SinkNames lists the sinks in delivery order.
func (e *LogExporter) SinkNames() []string {
	names := make([]string, len(e.sinks))
	for i, s := range e.sinks {
		names[i] = s.sink.Name()
	}

	return names
}

// Enqueue hands an entry to the background worker without blocking.
func (e *LogExporter) Enqueue(l *domain.RequestLog) {
	e.enqueueMu.RLock()
	defer e.enqueueMu.RUnlock()

	if e.closed.Load() {
		e.dropAll(1)
		return
	}

	select {
	case e.queue <- l:
	default:
		e.dropAll(1)
	}
}

func (e *LogExporter) dropAll(n uint64) {
	for _, s := range e.sinks {
		s.dropped.Add(n)
	}
}

// Close stops accepting entries and flushes what is queued, waiting at most
// until ctx ends.
func (e *LogExporter) Close(ctx context.Context) error {
	err := ErrExporterClosed

	e.closeOnce.Do(func() {
		e.enqueueMu.Lock()
		e.closed.Store(true)
		close(e.stop)
		e.enqueueMu.Unlock()

		select {
		case <-e.done:
			err = nil
		case <-ctx.Done():
			err = ctx.Err()
		}
	})

	return err
}

func (e *LogExporter) run() {
	defer close(e.done)

	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	b := &batcher{e: e, pending: make([]*domain.RequestLog, 0, e.batch)}

	for {
		select {
		case l := <-e.queue:
			b.add(l)
		case <-ticker.C:
			b.flush()
		case <-e.stop:
			b.drain()

			return
		}
	}
}

// batcher collects entries into batches for the sinks.
type batcher struct {
	e       *LogExporter
	pending []*domain.RequestLog
}

func (b *batcher) add(l *domain.RequestLog) {
	b.pending = append(b.pending, l)
	if len(b.pending) >= b.e.batch {
		b.flush()
	}
}

func (b *batcher) flush() {
	if len(b.pending) > 0 {
		b.e.deliver(b.pending)
		b.pending = b.pending[:0]
	}
}

// drain sends everything still queued, then the partial batch.
func (b *batcher) drain() {
	for {
		select {
		case l := <-b.e.queue:
			b.add(l)
		default:
			b.flush()

			return
		}
	}
}

// deliver sends a batch to every sink, retrying a failing sink a few times.
func (e *LogExporter) deliver(batch []*domain.RequestLog) {
	for _, s := range e.sinks {
		if e.sendWithRetry(s, batch) {
			s.sent.Add(uint64(len(batch)))
		} else {
			s.failed.Add(uint64(len(batch)))
		}
	}
}

func (e *LogExporter) sendWithRetry(s *sinkState, batch []*domain.RequestLog) bool {
	delay := e.retry

	for attempt := range exportAttempts {
		ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
		err := s.sink.Send(ctx, batch)

		cancel()

		if err == nil {
			return true
		}

		if attempt < exportAttempts-1 {
			time.Sleep(delay)

			delay *= 2
		}
	}

	return false
}

// ExporterStats are the delivery counters of one sink.
type ExporterStats struct {
	Sink    string
	Sent    uint64
	Failed  uint64
	Dropped uint64
}

// Stats returns the counters of every sink and the number of queued entries.
func (e *LogExporter) Stats() (stats []ExporterStats, queued int) {
	stats = make([]ExporterStats, len(e.sinks))
	for i, s := range e.sinks {
		stats[i] = ExporterStats{
			Sink: s.sink.Name(), Sent: s.sent.Load(), Failed: s.failed.Load(), Dropped: s.dropped.Load(),
		}
	}

	return stats, len(e.queue)
}
