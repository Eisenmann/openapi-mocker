package usecase

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// WebSocket close codes used by the mock.
const (
	WSCloseNormal    = 1000
	WSCloseGoingAway = 1001
	WSClosePolicy    = 1008
)

// WSConn is the connection a WSPlan runs on. Writes may come from several
// goroutines; the implementation must serialize them.
type WSConn interface {
	// ReadMessage blocks for the next data message (text or binary). It
	// returns an error when the connection is closed.
	ReadMessage() ([]byte, error)
	WriteText(data string) error
	WriteBinary(data []byte) error
	Ping() error
	Close(code int, reason string) error
}

// WSPlan is a ready WebSocket behaviour for one upgraded request.
type WSPlan struct {
	script *domain.WSScript
	// render fills the placeholders of data; msg is the received message
	// (nil for the timeline played on connect).
	render func(data string, msg []byte) string
	regex  map[string]*regexp.Regexp
}

// Run plays the script on conn until the client leaves, the script closes
// the connection, or ctx ends.
func (p *WSPlan) Run(ctx context.Context, conn WSConn) {
	ctx, cancel := context.WithTimeout(ctx, maxStreamDuration)
	defer cancel()

	var wg sync.WaitGroup

	defer wg.Wait()
	defer cancel()

	closed := make(chan struct{})

	var closeOnce sync.Once

	closeConn := func(code int, reason string) {
		closeOnce.Do(func() {
			_ = conn.Close(code, reason)

			close(closed)
		})
	}

	replies := make(chan wsJob, wsReplyQueue)

	wg.Add(1)

	go func() {
		defer wg.Done()

		p.worker(ctx, conn, replies, closeConn)
	}()

	p.startTimers(ctx, &wg, conn, closeConn)

	go func() {
		select {
		case <-closed:
			cancel()
		case <-ctx.Done():
			// Server shutdown or the time limit: a blocked read must end too.
			closeConn(WSCloseGoingAway, "going away")
		}
	}()

	p.readLoop(ctx, conn, replies, closeConn)
}

const wsReplyQueue = 64

// wsJob is one matched message waiting for its reply.
type wsJob struct {
	rule *domain.WSRule
	msg  []byte
}

func (p *WSPlan) startTimers(ctx context.Context, wg *sync.WaitGroup, conn WSConn, closeConn func(int, string)) {
	if len(p.script.OnConnect) > 0 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			_ = playTimeline(ctx, p.script.OnConnect, p.script.Loop, func(st *domain.StreamStep, data string) error {
				return writeStep(conn, st, data)
			}, func(d string) string { return p.render(d, nil) })
		}()
	}

	if p.script.PingIntervalMs > 0 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			t := time.NewTicker(time.Duration(p.script.PingIntervalMs) * time.Millisecond)
			defer t.Stop()

			for {
				select {
				case <-t.C:
					if conn.Ping() != nil {
						return
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	if p.script.CloseAfterMs > 0 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			if sleepCtx(ctx, p.script.CloseAfterMs) == nil {
				closeConn(WSCloseNormal, "closed by mock")
			}
		}()
	}
}

// worker sends the replies one after another, so they keep their order.
func (p *WSPlan) worker(ctx context.Context, conn WSConn, jobs <-chan wsJob, closeConn func(int, string)) {
	for {
		select {
		case job := <-jobs:
			_ = playTimeline(ctx, job.rule.Reply, false, func(st *domain.StreamStep, data string) error {
				return writeStep(conn, st, data)
			}, func(d string) string { return p.render(d, job.msg) })

			if job.rule.Close {
				closeConn(WSCloseNormal, "closed by mock rule")
			}
		case <-ctx.Done():
			return
		}
	}
}

func (p *WSPlan) readLoop(ctx context.Context, conn WSConn, jobs chan<- wsJob, closeConn func(int, string)) {
	fallback := p.script.Fallback
	if fallback == "" {
		fallback = domain.WSFallbackEcho
	}

	for {
		msg, err := conn.ReadMessage()
		if err != nil {
			return // the client closing or dropping is the normal end.
		}

		rule := p.match(msg)
		if rule == nil {
			switch fallback {
			case domain.WSFallbackEcho:
				rule = &domain.WSRule{Reply: []domain.StreamStep{{Data: string(msg)}}}
			case domain.WSFallbackClose:
				closeConn(WSClosePolicy, "message not understood")

				return
			default:
				continue
			}
		}

		select {
		case jobs <- wsJob{rule: rule, msg: msg}:
		case <-ctx.Done():
			return
		}
	}
}

// match returns the first rule that matches the message.
func (p *WSPlan) match(msg []byte) *domain.WSRule {
	for i := range p.script.Rules {
		r := &p.script.Rules[i]
		if p.matches(&r.Match, msg) {
			return r
		}
	}

	return nil
}

func (p *WSPlan) matches(m *domain.WSMatch, msg []byte) bool {
	switch m.Type {
	case domain.WSMatchEquals:
		return string(msg) == m.Pattern
	case domain.WSMatchRegex:
		re := p.regex[m.Pattern]

		return re != nil && re.Match(msg)
	case domain.WSMatchJSON:
		return jsonSubset(m.Pattern, msg)
	default: // contains.
		return strings.Contains(string(msg), m.Pattern)
	}
}

