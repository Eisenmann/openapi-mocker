package domain

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

// ProxyMode says what the mock server does with a request that no mock rule
// answers.
type ProxyMode string

const (
	// ProxyOff answers from the stored state or the contract's schema example
	// (the default).
	ProxyOff ProxyMode = "off"
	// ProxyForward sends the request to the upstream server and returns its
	// response, without saving anything.
	ProxyForward ProxyMode = "forward"
	// ProxyRecord forwards like ProxyForward and also saves the response as a
	// mock rule, so the same request is answered by the mock from then on.
	ProxyRecord ProxyMode = "record"
)

// Valid reports whether m is a known mode. The empty string means "off", so
// projects stored before this setting existed keep working.
func (m ProxyMode) Valid() bool {
	switch m {
	case "", ProxyOff, ProxyForward, ProxyRecord:
		return true
	default:
		return false
	}
}

// Forwards reports whether the mode sends unmatched requests upstream.
func (m ProxyMode) Forwards() bool {
	return m == ProxyForward || m == ProxyRecord
}

// ProxyConfig is the record/replay and drift-detection part of a project.
type ProxyConfig struct {
	// UpstreamURL is the base URL of the real server, e.g.
	// https://api.example.com/v1. The request path is appended to it.
	UpstreamURL string    `json:"upstreamUrl"`
	Mode        ProxyMode `json:"proxyMode"`
	// DriftCheck compares what the mock answers with what the upstream
	// answers to the same (safe) request, and reports the differences.
	DriftCheck bool `json:"driftCheck"`
}

// Errors returned by ProxyConfig.Validate.
var (
	ErrProxyMode        = errors.New("proxyMode must be one of: off, forward, record")
	ErrUpstreamURL      = errors.New("upstreamUrl must be an absolute http or https URL without credentials")
	ErrUpstreamRequired = errors.New("upstreamUrl is required for proxyMode forward or record and for driftCheck")
)

// Validate checks the configuration.
func (c ProxyConfig) Validate() error {
	if !c.Mode.Valid() {
		return ErrProxyMode
	}

	if strings.TrimSpace(c.UpstreamURL) == "" {
		if c.Mode.Forwards() || c.DriftCheck {
			return ErrUpstreamRequired
		}

		return nil
	}

	u, err := url.Parse(c.UpstreamURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ErrUpstreamURL
	}

	return nil
}

// Recording marks a mock rule that was saved from an upstream response.
type Recording struct {
	// Key identifies the recorded request (method, path, query and body), so
	// the same request is answered by this rule from then on.
	Key string `json:"key"`
	// Request is a readable form of the recorded request, e.g.
	// "GET /users/7?limit=2".
	Request    string    `json:"request"`
	RecordedAt time.Time `json:"recordedAt"`
}
