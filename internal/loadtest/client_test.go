package loadtest_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest"
)

func get(t *testing.T, c *loadtest.Client, url string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	return resp
}

func tlsServer(t *testing.T, h2 bool) *httptest.Server {
	t.Helper()

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	srv.EnableHTTP2 = h2
	srv.StartTLS()
	t.Cleanup(srv.Close)

	return srv
}

func TestClient_ProtocolInUse(t *testing.T) {
	t.Parallel()

	h2srv := tlsServer(t, true)

	cases := []struct {
		name  string
		proto domain.Protocol
		major int
	}{
		{"negotiated uses h2 when offered", "", 2},
		{"http2", domain.ProtocolHTTP2, 2},
		{"http1 stays on 1.1 even if h2 is offered", domain.ProtocolHTTP1, 1},
	}

	for _, tc := range cases {
		c := loadtest.NewClient(&domain.HTTPOptions{Protocol: tc.proto, InsecureSkipVerify: true})

		if got := get(t, c, h2srv.URL).ProtoMajor; got != tc.major {
			t.Errorf("%s: HTTP/%d, want HTTP/%d", tc.name, got, tc.major)
		}

		c.Close()
	}
}

func TestClient_H2C(t *testing.T) {
	t.Parallel()

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetHTTP1(true)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)

	c := loadtest.NewClient(&domain.HTTPOptions{Protocol: domain.ProtocolH2C})
	if got := get(t, c, srv.URL).ProtoMajor; got != 2 {
		t.Errorf("h2c: HTTP/%d, want HTTP/2", got)
	}
}

func TestClient_TLSVerification(t *testing.T) {
	t.Parallel()

	srv := tlsServer(t, false)
	strict := loadtest.NewClient(&domain.HTTPOptions{})

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if resp, err := strict.HTTP.Do(req); err == nil {
		_ = resp.Body.Close()

		t.Error("a self-signed certificate must be rejected unless insecureSkipVerify is set")
	}
}

func TestClient_ConnectionReuse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	reuse := loadtest.NewClient(&domain.HTTPOptions{})
	fresh := loadtest.NewClient(&domain.HTTPOptions{DisableKeepAlives: true})

	for range 10 {
		get(t, reuse, srv.URL)
		get(t, fresh, srv.URL)
	}

	if reuse.OpenedConns() != 1 {
		t.Errorf("keep-alive client opened %d connections, want 1", reuse.OpenedConns())
	}

	if fresh.OpenedConns() != 10 {
		t.Errorf("no-keep-alive client opened %d connections, want 10", fresh.OpenedConns())
	}
}

func TestClient_RedirectsAreResults(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	if got := get(t, loadtest.NewClient(&domain.HTTPOptions{}), srv.URL).StatusCode; got != http.StatusFound {
		t.Errorf("status %d, want the 302 itself", got)
	}
}

func TestClient_Timeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	t.Cleanup(srv.Close)

	c := loadtest.NewClient(&domain.HTTPOptions{Timeout: domain.Duration(50 * time.Millisecond)})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)

	if resp, err := c.HTTP.Do(req); err == nil {
		_ = resp.Body.Close()

		t.Error("the request must time out")
	}
}

func TestClient_BlocksInternalAddresses(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	guarded := loadtest.NewClientWith(&domain.HTTPOptions{}, true)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)

	resp, err := guarded.HTTP.Do(req)
	if err == nil {
		_ = resp.Body.Close()

		t.Fatal("a guarded client must not connect to a loopback address")
	}

	if !errors.Is(err, loadtest.ErrInternalAddress) {
		t.Errorf("error = %v, want ErrInternalAddress", err)
	}

	if got := get(t, loadtest.NewClientWith(&domain.HTTPOptions{}, false), srv.URL).StatusCode; got != http.StatusOK {
		t.Errorf("an unguarded client got %d", got)
	}
}
