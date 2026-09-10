package notifier

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/agents"
)

func TestNew(t *testing.T) {
	n := New()
	if n == nil {
		t.Fatal("expected non-nil notifier")
	}
}

func TestNotify(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	n := New()

	event := agents.AgentEvent{
		Type:    "generation.completed",
		Payload: map[string]interface{}{"language": "go", "files": 3},
	}

	err := n.Notify(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "AGENT EVENT") {
		t.Error("expected AGENT EVENT prefix")
	}
	if !strings.Contains(output, "generation.completed") {
		t.Error("expected event type in output")
	}
	if !strings.Contains(output, "go") {
		t.Error("expected language in output")
	}
}

func TestNotify_WithNilPayload(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)

	n := New()

	event := agents.AgentEvent{
		Type:    "generation.planned",
		Payload: nil,
	}

	err := n.Notify(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "generation.planned") {
		t.Error("expected event type in output")
	}
}
