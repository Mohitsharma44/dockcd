package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"context"
)

func TestWebhookNotifierName(t *testing.T) {
	n := NewWebhookNotifier("my-webhook", "http://example.com/hook")
	if got := n.Name(); got != "my-webhook" {
		t.Errorf("Name() = %q, want %q", got, "my-webhook")
	}
}

func TestWebhookNotifierSend(t *testing.T) {
	var (
		gotMethod      string
		gotContentType string
		gotBody        map[string]interface{}
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ts, _ := time.Parse(time.RFC3339, "2026-04-04T12:00:00Z")
	event := Event{
		Type:      EventDeployFailure,
		Timestamp: ts,
		Stack:     "traefik",
		Host:      "server04",
		Commit:    "abc123",
		Branch:    "main",
		Message:   "health check timed out",
		Metadata:  map[string]string{"key": "val"},
	}

	n := NewWebhookNotifier("test-hook", srv.URL)
	if err := n.Send(context.Background(), event); err != nil {
		t.Fatalf("Send() unexpected error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}

	checks := map[string]string{
		"event":   string(EventDeployFailure),
		"stack":   "traefik",
		"host":    "server04",
		"commit":  "abc123",
		"branch":  "main",
		"message": "health check timed out",
	}
	for field, want := range checks {
		got, ok := gotBody[field]
		if !ok {
			t.Errorf("payload missing field %q", field)
			continue
		}
		if got.(string) != want {
			t.Errorf("payload[%q] = %q, want %q", field, got, want)
		}
	}

	// timestamp must be present and parseable as RFC3339
	rawTS, ok := gotBody["timestamp"]
	if !ok {
		t.Fatal("payload missing field \"timestamp\"")
	}
	if _, err := time.Parse(time.RFC3339, rawTS.(string)); err != nil {
		t.Errorf("timestamp %q is not RFC3339: %v", rawTS, err)
	}
}

func TestWebhookNotifierNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	n := NewWebhookNotifier("failing-hook", srv.URL)
	err := n.Send(context.Background(), Event{Type: EventDeployFailure})
	if err == nil {
		t.Fatal("Send() expected error on 500 response, got nil")
	}
}

func TestWebhookNotifierNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// Close the server before making the request to simulate a network error.
	srv.Close()

	n := NewWebhookNotifier("dead-hook", srv.URL)
	err := n.Send(context.Background(), Event{Type: EventDeployFailure})
	if err == nil {
		t.Fatal("Send() expected error on closed server, got nil")
	}
}
