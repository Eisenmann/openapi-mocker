package graphql_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	graphql "github.com/Eisenmann/openapi-mocker/internal/adapter/graphql"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const testSchema = `
type Query {
  ping: String!
  users(limit: Int = 10): [User!]!
  user(id: ID!): User
  search(term: String): SearchResult
  node(id: ID!): Node
}
type Mutation { createUser(input: CreateUserInput!): User! }
type User { id: ID! name: String! email: String! age: Int }
type Admin implements Node { id: ID! name: String! level: Int }
input CreateUserInput { name: String! email: String! age: Int }
interface Node { id: ID! }
union SearchResult = User | Admin
`

// TestValidate covers basic schema validation including the fix for #5:
// the gqlparser Prelude makes built-in scalars implicitly available instead
// of a hand-rolled prologue, so re-declaring a built-in must now be valid.
func TestValidate(t *testing.T) {
	t.Parallel()

	e := graphql.NewEngine()

	res := e.Validate([]byte(testSchema))
	if !res.Valid {
		t.Fatalf("expected valid schema, got errors: %v", res.Errors)
	}

	if res.TypeCount == 0 {
		t.Errorf("expected some types, got %d", res.TypeCount)
	}
	// Query fields: ping, users, user, search, node = 5.
	if res.QueryFields != 5 {
		t.Errorf("expected 5 query fields, got %d", res.QueryFields)
	}

	if res.MutationFields != 1 {
		t.Errorf("expected 1 mutation field, got %d", res.MutationFields)
	}

	bad := e.Validate([]byte(`type Query { a: MissingType }`))
	if bad.Valid {
		t.Errorf("expected invalid schema to fail validation")
	}

	if len(bad.Errors) == 0 {
		t.Errorf("expected error messages for invalid schema")
	}
}

// TestValidateBuiltinRedeclare verifies that a schema declaring `scalar String`
// fails with a correct error location (line 1, col 8) — not shifted by the
// old hand-rolled prologue's 7 lines.
func TestValidateBuiltinRedeclare(t *testing.T) {
	t.Parallel()

	e := graphql.NewEngine()
	schema := `scalar String

type Query { ping: String! }
`

	res := e.Validate([]byte(schema))
	if res.Valid {
		t.Fatalf("schema redeclaring scalar String must fail validation")
	}

	if len(res.Errors) == 0 {
		t.Fatal("expected an error message")
	}
	// The error location must point at line 1, col 8 (the redeclared scalar),
	// not at line 8+ as the old prologue would have caused.
	if !strings.Contains(res.Errors[0], "line 1, col 8") {
		t.Errorf("expected error at line 1, col 8, got: %q", res.Errors[0])
	}
}

// TestValidateMultipleErrors ensures schema errors are surfaced with
// correct source locations (not shifted by a prologue).
func TestValidateMultipleErrors(t *testing.T) {
	t.Parallel()

	e := graphql.NewEngine()
	schema := `type Query {
  a: MissingTypeA
}
`

	res := e.Validate([]byte(schema))
	if res.Valid {
		t.Fatalf("expected schema with undefined type to fail validation")
	}

	if len(res.Errors) == 0 {
		t.Fatal("expected at least one error message")
	}
	// Error location must point at line 2, col 6 (the field with the
	// undefined type), not shifted by a prologue.
	if !strings.Contains(res.Errors[0], "line 2, col 6") {
		t.Errorf("expected error at line 2, col 6, got: %q", res.Errors[0])
	}
}

func TestIntrospection(t *testing.T) {
	t.Parallel()

	e := graphql.NewEngine()

	sdl, err := e.Introspection([]byte(testSchema))
	if err != nil {
		t.Fatalf("introspection failed: %v", err)
	}

	for _, want := range []string{"type Query", "users", "union SearchResult", "interface Node"} {
		if !strings.Contains(sdl, want) {
			t.Errorf("introspected SDL missing %q: %s", want, sdl)
		}
	}
	// The prelude scalars shouldn't leak into the exported SDL.
	for _, noise := range []string{"scalar Boolean", "scalar Float", "scalar ID", "scalar Int", "scalar String"} {
		if strings.Contains(sdl, noise) {
			t.Errorf("SDL export must not include builtin scalar noise: %q", noise)
		}
	}
}

