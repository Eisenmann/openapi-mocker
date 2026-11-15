// Package examples embeds the example contracts that the binary can serve
// without any file on disk.
package examples

import _ "embed"

// Petstore is the Petstore contract that "openapi-mocker serve --demo" serves.
//
//go:embed petstore.yaml
var Petstore string
