package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mohitsharma44/dockcd/internal/server"
)

// sampleStack returns a consistent StackState for use in handler fixtures.
func sampleStack(name string) server.StackState {
	return server.StackState{
		Name:       name,
		Branch:     "main",
		Status:     "Ready",
		Health:     "Healthy",
		LastDeploy: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		Commit:     "abc1234",
	}
}

func TestGetStacks(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/stacks" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"stacks": []server.StackState{sampleStack("web")},
		})
	}))
	defer ts.Close()

	c := New(ts.URL)
	stacks, err := c.GetStacks()
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 1 {
		t.Fatalf("expected 1 stack, got %d", len(stacks))
	}
	if stacks[0].Name != "web" {
		t.Errorf("expected name %q, got %q", "web", stacks[0].Name)
	}
	if stacks[0].Status != "Ready" {
		t.Errorf("expected status %q, got %q", "Ready", stacks[0].Status)
	}
}

func TestGetStacksEmpty(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"stacks": []server.StackState{}})
	}))
	defer ts.Close()

	c := New(ts.URL)
	stacks, err := c.GetStacks()
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 0 {
		t.Errorf("expected 0 stacks, got %d", len(stacks))
	}
}

func TestGetStackNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "stack not found: missing"})
	}))
	defer ts.Close()

	c := New(ts.URL)
	_, err := c.GetStack("missing")
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("expected stack name in error, got: %v", err)
	}
}

func TestGetStackFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/stacks/web" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		json.NewEncoder(w).Encode(sampleStack("web"))
	}))
	defer ts.Close()

	c := New(ts.URL)
	stack, err := c.GetStack("web")
	if err != nil {
		t.Fatal(err)
	}
	if stack.Name != "web" {
		t.Errorf("expected name %q, got %q", "web", stack.Name)
	}
	if stack.Health != "Healthy" {
		t.Errorf("expected health %q, got %q", "Healthy", stack.Health)
	}
}

func TestReconcile(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/reconcile/web" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{
			"id":     "rec-001",
			"status": "queued",
			"stack":  "web",
		})
	}))
	defer ts.Close()

	c := New(ts.URL)
	resp, err := c.Reconcile("web")
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "rec-001" {
		t.Errorf("expected ID %q, got %q", "rec-001", resp.ID)
	}
	if resp.Status != "queued" {
		t.Errorf("expected status %q, got %q", "queued", resp.Status)
	}
	if resp.Stack != "web" {
		t.Errorf("expected stack %q, got %q", "web", resp.Stack)
	}
}

func TestReconcileAll(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/reconcile" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{
			"id":     "rec-all-001",
			"status": "queued",
		})
	}))
	defer ts.Close()

	c := New(ts.URL)
	resp, err := c.ReconcileAll()
	if err != nil {
		t.Fatal(err)
	}
	if resp.ID != "rec-all-001" {
		t.Errorf("expected ID %q, got %q", "rec-all-001", resp.ID)
	}
	if resp.Status != "queued" {
		t.Errorf("expected status %q, got %q", "queued", resp.Status)
	}
}

func TestSuspend(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/stacks/web/suspend" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		st := sampleStack("web")
		st.Suspended = true
		st.Status = "Suspended"
		json.NewEncoder(w).Encode(st)
	}))
	defer ts.Close()

	c := New(ts.URL)
	stack, err := c.Suspend("web")
	if err != nil {
		t.Fatal(err)
	}
	if !stack.Suspended {
		t.Error("expected Suspended=true")
	}
	if stack.Status != "Suspended" {
		t.Errorf("expected status %q, got %q", "Suspended", stack.Status)
	}
}

func TestSuspendNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "stack not found: ghost"})
	}))
	defer ts.Close()

	c := New(ts.URL)
	_, err := c.Suspend("ghost")
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestResume(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/stacks/web/resume" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		st := sampleStack("web")
		st.Suspended = false
		st.Status = "Ready"
		json.NewEncoder(w).Encode(st)
	}))
	defer ts.Close()

	c := New(ts.URL)
	stack, err := c.Resume("web")
	if err != nil {
		t.Fatal(err)
	}
	if stack.Suspended {
		t.Error("expected Suspended=false after resume")
	}
	if stack.Status != "Ready" {
		t.Errorf("expected status %q, got %q", "Ready", stack.Status)
	}
}

func TestResumeNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "stack not found: ghost"})
	}))
	defer ts.Close()

	c := New(ts.URL)
	_, err := c.Resume("ghost")
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestVersion(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/version" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		json.NewEncoder(w).Encode(map[string]string{
			"server": "v0.1.0",
			"go":     "go1.25.0",
		})
	}))
	defer ts.Close()

	c := New(ts.URL)
	v, err := c.Version()
	if err != nil {
		t.Fatal(err)
	}
	if v.Server != "v0.1.0" {
		t.Errorf("expected server version %q, got %q", "v0.1.0", v.Server)
	}
	if v.Go != "go1.25.0" {
		t.Errorf("expected go version %q, got %q", "go1.25.0", v.Go)
	}
}

func TestServerError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		call       func(c *Client) error
	}{
		{
			name:       "GetStacks 500",
			statusCode: http.StatusInternalServerError,
			call:       func(c *Client) error { _, err := c.GetStacks(); return err },
		},
		{
			name:       "GetStack 500",
			statusCode: http.StatusInternalServerError,
			call:       func(c *Client) error { _, err := c.GetStack("web"); return err },
		},
		{
			name:       "Reconcile 500",
			statusCode: http.StatusInternalServerError,
			call:       func(c *Client) error { _, err := c.Reconcile("web"); return err },
		},
		{
			name:       "ReconcileAll 500",
			statusCode: http.StatusInternalServerError,
			call:       func(c *Client) error { _, err := c.ReconcileAll(); return err },
		},
		{
			name:       "Suspend 500",
			statusCode: http.StatusInternalServerError,
			call:       func(c *Client) error { _, err := c.Suspend("web"); return err },
		},
		{
			name:       "Resume 500",
			statusCode: http.StatusInternalServerError,
			call:       func(c *Client) error { _, err := c.Resume("web"); return err },
		},
		{
			name:       "Version 500",
			statusCode: http.StatusInternalServerError,
			call:       func(c *Client) error { _, err := c.Version(); return err },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
			}))
			defer ts.Close()

			c := New(ts.URL)
			err := tt.call(c)
			if err == nil {
				t.Fatal("expected error for 5xx response, got nil")
			}
			if !strings.Contains(err.Error(), "500") {
				t.Errorf("expected status code in error, got: %v", err)
			}
		})
	}
}

func TestConnectionError(t *testing.T) {
	// Point at a port that will refuse connections.
	c := New("http://127.0.0.1:1")

	t.Run("GetStacks connection refused", func(t *testing.T) {
		_, err := c.GetStacks()
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "connecting to server") {
			t.Errorf("expected 'connecting to server' in error, got: %v", err)
		}
	})
}
