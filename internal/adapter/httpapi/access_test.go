package httpapi_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/httpapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/openapi"
	"github.com/Eisenmann/openapi-mocker/internal/adapter/repository/jsonstore"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const (
	accessSpec = `openapi: 3.0.0
info: {title: T, version: "1"}
paths:
  /ping:
    get:
      responses:
        '200': {description: ok}
`
	adminEnvToken = "env-admin-secret"
	authBody      = `{"mode":"contract","credentials":[{"value":"mock-cred"}]}`
)

type accessEnv struct {
	h     http.Handler
	store *jsonstore.Store
	id    string
}

func newAccessEnv(t *testing.T) *accessEnv {
	t.Helper()

	store, err := jsonstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	engine := openapi.NewEngine()
	projects := usecase.NewProjectService(store)

	h := httpapi.NewRouter(&httpapi.Services{
		Projects:    projects,
		Contracts:   usecase.NewContractService(store, store, engine, nil, usecase.FormatValidators{}),
		Mocks:       usecase.NewMockService(store, store, store, engine, nil),
		Auth:        usecase.NewAuthService(store, engine, usecase.WithAuthLogs(store)),
		MockServing: usecase.NewMockServingService(store, store, store, engine, usecase.WithProjects(store)),
		Access:      usecase.NewAccessService(store, store, store, usecase.WithAdminToken(adminEnvToken)),
	})

	p, err := projects.Create("acc", "")
	if err != nil {
		t.Fatal(err)
	}

	return &accessEnv{h: h, store: store, id: p.ID}
}

func (e *accessEnv) do(t *testing.T, method, path, body, bearer string, extra ...string) *httptest.ResponseRecorder {
	t.Helper()

	headers := map[string]string{"Content-Type": "application/json"}
	if bearer != "" {
		headers["Authorization"] = "Bearer " + bearer
	}

	for i := 0; i+1 < len(extra); i += 2 {
		headers[extra[i]] = extra[i+1]
	}

	return doRequest(t, e.h, method, path, strings.NewReader(body), headers)
}

func (e *accessEnv) url(rest string) string { return "/api/projects/" + e.id + rest }

type created struct {
	Token string `json:"token"`
	Info  struct {
		ID string `json:"id"`
	} `json:"info"`
}

func (e *accessEnv) mkToken(t *testing.T, role, bearer string) created {
	t.Helper()

	rec := e.do(t, http.MethodPost, e.url("/access/tokens"), `{"name":"`+role+`-tok","role":"`+role+`"}`, bearer)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s token: %d %s", role, rec.Code, rec.Body.String())
	}

	var c created

	decodeJSON(t, rec, &c)

	return c
}

func (e *accessEnv) audit(t *testing.T, bearer, query string) []map[string]any {
	t.Helper()

	rec := e.do(t, http.MethodGet, e.url("/audit"+query), "", bearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit: %d %s", rec.Code, rec.Body.String())
	}

	var entries []map[string]any

	decodeJSON(t, rec, &entries)

	return entries
}

func TestAccess_OpenProjectIsAnonymousAndAudited(t *testing.T) {
	t.Parallel()

	e := newAccessEnv(t)

	rec := e.do(t, http.MethodPost, e.url("/contract"), `{"raw":`+jsonString(accessSpec)+`,"source":"manual"}`, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}

	if rec := e.do(t, http.MethodPost, e.url("/mocks"),
		`{"method":"GET","path":"/ping","statusCode":200,"body":"{}"}`, ""); rec.Code != http.StatusCreated {
		t.Fatalf("mock: %d %s", rec.Code, rec.Body.String())
	}

	entries := e.audit(t, "", "")
	if len(entries) != 2 || entries[0]["action"] != "mock.create" || entries[1]["action"] != "contract.publish" {
		t.Fatalf("audit entries: %v", entries)
	}

	actor := entries[1]["actor"].(map[string]any)
	if actor["kind"] != "anonymous" || !strings.Contains(entries[1]["note"].(string), "version 1") {
		t.Errorf("anonymous actor with the version in the note: %v", entries[1])
	}

	// Reads are not audited, and neither are failed or non-changing calls.
	e.do(t, http.MethodGet, e.url("/contract"), "", "")
	e.do(t, http.MethodPost, e.url("/contract/validate"), `{"raw":"x"}`, "")
	e.do(t, http.MethodPost, e.url("/contract"), `{"raw":""}`, "")

	if got := len(e.audit(t, "", "")); got != 2 {
		t.Errorf("only successful changes are audited, have %d entries", got)
	}
}

