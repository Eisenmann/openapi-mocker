package exchange

import (
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

var (
	templateVar = regexp.MustCompile(`\{\{\s*(?:_\.)?([^}\s]+)\s*\}\}`)
	badName     = regexp.MustCompile(`[^A-Za-z0-9_.-]`)
	allDigits   = regexp.MustCompile(`^\d+$`)
	uuidSeg     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	hexSeg      = regexp.MustCompile(`^[0-9a-fA-F]{16,}$`)
)

// splitRequestURL reduces a URL as Postman or Insomnia store it (which may
// start with a {{baseUrl}} variable and hold {{variables}} anywhere) to its
// templated path and the names of its query parameters. A {{variable}} or
// :name in the path becomes a {parameter}.
func splitRequestURL(raw string) (path string, query []string) {
	raw = strings.TrimSpace(raw)
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		raw = raw[:i]
	}

	raw, queryText, _ := strings.Cut(raw, "?")

	raw = stripOrigin(raw)
	path = templatePathSegments(raw)

	for _, pair := range strings.Split(queryText, "&") {
		name, _, _ := strings.Cut(pair, "=")
		if name = templateVar.ReplaceAllString(name, "$1"); name != "" {
			query = append(query, name)
		}
	}

	return path, query
}

// stripOrigin removes a leading scheme://host[:port] or {{variable}}[:port].
func stripOrigin(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 && !strings.ContainsAny(raw[:i], "/{") {
		rest := raw[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			return rest[j:]
		}

		return "/"
	}

	if strings.HasPrefix(raw, "{{") {
		if end := strings.Index(raw, "}}"); end >= 0 {
			raw = raw[end+2:]

			if strings.HasPrefix(raw, ":") { // {{host}}:8080/path.
				if j := strings.IndexByte(raw, '/'); j >= 0 {
					return raw[j:]
				}

				return "/"
			}
		}
	}

	return raw
}

// templatePathSegments turns ":id", "{{id}}" and "{id}" segments into {id}.
func templatePathSegments(path string) string {
	if path == "" {
		return "/"
	}

	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i, seg := range segments {
		if strings.HasPrefix(seg, ":") && len(seg) > 1 {
			segments[i] = "{" + cleanName(seg[1:]) + "}"

			continue
		}

		segments[i] = templateVar.ReplaceAllStringFunc(seg, func(m string) string {
			return "{" + cleanName(templateVar.FindStringSubmatch(m)[1]) + "}"
		})
	}

	return "/" + strings.Join(segments, "/")
}

func cleanName(name string) string {
	name = badName.ReplaceAllString(strings.TrimSpace(name), "_")
	if name == "" {
		return "param"
	}

	return name
}

// templateObservedPath turns the path of a recorded request ("/users/42/posts")
// into a template ("/users/{userId}/posts"): numeric, UUID and long hex
// segments are identifiers. types gives the JSON type of each parameter.
func templateObservedPath(path string) (templated string, types map[string]string) {
	types = map[string]string{}

	if path == "" || path == "/" {
		return "/", types
	}

	segments := strings.Split(strings.Trim(path, "/"), "/")
	used := map[string]bool{}
	prev := ""

	for i, seg := range segments {
		decoded, err := url.PathUnescape(seg)
		if err != nil {
			decoded = seg
		}

		switch {
		case allDigits.MatchString(decoded) && len(decoded) < 19:
			segments[i] = newParam(prev, used, types, "integer")
		case uuidSeg.MatchString(decoded), hexSeg.MatchString(decoded), allDigits.MatchString(decoded):
			segments[i] = newParam(prev, used, types, "string")
		default:
			prev = decoded
		}
	}

	return "/" + strings.Join(segments, "/"), types
}

// newParam names a path parameter after the segment before it ("users" gives
// {userId}), or {id}, and keeps the names of one path unique.
func newParam(prev string, used map[string]bool, types map[string]string, typ string) string {
	base := "id"
	if s := singular(prev); s != "" {
		base = s + "Id"
	}

	name := base
	for n := 2; used[name]; n++ {
		name = base + string(rune('0'+n%10))
		if n > 9 { //nolint:mnd // more than nine identifiers in one path is unrealistic.
			name = base + "X" + string(rune('a'+n%26))
		}
	}

	used[name] = true
	types[name] = typ

	return "{" + name + "}"
}

func singular(word string) string {
	word = badName.ReplaceAllString(strings.ToLower(word), "")

	switch {
	case word == "":
		return ""
	case strings.HasSuffix(word, "ies") && len(word) > 4:
		return word[:len(word)-3] + "y"
	case strings.HasSuffix(word, "s") && !strings.HasSuffix(word, "ss") && len(word) > 3:
		return word[:len(word)-1]
	default:
		return word
	}
}

// endpointIndex collects imported endpoints, merging the ones that share a
// method and path.
type endpointIndex struct {
	byKey map[string]int
	list  []usecase.ImportedEndpoint
}

func newEndpointIndex() *endpointIndex {
	return &endpointIndex{byKey: map[string]int{}, list: nil}
}

// get returns the endpoint for method and path, creating it when new.
func (x *endpointIndex) get(method, path string) *usecase.ImportedEndpoint {
	key := strings.ToUpper(method) + " " + path

	if i, ok := x.byKey[key]; ok {
		return &x.list[i]
	}

	x.byKey[key] = len(x.list)
	x.list = append(x.list, usecase.ImportedEndpoint{
		Method: strings.ToUpper(method), Path: path, Summary: "", PathTypes: map[string]string{},
		Query: nil, Request: nil, Responses: nil,
	})

	return &x.list[len(x.list)-1]
}

func (x *endpointIndex) endpoints() []usecase.ImportedEndpoint {
	return x.list
}

// addQuery records query parameter names without duplicates.
func addQuery(ep *usecase.ImportedEndpoint, names []string) {
	have := map[string]bool{}
	for _, q := range ep.Query {
		have[q] = true
	}

	for _, name := range names {
		if !have[name] && name != "" {
			ep.Query = append(ep.Query, name)
			have[name] = true
		}
	}

	sort.Strings(ep.Query)
}
