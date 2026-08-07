// Package webui embeds the static files of the web interface into the binary
// (Frameworks & Drivers / delivery in Clean Architecture terms) — the entire
// UI is just an HTTP client of the REST API in internal/adapter/httpapi; it
// has no knowledge of the usecase layer.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed static
var staticFS embed.FS

// FS returns a file system with the contents of static/ (without the
// "static/" prefix), suitable for http.FileServer(http.FS(FS())).
func FS() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
