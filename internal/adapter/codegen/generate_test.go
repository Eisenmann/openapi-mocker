package codegen

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

const testOpenAPI = `openapi: 3.0.0
info:
  title: Test API
  version: 1.0.0
paths:
  /users:
    get:
      operationId: listUsers
      responses:
        '200':
          description: OK
    post:
      operationId: createUser
      responses:
        '201':
          description: Created
  /users/{id}:
    get:
      operationId: getUser
      parameters:
        - name: id
          in: path
          required: true
          schema:
            type: string
      responses:
        '200':
          description: OK
`

func TestToGoIdent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want string
	}{
		{"listUsers", "ListUsers"},
		{"get-user", "GetUser"},
		{"get_user", "Get_user"},
		{"get user", "GetUser"},
		{"123abc", "Op123abc"},
		{"", "Operation"},
		{"---", "Operation"},
		{"a", "A"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()

			got := toGoIdent(tt.in)
			if got != tt.want {
				t.Errorf("toGoIdent(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExtractPathParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want []string
	}{
		{"/users", nil},
		{"/users/{id}", []string{"id"}},
		{"/users/{id}/orders/{orderId}", []string{"id", "orderId"}},
		{"/a/{x}/b/{y}/c", []string{"x", "y"}},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()

			got := extractPathParams(tt.path)
			if len(got) != len(tt.want) {
				t.Fatalf("extractPathParams(%q) = %v, want %v", tt.path, got, tt.want)
			}

			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("extractPathParams(%q)[%d] = %q, want %q", tt.path, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestHasBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method string
		want   bool
	}{
		{"GET", false},
		{"POST", true},
		{"PUT", true},
		{"PATCH", true},
		{"DELETE", false},
		{"HEAD", false},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			t.Parallel()

			if got := hasBody(tt.method); got != tt.want {
				t.Errorf("hasBody(%q) = %v, want %v", tt.method, got, tt.want)
			}
		})
	}
}

func TestCollectOperations(t *testing.T) {
	t.Parallel()

	doc, err := parseAndValidate(context.Background(), []byte(testOpenAPI))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ops := collectOperations(doc)
	if len(ops) != 3 {
		t.Fatalf("expected 3 operations, got %d", len(ops))
	}

	// Sorted by path then method.
	if ops[0].Path != "/users" || ops[0].Method != "GET" || ops[0].OpID != "ListUsers" {
		t.Errorf("unexpected first op: %+v", ops[0])
	}

	if ops[1].Path != "/users" || ops[1].Method != "POST" || ops[1].OpID != "CreateUser" {
		t.Errorf("unexpected second op: %+v", ops[1])
	}

	if ops[2].Path != "/users/{id}" || ops[2].OpID != "GetUser" {
		t.Errorf("unexpected third op: %+v", ops[2])
	}
}

func TestCollectOperations_NoOperationID(t *testing.T) {
	t.Parallel()

	raw := `openapi: 3.0.0
info:
  title: Test
  version: 1.0.0
paths:
  /ping:
    get:
      responses:
        '200':
          description: OK
`

	doc, err := parseAndValidate(context.Background(), []byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ops := collectOperations(doc)
	if len(ops) != 1 {
		t.Fatalf("expected 1 operation, got %d", len(ops))
	}

	if ops[0].OpID != "GET_Ping" {
		t.Errorf("expected generated OpID GET_Ping, got %q", ops[0].OpID)
	}
}

func TestGenerateGoServer(t *testing.T) {
	t.Parallel()

	doc, err := parseAndValidate(context.Background(), []byte(testOpenAPI))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	files := generateGoServer(doc, "api")
	if len(files) != 4 {
		t.Fatalf("expected 4 files, got %d", len(files))
	}

	// server_interface.go.
	iface, ok := files["server_interface.go"]
	if !ok {
		t.Fatal("missing server_interface.go")
	}

	if !strings.Contains(iface, "type ServerInterface interface") {
		t.Error("missing ServerInterface declaration")
	}

	if !strings.Contains(iface, "ListUsers(w http.ResponseWriter, r *http.Request)") {
		t.Error("missing ListUsers method")
	}

	if !strings.Contains(iface, "CreateUser(w http.ResponseWriter, r *http.Request)") {
		t.Error("missing CreateUser method")
	}

	if !strings.Contains(iface, "GetUser(w http.ResponseWriter, r *http.Request)") {
		t.Error("missing GetUser method")
	}

	// router.go.
	router, ok := files["router.go"]
	if !ok {
		t.Fatal("missing router.go")
	}

	if !strings.Contains(router, `mux.HandleFunc("GET /users", impl.ListUsers)`) {
		t.Error("missing GET /users route")
	}

	if !strings.Contains(router, `mux.HandleFunc("POST /users", impl.CreateUser)`) {
		t.Error("missing POST /users route")
	}

	if !strings.Contains(router, `mux.HandleFunc("GET /users/{id}", impl.GetUser)`) {
		t.Error("missing GET /users/{id} route")
	}

	// unimplemented.go.
	impl, ok := files["unimplemented.go"]
	if !ok {
		t.Fatal("missing unimplemented.go")
	}

	if !strings.Contains(impl, "type UnimplementedServer struct{}") {
		t.Error("missing UnimplementedServer")
	}

	if !strings.Contains(impl, "func (UnimplementedServer) ListUsers(") {
		t.Error("missing ListUsers implementation")
	}

	// cmd_main_example.go.
	mainFile, ok := files["cmd_main_example.go"]
	if !ok {
		t.Fatal("missing cmd_main_example.go")
	}

	if !strings.Contains(mainFile, "package main") {
		t.Error("missing package main")
	}

	if !strings.Contains(mainFile, "srv.NewRouter(srv.UnimplementedServer{})") {
		t.Error("missing router setup")
	}
}

func TestGenerateGoClient(t *testing.T) {
	t.Parallel()

	doc, err := parseAndValidate(context.Background(), []byte(testOpenAPI))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	files := generateGoClient(doc, "api")
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	client, ok := files["client.go"]
	if !ok {
		t.Fatal("missing client.go")
	}

	if !strings.Contains(client, "type Client struct") {
		t.Error("missing Client struct")
	}

	if !strings.Contains(client, "func NewClient(baseURL string) *Client") {
		t.Error("missing NewClient")
	}

	if !strings.Contains(client, "func (c *Client) ListUsers(ctx context.Context, pathParams map[string]string)") {
		t.Error("missing ListUsers client method")
	}

	if !strings.Contains(client, "func (c *Client) CreateUser(ctx context.Context, pathParams map[string]string, body interface{})") {
		t.Error("missing CreateUser client method with body")
	}

	if !strings.Contains(client, "// required path parameters: id") {
		t.Error("missing path parameter comment")
	}
}

func TestGenerator_GenerateServerZip(t *testing.T) {
	t.Parallel()

	g := NewGenerator()

	zipData, err := g.GenerateServerZip([]byte(testOpenAPI), "api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		t.Fatalf("invalid zip: %v", err)
	}

	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}

	for _, want := range []string{"server_interface.go", "router.go", "unimplemented.go", "cmd_main_example.go"} {
		if !names[want] {
			t.Errorf("missing %s in zip", want)
		}
	}

	// Invalid contract.
	_, err = g.GenerateServerZip([]byte("invalid"), "api")
	if err == nil {
		t.Fatal("expected error for invalid contract")
	}
}

func TestGenerator_GenerateClientZip(t *testing.T) {
	t.Parallel()

	g := NewGenerator()

	zipData, err := g.GenerateClientZip([]byte(testOpenAPI), "api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		t.Fatalf("invalid zip: %v", err)
	}

	if len(zr.File) != 1 {
		t.Fatalf("expected 1 file in zip, got %d", len(zr.File))
	}

	if zr.File[0].Name != "client.go" {
		t.Errorf("expected client.go, got %q", zr.File[0].Name)
	}

	// Read content to verify it's valid Go.
	rc, err := zr.File[0].Open()
	if err != nil {
		t.Fatalf("failed to open file: %v", err)
	}
	defer rc.Close()

	content, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if !strings.Contains(string(content), "package api") {
		t.Error("expected package api in client.go")
	}

	// Invalid contract.
	_, err = g.GenerateClientZip([]byte("invalid"), "api")
	if err == nil {
		t.Fatal("expected error for invalid contract")
	}
}
