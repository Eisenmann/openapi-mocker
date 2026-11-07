package usecase

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// OAuth2 grant types the token endpoint supports.
const (
	grantClientCredentials = "client_credentials"
	grantPassword          = "password"
	grantAuthCode          = "authorization_code"
	grantRefresh           = "refresh_token"
)

// oauthError is an RFC 6749 error response.
type oauthError struct {
	status int
	code   string
	desc   string
}

func (e *oauthError) response() *MockResponse {
	body, err := json.Marshal(map[string]string{keyError: e.code, "error_description": e.desc})
	if err != nil {
		body = errorBody(e.desc)
	}

	headers := map[string]string{"Cache-Control": "no-store", "Pragma": "no-cache"}
	if e.status == http.StatusUnauthorized {
		headers["WWW-Authenticate"] = `Basic realm="mock"`
	}

	return &MockResponse{
		StatusCode: e.status, ContentType: ContentTypeJSON, Headers: headers, Body: body, Source: SourceAuth,
	}
}

func oauthFail(status int, code, desc string) *oauthError {
	return &oauthError{status: status, code: code, desc: desc}
}

// HandleToken answers requests to the project's OAuth2 token endpoint. It
// reports false when the request is not for the endpoint (or the simulation
// is off), so the caller serves it as an ordinary mock request.
func (a *AuthService) HandleToken(projectID string, req *MockRequest) (*MockResponse, bool) {
	cfg := a.config(projectID)
	if cfg == nil || req.Path != cfg.Path() {
		return nil, false
	}

	if !strings.EqualFold(req.Method, http.MethodPost) {
		resp := oauthFail(http.StatusMethodNotAllowed, errInvalidRequest, "use POST").response()
		resp.Headers["Allow"] = http.MethodPost

		return resp, true
	}

	params := tokenParams(req)

	granted, oerr := a.grant(projectID, cfg, req, params)
	if oerr != nil {
		return oerr.response(), true
	}

	return a.issue(projectID, cfg, granted), true
}

// tokenParams reads the request's parameters: a form body (the standard), a
// JSON object, or the query string.
func tokenParams(req *MockRequest) url.Values {
	params := url.Values{}
	for k, v := range req.Query {
		params[k] = v
	}

	if strings.Contains(strings.ToLower(headerValue(req.Header, hdrContentType)), subtypeJSON) {
		var obj map[string]any

		err := json.Unmarshal(req.Body, &obj)
		if err == nil {
			for k, v := range obj {
				if s, ok := v.(string); ok {
					params.Set(k, s)
				}
			}
		}

		return params
	}

	form, err := url.ParseQuery(string(req.Body))
	if err == nil {
		for k, v := range form {
			params[k] = v
		}
	}

	return params
}

// grantResult is what the endpoint decided to issue.
type grantResult struct {
	subject  string
	scopes   []string
	scenario domain.AuthScenario
	refresh  bool // issue a refresh token too.
}

func (a *AuthService) grant(
	projectID string, cfg *domain.AuthConfig, req *MockRequest, params url.Values,
) (*grantResult, *oauthError) {
	client, oerr := authenticateClient(cfg, req, params)
	if oerr != nil {
		return nil, oerr
	}

	requested := strings.Fields(params.Get("scope"))

	switch params.Get("grant_type") {
	case grantClientCredentials:
		return clientCredentialsGrant(cfg, client, requested)
	case grantPassword:
		return passwordGrant(cfg, client, params, requested)
	case grantAuthCode:
		return authCodeGrant(client, params, requested)
	case grantRefresh:
		return a.refreshGrant(projectID, params)
	case "":
		return nil, oauthFail(http.StatusBadRequest, errInvalidRequest, "grant_type is required")
	default:
		return nil, oauthFail(http.StatusBadRequest, "unsupported_grant_type",
			"supported grants: client_credentials, password, authorization_code, refresh_token")
	}
}

// authenticateClient checks the client credentials sent in the Authorization
// header or the body. It returns nil client when none were sent, or when no
// clients are configured (every client is then accepted).
func authenticateClient(cfg *domain.AuthConfig, req *MockRequest, params url.Values) (*domain.AuthClient, *oauthError) {
	id, secret := params.Get("client_id"), params.Get("client_secret")

	if c := basicCredential(req); c.user != "" {
		id, secret = c.user, c.value
	}

	if id == "" {
		return nil, nil
	}

	if len(cfg.Clients) == 0 {
		return nil, nil
	}

	for i := range cfg.Clients {
		c := &cfg.Clients[i]
		if c.ID == id && c.Secret == secret {
			return c, nil
		}
	}

	return nil, oauthFail(http.StatusUnauthorized, "invalid_client", "unknown client or wrong secret")
}

func clientCredentialsGrant(
	cfg *domain.AuthConfig, client *domain.AuthClient, requested []string,
) (*grantResult, *oauthError) {
	if client == nil && len(cfg.Clients) > 0 {
		return nil, oauthFail(http.StatusUnauthorized, "invalid_client", "client authentication is required")
	}

	allowed, scenario, subject := clientDefaults(client)

	scopes, ok := grantScopes(requested, allowed)
	if !ok {
		return nil, oauthFail(http.StatusBadRequest, errInvalidScope, "the client may not request these scopes")
	}

	return &grantResult{subject: subject, scopes: scopes, scenario: scenario, refresh: false}, nil
}