func TestListOperations(t *testing.T) {
	t.Parallel()

	e := graphql.NewEngine()

	ops, err := e.ListOperations([]byte(testSchema))
	if err != nil {
		t.Fatalf("ListOperations failed: %v", err)
	}
	// 5 query + 1 mutation (+0 subscription) = 6 operations.
	if len(ops) != 6 {
		t.Fatalf("expected 6 operations, got %d", len(ops))
	}

	types := map[string]int{}
	for _, o := range ops {
		types[o.Type]++
	}

	if types["query"] != 5 || types["mutation"] != 1 {
		t.Errorf("unexpected op counts: %v", types)
	}
}

func TestExecute(t *testing.T) {
	t.Parallel()

	t.Run("simple query", testExecuteSimpleQuery)
	t.Run("query with variables and alias", testExecuteWithVarsAndAlias)
	t.Run("validation error for unknown field", testExecuteValidationError)
}

// testExecuteSimpleQuery verifies a basic scalar query returns a mock value.
func testExecuteSimpleQuery(t *testing.T) {
	t.Parallel()

	e := graphql.NewEngine()

	body := executeQuery(t, e, "{ ping }", "", nil)

	data := parseData(t, body)
	if data["ping"] != "example" {
		t.Errorf("expected ping=example, got %v", data["ping"])
	}
}

// testExecuteWithVarsAndAlias verifies a query with variables and an alias.
func testExecuteWithVarsAndAlias(t *testing.T) {
	t.Parallel()

	e := graphql.NewEngine()
	query := `query($id: ID!) { u: user(id: $id) { name email } }`

	body := executeQuery(t, e, query, "",
		map[string]interface{}{"id": "42"})

	data := parseData(t, body)

	u, _ := data["u"].(map[string]interface{})
	if u == nil {
		t.Fatalf("expected aliased user object, got %v", data)
	}

	if u["name"] == nil || u["email"] == nil {
		t.Errorf("expected name+email in user, got %v", u)
	}
}

// testExecuteValidationError verifies an unknown field returns a
// *GraphQLQueryError.
func testExecuteValidationError(t *testing.T) {
	t.Parallel()

	e := graphql.NewEngine()

	_, err := e.Execute([]byte(testSchema), "{ unknownField }", "", nil)
	if err == nil {
		t.Fatalf("expected validation error for unknown field")
	}

	qe := &usecase.GraphQLQueryError{}

	ok := errors.As(err, &qe)
	if !ok {
		t.Errorf("expected GraphQLQueryError, got %T: %v", err, err)
	} else if len(qe.Errors) == 0 {
		t.Errorf("expected at least one error detail")
	}
}

// executeQuery runs a query against the given engine and fails the test on
// error.
func executeQuery(t *testing.T, e *graphql.Engine, query, opName string,
	vars map[string]interface{},
) []byte {
	t.Helper()

	body, err := e.Execute([]byte(testSchema), query, opName, vars)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	return body
}

// parseData unmarshals a response body and returns its "data" object.
func parseData(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()

	var parsed map[string]interface{}

	err := json.Unmarshal(body, &parsed)
	if err != nil {
		t.Fatalf("bad json: %v (body=%s)", err, body)
	}

	data, _ := parsed["data"].(map[string]interface{})
	if data == nil {
		t.Fatalf("response has no data object: %s", body)
	}

	return data
}

func TestExecuteFragmentSpreadList(t *testing.T) {
	t.Parallel()

	// Verifies #1: fragment spreads on list fields return arrays,
	// not single objects.
	e := graphql.NewEngine()
	schema := `type Query { users: [User!]! }
type User { name: String! }
`
	query := `query { ...Q }
fragment Q on Query { users { name } }`

	body, err := e.Execute([]byte(schema), query, "", nil)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	var parsed map[string]interface{}

	err = json.Unmarshal(body, &parsed)
	if err != nil {
		t.Fatalf("bad json: %v (body=%s)", err, body)
	}

	data := parsed["data"].(map[string]interface{})

	users, ok := data["users"].([]interface{})
	if !ok {
		t.Fatalf("expected users to be a list, got %T: %v",
			data["users"], data["users"])
	}

	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d", len(users))
	}

	for i, u := range users {
		m, ok := u.(map[string]interface{})
		if !ok {
			t.Fatalf("user %d is not an object: %T", i, u)
		}

		if m["name"] != "example" {
			t.Errorf("user %d: expected name=example, got %v", i, m["name"])
		}
	}
}

