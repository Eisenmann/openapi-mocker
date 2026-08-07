package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func (a *api) getContract(w http.ResponseWriter, r *http.Request) {
	c, err := a.s.Contracts.GetActive(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *api) listContractVersions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.s.Contracts.ListVersions(r.PathValue("id")))
}

func (a *api) getContractVersion(w http.ResponseWriter, r *http.Request) {
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid version number"))
		return
	}
	c, err := a.s.Contracts.GetVersion(r.PathValue("id"), version)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// saveContract publishes the contract: JSON {"raw": "..."} or a multipart
// file (field "file"). Publishing makes endpoints live immediately — see
// usecase.ContractService.Publish.
func (a *api) saveContract(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	var raw, source string

	if ct := r.Header.Get("Content-Type"); strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("file not provided (field 'file')"))
			return
		}
		defer file.Close()
		b, err := io.ReadAll(file)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		raw, source = string(b), "upload"
	} else {
		var body struct{ Raw, Source string }
		if err := readJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		raw, source = body.Raw, body.Source
	}

	c, err := a.s.Contracts.Publish(projectID, raw, source)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (a *api) rollbackContract(w http.ResponseWriter, r *http.Request) {
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid version number"))
		return
	}
	c, err := a.s.Contracts.Rollback(r.PathValue("id"), version)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (a *api) diffContract(w http.ResponseWriter, r *http.Request) {
	fromVersion, err := strconv.Atoi(r.URL.Query().Get("from"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("from parameter is required and must be a number"))
		return
	}
	var toVersion *int
	if toStr := r.URL.Query().Get("to"); toStr != "" {
		v, err := strconv.Atoi(toStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("to parameter must be a number"))
			return
		}
		toVersion = &v
	}
	diff, err := a.s.Contracts.Diff(r.PathValue("id"), fromVersion, toVersion)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, diff)
}

func (a *api) validateContract(w http.ResponseWriter, r *http.Request) {
	var body struct{ Raw string }
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, a.s.Contracts.Validate(body.Raw))
}

func (a *api) listEndpoints(w http.ResponseWriter, r *http.Request) {
	endpoints, err := a.s.Contracts.ListEndpoints(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("contract has not been loaded yet"))
		return
	}
	writeJSON(w, http.StatusOK, endpoints)
}

func (a *api) generateContract(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProviderID  string `json:"providerId"`
		Description string `json:"description"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	raw, warning, err := a.s.Contracts.GenerateFromDescription(r.Context(), body.ProviderID, body.Description)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"raw": raw, "warning": warning})
}
