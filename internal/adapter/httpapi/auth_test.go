package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/exchange"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const authContract = `openapi: 3.0.3
info: {title: Secure, version: "1"}
components:
  securitySchemes:
    key: {type: apiKey, in: header, name: X-Api-Key}
    qkey: {type: apiKey, in: query, name: token}
    bearer: {type: http, scheme: bearer}
    basic: {type: http, scheme: basic}
    oauth:
      type: oauth2
      flows:
        clientCredentials:
          tokenUrl: /oauth/token
          scopes: {read: read things, write: write things}
paths:
  /public:
    get:
      security: []
      responses: {'200': {description: ok, content: {application/json: {schema: {type: object, properties: {ok: {type: boolean}}}}}}}
  /things:
    get:
      security: [{oauth: [read]}]
      responses: {'200': {description: ok, content: {application/json: {schema: {type: object, properties: {ok: {type: boolean}}}}}}}
    post:
      security: [{oauth: [write]}]
      responses: {'200': {description: ok, content: {application/json: {schema: {type: object, properties: {ok: {type: boolean}}}}}}}
  /keyed:
    get:
      security: [{key: []}, {qkey: []}]
      responses: {'200': {description: ok, content: {application/json: {schema: {type: object, properties: {ok: {type: boolean}}}}}}}
  /private:
    get:
      security: [{basic: []}]
      responses: {'200': {description: ok, content: {application/json: {schema: {type: object, properties: {ok: {type: boolean}}}}}}}
  /global:
    get:
      responses: {'200': {description: ok, content: {application/json: {schema: {type: object, properties: {ok: {type: boolean}}}}}}}
`

const authConfig = `{
 "mode":"contract","tokenTtlSeconds":60,
 "credentials":[
  {"name":"good","value":"tok-good"},
  {"name":"reader","value":"tok-reader","scopes":["read"]},
  {"name":"old","value":"tok-old","scenario":"expired"},
  {"name":"gone","value":"tok-gone","scenario":"revoked"},
  {"name":"banned","value":"tok-banned","scenario":"forbidden"},
  {"name":"weak","value":"tok-weak","scenario":"insufficient_scope"},
  {"name":"k","value":"key-1"}
 ],
 "users":[{"username":"ann","password":"pw","scopes":["read"]},{"username":"bob","password":"pw2","scenario":"forbidden"}],
 "clients":[
  {"id":"svc","secret":"s3","scopes":["read","write"]},
  {"id":"limited","secret":"s4","scopes":["read"]},
  {"id":"sleepy","secret":"s5","scenario":"expired"}
 ]}`

type authEnv struct {
	h   http.Handler
	id  string
	now time.Time
}

func newAuthEnv(t *testing.T, config string) *authEnv {
	t.Helper()

	store, err := jsonstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	env := &authEnv{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	engine := openapi.NewEngine()
	projects := usecase.NewProjectService(store)
	auth := usecase.NewAuthService(store, engine, usecase.WithAuthClock(func() time.Time { return env.now }))

	contracts := usecase.NewContractService(store, store, engine, nil).WithConverter(engine)
	mocks := usecase.NewMockService(store, store, store, engine, nil)

	env.h = httpapi.NewRouter(&httpapi.Services{
		Projects:  projects,
		Contracts: contracts,
		Mocks:     mocks,
		Exchange:  usecase.NewExchangeService(projects, store, contracts, mocks, exchange.New()),
		MockServing: usecase.NewMockServingService(
			store, store, store, engine, usecase.WithProjects(store), usecase.WithAuth(auth)),
		Logs: usecase.NewLogService(store),
	})

	p, err := projects.Create("secure", "")
	if err != nil {
		t.Fatal(err)
	}

	env.id = p.ID
	store.AddVersion(p.ID, "yaml", authContract, "test")

	if config != "" {
		if rec := call(t, env.h, http.MethodPut, "/api/projects/"+p.ID+"/auth", config); rec.Code != http.StatusOK {
			t.Fatalf("set auth: %d %s", rec.Code, rec.Body.String())
		}
	}

	return env
}

func (e *authEnv) get(t *testing.T, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	return doRequest(t, e.h, http.MethodGet, "/mock/"+e.id+path, nil, headers)
}

func (e *authEnv) token(t *testing.T, form url.Values, headers map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()

	h := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	for k, v := range headers {
		h[k] = v
	}

	rec := doRequest(t, e.h, http.MethodPost, "/mock/"+e.id+"/oauth/token", strings.NewReader(form.Encode()), h)

	var out map[string]any

	decodeJSON(t, rec, &out)

	return rec, out
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

func TestAuth_OffByDefault(t *testing.T) {
	t.Parallel()

	env := newAuthEnv(t, "")

	if rec := env.get(t, "/things", nil); rec.Code != http.StatusOK {
		t.Fatalf("auth is off: want 200, got %d", rec.Code)
	}
}

func TestAuth_PublicAndMissingCredentials(t *testing.T) {
	t.Parallel()

	env := newAuthEnv(t, authConfig)

	if rec := env.get(t, "/public", nil); rec.Code != http.StatusOK {
		t.Errorf("security [] stays public: %d", rec.Code)
	}

	rec := env.get(t, "/things", nil)
	if rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Bearer") ||
		rec.Header().Get("X-Mock-Source") != "auth" {
		t.Errorf("missing token: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}

	if rec := env.get(t, "/private", nil); rec.Code != http.StatusUnauthorized ||
		!strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Basic") {
		t.Errorf("missing basic: %d %v", rec.Code, rec.Header())
	}

	// An unknown route is still a 404, not a 401.
	if rec := env.get(t, "/nope", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown route: %d", rec.Code)
	}
}

func TestAuth_StaticTokenScenarios(t *testing.T) {
	t.Parallel()

	env := newAuthEnv(t, authConfig)

	for tok, want := range map[string]int{
		"tok-good": http.StatusOK, "tok-reader": http.StatusOK, "tok-old": http.StatusUnauthorized,
		"tok-gone": http.StatusUnauthorized, "tok-banned": http.StatusForbidden,
		"tok-weak": http.StatusForbidden, "tok-unknown": http.StatusUnauthorized,
	} {
		if rec := env.get(t, "/things", bearer(tok)); rec.Code != want {
			t.Errorf("%s: want %d, got %d %s", tok, want, rec.Code, rec.Body.String())
		}
	}

	expired := env.get(t, "/things", bearer("tok-old"))
	if !strings.Contains(expired.Header().Get("WWW-Authenticate"), `error="invalid_token"`) ||
		!strings.Contains(expired.Body.String(), "expired") {
		t.Errorf("expired token response: %v %s", expired.Header(), expired.Body.String())
	}

	weak := env.get(t, "/things", bearer("tok-weak"))
	if !strings.Contains(weak.Header().Get("WWW-Authenticate"), `insufficient_scope", scope="read"`) {
		t.Errorf("insufficient_scope challenge: %v", weak.Header())
	}

	// Scopes: tok-reader may read but not write.
	if rec := doRequest(t, env.h, http.MethodPost, "/mock/"+env.id+"/things", nil, bearer("tok-reader")); rec.Code != http.StatusForbidden {
		t.Errorf("reader writing: want 403, got %d", rec.Code)
	}

	if rec := doRequest(t, env.h, http.MethodPost, "/mock/"+env.id+"/things", nil, bearer("tok-good")); rec.Code != http.StatusOK {
		t.Errorf("unrestricted token writing: want 200, got %d", rec.Code)
	}
}

