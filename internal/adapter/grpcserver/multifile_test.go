package grpcserver_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/grpcserver"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/protobuf"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const multiFileShop = `
// ==== file: common/types.proto ====
syntax = "proto3";
package common;
message Money { string currency = 1; int64 units = 2; }

// ==== file: shop/v1/shop.proto ====
syntax = "proto3";
package shop.v1;
import "common/types.proto";
message Item { string id = 1; common.Money price = 2; }
message GetItemRequest { string id = 1; }
service Shop { rpc GetItem (GetItemRequest) returns (Item); }
`

func TestMultiFileContract_CallAndReflection(t *testing.T) {
	t.Parallel()

	store, err := jsonstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	engine := protobuf.NewEngine()
	mocks := usecase.NewMockServingService(store, store, &testLogs{}, openapi.NewEngine())
	svc := usecase.NewGRPCServingService(store, store, engine, mocks)
	p := store.Create("shop", "")
	store.AddVersion(p.ID, usecase.FormatProto, multiFileShop, "test")
	store.CreateMock(&domain.MockRule{
		ProjectID: p.ID, Path: "/shop.v1.Shop/GetItem", Method: http.MethodPost, StatusCode: http.StatusOK,
		ContentType: "application/json", Body: `{"id":"{{request.body.id}}","price":{"currency":"EUR","units":"12"}}`,
	})

	srv := httptest.NewUnstartedServer(grpcserver.New(svc))
	srv.Config.Protocols = new(http.Protocols)
	srv.Config.Protocols.SetUnencryptedHTTP2(true)
	srv.Start()
	t.Cleanup(srv.Close)

	tr := &http.Transport{Protocols: new(http.Protocols)}
	tr.Protocols.SetUnencryptedHTTP2(true)

	e := &env{t: t, srv: srv, store: store, engine: engine, pid: p.ID, client: &http.Client{Transport: tr}}

	reqWire, err := engine.JSONToWire([]byte(multiFileShop), "shop.v1.GetItemRequest", []byte(`{"id":"9"}`))
	if err != nil {
		t.Fatal(err)
	}

	res := e.do("/shop.v1.Shop/GetItem", bytes.NewReader(frame(reqWire)), map[string]string{"X-Mock-Project": p.ID})
	if res.status != 0 || len(res.msgs) != 1 {
		t.Fatalf("status %d (%s), %d messages", res.status, res.message, len(res.msgs))
	}

	body, err := engine.WireToJSON([]byte(multiFileShop), "shop.v1.Item", res.msgs[0])
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(body), `"id":"9"`) || !strings.Contains(string(body), `"currency":"EUR"`) {
		t.Errorf("response: %s", body)
	}

	// Reflection returns the file and the file it imports, so grpcurl can resolve every type.
	descs, gerr := svc.ReflectFile(p.ID, "shop/v1/shop.proto")
	if gerr != nil || len(descs) != 2 {
		t.Fatalf("ReflectFile: %d descriptors, %v", len(descs), gerr)
	}

	descs, gerr = svc.ReflectSymbol(p.ID, "common.Money")
	if gerr != nil || len(descs) != 1 {
		t.Fatalf("ReflectSymbol: %d descriptors, %v", len(descs), gerr)
	}
}
