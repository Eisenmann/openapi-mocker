package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/protobuf"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const grpcTestProto = `syntax = "proto3";
package shop.v1;
message Item { string id = 1; string name = 2; }
message GetItemRequest { string id = 1; }
service Shop {
  rpc GetItem (GetItemRequest) returns (Item);
  rpc ListItems (GetItemRequest) returns (stream Item);
}`

func newGRPCRouter(t *testing.T, grpcAddr string) (h http.Handler, projectID string) {
	t.Helper()

	projects := newMemProjectRepo()
	contracts := newMemContractRepo()
	mocks := newMemMockRepo()
	logs := newMemLogRepo()

	p := projects.Create("shop", "")
	contracts.AddVersion(p.ID, usecase.FormatProto, grpcTestProto, "manual")

	served := usecase.NewMockServingService(contracts, mocks, logs, openapi.NewEngine())

	svc := httpapi.Services{
		Projects:    usecase.NewProjectService(projects),
		MockServing: served,
		GRPCServing: usecase.NewGRPCServingService(projects, contracts, protobuf.NewEngine(), served),
		GRPCAddr:    grpcAddr,
	}

	return httpapi.NewRouter(&svc), p.ID
}

func TestRouter_GRPCServices(t *testing.T) {
	t.Parallel()

	h, id := newGRPCRouter(t, ":9090")

	rec := call(t, h, http.MethodGet, "/api/projects/"+id+"/grpc/services", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	var got []struct {
		Name    string `json:"name"`
		Methods []struct {
			Name            string `json:"name"`
			Path            string `json:"path"`
			ServerStreaming bool   `json:"serverStreaming"`
		} `json:"methods"`
	}

	decodeJSON(t, rec, &got)

	if len(got) != 1 || got[0].Name != "shop.v1.Shop" || len(got[0].Methods) != 2 {
		t.Fatalf("unexpected services: %+v", got)
	}

	if got[0].Methods[0].Path != "/shop.v1.Shop/GetItem" || !got[0].Methods[1].ServerStreaming {
		t.Errorf("unexpected methods: %+v", got[0].Methods)
	}
}

func TestRouter_GRPCServices_NotEnabled(t *testing.T) {
	t.Parallel()

	svc := httpapi.Services{Projects: usecase.NewProjectService(newMemProjectRepo())}
	h := httpapi.NewRouter(&svc)

	if rec := call(t, h, http.MethodGet, "/api/projects/x/grpc/services", ""); rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}

func TestRouter_GRPCInfo(t *testing.T) {
	t.Parallel()

	h, _ := newGRPCRouter(t, ":9090")

	var on struct {
		Enabled    bool   `json:"enabled"`
		Addr       string `json:"addr"`
		ProjectKey string `json:"projectKey"`
	}

	decodeJSON(t, call(t, h, http.MethodGet, "/api/grpc/info", ""), &on)

	if !on.Enabled || on.Addr != ":9090" || on.ProjectKey != "x-mock-project" {
		t.Errorf("unexpected info: %+v", on)
	}

	h, _ = newGRPCRouter(t, "")

	var off struct {
		Enabled bool `json:"enabled"`
	}

	decodeJSON(t, call(t, h, http.MethodGet, "/api/grpc/info", ""), &off)

	if off.Enabled {
		t.Error("gRPC must be reported as disabled without an address")
	}
}
