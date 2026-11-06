package exchange

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const (
	postmanSchema = "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"
	mediaJSON     = "application/json"
)

type pmCollection struct {
	Info struct {
		Name string `json:"name"`
	} `json:"info"`
	Item []pmItem `json:"item"`
}

type pmItem struct {
	Name     string          `json:"name"`
	Item     []pmItem        `json:"item"`
	Request  json.RawMessage `json:"request"`
	Response []pmResponse    `json:"response"`
}

type pmRequest struct {
	Method string          `json:"method"`
	URL    json.RawMessage `json:"url"`
	Header []pmKV          `json:"header"`
	Body   struct {
		Mode    string `json:"mode"`
		Raw     string `json:"raw"`
		Options struct {
			Raw struct {
				Language string `json:"language"`
			} `json:"raw"`
		} `json:"options"`
	} `json:"body"`
}

type pmURL struct {
	Raw   string `json:"raw"`
	Path  []any  `json:"path"`
	Query []pmKV `json:"query"`
}

type pmKV struct {
	Key      string `json:"key"`
	Value    any    `json:"value"`
	Disabled bool   `json:"disabled"`
}

type pmResponse struct {
	Name    string          `json:"name"`
	Code    int             `json:"code"`
	Header  json.RawMessage `json:"header"`
	Body    string          `json:"body"`
	Preview string          `json:"_postman_previewlanguage"`
}

func parsePostman(data []byte) (*usecase.ImportedAPI, error) {
	var col pmCollection

	err := json.Unmarshal(data, &col)
	if err != nil {
		return nil, fmt.Errorf("invalid Postman collection: %w", err)
	}

	idx := newEndpointIndex()
	api := &usecase.ImportedAPI{Name: col.Info.Name, Endpoints: nil, Warnings: nil}

	collectPostman(col.Item, idx, api)
	api.Endpoints = idx.endpoints()

	return api, nil
}

// collectPostman walks the items; folders nest to any depth.
func collectPostman(items []pmItem, idx *endpointIndex, api *usecase.ImportedAPI) {
	for i := range items {
		it := &items[i]

		collectPostman(it.Item, idx, api)

		if len(it.Request) == 0 || string(it.Request) == "null" {
			continue
		}

		err := addPostmanRequest(it, idx)
		if err != nil {
			api.Warnings = append(api.Warnings, fmt.Sprintf("skipped %q: %v", it.Name, err))
		}
	}
}

func addPostmanRequest(it *pmItem, idx *endpointIndex) error {
	req, err := decodePostmanRequest(it.Request)
	if err != nil {
		return err
	}

	path, query, err := postmanPath(req.URL)
	if err != nil {
		return err
	}

	method := strings.ToUpper(req.Method)
	if method == "" {
		method = http.MethodGet
	}

	ep := idx.get(method, path)
	if ep.Summary == "" {
		ep.Summary = it.Name
	}

	addQuery(ep, query)

	if ep.Request == nil && req.Body.Mode == "raw" && strings.TrimSpace(req.Body.Raw) != "" {
		ep.Request = &usecase.ImportedBody{ContentType: postmanBodyType(req), Text: req.Body.Raw}
	}

	for i := range it.Response {
		if r, ok := postmanResponse(&it.Response[i]); ok {
			ep.Responses = append(ep.Responses, r)
		}
	}

	return nil
}

// decodePostmanRequest reads a request, which may be a bare URL string (GET).
func decodePostmanRequest(raw json.RawMessage) (*pmRequest, error) {
	var req pmRequest

	if raw[0] == '"' {
		var bare string

		err := json.Unmarshal(raw, &bare)
		if err != nil {
			return nil, fmt.Errorf("invalid request: %w", err)
		}

		req.URL = mustJSON(bare)

		return &req, nil
	}

	err := json.Unmarshal(raw, &req)
	if err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	return &req, nil
}

// mustJSON encodes a string; encoding a string cannot fail.
func mustJSON(s string) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		return nil
	}

	return b
}

// postmanPath reads the templated path and query names out of a Postman URL,
// which is either a string or an object.
func postmanPath(raw json.RawMessage) (path string, query []string, err error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil, errNoURL
	}

	if raw[0] == '"' {
		var s string

		err = json.Unmarshal(raw, &s)
		if err != nil {
			return "", nil, fmt.Errorf("invalid url: %w", err)
		}

		path, query = splitRequestURL(s)

		return path, query, nil
	}

	var u pmURL

	err = json.Unmarshal(raw, &u)
	if err != nil {
		return "", nil, fmt.Errorf("invalid url: %w", err)
	}

	path, query = postmanURLPath(&u)

	for _, q := range u.Query {
		if !q.Disabled && q.Key != "" {
			query = append(query, q.Key)
		}
	}

	return path, query, nil
}

func postmanURLPath(u *pmURL) (path string, query []string) {
	if strings.TrimSpace(u.Raw) != "" {
		return splitRequestURL(u.Raw)
	}

	segs := make([]string, 0, len(u.Path))

	for _, p := range u.Path {
		switch t := p.(type) {
		case string:
			segs = append(segs, t)
		case map[string]any:
			segs = append(segs, str(t[keyValue]))
		}
	}

	path = templatePathSegments("/" + strings.Join(segs, "/"))

	return path, nil
}

