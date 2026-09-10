package httpapi

import "net/http"

func (a *api) listProjects(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.s.Projects.List())
}

func (a *api) createProject(w http.ResponseWriter, r *http.Request) {
	var body struct{ Name, Description string }

	err := readJSON(r, &body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	p, err := a.s.Projects.Create(body.Name, body.Description)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusCreated, p)
}

func (a *api) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := a.s.Projects.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	writeJSON(w, http.StatusOK, p)
}

func (a *api) deleteProject(w http.ResponseWriter, r *http.Request) {
	err := a.s.Projects.Delete(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
