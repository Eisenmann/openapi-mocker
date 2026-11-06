package usecase

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Eisenmann/openapi-mocker/internal/domain"
)

// importCollection creates a project from a Postman, Insomnia or HAR file: an
// OpenAPI contract inferred from the requests and examples, and a mock rule
// for every example response.
func (s *ExchangeService) importCollection(kind string, data []byte, name string) (*ImportResult, error) {
	api, err := s.codec.ParseImport(kind, data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidImport, err)
	}

	if len(api.Endpoints) == 0 {
		return nil, fmt.Errorf("%w: the file has no requests", ErrInvalidImport)
	}

	if len(api.Endpoints) > maxImportEndpoint {
		return nil, fmt.Errorf("%w: more than %d endpoints", ErrInvalidImport, maxImportEndpoint)
	}

	p, err := s.createProject(name, "Imported from "+kind, api.Name)
	if err != nil {
		return nil, err
	}

	contract, err := BuildContract(api)
	if err != nil {
		return nil, s.fail(p.ID, fmt.Errorf("%w: %w", ErrInvalidImport, err))
	}

	_, err = s.service.Publish(p.ID, contract, "import:"+kind)
	if err != nil {
		return nil, s.fail(p.ID, fmt.Errorf("%w: the inferred contract is invalid: %w", ErrInvalidImport, err))
	}

	res := &ImportResult{
		Project: p, Kind: "", Contracts: 1, Endpoints: len(api.Endpoints), Mocks: 0, Warnings: api.Warnings,
	}

	for _, rule := range BuildMocks(api) {
		rule.ProjectID = p.ID

		_, err := s.mocks.Create(rule)
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("skipped mock %s %s: %v", rule.Method, rule.Path, err))

			continue
		}

		res.Mocks++
	}

	return res, nil
}

// BuildMocks makes a mock rule of every example response: the lowest 2xx
// response of an endpoint answers by default, the others by scenario
// ("status-404"), so a client can ask for them with X-Mock-Scenario. Bodies are
// served verbatim (they may hold {{...}} that is not for the mock server).
func BuildMocks(api *ImportedAPI) []*domain.MockRule {
	var rules []*domain.MockRule

	for i := range api.Endpoints {
		ep := &api.Endpoints[i]

		seen := map[int]bool{}
		responses := make([]ImportedResponse, 0, len(ep.Responses))

		for _, r := range ep.Responses {
			if r.Status < 100 || r.Status > 599 || seen[r.Status] || len(r.Body) > maxImportedBody || !utf8.ValidString(r.Body) {
				continue
			}

			seen[r.Status] = true

			responses = append(responses, r)
		}

		sort.SliceStable(responses, func(a, b int) bool { return responses[a].Status < responses[b].Status })

		defaultIdx := defaultResponse(responses)

		for idx, r := range responses {
			scenario := ""
			if idx != defaultIdx {
				scenario = "status-" + strconv.Itoa(r.Status)
			}

			rules = append(rules, &domain.MockRule{
				Path: ep.Path, Method: ep.Method, Scenario: scenario, StatusCode: r.Status,
				ContentType: r.ContentType, Headers: recordedHeaders(r.Headers), Body: r.Body, Raw: true,
			})
		}
	}

	return rules
}

// defaultResponse is the index of the lowest 2xx response, or of the first
// one when there is none.
func defaultResponse(responses []ImportedResponse) int {
	for i, r := range responses {
		if r.Status >= http.StatusOK && r.Status < http.StatusMultipleChoices {
			return i
		}
	}

	return 0
}

// BuildContract writes an OpenAPI 3.0 contract (as JSON) for the imported
// endpoints. Schemas are inferred from the example bodies: for responses, a
// field is required when every example has it; request bodies are described
// loosely (no required fields), since one example says little about what a
// server demands.
func BuildContract(api *ImportedAPI) (string, error) {
	paths := map[string]any{}

	for i := range api.Endpoints {
		ep := &api.Endpoints[i]

		item, _ := paths[ep.Path].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[ep.Path] = item
		}

		item[strings.ToLower(ep.Method)] = buildOperation(ep)
	}

	title := api.Name
	if strings.TrimSpace(title) == "" {
		title = "Imported API"
	}

	doc := map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": title, "version": "1.0.0"},
		"paths":   paths,
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode contract: %w", err)
	}

	return string(out), nil
}

