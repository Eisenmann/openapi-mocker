package grpcserver_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/grpcserver"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/protobuf"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// writeSelfSigned writes a throw-away certificate for localhost and returns the file paths.
func writeSelfSigned(t *testing.T) (certFile, keyFile string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{ //nolint:exhaustruct // only what a test certificate needs.
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"}, //nolint:exhaustruct // only the name.
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")

	write := func(path, kind string, b []byte) {
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: b}), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(certFile, "CERTIFICATE", der)
	write(keyFile, "EC PRIVATE KEY", keyDER)

	return certFile, keyFile
}

func TestTLS_UnaryOverHTTP2(t *testing.T) {
	t.Parallel()

	store, err := jsonstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	engine := protobuf.NewEngine()
	mocks := usecase.NewMockServingService(store, store, &testLogs{}, openapi.NewEngine())
	svc := usecase.NewGRPCServingService(store, store, engine, mocks)
	p := store.Create("shop", "")
	store.AddVersion(p.ID, usecase.FormatProto, shopProto, "test")

	srv := grpcserver.New(svc).HTTPServer("127.0.0.1:0")
	certFile, keyFile := writeSelfSigned(t)

	if grpcserver.IsTLS(srv) {
		t.Fatal("TLS must be off before EnableTLS")
	}

	if err = grpcserver.EnableTLS(srv, certFile, keyFile); err != nil {
		t.Fatal(err)
	}

	if !grpcserver.IsTLS(srv) {
		t.Fatal("TLS must be on after EnableTLS")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	go func() { _ = srv.ServeTLS(ln, "", "") }()

	t.Cleanup(func() { _ = srv.Close() })

	client := &http.Client{Transport: &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec // test certificate.
	}}

	e := &env{t: t, store: store, engine: engine, pid: p.ID, client: client}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://"+ln.Addr().String()+"/shop.v1.Shop/GetItem", bytes.NewReader(frame(e.wire("shop.v1.GetItemRequest", `{"id":"7"}`))))
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("Te", "trailers")
	req.Header.Set("X-Mock-Project", p.ID)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.ProtoMajor != 2 {
		t.Errorf("negotiated HTTP/%d, want HTTP/2", resp.ProtoMajor)
	}

	res := readResult(t, resp)
	if res.status != 0 || len(res.msgs) != 1 {
		t.Errorf("status %d, %d messages", res.status, len(res.msgs))
	}
}

func TestEnableTLS_BadFiles(t *testing.T) {
	t.Parallel()

	srv := &http.Server{} //nolint:exhaustruct // only the TLS fields matter.
	if err := grpcserver.EnableTLS(srv, "/no/such/cert.pem", "/no/such/key.pem"); err == nil {
		t.Error("missing files must be an error")
	}

	if grpcserver.IsTLS(srv) {
		t.Error("a failed EnableTLS must leave TLS off")
	}
}
