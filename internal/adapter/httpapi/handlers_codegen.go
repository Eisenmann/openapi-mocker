package httpapi

import "net/http"

func (a *api) codegenServer(w http.ResponseWriter, r *http.Request) {
	zipBytes, err := a.s.Codegen.GenerateServerZip(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeZip(w, "server.zip", zipBytes)
}

func (a *api) codegenClient(w http.ResponseWriter, r *http.Request) {
	zipBytes, err := a.s.Codegen.GenerateClientZip(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeZip(w, "client.zip", zipBytes)
}

func writeZip(w http.ResponseWriter, filename string, data []byte) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	if _, err := w.Write(data); err != nil {
		http.Error(w, "failed to write response", http.StatusInternalServerError)
	}
}

func (a *api) listLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.s.Logs.List(r.PathValue("id"), 100))
}