func buildOperation(ep *ImportedEndpoint) map[string]any {
	op := map[string]any{"responses": buildResponses(ep)}

	if ep.Summary != "" {
		op["summary"] = ep.Summary
	}

	params := make([]any, 0, len(ep.Query)+len(ep.PathTypes))

	for _, name := range pathParamNames(ep.Path) {
		typ := ep.PathTypes[name]
		if typ == "" {
			typ = kindString
		}

		params = append(params, map[string]any{
			wordName: name, "in": wordPath, schRequired: true, wordSchema: map[string]any{schType: typ},
		})
	}

	for _, q := range ep.Query {
		params = append(params, map[string]any{wordName: q, "in": "query", wordSchema: map[string]any{schType: kindString}})
	}

	if len(params) > 0 {
		op["parameters"] = params
	}

	if body := buildRequestBody(ep.Request); body != nil {
		op["requestBody"] = body
	}

	return op
}

func buildRequestBody(b *ImportedBody) map[string]any {
	if b == nil || strings.TrimSpace(b.Text) == "" {
		return nil
	}

	media := mediaOf(b.ContentType)
	if media == "" {
		media = "application/json"
	}

	var schema any = map[string]any{schType: kindString}

	if isJSONMedia(media) {
		if v, ok := decodeJSON(b.Text); ok {
			schema = looseSchema(inferSchema(v))
		}
	}

	return map[string]any{"content": map[string]any{media: map[string]any{wordSchema: schema}}}
}

func buildResponses(ep *ImportedEndpoint) map[string]any {
	grouped := map[int][]ImportedResponse{}

	for _, r := range ep.Responses {
		if r.Status >= 100 && r.Status <= 599 {
			grouped[r.Status] = append(grouped[r.Status], r)
		}
	}

	if len(grouped) == 0 {
		return map[string]any{"200": map[string]any{schDescription: "OK"}}
	}

	out := map[string]any{}

	for status, list := range grouped {
		out[strconv.Itoa(status)] = buildResponse(status, list)
	}

	return out
}

func buildResponse(status int, list []ImportedResponse) map[string]any {
	resp := map[string]any{schDescription: http.StatusText(status)}
	if resp[schDescription] == "" {
		resp[schDescription] = "Response"
	}

	media := ""
	for _, r := range list {
		if m := mediaOf(r.ContentType); m != "" {
			media = m

			break
		}
	}

	hasBody := false

	for _, r := range list {
		if strings.TrimSpace(r.Body) != "" {
			hasBody = true
		}
	}

	if media == "" || !hasBody {
		return resp
	}

	var schema any = map[string]any{schType: kindString}

	if isJSONMedia(media) {
		if merged, ok := mergedSample(list); ok {
			schema = merged
		}
	}

	resp["content"] = map[string]any{media: map[string]any{wordSchema: schema}}

	return resp
}

// mergedSample infers one schema out of all JSON example bodies of a response.
func mergedSample(list []ImportedResponse) (map[string]any, bool) {
	var merged map[string]any

	for _, r := range list {
		v, ok := decodeJSON(r.Body)
		if !ok {
			continue
		}

		s := inferSchema(v)
		if merged == nil {
			merged = s
		} else {
			merged = mergeSchemas(merged, s)
		}
	}

	return merged, merged != nil
}

func mediaOf(contentType string) string {
	media, _, _ := strings.Cut(contentType, ";")

	return strings.ToLower(strings.TrimSpace(media))
}

func isJSONMedia(media string) bool {
	return media == "application/json" || strings.HasSuffix(media, "+json")
}

func decodeJSON(text string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()

	var v any

	err := dec.Decode(&v)

	return v, err == nil
}

// pathParamNames lists the {names} of a templated path in order.
func pathParamNames(path string) []string {
	var names []string

	for _, seg := range strings.Split(path, "/") {
		if len(seg) > 2 && seg[0] == '{' && seg[len(seg)-1] == '}' {
			names = append(names, seg[1:len(seg)-1])
		}
	}

	return names
}

// Schema inference.

// inferSchema describes a decoded JSON value.
func inferSchema(v any) map[string]any {
	switch t := v.(type) {
	case nil:
		return map[string]any{schNullable: true}
	case bool:
		return map[string]any{schType: "boolean"}
	case string:
		return map[string]any{schType: kindString}
	case json.Number:
		_, err := t.Int64()
		if err == nil {
			return map[string]any{schType: "integer"}
		}

		return map[string]any{schType: "number"}
	case []any:
		return inferArray(t)
	case map[string]any:
		return inferObject(t)
	default:
		return map[string]any{}
	}
}

