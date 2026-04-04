package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/mohitsharma44/dockcd/internal/notify"
)

// Version is the server version string. Set by the caller (e.g. main) before
// calling New. Defaults to "dev".
var Version = "dev"

// StackState tracks the runtime state of a single stack.
type StackState struct {
	Name       string    `json:"name"`
	Host       string    `json:"host"`
	Branch     string    `json:"branch"`
	Status     string    `json:"status"` // Ready, Failed, Suspended, Reconciling
	Health     string    `json:"health"` // Healthy, Degraded, Unknown
	LastDeploy time.Time `json:"last_deploy"`
	Commit     string    `json:"commit"`
	Suspended  bool      `json:"suspended"`
}

// ReconcileTriggerer triggers async reconciliation.
type ReconcileTriggerer interface {
	TriggerReconcile(ctx context.Context, stackName string) (string, error)
	TriggerReconcileAll(ctx context.Context) (string, error)
}

// Suspender manages stack suspension state.
type Suspender interface {
	SuspendStack(name string) error
	ResumeStack(name string) error
	IsSuspended(name string) bool
}

// Status holds shared state for the healthz endpoint and stack tracking.
// It is safe for concurrent reads and writes (protected by RWMutex).
type Status struct {
	mu       sync.RWMutex
	lastSync time.Time
	commit   string
	stacks   map[string]*StackState
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

// UpdateStack creates or replaces the state for the named stack.
func (s *Status) UpdateStack(name string, state *StackState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stacks == nil {
		s.stacks = make(map[string]*StackState)
	}
	s.stacks[name] = state
}

// GetStacks returns a snapshot of all tracked stacks. Never returns nil.
func (s *Status) GetStacks() []StackState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]StackState, 0, len(s.stacks))
	for _, st := range s.stacks {
		result = append(result, *st)
	}
	return result
}

// GetStack returns a copy of the named stack's state, or nil if not found.
func (s *Status) GetStack(name string) *StackState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st, ok := s.stacks[name]; ok {
		cp := *st
		return &cp
	}
	return nil
}

// Server wraps an http.Server with dockcd-specific routes.
type Server struct {
	httpServer *http.Server
	status     *Status
	logger     *slog.Logger
	suspender  Suspender
	triggerer  ReconcileTriggerer
	eventBus   *notify.EventBus
}

// New creates a Server listening on the given address (e.g. ":9092").
// suspender, triggerer, and eventBus are optional; pass nil to disable those
// features (affected endpoints return 501 Not Implemented).
//
// Registered routes:
//
//	GET  /metrics                      — Prometheus metrics
//	GET  /healthz                      — JSON health check
//	GET  /api/v1/stacks                — list all stacks
//	GET  /api/v1/stacks/{name}         — get single stack
//	POST /api/v1/stacks/{name}/suspend — suspend a stack
//	POST /api/v1/stacks/{name}/resume  — resume a stack
//	POST /api/v1/reconcile             — trigger reconcile for all stacks
//	POST /api/v1/reconcile/{name}      — trigger reconcile for one stack
//	GET  /api/v1/version               — server version info
func New(addr string, status *Status, logger *slog.Logger, suspender Suspender, triggerer ReconcileTriggerer, eventBus *notify.EventBus) *Server {
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
		status:    status,
		logger:    logger,
		suspender: suspender,
		triggerer: triggerer,
		eventBus:  eventBus,
	}

	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	mux.HandleFunc("GET /api/v1/stacks", s.handleGetStacks)
	mux.HandleFunc("GET /api/v1/stacks/{name}", s.handleGetStack)
	mux.HandleFunc("POST /api/v1/stacks/{name}/suspend", s.handleSuspend)
	mux.HandleFunc("POST /api/v1/stacks/{name}/resume", s.handleResume)
	mux.HandleFunc("POST /api/v1/reconcile", s.handleReconcileAll)
	mux.HandleFunc("POST /api/v1/reconcile/{name}", s.handleReconcile)
	mux.HandleFunc("GET /api/v1/version", s.handleVersion)

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

// handleGetStacks returns all tracked stacks as a JSON array.
func (s *Server) handleGetStacks(w http.ResponseWriter, r *http.Request) {
	stacks := s.status.GetStacks()
	if stacks == nil {
		stacks = []StackState{}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string][]StackState{"stacks": stacks}); err != nil {
		s.logger.Error("failed to encode /api/v1/stacks response", "error", err)
	}
}

// handleGetStack returns a single stack by name.
func (s *Server) handleGetStack(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	stack := s.status.GetStack(name)
	if stack == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "stack not found: " + name})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(stack); err != nil {
		s.logger.Error("failed to encode stack response", "error", err, "stack", name)
	}
}

// handleSuspend suspends the named stack.
func (s *Server) handleSuspend(w http.ResponseWriter, r *http.Request) {
	if s.suspender == nil {
		http.Error(w, "suspend not available", http.StatusNotImplemented)
		return
	}
	name := r.PathValue("name")
	stack := s.status.GetStack(name)
	if stack == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "stack not found: " + name})
		return
	}
	if err := s.suspender.SuspendStack(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stack.Suspended = true
	stack.Status = "Suspended"
	s.status.UpdateStack(name, stack)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(stack); err != nil {
		s.logger.Error("failed to encode suspend response", "error", err, "stack", name)
	}
}

// handleResume resumes the named stack.
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	if s.suspender == nil {
		http.Error(w, "resume not available", http.StatusNotImplemented)
		return
	}
	name := r.PathValue("name")
	stack := s.status.GetStack(name)
	if stack == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "stack not found: " + name})
		return
	}
	if err := s.suspender.ResumeStack(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stack.Suspended = false
	stack.Status = "Ready"
	s.status.UpdateStack(name, stack)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(stack); err != nil {
		s.logger.Error("failed to encode resume response", "error", err, "stack", name)
	}
}

// handleReconcileAll triggers reconciliation for all stacks.
func (s *Server) handleReconcileAll(w http.ResponseWriter, r *http.Request) {
	if s.triggerer == nil {
		http.Error(w, "reconcile not available", http.StatusNotImplemented)
		return
	}
	id, err := s.triggerer.TriggerReconcileAll(r.Context())
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(map[string]string{"id": id, "status": "queued"}); err != nil {
		s.logger.Error("failed to encode reconcile-all response", "error", err)
	}
}

// handleReconcile triggers reconciliation for a single named stack.
func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	if s.triggerer == nil {
		http.Error(w, "reconcile not available", http.StatusNotImplemented)
		return
	}
	name := r.PathValue("name")
	id, err := s.triggerer.TriggerReconcile(r.Context(), name)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(map[string]string{"id": id, "status": "queued", "stack": name}); err != nil {
		s.logger.Error("failed to encode reconcile response", "error", err, "stack", name)
	}
}

// handleVersion returns the server and Go runtime version.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{
		"server": Version,
		"go":     runtime.Version(),
	}); err != nil {
		s.logger.Error("failed to encode version response", "error", err)
	}
}