func clientDefaults(client *domain.AuthClient) (scopes []string, scenario domain.AuthScenario, subject string) {
	if client == nil {
		return nil, domain.AuthValid, "client"
	}

	return client.Scopes, client.Scenario, client.ID
}

func passwordGrant(
	cfg *domain.AuthConfig, client *domain.AuthClient, params url.Values, requested []string,
) (*grantResult, *oauthError) {
	username, password := params.Get("username"), params.Get("password")
	if username == "" || password == "" {
		return nil, oauthFail(http.StatusBadRequest, errInvalidRequest, "username and password are required")
	}

	allowed, scenario, _ := clientDefaults(client)

	if len(cfg.Users) > 0 {
		user := lookupUser(cfg, username, password)
		if user == nil {
			return nil, oauthFail(http.StatusBadRequest, errInvalidGrant, "wrong username or password")
		}

		allowed = user.scopes
		if user.scenario != "" && user.scenario != domain.AuthValid {
			scenario = user.scenario
		}
	}

	scopes, ok := grantScopes(requested, allowed)
	if !ok {
		return nil, oauthFail(http.StatusBadRequest, errInvalidScope, "the user may not request these scopes")
	}

	return &grantResult{subject: username, scopes: scopes, scenario: scenario, refresh: true}, nil
}

// authCodeGrant accepts any authorization code, except the ones that simulate
// a failure: "invalid", "expired" and "used".
func authCodeGrant(client *domain.AuthClient, params url.Values, requested []string) (*grantResult, *oauthError) {
	code := params.Get("code")

	switch code {
	case "":
		return nil, oauthFail(http.StatusBadRequest, errInvalidRequest, "code is required")
	case "invalid", "expired", "used":
		return nil, oauthFail(http.StatusBadRequest, errInvalidGrant, "the authorization code is "+code)
	}

	allowed, scenario, subject := clientDefaults(client)

	scopes, ok := grantScopes(requested, allowed)
	if !ok {
		return nil, oauthFail(http.StatusBadRequest, errInvalidScope, "the client may not request these scopes")
	}

	return &grantResult{subject: subject, scopes: scopes, scenario: scenario, refresh: true}, nil
}

// refreshGrant exchanges a refresh token for new tokens. The old refresh
// token stops working (rotation).
func (a *AuthService) refreshGrant(projectID string, params url.Values) (*grantResult, *oauthError) {
	token := params.Get("refresh_token")

	a.mu.Lock()
	defer a.mu.Unlock()

	who, ok := a.refresh[tokenKey(projectID, token)]
	if !ok || token == "" {
		return nil, oauthFail(http.StatusBadRequest, errInvalidGrant, "the refresh token is not valid")
	}

	delete(a.refresh, tokenKey(projectID, token))

	return &grantResult{subject: who.subject, scopes: who.scopes, scenario: who.scenario, refresh: true}, nil
}

// grantScopes picks the scopes to grant: those requested when the owner may
// have them, else everything the owner may have. ok is false when a requested
// scope is not allowed.
func grantScopes(requested, allowed []string) (scopes []string, ok bool) {
	if len(requested) == 0 {
		return append([]string(nil), allowed...), true
	}

	if len(allowed) == 0 {
		return requested, true
	}

	if !hasScopes(allowed, requested) {
		return nil, false
	}

	return requested, true
}

// issue stores new tokens and builds the token response.
func (a *AuthService) issue(projectID string, cfg *domain.AuthConfig, g *grantResult) *MockResponse {
	ttl := time.Duration(cfg.TTLSeconds()) * time.Second
	now := a.now()
	who := &principal{subject: g.subject, scopes: g.scopes, scenario: g.scenario, expires: now.Add(ttl)}
	access := randomToken("mock_at_")

	out := map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": int(ttl.Seconds()),
	}

	if len(g.scopes) > 0 {
		out["scope"] = strings.Join(g.scopes, " ")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.prune(now)
	a.access[tokenKey(projectID, access)] = who

	if g.refresh {
		refresh := randomToken("mock_rt_")
		a.refresh[tokenKey(projectID, refresh)] = &principal{
			subject: g.subject, scopes: g.scopes, scenario: g.scenario, expires: time.Time{},
		}
		out["refresh_token"] = refresh
	}

	body, err := json.Marshal(out)
	if err != nil {
		return oauthFail(http.StatusInternalServerError, "server_error", "cannot encode the token").response()
	}

	return &MockResponse{
		StatusCode: http.StatusOK, ContentType: ContentTypeJSON, Body: body, Source: SourceAuth,
		Headers: map[string]string{"Cache-Control": "no-store", "Pragma": "no-cache"},
	}
}

// prune forgets access tokens that expired long ago, so a busy test run does
// not grow the table forever. The caller holds a.mu.
func (a *AuthService) prune(now time.Time) {
	const grace = time.Hour

	for k, p := range a.access {
		if !p.expires.IsZero() && now.After(p.expires.Add(grace)) {
			delete(a.access, k)
		}
	}
}

// OAuth2 error codes and names shared by the token endpoint.
const (
	errInvalidGrant   = "invalid_grant"
	errInvalidRequest = "invalid_request"
	errInvalidScope   = "invalid_scope"
	hdrContentType    = "Content-Type"
	subtypeJSON       = "json"
)

// tokenKey is the key of a token in the per-project token tables.
func tokenKey(projectID, token string) string {
	return projectID + "\x00" + token
}
