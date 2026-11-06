package notifier_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Eisenmann/openapi-mocker/internal/adapter/notifier"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

func TestNotifier_PostsAlertsToWebhook(t *testing.T) {
	t.Parallel()

	got := make(chan map[string]any, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		var m map[string]any

		_ = json.Unmarshal(body, &m)

		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type %q", r.Header.Get("Content-Type"))
		}

		got <- m
	}))
	t.Cleanup(srv.Close)

	n, err := notifier.NewWithWebhook(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	alert := usecase.BreakingChangesAlert{ProjectID: "p1", FromVersion: 1, ToVersion: 2, Breaking: 3}

	err = n.Alerts().Notify(context.Background(), usecase.EventBreakingChanges, &alert)
	if err != nil {
		t.Fatal(err)
	}

	n.Wait()

	m := <-got
	if m["type"] != usecase.EventBreakingChanges || m["text"] != alert.Summary() {
		t.Errorf("body: %v", m)
	}

	payload, _ := m["payload"].(map[string]any)
	if payload["breaking"] != float64(3) || payload["projectId"] != "p1" {
		t.Errorf("payload: %v", payload)
	}
}

func TestNotifier_WebhookURLIsValidated(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"ftp://x.example", "/relative", "http://"} {
		if _, err := notifier.NewWithWebhook(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}

	if n, err := notifier.NewWithWebhook(""); err != nil || n == nil {
		t.Errorf("an empty URL means log only: %v", err)
	}
}

func TestNotifier_WebhookFailureDoesNotFailNotify(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	n, _ := notifier.NewWithWebhook(srv.URL)

	if err := n.Alerts().Notify(context.Background(), "x", map[string]int{"a": 1}); err != nil {
		t.Errorf("delivery is best effort, got %v", err)
	}

	n.Wait()
}
