package main

import (
	"fmt"
)

// demoPets are the pets of the built-in Petstore when "serve --demo" starts.
const demoPets = `[
  {"id": 1, "name": "Rex", "tag": "dog", "status": "available"},
  {"id": 2, "name": "Tom", "tag": "cat", "status": "pending"},
  {"id": 3, "name": "Nemo", "tag": "fish", "status": "sold"}
]`

// seedDemo fills the pets collection of the demo project. A collection that
// already holds pets (a --data-dir that was used before) is left alone.
func seedDemo(a *app, projectID string) error {
	snapshot, err := a.services.State.Snapshot(projectID)
	if err != nil {
		return fmt.Errorf("read demo state: %w", err)
	}

	if len(snapshot["pets"]) > 0 {
		return nil
	}

	_, err = a.services.State.Seed(projectID, "pets", []byte(demoPets))
	if err != nil {
		return fmt.Errorf("seed demo pets: %w", err)
	}

	return nil
}
