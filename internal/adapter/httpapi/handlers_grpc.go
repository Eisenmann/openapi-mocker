package httpapi

import (
	"errors"
	"net/http"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

var errGRPCNotEnabled = errors.New("gRPC is not enabled")

// grpcServices lists the services and methods of a project's .proto contract,
// with sample request and response messages.
func (a *api) grpcServices(w http.ResponseWriter, r *http.Request) {
	if a.s.GRPCServing == nil {
		writeError(w, http.StatusNotFound, errGRPCNotEnabled)

		return
	}

	svcs, err := a.s.GRPCServing.Services(r.PathValue("id"))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)

		return
	}

	writeJSON(w, http.StatusOK, svcs)
}

// grpcInfo tells the UI where the gRPC listener is (if enabled).
func (a *api) grpcInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":    a.s.GRPCAddr != "",
		"addr":       a.s.GRPCAddr,
		"reflection": a.s.GRPCAddr != "",
		"tls":        a.s.GRPCTLS,
		"projectKey": usecase.GRPCProjectMetadata,
	})
}
