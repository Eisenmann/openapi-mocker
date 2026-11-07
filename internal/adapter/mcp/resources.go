package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// codeResourceNotFound is the MCP error code of resources/read for a URI the
// server does not know.
const codeResourceNotFound = -32002

// jsonNull is the JSON null literal.
const jsonNull = "null"

// Keys shared by several MCP result objects.
const (
	keyDescription = "description"
	keyMimeType    = "mimeType"
	keyTitle       = "title"
	keyText        = "text"
	keyContent     = "content"
	keyType        = "type"
	keyURI         = "uri"
	keyListChanged = "listChanged"
)

// Static validation errors of resources and prompts.
var (
	errResourceURI       = errors.New("resource uri is required")
	errDuplicateResource = errors.New("duplicate resource uri")
	errBadBlob           = errors.New("blob is not valid base64")
	errBadTemplate       = errors.New("uriTemplate needs at least one {variable} and balanced braces")
	errPromptName        = errors.New("prompt name is required")
	errDuplicatePrompt   = errors.New("duplicate prompt name")
	errPromptArgument    = errors.New("prompt argument names must be non-empty and unique")
	errPromptMessage     = errors.New("prompt needs at least one message with role user or assistant and content")
	errNotObject         = errors.New("must be a JSON object")
)

// manifestResource is a resource or, when URITemplate is set, a resource
// template of the manifest. Text may be a JSON string or any other JSON value
// (served as its compact JSON text); Blob is base64.
type manifestResource struct {
	URI         string          `json:"uri"`
	URITemplate string          `json:"uriTemplate"`
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	MimeType    string          `json:"mimeType"`
	Text        json.RawMessage `json:"text"`
	Blob        string          `json:"blob"`
	Annotations json.RawMessage `json:"annotations"`
}

// manifestPrompt is one prompt of the manifest.
type manifestPrompt struct {
	Name        string                      `json:"name"`
	Title       string                      `json:"title"`
	Description string                      `json:"description"`
	Arguments   []usecase.MCPPromptArgument `json:"arguments"`
	Messages    []promptMessage             `json:"messages"`
}

// promptMessage is one message of a prompt: content is a string (text) or an
// MCP content object (image, resource, ...).
type promptMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// nonNull returns nil for an absent or JSON null value.
func nonNull(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == jsonNull {
		return nil
	}

	return raw
}

// isObject reports whether raw (when present) is a JSON object.
func isObject(raw json.RawMessage) bool {
	raw = nonNull(raw)
	if raw == nil {
		return true
	}

	var v map[string]any

	return json.Unmarshal(raw, &v) == nil && v != nil
}

// checkMetadata validates the optional outputSchema and annotations.
func (t *manifestTool) checkMetadata() error {
	if !isObject(t.OutputSchema) {
		return fmt.Errorf("outputSchema %w", errNotObject)
	}

	if !isObject(t.Annotations) {
		return fmt.Errorf("annotations %w", errNotObject)
	}

	return nil
}

// ensureStructured gives a tool result with an outputSchema its
// structuredContent: when the mock only has a text item holding a JSON
// object, that object becomes the structured content.
func ensureStructured(result map[string]any) {
	if _, ok := result["structuredContent"]; ok {
		return
	}

	items, _ := result[keyContent].([]any)
	for _, it := range items {
		item, ok := it.(map[string]any)
		if !ok || item[keyType] != keyText {
			continue
		}

		txt, _ := item[keyText].(string)

		var obj map[string]any

		err := json.Unmarshal([]byte(txt), &obj)
		if err == nil && obj != nil {
			result["structuredContent"] = obj

			return
		}
	}
}

// capabilities lists the MCP features the manifest provides.
func (m *manifest) capabilities() map[string]any {
	caps := map[string]any{}

	if len(m.Tools) > 0 {
		caps["tools"] = map[string]any{keyListChanged: false}
	}

	if len(m.Resources)+len(m.Templates) > 0 {
		caps["resources"] = map[string]any{"subscribe": false, keyListChanged: false}
	}

	if len(m.Prompts) > 0 {
		caps["prompts"] = map[string]any{keyListChanged: false}
	}

	return caps
}

// Validation of resources and prompts.

