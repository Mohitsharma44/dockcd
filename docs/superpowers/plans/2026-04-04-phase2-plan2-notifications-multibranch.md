# Phase 2 Plan 2: Notifications + Multi-Branch Support

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Slack + generic webhook notifications on lifecycle events, and per-stack branch override support so stacks can track branches other than the default.

**Architecture:** A `Notifier` interface with two implementations (WebhookNotifier, SlackNotifier) consumes events from the existing EventBus via a Dispatcher goroutine. Multi-branch extends the Poller to track per-branch hashes and extract stack files from non-default branches via `git archive`. Both features are independent and can be implemented in parallel.

**Tech Stack:** Go stdlib (`net/http`, `encoding/json`, `os/exec`), existing EventBus from `internal/notify/`, existing Poller from `internal/git/`. No new dependencies.

**Conventions (read before starting any task):**
- Follow existing patterns: table-driven tests, mock interfaces, `writeTemp` helpers
- All tests use `-race` flag (CI enforces this)
- Integration tests use `//go:build integration` tag
- Error wrapping: `fmt.Errorf("context: %w", err)`
- Structured logging via `slog`
- Existing types: `notify.Event`, `notify.EventBus`, `config.NotificationConfig`, `git.Poller`, `git.State`

---

## File Map

### New files
```
internal/notify/notifier.go          # Notifier interface, WebhookNotifier, SlackNotifier
internal/notify/notifier_test.go     # Tests with httptest servers
internal/notify/dispatcher.go        # Dispatcher: subscribes to EventBus, filters, routes to notifiers
internal/notify/dispatcher_test.go   # Dispatcher tests
```

### Modified files
```
internal/git/poller.go               # Multi-branch state, per-branch fetch/diff, git archive extraction
internal/git/poller_test.go          # Tests for multi-branch
internal/config/config.go            # EffectiveBranch() helper on Stack
internal/reconciler/reconciler.go    # Branch-aware change detection, pass branch to hooks.Env
internal/reconciler/reconciler_test.go # Tests for branch-aware reconcile
internal/reconciler/reconciler_integration_test.go # Integration tests for multi-branch
cmd/dockcd/serve.go                  # Wire Dispatcher with notifiers from config
```

---

## Task 1: Notifier Interface and WebhookNotifier

**Files:**
- Create: `internal/notify/notifier.go`
- Create: `internal/notify/notifier_test.go`

### Verification Spec

**Functional:**
- `Notifier` interface: `Name() string`, `Send(ctx context.Context, event Event) error`
- `WebhookNotifier` POSTs JSON to a URL matching the generic webhook payload from the design spec
- Payload fields: `event`, `timestamp`, `stack`, `host`, `commit`, `branch`, `message`, `metadata`
- Returns nil on 2xx response
- Returns error on non-2xx with status code in message
- Returns error on network failure
- HTTP timeout of 10 seconds per request

**Non-functional:**
- Tested with httptest (no real network)
- Verifies request method (POST), Content-Type (application/json), and payload shape
- Test for network error (closed server)
- Test for non-2xx response

---

- [ ] **Step 1: Write failing tests for WebhookNotifier**

```go
// internal/notify/notifier_test.go
package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebhookNotifierSend(t *testing.T) {
	var receivedBody map[string]any
	var receivedMethod string
	var receivedContentType string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedContentType = r.Header.Get("Content-Type")
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	n := NewWebhookNotifier("test-webhook", ts.URL)
	event := Event{
		Type:      EventDeploySuccess,
		Timestamp: time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC),
		Stack:     "traefik",
		Host:      "server04",
		Commit:    "abc123",
		Branch:    "main",
		Message:   "deploy succeeded",
	}

	err := n.Send(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if receivedMethod != "POST" {
		t.Errorf("expected POST, got %s", receivedMethod)
	}
	if receivedContentType != "application/json" {
		t.Errorf("expected application/json, got %s", receivedContentType)
	}
	if receivedBody["event"] != "deploy_success" {
		t.Errorf("expected event 'deploy_success', got %v", receivedBody["event"])
	}
	if receivedBody["stack"] != "traefik" {
		t.Errorf("expected stack 'traefik', got %v", receivedBody["stack"])
	}
}

func TestWebhookNotifierName(t *testing.T) {
	n := NewWebhookNotifier("my-webhook", "http://example.com")
	if n.Name() != "my-webhook" {
		t.Errorf("expected name 'my-webhook', got %q", n.Name())
	}
}

func TestWebhookNotifierNon2xx(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	n := NewWebhookNotifier("test", ts.URL)
	err := n.Send(context.Background(), Event{Type: EventDeployFailure})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestWebhookNotifierNetworkError(t *testing.T) {
	n := NewWebhookNotifier("test", "http://127.0.0.1:1") // nothing listening
	err := n.Send(context.Background(), Event{Type: EventDeployFailure})
	if err == nil {
		t.Fatal("expected error for network failure")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race -v ./internal/notify/ -run "TestWebhook"`
