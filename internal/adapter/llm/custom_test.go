package llm

import (
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func TestRenderBodyTemplate(t *testing.T) {
	t.Parallel()

	req := usecase.ChatRequest{
		SystemPrompt: "You are a helper",
		UserPrompt:   "Hello world",
	}

	t.Run("default template", func(t *testing.T) {
		t.Parallel()

		rendered, err := renderBodyTemplate("", "gpt-4", req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if rendered == "" {
			t.Fatal("expected non-empty rendered body")
		}
	})

	t.Run("custom template with placeholders", func(t *testing.T) {
		t.Parallel()

		tpl := `{"model":"{{MODEL}}","system":"{{SYSTEM}}","user":"{{USER}}"}`

		rendered, err := renderBodyTemplate(tpl, "claude", req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if rendered != `{"model":"claude","system":"You are a helper","user":"Hello world"}` {
			t.Errorf("unexpected rendered: %q", rendered)
		}
	})

	t.Run("json placeholders", func(t *testing.T) {
		t.Parallel()

		tpl := `{"system":{{SYSTEM_JSON}},"user":{{USER_JSON}}}`

		rendered, err := renderBodyTemplate(tpl, "model", req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if rendered != `{"system":"You are a helper","user":"Hello world"}` {
			t.Errorf("unexpected rendered: %q", rendered)
		}
	})
}

func TestExtractCustomResponse(t *testing.T) {
	t.Parallel()

	t.Run("valid path", func(t *testing.T) {
		t.Parallel()

		body := []byte(`{"choices":[{"message":{"content":"hello"}}]}`)

		got, err := extractCustomResponse(body, "choices.0.message.content")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got != "hello" {
			t.Errorf("expected 'hello', got %q", got)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		t.Parallel()

		_, err := extractCustomResponse([]byte("not json"), "a.b")
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})

	t.Run("field not found", func(t *testing.T) {
		t.Parallel()

		_, err := extractCustomResponse([]byte(`{"a": 1}`), "b.c")
		if err == nil {
			t.Fatal("expected error for missing field")
		}
	})

	t.Run("not a string", func(t *testing.T) {
		t.Parallel()

		_, err := extractCustomResponse([]byte(`{"a": 42}`), "a")
		if err == nil {
			t.Fatal("expected error for non-string value")
		}
	})

	t.Run("invalid array index", func(t *testing.T) {
		t.Parallel()

		_, err := extractCustomResponse([]byte(`{"a": [1, 2]}`), "a.5")
		if err == nil {
			t.Fatal("expected error for invalid array index")
		}
	})

	t.Run("non-array index", func(t *testing.T) {
		t.Parallel()

		_, err := extractCustomResponse([]byte(`{"a": {"b": 1}}`), "a.0")
		if err == nil {
			t.Fatal("expected error for non-array index")
		}
	})

	t.Run("cannot descend into scalar", func(t *testing.T) {
		t.Parallel()

		_, err := extractCustomResponse([]byte(`{"a": 1}`), "a.b")
		if err == nil {
			t.Fatal("expected error for descending into scalar")
		}
	})
}
