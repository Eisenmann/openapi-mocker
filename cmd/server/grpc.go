package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/grpcserver"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// setupGRPC wires the gRPC mock. The listener is opt-in: GRPC_PORT (for
// example 9090) or GRPC_ADDR (for example 127.0.0.1:9090) switches it on.
// It returns nil when gRPC is disabled.
func setupGRPC(
	services *httpapi.Services, store *jsonstore.Store, engine usecase.ProtoEngine,
	mocks *usecase.MockServingService, mockService *usecase.MockService,
) *http.Server {
	services.GRPCServing = usecase.NewGRPCServingService(store, store, engine, mocks)
	mockService.WithGRPCMethods(services.GRPCServing)

	addr := grpcAddr(os.Getenv("GRPC_ADDR"), os.Getenv("GRPC_PORT"))
	if addr == "" {
		return nil
	}

	services.GRPCAddr = addr

	srv := grpcserver.New(services.GRPCServing).HTTPServer(addr)

	cert, key := strings.TrimSpace(os.Getenv("GRPC_TLS_CERT")), strings.TrimSpace(os.Getenv("GRPC_TLS_KEY"))
	if cert == "" && key == "" {
		log.Printf("gRPC mock (plain HTTP/2, reflection on) on %s", addr) //nolint:gosec // G706: operator-set address.

		return srv
	}

	err := grpcserver.EnableTLS(srv, cert, key)
	if err != nil {
		log.Fatalf("gRPC TLS: %v (set both GRPC_TLS_CERT and GRPC_TLS_KEY to PEM files)", err)
	}

	services.GRPCTLS = true

	log.Printf("gRPC mock (TLS, reflection on) on %s", addr) //nolint:gosec // G706: operator-set address.

	return srv
}

// grpcAddr builds the listen address from GRPC_ADDR / GRPC_PORT ("" = off).
func grpcAddr(addr, port string) string {
	addr, port = strings.TrimSpace(addr), strings.TrimSpace(port)

	switch {
	case addr != "":
		return addr
	case port != "":
		return ":" + strings.TrimPrefix(port, ":")
	default:
		return ""
	}
}