// jsonSubset reports whether every top-level field of the pattern object is
// present, with an equal value, in the JSON message.
func jsonSubset(pattern string, msg []byte) bool {
	var want, got map[string]any

	if json.Unmarshal([]byte(pattern), &want) != nil || json.Unmarshal(msg, &got) != nil {
		return false
	}

	for k, w := range want {
		g, ok := got[k]
		if !ok || !reflect.DeepEqual(w, g) {
			return false
		}
	}

	return true
}

// planWebSocket builds the WebSocket behaviour of a script for a request.
func (s *MockServingService) planWebSocket(script *domain.WSScript, tc *TemplateContext) *WSPlan {
	p := &WSPlan{script: script, regex: map[string]*regexp.Regexp{}}

	for i := range script.Rules {
		m := &script.Rules[i].Match
		if m.Type != domain.WSMatchRegex {
			continue
		}

		re, err := regexp.Compile(m.Pattern)
		if err == nil {
			p.regex[m.Pattern] = re
		}
	}

	render := s.renderFunc(tc)

	p.render = func(data string, msg []byte) string {
		if msg == nil {
			return render(data)
		}

		// A reply can use the received message as {{request.body...}}.
		c := *tc
		c.Body, c.body, c.parsed = msg, nil, false

		return s.renderFunc(&c)(data)
	}

	return p
}

// websocketResponse is the MockResponse of an upgrade that gets a script.
func websocketResponse(plan *WSPlan, delayMs int, source, ruleID string) *MockResponse {
	return &MockResponse{
		StatusCode: StatusSwitchingProtocols, DelayMs: delayMs, Source: source, MatchedRule: ruleID,
		Matched: true, WebSocket: plan,
	}
}

// writeStep sends one rendered step as a text frame, or, for the base64 and
// hex encodings, as a binary frame holding the decoded bytes.
func writeStep(conn WSConn, st *domain.StreamStep, data string) error {
	if st.Encoding == "" || st.Encoding == domain.StepEncodingText {
		return conn.WriteText(data)
	}

	raw, err := decodeStepData(st.Encoding, data)
	if err != nil {
		return err
	}

	return conn.WriteBinary(raw)
}

// decodeStepData turns the data of a binary step into bytes.
func decodeStepData(encoding, data string) ([]byte, error) {
	data = strings.TrimSpace(data)

	switch encoding {
	case domain.StepEncodingBase64:
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, fmt.Errorf("%w: base64 data: %w", ErrInvalidStream, err)
		}

		return raw, nil
	case domain.StepEncodingHex:
		raw, err := hex.DecodeString(strings.NewReplacer(" ", "", ":", "").Replace(data))
		if err != nil {
			return nil, fmt.Errorf("%w: hex data: %w", ErrInvalidStream, err)
		}

		return raw, nil
	default:
		return nil, fmt.Errorf("%w: unknown encoding %q", ErrInvalidStream, encoding)
	}
}

// Validation.

var errWSMatchType = errors.New("match type must be contains, equals, regex or json")

// validateWebSocket checks a WebSocket script.
func validateWebSocket(w *domain.WSScript) error {
	if w == nil {
		return nil
	}

	err := validateSteps("onConnect", w.OnConnect)
	if err != nil {
		return err
	}

	err = validateWSSettings(w)
	if err != nil {
		return err
	}

	for i := range w.Rules {
		err = validateWSRule(i+1, &w.Rules[i])
		if err != nil {
			return err
		}
	}

	return nil
}

func validateWSSettings(w *domain.WSScript) error {
	if len(w.Rules) > maxWSRules {
		return fmt.Errorf("%w: more than %d rules", ErrInvalidStream, maxWSRules)
	}

	switch w.Fallback {
	case "", domain.WSFallbackEcho, domain.WSFallbackIgnore, domain.WSFallbackClose:
	default:
		return fmt.Errorf("%w: fallback must be echo, ignore or close", ErrInvalidStream)
	}

	if w.PingIntervalMs < 0 || w.CloseAfterMs < 0 || w.PingIntervalMs > maxStepDelayMs || w.CloseAfterMs > maxStepDelayMs {
		return fmt.Errorf("%w: pingIntervalMs and closeAfterMs are out of range", ErrInvalidStream)
	}

	return nil
}

func validateWSRule(n int, r *domain.WSRule) error {
	switch r.Match.Type {
	case domain.WSMatchContains, domain.WSMatchEquals:
	case domain.WSMatchRegex:
		_, err := regexp.Compile(r.Match.Pattern)
		if err != nil {
			return fmt.Errorf("%w: rule %d: bad regex: %w", ErrInvalidStream, n, err)
		}
	case domain.WSMatchJSON:
		var o map[string]any

		err := json.Unmarshal([]byte(r.Match.Pattern), &o)
		if err != nil {
			return fmt.Errorf("%w: rule %d: a json pattern must be a JSON object: %w", ErrInvalidStream, n, err)
		}
	default:
		return fmt.Errorf("%w: rule %d: %w", ErrInvalidStream, n, errWSMatchType)
	}

	return validateSteps(fmt.Sprintf("rule %d reply", n), r.Reply)
}
