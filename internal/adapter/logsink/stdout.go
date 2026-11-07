package logsink

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sync"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

var _ usecase.LogSink = (*Stdout)(nil)

// Stdout writes one JSON object per line, the format log collectors expect
// from a container's standard output.
type Stdout struct {
	mu sync.Mutex
	w  io.Writer
}

// NewStdout writes to w (os.Stdout in the server).
func NewStdout(w io.Writer) *Stdout { return &Stdout{mu: sync.Mutex{}, w: w} }

// Name implements usecase.LogSink.
func (*Stdout) Name() string { return "stdout" }

// Send implements usecase.LogSink.
func (s *Stdout) Send(_ context.Context, batch []*domain.RequestLog) error {
	lines, err := ndjson(batch)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err = s.w.Write(lines)

	return err
}

// avgRecordBytes is a rough size of one record, to size the buffer.
const avgRecordBytes = 384

// newEncoder writes JSON lines without escaping <, > and & (messages carry
// "->" and paths carry "&").
func newEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	return enc
}

// ndjson renders the batch as newline-delimited JSON records.
func ndjson(batch []*domain.RequestLog) ([]byte, error) {
	var buf bytes.Buffer

	buf.Grow(len(batch) * avgRecordBytes)

	enc := newEncoder(&buf)

	for _, l := range batch {
		err := enc.Encode(ToRecord(l))
		if err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}
