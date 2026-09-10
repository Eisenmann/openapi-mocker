package httpapi

import (
	"errors"
	"net/http"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

// serveGraphQL executes a GraphQL operation against the project's schema.
// Standard GraphQL over HTTP: POST with JSON body
// { "query": "...", "operationName": "...", "variables": {...} }.
func (a *api) serveGraphQL(w http.ResponseWriter, r *http.Request) {
	if a.s.GraphQLServing == nil {
		writeError(w, http.StatusNotFound, errGraphQLNotEnabled)
		return
	}

	projectID := r.PathValue("projectId")

	var body struct {
		Query         string         `json:"query"`
		OperationName string         `json:"operationName"`
		Variables     map[string]any `json:"variables"`
	}

	err := readJSON(r, &body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	if body.Query == "" {
		writeError(w, http.StatusBadRequest, errQueryRequired)
		return
	}

	resp, err := a.s.GraphQLServing.Serve(projectID, body.Query,
		body.OperationName, body.Variables)
	if err != nil {
		// GraphQL query errors (parse/validation) are returned with HTTP 200
		// and an "errors" array per GraphQL-over-HTTP.
		var qe *usecase.GraphQLQueryError
		if errors.As(err, &qe) {
			writeJSON(w, http.StatusOK, map[string]any{
				"errors": qe.Errors,
			})

			return
		}
		// Server-side failures: wrong project, not a GraphQL contract,
		// schema load failure — these are request-independent and should
		// not masquerade as 200. The status mapping lives in usecase so the
		// request logger and the response writer agree.
		writeError(w, usecase.StatusFromError(err), err)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resp)
}

func (a *api) graphQLSchema(w http.ResponseWriter, r *http.Request) {
	if a.s.GraphQLServing == nil {
		writeError(w, http.StatusNotFound, errGraphQLNotEnabled)
		return
	}

	sdl, err := a.s.GraphQLServing.Schema(r.PathValue("id"))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"schema": sdl})
}

func (a *api) graphQLOperations(w http.ResponseWriter, r *http.Request) {
	if a.s.GraphQLServing == nil {
		writeError(w, http.StatusNotFound, errGraphQLNotEnabled)
		return
	}

	ops, err := a.s.GraphQLServing.Operations(r.PathValue("id"))
	if err != nil {
		writeError(w, usecase.StatusFromError(err), err)
		return
	}

	writeJSON(w, http.StatusOK, ops)
}