// validateResources checks resource URIs, blobs and template syntax.
func validateResources(resources, templates []manifestResource) error {
	seen := map[string]bool{}

	for i := range resources {
		res := &resources[i]

		switch {
		case res.URI == "":
			return fmt.Errorf("resource %d: %w", i, errResourceURI)
		case seen[res.URI]:
			return fmt.Errorf("resource %q: %w", res.URI, errDuplicateResource)
		case !validBlob(res.Blob):
			return fmt.Errorf("resource %q: %w", res.URI, errBadBlob)
		case !isObject(res.Annotations):
			return fmt.Errorf("resource %q: annotations %w", res.URI, errNotObject)
		}

		seen[res.URI] = true
	}

	for i := range templates {
		tpl := &templates[i]
		if seen[tpl.URITemplate] {
			return fmt.Errorf("resource template %q: %w", tpl.URITemplate, errDuplicateResource)
		}

		seen[tpl.URITemplate] = true

		_, err := compileTemplate(tpl.URITemplate)
		if err != nil {
			return fmt.Errorf("resource template %q: %w", tpl.URITemplate, err)
		}
	}

	return nil
}

func validBlob(blob string) bool {
	if blob == "" {
		return true
	}

	_, err := base64.StdEncoding.DecodeString(blob)

	return err == nil
}

// validatePrompts checks prompt names, arguments and messages.
func validatePrompts(prompts []manifestPrompt) error {
	seen := map[string]bool{}

	for i := range prompts {
		p := &prompts[i]

		switch {
		case p.Name == "":
			return fmt.Errorf("prompt %d: %w", i, errPromptName)
		case seen[p.Name]:
			return fmt.Errorf("prompt %q: %w", p.Name, errDuplicatePrompt)
		case !validPromptArgs(p.Arguments):
			return fmt.Errorf("prompt %q: %w", p.Name, errPromptArgument)
		case !validPromptMessages(p.Messages):
			return fmt.Errorf("prompt %q: %w", p.Name, errPromptMessage)
		}

		seen[p.Name] = true
	}

	return nil
}

func validPromptArgs(args []usecase.MCPPromptArgument) bool {
	seen := map[string]bool{}

	for _, a := range args {
		if a.Name == "" || seen[a.Name] {
			return false
		}

		seen[a.Name] = true
	}

	return true
}

func validPromptMessages(msgs []promptMessage) bool {
	if len(msgs) == 0 {
		return false
	}

	for _, msg := range msgs {
		if (msg.Role != "user" && msg.Role != "assistant") || nonNull(msg.Content) == nil {
			return false
		}
	}

	return true
}

// URI templates of resources.

// templateVar matches one RFC 6570 expression, e.g. {id}, {+path}, {?a,b}.
var templateVar = regexp.MustCompile(`\{([+#.;/?&]?)([^{}]*)\}`)

// uriTemplate is a compiled resource template.
type uriTemplate struct {
	re   *regexp.Regexp
	vars []string
}

// compileTemplate turns a URI template (level 1 {var}, {+var} for reserved
// characters and {?query} lists) into a matcher.
func compileTemplate(tpl string) (*uriTemplate, error) {
	matches := templateVar.FindAllStringSubmatchIndex(tpl, -1)
	if len(matches) == 0 || strings.Count(tpl, "{") != len(matches) || strings.Count(tpl, "}") != len(matches) {
		return nil, errBadTemplate
	}

	var (
		pattern strings.Builder
		vars    []string
		last    int
	)

	pattern.WriteString("^")

	for _, loc := range matches {
		pattern.WriteString(regexp.QuoteMeta(tpl[last:loc[0]]))

		op, names := tpl[loc[2]:loc[3]], tpl[loc[4]:loc[5]]
		if names == "" {
			return nil, errBadTemplate
		}

		switch op {
		case "":
			pattern.WriteString(`([^/?#]+)`)

			vars = append(vars, names)
		case "+", "#":
			pattern.WriteString(`(.+)`)

			vars = append(vars, names)
		default:
			pattern.WriteString(`(?:[?&#].*)?`)
		}

		last = loc[1]
	}

	pattern.WriteString(regexp.QuoteMeta(tpl[last:]) + "$")

	re, err := regexp.Compile(pattern.String())
	if err != nil {
		return nil, errBadTemplate
	}

	return &uriTemplate{re: re, vars: vars}, nil
}

