// Package upstream is the HTTP client that sends proxied requests to the real
// server behind a project (the adapter of usecase.UpstreamClient).
package upstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const (
	// DefaultTimeout bounds one upstream request.
	DefaultTimeout = 30 * time.Second
	// MaxResponseBytes caps the upstream response body that is read.
	MaxResponseBytes = 8 << 20
)

// ErrResponseTooLarge is returned when the upstream body exceeds MaxResponseBytes.
var ErrResponseTooLarge = errors.New("upstream response is too large")

// hopByHop are the connection-level headers a proxy must not forward.
var hopByHop = map[string]bool{
	"Connection": true, "Proxy-Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

// Client is an usecase.UpstreamClient over net/http.
type Client struct {
	http *http.Client
}

// New returns a client with the given timeout (DefaultTimeout when zero).
// Redirects are not followed: the upstream's own answer is what gets proxied.
func New(timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	return &Client{http: &http.Client{
		Transport: nil,
		Jar:       nil,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Do sends the request and reads the whole response.
func (c *Client) Do(ctx context.Context, r *usecase.UpstreamRequest) (*usecase.UpstreamResponse, error) {
	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, bytes.NewReader(r.Body))
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}

	copyHeader(req.Header, r.Header)

	resp, err := c.http.Do(req) //nolint:gosec // the upstream URL is project configuration, validated when saved.
	if err != nil {
		return nil, fmt.Errorf("upstream request failed: %w", scrub(err))
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read upstream response: %w", err)
	}

	if len(body) > MaxResponseBytes {
		return nil, ErrResponseTooLarge
	}

	header := make(map[string][]string, len(resp.Header))
	copyHeader(header, resp.Header)

	return &usecase.UpstreamResponse{StatusCode: resp.StatusCode, Header: header, Body: body}, nil
}

// copyHeader copies the end-to-end headers. Accept-Encoding is left out so
// the transport asks for gzip itself and decodes it: the body that is read
// here is always plain.
func copyHeader(dst, src map[string][]string) {
	for name, values := range src {
		canon := http.CanonicalHeaderKey(name)
		if hopByHop[canon] || canon == "Accept-Encoding" {
			continue
		}

		dst[canon] = append(dst[canon], values...)
	}
}

// scrub drops the request URL from net/http's error text: it can carry a
// token in the query string, and the error ends up in responses and logs.
func scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}

	return err
}
