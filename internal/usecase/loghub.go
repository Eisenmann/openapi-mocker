package usecase

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

const (
	// hubRingSize is how many recent entries (with their captured bodies)
	// the hub keeps in memory.
	hubRingSize = 500
	// subscriberBuffer is the queue of one stream; a slow reader loses entries.
	subscriberBuffer = 256
	// MaxStreamSubscribers bounds the number of concurrent live streams.
	MaxStreamSubscribers = 200
	// storedLogScan is how many stored entries Get searches when an entry is
	// no longer in memory (the store keeps 5000 in all).
	storedLogScan = 5000
)

// ErrTooManyStreams is returned when no more live streams can be opened.
var ErrTooManyStreams = errors.New("too many open log streams")

// ErrStreamsClosed is returned when the server is shutting down.
var ErrStreamsClosed = errors.New("log streams are closed: the server is shutting down")

// LogHub sits in front of a LogRepository. It stores entries without their
// bodies, keeps the recent ones with bodies in memory, pushes every new entry
// to live streams, and feeds the metrics and per-endpoint statistics.
type LogHub struct {
	inner  LogRepository
	policy CapturePolicy
	now    func() time.Time

	mu      sync.Mutex
	ring    []*domain.RequestLog
	subs    map[int]*Subscription
	nextSub int
	closing bool

	obs     *observations
	journal *journal

	exporter     *LogExporter
	exportBodies bool
	info         LogInfo
}

// LogInfo describes how logs are kept, for the UI.
type LogInfo struct {
	// Mode is "local" (stored in the data file) or "distributed" (shipped to
	// external systems, kept in memory only).
	Mode string `json:"mode"`
	// Sinks names the systems entries are shipped to.
	Sinks []string `json:"sinks"`
	// ExportBodies says whether shipped entries carry request and response bodies.
	ExportBodies bool `json:"exportBodies"`
}

// Subscription is one live log stream.
type Subscription struct {
	projectID string
	ch        chan *domain.RequestLog
	dropped   atomic.Int64
	hub       *LogHub
	id        int
}

// Entries delivers new log entries.
func (s *Subscription) Entries() <-chan *domain.RequestLog { return s.ch }

// TakeDropped returns how many entries were lost since the last call because
// the reader was too slow.
func (s *Subscription) TakeDropped() int64 { return s.dropped.Swap(0) }

// Close ends the subscription.
func (s *Subscription) Close() { s.hub.unsubscribe(s.id) }

// LogHubOption configures a LogHub.
type LogHubOption func(*LogHub)

// WithLogClock replaces the clock (for tests).
func WithLogClock(now func() time.Time) LogHubOption {
	return func(h *LogHub) { h.now = now }
}

// WithLogExport ships every entry to the exporter's sinks. Bodies are only
// included when exportBodies is set (and capture is enabled).
func WithLogExport(exp *LogExporter, exportBodies bool) LogHubOption {
	return func(h *LogHub) {
		h.exporter = exp
		h.exportBodies = exportBodies
	}
}

// WithLogMode records the logging mode reported by Info.
func WithLogMode(mode string) LogHubOption {
	return func(h *LogHub) { h.info.Mode = mode }
}

// Info reports the logging mode and the sinks in use.
func (h *LogHub) Info() LogInfo {
	info := h.info
	info.Sinks = []string{}

	if h.exporter != nil {
		info.Sinks = h.exporter.SinkNames()
		info.ExportBodies = h.exportBodies
	}

	return info
}

// NewLogHub wraps inner. The policy decides which bodies are kept.
func NewLogHub(inner LogRepository, policy CapturePolicy, opts ...LogHubOption) *LogHub {
	h := &LogHub{
		inner: inner, policy: policy.normalized(), now: time.Now,
		mu: sync.Mutex{}, ring: nil, subs: map[int]*Subscription{}, nextSub: 0, closing: false,
		obs: newObservations(), journal: newJournal(), exporter: nil, exportBodies: false,
		info: LogInfo{Mode: "local", Sinks: nil, ExportBodies: false},
	}

	for _, opt := range opts {
		opt(h)
	}

	h.obs.started = h.now()

	return h
}