func TestAccess_FirstTokenLocksTheProject(t *testing.T) {
	t.Parallel()

	e := newAccessEnv(t)

	if rec := e.do(t, http.MethodPost, e.url("/access/tokens"), `{"name":"ed","role":"editor"}`, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("the first token must be an admin token: %d", rec.Code)
	}

	admin := e.mkToken(t, "admin", "")

	if !strings.HasPrefix(admin.Token, "omk_") || len(admin.Token) < 40 {
		t.Errorf("token secret %q", admin.Token)
	}

	rec := e.do(t, http.MethodGet, e.url("/contract"), "", "")
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("locked project without token: %d %v", rec.Code, rec.Header())
	}

	if rec := e.do(t, http.MethodGet, e.url("/contract"), "", "omk_wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", rec.Code)
	}

	// The secret is never listed again; the hash is never shown.
	list := e.do(t, http.MethodGet, e.url("/access/tokens"), "", admin.Token)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), admin.Token) ||
		strings.Contains(list.Body.String(), `"hash"`) || !strings.Contains(list.Body.String(), `"locked":true`) {
		t.Errorf("token list leaks or lies: %d %s", list.Code, list.Body.String())
	}

	// A token of another project does not work here.
	other := newAccessEnv(t)
	otherAdmin := other.mkToken(t, "admin", "")

	if rec := e.do(t, http.MethodGet, e.url("/contract"), "", otherAdmin.Token); rec.Code != http.StatusUnauthorized {
		t.Errorf("foreign token: %d", rec.Code)
	}
}

func TestAccess_Roles(t *testing.T) {
	t.Parallel()

	e := newAccessEnv(t)
	admin := e.mkToken(t, "admin", "")
	editor := e.mkToken(t, "editor", admin.Token)
	viewer := e.mkToken(t, "viewer", admin.Token)

	publish := `{"raw":` + jsonString(accessSpec) + `}`

	cases := []struct {
		name         string
		method, path string
		body         string
		token        string
		want         int
	}{
		{"viewer reads", http.MethodGet, e.url("/mocks"), "", viewer.Token, http.StatusOK},
		{"viewer cannot publish", http.MethodPost, e.url("/contract"), publish, viewer.Token, http.StatusForbidden},
		{"viewer cannot read auth config", http.MethodGet, e.url("/auth"), "", viewer.Token, http.StatusForbidden},
		{"viewer cannot export", http.MethodGet, e.url("/export"), "", viewer.Token, http.StatusForbidden},
		{"viewer can validate", http.MethodPost, e.url("/contract/validate"), `{"raw":"x"}`, viewer.Token, http.StatusOK},
		{"editor publishes", http.MethodPost, e.url("/contract"), publish, editor.Token, http.StatusCreated},
		{"editor sets auth", http.MethodPut, e.url("/auth"), authBody, editor.Token, http.StatusOK},
		{"editor reads auth", http.MethodGet, e.url("/auth"), "", editor.Token, http.StatusOK},
		{"editor cannot manage tokens", http.MethodGet, e.url("/access/tokens"), "", editor.Token, http.StatusForbidden},
		{"editor cannot change the project", http.MethodPatch, e.url(""), `{"validationMode":"warn"}`, editor.Token, http.StatusForbidden},
		{"admin changes the project", http.MethodPatch, e.url(""), `{"validationMode":"warn"}`, admin.Token, http.StatusOK},
		{"env admin token", http.MethodGet, e.url("/access/tokens"), "", adminEnvToken, http.StatusOK},
		{"unknown project passes through", http.MethodGet, "/api/projects/nope/contract", "", "", http.StatusNotFound},
	}

	for _, tc := range cases {
		rec := e.do(t, tc.method, tc.path, tc.body, tc.token)
		if rec.Code != tc.want {
			t.Errorf("%s: want %d, got %d %s", tc.name, tc.want, rec.Code, rec.Body.String())
		}
	}

	// The project's mock credentials are hidden from readers, in the list too.
	if body := e.do(t, http.MethodGet, e.url(""), "", viewer.Token).Body.String(); strings.Contains(body, "mock-cred") {
		t.Errorf("viewer sees mock credentials: %s", body)
	}

	if body := e.do(t, http.MethodGet, e.url(""), "", editor.Token).Body.String(); !strings.Contains(body, "mock-cred") {
		t.Errorf("editor should see the auth config: %s", body)
	}

	if body := e.do(t, http.MethodGet, "/api/projects", "", "").Body.String(); strings.Contains(body, "mock-cred") {
		t.Errorf("the project list leaks credentials of a locked project: %s", body)
	}

	// Who did what, and the refused attempts.
	log := e.audit(t, viewer.Token, "")

	actions := map[string]string{}
	for _, en := range log {
		actions[en["action"].(string)] = en["actor"].(map[string]any)["name"].(string)
	}

	if actions["contract.publish"] != "editor-tok" || actions["access.denied"] != "viewer-tok" || actions["auth.update"] != "editor-tok" ||
		actions["project.update"] != "admin-tok" {
		t.Errorf("audit attribution: %v", actions)
	}

	denied := e.audit(t, admin.Token, "?action=access.denied")
	if len(denied) == 0 || !strings.Contains(denied[0]["note"].(string), "needs role") {
		t.Errorf("denied entries: %v", denied)
	}
}

