// Package notifier provides a simple AgentNotifier implementation that
// logs generation events using the standard library log package.
package notifier

import (
	"context"
	"encoding/json"
	"log"

	"github.com/Eisenmann/openapi-mocker/internal/agents"
)

// Notifier logs agent events as JSON.
type Notifier struct{}

// New creates a new notifier.
func New() *Notifier { return &Notifier{} }

// Notify logs the event as JSON.
func (n *Notifier) Notify(_ context.Context, event agents.AgentEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}

	log.Printf("AGENT EVENT: %s", data)

	return nil
}
