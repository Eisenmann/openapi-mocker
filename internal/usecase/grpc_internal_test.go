package usecase

import (
	"net/http"
	"testing"
)

func TestIsProto(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"empty", "", false},
		{"proto3", `syntax = "proto3"; message A {}`, true},
		{"proto2", "syntax = 'proto2';\nmessage A {}", true},
		{"rpc only", "service S {\n  rpc Get (A) returns (B);\n}", true},
		{"graphql", "type Query { ping: String }", false},
		{"openapi", "openapi: 3.0.0", false},
		{"json", `{"openapi": "3.0.0"}`, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := IsProto(tc.raw); got != tc.want {
				t.Errorf("IsProto = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetectFormat_Proto(t *testing.T) {
	t.Parallel()

	if got := detectFormat(`syntax = "proto3"; service S { rpc A (X) returns (Y); }`); got != FormatProto {
		t.Errorf("proto contract detected as %q", got)
	}

	if got := detectFormat("type Query { ping: String }"); got != FormatGraphQL {
		t.Errorf("graphql detected as %q", got)
	}
}

func TestGRPCMethodKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		client, server bool
		want           string
	}{
		{false, false, "unary"},
		{false, true, "server-stream"},
		{true, false, "client-stream"},
		{true, true, "bidi"},
	}

	for _, tc := range tests {
		m := &GRPCMethod{ClientStreaming: tc.client, ServerStreaming: tc.server} //nolint:exhaustruct // only the flags matter.
		if got := m.Kind(); got != tc.want {
			t.Errorf("client=%v server=%v: Kind = %q, want %q", tc.client, tc.server, got, tc.want)
		}
	}
}

func TestGRPCCodeOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status int
		fault  string
		want   int
	}{
		{http.StatusOK, "", GRPCOK},
		{http.StatusNotFound, "", GRPCNotFound},
		{http.StatusUnauthorized, "", GRPCUnauthenticated},
		{http.StatusTooManyRequests, "", GRPCResourceExhausted},
		{http.StatusServiceUnavailable, "", GRPCUnavailable},
		{http.StatusInternalServerError, "", GRPCInternal},
		{http.StatusTeapot, "", GRPCInvalidArgument},
		{http.StatusOK, FaultDrop, GRPCUnavailable},
	}

	for _, tc := range tests {
		r := &MockResponse{StatusCode: tc.status, Fault: tc.fault} //nolint:exhaustruct // only status and fault matter.
		if got := grpcCodeOf(r); got != tc.want {
			t.Errorf("status %d fault %q: code %d, want %d", tc.status, tc.fault, got, tc.want)
		}
	}
}

func TestGRPCMessageOf(t *testing.T) {
	t.Parallel()

	r := &MockResponse{Body: []byte(`{"message":"no such item"}`)} //nolint:exhaustruct // only the body matters.
	if got := grpcMessageOf(r); got != "no such item" {
		t.Errorf("message = %q", got)
	}

	r = &MockResponse{Body: []byte("plain text")} //nolint:exhaustruct // only the body matters.
	if got := grpcMessageOf(r); got != "plain text" {
		t.Errorf("message = %q", got)
	}
}

func TestSplitMessages(t *testing.T) {
	t.Parallel()

	got, err := splitMessages([]byte(`[{"a":1},{"a":2}]`), true)
	if err != nil || len(got) != 2 || string(got[1]) != `{"a":2}` {
		t.Fatalf("array on a streaming method: %q, %v", got, err)
	}

	got, err = splitMessages([]byte(`{"a":1}`), false)
	if err != nil || len(got) != 1 {
		t.Fatalf("single object: %q, %v", got, err)
	}

	got, err = splitMessages(nil, false)
	if err != nil || len(got) != 1 || string(got[0]) != "{}" {
		t.Fatalf("empty body: %q, %v", got, err)
	}

	if _, err = splitMessages([]byte(`[1]`), false); err == nil {
		t.Error("an array on a unary method must be an error")
	}

	if _, err = splitMessages([]byte(`[1,`), true); err == nil {
		t.Error("a malformed array must be an error")
	}
}