// match returns the variable values when uri fits the template.
func (t *uriTemplate) match(uri string) (map[string]string, bool) {
	sub := t.re.FindStringSubmatch(uri)
	if sub == nil {
		return nil, false
	}

	values := make(map[string]string, len(t.vars))
	for i, name := range t.vars {
		values[name] = sub[i+1]
	}

	return values, true
}

// Resource listing and reading.

// resourceInfo is the resources/list entry of a resource or template.
func resourceInfo(res *manifestResource) map[string]any {
	info := map[string]any{"name": orDefault(res.Name, orDefault(res.URI, res.URITemplate))}

	if res.URITemplate != "" {
		info["uriTemplate"] = res.URITemplate
	} else {
		info[keyURI] = res.URI
	}

	for k, v := range map[string]string{keyTitle: res.Title, keyDescription: res.Description, keyMimeType: res.MimeType} {
		if v != "" {
			info[k] = v
		}
	}

	if ann := nonNull(res.Annotations); ann != nil {
		info["annotations"] = ann
	}

	return info
}

func listResources(m *manifest, r *jsonrpcRequest, _ *callState) ([]byte, error) {
	list := make([]map[string]any, 0, len(m.Resources))
	for i := range m.Resources {
		list = append(list, resourceInfo(&m.Resources[i]))
	}

	return respond(r.ID, map[string]any{"resources": list})
}

func listResourceTemplates(m *manifest, r *jsonrpcRequest, _ *callState) ([]byte, error) {
	list := make([]map[string]any, 0, len(m.Templates))
	for i := range m.Templates {
		list = append(list, resourceInfo(&m.Templates[i]))
	}

	return respond(r.ID, map[string]any{"resourceTemplates": list})
}

// readResource answers resources/read: an exact URI first, then the first
// template the URI fits (its variables fill {{name}} in the content).
func readResource(m *manifest, r *jsonrpcRequest, _ *callState) ([]byte, error) {
	var p struct {
		URI string `json:"uri"`
	}

	err := json.Unmarshal(r.Params, &p)
	if err != nil || p.URI == "" {
		return respondErr(r.ID, codeInvalidParams, "resources/read requires params.uri")
	}

	for i := range m.Resources {
		if m.Resources[i].URI == p.URI {
			return respond(r.ID, map[string]any{"contents": []any{resourceContent(&m.Resources[i], p.URI, nil)}})
		}
	}

	for i := range m.Templates {
		tpl, err := compileTemplate(m.Templates[i].URITemplate)
		if err != nil {
			continue
		}

		if vars, ok := tpl.match(p.URI); ok {
			return respond(r.ID, map[string]any{"contents": []any{resourceContent(&m.Templates[i], p.URI, vars)}})
		}
	}

	return respondErrData(r.ID, codeResourceNotFound, "Resource not found", map[string]string{keyURI: p.URI})
}

// resourceContent builds the contents entry for one resource.
func resourceContent(res *manifestResource, uri string, vars map[string]string) map[string]any {
	out := map[string]any{keyURI: uri}
	if res.MimeType != "" {
		out[keyMimeType] = res.MimeType
	}

	if res.Blob != "" {
		out["blob"] = res.Blob

		return out
	}

	text := resourceText(res)
	if vars != nil && res.Text == nil {
		text = string(mustMarshal(vars))
	}

	out[keyText] = replacer(vars).Replace(text)

	return out
}

// resourceText returns the text of a resource: a JSON string as is, any
// other JSON value as compact JSON, nothing as the empty string.
func resourceText(res *manifestResource) string {
	raw := nonNull(res.Text)
	if raw == nil {
		return ""
	}

	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return string(raw)
	}

	return buf.String()
}

// replacer substitutes {{name}} placeholders with the given values.
func replacer(values map[string]string) *strings.Replacer {
	pairs := make([]string, 0, len(values))
	for k, v := range values {
		pairs = append(pairs, "{{"+k+"}}", v)
	}

	return strings.NewReplacer(pairs...)
}

// Prompt listing and rendering.

