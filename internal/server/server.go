package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Status holds shared state for the healthz endpoint.
// It is safe for concurrent reads and writes (protected by RWMutex).
type Status struct {
	mu       sync.RWMutex
	lastSync time.Time
	commit   string
}

// Update sets the last sync time and current commit.
// Called by the deploy runner after a successful deploy.
func (s *Status) Update(lastSync time.Time, commit string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSync = lastSync
	s.commit = commit
}

// healthResponse is the JSON shape returned by /healthz.
type healthResponse struct {
	Status   string `json:"status"`
	LastSync string `json:"last_sync"`
	Commit   string `json:"commit"`
}

// Snapshot reads the current status under a read lock.
func (s *Status) Snapshot() (time.Time, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastSync, s.commit
}

// Server wraps an http.Server with dockcd-specific routes.
type Server struct {
	httpServer *http.Server
	status     *Status
	logger     *slog.Logger
}

// New creates a Server listening on the given address (e.g. ":9092").
// It registers:
//   - GET /metrics  — Prometheus metrics (served by OTel Prometheus exporter)
//   - GET /healthz  — JSON health check with last sync time and commit
func New(addr string, status *Status, logger *slog.Logger) *Server {
	if status == nil {
		status = &Status{}
	}
	if logger == nil {
		logger = slog.Default()
	}

	mux := http.NewServeMux()

	s := &Server{
		httpServer: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		status: status,
		logger: logger,
	}

	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	return s
}

// Start begins listening. It blocks until the server stops.
// Returns http.ErrServerClosed on graceful shutdown (which is expected, not an error).
func (s *Server) Start() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the server, waiting for in-flight requests to finish.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// handleHealthz returns JSON with the current status.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	lastSync, commit := s.status.Snapshot()

	resp := healthResponse{
		Status: "ok",
		Commit: commit,
	}

	if !lastSync.IsZero() {
		resp.LastSync = lastSync.Format(time.RFC3339)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.Error("failed to encode /healthz response", "error", err)
	}
}
