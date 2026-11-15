package loadtest_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/loadtest"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func testOps() []usecase.LoadOperation {
	return []usecase.LoadOperation{
		{
			ID: "listPets", Method: "GET", Path: "/pets", Safe: true,
			QueryParams:  []usecase.LoadParam{{Name: "limit", Required: true, Values: []string{"10"}}, {Name: "q", Values: []string{"x"}}},
			HeaderParams: []usecase.LoadParam{{Name: "X-Tenant", Required: true, Values: []string{"t1"}}},
		},
		{
			Method: "GET", Path: "/pets/{petId}", Safe: true,
			PathParams: []usecase.LoadParam{{Name: "petId", Required: true, Values: []string{"a b/7"}}},
		},
		{
			ID: "createPet", Method: "POST", Path: "/pets", BodyContentType: "application/json",
			Bodies: [][]byte{[]byte(`{"name":"rex","age":3}`)},
		},
	}
}

func newTestBuilder(t *testing.T, mutate func(c *loadtest.BuilderConfig)) *loadtest.Builder {
	t.Helper()

	base, _ := url.Parse("http://127.0.0.1:8080/mock/p1")
	users, _ := loadtest.NewFeeder("users", domain.FeederUnique, []map[string]any{
		{"email": "ann@x.io", "profile": map[string]any{"city": "Oslo"}},
	})

	cfg := &loadtest.BuilderConfig{
		Target: base, ProjectID: "p1", Engine: usecase.NewTemplateEngine(), Operations: testOps(),
		Feeders: map[string]*loadtest.Feeder{"users": users},
	}
	if mutate != nil {
		mutate(cfg)
	}

	b, err := loadtest.NewBuilder(cfg)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func build(t *testing.T, b *loadtest.Builder, st *domain.Step, state *loadtest.IterState) *loadtest.Built {
	t.Helper()

	got, err := b.Build(context.Background(), "sc", st, state)
	if err != nil {
		t.Fatal(err)
	}

	return got
}

func TestBuild_FromContractOperation(t *testing.T) {
	t.Parallel()

	b := newTestBuilder(t, nil)

	list := build(t, b, &domain.Step{Operation: "listPets"}, loadtest.NewIterState())
	req := list.Request

	if req.Method != "GET" || req.URL.Path != "/mock/p1/pets" || req.URL.Query().Get("limit") != "10" ||
		req.URL.Query().Has("q") || req.Header.Get("X-Tenant") != "t1" || req.Body != nil {
		t.Errorf("list: %s %s headers %v", req.Method, req.URL, req.Header)
	}

	if list.Operation != "listPets" || list.Step != "sc/listPets" || list.Path != "/pets" || list.Spec == nil {
		t.Errorf("names: %+v", list)
	}

	// "METHOD /path" finds the operation too, and path params are escaped.
	get := build(t, b, &domain.Step{Operation: "get /pets/{petId}"}, loadtest.NewIterState())
	if get.Request.URL.EscapedPath() != "/mock/p1/pets/a%20b%2F7" || get.Operation != "GET /pets/{petId}" || get.Path != "/pets/a%20b%2F7" {
		t.Errorf("get: %s op %q path %q", get.Request.URL.EscapedPath(), get.Operation, get.Path)
	}

	create := build(t, b, &domain.Step{Operation: "createPet"}, loadtest.NewIterState())

	data, _ := io.ReadAll(create.Request.Body)
	if create.Request.Method != "POST" || string(data) != `{"name":"rex","age":3}` ||
		create.Request.Header.Get("Content-Type") != "application/json" || create.Request.ContentLength != int64(len(data)) {
		t.Errorf("create: %s %q %v", create.Request.Method, data, create.Request.Header)
	}
}

func TestBuild_StepOverridesAndTemplates(t *testing.T) {
	t.Parallel()

	b := newTestBuilder(t, nil)
	st := loadtest.NewIterState()
	st.Vars["token"] = "abc"
	st.Vars["order"] = json.Number("42")

	got := build(t, b, &domain.Step{
		Name: "pay", Method: "POST", Path: "/orders/{{vars.order}}/pay?x=1",
		Headers: map[string]string{"Authorization": "Bearer {{vars.token}}", "X-Req": "{{request.method}}"},
		Body:    `{"email": "{{data.users.email}}", "city": "{{data.users.profile.city}}", "order": {{vars.order}}, "missing": {{vars.nope}}}`,
	}, st)

	req := got.Request
	data, _ := io.ReadAll(req.Body)

	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("body is not JSON: %s (%v)", data, err)
	}

	if body["email"] != "ann@x.io" || body["city"] != "Oslo" || body["order"] != float64(42) || body["missing"] != nil {
		t.Errorf("body: %s", data)
	}

	if req.URL.Path != "/mock/p1/orders/42/pay" || req.URL.Query().Get("x") != "1" || req.Header.Get("Authorization") != "Bearer abc" ||
		req.Header.Get("X-Req") != "POST" || req.Header.Get("Content-Type") != "application/json" || got.Step != "sc/pay" ||
		got.Operation != "POST /orders/{{vars.order}}/pay?x=1" || got.Spec != nil {
		t.Errorf("request: %s headers %v op %q", req.URL, req.Header, got.Operation)
	}

	// The feeder row is drawn once per iteration: a second step sees the same row.
	again := build(t, b, &domain.Step{Method: "GET", Path: "/u/{{data.users.email}}"}, st)
	if again.Request.URL.Path != "/mock/p1/u/ann@x.io" {
		t.Errorf("second step: %s", again.Request.URL)
	}
}

