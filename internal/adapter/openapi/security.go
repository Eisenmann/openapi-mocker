package openapi

import (
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// OperationSecurity returns the security requirements that apply to an
// operation: its own, or else the contract's global ones. An empty
// requirement object ({}) makes authentication optional, so the operation
// is reported as public.
func (e *Engine) OperationSecurity(raw []byte, method, path string) (usecase.OperationSecurity, bool) {
	doc, err := parse(raw)
	if err != nil {
		return usecase.OperationSecurity{Alternatives: nil}, false
	}

	m := findOperation(doc, method, path)
	if m == nil {
		return usecase.OperationSecurity{Alternatives: nil}, false
	}

	reqs := doc.Security
	if m.Operation.Security != nil {
		reqs = *m.Operation.Security
	}

	var alts []usecase.SecurityAlternative

	for _, req := range reqs {
		if len(req) == 0 {
			return usecase.OperationSecurity{Alternatives: nil}, true // optional: public.
		}

		alts = append(alts, alternativeOf(doc, req))
	}

	return usecase.OperationSecurity{Alternatives: alts}, true
}

// alternativeOf turns one security requirement into the schemes it names.
func alternativeOf(doc *openapi3.T, req openapi3.SecurityRequirement) usecase.SecurityAlternative {
	names := make([]string, 0, len(req))
	for name := range req {
		names = append(names, name)
	}

	sort.Strings(names)

	alt := make(usecase.SecurityAlternative, 0, len(names))

	for _, name := range names {
		use := usecase.SchemeUse{Name: name, Kind: usecase.SchemeBearer, In: "", Param: "", Scopes: req[name]}

		if doc.Components != nil {
			if ref := doc.Components.SecuritySchemes[name]; ref != nil && ref.Value != nil {
				use = schemeUse(name, ref.Value, req[name])
			}
		}

		alt = append(alt, use)
	}

	return alt
}

func schemeUse(name string, s *openapi3.SecurityScheme, scopes []string) usecase.SchemeUse {
	use := usecase.SchemeUse{Name: name, Kind: usecase.SchemeBearer, In: "", Param: "", Scopes: scopes}

	switch strings.ToLower(s.Type) {
	case "apikey":
		use.Kind, use.In, use.Param = usecase.SchemeAPIKey, strings.ToLower(s.In), s.Name
	case "http":
		if strings.EqualFold(s.Scheme, "basic") {
			use.Kind = usecase.SchemeBasic
		}
	}

	return use
}
