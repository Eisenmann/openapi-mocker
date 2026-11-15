// Package loadtest runs load plans against a target and measures the result.
package loadtest

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// Client defaults used when the plan leaves an option at zero.
const (
	defaultTimeout             = 30 * time.Second
	defaultDialTimeout         = 10 * time.Second
	defaultTLSHandshakeTimeout = 10 * time.Second
	defaultIdleConnTimeout     = 90 * time.Second
	defaultMaxIdleConnsPerHost = 1024
	keepAliveProbeInterval     = 30 * time.Second
)

// Client is the HTTP client of one run. It is tuned for load: connections are
// pooled per host without the standard library's low idle limits, redirects
// are returned as they are (a 302 is a result, not something to follow), and
// every opened connection is counted so the report can show how well
// connections were reused.
type Client struct {
	// HTTP sends the requests.
	HTTP      *http.Client
	transport *http.Transport
	opened    atomic.Int64
}

// ErrInternalAddress is returned when a guarded client is made to connect to a
// private, loopback or link-local address.
var ErrInternalAddress = errors.New("connection to a private, loopback or link-local address refused")

// NewClient builds the client for a target's HTTP options; zero options mean
// the defaults above.
func NewClient(o *domain.HTTPOptions) *Client {
	return NewClientWith(o, false)
}

// NewClientWith is NewClient; with blockInternal the client refuses to connect
// to private, loopback and link-local addresses. The check runs on the address
// the name resolved to, so a DNS name that points inside is refused as well.
func NewClientWith(o *domain.HTTPOptions, blockInternal bool) *Client {
	c := new(Client)

	dialer := new(net.Dialer)

	if blockInternal {
		dialer.Control = refuseInternal
	}

	dialer.Timeout = orDefault(o.DialTimeout.Std(), defaultDialTimeout)
	dialer.KeepAlive = keepAliveProbeInterval

	t := new(http.Transport)
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, addr)
		if err == nil {
			c.opened.Add(1)
		}

		return conn, err //nolint:wrapcheck // a dial error is passed on unchanged so net/http classifies it
	}
	t.TLSHandshakeTimeout = orDefault(o.TLSHandshakeTimeout.Std(), defaultTLSHandshakeTimeout)
	t.IdleConnTimeout = orDefault(o.IdleConnTimeout.Std(), defaultIdleConnTimeout)
	t.DisableKeepAlives = o.DisableKeepAlives
	t.MaxConnsPerHost = o.MaxConnsPerHost
	t.MaxIdleConnsPerHost = o.MaxIdleConnsPerHost

	if t.MaxIdleConnsPerHost == 0 {
		t.MaxIdleConnsPerHost = defaultMaxIdleConnsPerHost
	}

	// No overall idle limit; the per-host limit applies.
	t.MaxIdleConns = 0
	t.TLSClientConfig = tlsConfig(o.InsecureSkipVerify)
	t.Protocols = protocols(o.Protocol)
	t.ForceAttemptHTTP2 = o.Protocol != domain.ProtocolHTTP1 && o.Protocol != domain.ProtocolH2C

	c.transport = t
	c.HTTP = new(http.Client)
	c.HTTP.Transport = t
	c.HTTP.Timeout = orDefault(o.Timeout.Std(), defaultTimeout)
	c.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	return c
}

// OpenedConns is the number of connections dialed so far. Compared with the
// number of requests it gives the connection reuse ratio.
func (c *Client) OpenedConns() int64 { return c.opened.Load() }

// Close drops idle connections. Call it when the run ends.
func (c *Client) Close() { c.transport.CloseIdleConnections() }

// refuseInternal is a net.Dialer Control: it sees the resolved address.
func refuseInternal(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %q", ErrInternalAddress, address)
	}

	ip := net.ParseIP(host)
	if ip == nil || usecase.IsInternalIP(ip) {
		return fmt.Errorf("%w: %s", ErrInternalAddress, host)
	}

	return nil
}

func orDefault(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}

	return v
}

func tlsConfig(insecure bool) *tls.Config {
	cfg := new(tls.Config)
	cfg.MinVersion = tls.VersionTLS12
	// Skipping verification is an explicit per-plan option for targets with
	// self-signed certificates (staging, local).
	cfg.InsecureSkipVerify = insecure //nolint:gosec // opt-in via http.insecureSkipVerify in the plan

	return cfg
}

func protocols(p domain.Protocol) *http.Protocols {
	ps := new(http.Protocols)

	switch p {
	case domain.ProtocolHTTP1:
		ps.SetHTTP1(true)
	case domain.ProtocolHTTP2:
		ps.SetHTTP2(true)
	case domain.ProtocolH2C:
		ps.SetUnencryptedHTTP2(true)
	default:
		ps.SetHTTP1(true)
		ps.SetHTTP2(true)
	}

	return ps
}