func TestBuild_FakerPlaceholdersAndFromSchema(t *testing.T) {
	t.Parallel()

	b := newTestBuilder(t, nil)

	got := build(t, b, &domain.Step{Method: "POST", Path: "/x", Body: `{"id": "{{uuid}}", "n": {{faker.int 5 5}}}`}, loadtest.NewIterState())
	data, _ := io.ReadAll(got.Request.Body)

	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil || len(body["id"].(string)) < 16 || body["n"] != float64(5) {
		t.Errorf("faker body: %s %v", data, err)
	}

	schema := build(t, b, &domain.Step{Operation: "createPet", Body: `{"wrap": {{faker.fromSchema}}}`}, loadtest.NewIterState())
	data, _ = io.ReadAll(schema.Request.Body)

	if string(data) != `{"wrap": {"age":3,"name":"rex"}}` {
		t.Errorf("fromSchema body: %s", data)
	}

	_, err := b.Build(context.Background(), "sc", &domain.Step{Method: "POST", Path: "/x", Body: `{{faker.fromSchema}}`}, loadtest.NewIterState())
	if !errors.Is(err, loadtest.ErrNoSchemaBody) {
		t.Errorf("fromSchema without an operation: %v", err)
	}
}

func TestBuild_Errors(t *testing.T) {
	t.Parallel()

	b := newTestBuilder(t, nil)
	ctx := context.Background()

	if _, err := b.Build(ctx, "sc", &domain.Step{Operation: "nope"}, loadtest.NewIterState()); !errors.Is(err, loadtest.ErrUnknownOperation) {
		t.Errorf("unknown operation: %v", err)
	}

	if _, err := b.Build(ctx, "sc", &domain.Step{Method: "GET", Path: "/{{data.ghosts.id}}"}, loadtest.NewIterState()); !errors.Is(err, loadtest.ErrNoFeeder) {
		t.Errorf("unknown feeder: %v", err)
	}

	// The unique feeder has one row: the second iteration cannot get one.
	step := &domain.Step{Method: "GET", Path: "/{{data.users.email}}"}
	if _, err := b.Build(ctx, "sc", step, loadtest.NewIterState()); err != nil {
		t.Fatal(err)
	}

	if _, err := b.Build(ctx, "sc", step, loadtest.NewIterState()); !errors.Is(err, loadtest.ErrFeederExhausted) {
		t.Errorf("exhausted feeder: %v", err)
	}

	if _, err := b.Build(ctx, "sc", &domain.Step{Method: "GET", Path: "/x?a=%zz"}, loadtest.NewIterState()); !errors.Is(err, loadtest.ErrBadRequest) {
		t.Errorf("bad query: %v", err)
	}

	if _, err := b.Build(ctx, "sc", &domain.Step{Method: "BAD METHOD", Path: "/x"}, loadtest.NewIterState()); !errors.Is(err, loadtest.ErrBadRequest) {
		t.Errorf("bad method: %v", err)
	}
}

func TestBuild_Auth(t *testing.T) {
	t.Parallel()

	secrets := func(ref string) string { return map[string]string{"TOK": "s3cret", "BASIC": "u:p"}[ref] }

	cases := []struct {
		auth   domain.AuthRef
		header string
		want   string
	}{
		{domain.AuthRef{Type: domain.AuthBearer, Ref: "TOK"}, "Authorization", "Bearer s3cret"},
		{domain.AuthRef{Type: domain.AuthBasic, Ref: "BASIC"}, "Authorization", "Basic dTpw"},
		{domain.AuthRef{Type: domain.AuthHeader, Ref: "TOK", Header: "X-Api-Key"}, "X-Api-Key", "s3cret"},
	}

	for _, tc := range cases {
		b := newTestBuilder(t, func(c *loadtest.BuilderConfig) { c.Auth = &tc.auth; c.Secrets = secrets })

		got := build(t, b, &domain.Step{Method: "GET", Path: "/x"}, loadtest.NewIterState())
		if got.Request.Header.Get(tc.header) != tc.want {
			t.Errorf("%s: %q", tc.auth.Type, got.Request.Header.Get(tc.header))
		}

		own := build(t, b, &domain.Step{Method: "GET", Path: "/x", Headers: map[string]string{tc.header: "mine"}}, loadtest.NewIterState())
		if own.Request.Header.Get(tc.header) != "mine" {
			t.Errorf("%s: a header the step sets itself wins", tc.auth.Type)
		}
	}

	base, _ := url.Parse("http://h")
	_, err := loadtest.NewBuilder(&loadtest.BuilderConfig{
		Target: base, Engine: usecase.NewTemplateEngine(),
		Auth: &domain.AuthRef{Type: domain.AuthBearer, Ref: "MISSING"}, Secrets: secrets,
	})

	if !errors.Is(err, loadtest.ErrNoSecret) || !strings.Contains(err.Error(), "MISSING") {
		t.Errorf("missing secret: %v", err)
	}
}