// inferArray looks at the first few elements: they are enough to see the shape.
func inferArray(elems []any) map[string]any {
	const sampled = 5

	s := map[string]any{schType: kindArray, schItems: map[string]any{}}

	var items map[string]any

	for i, e := range elems {
		if i >= sampled {
			break
		}

		if items == nil {
			items = inferSchema(e)
		} else {
			items = mergeSchemas(items, inferSchema(e))
		}
	}

	if items != nil {
		s[schItems] = items
	}

	return s
}

func inferObject(obj map[string]any) map[string]any {
	props := make(map[string]any, len(obj))
	required := make([]string, 0, len(obj))

	for k, e := range obj {
		props[k] = inferSchema(e)

		required = append(required, k)
	}

	sort.Strings(required)

	s := map[string]any{schType: kindObject, schProps: props}
	if len(required) > 0 {
		s[schRequired] = required
	}

	return s
}

// mergeSchemas combines the schemas of two examples of the same thing: object
// fields are united and stay required only when both have them; a field that
// is null in one example is nullable; differing types fall back to "any".
func mergeSchemas(a, b map[string]any) map[string]any {
	if len(a) == 0 || len(b) == 0 {
		return map[string]any{}
	}

	switch {
	case isNullOnly(a):
		return withNullable(b)
	case isNullOnly(b):
		return withNullable(a)
	case a[schType] != b[schType]:
		return map[string]any{}
	}

	out := map[string]any{schType: a[schType]}
	if a[schNullable] == true || b[schNullable] == true {
		out[schNullable] = true
	}

	switch a[schType] {
	case kindObject:
		mergeObjects(out, a, b)
	case kindArray:
		out[schItems] = mergeItems(a, b)
	}

	return out
}

func mergeItems(a, b map[string]any) map[string]any {
	ai, _ := a[schItems].(map[string]any)
	bi, _ := b[schItems].(map[string]any)

	switch {
	case len(ai) == 0 && bi == nil:
		return map[string]any{}
	case len(ai) == 0:
		return bi
	case len(bi) == 0:
		return ai
	default:
		return mergeSchemas(ai, bi)
	}
}

// isNullOnly is the schema of a null example.
func isNullOnly(s map[string]any) bool {
	return s[schNullable] == true && s[schType] == nil
}

func withNullable(s map[string]any) map[string]any {
	out := make(map[string]any, len(s)+1)
	for k, v := range s {
		out[k] = v
	}

	if len(out) > 0 {
		out[schNullable] = true
	}

	return out
}

func mergeObjects(out, a, b map[string]any) {
	ap, _ := a[schProps].(map[string]any)
	bp, _ := b[schProps].(map[string]any)
	props := map[string]any{}

	for k, v := range ap {
		props[k], _ = v.(map[string]any)
		if other, ok := bp[k].(map[string]any); ok {
			props[k] = mergeSchemas(v.(map[string]any), other) //nolint:forcetypeassert // inferSchema builds maps only.
		}
	}

	for k, v := range bp {
		if _, ok := props[k]; !ok {
			props[k] = v
		}
	}

	out[schProps] = props

	ar, _ := a[schRequired].([]string)
	br, _ := b[schRequired].([]string)
	inB := map[string]bool{}

	for _, k := range br {
		inB[k] = true
	}

	var required []string

	for _, k := range ar {
		if inB[k] {
			required = append(required, k)
		}
	}

	if len(required) > 0 {
		out[schRequired] = required
	}
}

// looseSchema drops every schRequired list: a request body example does not
// say which fields a server insists on.
func looseSchema(s map[string]any) map[string]any {
	delete(s, schRequired)

	if props, ok := s[schProps].(map[string]any); ok {
		for _, p := range props {
			if m, ok := p.(map[string]any); ok {
				looseSchema(m)
			}
		}
	}

	if items, ok := s[schItems].(map[string]any); ok {
		looseSchema(items)
	}

	return s
}

const (
	wordName   = "name"
	wordPath   = "path"
	wordSchema = "schema"
)

// OpenAPI schema keywords.
const (
	schType        = "type"
	schItems       = "items"
	schNullable    = "nullable"
	schProps       = "properties"
	schRequired    = "required"
	schDescription = "description"
)
