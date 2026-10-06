package usecase_test

import (
	"errors"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func matchFixture(t *testing.T, rules ...*domain.MockRule) (*usecase.MockServingService, string) {
	t.Helper()

	projects := newMockProjectRepo()
	p := projects.Create("x", "")
	contracts := newMockContractRepo()
	contracts.AddVersion(p.ID, "yaml", "raw", "manual")

	mocks := newMockMockRepo()
	for _, r := range rules {
		r.ProjectID = p.ID
		r.Path, r.Method = "/orders/{id}", "POST"
		mocks.CreateMock(r)
	}

	engine := &mockContractEngine{found: true, pathTemplate: "/orders/{id}"}

	return usecase.NewMockServingService(contracts, mocks, newMockLogRepo(), engine), p.ID
}

func serveMatch(svc *usecase.MockServingService, pid, scenario, body string) usecase.MockResponse {
	return svc.ServeRequest(pid, &usecase.MockRequest{
		Method: "POST", Path: "/orders/42", Scenario: scenario, Body: []byte(body),
		Query:  map[string][]string{"mode": {"fast"}},
		Header: map[string][]string{"X-Tenant": {"acme"}}, SkipValidation: true,
	})
}

func TestMatching_PicksRuleByBodyQueryHeaderAndPath(t *testing.T) {
	t.Parallel()

	svc, pid := matchFixture(t,
		&domain.MockRule{Scenario: "default", StatusCode: 200, Body: `{"r":"default"}`},
		&domain.MockRule{Scenario: "vip", StatusCode: 402, Body: `{"r":"vip"}`, Match: []domain.MatchCondition{
			{Source: domain.MatchBody, Name: "user.tier", Value: "vip"},
		}},
		&domain.MockRule{Scenario: "acme", StatusCode: 409, Body: `{"r":"acme"}`, Match: []domain.MatchCondition{
			{Source: domain.MatchHeader, Name: "x-tenant", Value: "acme"},
			{Source: domain.MatchQuery, Name: "mode", Op: domain.OpRegex, Value: "^fa"},
		}},
		&domain.MockRule{Scenario: "id", StatusCode: 404, Body: `{"r":"id"}`, Match: []domain.MatchCondition{
			{Source: domain.MatchPath, Name: "id", Value: "99"},
		}},
	)

	// Header + query rule has two satisfied conditions, so it beats the one-condition vip rule.
	if r := serveMatch(svc, pid, "", `{"user":{"tier":"vip"}}`); r.StatusCode != 409 {
		t.Errorf("most conditions must win, got %d %s", r.StatusCode, r.Body)
	}

	if r := serveMatch(svc, pid, "", `{}`); r.StatusCode != 409 {
		t.Errorf("header+query rule: got %d", r.StatusCode)
	}

	// The path rule does not match id 42.
	if r := serveMatch(svc, pid, "", `{}`); string(r.Body) == `{"r":"id"}` {
		t.Error("path rule must not match another id")
	}
}

func TestMatching_FallbackAndScenarioPriority(t *testing.T) {
	t.Parallel()

	svc, pid := matchFixture(t,
		&domain.MockRule{Scenario: "default", StatusCode: 200, Body: `{"r":"default"}`},
		&domain.MockRule{Scenario: "slow", StatusCode: 504, Body: `{"r":"slow"}`},
		&domain.MockRule{Scenario: "vip", StatusCode: 402, Body: `{"r":"vip"}`, Match: []domain.MatchCondition{
			{Source: domain.MatchBody, Name: "tier", Value: "vip"},
		}},
	)

	if r := serveMatch(svc, pid, "", `{"tier":"basic"}`); r.StatusCode != 200 {
		t.Errorf("no condition holds: default rule expected, got %d", r.StatusCode)
	}

	if r := serveMatch(svc, pid, "", `{"tier":"vip"}`); r.StatusCode != 402 {
		t.Errorf("condition holds: got %d", r.StatusCode)
	}

	if r := serveMatch(svc, pid, "slow", `{"tier":"vip"}`); r.StatusCode != 504 {
		t.Errorf("X-Mock-Scenario must win over conditions, got %d", r.StatusCode)
	}

	if r := serveMatch(svc, pid, "", `not json`); r.StatusCode != 200 {
		t.Errorf("unparsable body: default rule expected, got %d", r.StatusCode)
	}
}

func TestMatching_ConditionalOnlyFallsBackToExample(t *testing.T) {
	t.Parallel()

	svc, pid := matchFixture(t, &domain.MockRule{Scenario: "vip", StatusCode: 402, Body: `{}`, Match: []domain.MatchCondition{
		{Source: domain.MatchBody, Name: "tier", Value: "vip"},
	}})

	if r := serveMatch(svc, pid, "", `{"tier":"basic"}`); r.Source != "schema-example" {
		t.Errorf("a conditional rule must not act as the default, got source %q", r.Source)
	}
}

func TestMockService_ValidatesMatchConditions(t *testing.T) {
	t.Parallel()

	svc := usecase.NewMockService(newMockMockRepo(), newMockContractRepo(), newMockProviderRepo(),
		&mockContractEngine{}, &mockLLMGateway{})

	bad := &domain.MockRule{ProjectID: "p", Path: "/x", Method: "GET", Match: []domain.MatchCondition{
		{Source: domain.MatchBody, Op: domain.OpRegex, Value: "("},
	}}

	if _, err := svc.Create(bad); !errors.Is(err, usecase.ErrInvalidMatch) {
		t.Errorf("want ErrInvalidMatch, got %v", err)
	}

	if usecase.StatusFromError(usecase.ErrInvalidMatch) != 400 {
		t.Error("invalid match must map to 400")
	}

	good := &domain.MockRule{ProjectID: "p", Path: "/x", Method: "GET", Match: []domain.MatchCondition{
		{Source: domain.MatchQuery, Name: "a", Value: "b"},
	}}
	if _, err := svc.Create(good); err != nil {
		t.Errorf("valid conditions: %v", err)
	}
}