func postmanBodyType(req *pmRequest) string {
	for _, h := range req.Header {
		if strings.EqualFold(h.Key, headerContentType) && !h.Disabled {
			return fmt.Sprint(h.Value)
		}
	}

	switch req.Body.Options.Raw.Language {
	case langJSON:
		return mediaJSON
	case langXML:
		return mediaXML
	case langText:
		return mediaText
	}

	if json.Valid([]byte(req.Body.Raw)) {
		return mediaJSON
	}

	return mediaText
}

func postmanResponse(r *pmResponse) (usecase.ImportedResponse, bool) {
	if r.Code < 100 || r.Code > 599 {
		return usecase.ImportedResponse{Status: 0, ContentType: "", Headers: nil, Body: ""}, false
	}

	headers := postmanHeaders(r.Header)
	contentType := firstHeader(headers, headerContentType)

	if contentType == "" && strings.TrimSpace(r.Body) != "" {
		switch {
		case r.Preview == langJSON || json.Valid([]byte(r.Body)):
			contentType = mediaJSON
		case r.Preview == langXML:
			contentType = mediaXML
		default:
			contentType = mediaText
		}
	}

	delete(headers, headerContentType)

	return usecase.ImportedResponse{Status: r.Code, ContentType: contentType, Headers: headers, Body: r.Body}, true
}

// postmanHeaders reads a header list ([{key, value}]; Postman also allows a
// raw string, which is ignored).
func postmanHeaders(raw json.RawMessage) map[string][]string {
	out := map[string][]string{}

	var list []pmKV

	err := json.Unmarshal(raw, &list)
	if err != nil {
		return out
	}

	for _, h := range list {
		if h.Key != "" && !h.Disabled {
			out[http.CanonicalHeaderKey(h.Key)] = append(out[http.CanonicalHeaderKey(h.Key)], fmt.Sprint(h.Value))
		}
	}

	return out
}

func firstHeader(h map[string][]string, name string) string {
	if v := h[name]; len(v) > 0 {
		return v[0]
	}

	return ""
}

// Export.

func exportPostman(data *usecase.ExportData) ([]byte, error) {
	items := make([]any, 0, len(data.Endpoints))

	for _, ep := range data.Endpoints {
		request := postmanRequest(ep, data.BaseURL)
		items = append(items, map[string]any{
			keyName:    ep.Method + " " + ep.Path,
			keyRequest: request,
			"response": postmanExamples(request, mocksFor(data.Mocks, ep)),
		})
	}

	out, err := json.MarshalIndent(map[string]any{
		"info": map[string]any{keyName: data.Name, keySchema: postmanSchema},
		"variable": []any{
			map[string]any{keyKey: "baseUrl", keyValue: data.BaseURL},
		},
		"item": items,
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode Postman collection: %w", err)
	}

	return out, nil
}

func postmanRequest(ep usecase.Endpoint, _ string) map[string]any {
	segs := strings.Split(strings.Trim(ep.Path, "/"), "/")
	path := make([]string, 0, len(segs))
	vars := []any{}

	for _, seg := range segs {
		if len(seg) > 2 && seg[0] == '{' && seg[len(seg)-1] == '}' {
			name := seg[1 : len(seg)-1]
			path = append(path, ":"+name)
			vars = append(vars, map[string]any{keyKey: name, keyValue: ""})

			continue
		}

		path = append(path, seg)
	}

	raw := "{{baseUrl}}/" + strings.Join(path, "/")

	return map[string]any{
		"method": ep.Method,
		"header": []any{},
		"url": map[string]any{
			"raw": raw, "host": []any{"{{baseUrl}}"}, "path": path, "variable": vars,
		},
	}
}

func postmanExamples(request map[string]any, rules []*ruleView) []any {
	out := make([]any, 0, len(rules))

	for _, r := range rules {
		headers := []any{}
		if r.contentType != "" {
			headers = append(headers, map[string]any{keyKey: headerContentType, keyValue: r.contentType})
		}

		for _, k := range sortedKeys(r.headers) {
			headers = append(headers, map[string]any{keyKey: k, keyValue: r.headers[k]})
		}

		preview := "text"
		if strings.Contains(r.contentType, langJSON) {
			preview = langJSON
		}

		out = append(out, map[string]any{
			keyName: r.name, "originalRequest": request, "status": http.StatusText(r.status), "code": r.status,
			"_postman_previewlanguage": preview, "header": headers, "cookie": []any{}, "body": r.body,
		})
	}

	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}

const (
	langJSON  = "json"
	langXML   = "xml"
	langText  = "text"
	mediaXML  = "application/xml"
	mediaText = "text/plain"
)

// Keys and values shared by the exported documents.
const (
	keyName    = "name"
	keyKey     = "key"
	keyID      = "_id"
	keyRequest = "request"
	keyType    = "_type"
	keyValue   = "value"
	keySchema  = "schema"
)

const headerContentType = "Content-Type"
