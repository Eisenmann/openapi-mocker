package usecase

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// SourceAuth is the MockResponse.Source of responses produced by the auth
// simulation (rejections and the token endpoint).
const SourceAuth = "auth"

// AuthService simulates authentication for the mock server: an OAuth2 token
// endpoint, bearer / basic / API-key checks that follow the contract's
// security requirements, and per-credential scenarios (expired, revoked,
// forbidden, insufficient scope) so clients can exercise their 401 and 403
// paths without an identity provider.
//
// Issued tokens are kept in memory and are lost on restart; the credentials
// in the project's auth config are not.
type AuthService struct {
	projects ProjectGetter
	security SecurityReader
	now      func() time.Time

	logs LogRepository // optional; records the rejections of AuthorizeEndpoint.

	mu      sync.Mutex
	access  map[string]*principal // by project ID + token.
	refresh map[string]*principal // by project ID + refresh token.
}

// AuthOption configures an AuthService.
type AuthOption func(*AuthService)

// WithAuthClock replaces the clock, so tests can move time forward.
func WithAuthClock(now func() time.Time) AuthOption {
	return func(a *AuthService) { a.now = now }
}

// WithAuthLogs makes AuthorizeEndpoint record its rejections in the request log
// (the REST mock server logs its own responses).
func WithAuthLogs(logs LogRepository) AuthOption {
	return func(a *AuthService) { a.logs = logs }
}

// NewAuthService creates the service. security tells which credentials the
// contract asks for; without it only mode "all" can be enforced.
func NewAuthService(projects ProjectGetter, security SecurityReader, opts ...AuthOption) *AuthService {
	a := &AuthService{
		projects: projects, security: security, now: time.Now, logs: nil, mu: sync.Mutex{},
		access: map[string]*principal{}, refresh: map[string]*principal{},
	}

	for _, opt := range opts {
		opt(a)
	}

	return a
}

// principal is who a credential belongs to and what it may do.
type principal struct {
	subject  string
	scopes   []string // empty means every scope.
	scenario domain.AuthScenario
	expires  time.Time // zero means it never expires.
}

// config returns the project's auth config, or nil when the simulation is off.
func (a *AuthService) config(projectID string) *domain.AuthConfig {
	if a == nil || a.projects == nil {
		return nil
	}

	p, err := a.projects.Get(projectID)
	if err != nil || !p.Auth.Enabled() {
		return nil
	}

	return p.Auth
}

// authFailure is a rejected request.
type authFailure struct {
	status    int
	code      string
	desc      string
	challenge string
	rank      int
}

// Failure ranks: when a request fails every alternative of a requirement, the
// most specific failure is reported (a known but forbidden caller beats a
// missing credential).
const (
	rankMissing = iota + 1
	rankInvalid
	rankForbidden
)

func (f *authFailure) response() *MockResponse {
	body, err := json.Marshal(map[string]string{keyError: f.code, "error_description": f.desc})
	if err != nil {
		body = errorBody(f.desc)
	}

	headers := map[string]string{}
	if f.challenge != "" {
		headers["WWW-Authenticate"] = f.challenge
	}

	return &MockResponse{
		StatusCode: f.status, ContentType: ContentTypeJSON, Headers: headers, Body: body, Source: SourceAuth,
	}
}

// Authorize checks the request's credentials. It returns nil when the request
// may proceed, or the 401/403 response to send instead.
func (a *AuthService) Authorize(projectID, raw string, req *MockRequest) *MockResponse {
	cfg := a.config(projectID)
	if cfg == nil {
		return nil
	}

	return a.evaluate(projectID, cfg, a.requirements(cfg, raw, req), req)
}

// AuthorizeEndpoint guards a whole endpoint that has no per-operation
// security metadata in its contract (GraphQL and MCP): when the simulation is
// on, in either mode, the request needs a bearer token, basic credentials or
// an API key. It returns nil when the request may proceed, or the 401/403
// response to send; rejections are logged.
func (a *AuthService) AuthorizeEndpoint(projectID string, req *MockRequest) *MockResponse {
	cfg := a.config(projectID)
	if cfg == nil {
		return nil
	}

	denied := a.evaluate(projectID, cfg, anyRequirement(), req)
	if denied != nil && a.logs != nil {
		a.logs.Add(&domain.RequestLog{
			ID: "", ProjectID: projectID, Method: req.Method, Path: req.Path, StatusCode: denied.StatusCode,
			MatchedRule: "", Matched: false, Source: SourceAuth, Timestamp: a.now().UTC(),
		})
	}

	return denied
}

func anyRequirement() []SecurityAlternative {
	return []SecurityAlternative{{{Name: "", Kind: SchemeAny, In: "", Param: "", Scopes: nil}}}
}

