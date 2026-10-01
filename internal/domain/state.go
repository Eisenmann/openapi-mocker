package domain

import "encoding/json"

// StateItem is one stored resource of a stateful collection. Data is the
// resource as a JSON object (including its id field); ID is the id rendered
// as the string that appears in the URL.
type StateItem struct {
	ID   string          `json:"id"`
	Data json.RawMessage `json:"data"`
}

// StateCollection is an ordered set of resources, e.g. everything under
// /users. NextID feeds sequential integer ids.
type StateCollection struct {
	Items  []StateItem `json:"items"`
	NextID int         `json:"nextId"`
}
