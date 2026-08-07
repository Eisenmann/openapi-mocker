// Package openapi is the interface adapter (Interface Adapter in Clean
// Architecture terms) encapsulating a specific OpenAPI parsing library
// (kin-openapi).
// It exposes only Engine, implemented with usecase-layer primitive types
// (usecase.Endpoint, usecase.DiffLine, etc.) — no kin-openapi type ever
// leaves this package.
package openapi

import (
	"context"
	"fmt"

	"github.com/getkin/kin-openapi/openapi3"
)

func parse(raw []byte) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	doc, err := loader.LoadFromData(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse contract: %w", err)
	}
	return doc, nil
}

func parseAndValidate(raw []byte) (*openapi3.T, error) {
	doc, err := parse(raw)
	if err != nil {
		return nil, err
	}
	if err := doc.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("contract failed validation: %w", err)
	}
	return doc, nil
}
