package usecase

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

func TestStreamEvent_Format(t *testing.T) {
	t.Parallel()

	got := StreamEvent{Event: "a\nb", ID: "7", Data: "x\r\ny"}.Format()
	if want := "event: ab\nid: 7\ndata: x\ndata: y\n\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	if got := (StreamEvent{Data: ""}).Format(); got != "data: \n\n" {
		t.Errorf("empty data: %q", got)
	}
}

func TestPlayTimeline_RepeatAndRender(t *testing.T) {
	t.Parallel()

	n := 0
	steps := []domain.StreamStep{{Data: "d", Repeat: 3, IntervalMs: 1}, {Data: "end", DelayMs: 1}}

	var got []string

	err := playTimeline(context.Background(), steps, false, func(_ *domain.StreamStep, d string) error {
		got = append(got, d)

		return nil
	}, func(s string) string { n++; return strings.ToUpper(s) })
	if err != nil {
		t.Fatal(err)
	}

	if strings.Join(got, ",") != "D,D,D,END" || n != 4 {
		t.Errorf("got %v, rendered %d times", got, n)
	}
}

func TestPlayTimeline_LoopStopsOnCancelAndErrorsPropagate(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	count := 0

	err := playTimeline(ctx, []domain.StreamStep{{Data: "x"}}, true, func(*domain.StreamStep, string) error {
		if count++; count == 3 {
			cancel()
		}

		return nil
	}, func(s string) string { return s })
	if err != nil || count != 3 {
		t.Errorf("loop: err %v, count %d", err, count)
	}

	boom := errors.New("boom")

	err = playTimeline(context.Background(), []domain.StreamStep{{Data: "x"}}, false, func(*domain.StreamStep, string) error {
		return boom
	}, func(s string) string { return s })
	if !errors.Is(err, boom) {
		t.Errorf("want the emit error, got %v", err)
	}
}

func TestScriptFromEventStream(t *testing.T) {
	t.Parallel()

	s := scriptFromEventStream([]byte("event: a\nid: 1\ndata: x\ndata: y\n\n: comment\n\ndata: z\n"))
	if len(s.Steps) != 2 {
		t.Fatalf("steps: %+v", s.Steps)
	}

	if st := s.Steps[0]; st.Event != "a" || st.ID != "1" || st.Data != "x\ny" || st.DelayMs != defaultStreamIntervalMs {
		t.Errorf("first step %+v", st)
	}

	if s.Steps[1].Data != "z" {
		t.Errorf("second step %+v", s.Steps[1])
	}
}

func TestValidateStreamAndWebSocket(t *testing.T) {
	t.Parallel()

	ok := []domain.StreamStep{{Data: "x"}}
	bad := map[string]error{
		"no steps":      validateStream(&domain.StreamScript{}),
		"neg delay":     validateStream(&domain.StreamScript{Steps: []domain.StreamStep{{DelayMs: -1}}}),
		"big repeat":    validateStream(&domain.StreamScript{Steps: []domain.StreamStep{{Repeat: maxStepRepeat + 1}}}),
		"big data":      validateStream(&domain.StreamScript{Steps: []domain.StreamStep{{Data: strings.Repeat("x", maxStepDataBytes+1)}}}),
		"bad retry":     validateStream(&domain.StreamScript{Steps: ok, RetryMs: -5}),
		"ws fallback":   validateWebSocket(&domain.WSScript{Fallback: "nope"}),
		"ws ping":       validateWebSocket(&domain.WSScript{PingIntervalMs: -1}),
		"ws match type": validateWebSocket(&domain.WSScript{Rules: []domain.WSRule{{Match: domain.WSMatch{Type: "x"}}}}),
		"ws regex":      validateWebSocket(&domain.WSScript{Rules: []domain.WSRule{{Match: domain.WSMatch{Type: domain.WSMatchRegex, Pattern: "("}}}}),
		"ws json":       validateWebSocket(&domain.WSScript{Rules: []domain.WSRule{{Match: domain.WSMatch{Type: domain.WSMatchJSON, Pattern: "[]"}}}}),
	}

	for name, err := range bad {
		if !errors.Is(err, ErrInvalidStream) {
			t.Errorf("%s: want ErrInvalidStream, got %v", name, err)
		}
	}

	if err := validateStream(nil); err != nil {
		t.Error(err)
	}

	if err := validateWebSocket(&domain.WSScript{Fallback: domain.WSFallbackClose, Rules: []domain.WSRule{
		{Match: domain.WSMatch{Type: domain.WSMatchJSON, Pattern: `{"a":1}`}},
	}}); err != nil {
		t.Error(err)
	}
}

// fakeConn is an in-memory WSConn.
type fakeConn struct {
	in     chan string
	mu     sync.Mutex
	out    []string
	closed chan int
	once   sync.Once
}

func newFakeConn() *fakeConn {
	return &fakeConn{in: make(chan string, 16), closed: make(chan int, 1)}
}

func (f *fakeConn) ReadMessage() ([]byte, error) {
	select {
	case m, ok := <-f.in:
		if !ok {
			return nil, io.EOF
		}

		return []byte(m), nil
	case <-f.closed:
		return nil, io.EOF
	}
}

func (f *fakeConn) WriteText(d string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.out = append(f.out, d)

	return nil
}

func (f *fakeConn) WriteBinary(d []byte) error { return f.WriteText("bin:" + hex.EncodeToString(d)) }

func (f *fakeConn) Ping() error { return f.WriteText("<ping>") }

func (f *fakeConn) Close(code int, _ string) error {
	f.once.Do(func() { f.closed <- code; close(f.closed) })

	return nil
}

