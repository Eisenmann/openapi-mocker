package graphql_test

import (
	"fmt"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/graphql"
)

// TestSchemaCacheBounded verifies the LRU cache evicts old entries when the
// cap is reached, preventing unbounded memory growth.
func TestSchemaCacheBounded(t *testing.T) {
	t.Parallel()

	// Use a single engine so the cache is shared across all schemas.
	e := graphql.NewEngine()

	for i := range graphql.MaxCachedSchemas + 10 {
		schema := fmt.Sprintf("type Query { f%d: String }", i)

		query := fmt.Sprintf("{ f%d }", i)

		_, err := e.Execute([]byte(schema), query, "", nil)
		if err != nil {
			t.Fatalf("schema %d failed: %v", i, err)
		}
	}

	if got := e.CachedSchemaCount(); got > graphql.MaxCachedSchemas {
		t.Errorf("cache grew beyond cap: %d entries (cap %d)",
			got, graphql.MaxCachedSchemas)
	}
}