func TestAccess_RevokeRules(t *testing.T) {
	t.Parallel()

	e := newAccessEnv(t)
	admin := e.mkToken(t, "admin", "")
	viewer := e.mkToken(t, "viewer", admin.Token)

	// The last admin token stays while others exist.
	if rec := e.do(t, http.MethodDelete, e.url("/access/tokens/"+admin.Info.ID), "", admin.Token); rec.Code != http.StatusBadRequest {
		t.Errorf("revoking the last admin: %d", rec.Code)
	}

	if rec := e.do(t, http.MethodDelete, e.url("/access/tokens/"+viewer.Info.ID), "", admin.Token); rec.Code != http.StatusNoContent {
		t.Errorf("revoke viewer: %d", rec.Code)
	}

	if rec := e.do(t, http.MethodGet, e.url("/endpoints"), "", viewer.Token); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked token: %d", rec.Code)
	}

	// Revoking twice is fine; an unknown token is a 404.
	if rec := e.do(t, http.MethodDelete, e.url("/access/tokens/"+viewer.Info.ID), "", admin.Token); rec.Code != http.StatusNoContent {
		t.Errorf("second revoke: %d", rec.Code)
	}

	if rec := e.do(t, http.MethodDelete, e.url("/access/tokens/zzz"), "", admin.Token); rec.Code != http.StatusNotFound {
		t.Errorf("unknown token: %d", rec.Code)
	}

	// Revoking the only token opens the project again.
	if rec := e.do(t, http.MethodDelete, e.url("/access/tokens/"+admin.Info.ID), "", admin.Token); rec.Code != http.StatusNoContent {
		t.Errorf("revoke the only token: %d", rec.Code)
	}

	if rec := e.do(t, http.MethodGet, e.url("/endpoints"), "", ""); rec.Code == http.StatusUnauthorized {
		t.Error("a project without live tokens is open again")
	}
}

