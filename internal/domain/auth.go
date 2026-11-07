package domain

import (
	"errors"
	"fmt"
	"strings"
)

// AuthMode says how the mock server checks credentials.
type AuthMode string

const (
	// AuthOff checks nothing (the default).
	AuthOff AuthMode = "off"
	// AuthContract checks the credentials the contract asks for: the
	// security requirements of each operation (or the global ones).
	// Operations without a requirement stay public.
	AuthContract AuthMode = "contract"
	// AuthAll requires a credential (bearer token, basic credentials or API
	// key) on every request, whatever the contract says.
	AuthAll AuthMode = "all"
)

// Valid reports whether m is a known mode. The empty string means "off".
func (m AuthMode) Valid() bool {
	switch m {
	case "", AuthOff, AuthContract, AuthAll:
		return true
	default:
		return false
	}
}

// AuthScenario is what happens to a request that carries a given credential.
type AuthScenario string

const (
	// AuthValid accepts the credential (the default).
	AuthValid AuthScenario = "valid"
	// AuthExpired answers 401: the token expired.
	AuthExpired AuthScenario = "expired"
	// AuthRevoked answers 401: the token was revoked.
	AuthRevoked AuthScenario = "revoked"
	// AuthForbidden answers 403: the caller is known but not allowed.
	AuthForbidden AuthScenario = "forbidden"
	// AuthInsufficientScope answers 403 with error=insufficient_scope.
	AuthInsufficientScope AuthScenario = "insufficient_scope"
)

// Valid reports whether s is a known scenario. The empty string means valid.
func (s AuthScenario) Valid() bool {
	switch s {
	case "", AuthValid, AuthExpired, AuthRevoked, AuthForbidden, AuthInsufficientScope:
		return true
	default:
		return false
	}
}

// Limits of an AuthConfig.
const (
	maxAuthEntries   = 200
	maxAuthValueLen  = 512
	maxAuthTTL       = 365 * 24 * 3600
	defaultTokenPath = "/oauth/token" //nolint:gosec // a URL path, not a credential.
	// DefaultTokenTTL is the lifetime of issued tokens in seconds.
	DefaultTokenTTL = 3600
	// DefaultAPIKeyHeader is where mode "all" looks for an API key.
	DefaultAPIKeyHeader = "X-API-Key" //nolint:gosec // a header name, not a credential.
)

// AuthCredential is a fixed bearer token or API key.
type AuthCredential struct {
	Name  string `json:"name,omitempty"`
	Value string `json:"value"`
	// Scopes the credential grants; empty means every scope.
	Scopes   []string     `json:"scopes,omitempty"`
	Scenario AuthScenario `json:"scenario,omitempty"`
}

// AuthUser is a user for basic authentication and the password grant.
type AuthUser struct {
	Username string       `json:"username"`
	Password string       `json:"password"`
	Scopes   []string     `json:"scopes,omitempty"`
	Scenario AuthScenario `json:"scenario,omitempty"`
}

// AuthClient is an OAuth2 client of the token endpoint. Its Scenario applies
// to the tokens it gets, so a client can be set up to receive tokens that
// are expired, revoked or forbidden.
type AuthClient struct {
	ID       string       `json:"id"`
	Secret   string       `json:"secret"`
	Scopes   []string     `json:"scopes,omitempty"`
	Scenario AuthScenario `json:"scenario,omitempty"`
}

// AuthConfig is the auth simulation of a project.
type AuthConfig struct {
	Mode AuthMode `json:"mode"`
	// TokenPath is where the OAuth2 token endpoint listens, relative to the
	// project's mock URL (default /oauth/token).
	TokenPath string `json:"tokenPath,omitempty"`
	// TokenTTL is the lifetime of issued tokens, in seconds.
	TokenTTL int `json:"tokenTtlSeconds,omitempty"`
	// APIKeyHeader is the header carrying the API key in mode "all".
	APIKeyHeader string           `json:"apiKeyHeader,omitempty"`
	Credentials  []AuthCredential `json:"credentials,omitempty"`
	Users        []AuthUser       `json:"users,omitempty"`
	Clients      []AuthClient     `json:"clients,omitempty"`
}

