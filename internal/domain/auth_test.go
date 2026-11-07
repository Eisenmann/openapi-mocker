package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

func TestAuthConfig_Defaults(t *testing.T) {
	t.Parallel()

	var nilCfg *domain.AuthConfig
	if nilCfg.Enabled() || domain.NoAuth().Enabled() {
		t.Error("nil and off configs are disabled")
	}

	cfg := &domain.AuthConfig{Mode: domain.AuthContract}
	if !cfg.Enabled() || cfg.Path() != "/oauth/token" || cfg.TTLSeconds() != domain.DefaultTokenTTL ||
		cfg.KeyHeader() != domain.DefaultAPIKeyHeader {
		t.Errorf("defaults: %q %d %q", cfg.Path(), cfg.TTLSeconds(), cfg.KeyHeader())
	}

	cfg = &domain.AuthConfig{Mode: domain.AuthAll, TokenPath: "/t", TokenTTL: 5, APIKeyHeader: "X-K"}
	if cfg.Path() != "/t" || cfg.TTLSeconds() != 5 || cfg.KeyHeader() != "X-K" {
		t.Errorf("explicit settings: %q %d %q", cfg.Path(), cfg.TTLSeconds(), cfg.KeyHeader())
	}
}

func TestAuthModeAndScenarioValidity(t *testing.T) {
	t.Parallel()

	for _, m := range []domain.AuthMode{"", domain.AuthOff, domain.AuthContract, domain.AuthAll} {
		if !m.Valid() {
			t.Errorf("mode %q should be valid", m)
		}
	}

	for _, s := range []domain.AuthScenario{
		"", domain.AuthValid, domain.AuthExpired, domain.AuthRevoked,
		domain.AuthForbidden, domain.AuthInsufficientScope,
	} {
		if !s.Valid() {
			t.Errorf("scenario %q should be valid", s)
		}
	}

	if domain.AuthMode("x").Valid() || domain.AuthScenario("x").Valid() {
		t.Error("unknown values must be invalid")
	}
}

func TestAuthConfig_Validate(t *testing.T) {
	t.Parallel()

	ok := &domain.AuthConfig{
		Mode:        domain.AuthContract,
		Credentials: []domain.AuthCredential{{Value: "a"}},
		Users:       []domain.AuthUser{{Username: "u", Password: "p", Scenario: domain.AuthForbidden}},
		Clients:     []domain.AuthClient{{ID: "c", Secret: "s"}},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a valid config: %v", err)
	}

	many := make([]domain.AuthCredential, 201)
	for i := range many {
		many[i] = domain.AuthCredential{Value: strings.Repeat("x", i+1)}
	}

	for name, cfg := range map[string]*domain.AuthConfig{
		"mode":         {Mode: "bad"},
		"path":         {Mode: domain.AuthAll, TokenPath: "no-slash"},
		"path space":   {Mode: domain.AuthAll, TokenPath: "/a b"},
		"ttl":          {Mode: domain.AuthAll, TokenTTL: -1},
		"ttl too big":  {Mode: domain.AuthAll, TokenTTL: 1 << 30},
		"header":       {Mode: domain.AuthAll, APIKeyHeader: "X K"},
		"too many":     {Mode: domain.AuthAll, Credentials: many},
		"empty value":  {Mode: domain.AuthAll, Credentials: []domain.AuthCredential{{Value: ""}}},
		"long value":   {Mode: domain.AuthAll, Credentials: []domain.AuthCredential{{Value: strings.Repeat("a", 600)}}},
		"duplicate":    {Mode: domain.AuthAll, Users: []domain.AuthUser{{Username: "u"}, {Username: "u"}}},
		"bad scenario": {Mode: domain.AuthAll, Clients: []domain.AuthClient{{ID: "c", Scenario: "x"}}},
	} {
		err := cfg.Validate()
		if !errors.Is(err, domain.ErrInvalidAuth) {
			t.Errorf("%s: want ErrInvalidAuth, got %v", name, err)
		}
	}
}