func TestExecuteFragmentTypeCondition(t *testing.T) {
	t.Parallel()

	// Verifies #2: `... on User` inside a union/interface resolves
	// against the concrete type, not the parent.
	e := graphql.NewEngine()

	// Union case.
	body, err := e.Execute([]byte(testSchema),
		`{ search(term: "x") { ... on User { name } } }`, "", nil)
	if err != nil {
		t.Fatalf("execute union fragment failed: %v", err)
	}

	var parsed map[string]interface{}

	err = json.Unmarshal(body, &parsed)
	if err != nil {
		t.Fatalf("bad json: %v (body=%s)", err, body)
	}

	data := parsed["data"].(map[string]interface{})

	search, _ := data["search"].(map[string]interface{})
	if search == nil {
		t.Fatalf("expected search object, got %v", data)
	}

	if search["name"] == nil {
		t.Errorf("expected name from ... on User, got %v", search)
	}

	// Interface case.
	body, err = e.Execute([]byte(testSchema),
		`{ node(id: "1") { ... on Admin { level } } }`, "", nil)
	if err != nil {
		t.Fatalf("execute interface fragment failed: %v", err)
	}

	err = json.Unmarshal(body, &parsed)
	if err != nil {
		t.Fatalf("bad json: %v (body=%s)", err, body)
	}

	data = parsed["data"].(map[string]interface{})

	node, _ := data["node"].(map[string]interface{})
	if node == nil {
		t.Fatalf("expected node object, got %v", data)
	}

	if node["level"] == nil {
		t.Errorf("expected level from ... on Admin, got %v", node)
	}
}

func TestExecuteTypeName(t *testing.T) {
	t.Parallel()

	// Verifies #3: __typename resolves to the current type name.
	e := graphql.NewEngine()

	body, err := e.Execute([]byte(testSchema),
		`{ __typename user(id: "1") { __typename name } }`, "", nil)
	if err != nil {
		t.Fatalf("execute with __typename failed: %v", err)
	}

	var parsed map[string]interface{}

	err = json.Unmarshal(body, &parsed)
	if err != nil {
		t.Fatalf("bad json: %v (body=%s)", err, body)
	}

	data := parsed["data"].(map[string]interface{})
	if data["__typename"] != "Query" {
		t.Errorf("expected data.__typename=Query, got %v", data["__typename"])
	}

	u, _ := data["user"].(map[string]interface{})
	if u == nil || u["__typename"] != "User" {
		t.Errorf("expected user.__typename=User, got %v", u)
	}
}

func TestExecuteLimitArg(t *testing.T) {
	t.Parallel()

	// Verifies #4: the limit argument sizes list responses, including
	// default values and variables.
	e := graphql.NewEngine()

	// Default limit=10 → 10 items.
	body, err := e.Execute([]byte(testSchema), `{ users { name } }`, "", nil)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	assertUsersLen(t, body, 10, "default limit")

	// Explicit literal limit=3.
	body, err = e.Execute([]byte(testSchema), `{ users(limit: 3) { name } }`, "", nil)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	assertUsersLen(t, body, 3, "literal limit=3")

	// Variable limit.
	body, err = e.Execute([]byte(testSchema),
		`query($n: Int) { users(limit: $n) { name } }`, "",
		map[string]interface{}{"n": 5})
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	assertUsersLen(t, body, 5, "variable limit=5")

	// Variable limit as float64 (JSON numbers).
	body, err = e.Execute([]byte(testSchema),
		`query($n: Int) { users(limit: $n) { name } }`, "",
		map[string]interface{}{"n": float64(4)})
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	assertUsersLen(t, body, 4, "float variable limit=4")
}

func assertUsersLen(t *testing.T, body []byte, want int, label string) {
	t.Helper()

	var parsed map[string]interface{}

	err := json.Unmarshal(body, &parsed)
	if err != nil {
		t.Fatalf("%s: bad json: %v (body=%s)", label, err, body)
	}

	data := parsed["data"].(map[string]interface{})

	users, ok := data["users"].([]interface{})
	if !ok {
		t.Fatalf("%s: expected users list, got %T: %v", label, data["users"], data["users"])
	}

	if len(users) != want {
		t.Errorf("%s: expected %d users, got %d", label, want, len(users))
	}
}

