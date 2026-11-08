package usecase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// ContentTypeEventStream is the media type of Server-Sent Events.
const ContentTypeEventStream = "text/event-stream"

// Header names used when answering and recognising upgrade and stream requests.
const (
	headerCacheControl = "Cache-Control"
	headerUpgrade      = "Upgrade"
	headerConnection   = "Connection"
	cacheNoCache       = "no-cache"
)

// Limits of stream scripts, so a script cannot exhaust the server.
const (
	maxScriptSteps   = 500
	maxStepRepeat    = 100000
	maxStepDelayMs   = 3600000
	maxStepDataBytes = 64 << 10
	maxWSRules       = 100
	// maxStreamEvents bounds the events of one connection.
	maxStreamEvents = 1000000
	// maxStreamDuration ends a stream that nobody closed.
	maxStreamDuration = time.Hour
	// minLoopCycle keeps a looping script without delays from spinning.
	minLoopCycle = 100 * time.Millisecond
	// defaultStreamIntervalMs spaces the events of a text/event-stream example.
	defaultStreamIntervalMs = 1000
)

// ErrInvalidStream means a stream or WebSocket script is not valid.
var ErrInvalidStream = errors.New("invalid stream script")

// StreamEvent is one Server-Sent Event.
type StreamEvent struct {
	Event string
	ID    string
	Data  string
}

// Format renders the event in the text/event-stream wire format.
func (e StreamEvent) Format() string {
	var b strings.Builder

	if e.Event != "" {
		b.WriteString("event: " + sseField(e.Event) + "\n")
	}

	if e.ID != "" {
		b.WriteString("id: " + sseField(e.ID) + "\n")
	}

	data := strings.ReplaceAll(strings.ReplaceAll(e.Data, "\r\n", "\n"), "\r", "\n")
	for _, line := range strings.Split(data, "\n") {
		b.WriteString("data: " + line + "\n")
	}

	b.WriteString("\n")

	return b.String()
}

// sseField keeps a field value on one line.
func sseField(v string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(v)
}

// StreamPlan is a ready Server-Sent Events response. The HTTP adapter plays it
// to the client.
type StreamPlan struct {
	script *domain.StreamScript
	render func(string) string
}

// RetryMs is the reconnection delay to advertise (0: none).
func (p *StreamPlan) RetryMs() int { return p.script.RetryMs }

// Play sends the events until the script ends, ctx is done or send fails.
func (p *StreamPlan) Play(ctx context.Context, send func(StreamEvent) error) error {
	return playTimeline(ctx, p.script.Steps, p.script.Loop, func(st *domain.StreamStep, data string) error {
		return send(StreamEvent{Event: st.Event, ID: st.ID, Data: data})
	}, p.render)
}

// playTimeline emits the steps with their delays and repetitions; every
// emission renders the step's data afresh. emit errors and ctx end it.
func playTimeline(
	ctx context.Context, steps []domain.StreamStep, loop bool,
	emit func(*domain.StreamStep, string) error, render func(string) string,
) error {
	ctx, cancel := context.WithTimeout(ctx, maxStreamDuration)
	defer cancel()

	sent := 0

	for {
		cycleStart := time.Now()

		for i := range steps {
			done, err := playStep(ctx, &steps[i], &sent, emit, render)
			if done || err != nil {
				return err
			}
		}

		if !loop || ctx.Err() != nil {
			return nil
		}

		// A cycle without any delay would spin; pace it.
		wait := minLoopCycle - time.Since(cycleStart)
		if wait > 0 && sleepCtx(ctx, int(wait.Milliseconds())) != nil {
			return nil
		}
	}
}

// playStep emits one step. done is true when playback must stop without an
// error: the client left or the event budget is spent.
func playStep(
	ctx context.Context, st *domain.StreamStep, sent *int,
	emit func(*domain.StreamStep, string) error, render func(string) string,
) (done bool, err error) {
	if sleepCtx(ctx, st.DelayMs) != nil {
		return true, nil // a client that left is not a failure.
	}

	for n := range max(st.Repeat, 1) {
		if n > 0 && sleepCtx(ctx, st.IntervalMs) != nil {
			return true, nil
		}

		*sent++
		if *sent > maxStreamEvents {
			return true, nil
		}

		err = emit(st, render(st.Data))
		if err != nil {
			return true, err
		}
	}

	return false, nil
}

