package usecase_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func tctx(jsonDoc bool, body string) *usecase.TemplateContext {
	return &usecase.TemplateContext{
		ProjectID: "p1", Method: "POST", Path: "/users/7",
		PathParams: map[string]string{"id": "7"},
		Query:      map[string][]string{"page": {"2"}},
		Header:     map[string][]string{"X-Trace": {"abc"}},
		Body:       []byte(body), JSON: jsonDoc,
	}
}

func TestTemplate_RequestFields(t *testing.T) {
	t.Parallel()

	eng := usecase.NewTemplateEngine()
	ctx := tctx(true, `{"user":{"name":"Ann","tags":["a","b"]},"age":30}`)

	got := eng.Render(`{"id":{{path.id}},"name":"{{request.body.user.name}}","tag":"{{request.body.user.tags[1]}}",`+
		`"age":{{request.body.age}},"page":"{{request.query.page}}","trace":"{{request.header.x-trace}}",`+
		`"m":"{{request.method}}","p":"{{request.path}}"}`, ctx)

	want := `{"id":7,"name":"Ann","tag":"b","age":30,"page":"2","trace":"abc","m":"POST","p":"/users/7"}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}

	if !json.Valid([]byte(got)) {
		t.Errorf("result is not valid JSON: %s", got)
	}
}

func TestTemplate_JSONEscapingAndMissing(t *testing.T) {
	t.Parallel()

	eng := usecase.NewTemplateEngine()
	ctx := tctx(true, `{"name":"say \"hi\"\n<b>","nested":{"a":1}}`)

	got := eng.Render(`{"in":"{{request.body.name}}","out":{{request.body.name}},"obj":{{request.body.nested}},`+
		`"gone":{{request.body.missing}},"s":"{{request.body.missing}}"}`, ctx)

	if !json.Valid([]byte(got)) {
		t.Fatalf("not valid JSON: %s", got)
	}

	var v map[string]any
	_ = json.Unmarshal([]byte(got), &v)

	if v["in"] != "say \"hi\"\n<b>" || v["out"] != "say \"hi\"\n<b>" {
		t.Errorf("strings must round-trip, got %#v", v)
	}

	if v["gone"] != nil || v["s"] != "" {
		t.Errorf("missing field: null outside a string, empty inside, got %#v", v)
	}

	if obj, ok := v["obj"].(map[string]any); !ok || obj["a"] != float64(1) {
		t.Errorf("objects are inserted as JSON, got %#v", v["obj"])
	}
}

func TestTemplate_PlainTextAndUnknownTags(t *testing.T) {
	t.Parallel()

	eng := usecase.NewTemplateEngine()
	ctx := tctx(false, `{"n":"Ann"}`)

	got := eng.Render(`Hello {{request.body.n}}! {{unknown}} {{faker.nope}} {{faker.int x}} {{now +zz}} {{ }} {{open`, ctx)
	want := `Hello Ann! {{unknown}} {{faker.nope}} {{faker.int x}} {{now +zz}} {{ }} {{open`

	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}

	if eng.Render("no tags", ctx) != "no tags" {
		t.Error("text without tags must be unchanged")
	}
}

func TestTemplate_CountersArePerProjectAndName(t *testing.T) {
	t.Parallel()

	eng := usecase.NewTemplateEngine()
	a, b := tctx(false, ""), tctx(false, "")
	b.ProjectID = "p2"

	got := []string{
		eng.Render("{{counter}}", a), eng.Render("{{counter}}", a), eng.Render("{{counter orders}}", a),
		eng.Render("{{counter}}", b), eng.Render("{{counter}}", a),
	}

	if strings.Join(got, ",") != "1,2,1,1,3" {
		t.Errorf("counters: %v", got)
	}
}

func TestTemplate_GeneratedValues(t *testing.T) {
	t.Parallel()

	eng := usecase.NewTemplateEngine()
	ctx := tctx(false, "")

	uuidRe := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if u := eng.Render("{{uuid}}", ctx); !uuidRe.MatchString(u) {
		t.Errorf("bad uuid %q", u)
	}

	for i := 0; i < 50; i++ {
		n := eng.Render("{{faker.int 5 7}}", ctx)
		if n != "5" && n != "6" && n != "7" {
			t.Fatalf("faker.int out of range: %q", n)
		}
	}

	if !regexp.MustCompile(`^\S+@example\.com$`).MatchString(eng.Render("{{faker.email}}", ctx)) {
		t.Error("bad faker.email")
	}

	if v := eng.Render("{{faker.pick red green}}", ctx); v != "red" && v != "green" {
		t.Errorf("faker.pick: %q", v)
	}

	if v := eng.Render("{{faker.bool}}", ctx); v != "true" && v != "false" {
		t.Errorf("faker.bool: %q", v)
	}

	if s := eng.Render("{{faker.sentence 4}}", ctx); len(strings.Fields(s)) != 4 || !strings.HasSuffix(s, ".") {
		t.Errorf("faker.sentence: %q", s)
	}

	for _, tag := range []string{"name", "firstName", "lastName", "username", "phone", "city", "country", "company", "word", "float"} {
		if eng.Render("{{faker."+tag+"}}", ctx) == "" {
			t.Errorf("faker.%s rendered empty", tag)
		}
	}
}

func TestTemplate_Time(t *testing.T) {
	t.Parallel()

	eng := usecase.NewTemplateEngine()
	ctx := tctx(false, "")

	now, err := time.Parse(time.RFC3339, eng.Render("{{now}}", ctx))
	if err != nil || time.Since(now) > time.Minute {
		t.Fatalf("{{now}}: %v %v", now, err)
	}

	later, _ := time.Parse(time.RFC3339, eng.Render("{{now +2h}}", ctx))
	if d := later.Sub(now); d < 119*time.Minute || d > 121*time.Minute {
		t.Errorf("+2h shifted by %v", d)
	}

	earlier, _ := time.Parse(time.RFC3339, eng.Render("{{now -3d}}", ctx))
	if d := now.Sub(earlier); d < 71*time.Hour || d > 73*time.Hour {
		t.Errorf("-3d shifted by %v", d)
	}

	if !regexp.MustCompile(`^\d{10}$`).MatchString(eng.Render("{{timestamp}}", ctx)) ||
		!regexp.MustCompile(`^\d{10}$`).MatchString(eng.Render("{{now.unix}}", ctx)) {
		t.Error("unix timestamps must be 10 digits")
	}

	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(eng.Render("{{date}}", ctx)) {
		t.Error("{{date}} must be YYYY-MM-DD")
	}
}

func TestTemplate_RenderValueKeepsTypes(t *testing.T) {
	t.Parallel()

	eng := usecase.NewTemplateEngine()
	ctx := tctx(false, `{"n":5,"s":"x"}`)

	var in any

	_ = json.Unmarshal([]byte(`{"a":"{{request.body.n}}","b":"hi {{request.body.s}}","c":["{{counter}}","{{nope}}"],"d":1}`), &in)

	out, _ := json.Marshal(eng.RenderValue(in, ctx))
	if string(out) != `{"a":5,"b":"hi x","c":[1,"{{nope}}"],"d":1}` {
		t.Errorf("got %s", out)
	}
}

func TestServeRequest_RuleBodyIsTemplated(t *testing.T) {
	t.Parallel()

	projects := newMockProjectRepo()
	p := projects.Create("x", "")
	contracts := newMockContractRepo()
	contracts.AddVersion(p.ID, "yaml", "raw", "manual")

	mocks := newMockMockRepo()
	mocks.CreateMock(&domain.MockRule{
		ProjectID: p.ID, Path: "/users/{id}", Method: "POST", StatusCode: 201,
		Headers: map[string]string{"Location": "/users/{{counter users}}"},
		Body:    `{"id":{{path.id}},"name":"{{request.body.name}}","seq":{{counter}}}`,
	})

	engine := &mockContractEngine{found: true, pathTemplate: "/users/{id}"}
	svc := usecase.NewMockServingService(contracts, mocks, newMockLogRepo(), engine)

	req := &usecase.MockRequest{Method: "POST", Path: "/users/42", Body: []byte(`{"name":"Ann"}`), SkipValidation: true}

	first := svc.ServeRequest(p.ID, req)
	if string(first.Body) != `{"id":42,"name":"Ann","seq":1}` {
		t.Errorf("body: %s", first.Body)
	}

	if first.Headers["Location"] != "/users/1" {
		t.Errorf("header not rendered: %v", first.Headers)
	}

	if second := svc.ServeRequest(p.ID, req); !strings.Contains(string(second.Body), `"seq":2`) {
		t.Errorf("counter must advance: %s", second.Body)
	}

	if mocks.ListMocks(p.ID)[0].Body == "" || strings.Contains(mocks.ListMocks(p.ID)[0].Body, "Ann") {
		t.Error("the stored rule must keep its placeholders")
	}
}

func TestMockService_AcceptsPlaceholdersInBody(t *testing.T) {
	t.Parallel()

	svc := usecase.NewMockService(newMockMockRepo(), newMockContractRepo(), newMockProviderRepo(),
		&mockContractEngine{}, &mockLLMGateway{})

	ok := &domain.MockRule{ProjectID: "p", Path: "/x", Method: "GET", Body: `{"id":{{counter}},"n":"{{faker.name}}","o":{{request.body.o}}}`}
	if _, err := svc.Create(ok); err != nil {
		t.Errorf("placeholders must pass the JSON check: %v", err)
	}

	bad := &domain.MockRule{ProjectID: "p", Path: "/x", Method: "GET", Body: `{"id":{{unknown}}}`}
	if _, err := svc.Create(bad); err == nil {
		t.Error("an unknown placeholder outside a string is invalid JSON")
	}
}