// evaluate checks the request against the alternatives (any one suffices).
func (a *AuthService) evaluate(
	projectID string, cfg *domain.AuthConfig, alts []SecurityAlternative, req *MockRequest,
) *MockResponse {
	if len(alts) == 0 {
		return nil
	}

	var worst *authFailure

	for _, alt := range alts {
		fail := a.checkAlternative(projectID, cfg, alt, req)
		if fail == nil {
			return nil
		}

		if worst == nil || fail.rank > worst.rank {
			worst = fail
		}
	}

	return worst.response()
}

// requirements returns the alternatives the request must satisfy one of; none
// means the operation is public.
func (a *AuthService) requirements(cfg *domain.AuthConfig, raw string, req *MockRequest) []SecurityAlternative {
	if cfg.Mode == domain.AuthAll {
		return anyRequirement()
	}

	if a.security == nil {
		return nil
	}

	sec, ok := a.security.OperationSecurity([]byte(raw), req.Method, req.Path)
	if !ok {
		return nil
	}

	return sec.Alternatives
}

func (a *AuthService) checkAlternative(
	projectID string, cfg *domain.AuthConfig, alt SecurityAlternative, req *MockRequest,
) *authFailure {
	for i := range alt {
		fail := a.checkScheme(projectID, cfg, &alt[i], req)
		if fail != nil {
			return fail
		}
	}

	return nil
}

func (a *AuthService) checkScheme(
	projectID string, cfg *domain.AuthConfig, use *SchemeUse, req *MockRequest,
) *authFailure {
	cred := extractCredential(cfg, use, req)
	if cred.value == "" && cred.user == "" {
		return missingCredential(use)
	}

	var who *principal

	if cred.basic {
		who = lookupUser(cfg, cred.user, cred.value)
	} else {
		who = a.lookupToken(projectID, cfg, cred.value)
	}

	if who == nil {
		return invalidCredential(use, cred.basic)
	}

	return a.judge(who, use)
}

// judge applies the credential's scenario, expiry and scopes.
func (a *AuthService) judge(who *principal, use *SchemeUse) *authFailure {
	switch {
	case who.scenario == domain.AuthExpired || (!who.expires.IsZero() && !a.now().Before(who.expires)):
		return bearerFailure(use, http.StatusUnauthorized, errInvalidToken, "The access token expired", rankInvalid)
	case who.scenario == domain.AuthRevoked:
		return bearerFailure(use, http.StatusUnauthorized, errInvalidToken, "The access token was revoked", rankInvalid)
	case who.scenario == domain.AuthForbidden:
		return bearerFailure(use, http.StatusForbidden, "forbidden",
			"The credential is not allowed to do this", rankForbidden)
	case who.scenario == domain.AuthInsufficientScope || !hasScopes(who.scopes, use.Scopes):
		return scopeFailure(use)
	default:
		return nil
	}
}

// hasScopes reports whether granted covers every required scope. A credential
// with no scopes listed may do anything.
func hasScopes(granted, required []string) bool {
	if len(granted) == 0 {
		return true
	}

	have := make(map[string]bool, len(granted))
	for _, s := range granted {
		have[s] = true
	}

	for _, s := range required {
		if !have[s] {
			return false
		}
	}

	return true
}