func (f *fakeConn) written() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.out...)
}

func newTestService() (*MockServingService, *TemplateContext) {
	return NewMockServingService(nil, nil, nil, nil), &TemplateContext{}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}

		time.Sleep(5 * time.Millisecond)
	}
}

func TestWSPlan_RulesFallbackAndClose(t *testing.T) {
	t.Parallel()

	svc, tc := newTestService()
	plan := svc.planWebSocket(&domain.WSScript{
		OnConnect: []domain.StreamStep{{Data: "hi"}},
		Rules: []domain.WSRule{
			{Match: domain.WSMatch{Type: domain.WSMatchEquals, Pattern: "ping"}, Reply: []domain.StreamStep{{Data: "pong"}}},
			{Match: domain.WSMatch{Type: domain.WSMatchRegex, Pattern: `^n\d+$`}, Reply: []domain.StreamStep{{Data: "num"}}},
			{Match: domain.WSMatch{Type: domain.WSMatchJSON, Pattern: `{"op":"sub"}`}, Reply: []domain.StreamStep{{Data: "subbed {{request.body.id}}"}}},
			{Match: domain.WSMatch{Type: domain.WSMatchContains, Pattern: "bye"}, Reply: []domain.StreamStep{{Data: "ciao"}}, Close: true},
		},
		Fallback: domain.WSFallbackEcho,
	}, tc)

	conn := newFakeConn()
	done := make(chan error, 1)

	go func() { plan.Run(context.Background(), conn); done <- nil }()

	waitFor(t, func() bool { return len(conn.written()) == 1 })

	for _, m := range []string{"ping", "n42", `{"op":"sub","id":7,"extra":true}`, "other", "so bye"} {
		conn.in <- m
	}

	select {
	case code := <-conn.closed:
		if code != WSCloseNormal {
			t.Errorf("close code %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("not closed")
	}

	<-done

	got := strings.Join(conn.written(), "|")
	if want := "hi|pong|num|subbed 7|other|ciao"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWSPlan_FallbackCloseAndTimers(t *testing.T) {
	t.Parallel()

	svc, tc := newTestService()

	conn := newFakeConn()
	plan := svc.planWebSocket(&domain.WSScript{Fallback: domain.WSFallbackClose}, tc)

	go func() { plan.Run(context.Background(), conn) }()

	conn.in <- "unknown"

	if code := <-conn.closed; code != WSClosePolicy {
		t.Errorf("fallback close code %d", code)
	}

	conn = newFakeConn()
	plan = svc.planWebSocket(&domain.WSScript{PingIntervalMs: 10, CloseAfterMs: 120}, tc)

	go func() { plan.Run(context.Background(), conn) }()

	if code := <-conn.closed; code != WSCloseNormal {
		t.Errorf("closeAfter code %d", code)
	}

	if pings := strings.Count(strings.Join(conn.written(), ""), "<ping>"); pings < 2 {
		t.Errorf("want periodic pings, got %d", pings)
	}
}

func TestWSPlan_IgnoreFallbackAndOnConnectLoop(t *testing.T) {
	t.Parallel()

	svc, tc := newTestService()
	conn := newFakeConn()
	plan := svc.planWebSocket(&domain.WSScript{
		OnConnect: []domain.StreamStep{{Data: "tick", IntervalMs: 1}},
		Loop:      true, Fallback: domain.WSFallbackIgnore,
	}, tc)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { plan.Run(ctx, conn); done <- nil }()

	conn.in <- "ignored"

	waitFor(t, func() bool { return len(conn.written()) >= 3 })
	cancel()
	<-done

	for _, w := range conn.written() {
		if w != "tick" {
			t.Fatalf("only ticks expected, got %q", w)
		}
	}
}

func TestWSPlan_BinaryEncodings(t *testing.T) {
	t.Parallel()

	svc, tc := newTestService()
	plan := svc.planWebSocket(&domain.WSScript{
		OnConnect: []domain.StreamStep{
			{Data: "AQID", Encoding: domain.StepEncodingBase64},
			{Data: "de ad:be ef", Encoding: domain.StepEncodingHex},
			{Data: "plain", Encoding: domain.StepEncodingText},
		},
		Fallback: domain.WSFallbackIgnore,
	}, tc)

	conn := newFakeConn()
	done := make(chan struct{})

	go func() { plan.Run(context.Background(), conn); close(done) }()

	waitFor(t, func() bool { return len(conn.written()) == 3 })

	_ = conn.Close(WSCloseNormal, "")

	<-done

	if got, want := strings.Join(conn.written(), "|"), "bin:010203|bin:deadbeef|plain"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestValidateEncoding(t *testing.T) {
	t.Parallel()

	for name, st := range map[string]domain.StreamStep{
		"unknown":       {Data: "x", Encoding: "rot13"},
		"bad base64":    {Data: "***", Encoding: domain.StepEncodingBase64},
		"odd hex":       {Data: "abc", Encoding: domain.StepEncodingHex},
		"hex not a hex": {Data: "zz", Encoding: domain.StepEncodingHex},
	} {
		err := validateSteps("t", []domain.StreamStep{st})
		if err == nil {
			t.Errorf("%s: want an error", name)
		}
	}

	ok := []domain.StreamStep{
		{Data: "{{uuid}}", Encoding: domain.StepEncodingHex},
		{Data: "AQID", Encoding: domain.StepEncodingBase64},
	}
	if err := validateSteps("t", ok); err != nil {
		t.Errorf("valid steps rejected: %v", err)
	}
}
