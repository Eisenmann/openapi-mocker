package usecase

import (
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/example/openapi-mocker/internal/domain"
)

// MockServingService is the central business scenario of the application:
// given a request (projectID, method, path, scenario), build the response
// using the project's active contract and configured mock rules, falling
// back to an example generated directly from the contract's JSON Schema.
// It knows nothing about net/http — it decides only "what to respond" and
// not "how to write it to the socket.".
type MockServingService struct {
	contracts ContractRepository
	mocks     MockRepository
	logs      LogRepository
	engine    ContractEngine
}

func NewMockServingService(
	contracts ContractRepository,
	mocks MockRepository,
	logs LogRepository,
	engine ContractEngine,
) *MockServingService {
	return &MockServingService{
		contracts: contracts,
		mocks:     mocks,
		logs:      logs,
		engine:    engine,
	}
}

// Serve returns a ready MockResponse. An error is returned only for truly
// exceptional situations — for ordinary "endpoint not described in the
// contract" it returns a MockResponse with status 404, so the calling code
// (the HTTP adapter) doesn't have to duplicate error mapping and can just
// write the response as-is, logging it.
func (s *MockServingService) Serve(projectID, method, path, scenario string) MockResponse {
	contract, err := s.contracts.GetActive(projectID)
	if err != nil {
		return s.finish(projectID, method, path, &MockResponse{
			StatusCode: 404, ContentType: "application/json",
			Body: errorBody("no contract has been uploaded for this project yet"),
		})
	}

	pathTemplate, _, found := s.engine.FindOperation([]byte(contract.Raw), method, path)
	if !found {
		return s.finish(projectID, method, path, &MockResponse{
			StatusCode: 404, ContentType: "application/json",
			Body: errorBody(fmt.Sprintf("endpoint not described in contract: %s %s", method, path)),
		})
	}

	if rule := s.findRule(projectID, pathTemplate, method, scenario); rule != nil {
		return s.finish(projectID, method, path, s.fromRule(rule))
	}

	// Fallback: no rule configured manually or via LLM — example from schema, 200.
	status := "200"
	body, contentType, err := s.engine.ExampleResponse([]byte(contract.Raw), method, path, status)
	if err != nil {
		return s.finish(projectID, method, path, &MockResponse{
			StatusCode: 500, ContentType: "application/json",
			Body: errorBody("failed to build response example: " + err.Error()),
		})
	}
	code, _ := strconv.Atoi(status)
	return s.finish(projectID, method, path, &MockResponse{
		StatusCode: code, ContentType: nonEmpty(contentType, "application/json"),
		Body: body, Source: "schema-example", Matched: true,
	})
}

func (s *MockServingService) findRule(projectID, pathTemplate, method, scenario string) *domain.MockRule {
	method = strings.ToUpper(method)
	var fallback *domain.MockRule
	for _, m := range s.mocks.ListMocks(projectID) {
		if !strings.EqualFold(m.Method, method) || m.Path != pathTemplate {
			continue
		}
		if scenario != "" && m.Scenario == scenario {
			return m
		}
		if m.Scenario == "" || m.Scenario == "default" {
			fallback = m
		}
	}
	return fallback
}

func (s *MockServingService) fromRule(rule *domain.MockRule) *MockResponse {
	// Chaos testing: with probability FailRatePct, return a random 5xx error,
	// even when the rule describes a successful scenario.
	if rule.FailRatePct > 0 && rand.Intn(100) < rule.FailRatePct {
		code := 500 + rand.Intn(4) // 500-503
		return &MockResponse{
			StatusCode: code, ContentType: "application/json",
			Body:    errorBody("injected failure for chaos testing"),
			DelayMs: rule.DelayMs, Source: "chaos-injection", MatchedRule: rule.ID, Matched: true,
		}
	}
	code := rule.StatusCode
	if code == 0 {
		code = 200
	}
	return &MockResponse{
		StatusCode: code, ContentType: nonEmpty(rule.ContentType, "application/json"),
		Headers: rule.Headers, Body: []byte(rule.Body), DelayMs: rule.DelayMs,
		Source: "rule", MatchedRule: rule.ID, Matched: true,
	}
}

func (s *MockServingService) finish(projectID, method, path string, resp *MockResponse) MockResponse {
	s.logs.Add(&domain.RequestLog{
		ProjectID: projectID, Method: method, Path: path,
		StatusCode: resp.StatusCode, MatchedRule: resp.MatchedRule, Matched: resp.Matched,
		Timestamp: time.Now().UTC(),
	})
	return *resp
}

func nonEmpty(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func errorBody(msg string) []byte {
	// equivalent to json.Marshal(map[string]string{"error": msg}), but without
	// importing encoding/json just for one string — manually escape the minimum needed.
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(msg)
	return []byte(`{"error":"` + esc + `"}`)
}
