package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- mock implementations ---

type mockSuspender struct {
	suspended map[string]bool
	err       error // if non-nil, SuspendStack/ResumeStack returns this
}

func newMockSuspender() *mockSuspender {
	return &mockSuspender{suspended: make(map[string]bool)}
}

func (m *mockSuspender) SuspendStack(name string) error {
	if m.err != nil {
		return m.err
	}
	m.suspended[name] = true
	return nil
}

func (m *mockSuspender) ResumeStack(name string) error {
	if m.err != nil {
		return m.err
	}
	m.suspended[name] = false
	return nil
}

func (m *mockSuspender) IsSuspended(name string) bool {
	return m.suspended[name]
}

type mockTriggerer struct {
	lastID      string
	knownStacks map[string]bool // if set, TriggerReconcile errors on unknown stacks
	err         error
}

func newMockTriggerer(id string) *mockTriggerer {
	return &mockTriggerer{lastID: id}
}

func (m *mockTriggerer) TriggerReconcile(ctx context.Context, stackName string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	if m.knownStacks != nil && !m.knownStacks[stackName] {
		return "", &stackNotFoundError{name: stackName}
	}
	return m.lastID, nil
}

func (m *mockTriggerer) TriggerReconcileAll(ctx context.Context) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.lastID, nil
}

// stackNotFoundError is a simple error type returned by the mock triggerer.
type stackNotFoundError struct{ name string }

func (e *stackNotFoundError) Error() string { return "stack not found: " + e.name }

// --- helpers ---

func newTestServer(status *Status, suspender Suspender, triggerer ReconcileTriggerer) *Server {
	return New(":0", status, nil, suspender, triggerer, nil)
}

func do(srv *Server, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	return rec
}

// --- existing healthz tests (updated signature) ---

func TestHealthzReturnsOK(t *testing.T) {
	status := &Status{}
	srv := New(":0", status, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()

	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var resp healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", resp.Status)
	}
	if resp.LastSync != "" {
		t.Errorf("expected empty last_sync before any deploy, got %q", resp.LastSync)
	}
	if resp.Commit != "" {
		t.Errorf("expected empty commit before any deploy, got %q", resp.Commit)
	}
}

func TestHealthzReflectsStatusUpdate(t *testing.T) {
	status := &Status{}
	srv := New(":0", status, nil, nil, nil, nil)

	syncTime := time.Date(2026, 3, 12, 10, 0, 0, 0, time.UTC)
	status.Update(syncTime, "abc1234")

	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()

	srv.httpServer.Handler.ServeHTTP(rec, req)

	var resp healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Commit != "abc1234" {
		t.Errorf("expected commit 'abc1234', got %q", resp.Commit)
	}
	if resp.LastSync != "2026-03-12T10:00:00Z" {
		t.Errorf("expected last_sync '2026-03-12T10:00:00Z', got %q", resp.LastSync)
	}
}

func TestHealthzContentType(t *testing.T) {
	status := &Status{}
	srv := New(":0", status, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()

	srv.httpServer.Handler.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected Content-Type 'application/json', got %q", ct)
	}
}

func TestMetricsEndpointResponds(t *testing.T) {
	status := &Status{}
	srv := New(":0", status, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()

	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if len(body) == 0 {
		t.Error("expected non-empty metrics response")
	}
}

// --- stack API tests ---

func TestGetStacksEmpty(t *testing.T) {
	srv := newTestServer(&Status{}, nil, nil)
	rec := do(srv, "GET", "/api/v1/stacks")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string][]StackState
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	stacks, ok := body["stacks"]
	if !ok {
		t.Fatal("expected 'stacks' key in response")
	}
	if len(stacks) != 0 {
		t.Errorf("expected empty stacks array, got %d items", len(stacks))
	}
	// Ensure it encodes as [] not null.
	raw := rec.Body.String()
	if strings.Contains(raw, "null") {
		t.Errorf("stacks should be [] not null, got: %s", raw)
	}
}

