package openapi_test

import (
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
)

const streamSpec = `openapi: 3.0.0
info: {title: T, version: "1"}
paths:
  /events:
    get:
      x-mock-stream:
        loop: true
        retryMs: 2000
        steps:
          - {event: tick, data: '{"n": {{counter}}}', intervalMs: 500, repeat: 3}
          - {data: done, delayMs: 10}
      responses:
        '200': {description: ok, content: {text/event-stream: {schema: {type: string}}}}
  /ws:
    get:
      x-mock-websocket:
        onConnect: [{data: hello}]
        rules:
          - {match: {type: contains, pattern: ping}, reply: [{data: pong}]}
        fallback: ignore
      responses:
        '101': {description: switching}
  /echo:
    get:
      x-mock-websocket: {}
      responses:
        '101': {description: switching}
  /plain:
    get:
      responses:
        '200': {description: ok}
  /bad:
    get:
      x-mock-stream: "not an object"
      responses:
        '200': {description: ok}
`

func TestStreamScripts(t *testing.T) {
	t.Parallel()

	e := openapi.NewEngine()

	stream, ws, err := e.StreamScripts([]byte(streamSpec), "GET", "/events")
	if err != nil || ws != nil || stream == nil {
		t.Fatalf("stream=%+v ws=%+v err=%v", stream, ws, err)
	}

	if !stream.Loop || stream.RetryMs != 2000 || len(stream.Steps) != 2 ||
		stream.Steps[0].Event != "tick" || stream.Steps[0].Repeat != 3 || stream.Steps[1].DelayMs != 10 {
		t.Errorf("stream = %+v", stream)
	}

	stream, ws, err = e.StreamScripts([]byte(streamSpec), "GET", "/ws")
	if err != nil || stream != nil || ws == nil || ws.Fallback != "ignore" ||
		len(ws.OnConnect) != 1 || len(ws.Rules) != 1 || ws.Rules[0].Reply[0].Data != "pong" {
		t.Fatalf("stream=%+v ws=%+v err=%v", stream, ws, err)
	}

	_, ws, err = e.StreamScripts([]byte(streamSpec), "GET", "/echo")
	if err != nil || ws == nil {
		t.Errorf("an empty x-mock-websocket declares an echo endpoint: ws=%+v err=%v", ws, err)
	}

	stream, ws, err = e.StreamScripts([]byte(streamSpec), "GET", "/plain")
	if err != nil || stream != nil || ws != nil {
		t.Errorf("plain: %+v %+v %v", stream, ws, err)
	}

	_, _, err = e.StreamScripts([]byte(streamSpec), "GET", "/bad")
	if err == nil {
		t.Error("a malformed extension must be an error")
	}

	stream, ws, err = e.StreamScripts([]byte(streamSpec), "GET", "/unknown")
	if err != nil || stream != nil || ws != nil {
		t.Errorf("unknown operation: %+v %+v %v", stream, ws, err)
	}

	if _, _, err = e.StreamScripts([]byte("not yaml: ["), "GET", "/x"); err == nil {
		t.Error("an unparsable contract must be an error")
	}
}
