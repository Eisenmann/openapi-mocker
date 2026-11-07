package usecase

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

const (
	// DefaultCaptureBytes is how much of a body is kept by default.
	DefaultCaptureBytes = 16 << 10
	// MaxCaptureBytes is the largest body capture that can be configured.
	MaxCaptureBytes = 256 << 10
	// rawCaptureBytes bounds what the serving services hand to the log
	// hub, so a 10 MB upload is never copied just to be cut down later.
	rawCaptureBytes = MaxCaptureBytes

	redactedValue = "[redacted]"
)

// CapturePolicy says whether and how much of each body is kept in the log.
type CapturePolicy struct {
	Enabled  bool
	MaxBytes int
}

// DefaultCapturePolicy captures up to DefaultCaptureBytes of every body.
func DefaultCapturePolicy() CapturePolicy {
	return CapturePolicy{Enabled: true, MaxBytes: DefaultCaptureBytes}
}

// normalized clamps MaxBytes into 1..MaxCaptureBytes.
func (p CapturePolicy) normalized() CapturePolicy {
	switch {
	case p.MaxBytes <= 0:
		p.MaxBytes = DefaultCaptureBytes
	case p.MaxBytes > MaxCaptureBytes:
		p.MaxBytes = MaxCaptureBytes
	}

	return p
}

// sensitiveHeaders are never stored with their value.
var sensitiveHeaders = map[string]bool{ //nolint:gochecknoglobals // a constant table.
	"authorization":       true,
	"proxy-authorization": true,
	"cookie":              true,
	"set-cookie":          true,
	"x-api-key":           true,
	"api-key":             true,
	"x-auth-token":        true,
	"x-share-token":       true,
}

// rawCapture builds the capture a serving service attaches to a log entry.
// It is bounded but not yet subject to the policy: the LogHub applies that.
func rawCapture(header http.Header, body []byte) *domain.HTTPCapture {
	c := &domain.HTTPCapture{
		Headers: nil, Body: "", Size: len(body), Truncated: false, Binary: false,
	}

	if len(header) > 0 {
		c.Headers = make(map[string][]string, len(header))
		for k, v := range header {
			c.Headers[k] = append([]string(nil), v...)
		}
	}

	if len(body) > rawCaptureBytes {
		body = body[:rawCaptureBytes]
		c.Truncated = true
	}

	c.Body = string(body)

	return c
}

// responseCapture captures a response; contentType and extra headers are
// folded into the header map.
func responseCapture(contentType string, headers map[string]string, body []byte) *domain.HTTPCapture {
	h := make(http.Header, len(headers)+1)

	for k, v := range headers {
		h.Set(k, v)
	}

	if contentType != "" {
		h.Set("Content-Type", contentType)
	}

	return rawCapture(h, body)
}

// apply enforces the policy on a raw capture and returns the stored form.
func (p CapturePolicy) apply(c *domain.HTTPCapture) *domain.HTTPCapture {
	if c == nil || !p.Enabled {
		return nil
	}

	out := &domain.HTTPCapture{
		Headers: redactHeaders(c.Headers), Body: c.Body, Size: c.Size,
		Truncated: c.Truncated, Binary: false,
	}

	if len(out.Body) > p.MaxBytes {
		out.Body = cutUTF8(out.Body, p.MaxBytes)
		out.Truncated = true
	}

	if out.Truncated {
		out.Body = trimPartialRune(out.Body)
	}

	if !utf8.ValidString(out.Body) {
		out.Body = ""
		out.Binary = true
		out.Truncated = false
	}

	return out
}

// cutUTF8 shortens s to at most n bytes without splitting a character.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}

	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}

	return s[:n]
}

func redactHeaders(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}

	out := make(map[string][]string, len(in))

	for k, v := range in {
		if sensitiveHeaders[strings.ToLower(k)] {
			out[k] = []string{redactedValue}
			continue
		}

		out[k] = append([]string(nil), v...)
	}

	return out
}

// trimPartialRune drops an incomplete character left at the end by a cut.
func trimPartialRune(s string) string {
	for range utf8.UTFMax - 1 {
		if utf8.ValidString(s) {
			return s
		}

		s = s[:len(s)-1]
	}

	return s
}

// graphQLEndpoint is the Endpoint of every served GraphQL operation.
const graphQLEndpoint = "graphql"

func jsonHeader() http.Header {
	return http.Header{"Content-Type": []string{ContentTypeJSON}}
}
