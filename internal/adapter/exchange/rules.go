package exchange

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

var errNoURL = errors.New("the request has no URL")

// ruleView is a mock rule as the exporters present it.
type ruleView struct {
	name        string
	status      int
	contentType string
	headers     map[string]string
	body        string
}

// mocksFor lists the plain mock rules of an endpoint, the default first.
// Rules with match conditions or faults are left out: they are not
// examples of a response.
func mocksFor(mocks []*domain.MockRule, ep usecase.Endpoint) []*ruleView {
	out := make([]*ruleView, 0, len(mocks))

	for _, m := range mocks {
		if m.Path != ep.Path || !strings.EqualFold(m.Method, ep.Method) || len(m.Match) > 0 || m.Fault != nil {
			continue
		}

		name := m.Scenario
		if name == "" || name == defaultScenario {
			name = defaultScenario
		}

		out = append(out, &ruleView{
			name: fmt.Sprintf("%s (%d)", name, m.StatusCode), status: m.StatusCode,
			contentType: m.ContentType, headers: m.Headers, body: m.Body,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		return strings.HasPrefix(out[i].name, defaultScenario) && !strings.HasPrefix(out[j].name, defaultScenario)
	})

	return out
}

const defaultScenario = "default"