func TestGetStacksWithData(t *testing.T) {
	status := &Status{}
	status.UpdateStack("web", &StackState{
		Name:   "web",
		Host:   "host1",
		Branch: "main",
		Status: "Ready",
		Health: "Healthy",
	})
	status.UpdateStack("api", &StackState{
		Name:   "api",
		Host:   "host1",
		Branch: "main",
		Status: "Ready",
		Health: "Healthy",
	})

	srv := newTestServer(status, nil, nil)
	rec := do(srv, "GET", "/api/v1/stacks")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string][]StackState
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if len(body["stacks"]) != 2 {
		t.Errorf("expected 2 stacks, got %d", len(body["stacks"]))
	}
}

func TestGetStacksContentType(t *testing.T) {
	srv := newTestServer(&Status{}, nil, nil)
	rec := do(srv, "GET", "/api/v1/stacks")

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json, got %q", ct)
	}
}

func TestGetStackNotFound(t *testing.T) {
	srv := newTestServer(&Status{}, nil, nil)
	rec := do(srv, "GET", "/api/v1/stacks/nonexistent")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if !strings.Contains(body["error"], "nonexistent") {
		t.Errorf("expected error to mention stack name, got %q", body["error"])
	}
}

func TestGetStackFound(t *testing.T) {
	status := &Status{}
	status.UpdateStack("web", &StackState{
		Name:   "web",
		Host:   "host1",
		Branch: "main",
		Status: "Ready",
		Health: "Healthy",
		Commit: "deadbeef",
	})

	srv := newTestServer(status, nil, nil)
	rec := do(srv, "GET", "/api/v1/stacks/web")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var stack StackState
	if err := json.NewDecoder(rec.Body).Decode(&stack); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if stack.Name != "web" {
		t.Errorf("expected name 'web', got %q", stack.Name)
	}
	if stack.Commit != "deadbeef" {
		t.Errorf("expected commit 'deadbeef', got %q", stack.Commit)
	}
	if stack.Status != "Ready" {
		t.Errorf("expected status 'Ready', got %q", stack.Status)
	}
}

func TestSuspendStack(t *testing.T) {
	status := &Status{}
	status.UpdateStack("web", &StackState{
		Name:   "web",
		Status: "Ready",
	})

	suspender := newMockSuspender()
	srv := newTestServer(status, suspender, nil)
	rec := do(srv, "POST", "/api/v1/stacks/web/suspend")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var stack StackState
	if err := json.NewDecoder(rec.Body).Decode(&stack); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if !stack.Suspended {
		t.Error("expected Suspended=true")
	}
	if stack.Status != "Suspended" {
		t.Errorf("expected status 'Suspended', got %q", stack.Status)
	}

	// Verify persisted in status.
	persisted := status.GetStack("web")
	if persisted == nil || !persisted.Suspended {
		t.Error("expected status to persist Suspended=true")
	}
}

func TestResumeStack(t *testing.T) {
	status := &Status{}
	status.UpdateStack("web", &StackState{
		Name:      "web",
		Status:    "Suspended",
		Suspended: true,
	})

	suspender := newMockSuspender()
	suspender.suspended["web"] = true
	srv := newTestServer(status, suspender, nil)
	rec := do(srv, "POST", "/api/v1/stacks/web/resume")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var stack StackState
	if err := json.NewDecoder(rec.Body).Decode(&stack); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if stack.Suspended {
		t.Error("expected Suspended=false after resume")
	}
	if stack.Status != "Ready" {
		t.Errorf("expected status 'Ready', got %q", stack.Status)
	}

	persisted := status.GetStack("web")
	if persisted == nil || persisted.Suspended {
		t.Error("expected status to persist Suspended=false")
	}
}

func TestSuspendNotFound(t *testing.T) {
	suspender := newMockSuspender()
	srv := newTestServer(&Status{}, suspender, nil)
	rec := do(srv, "POST", "/api/v1/stacks/ghost/suspend")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !strings.Contains(body["error"], "ghost") {
		t.Errorf("expected error to mention stack name, got %q", body["error"])
	}
}

