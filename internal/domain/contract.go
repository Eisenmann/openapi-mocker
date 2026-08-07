package domain

import "time"

// Contract is a single immutable version of a project's OpenAPI contract.
// The full version history is stored (see usecase.ContractRepository):
// publishing a new version and rolling back to an old one both merely append
// an entry to the history; it is never rewritten.
type Contract struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	Format    string    `json:"format"` // yaml | json.
	Raw       string    `json:"raw"`
	Version   int       `json:"version"`
	Source    string    `json:"source"` // upload | manual | generated | rollback-to-vN.
	CreatedAt time.Time `json:"createdAt"`
}