func TestAuth_APIKeyAlternativesAndBasic(t *testing.T) {
	t.Parallel()

	env := newAuthEnv(t, authConfig)

	if rec := env.get(t, "/keyed", map[string]string{"X-Api-Key": "key-1"}); rec.Code != http.StatusOK {
		t.Errorf("header key: %d", rec.Code)
	}

	if rec := env.get(t, "/keyed?token=key-1", nil); rec.Code != http.StatusOK {
		t.Errorf("query key (second alternative): %d", rec.Code)
	}

	if rec := env.get(t, "/keyed", map[string]string{"X-Api-Key": "wrong"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong key: %d", rec.Code)
	}

	basic := func(u, p string) map[string]string {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.SetBasicAuth(u, p)

		return map[string]string{"Authorization": r.Header.Get("Authorization")}
	}

	for _, tc := range []struct {
		user, pass string
		want       int
	}{{"ann", "pw", 200}, {"ann", "bad", 401}, {"bob", "pw2", 403}, {"zed", "x", 401}} {
		if rec := env.get(t, "/private", basic(tc.user, tc.pass)); rec.Code != tc.want {
			t.Errorf("basic %s/%s: want %d, got %d", tc.user, tc.pass, tc.want, rec.Code)
		}
	}
}

func TestAuth_GlobalSecurityAndModeAll(t *testing.T) {
	t.Parallel()

	env := newAuthEnv(t, `{"mode":"all","credentials":[{"value":"k1"}],"apiKeyHeader":"X-Token"}`)

	for _, path := range []string{"/public", "/global", "/things"} {
		if rec := env.get(t, path, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("mode all, %s without credentials: %d", path, rec.Code)
		}
	}

	if rec := env.get(t, "/public", map[string]string{"X-Token": "k1"}); rec.Code != http.StatusOK {
		t.Errorf("api key header: %d", rec.Code)
	}

	if rec := env.get(t, "/public?api_key=k1", nil); rec.Code != http.StatusOK {
		t.Errorf("api_key query: %d", rec.Code)
	}

	if rec := env.get(t, "/public", bearer("k1")); rec.Code != http.StatusOK {
		t.Errorf("bearer: %d", rec.Code)
	}
}

func TestAuth_ClientCredentialsFlow(t *testing.T) {
	t.Parallel()

	env := newAuthEnv(t, authConfig)

	rec, tok := env.token(t, url.Values{"grant_type": {"client_credentials"}, "scope": {"read"}},
		map[string]string{"Authorization": "Basic " + basicB64("svc", "s3")})
	if rec.Code != http.StatusOK || tok["token_type"] != "Bearer" || tok["scope"] != "read" ||
		tok["expires_in"].(float64) != 60 || tok["refresh_token"] != nil ||
		rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("token response: %d %v %v", rec.Code, tok, rec.Header())
	}

	access, _ := tok["access_token"].(string)

	if rec := env.get(t, "/things", bearer(access)); rec.Code != http.StatusOK {
		t.Errorf("issued token on read: %d", rec.Code)
	}

	if rec := doRequest(t, env.h, http.MethodPost, "/mock/"+env.id+"/things", nil, bearer(access)); rec.Code != http.StatusForbidden {
		t.Errorf("a read-only token must not write: %d", rec.Code)
	}

	// The token expires after its lifetime.
	env.now = env.now.Add(61 * time.Second)

	if rec := env.get(t, "/things", bearer(access)); rec.Code != http.StatusUnauthorized ||
		!strings.Contains(rec.Body.String(), "expired") {
		t.Errorf("expired by time: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAuth_TokenEndpointErrors(t *testing.T) {
	t.Parallel()

	env := newAuthEnv(t, authConfig)
	svc := map[string]string{"Authorization": "Basic " + basicB64("svc", "s3")}

	for name, tc := range map[string]struct {
		form    url.Values
		headers map[string]string
		status  int
		code    string
	}{
		"no grant type":      {url.Values{}, svc, 400, "invalid_request"},
		"unknown grant":      {url.Values{"grant_type": {"magic"}}, svc, 400, "unsupported_grant_type"},
		"wrong secret":       {url.Values{"grant_type": {"client_credentials"}}, map[string]string{"Authorization": "Basic " + basicB64("svc", "nope")}, 401, "invalid_client"},
		"no client":          {url.Values{"grant_type": {"client_credentials"}}, nil, 401, "invalid_client"},
		"scope not allowed":  {url.Values{"grant_type": {"client_credentials"}, "scope": {"admin"}}, svc, 400, "invalid_scope"},
		"bad password":       {url.Values{"grant_type": {"password"}, "username": {"ann"}, "password": {"x"}}, nil, 400, "invalid_grant"},
		"missing password":   {url.Values{"grant_type": {"password"}, "username": {"ann"}}, nil, 400, "invalid_request"},
		"invalid code":       {url.Values{"grant_type": {"authorization_code"}, "code": {"invalid"}}, nil, 400, "invalid_grant"},
		"unknown refresh":    {url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"nope"}}, nil, 400, "invalid_grant"},
		"limited asks write": {url.Values{"grant_type": {"client_credentials"}, "client_id": {"limited"}, "client_secret": {"s4"}, "scope": {"write"}}, nil, 400, "invalid_scope"},
	} {
		rec, out := env.token(t, tc.form, tc.headers)
		if rec.Code != tc.status || out["error"] != tc.code {
			t.Errorf("%s: want %d %s, got %d %v", name, tc.status, tc.code, rec.Code, out)
		}
	}

	if rec := doRequest(t, env.h, http.MethodGet, "/mock/"+env.id+"/oauth/token", nil, nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET on the token endpoint: %d", rec.Code)
	}
}

