package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthzReturnsOK(t *testing.T) {
	status := &Status{}
	srv := New(":0", status, nil)

	// httptest.NewRequest creates a fake request (no real network).
	// httptest.NewRecorder captures the response like flask.test_client().
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
	srv := New(":0", status, nil)

	// Simulate a successful deploy updating the status.
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
	srv := New(":0", status, nil)

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
	srv := New(":0", status, nil)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()

	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	// Prometheus metrics endpoint should return text content.
	body := rec.Body.String()
	if len(body) == 0 {
		t.Error("expected non-empty metrics response")
	}
}
