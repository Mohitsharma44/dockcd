package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Notifier delivers events to an external system.
type Notifier interface {
	// Name returns the human-readable identifier for this notifier.
	Name() string
	// Send delivers the event to the external system. It returns an error if
	// delivery fails.
	Send(ctx context.Context, event Event) error
}

// webhookPayload is the JSON body sent by WebhookNotifier.
type webhookPayload struct {
	Event     string            `json:"event"`
	Timestamp time.Time         `json:"timestamp"`
	Stack     string            `json:"stack,omitempty"`
	Host      string            `json:"host,omitempty"`
	Commit    string            `json:"commit,omitempty"`
	Branch    string            `json:"branch,omitempty"`
	Message   string            `json:"message,omitempty"`
	Metadata  map[string]string `json:"metadata"`
}

// WebhookNotifier POSTs a JSON payload to a configured URL for each event.
type WebhookNotifier struct {
	name   string
	url    string
	client *http.Client
}

// NewWebhookNotifier returns a WebhookNotifier that delivers events to url.
// The name is used in error messages and returned by Name.
func NewWebhookNotifier(name, url string) *WebhookNotifier {
	return &WebhookNotifier{
		name: name,
		url:  url,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Name returns the configured notifier name.
func (w *WebhookNotifier) Name() string {
	return w.name
}

// Send marshals event into a JSON payload and POSTs it to the configured URL.
// It returns nil on a 2xx response, and a descriptive error otherwise.
func (w *WebhookNotifier) Send(ctx context.Context, event Event) error {
	meta := event.Metadata
	if meta == nil {
		meta = map[string]string{}
	}

	payload := webhookPayload{
		Event:     string(event.Type),
		Timestamp: event.Timestamp,
		Stack:     event.Stack,
		Host:      event.Host,
		Commit:    event.Commit,
		Branch:    event.Branch,
		Message:   event.Message,
		Metadata:  meta,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("notifier %q: marshal payload: %w", w.name, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notifier %q: create request: %w", w.name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("notifier %q: send request: %w", w.name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notifier %q: unexpected status %d", w.name, resp.StatusCode)
	}

	return nil
}