func TestExecuteDuplicateResponseKeys(t *testing.T) {
	t.Parallel()

	// Verifies #7: duplicate response keys are merged, not dropped.
	// Two fragments selecting different subfields of the same field
	// must merge their sub-selections (client-side fragment composition).
	e := graphql.NewEngine()
	query := `query { user(id: "1") { ...A ...B } }
fragment A on User { name }
fragment B on User { email }`

	body, err := e.Execute([]byte(testSchema), query, "", nil)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	var parsed map[string]interface{}

	err = json.Unmarshal(body, &parsed)
	if err != nil {
		t.Fatalf("bad json: %v (body=%s)", err, body)
	}

	data := parsed["data"].(map[string]interface{})

	u, _ := data["user"].(map[string]interface{})
	if u == nil {
		t.Fatalf("expected merged user object, got %v", data)
	}

	if u["name"] == nil {
		t.Errorf("expected name from fragment A, got %v", u)
	}

	if u["email"] == nil {
		t.Errorf("expected email from fragment B, got %v", u)
	}
}

func TestExecuteMutation(t *testing.T) {
	t.Parallel()

	// Verifies mutation execution with input object argument.
	e := graphql.NewEngine()
	query := `mutation { createUser(input: {name: "Ada", email: "ada@example.com"}) { id name email } }`

	body, err := e.Execute([]byte(testSchema), query, "", nil)
	if err != nil {
		t.Fatalf("execute mutation failed: %v", err)
	}

	var parsed map[string]interface{}

	err = json.Unmarshal(body, &parsed)
	if err != nil {
		t.Fatalf("bad json: %v (body=%s)", err, body)
	}

	data := parsed["data"].(map[string]interface{})

	u, _ := data["createUser"].(map[string]interface{})
	if u == nil {
		t.Fatalf("expected createUser object, got %v", data)
	}

	for _, key := range []string{"id", "name", "email"} {
		if u[key] == nil {
			t.Errorf("expected %s in created user, got %v", key, u)
		}
	}
}

func TestExecuteMultipleOperationSelection(t *testing.T) {
	t.Parallel()

	e := graphql.NewEngine()

	doc := `query A { ping } query B { users { name } }`

	_, err := e.Execute([]byte(testSchema), doc, "", nil)
	if err == nil {
		t.Fatalf("expected error when operation name required for multiple ops")
	}

	_, err = e.Execute([]byte(testSchema), doc, "B", nil)
	if err != nil {
		t.Fatalf("expected operation B to execute, got: %v", err)
	}

	_, err = e.Execute([]byte(testSchema), doc, "Nope", nil)
	if err == nil {
		t.Fatalf("expected error for unknown operation name")
	}
}

func TestExecuteQueryErrorLocations(t *testing.T) {
	t.Parallel()

	// Verifies spec-compliant error locations in the errors array.
	e := graphql.NewEngine()
	_, err := e.Execute([]byte(testSchema), "{ unknownField }", "", nil)

	var qe *usecase.GraphQLQueryError
	if !errors.As(err, &qe) {
		t.Fatalf("expected GraphQLQueryError, got %T: %v", err, err)
	}

	if len(qe.Errors) == 0 {
		t.Fatal("expected at least one error entry")
	}

	for _, ge := range qe.Errors {
		if ge.Message == "" {
			t.Errorf("error message must not be empty")
		}

		if len(ge.Locations) == 0 {
			t.Errorf("expected locations, got empty for %q", ge.Message)
		} else {
			loc := ge.Locations[0]
			if loc.Line == 0 || loc.Column == 0 {
				t.Errorf("expected line+col, got line=%d col=%d",
					loc.Line, loc.Column)
			}
		}
	}
}