func TestAuth_PasswordAndRefreshGrants(t *testing.T) {
	t.Parallel()

	env := newAuthEnv(t, authConfig)

	rec, tok := env.token(t, url.Values{"grant_type": {"password"}, "username": {"ann"}, "password": {"pw"}}, nil)
	if rec.Code != http.StatusOK || tok["refresh_token"] == nil || tok["scope"] != "read" {
		t.Fatalf("password grant: %d %v", rec.Code, tok)
	}

	if r := env.get(t, "/things", bearer(tok["access_token"].(string))); r.Code != http.StatusOK {
		t.Errorf("user token: %d", r.Code)
	}

	refresh := tok["refresh_token"].(string)

	rec, next := env.token(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}, nil)
	if rec.Code != http.StatusOK || next["access_token"] == tok["access_token"] {
		t.Fatalf("refresh grant: %d %v", rec.Code, next)
	}

	// A refresh token works once.
	if rec, out := env.token(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}, nil); rec.Code != 400 ||
		out["error"] != "invalid_grant" {
		t.Errorf("reused refresh token: %d %v", rec.Code, out)
	}

	// Authorization code: any code works, with a JSON body too.
	rec = doRequest(t, env.h, http.MethodPost, "/mock/"+env.id+"/oauth/token",
		strings.NewReader(`{"grant_type":"authorization_code","code":"abc"}`), map[string]string{"Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Errorf("authorization_code via JSON: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAuth_ClientScenarioAppliesToIssuedTokens(t *testing.T) {
	t.Parallel()

	env := newAuthEnv(t, authConfig)

	_, tok := env.token(t, url.Values{"grant_type": {"client_credentials"}, "client_id": {"sleepy"}, "client_secret": {"s5"}}, nil)

	rec := env.get(t, "/things", bearer(tok["access_token"].(string)))
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "expired") {
		t.Errorf("a token from the expired-scenario client: %d %s", rec.Code, rec.Body.String())
	}

	// A user with a forbidden scenario gets forbidden tokens.
	_, tok = env.token(t, url.Values{"grant_type": {"password"}, "username": {"bob"}, "password": {"pw2"}}, nil)
	if rec := env.get(t, "/things", bearer(tok["access_token"].(string))); rec.Code != http.StatusForbidden {
		t.Errorf("forbidden user token: %d", rec.Code)
	}
}

func TestAuth_ConfigEndpointValidation(t *testing.T) {
	t.Parallel()

	env := newAuthEnv(t, "")
	url := "/api/projects/" + env.id + "/auth"

	for name, body := range map[string]string{
		"bad mode":      `{"mode":"strict"}`,
		"bad scenario":  `{"mode":"all","credentials":[{"value":"a","scenario":"nope"}]}`,
		"empty value":   `{"mode":"all","credentials":[{"value":" "}]}`,
		"duplicate":     `{"mode":"all","credentials":[{"value":"a"},{"value":"a"}]}`,
		"bad tokenPath": `{"mode":"all","tokenPath":"oauth token"}`,
		"negative ttl":  `{"mode":"all","tokenTtlSeconds":-5}`,
	} {
		if rec := call(t, env.h, http.MethodPut, url, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d %s", name, rec.Code, rec.Body.String())
		}
	}

	rec := call(t, env.h, http.MethodGet, url, "")

	var cfg struct{ Mode string }

	decodeJSON(t, rec, &cfg)

	if rec.Code != 200 || cfg.Mode != "off" {
		t.Errorf("default config: %d %s", rec.Code, rec.Body.String())
	}

	call(t, env.h, http.MethodPut, url, authConfig)

	if rec := call(t, env.h, http.MethodDelete, url, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}

	if rec := env.get(t, "/things", nil); rec.Code != http.StatusOK {
		t.Errorf("after delete auth is off: %d", rec.Code)
	}

	if rec := call(t, env.h, http.MethodGet, "/api/projects/nope/auth", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project: %d", rec.Code)
	}
}

func TestAuth_LoggedAsAuthSource(t *testing.T) {
	t.Parallel()

	env := newAuthEnv(t, authConfig)
	env.get(t, "/things", nil)

	rec := call(t, env.h, http.MethodGet, "/api/projects/"+env.id+"/logs", "")
	if !strings.Contains(rec.Body.String(), `"source":"auth"`) || !strings.Contains(rec.Body.String(), "401") {
		t.Errorf("the rejection must be in the request log: %s", rec.Body.String())
	}
}

func basicB64(u, p string) string {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.SetBasicAuth(u, p)

	return strings.TrimPrefix(r.Header.Get("Authorization"), "Basic ")
}

func TestAuth_TravelsInBundle(t *testing.T) {
	t.Parallel()

	src := newAuthEnv(t, authConfig)
	exp := doRequest(t, src.h, http.MethodGet, "/api/projects/"+src.id+"/export", nil, nil)

	if exp.Code != http.StatusOK || !strings.Contains(exp.Body.String(), `"tok-good"`) {
		t.Fatalf("export: %d %s", exp.Code, exp.Body.String())
	}

	dst := newAuthEnv(t, "")
	rec := doRequest(t, dst.h, http.MethodPost, "/api/import?name=copy", strings.NewReader(exp.Body.String()), nil)

	var res struct{ Project struct{ ID string } }

	decodeJSON(t, rec, &res)

	if rec.Code != http.StatusCreated {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}

	copied := doRequest(t, dst.h, http.MethodGet, "/mock/"+res.Project.ID+"/things", nil, nil)
	if copied.Code != http.StatusUnauthorized {
		t.Errorf("the copy must enforce auth too: %d", copied.Code)
	}
}
