// Package notifier provides the application's event notifier. Events are
// logged as JSON and, when a webhook URL is configured, also posted to it.
// It serves the code generation agent (agents.AgentNotifier) and, through
// Alerts, the contract change alerts (usecase.AlertNotifier).
package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/Eisenmann/openapi-mocker/internal/agents"
	"github.com/Eisenmann/openapi-mocker/internal/usecase"
)

const webhookTimeout = 5 * time.Second

// ErrWebhookURL is returned for a webhook URL that is not an http(s) URL.
var ErrWebhookURL = errors.New("webhook URL must be an absolute http or https URL")

// Notifier logs events as JSON and optionally posts them to a webhook.
type Notifier struct {
	webhookURL string
	client     *http.Client
	wg         sync.WaitGroup
}

// New creates a notifier that only logs.
func New() *Notifier {
	return &Notifier{webhookURL: "", client: nil, wg: sync.WaitGroup{}}
}

// NewWithWebhook creates a notifier that also posts every event, as JSON, to
// webhookURL. An empty URL means log only.
func NewWithWebhook(webhookURL string) (*Notifier, error) {
	n := New()

	if webhookURL == "" {
		return n, nil
	}

	u, err := url.Parse(webhookURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, ErrWebhookURL
	}

	n.webhookURL = webhookURL
	n.client = &http.Client{
		Transport: nil, CheckRedirect: nil, Jar: nil, Timeout: webhookTimeout,
	}

	return n, nil
}

// Notify handles an agent event.
func (n *Notifier) Notify(ctx context.Context, event agents.AgentEvent) error {
	return n.emit(ctx, "AGENT EVENT", event.Type, event.Payload)
}

// Alerts returns the notifier as a usecase.AlertNotifier.
func (n *Notifier) Alerts() AlertSink {
	return AlertSink{n: n}
}

// Wait blocks until the webhook deliveries started so far are done.
func (n *Notifier) Wait() {
	n.wg.Wait()
}

// AlertSink adapts the notifier to usecase.AlertNotifier.
type AlertSink struct{ n *Notifier }

var _ usecase.AlertNotifier = AlertSink{n: nil}

func (a AlertSink) Notify(ctx context.Context, eventType string, payload any) error {
	return a.n.emit(ctx, "ALERT", eventType, payload)
}

// summarizer is implemented by payloads that can describe themselves in one
// line; it becomes the "text" field of the webhook body, which chat webhooks
// (Slack, Mattermost...) show.
type summarizer interface{ Summary() string }

type webhookBody struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Payload any    `json:"payload"`
}

func (n *Notifier) emit(ctx context.Context, logPrefix, eventType string, payload any) error {
	body := webhookBody{Type: eventType, Text: "", Payload: payload}

	if s, ok := payload.(summarizer); ok {
		body.Text = s.Summary()
	}

	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}

	log.Printf("%s: %s", logPrefix, data)

	if n.webhookURL != "" {
		n.wg.Add(1)

		go n.post(context.WithoutCancel(ctx), data)
	}

	return nil
}

// post delivers one event. It runs in the background with its own timeout, so
// a slow webhook never delays the request that raised the event.
func (n *Notifier) post(parent context.Context, data []byte) {
	defer n.wg.Done()

	ctx, cancel := context.WithTimeout(parent, webhookTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(data))
	if err != nil {
		log.Printf("webhook: %v", err)

		return
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req) //nolint:gosec // the URL is operator configuration (ALERT_WEBHOOK_URL).
	if err != nil {
		log.Printf("webhook delivery failed: %v", err)

		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		log.Printf("webhook answered %d", resp.StatusCode)
	}
}
