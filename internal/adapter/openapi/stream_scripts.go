package openapi

import (
	"encoding/json"
	"fmt"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// Operation extensions that script streaming endpoints.
const (
	// ExtStream scripts a Server-Sent Events stream: {"steps": [...], "loop": bool}.
	ExtStream = "x-mock-stream"
	// ExtWebSocket scripts a WebSocket endpoint: {"onConnect": [...], "rules": [...]}.
	ExtWebSocket = "x-mock-websocket"
)

var _ usecase.StreamScripter = (*Engine)(nil)

// StreamScripts returns the x-mock-stream and x-mock-websocket scripts of the
// operation. An empty x-mock-websocket object ({}) declares an echo endpoint.
func (e *Engine) StreamScripts(raw []byte, method, path string) (*domain.StreamScript, *domain.WSScript, error) {
	doc, err := parse(raw)
	if err != nil {
		return nil, nil, err
	}

	m := findOperation(doc, method, path)
	if m == nil || m.Operation == nil {
		return nil, nil, nil
	}

	var (
		stream *domain.StreamScript
		ws     *domain.WSScript
	)

	if v, ok := m.Operation.Extensions[ExtStream]; ok {
		stream = new(domain.StreamScript)

		err = decodeExtension(ExtStream, v, stream)
		if err != nil {
			return nil, nil, err
		}

		if len(stream.Steps) == 0 {
			stream = nil
		}
	}

	if v, ok := m.Operation.Extensions[ExtWebSocket]; ok {
		ws = new(domain.WSScript)

		err = decodeExtension(ExtWebSocket, v, ws)
		if err != nil {
			return nil, nil, err
		}
	}

	return stream, ws, nil
}

func decodeExtension(name string, v, into any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	err = json.Unmarshal(b, into)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	return nil
}
