package logsink

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

var _ usecase.LogSink = (*HTTP)(nil)

// ErrNoURL is returned by the constructors when no endpoint is configured.
var ErrNoURL = errors.New("log sink URL is required")

// HTTP posts batches as newline-delimited JSON (application/x-ndjson), which
// Logstash (http input, json_lines codec), Vector and similar collectors accept.
type HTTP struct {
	url    string
	token  string
	client *http.Client
}

// NewHTTP posts to url; a non-empty token is sent as a bearer token.
func NewHTTP(url, token string, client *http.Client) (*HTTP, error) {
	if url == "" {
		return nil, ErrNoURL
	}

	if client == nil {
		client = http.DefaultClient
	}

	return &HTTP{url: url, token: token, client: client}, nil
}

// Name implements usecase.LogSink.
func (*HTTP) Name() string { return "http" }

// Send implements usecase.LogSink.
func (h *HTTP) Send(ctx context.Context, batch []*domain.RequestLog) error {
	body, err := ndjson(batch)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/x-ndjson")

	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}

	return doOK(h.client, req)
}

// doOK sends the request and treats any non-2xx status as an error.
func doOK(client *http.Client, req *http.Request) error {
	resp, err := client.Do(req) //nolint:gosec // the URL is operator configuration.
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode/statusClassDiv != successClass {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyBytes))

		return fmt.Errorf("%w: %s: %s", errStatus, resp.Status, bytes.TrimSpace(msg))
	}

	_, _ = io.Copy(io.Discard, resp.Body)

	return nil
}

const (
	statusClassDiv = 100
	successClass   = 2
	// errorBodyBytes is how much of an error response goes into the error.
	errorBodyBytes = 512
	// bulkReplyBytes bounds the bulk response that is read and parsed.
	bulkReplyBytes = 4 << 20
)

var errStatus = errors.New("log sink rejected the batch")
