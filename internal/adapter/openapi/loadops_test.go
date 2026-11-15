package openapi_test

import (
	"encoding/json"
	"strconv"
	"testing"

	openapi "github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const loadOpsSpec = `openapi: 3.0.0
info:
  title: Orders
  version: 1.0.0
paths:
  /orders:
    get:
      operationId: listOrders
      parameters:
        - name: limit
          in: query
          schema:
            type: integer
            minimum: 1
            maximum: 100
        - name: status
          in: query
          required: true
          schema:
            type: string
            enum: [open, closed]
        - name: X-Trace-Id
          in: header
          required: true
          schema:
            type: string
            minLength: 6
      responses:
        '200':
          description: OK
    post:
      operationId: createOrder
      requestBody:
        content:
          application/json:
            schema:
              type: object
              required: [quantity, sku]
              properties:
                quantity:
                  type: integer
                  minimum: 1
                  maximum: 5
                sku:
                  type: string
                  enum: [A1, B2]
      responses:
        '201':
          description: created
  /orders/{orderId}:
    parameters:
      - name: orderId
        in: path
        required: true
        schema:
          type: string
          format: uuid
    delete:
      responses:
        '204':
          description: gone
    get:
      parameters:
        - name: orderId
          in: path
          required: true
          schema:
            type: integer
            minimum: 10
            maximum: 12
      responses:
        '200':
          description: OK
  /upload:
    put:
      requestBody:
        content:
          application/octet-stream:
            schema:
              type: string
              format: binary
      responses:
        '200':
          description: OK
`

func loadOps(t *testing.T) map[string]usecase.LoadOperation {
	t.Helper()

	ops, err := openapi.NewEngine().LoadOperations([]byte(loadOpsSpec))
	if err != nil {
		t.Fatal(err)
	}

	out := map[string]usecase.LoadOperation{}
	for _, op := range ops {
		out[op.Ref()] = op
	}

	return out
}

func TestLoadOperations_ListsEverythingInStableOrder(t *testing.T) {
	t.Parallel()

	ops, err := openapi.NewEngine().LoadOperations([]byte(loadOpsSpec))
	if err != nil {
		t.Fatal(err)
	}

	var refs []string
	for _, op := range ops {
		refs = append(refs, op.Ref())
	}

	want := []string{
		"GET /orders", "POST /orders", "DELETE /orders/{orderId}", "GET /orders/{orderId}", "PUT /upload",
	}
	if len(refs) != len(want) {
		t.Fatalf("operations = %v", refs)
	}

	for i := range want {
		if refs[i] != want[i] {
			t.Errorf("operation %d = %q, want %q", i, refs[i], want[i])
		}
	}
}

func TestLoadOperations_Parameters(t *testing.T) {
	t.Parallel()

	list := loadOps(t)["GET /orders"]

	if list.ID != "listOrders" || !list.Safe || list.Name() != "listOrders" {
		t.Errorf("operation: %+v", list)
	}

	if len(list.QueryParams) != 2 || len(list.HeaderParams) != 1 {
		t.Fatalf("params: %+v", list)
	}

	limit, status := list.QueryParams[0], list.QueryParams[1]
	if limit.Name != "limit" || limit.Required {
		t.Errorf("limit: %+v", limit)
	}

	for _, v := range limit.Values {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			t.Errorf("limit value %q outside 1..100", v)
		}
	}

	if !status.Required || len(status.Values) != 2 || status.Values[0] != "open" {
		t.Errorf("status: %+v", status)
	}

	if hv := list.HeaderParams[0].Values; len(hv) != 1 || len(hv[0]) < 6 {
		t.Errorf("header value must satisfy minLength: %v", hv)
	}
}

func TestLoadOperations_PathLevelParametersAndOverride(t *testing.T) {
	t.Parallel()

	ops := loadOps(t)

	del := ops["DELETE /orders/{orderId}"]
	if del.Safe || len(del.PathParams) != 1 || !del.PathParams[0].Required || del.PathParams[0].Values[0] == "" {
		t.Errorf("inherited path param: %+v", del)
	}

	get := ops["GET /orders/{orderId}"]
	if len(get.PathParams) != 1 {
		t.Fatalf("override must replace, not duplicate: %+v", get.PathParams)
	}

	for _, v := range get.PathParams[0].Values {
		n, err := strconv.Atoi(v)
		if err != nil || n < 10 || n > 12 {
			t.Errorf("overriding schema ignored: %q", v)
		}
	}
}

func TestLoadOperations_Bodies(t *testing.T) {
	t.Parallel()

	ops := loadOps(t)

	create := ops["POST /orders"]
	if create.BodyContentType != "application/json" || len(create.Bodies) == 0 {
		t.Fatalf("create: %+v", create)
	}

	for _, b := range create.Bodies {
		var got struct {
			Quantity int    `json:"quantity"`
			SKU      string `json:"sku"`
		}

		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}

		if got.Quantity < 1 || got.Quantity > 5 || (got.SKU != "A1" && got.SKU != "B2") {
			t.Errorf("body violates its schema: %s", b)
		}
	}

	upload := ops["PUT /upload"]
	if upload.BodyContentType != "application/octet-stream" || len(upload.Bodies) != 0 {
		t.Errorf("non-JSON body: %+v", upload)
	}

	if ops["GET /orders"].BodyContentType != "" {
		t.Error("GET must have no body")
	}
}

func TestLoadOperations_BadContract(t *testing.T) {
	t.Parallel()

	if _, err := openapi.NewEngine().LoadOperations([]byte("{{{")); err == nil {
		t.Error("expected an error")
	}
}
