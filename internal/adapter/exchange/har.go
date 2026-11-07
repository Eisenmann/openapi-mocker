package exchange

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

type harFile struct {
	Log struct {
		Entries []harEntry `json:"entries"`
	} `json:"log"`
}

type harEntry struct {
	Request struct {
		Method      string `json:"method"`
		URL         string `json:"url"`
		QueryString []struct {
			Name string `json:"name"`
		} `json:"queryString"`
		PostData struct {
			MimeType string `json:"mimeType"`
			Text     string `json:"text"`
		} `json:"postData"`
	} `json:"request"`
	Response struct {
		Status  int `json:"status"`
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"headers"`
		Content struct {
			MimeType string `json:"mimeType"`
			Text     string `json:"text"`
			Encoding string `json:"encoding"`
		} `json:"content"`
	} `json:"response"`
}

// parseHAR reads the API calls of a HAR file. Page assets (images, scripts,
// stylesheets, HTML) and CORS preflights are skipped.
func parseHAR(data []byte) (*usecase.ImportedAPI, error) {
	var file harFile

	err := json.Unmarshal(data, &file)
	if err != nil {
		return nil, fmt.Errorf("invalid HAR file: %w", err)
	}

	idx := newEndpointIndex()
	hosts := map[string]bool{}
	skipped := 0

	for i := range file.Log.Entries {
		e := &file.Log.Entries[i]

		if !addHAREntry(idx, hosts, e) {
			skipped++
		}
	}

	api := &usecase.ImportedAPI{Name: harName(hosts), Endpoints: idx.endpoints(), Warnings: nil}

	if skipped > 0 {
		api.Warnings = append(api.Warnings, fmt.Sprintf("skipped %d entries that are not API calls", skipped))
	}

	if len(hosts) > 1 {
		api.Warnings = append(api.Warnings, fmt.Sprintf("entries of %d hosts were merged; only paths are kept", len(hosts)))
	}

	return api, nil
}

// addHAREntry records one entry and reports whether it was an API call.
func addHAREntry(idx *endpointIndex, hosts map[string]bool, e *harEntry) bool {
	u, err := url.Parse(e.Request.URL)
	if err != nil || u.Host == "" || !isAPICall(e) {
		return false
	}

	hosts[u.Host] = true

	path, types := templateObservedPath(u.Path)
	ep := idx.get(e.Request.Method, path)

	for name, typ := range types {
		if prev, seen := ep.PathTypes[name]; seen && prev != typ {
			typ = "string" // samples of different types: only a string fits all.
		}

		ep.PathTypes[name] = typ
	}

	addQuery(ep, queryNames(u))

	if ep.Request == nil && strings.TrimSpace(e.Request.PostData.Text) != "" {
		ep.Request = &usecase.ImportedBody{ContentType: e.Request.PostData.MimeType, Text: e.Request.PostData.Text}
	}

	ep.Responses = append(ep.Responses, harResponse(e))

	return true
}

func harName(hosts map[string]bool) string {
	if len(hosts) == 1 {
		for h := range hosts {
			return h
		}
	}

	return "Imported HAR"
}

func queryNames(u *url.URL) []string {
	query := u.Query()
	names := make([]string, 0, len(query))

	for name := range query {
		names = append(names, name)
	}

	return names
}

// isAPICall keeps entries that look like API traffic: a real response, and a
// JSON, XML or plain-text body or none at all.
func isAPICall(e *harEntry) bool {
	if e.Request.Method == http.MethodOptions || e.Response.Status < 100 || e.Response.Status == http.StatusNotModified {
		return false
	}

	mime := strings.ToLower(e.Response.Content.MimeType)

	return mime == "" || strings.Contains(mime, "json") || strings.Contains(mime, "xml") ||
		strings.HasPrefix(mime, "text/plain")
}

func harResponse(e *harEntry) usecase.ImportedResponse {
	body := e.Response.Content.Text

	if e.Response.Content.Encoding == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(body)
		if err == nil && utf8.Valid(decoded) {
			body = string(decoded)
		} else {
			body = ""
		}
	}

	headers := map[string][]string{}

	for _, h := range e.Response.Headers {
		name := http.CanonicalHeaderKey(h.Name)
		headers[name] = append(headers[name], h.Value)
	}

	contentType := firstHeader(headers, headerContentType)
	if contentType == "" {
		contentType = e.Response.Content.MimeType
	}

	delete(headers, headerContentType)

	return usecase.ImportedResponse{
		Status: e.Response.Status, ContentType: contentType, Headers: headers, Body: body,
	}
}