func TestAccess_ShareLinks(t *testing.T) {
	t.Parallel()

	e := newAccessEnv(t)
	admin := e.mkToken(t, "admin", "")

	e.do(t, http.MethodPost, e.url("/contract"), `{"raw":`+jsonString(accessSpec)+`}`, admin.Token)
	e.do(t, http.MethodPost, e.url("/mocks"), `{"method":"GET","path":"/ping","statusCode":200,"body":"{}"}`, admin.Token)
	e.do(t, http.MethodPut, e.url("/auth"), authBody, admin.Token)

	rec := e.do(t, http.MethodPost, e.url("/access/share-links"), `{"label":"for QA","ttlDays":7}`, admin.Token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create link: %d %s", rec.Code, rec.Body.String())
	}

	var link struct {
		Token string `json:"token"`
		URL   string `json:"url"`
		Info  struct {
			ID string `json:"id"`
		} `json:"info"`
	}

	decodeJSON(t, rec, &link)

	if !strings.HasPrefix(link.Token, "oms_") || link.URL != "/api/share/"+link.Token {
		t.Fatalf("link: %+v", link)
	}

	// The snapshot needs no other credential and holds no mock credentials.
	snap := e.do(t, http.MethodGet, link.URL, "", "")
	if snap.Code != http.StatusOK || !strings.Contains(snap.Body.String(), `"readOnly":true`) ||
		!strings.Contains(snap.Body.String(), "/ping") || strings.Contains(snap.Body.String(), "mock-cred") {
		t.Errorf("snapshot: %d %s", snap.Code, snap.Body.String())
	}

	// The link also opens the read routes of its project: header, query and bearer forms.
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"header": e.do(t, http.MethodGet, e.url("/endpoints"), "", "", "X-Share-Token", link.Token),
		"query":  e.do(t, http.MethodGet, e.url("/mocks?share="+link.Token), "", ""),
		"bearer": e.do(t, http.MethodGet, e.url("/contract"), "", link.Token),
	} {
		if rec.Code != http.StatusOK {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	// ...and nothing else.
	for name, tc := range map[string]struct{ method, path, body string }{
		"write":  {http.MethodPost, e.url("/contract"), `{"raw":"x"}`},
		"auth":   {http.MethodGet, e.url("/auth"), ""},
		"export": {http.MethodGet, e.url("/export"), ""},
		"audit":  {http.MethodGet, e.url("/audit"), ""},
		"logs":   {http.MethodGet, e.url("/logs"), ""},
		"tokens": {http.MethodGet, e.url("/access/tokens"), ""},
	} {
		rec := e.do(t, tc.method, tc.path, tc.body, "", "X-Share-Token", link.Token)
		if rec.Code != http.StatusForbidden {
			t.Errorf("share link on %s: %d", name, rec.Code)
		}
	}

	// A query-string secret is not accepted for writes.
	if rec := e.do(t, http.MethodPost, e.url("/contract?share="+link.Token), `{"raw":"x"}`, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("write with a query secret: %d", rec.Code)
	}

	who := e.do(t, http.MethodGet, e.url("/access/whoami"), "", "", "X-Share-Token", link.Token)
	if !strings.Contains(who.Body.String(), `"share":true`) || !strings.Contains(who.Body.String(), "for QA") {
		t.Errorf("whoami: %s", who.Body.String())
	}

	// A link of another project does not open this one.
	other := newAccessEnv(t)
	otherAdmin := other.mkToken(t, "admin", "")
	otherLink := other.do(t, http.MethodPost, other.url("/access/share-links"), `{"label":"x"}`, otherAdmin.Token)

	var ol struct{ Token string }

	decodeJSON(t, otherLink, &ol)

	if rec := e.do(t, http.MethodGet, e.url("/endpoints"), "", "", "X-Share-Token", ol.Token); rec.Code != http.StatusUnauthorized {
		t.Errorf("foreign link: %d", rec.Code)
	}

	// Revoking the link ends the access.
	if rec := e.do(t, http.MethodDelete, e.url("/access/share-links/"+link.Info.ID), "", admin.Token); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke link: %d", rec.Code)
	}

	if rec := e.do(t, http.MethodGet, link.URL, "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("revoked link snapshot: %d", rec.Code)
	}

	if rec := e.do(t, http.MethodGet, e.url("/endpoints"), "", "", "X-Share-Token", link.Token); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked link route: %d", rec.Code)
	}

	if rec := e.do(t, http.MethodDelete, e.url("/access/share-links/zzz"), "", admin.Token); rec.Code != http.StatusNotFound {
		t.Errorf("unknown link: %d", rec.Code)
	}

	list := e.do(t, http.MethodGet, e.url("/access/share-links"), "", admin.Token)
	if !strings.Contains(list.Body.String(), `"revoked":true`) || strings.Contains(list.Body.String(), link.Token) {
		t.Errorf("link list: %s", list.Body.String())
	}

	if rec := e.do(t, http.MethodGet, "/api/share/oms_unknown", "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown link: %d", rec.Code)
	}

	actions := e.audit(t, admin.Token, "?action=share-link.create")
	if len(actions) != 1 || strings.Contains(actions[0]["note"].(string), link.Token) {
		t.Errorf("audit must name the link but never hold its secret: %v", actions)
	}
}

func TestAccess_MockChangesAreAudited(t *testing.T) {
	t.Parallel()

	e := newAccessEnv(t)
	admin := e.mkToken(t, "admin", "")
	viewer := e.mkToken(t, "viewer", admin.Token)

	rec := e.do(t, http.MethodPost, e.url("/mocks"), `{"method":"GET","path":"/ping","statusCode":200,"body":"{}"}`, admin.Token)

	var mock struct{ ID string }

	decodeJSON(t, rec, &mock)

	// Mocks are addressed by id; the project is found through the mock.
	if rec := e.do(t, http.MethodPut, "/api/mocks/"+mock.ID, `{"method":"GET","path":"/ping","statusCode":201,"body":"{}"}`,
		viewer.Token); rec.Code != http.StatusForbidden {
		t.Errorf("viewer updates a mock: %d", rec.Code)
	}

	if rec := e.do(t, http.MethodPut, "/api/mocks/"+mock.ID, `{"method":"GET","path":"/ping","statusCode":201,"body":"{}"}`,
		admin.Token); rec.Code != http.StatusOK {
		t.Errorf("update: %d %s", rec.Code, rec.Body.String())
	}

	if rec := e.do(t, http.MethodDelete, "/api/mocks/"+mock.ID, "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("delete without a token: %d", rec.Code)
	}

	if rec := e.do(t, http.MethodDelete, "/api/mocks/"+mock.ID, "", admin.Token); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}

	if rec := e.do(t, http.MethodDelete, "/api/mocks/"+mock.ID, "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("a deleted mock is a plain 404: %d", rec.Code)
	}

	var got []string

	for _, en := range e.audit(t, admin.Token, "") {
		if en["targetType"] == "mock" {
			got = append(got, fmt.Sprint(en["action"], ":", en["targetId"]))
		}
	}

	want := "mock.delete:" + mock.ID
	if len(got) < 3 || got[0] != want {
		t.Errorf("mock audit entries: %v", got)
	}
}