// sleepCtx waits ms milliseconds; it returns the context's error if that ends first.
func sleepCtx(ctx context.Context, ms int) error {
	if ms <= 0 {
		return ctx.Err()
	}

	t := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer t.Stop()

	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// renderFunc returns the placeholder renderer of a script step for a request.
func (s *MockServingService) renderFunc(tc *TemplateContext) func(string) string {
	return func(data string) string {
		ctx := *tc
		trim := strings.TrimSpace(data)
		ctx.JSON = strings.HasPrefix(trim, "{") || strings.HasPrefix(trim, "[")

		return s.templates.Render(data, &ctx)
	}
}

// planStream builds the SSE response of a script.
func (s *MockServingService) planStream(script *domain.StreamScript, tc *TemplateContext) *StreamPlan {
	return &StreamPlan{script: script, render: s.renderFunc(tc)}
}

// streamResponse is the MockResponse of an event stream.
func streamResponse(plan *StreamPlan, delayMs int, source, ruleID string) *MockResponse {
	return &MockResponse{
		StatusCode: StatusOK, ContentType: ContentTypeEventStream,
		Headers: map[string]string{headerCacheControl: cacheNoCache, "X-Accel-Buffering": "no"},
		DelayMs: delayMs, Source: source, MatchedRule: ruleID, Matched: true, Stream: plan,
	}
}

// scriptFromEventStream turns a text/event-stream example into a timeline.
func scriptFromEventStream(body []byte) *domain.StreamScript {
	text := strings.ReplaceAll(strings.ReplaceAll(string(body), "\r\n", "\n"), "\r", "\n")
	steps := make([]domain.StreamStep, 0)

	for _, block := range strings.Split(strings.TrimSpace(text), "\n\n") {
		st := domain.StreamStep{DelayMs: defaultStreamIntervalMs}

		var data []string

		for _, line := range strings.Split(block, "\n") {
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")

			switch field {
			case "event":
				st.Event = value
			case "id":
				st.ID = value
			case "data":
				data = append(data, value)
			}
		}

		if len(data) == 0 && st.Event == "" {
			continue
		}

		st.Data = strings.Join(data, "\n")
		steps = append(steps, st)
	}

	return &domain.StreamScript{Steps: steps}
}

// Validation.

func validateSteps(what string, steps []domain.StreamStep) error {
	if len(steps) > maxScriptSteps {
		return fmt.Errorf("%w: %s has more than %d steps", ErrInvalidStream, what, maxScriptSteps)
	}

	for i := range steps {
		st := &steps[i]

		switch {
		case st.DelayMs < 0 || st.DelayMs > maxStepDelayMs, st.IntervalMs < 0 || st.IntervalMs > maxStepDelayMs:
			return fmt.Errorf("%w: %s step %d: delays must be between 0 and %d ms",
				ErrInvalidStream, what, i+1, maxStepDelayMs)
		case st.Repeat < 0 || st.Repeat > maxStepRepeat:
			return fmt.Errorf("%w: %s step %d: repeat must be between 0 and %d",
				ErrInvalidStream, what, i+1, maxStepRepeat)
		case len(st.Data) > maxStepDataBytes:
			return fmt.Errorf("%w: %s step %d: data is larger than %d bytes",
				ErrInvalidStream, what, i+1, maxStepDataBytes)
		}

		err := validateEncoding(what, i+1, st)
		if err != nil {
			return err
		}
	}

	return nil
}

// validateEncoding checks the encoding of a step and, when the data has no
// placeholders, that it decodes.
func validateEncoding(what string, n int, st *domain.StreamStep) error {
	switch st.Encoding {
	case "", domain.StepEncodingText:
		return nil
	case domain.StepEncodingBase64, domain.StepEncodingHex:
	default:
		return fmt.Errorf("%w: %s step %d: encoding must be text, base64 or hex", ErrInvalidStream, what, n)
	}

	if strings.Contains(st.Data, "{{") {
		return nil
	}

	_, err := decodeStepData(st.Encoding, st.Data)
	if err != nil {
		return fmt.Errorf("%s step %d: %w", what, n, err)
	}

	return nil
}

// validateStream checks an SSE script.
func validateStream(s *domain.StreamScript) error {
	if s == nil {
		return nil
	}

	if len(s.Steps) == 0 {
		return fmt.Errorf("%w: a stream needs at least one step", ErrInvalidStream)
	}

	if s.RetryMs < 0 || s.RetryMs > maxStepDelayMs {
		return fmt.Errorf("%w: retryMs is out of range", ErrInvalidStream)
	}

	return validateSteps("stream", s.Steps)
}