// NoAuth is the config of a project without auth simulation.
func NoAuth() *AuthConfig {
	return &AuthConfig{
		Mode: AuthOff, TokenPath: "", TokenTTL: 0, APIKeyHeader: "", Credentials: nil, Users: nil, Clients: nil,
	}
}

// Enabled reports whether the config checks requests.
func (c *AuthConfig) Enabled() bool {
	return c != nil && (c.Mode == AuthContract || c.Mode == AuthAll)
}

// Path returns the token endpoint path.
func (c *AuthConfig) Path() string {
	if c.TokenPath == "" {
		return defaultTokenPath
	}

	return c.TokenPath
}

// TTLSeconds returns the lifetime of issued tokens.
func (c *AuthConfig) TTLSeconds() int {
	if c.TokenTTL <= 0 {
		return DefaultTokenTTL
	}

	return c.TokenTTL
}

// KeyHeader returns the API key header of mode "all".
func (c *AuthConfig) KeyHeader() string {
	if c.APIKeyHeader == "" {
		return DefaultAPIKeyHeader
	}

	return c.APIKeyHeader
}

// ErrInvalidAuth wraps every AuthConfig.Validate failure.
var ErrInvalidAuth = errors.New("invalid auth config")

// Validate checks the configuration.
func (c *AuthConfig) Validate() error {
	if !c.Mode.Valid() {
		return authErr("mode must be one of: off, contract, all")
	}

	err := c.validateSettings()
	if err != nil {
		return err
	}

	return c.validateEntries()
}

func (c *AuthConfig) validateSettings() error {
	if c.TokenPath != "" && (!strings.HasPrefix(c.TokenPath, "/") || strings.ContainsAny(c.TokenPath, " ?#")) {
		return authErr("tokenPath must start with / and contain no spaces, ? or #")
	}

	if c.TokenTTL < 0 || c.TokenTTL > maxAuthTTL {
		return authErr("tokenTtlSeconds must be between 0 and %d", maxAuthTTL)
	}

	if strings.ContainsAny(c.APIKeyHeader, " :\r\n") {
		return authErr("apiKeyHeader is not a valid header name")
	}

	if len(c.Credentials) > maxAuthEntries || len(c.Users) > maxAuthEntries || len(c.Clients) > maxAuthEntries {
		return authErr("at most %d credentials, users and clients each", maxAuthEntries)
	}

	return nil
}

func (c *AuthConfig) validateEntries() error {
	seen := map[string]bool{}

	for i := range c.Credentials {
		cr := &c.Credentials[i]

		err := checkEntry("credential", cr.Value, cr.Scenario, seen)
		if err != nil {
			return err
		}
	}

	seen = map[string]bool{}

	for i := range c.Users {
		u := &c.Users[i]

		err := checkEntry("user", u.Username, u.Scenario, seen)
		if err != nil {
			return err
		}
	}

	seen = map[string]bool{}

	for i := range c.Clients {
		cl := &c.Clients[i]

		err := checkEntry("client", cl.ID, cl.Scenario, seen)
		if err != nil {
			return err
		}
	}

	return nil
}

// checkEntry validates the identifying value and scenario of one entry.
func checkEntry(kind, value string, scenario AuthScenario, seen map[string]bool) error {
	switch {
	case strings.TrimSpace(value) == "":
		return authErr("every %s needs a value", kind)
	case len(value) > maxAuthValueLen:
		return authErr("a %s is longer than %d characters", kind, maxAuthValueLen)
	case seen[value]:
		return authErr("duplicate %s %q", kind, value)
	case !scenario.Valid():
		return authErr("%s %q: scenario must be one of: valid, expired, revoked, forbidden, insufficient_scope", kind, value)
	}

	seen[value] = true

	return nil
}

func authErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidAuth, fmt.Sprintf(format, args...))
}