func TestAccess_AuditQuery(t *testing.T) {
	t.Parallel()

	e := newAccessEnv(t)
	admin := e.mkToken(t, "admin", "")
	e.mkToken(t, "viewer", admin.Token)

	if got := e.audit(t, admin.Token, "?limit=1"); len(got) != 1 {
		t.Errorf("limit: %v", got)
	}

	if got := e.audit(t, admin.Token, "?actor=admin-tok"); len(got) != 1 {
		t.Errorf("actor filter: %v", got)
	}

	if got := e.audit(t, admin.Token, "?actor=nobody"); len(got) != 0 {
		t.Errorf("actor filter: %v", got)
	}

	if got := e.audit(t, admin.Token, "?since=2999-01-01T00:00:00Z"); len(got) != 0 {
		t.Errorf("since filter: %v", got)
	}

	if got := e.audit(t, admin.Token, "?since=2000-01-01T00:00:00Z&action=token.create"); len(got) != 2 {
		t.Errorf("both token creations are audited: %v", got)
	}

	for _, q := range []string{"?limit=0", "?limit=x", "?since=yesterday"} {
		if rec := e.do(t, http.MethodGet, e.url("/audit"+q), "", admin.Token); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
}

func TestAccess_InputValidation(t *testing.T) {
	t.Parallel()

	e := newAccessEnv(t)
	admin := e.mkToken(t, "admin", "")

	for name, body := range map[string]string{
		"no name":   `{"name":" ","role":"viewer"}`,
		"bad role":  `{"name":"x","role":"root"}`,
		"bad ttl":   `{"name":"x","role":"viewer","ttlDays":-1}`,
		"huge ttl":  `{"name":"x","role":"viewer","ttlDays":99999}`,
		"long name": `{"name":"` + strings.Repeat("n", 200) + `","role":"viewer"}`,
		"bad json":  `{`,
	} {
		if rec := e.do(t, http.MethodPost, e.url("/access/tokens"), body, admin.Token); rec.Code != http.StatusBadRequest {
			t.Errorf("token %s: %d", name, rec.Code)
		}
	}

	for name, body := range map[string]string{"no label": `{"label":""}`, "bad ttl": `{"label":"x","ttlDays":-5}`, "bad json": `[`} {
		if rec := e.do(t, http.MethodPost, e.url("/access/share-links"), body, admin.Token); rec.Code != http.StatusBadRequest {
			t.Errorf("link %s: %d", name, rec.Code)
		}
	}

	if rec := e.do(t, http.MethodPost, "/api/projects/nope/access/tokens", `{"name":"x","role":"admin"}`, adminEnvToken); rec.Code != http.StatusNotFound {
		t.Errorf("token for an unknown project: %d", rec.Code)
	}
}

func TestAccess_DisabledWithoutService(t *testing.T) {
	t.Parallel()

	h := httpapi.NewRouter(&httpapi.Services{Projects: usecase.NewProjectService(newMemProjectRepo())})

	for _, path := range []string{"/api/projects/p/access/tokens", "/api/projects/p/audit", "/api/share/x"} {
		if rec := doRequest(t, h, http.MethodGet, path, nil, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
}

func TestAccess_OtherRoutesStayOpen(t *testing.T) {
	t.Parallel()

	e := newAccessEnv(t)
	e.mkToken(t, "admin", "")

	// Project list and the health check need no token; the mock server has
	// its own auth simulation.
	for _, path := range []string{"/api/projects", "/healthz", "/mock/" + e.id + "/ping"} {
		if rec := e.do(t, http.MethodGet, path, "", ""); rec.Code == http.StatusUnauthorized {
			t.Errorf("%s must not need an API token", path)
		}
	}
}