// Policy returns the capture policy in force.
func (h *LogHub) Policy() CapturePolicy { return h.policy }

// Add implements LogRepository.
func (h *LogHub) Add(l *domain.RequestLog) {
	full := *l
	full.Request = h.policy.apply(l.Request)
	full.Response = h.policy.apply(l.Response)
	full.Captured = full.Request != nil || full.Response != nil

	stored := full
	stored.Request, stored.Response = nil, nil

	h.inner.Add(&stored)

	full.ID = stored.ID
	l.ID = stored.ID
	l.Captured = full.Captured

	h.export(&full)

	h.mu.Lock()
	defer h.mu.Unlock()

	h.ring = append(h.ring, &full)
	if len(h.ring) > 2*hubRingSize {
		h.ring = append([]*domain.RequestLog(nil), h.ring[len(h.ring)-hubRingSize:]...)
	}

	h.obs.record(&full)
	h.journal.add(&full)

	for _, sub := range h.subs {
		if sub.projectID != full.ProjectID {
			continue
		}

		select {
		case sub.ch <- &full:
		default:
			sub.dropped.Add(1)
		}
	}
}

// ListLogs implements LogRepository; entries come without bodies.
func (h *LogHub) ListLogs(projectID string, limit int) []*domain.RequestLog {
	return h.inner.ListLogs(projectID, limit)
}

// Get returns one entry with its captured bodies, if it is still in memory,
// or else the stored entry without them.
func (h *LogHub) Get(projectID, id string) (*domain.RequestLog, bool) {
	h.mu.Lock()
	for i := len(h.ring) - 1; i >= 0; i-- {
		if h.ring[i].ID == id && h.ring[i].ProjectID == projectID {
			l := *h.ring[i]
			h.mu.Unlock()

			return &l, true
		}
	}
	h.mu.Unlock()

	for _, l := range h.inner.ListLogs(projectID, storedLogScan) {
		if l.ID == id {
			c := *l

			return &c, true
		}
	}

	return nil, false
}

// Subscribe opens a live stream of the project's new entries. When afterID
// names an entry still in memory, the entries logged after it are returned as
// backlog, so a reconnecting client misses nothing.
func (h *LogHub) Subscribe(projectID, afterID string) (*Subscription, []*domain.RequestLog, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closing {
		return nil, nil, ErrStreamsClosed
	}

	if len(h.subs) >= MaxStreamSubscribers {
		return nil, nil, ErrTooManyStreams
	}

	sub := &Subscription{
		projectID: projectID, ch: make(chan *domain.RequestLog, subscriberBuffer),
		dropped: atomic.Int64{}, hub: h, id: h.nextSub,
	}
	h.nextSub++
	h.subs[sub.id] = sub

	return sub, h.backlogLocked(projectID, afterID), nil
}

func (h *LogHub) backlogLocked(projectID, afterID string) []*domain.RequestLog {
	if afterID == "" {
		return nil
	}

	for i := range h.ring {
		if h.ring[i].ID != afterID {
			continue
		}

		var out []*domain.RequestLog

		for _, l := range h.ring[i+1:] {
			if l.ProjectID == projectID {
				out = append(out, l)
			}
		}

		return out
	}

	return nil
}

func (h *LogHub) unsubscribe(id int) {
	h.mu.Lock()
	delete(h.subs, id)
	h.mu.Unlock()
}

// Subscribers returns the number of open streams.
func (h *LogHub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return len(h.subs)
}

// export hands a copy of the entry to the exporter, without bodies unless the
// operator asked for them.
func (h *LogHub) export(full *domain.RequestLog) {
	if h.exporter == nil {
		return
	}

	out := *full
	if !h.exportBodies {
		out.Request, out.Response = nil, nil
	}

	h.exporter.Enqueue(&out)
}

// CloseStreams ends every live stream (their Entries channels close) and
// refuses new ones. The server calls it at shutdown, because a stream never
// ends on its own and would hold up the graceful stop.
func (h *LogHub) CloseStreams() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.closing = true

	for id, sub := range h.subs {
		close(sub.ch)
		delete(h.subs, id)
	}
}
