package domain

import "time"

// Project is an isolated workspace: one active OpenAPI contract + a set of
// mock rules served at /mock/{projectId}/...
type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