func TestResumeNotFound(t *testing.T) {
	suspender := newMockSuspender()
	srv := newTestServer(&Status{}, suspender, nil)
	rec := do(srv, "POST", "/api/v1/stacks/ghost/resume")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestSuspendNilSuspender(t *testing.T) {
	status := &Status{}
	status.UpdateStack("web", &StackState{Name: "web"})
	srv := newTestServer(status, nil, nil)
	rec := do(srv, "POST", "/api/v1/stacks/web/suspend")

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", rec.Code)
	}
}

func TestResumeNilSuspender(t *testing.T) {
	status := &Status{}
	status.UpdateStack("web", &StackState{Name: "web"})
	srv := newTestServer(status, nil, nil)
	rec := do(srv, "POST", "/api/v1/stacks/web/resume")

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", rec.Code)
	}
}

func TestReconcileStack(t *testing.T) {
	status := &Status{}
	status.UpdateStack("web", &StackState{Name: "web"})

	triggerer := newMockTriggerer("rec-123")
	srv := newTestServer(status, nil, triggerer)
	rec := do(srv, "POST", "/api/v1/reconcile/web")

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if body["id"] != "rec-123" {
		t.Errorf("expected id 'rec-123', got %q", body["id"])
	}
	if body["status"] != "queued" {
		t.Errorf("expected status 'queued', got %q", body["status"])
	}
	if body["stack"] != "web" {
		t.Errorf("expected stack 'web', got %q", body["stack"])
	}
}

func TestReconcileAll(t *testing.T) {
	triggerer := newMockTriggerer("rec-all-456")
	srv := newTestServer(&Status{}, nil, triggerer)
	rec := do(srv, "POST", "/api/v1/reconcile")

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if body["id"] != "rec-all-456" {
		t.Errorf("expected id 'rec-all-456', got %q", body["id"])
	}
	if body["status"] != "queued" {
		t.Errorf("expected status 'queued', got %q", body["status"])
	}
}

func TestReconcileNotFound(t *testing.T) {
	triggerer := newMockTriggerer("x")
	triggerer.knownStacks = map[string]bool{"web": true}
	srv := newTestServer(&Status{}, nil, triggerer)
	rec := do(srv, "POST", "/api/v1/reconcile/nonexistent")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !strings.Contains(body["error"], "nonexistent") {
		t.Errorf("expected error mentioning stack name, got %q", body["error"])
	}
}

func TestReconcileNilTriggerer(t *testing.T) {
	srv := newTestServer(&Status{}, nil, nil)
	rec := do(srv, "POST", "/api/v1/reconcile/web")

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", rec.Code)
	}
}

func TestReconcileAllNilTriggerer(t *testing.T) {
	srv := newTestServer(&Status{}, nil, nil)
	rec := do(srv, "POST", "/api/v1/reconcile")

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", rec.Code)
	}
}

func TestVersionEndpoint(t *testing.T) {
	srv := newTestServer(&Status{}, nil, nil)
	rec := do(srv, "GET", "/api/v1/version")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if _, ok := body["server"]; !ok {
		t.Error("expected 'server' field in version response")
	}
	if _, ok := body["go"]; !ok {
		t.Error("expected 'go' field in version response")
	}
	if !strings.HasPrefix(body["go"], "go") {
		t.Errorf("expected go version to start with 'go', got %q", body["go"])
	}
}

func TestVersionContentType(t *testing.T) {
	srv := newTestServer(&Status{}, nil, nil)
	rec := do(srv, "GET", "/api/v1/version")

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json, got %q", ct)
	}
}

// TestStatusConcurrency verifies no data races on Status under concurrent access.
func TestStatusConcurrency(t *testing.T) {
	status := &Status{}
	const goroutines = 20

	done := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			name := "stack"
			status.UpdateStack(name, &StackState{Name: name, Status: "Ready"})
			_ = status.GetStack(name)
			_ = status.GetStacks()
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
}