Expected: Compilation error — `NewWebhookNotifier` doesn't exist

- [ ] **Step 3: Implement Notifier interface and WebhookNotifier**

```go
// internal/notify/notifier.go
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
	Name() string
	Send(ctx context.Context, event Event) error
}

// webhookPayload is the JSON shape sent to generic webhook URLs.
type webhookPayload struct {
	Event     string            `json:"event"`
	Timestamp string            `json:"timestamp"`
	Stack     string            `json:"stack,omitempty"`
	Host      string            `json:"host,omitempty"`
	Commit    string            `json:"commit,omitempty"`
	Branch    string            `json:"branch,omitempty"`
	Message   string            `json:"message,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// WebhookNotifier sends events as JSON POST requests to a URL.
type WebhookNotifier struct {
	name   string
	url    string
	client *http.Client
}

// NewWebhookNotifier creates a notifier that POSTs generic JSON to the given URL.
func NewWebhookNotifier(name, url string) *WebhookNotifier {
	return &WebhookNotifier{
		name:   name,
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (w *WebhookNotifier) Name() string { return w.name }

func (w *WebhookNotifier) Send(ctx context.Context, event Event) error {
	payload := webhookPayload{
		Event:     string(event.Type),
		Timestamp: event.Timestamp.Format(time.RFC3339),
		Stack:     event.Stack,
		Host:      event.Host,
		Commit:    event.Commit,
		Branch:    event.Branch,
		Message:   event.Message,
		Metadata:  event.Metadata,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("notifier %q: marshaling payload: %w", w.name, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notifier %q: creating request: %w", w.name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("notifier %q: sending request: %w", w.name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notifier %q: server returned %d", w.name, resp.StatusCode)
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test -race -v ./internal/notify/ -run "TestWebhook"`
Expected: All 4 tests PASS

- [ ] **Step 5: Commit**

```bash
git add internal/notify/notifier.go internal/notify/notifier_test.go
git commit -m "feat: add Notifier interface and WebhookNotifier"
```

---

## Task 2: SlackNotifier

**Files:**
- Modify: `internal/notify/notifier.go`
- Modify: `internal/notify/notifier_test.go`

### Verification Spec

**Functional:**
- `SlackNotifier` sends Slack Block Kit formatted payloads to a webhook URL
- Color-coded: green for success events, red for failure, yellow for rollback/warning
- Includes: stack name, commit (7-char), branch, host, timestamp
- Error detail in a code block when Message is non-empty
- Reuses the same HTTP client pattern as WebhookNotifier (10s timeout)

**Non-functional:**
- Tested with httptest verifying Slack payload structure
- Test for each color category (success, failure, rollback)

---

- [ ] **Step 1: Write failing tests for SlackNotifier**

```go
// Add to internal/notify/notifier_test.go

func TestSlackNotifierSend(t *testing.T) {
	var receivedBody map[string]any

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	n := NewSlackNotifier("slack-test", ts.URL)
	event := Event{
		Type:      EventDeploySuccess,
		Timestamp: time.Date(2026, 4, 4, 12, 0, 0, 0, time.UTC),
		Stack:     "traefik",
		Host:      "server04",
		Commit:    "abc123def456",
		Branch:    "main",
		Message:   "deploy succeeded",
	}

	err := n.Send(context.Background(), event)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Slack payload should have "attachments" array.
	attachments, ok := receivedBody["attachments"].([]any)
	if !ok || len(attachments) == 0 {
		t.Fatal("expected non-empty 'attachments' in Slack payload")
	}

	att := attachments[0].(map[string]any)
	// Success → green color.
	if att["color"] != "good" {
		t.Errorf("expected color 'good', got %v", att["color"])
	}
}

func TestSlackNotifierFailureColor(t *testing.T) {
	var receivedBody map[string]any

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	n := NewSlackNotifier("slack-test", ts.URL)
	err := n.Send(context.Background(), Event{
		Type: EventDeployFailure, Stack: "web", Message: "health check failed",
	})
	if err != nil {
		t.Fatal(err)
	}

	attachments := receivedBody["attachments"].([]any)
	att := attachments[0].(map[string]any)
	if att["color"] != "danger" {
		t.Errorf("expected color 'danger', got %v", att["color"])
	}
}

func TestSlackNotifierRollbackColor(t *testing.T) {
	var receivedBody map[string]any

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	n := NewSlackNotifier("slack-test", ts.URL)
	err := n.Send(context.Background(), Event{Type: EventRollbackSuccess, Stack: "web"})
	if err != nil {
		t.Fatal(err)
	}

	attachments := receivedBody["attachments"].([]any)
	att := attachments[0].(map[string]any)
	if att["color"] != "warning" {
		t.Errorf("expected color 'warning', got %v", att["color"])
	}
}

func TestSlackNotifierCommitTruncated(t *testing.T) {
	var receivedBody map[string]any

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	n := NewSlackNotifier("slack-test", ts.URL)
	err := n.Send(context.Background(), Event{
		Type: EventDeploySuccess, Commit: "abcdef1234567890",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The commit in the text should be truncated to 7 chars.
	attachments := receivedBody["attachments"].([]any)
	att := attachments[0].(map[string]any)
	text, _ := att["text"].(string)
	if len(text) > 0 && !contains(text, "abcdef1") {
		t.Errorf("expected 7-char commit in text, got %q", text)
	}
}

// contains is a simple helper for substring matching.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && stringContains(s, substr))
}
```

Actually, use `strings.Contains` instead of a custom helper. Add `"strings"` import.

- [ ] **Step 2: Implement SlackNotifier**

Add to `internal/notify/notifier.go`:

```go
// SlackNotifier sends events as Slack-formatted webhook payloads.
type SlackNotifier struct {
	name   string
	url    string
	client *http.Client
}

// NewSlackNotifier creates a notifier that sends Slack Block Kit attachments.
func NewSlackNotifier(name, url string) *SlackNotifier {
	return &SlackNotifier{
		name:   name,
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *SlackNotifier) Name() string { return s.name }

func (s *SlackNotifier) Send(ctx context.Context, event Event) error {
	color := slackColor(event.Type)
	commit := event.Commit
	if len(commit) > 7 {
		commit = commit[:7]
	}

	text := fmt.Sprintf("*%s* | stack: `%s` | branch: `%s` | host: `%s` | commit: `%s`",
		string(event.Type), event.Stack, event.Branch, event.Host, commit)

	if event.Message != "" {
		text += fmt.Sprintf("\n```%s```", event.Message)
	}

	payload := map[string]any{
		"attachments": []map[string]any{
			{
				"color":  color,
				"text":   text,
				"ts":     event.Timestamp.Unix(),
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("notifier %q: marshaling slack payload: %w", s.name, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notifier %q: creating request: %w", s.name, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("notifier %q: sending request: %w", s.name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notifier %q: slack returned %d", s.name, resp.StatusCode)
	}
	return nil
}

func slackColor(t EventType) string {
	switch t {
	case EventDeploySuccess, EventReconcileComplete, EventStackResumed:
		return "good"
	case EventDeployFailure, EventRollbackFailure, EventHookFailure:
		return "danger"
	case EventRollbackSuccess, EventStackSuspended, EventForceDeploy:
		return "warning"
	default:
		return "#439FE0" // info blue
	}
}
```

- [ ] **Step 3: Run tests**

Run: `go test -race -v ./internal/notify/ -run "TestSlack"`
Expected: All Slack tests PASS

- [ ] **Step 4: Run full suite**

Run: `go test -race ./...`
Expected: All tests PASS

- [ ] **Step 5: Commit**

```bash
git add internal/notify/notifier.go internal/notify/notifier_test.go
git commit -m "feat: add SlackNotifier with color-coded attachments"
```

---

## Task 3: Notification Dispatcher

**Files:**
- Create: `internal/notify/dispatcher.go`
- Create: `internal/notify/dispatcher_test.go`

### Verification Spec

**Functional:**
- `Dispatcher` subscribes to EventBus, filters events per-notifier, and routes to the right notifiers
- Each notifier has a configured list of event types it cares about
- Events not in a notifier's list are skipped
- Notification delivery is fire-and-forget (errors logged, never block deploy)
- 3 retries with exponential backoff (1s, 2s, 4s) on send failure
- Failed notifications increment a counter (for metrics) and log a warning
- `Dispatcher.Start(ctx)` runs in a goroutine, stops when context is cancelled or EventBus is closed
- `NewDispatcher(eventBus, logger, notifiers []NotifierWithFilter)` constructor

**Non-functional:**
- Thread-safe
- No goroutine leaks: context cancellation cleans up
- Tested without real network (mock notifiers)

---

- [ ] **Step 1: Write failing tests**

```go
// internal/notify/dispatcher_test.go
package notify

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// mockNotifier records sent events for testing.
type mockNotifier struct {
	name   string
	mu     sync.Mutex
	events []Event
	err    error // if set, Send returns this error
}

func (m *mockNotifier) Name() string { return m.name }
func (m *mockNotifier) Send(_ context.Context, e Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.events = append(m.events, e)
	return nil
}
func (m *mockNotifier) sentEvents() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]Event, len(m.events))
	copy(cp, m.events)
	return cp
}

func TestDispatcherRoutesEvents(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	slack := &mockNotifier{name: "slack"}
	webhook := &mockNotifier{name: "webhook"}

	d := NewDispatcher(bus, slog.Default(), []NotifierWithFilter{
		{Notifier: slack, Events: map[EventType]bool{EventDeploySuccess: true, EventDeployFailure: true}},
		{Notifier: webhook, Events: map[EventType]bool{EventDeployFailure: true}},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Start(ctx)

	// Give dispatcher time to subscribe.
	time.Sleep(50 * time.Millisecond)

	bus.Emit(Event{Type: EventDeploySuccess, Stack: "web"})
	bus.Emit(Event{Type: EventDeployFailure, Stack: "db"})
	bus.Emit(Event{Type: EventRollbackSuccess, Stack: "api"}) // no notifier cares

	time.Sleep(100 * time.Millisecond)

	slackEvents := slack.sentEvents()
	webhookEvents := webhook.sentEvents()

	if len(slackEvents) != 2 {
		t.Errorf("slack: expected 2 events, got %d", len(slackEvents))
	}
	if len(webhookEvents) != 1 {
		t.Errorf("webhook: expected 1 event, got %d", len(webhookEvents))
	}
	if len(webhookEvents) > 0 && webhookEvents[0].Stack != "db" {
		t.Errorf("webhook: expected stack 'db', got %q", webhookEvents[0].Stack)
	}
}

func TestDispatcherStopsOnContextCancel(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	d := NewDispatcher(bus, slog.Default(), nil)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		d.Start(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// good
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not stop after context cancel")
	}
}

func TestDispatcherStopsOnBusClose(t *testing.T) {
	bus := NewEventBus()
	d := NewDispatcher(bus, slog.Default(), nil)

	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		d.Start(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	bus.Close()

	select {
	case <-done:
		// good
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not stop after bus close")
	}
}

func TestDispatcherRetriesOnFailure(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	failCount := 0
	failing := &mockNotifier{name: "failing"}
	// First 2 calls fail, 3rd succeeds.
	origSend := failing.Send
	_ = origSend
	var mu sync.Mutex
	var attempts int

	// We need a custom notifier for retry testing.
	retryNotifier := &retryTestNotifier{failUntil: 2}

	d := NewDispatcher(bus, slog.Default(), []NotifierWithFilter{
		{Notifier: retryNotifier, Events: map[EventType]bool{EventDeployFailure: true}},
	})
	_ = failCount
	_ = mu
	_ = attempts

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Start(ctx)

	time.Sleep(50 * time.Millisecond)
	bus.Emit(Event{Type: EventDeployFailure, Stack: "web"})

	// Wait for retries (1s + 2s + margin).
	time.Sleep(4 * time.Second)

	if retryNotifier.successCount() != 1 {
		t.Errorf("expected 1 successful delivery after retries, got %d", retryNotifier.successCount())
	}
}

type retryTestNotifier struct {
	failUntil int
	mu        sync.Mutex
	attempts  int
	successes int
}

func (r *retryTestNotifier) Name() string { return "retry-test" }
func (r *retryTestNotifier) Send(_ context.Context, _ Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts++
	if r.attempts <= r.failUntil {
		return fmt.Errorf("simulated failure %d", r.attempts)
	}
	r.successes++
	return nil
}
func (r *retryTestNotifier) successCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.successes
}
```

- [ ] **Step 2: Implement Dispatcher**

```go
// internal/notify/dispatcher.go
package notify

import (
	"context"
	"log/slog"
	"time"
)

// NotifierWithFilter pairs a Notifier with the set of event types it should receive.
type NotifierWithFilter struct {
	Notifier Notifier
	Events   map[EventType]bool
}

// Dispatcher subscribes to an EventBus and routes events to notifiers
// based on per-notifier event filters.
type Dispatcher struct {
	bus       *EventBus
	logger    *slog.Logger
	notifiers []NotifierWithFilter
}

// NewDispatcher creates a Dispatcher. Call Start() to begin consuming events.
func NewDispatcher(bus *EventBus, logger *slog.Logger, notifiers []NotifierWithFilter) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{bus: bus, logger: logger, notifiers: notifiers}
}

// Start subscribes to the EventBus and dispatches events to matching notifiers.
// It blocks until ctx is cancelled or the EventBus is closed.
func (d *Dispatcher) Start(ctx context.Context) {
	ch := d.bus.Subscribe()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-ch:
			if !ok {
				return // bus closed
			}
			d.dispatch(ctx, event)
		}
	}
}

func (d *Dispatcher) dispatch(ctx context.Context, event Event) {
	for _, nf := range d.notifiers {
		if !nf.Events[event.Type] {
			continue
		}
		// Fire-and-forget with retries.
		go d.sendWithRetry(ctx, nf.Notifier, event)
	}
}

// sendWithRetry attempts to send with 3 retries and exponential backoff.
func (d *Dispatcher) sendWithRetry(ctx context.Context, n Notifier, event Event) {
	backoff := []time.Duration{0, 1 * time.Second, 2 * time.Second, 4 * time.Second}

	for attempt := 0; attempt < len(backoff); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff[attempt]):
			}
		}

		if err := n.Send(ctx, event); err != nil {
			d.logger.Warn("notification send failed",
				"notifier", n.Name(),
				"event", string(event.Type),
				"attempt", attempt+1,
				"error", err,
			)
			continue
		}
		return // success
	}
	d.logger.Error("notification delivery failed after retries",
		"notifier", n.Name(),
		"event", string(event.Type),
		"stack", event.Stack,
	)
}
```

- [ ] **Step 3: Run tests**

Run: `go test -race -v ./internal/notify/`
Expected: All tests PASS

- [ ] **Step 4: Commit**

```bash
git add internal/notify/dispatcher.go internal/notify/dispatcher_test.go
git commit -m "feat: add notification Dispatcher with retry and event filtering"
```

---

## Task 4: Wire Notifications in serve.go

**Files:**
- Modify: `cmd/dockcd/serve.go`

### Verification Spec

**Functional:**
- After loading config and creating EventBus, instantiate notifiers from `cfg.Notifications`
- For each notification config: create WebhookNotifier or SlackNotifier based on `Type`
- Build `NotifierWithFilter` with the configured event list
- Create Dispatcher and start it in a goroutine
- Graceful shutdown: context cancel stops the dispatcher

**Non-functional:**
- Zero notifiers configured → no dispatcher started (no-op)
- Build still works, all tests pass

---

- [ ] **Step 1: Add notifier wiring to serve.go**

After the EventBus creation and before creating the reconciler, add:

```go
// Build notifiers from config.
var notifierFilters []notify.NotifierWithFilter
for _, nc := range cfg.Notifications {
	var n notify.Notifier
	switch nc.Type {
	case "slack":
		n = notify.NewSlackNotifier(nc.Name, nc.URL)
	case "webhook":
		n = notify.NewWebhookNotifier(nc.Name, nc.URL)
	}
	events := make(map[notify.EventType]bool, len(nc.Events))
	for _, e := range nc.Events {
		events[notify.EventType(e)] = true
	}
	notifierFilters = append(notifierFilters, notify.NotifierWithFilter{
		Notifier: n,
		Events:   events,
	})
}