func listPrompts(m *manifest, r *jsonrpcRequest, _ *callState) ([]byte, error) {
	list := make([]usecase.MCPPrompt, 0, len(m.Prompts))
	for i := range m.Prompts {
		list = append(list, m.Prompts[i].info())
	}

	return respond(r.ID, map[string]any{"prompts": list})
}

func (p *manifestPrompt) info() usecase.MCPPrompt {
	return usecase.MCPPrompt{Name: p.Name, Title: p.Title, Description: p.Description, Arguments: p.Arguments}
}

// getPrompt answers prompts/get: the prompt's messages with {{argument}}
// placeholders filled in.
func getPrompt(m *manifest, r *jsonrpcRequest, _ *callState) ([]byte, error) {
	var p struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}

	err := json.Unmarshal(r.Params, &p)
	if err != nil || p.Name == "" {
		return respondErr(r.ID, codeInvalidParams, "prompts/get requires params.name")
	}

	prompt := m.findPrompt(p.Name)
	if prompt == nil {
		return respondErr(r.ID, codeInvalidParams, "unknown prompt: "+p.Name)
	}

	for _, a := range prompt.Arguments {
		if _, given := p.Arguments[a.Name]; a.Required && !given {
			return respondErr(r.ID, codeInvalidParams, "missing required argument: "+a.Name)
		}
	}

	rep := replacer(p.Arguments)
	messages := make([]map[string]any, 0, len(prompt.Messages))

	for _, msg := range prompt.Messages {
		messages = append(messages, map[string]any{"role": msg.Role, keyContent: promptContent(msg.Content, rep)})
	}

	result := map[string]any{"messages": messages}
	if prompt.Description != "" {
		result[keyDescription] = prompt.Description
	}

	return respond(r.ID, result)
}

// findPrompt returns the prompt with the given name (nil when unknown).
func (m *manifest) findPrompt(name string) *manifestPrompt {
	for i := range m.Prompts {
		if m.Prompts[i].Name == name {
			return &m.Prompts[i]
		}
	}

	return nil
}

// promptContent turns a message's content into an MCP content object and
// substitutes the placeholders in all its strings.
func promptContent(raw json.RawMessage, rep *strings.Replacer) any {
	var v any

	err := json.Unmarshal(raw, &v)
	if err != nil {
		return map[string]any{keyType: keyText, keyText: string(raw)}
	}

	if s, ok := v.(string); ok {
		return map[string]any{keyType: keyText, keyText: rep.Replace(s)}
	}

	return substitute(v, rep)
}

// substitute applies rep to every string inside v.
func substitute(v any, rep *strings.Replacer) any {
	switch x := v.(type) {
	case string:
		return rep.Replace(x)
	case []any:
		for i := range x {
			x[i] = substitute(x[i], rep)
		}

		return x
	case map[string]any:
		for k := range x {
			x[k] = substitute(x[k], rep)
		}

		return x
	default:
		return v
	}
}

// Catalog for the web UI.

// Catalog describes everything the manifest exposes.
func (e *Engine) Catalog(raw []byte) (usecase.MCPCatalog, error) {
	m, err := parse(raw)
	if err != nil {
		return usecase.MCPCatalog{}, err
	}

	cat := usecase.MCPCatalog{
		Tools:     m.tools(),
		Resources: make([]usecase.MCPResource, 0, len(m.Resources)),
		Templates: make([]usecase.MCPResource, 0, len(m.Templates)),
		Prompts:   make([]usecase.MCPPrompt, 0, len(m.Prompts)),
	}

	for i := range m.Resources {
		cat.Resources = append(cat.Resources, catalogResource(&m.Resources[i]))
	}

	for i := range m.Templates {
		cat.Templates = append(cat.Templates, catalogResource(&m.Templates[i]))
	}

	for i := range m.Prompts {
		cat.Prompts = append(cat.Prompts, m.Prompts[i].info())
	}

	return cat, nil
}

func catalogResource(res *manifestResource) usecase.MCPResource {
	return usecase.MCPResource{
		URI: res.URI, URITemplate: res.URITemplate, Name: orDefault(res.Name, orDefault(res.URI, res.URITemplate)),
		Title: res.Title, Description: res.Description, MimeType: res.MimeType,
	}
}