// lookupToken finds a static credential or an issued token.
func (a *AuthService) lookupToken(projectID string, cfg *domain.AuthConfig, token string) *principal {
	for i := range cfg.Credentials {
		c := &cfg.Credentials[i]
		if c.Value == token {
			return &principal{subject: c.Name, scopes: c.Scopes, scenario: c.Scenario, expires: time.Time{}}
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	return a.access[tokenKey(projectID, token)]
}

// lookupUser checks basic-auth credentials. With no users configured every
// non-empty pair is accepted.
func lookupUser(cfg *domain.AuthConfig, username, password string) *principal {
	if len(cfg.Users) == 0 {
		return &principal{subject: username, scopes: nil, scenario: domain.AuthValid, expires: time.Time{}}
	}

	for i := range cfg.Users {
		u := &cfg.Users[i]
		if u.Username == username && u.Password == password {
			return &principal{subject: username, scopes: u.Scopes, scenario: u.Scenario, expires: time.Time{}}
		}
	}

	return nil
}

// credential is what the request presented.
type credential struct {
	value string // the token, API key or password.
	user  string // basic auth only.
	basic bool
}

// extractCredential reads the credential the scheme asks for from the request.
func extractCredential(cfg *domain.AuthConfig, use *SchemeUse, req *MockRequest) credential {
	switch use.Kind {
	case SchemeBasic:
		return basicCredential(req)
	case SchemeAPIKey:
		return credential{value: apiKeyValue(use.In, use.Param, req), user: "", basic: false}
	case SchemeBearer:
		return credential{value: bearerToken(req), user: "", basic: false}
	case SchemeAny:
		return anyCredential(cfg, req)
	default:
		return credential{value: "", user: "", basic: false}
	}
}

// anyCredential is mode "all": a bearer token, basic credentials or an API key.
func anyCredential(cfg *domain.AuthConfig, req *MockRequest) credential {
	if token := bearerToken(req); token != "" {
		return credential{value: token, user: "", basic: false}
	}

	if c := basicCredential(req); c.value != "" || c.user != "" {
		return c
	}

	key := apiKeyValue(InHeader, cfg.KeyHeader(), req)
	if key == "" {
		key = apiKeyValue(InQuery, "api_key", req)
	}

	return credential{value: key, user: "", basic: false}
}

func bearerToken(req *MockRequest) string {
	scheme, value, _ := strings.Cut(strings.TrimSpace(headerValue(req.Header, "Authorization")), " ")
	if !strings.EqualFold(scheme, "bearer") {
		return ""
	}

	return strings.TrimSpace(value)
}

func basicCredential(req *MockRequest) credential {
	scheme, value, _ := strings.Cut(strings.TrimSpace(headerValue(req.Header, "Authorization")), " ")
	if !strings.EqualFold(scheme, "basic") {
		return credential{value: "", user: "", basic: true}
	}

	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return credential{value: "", user: "", basic: true}
	}

	user, pass, _ := strings.Cut(string(raw), ":")

	return credential{value: pass, user: user, basic: true}
}

func apiKeyValue(in, name string, req *MockRequest) string {
	switch in {
	case InQuery:
		if vals := req.Query[name]; len(vals) > 0 {
			return vals[0]
		}
	case InCookie:
		return cookieValue(headerValue(req.Header, "Cookie"), name)
	default:
		return strings.TrimSpace(headerValue(req.Header, name))
	}

	return ""
}

func cookieValue(header, name string) string {
	for _, part := range strings.Split(header, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && k == name {
			return v
		}
	}

	return ""
}

// headerValue returns the first value of a header, matching names regardless
// of case.
func headerValue(h map[string][]string, name string) string {
	for k, vals := range h {
		if strings.EqualFold(k, name) && len(vals) > 0 {
			return vals[0]
		}
	}

	return ""
}

func missingCredential(use *SchemeUse) *authFailure {
	return &authFailure{
		status: http.StatusUnauthorized, code: "unauthorized", desc: "Authentication is required",
		challenge: challenge(use, "", ""), rank: rankMissing,
	}
}

func invalidCredential(use *SchemeUse, basic bool) *authFailure {
	if basic {
		return &authFailure{
			status: http.StatusUnauthorized, code: "invalid_credentials", desc: "The username or password is wrong",
			challenge: challenge(use, "", ""), rank: rankInvalid,
		}
	}

	return bearerFailure(use, http.StatusUnauthorized, errInvalidToken,
		"The access token or API key is not valid", rankInvalid)
}

func bearerFailure(use *SchemeUse, status int, code, desc string, rank int) *authFailure {
	c := ""
	if status == http.StatusUnauthorized {
		c = challenge(use, code, desc)
	}

	return &authFailure{status: status, code: code, desc: desc, challenge: c, rank: rank}
}

func scopeFailure(use *SchemeUse) *authFailure {
	scope := strings.Join(use.Scopes, " ")
	c := `Bearer error="insufficient_scope"`

	if scope != "" {
		c += `, scope=` + strconv.Quote(scope)
	}

	return &authFailure{
		status: http.StatusForbidden, code: "insufficient_scope",
		desc: "The token does not have the required scope", challenge: c, rank: rankForbidden,
	}
}

// challenge builds the WWW-Authenticate header of a 401. API keys have no
// standard challenge.
func challenge(use *SchemeUse, code, desc string) string {
	switch use.Kind {
	case SchemeBasic:
		return `Basic realm="mock", charset="UTF-8"`
	case SchemeAPIKey:
		return ""
	case SchemeBearer, SchemeAny:
		c := `Bearer realm="mock"`
		if code != "" {
			c += ", error=" + strconv.Quote(code) + ", error_description=" + strconv.Quote(desc)
		}

		return c
	default:
		return ""
	}
}

// randomToken returns an unguessable token with the given prefix.
func randomToken(prefix string) string {
	const size = 16

	b := make([]byte, size)

	_, err := rand.Read(b)
	if err != nil {
		panic("auth: no randomness: " + err.Error())
	}

	return prefix + hex.EncodeToString(b)
}

const (
	errInvalidToken = "invalid_token"
	keyError        = "error"
)