// TestExecuteIntrospectionRejected verifies that introspection queries
// (__schema/__type) are rejected with a clear error instead of returning
// mock garbage that would silently break GraphiQL/Playground/codegen tools.
func TestExecuteIntrospectionRejected(t *testing.T) {
	t.Parallel()

	e := graphql.NewEngine()

	// Direct __schema query.
	_, err := e.Execute([]byte(testSchema), `{ __schema { types { name } } }`, "", nil)
	if err == nil {
		t.Fatal("expected introspection query to be rejected")
	}

	var qe *usecase.GraphQLQueryError
	if !errors.As(err, &qe) {
		t.Fatalf("expected GraphQLQueryError, got %T: %v", err, err)
	}

	if len(qe.Errors) == 0 || !strings.Contains(qe.Errors[0].Message, "introspection") {
		t.Errorf("expected introspection error message, got: %v", qe.Errors)
	}

	// __type query.
	_, err = e.Execute([]byte(testSchema), `{ __type(name: "User") { name } }`, "", nil)
	if err == nil {
		t.Error("expected __type query to be rejected")
	}

	// Introspection field nested inside a fragment.
	query := `query { ...Intro }
fragment Intro on Query { __schema { types { name } } }`

	_, err = e.Execute([]byte(testSchema), query, "", nil)
	if err == nil {
		t.Error("expected introspection via fragment to be rejected")
	}
}

// TestExecuteLimitClamped verifies that limit values above the cap are
// clamped rather than silently ignored.
func TestExecuteCyclicFragments(t *testing.T) {
	t.Parallel()

	e := graphql.NewEngine()

	query := "fragment A on User { id ...B }\nfragment B on User { name ...A }\n{ user(id: \"1\") { ...A } }"

	_, err := e.Execute([]byte(testSchema), query, "", nil)
	if err == nil {
		t.Fatal("expected cyclic fragments to be rejected")
	}

	var qe *usecase.GraphQLQueryError
	if !errors.As(err, &qe) {
		t.Fatalf("expected GraphQLQueryError, got %T: %v", err, err)
	}

	if len(qe.Errors) == 0 {
		t.Error("expected at least one error entry")
	}
}

func TestExecuteSelfReferentialFragment(t *testing.T) {
	t.Parallel()

	e := graphql.NewEngine()

	query := "fragment Loop on User { id ...Loop }\n{ user(id: \"1\") { ...Loop } }"

	_, err := e.Execute([]byte(testSchema), query, "", nil)
	if err == nil {
		t.Fatal("expected self-referential fragment to be rejected")
	}

	var qe *usecase.GraphQLQueryError
	if !errors.As(err, &qe) {
		t.Fatalf("expected GraphQLQueryError, got %T: %v", err, err)
	}
}

func TestExecuteMaxDepthRejected(t *testing.T) {
	t.Parallel()

	e := graphql.NewEngine()

	// Use a recursive schema (type Node { child: Node }) to build a
	// deeply-nested query that passes structural validation but exceeds
	// the custom maxQueryDepth guard.
	recursiveSchema := `
type Query { root: Node! }
type Node { name: String! child: Node }
`

	deepQuery := "{ root"
	for i := 0; i < 200; i++ {
		deepQuery += " { child"
	}
	deepQuery += " { name"
	deepQuery += strings.Repeat("}", 202)

	_, err := e.Execute([]byte(recursiveSchema), deepQuery, "", nil)
	if err == nil {
		t.Fatal("expected overly-deep query to be rejected")
	}

	var qe *usecase.GraphQLQueryError
	if !errors.As(err, &qe) {
		t.Fatalf("expected GraphQLQueryError, got %T: %v", err, err)
	}

	if len(qe.Errors) != 1 || !strings.Contains(qe.Errors[0].Message, "maximum depth") {
		t.Errorf("expected max-depth error, got: %v", qe.Errors)
	}
}

func TestExecuteLimitClamped(t *testing.T) {
	t.Parallel()

	e := graphql.NewEngine()

	// limit=100 → clamped to 20.
	body, err := e.Execute([]byte(testSchema), `{ users(limit: 100) { name } }`, "", nil)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	assertUsersLen(t, body, 20, "clamped limit=100")

	// limit=0 → default 2 (non-positive values are ignored).
	body, err = e.Execute([]byte(testSchema), `{ users(limit: 0) { name } }`, "", nil)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	assertUsersLen(t, body, 2, "limit=0 falls back to default")

	// limit=-5 → default 2.
	body, err = e.Execute([]byte(testSchema), `{ users(limit: -5) { name } }`, "", nil)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	assertUsersLen(t, body, 2, "limit=-5 falls back to default")
}
