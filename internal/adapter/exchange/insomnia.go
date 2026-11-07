package exchange

import (
	"crypto/sha1" //nolint:gosec // ids of exported requests, not security.
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// insomniaNode is a resource of the v4 export (a flat list with parentId) or
// an item of the v5 collection (a tree with children); both are read into it.
type insomniaNode struct {
	Type       string `json:"_type"`
	Name       string `json:"name"`
	Method     string `json:"method"`
	URL        string `json:"url"`
	Body       insomniaBody
	Parameters []struct {
		Name     string `json:"name"`
		Disabled bool   `json:"disabled"`
	} `json:"parameters"`
	Headers []struct {
		Name     string `json:"name"`
		Value    string `json:"value"`
		Disabled bool   `json:"disabled"`
	} `json:"headers"`
	Children []insomniaNode `json:"children"`
}

type insomniaBody struct {
	MimeType string `json:"mimeType"`
	Text     string `json:"text"`
}

// UnmarshalJSON tolerates the several shapes of "body" (an object, or
// absent).
func (n *insomniaNode) UnmarshalJSON(data []byte) error {
	type plain insomniaNode

	aux := struct {
		*plain

		Body json.RawMessage `json:"body"`
	}{plain: (*plain)(n), Body: nil}

	err := json.Unmarshal(data, &aux)
	if err != nil {
		return err
	}

	if len(aux.Body) > 0 && aux.Body[0] == '{' {
		_ = json.Unmarshal(aux.Body, &n.Body)
	}

	return nil
}

type insomniaFile struct {
	Name       string         `json:"name"`
	Resources  []insomniaNode `json:"resources"`
	Collection []insomniaNode `json:"collection"`
}

func parseInsomnia(data []byte) (*usecase.ImportedAPI, error) {
	raw, err := toJSON(data)
	if err != nil {
		return nil, fmt.Errorf("invalid Insomnia export: %w", err)
	}

	var file insomniaFile

	err = json.Unmarshal(raw, &file)
	if err != nil {
		return nil, fmt.Errorf("invalid Insomnia export: %w", err)
	}

	idx := newEndpointIndex()
	api := &usecase.ImportedAPI{Name: file.Name, Endpoints: nil, Warnings: nil}

	walkInsomnia(append(file.Resources, file.Collection...), idx)

	for i := range file.Resources {
		if file.Resources[i].Type == "workspace" && api.Name == "" {
			api.Name = file.Resources[i].Name
		}
	}

	api.Endpoints = idx.endpoints()

	return api, nil
}

func walkInsomnia(nodes []insomniaNode, idx *endpointIndex) {
	for i := range nodes {
		n := &nodes[i]

		walkInsomnia(n.Children, idx)

		if n.URL != "" && (n.Type == "" || n.Type == keyRequest) {
			addInsomniaRequest(n, idx)
		}
	}
}

func addInsomniaRequest(n *insomniaNode, idx *endpointIndex) {
	path, query := splitRequestURL(n.URL)

	method := strings.ToUpper(n.Method)
	if method == "" {
		method = http.MethodGet
	}

	ep := idx.get(method, path)
	if ep.Summary == "" {
		ep.Summary = n.Name
	}

	for _, p := range n.Parameters {
		if !p.Disabled {
			query = append(query, p.Name)
		}
	}

	addQuery(ep, query)

	if ep.Request == nil && strings.TrimSpace(n.Body.Text) != "" {
		ep.Request = &usecase.ImportedBody{ContentType: n.Body.MimeType, Text: n.Body.Text}
	}
}

// exportInsomnia writes an Insomnia v4 export: a workspace, an environment
// with the mock server's base URL and a request per endpoint.
func exportInsomnia(data *usecase.ExportData) ([]byte, error) {
	workspace := map[string]any{
		keyID: workspaceID, keyType: "workspace", keyName: data.Name, "scope": "collection", keyParent: nil,
	}
	environment := map[string]any{
		keyID: "env_openapi_mocker", keyType: "environment", keyName: "Base Environment",
		keyParent: workspaceID, "data": map[string]any{"baseUrl": data.BaseURL},
	}
	resources := make([]any, 0, len(data.Endpoints)+fixedResources)
	resources = append(resources, workspace, environment)

	for _, ep := range data.Endpoints {
		resources = append(resources, map[string]any{
			keyID: "req_" + shortHash(ep.Method+" "+ep.Path), keyType: keyRequest, keyParent: workspaceID,
			keyName: ep.Method + " " + ep.Path, "method": ep.Method, "url": "{{ _.baseUrl }}" + insomniaPath(ep.Path),
			"body": map[string]any{}, "headers": []any{}, "parameters": []any{},
		})
	}

	out, err := json.MarshalIndent(map[string]any{
		keyType: "export", "__export_format": insomniaExportFormat,
		"__export_date": time.Now().UTC().Format(time.RFC3339), "__export_source": "openapi-mocker",
		"resources": resources,
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode Insomnia export: %w", err)
	}

	return out, nil
}

// insomniaPath writes {id} as :id, the path parameter syntax of Insomnia.
func insomniaPath(path string) string {
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		if len(seg) > 2 && seg[0] == '{' && seg[len(seg)-1] == '}' {
			segs[i] = ":" + seg[1:len(seg)-1]
		}
	}

	return strings.Join(segs, "/")
}

func shortHash(s string) string {
	sum := sha1.Sum([]byte(s)) //nolint:gosec // see import.

	return hex.EncodeToString(sum[:])[:shortHashLen]
}

const (
	insomniaExportFormat = 4
	shortHashLen         = 12
	fixedResources       = 2
)

const (
	keyParent   = "parentId"
	workspaceID = "wrk_openapi_mocker"
)
