package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

var (
	errImportTooLarge = errors.New("the file is too large")
	errUnknownFormat  = errors.New("unknown format")
	slugUnsafe        = regexp.MustCompile(`[^a-z0-9]+`)
)

func (a *api) registerExchangeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/import", a.importProject)
	mux.HandleFunc("GET /api/projects/{id}/export", a.exportProject)
	mux.HandleFunc("GET /api/projects/{id}/contract/export", a.exportContract)
}

// importProject creates a project from an uploaded file: the request body is
// the file itself (or a multipart form with a "file" field). The project name
// comes from the optional name query parameter, else from the file.
func (a *api) importProject(w http.ResponseWriter, r *http.Request) {
	data, err := readUpload(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	//nolint:contextcheck // alerting runs detached from the request.
	res, err := a.s.Exchange.Import(data, r.URL.Query().Get("name"))
	if err != nil {
		writeError(w, exchangeStatus(err), err)
		return
	}

	writeJSON(w, http.StatusCreated, res)
}

func readUpload(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, usecase.MaxImportBytes)

	var reader io.Reader = r.Body

	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		file, _, err := r.FormFile("file")
		if err != nil {
			return nil, errFileNotProvided
		}
		defer file.Close()

		reader = file
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, errImportTooLarge
		}

		return nil, fmt.Errorf("read upload: %w", err)
	}

	return data, nil
}

// exportProject downloads the project as one file: format=bundle (default,
// history=1 adds every contract version), postman or insomnia.
func (a *api) exportProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	p, err := a.s.Projects.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	var (
		body     []byte
		filename string
	)

	switch format := r.URL.Query().Get("format"); format {
	case "", "bundle":
		body, err = a.bundleJSON(id, r.URL.Query().Get("history") == "1")
		filename = slug(p.Name) + ".openapi-mocker.json"
	case usecase.CollectionPostman:
		body, err = a.s.Exchange.ExportCollection(id, format, mockBaseURL(r, id))
		filename = slug(p.Name) + ".postman_collection.json"
	case usecase.CollectionInsomnia:
		body, err = a.s.Exchange.ExportCollection(id, format, mockBaseURL(r, id))
		filename = slug(p.Name) + ".insomnia.json"
	default:
		writeError(w, http.StatusBadRequest, fmt.Errorf("%w: %q (use bundle, postman or insomnia)", errUnknownFormat, format))
		return
	}

	if err != nil {
		writeError(w, exchangeStatus(err), err)
		return
	}

	download(w, filename, "application/json", body)
}

func (a *api) bundleJSON(projectID string, history bool) ([]byte, error) {
	bundle, err := a.s.Exchange.ExportBundle(projectID, history)
	if err != nil {
		return nil, err
	}

	body, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode bundle: %w", err)
	}

	return body, nil
}

// exportContract downloads the active contract converted to format=openapi3
// (default), openapi31 or swagger2, as=yaml (default) or json.
func (a *api) exportContract(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	target := r.URL.Query().Get("format")

	if target == "" {
		target = usecase.ExportOpenAPI3
	}

	encoding := r.URL.Query().Get("as")
	if encoding == "" {
		encoding = "yaml"
	}

	if encoding != "yaml" && encoding != "json" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("%w: as must be yaml or json", errUnknownFormat))
		return
	}

	p, err := a.s.Projects.Get(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	out, err := a.s.Contracts.Export(id, target, encoding)
	if err != nil {
		writeError(w, exchangeStatus(err), err)
		return
	}

	download(w, slug(p.Name)+"."+target+"."+encoding, "text/plain; charset=utf-8", []byte(out))
}

// exchangeStatus is the HTTP status of an import or export error.
func exchangeStatus(err error) int {
	return usecase.StatusFromError(err)
}

func download(w http.ResponseWriter, filename, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// mockBaseURL is where this server answers for the project's mock.
func mockBaseURL(r *http.Request, projectID string) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}

	return scheme + "://" + r.Host + "/mock/" + projectID
}

func slug(name string) string {
	s := strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if s == "" {
		return "project"
	}

	return s
}