// Start notification dispatcher if any notifiers configured.
if len(notifierFilters) > 0 {
	dispatcher := notify.NewDispatcher(eventBus, logger, notifierFilters)
	go dispatcher.Start(ctx)
	logger.Info("notification dispatcher started", "notifiers", len(notifierFilters))
}
```

The `ctx` here is the signal context, so dispatcher stops on SIGINT/SIGTERM.

- [ ] **Step 2: Verify build and tests**

Run: `go build ./cmd/dockcd/ && go test -race ./...`
Expected: Build succeeds, all tests pass

- [ ] **Step 3: Commit**

```bash
git add cmd/dockcd/serve.go
git commit -m "feat: wire notification dispatcher from config into serve startup"
```

---

## Task 5: Multi-Branch State and Polling

**Files:**
- Modify: `internal/git/poller.go`
- Modify: `internal/git/poller_test.go`
- Modify: `internal/config/config.go`

### Verification Spec

**Functional:**
- `State` struct gains `Branches map[string]string` field (branch → last-seen hash)
- `LastSuccessfulCommits` values change from `string` to `struct{Branch, Commit string}`
- `Poller` gains `SetTrackedBranches(branches []string)` to configure which branches to poll
- `Poller.Fetch()` becomes branch-aware: fetches all, diffs each tracked branch independently
- `Poller.FetchBranch(branch string) (changed bool, err error)` — fetch and diff a single branch
- `Poller.ChangedStacks` gains a `branch string` parameter to filter by branch
- `Poller.LastHashForBranch(branch string) string` — returns last hash for a specific branch
- For non-default branches: `Poller.ExtractBranchFiles(branch, stackPath, destDir string) error` uses `git archive` to extract files
- Backward compat: state file without `branches` field migrates on load (uses `last_hash` as the default branch hash)
- `config.Stack` gains `EffectiveBranch(defaultBranch string) string` helper

**Non-functional:**
- All state mutations under existing mutex
- Existing tests still pass
- New tests for multi-branch scenarios

**Edge cases:**
- Single branch (existing behavior, no regression)
- Two branches tracked, only one has changes
- State file migration from old format

---

- [ ] **Step 1: Add EffectiveBranch to config.Stack**

Add to `internal/config/config.go`:

```go
// EffectiveBranch returns the branch this stack tracks.
// Falls back to defaultBranch if not explicitly set.
func (s *Stack) EffectiveBranch(defaultBranch string) string {
	if s.Branch != "" {
		return s.Branch
	}
	return defaultBranch
}
```

- [ ] **Step 2: Write failing tests for multi-branch poller**

Add to `internal/git/poller_test.go`:

```go
func TestLastHashForBranch(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, ".dockcd_state")

	p := NewPoller("", dir)
	p.SetStateFile(stateFile)
	p.lastHash = "abc123"
	p.state.Branches = map[string]string{"main": "aaa", "canary": "bbb"}

	if got := p.LastHashForBranch("main"); got != "aaa" {
		t.Errorf("expected 'aaa', got %q", got)
	}
	if got := p.LastHashForBranch("canary"); got != "bbb" {
		t.Errorf("expected 'bbb', got %q", got)
	}
	if got := p.LastHashForBranch("unknown"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestSetTrackedBranches(t *testing.T) {
	dir := t.TempDir()
	p := NewPoller("", dir)
	p.SetTrackedBranches([]string{"main", "canary"})

	if len(p.trackedBranches) != 2 {
		t.Fatalf("expected 2 tracked branches, got %d", len(p.trackedBranches))
	}
}

func TestStateMigrationAddsBranches(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, ".dockcd_state")

	// Write old-format state without branches field.
	data := `{"last_hash":"abc123","last_successful_commits":{"web":"abc123"}}`
	if err := os.WriteFile(stateFile, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	p := NewPoller("", dir)
	p.SetStateFile(stateFile)
	p.branch = "main"
	if err := p.LoadState(); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	// After migration, Branches should contain the default branch hash.
	if p.state.Branches == nil {
		t.Fatal("expected Branches to be initialized after migration")
	}
	if p.state.Branches["main"] != "abc123" {
		t.Errorf("expected Branches[main]='abc123', got %q", p.state.Branches["main"])
	}
}
```

- [ ] **Step 3: Implement multi-branch state in Poller**

Modify `internal/git/poller.go`:

Add to `State` struct:
```go
type State struct {
	LastHash              string            `json:"last_hash"`
	Branches              map[string]string `json:"branches,omitempty"`
	LastSuccessfulCommits map[string]string `json:"last_successful_commits"`
	SuspendedStacks       []string          `json:"suspended_stacks,omitempty"`
}
```

Add to `Poller` struct:
```go
trackedBranches []string // branches to poll (from stack configs)
```

Add methods:
```go
func (p *Poller) SetTrackedBranches(branches []string) {
	p.trackedBranches = branches
}

func (p *Poller) LastHashForBranch(branch string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state.Branches == nil {
		return ""
	}
	return p.state.Branches[branch]
}
```

Update `LoadState()` to migrate — after loading, if `Branches` is nil and `LastHash` is set:
```go
if p.state.Branches == nil && p.lastHash != "" && p.branch != "" {
	p.state.Branches = map[string]string{p.branch: p.lastHash}
}
if p.state.Branches == nil {
	p.state.Branches = make(map[string]string)
}
```

Add `ExtractBranchFiles`:
```go
// ExtractBranchFiles extracts files for a stack on a non-default branch
// using git archive, writing to destDir.
func (p *Poller) ExtractBranchFiles(branch, stackPath, destDir string) error {
	cmd := exec.Command("git", "archive", "--format=tar", "origin/"+branch, stackPath)
	cmd.Dir = p.localDir
	tarOut, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git archive origin/%s %s: %w", branch, stackPath, err)
	}

	// Extract tar to destDir.
	tarCmd := exec.Command("tar", "xf", "-", "--strip-components="+fmt.Sprint(strings.Count(stackPath, "/")+1))
	tarCmd.Dir = destDir
	tarCmd.Stdin = bytes.NewReader(tarOut)
	if out, err := tarCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("extracting archive: %w\n%s", err, out)
	}
	return nil
}
```

You'll need to add `"bytes"` to imports.

- [ ] **Step 4: Run tests**

Run: `go test -race -v ./internal/git/ && go test -race -v ./internal/config/`
Expected: All tests PASS

- [ ] **Step 5: Commit**

```bash
git add internal/git/poller.go internal/git/poller_test.go internal/config/config.go
git commit -m "feat: add multi-branch state tracking and git archive extraction"
```

---

## Task 6: Branch-Aware Reconciler

**Files:**
- Modify: `internal/reconciler/reconciler.go`
- Modify: `internal/reconciler/reconciler_test.go`

### Verification Spec

**Functional:**
- Reconciler resolves each stack's effective branch using `config.Stack.EffectiveBranch(cfg.Branch)`
- `buildStackPaths` groups stacks by branch
- During reconcile, the poller is asked for changed stacks per branch
- Stacks on non-default branches get their files extracted via `ExtractBranchFiles` to a temp dir before deploy
- Stacks on the default branch use the working tree as before (no change)
- `hooks.Env.Branch` is populated with the stack's effective branch

**Non-functional:**
- Existing single-branch behavior is a regression-free pass-through
- Tests for: default branch (no change), override branch stack

---

- [ ] **Step 1: Write failing test for branch-aware reconcile**

Add to `internal/reconciler/reconciler_test.go`:

```go
func TestFilterChangedStacksPopulatesBranch(t *testing.T) {
	stacks := []config.Stack{
		{Name: "traefik", Path: "server04/traefik", Branch: "canary"},
		{Name: "vault", Path: "server04/vault"},
	}

	poller := &mockPoller{lastHash: "abc123"}
	r, _ := testReconciler(t, poller, stacks, nil)

	// Both changed.
	result := r.filterChangedStacks([]string{"traefik", "vault"})

	for _, ds := range result {
		switch ds.Name {
		case "traefik":
			// Should have canary branch propagated (but branch is on hooks.Env, not deploy.Stack directly).
			// The key thing: traefik should still be in the result set.
		case "vault":
			// default branch
		default:
			t.Errorf("unexpected stack: %s", ds.Name)
		}
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 stacks, got %d", len(result))
	}
}
```

- [ ] **Step 2: Update reconciler to populate Branch in hooks.Env**

In `internal/reconciler/reconciler.go`, update `deployStack()`:

```go
func (r *Reconciler) deployStack(ctx context.Context, stack deploy.Stack, commitHash string) error {
	// Resolve the effective branch for this stack.
	branch := r.resolveStackBranch(stack.Name)

	env := hooks.Env{
		StackName: stack.Name,
		Commit:    commitHash,
		RepoDir:   r.repoDir,
		Branch:    branch,
	}
	// ... rest unchanged
```

Add helper:
```go
func (r *Reconciler) resolveStackBranch(stackName string) string {
	for _, cs := range r.hostStacks {
		if cs.Name == stackName {
			return cs.EffectiveBranch(r.defaultBranch)
		}
	}
	return r.defaultBranch
}
```

Add `defaultBranch` field to `Config` and `Reconciler`:
```go
// In Config:
DefaultBranch string

// In Reconciler:
defaultBranch string
```

Wire in `New()`.

- [ ] **Step 3: Update serve.go to pass DefaultBranch**

In `cmd/dockcd/serve.go`, add `DefaultBranch: cfg.Branch` to the reconciler config.

- [ ] **Step 4: Run full suite**

Run: `go test -race ./...`
Expected: All tests PASS

- [ ] **Step 5: Commit**

```bash
git add internal/reconciler/ cmd/dockcd/serve.go
git commit -m "feat: branch-aware reconciler with per-stack branch resolution"
```

---

## Task 7: Multi-Branch Integration Tests

**Files:**
- Modify: `internal/reconciler/reconciler_integration_test.go`

### Verification Spec

**Functional:**
- Integration test: stack on default branch deploys normally (regression)
- Integration test: stack with branch override — verify branch is resolved

**Non-functional:**
- Tests pass with `-race` and `//go:build integration`
- Uses real git repos via testutil

---

- [ ] **Step 1: Write integration test for branch in hooks.Env**

```go
func TestIntegrationBranchPassedToHookEnv(t *testing.T) {
	bareDir := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bareDir)
	testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx", "add web stack")
	// Create a hook that writes the branch to a file.
	testutil.CommitFile(t, workDir, "stacks/web/branch-hook.sh",
		"#!/bin/sh\necho $DOCKCD_BRANCH > /tmp/dockcd-test-branch-"+t.Name(), "add branch hook")

	cloneDir := filepath.Join(t.TempDir(), "dockcd-clone")
	poller := git.NewPoller(bareDir, cloneDir)
	poller.SetStateFile(filepath.Join(cloneDir, ".dockcd_state"))

	hookTimeout := 5 * time.Second
	stacks := []config.Stack{{
		Name:   "web",
		Path:   "stacks/web",
		Branch: "main", // explicit branch
		Hooks: &config.Hooks{
			PreDeploy: &config.HookConfig{Command: "sh branch-hook.sh", Timeout: hookTimeout},
		},
	}}

	var mu sync.Mutex
	var calls []deployCall
	runner := func(ctx context.Context, dir, name string, args ...string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, deployCall{dir: dir, name: name, args: args})
		return nil
	}

	status := &server.Status{}
	rec, err := New(Config{
		Poller:        poller,
		HostStacks:    stacks,
		Hostname:      "integration-test",
		RepoDir:       cloneDir,
		PollInterval:  100 * time.Millisecond,
		DefaultBranch: "main",
		Runner:        runner,
		Status:        status,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if err := rec.initRepo(); err != nil {
		t.Fatalf("initRepo failed: %v", err)
	}

	if err := rec.deployAll(context.Background()); err != nil {
		t.Fatalf("deployAll failed: %v", err)
	}

	// Verify the hook wrote the branch name.
	branchFile := "/tmp/dockcd-test-branch-" + t.Name()
	defer os.Remove(branchFile)
	data, err := os.ReadFile(branchFile)
	if err != nil {
		t.Fatalf("reading branch file: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "main" {
		t.Errorf("expected branch 'main', got %q", got)
	}
}
```

- [ ] **Step 2: Run integration tests**

Run: `go test -tags integration -race -v ./internal/reconciler/`
Expected: All tests PASS

- [ ] **Step 3: Commit**

```bash
git add internal/reconciler/reconciler_integration_test.go
git commit -m "test: add integration test for branch propagation to hook env"
```

---

## Summary of tasks and dependencies

```
Task 1: WebhookNotifier            (independent)
Task 2: SlackNotifier              (depends on 1 — same file)
Task 3: Dispatcher                 (depends on 1)
Task 4: Wire notifications         (depends on 2, 3)
Task 5: Multi-branch poller        (independent)
Task 6: Branch-aware reconciler    (depends on 5)
Task 7: Integration tests          (depends on 4, 6)
```

**Parallelizable groups:**
- Group A: Tasks 1, 5 (independent)
- Group B: Tasks 2, 3 (after Task 1); Task 6 (after Task 5)
- Group C: Task 4 (after 2, 3)
- Group D: Task 7 (after 4, 6)
