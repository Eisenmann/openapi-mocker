// Package domain contains the application's business entities (Entities in
// Clean Architecture terms) — the innermost layer of the application. The
// package imports nothing but the standard library: not net/http, not
// kin-openapi, not file storage. This is the single rule that matters here —
// entities must not know how they are serialized, stored, or served over the
// network.
//
// Pragmatic caveat: the structs carry encoding/json tags. Formally that is a
// serialization detail too, but encoding/json is part of the Go standard library
// rather than a swappable framework, so this compromise is common in Go
// adaptations of Clean Architecture and does not create a dependency from the
// domain to any particular transport (HTTP handlers still dictate nothing to
// the domain).
package domain

import "errors"

// ErrNotFound is the single "entity not found" error shared by all repositories.
// The HTTP layer decides how to map it to a status code (404) — the domain
// itself knows nothing about HTTP.
var ErrNotFound = errors.New("entity not found")
