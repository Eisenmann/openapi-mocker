package domain

import (
	"errors"
	"fmt"
	"time"
)

// ErrLoadPlanNotFound is returned for an unknown stored plan or version; it
// is also a ErrNotFound.
var ErrLoadPlanNotFound = fmt.Errorf("load plan: %w", ErrNotFound)

// ErrLoadPlanID is returned for a malformed plan id.
var ErrLoadPlanID = errors.New("invalid load plan id")

// StoredPlan is one version of a plan kept with a project. Every save makes a
// new version, like contracts; older versions stay readable so a run can say
// exactly which plan it used.
type StoredPlan struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	// Name is the plan's own name, copied up for lists.
	Name    string `json:"name"`
	Version int    `json:"version"`
	// Latest is the newest version number; set when a plan is read.
	Latest    int       `json:"latest,omitempty"`
	Plan      Plan      `json:"plan"`
	CreatedAt time.Time `json:"createdAt"`
	CreatedBy string    `json:"createdBy,omitempty"`
}
