package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/mohitsharma44/dockcd/internal/config"
	"github.com/mohitsharma44/dockcd/internal/deploy"
	"github.com/mohitsharma44/dockcd/internal/metrics"
	"github.com/mohitsharma44/dockcd/internal/server"
)

// GitPoller abstracts git operations so the reconciler can be tested
// without real git repos.
type GitPoller interface {
	Clone() error
	Fetch() (bool, error)
	ChangedStacks(sinceHash string, stackPaths map[string]string) ([]string, error)
	LastHash() string
	LoadState() error
}

// Config holds all dependencies for the Reconciler.
type Config struct {
	Poller       GitPoller
	HostStacks   []config.Stack
	Hostname     string
	BasePath     string
	RepoDir      string
	PollInterval time.Duration
	InitialSync  bool
	Runner       deploy.CommandRunner
	Metrics      *metrics.Metrics
	Status       *server.Status
	Logger       *slog.Logger
}

// Reconciler is the core loop that polls git for changes and deploys
// affected stacks in dependency order.
type Reconciler struct {
	poller       GitPoller
	hostStacks   []config.Stack
	hostname     string
	basePath     string
	repoDir      string
	pollInterval time.Duration
	initialSync  bool
	runner       deploy.CommandRunner
	metrics      *metrics.Metrics
	status       *server.Status
	logger       *slog.Logger
}

// New creates a Reconciler from the given config. Returns an error if
// required dependencies (Poller, Runner, Status) are missing.
func New(cfg Config) (*Reconciler, error) {
	if cfg.Poller == nil {
		return nil, fmt.Errorf("Poller is required")
	}
	if cfg.Runner == nil {
		return nil, fmt.Errorf("Runner is required")
	}
	if cfg.Status == nil {
		return nil, fmt.Errorf("Status is required")
	}
	if cfg.PollInterval <= 0 {
		return nil, fmt.Errorf("PollInterval must be positive, got %v", cfg.PollInterval)
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{
		poller:       cfg.Poller,
		hostStacks:   cfg.HostStacks,
		hostname:     cfg.Hostname,
		basePath:     cfg.BasePath,
		repoDir:      cfg.RepoDir,
		pollInterval: cfg.PollInterval,
		initialSync:  cfg.InitialSync,
		runner:       cfg.Runner,
		metrics:      cfg.Metrics,
		status:       cfg.Status,
		logger:       logger,
	}, nil
}

// Run starts the reconcile loop. It clones the repo (or loads state if
// already cloned), optionally performs an initial sync, then polls on
// the configured interval until the context is cancelled.
func (r *Reconciler) Run(ctx context.Context) error {
	if err := r.initRepo(); err != nil {
		return fmt.Errorf("initializing repo: %w", err)
	}

	// Initial sync: deploy all stacks regardless of changes.
	if r.initialSync {
		r.logger.Info("running initial sync")
		if err := r.deployAll(ctx); err != nil {
			r.logger.Error("initial sync failed", "error", err)
		} else {
			r.status.Update(time.Now(), r.poller.LastHash())
			r.logger.Info("initial sync complete")
		}
	}

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.reconcile(ctx); err != nil {
				r.logger.Error("reconcile failed", "error", err)
			}
		}
	}
}

// initRepo clones the repo if it doesn't exist, or loads saved state
// if the clone is already present (e.g. after a restart).
func (r *Reconciler) initRepo() error {
	_, err := os.Stat(r.repoDir)
	if err == nil {
		r.logger.Info("repo already cloned, loading state")
		return r.poller.LoadState()
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("checking repo dir: %w", err)
	}
	r.logger.Info("cloning repository")
	return r.poller.Clone()
}

// reconcile performs a single poll-and-deploy cycle.
func (r *Reconciler) reconcile(ctx context.Context) error {
	prevHash := r.poller.LastHash()

	changed, err := r.poller.Fetch()
	if err != nil {
		r.recordPollMetric(ctx, "error")
		return fmt.Errorf("fetch: %w", err)
	}

	if !changed {
		r.recordPollMetric(ctx, "unchanged")
		return nil
	}

	r.recordPollMetric(ctx, "changed")
	r.logger.Info("changes detected",
		"prev_commit", prevHash,
		"new_commit", r.poller.LastHash(),
	)

	// Update git commit metric.
	r.recordGitCommitMetric(ctx)

	// Determine which stacks have changed files.
	stackPaths := r.buildStackPaths()
	changedNames, err := r.poller.ChangedStacks(prevHash, stackPaths)
	if err != nil {
		return fmt.Errorf("detecting changed stacks: %w", err)
	}

	if len(changedNames) == 0 {
		r.logger.Info("no stacks affected by changes")
		return nil
	}

	r.logger.Info("deploying changed stacks", "stacks", changedNames)

	// Build deploy graph for changed stacks only. Dependencies not in
	// the changed set are stripped (they're already running).
	deployStacks := r.filterChangedStacks(changedNames)
	groups, err := deploy.BuildGraph(deployStacks)
	if err != nil {
		return fmt.Errorf("building deploy graph: %w", err)
	}

	start := time.Now()
	err = deploy.RunGroups(ctx, groups, r.repoDir, r.runner)
	duration := time.Since(start)

	if err != nil {
		r.recordDeployMetric(ctx, "failure", duration)
		return fmt.Errorf("deploy: %w", err)
	}

	r.recordDeployMetric(ctx, "success", duration)
	r.status.Update(time.Now(), r.poller.LastHash())
	r.logger.Info("deploy complete",
		"duration", duration,
		"stacks", changedNames,
	)

	return nil
}

// deployAll deploys all configured stacks in dependency order.
func (r *Reconciler) deployAll(ctx context.Context) error {
	stacks := r.configToDeployStacks(r.hostStacks)
	groups, err := deploy.BuildGraph(stacks)
	if err != nil {
		return fmt.Errorf("building deploy graph: %w", err)
	}
	return deploy.RunGroups(ctx, groups, r.repoDir, r.runner)
}

// buildStackPaths returns a map of stack name → path relative to repo root.
// Used by ChangedStacks to match git diff output against stack paths.
func (r *Reconciler) buildStackPaths() map[string]string {
	paths := make(map[string]string, len(r.hostStacks))
	for _, s := range r.hostStacks {
		paths[s.Name] = filepath.Join(r.basePath, s.Path)
	}
	return paths
}

// filterChangedStacks converts config stacks to deploy stacks, including
// only those in changedNames. Dependencies not in the changed set are
// stripped since they're already deployed and running.
func (r *Reconciler) filterChangedStacks(changedNames []string) []deploy.Stack {
	changedSet := make(map[string]bool, len(changedNames))
	for _, name := range changedNames {
		changedSet[name] = true
	}

	var stacks []deploy.Stack
	for _, cs := range r.hostStacks {
		if !changedSet[cs.Name] {
			continue
		}
		// Only keep deps that are also being deployed.
		var deps []string
		for _, d := range cs.DependsOn {
			if changedSet[d] {
				deps = append(deps, d)
			}
		}
		stacks = append(stacks, deploy.Stack{
			Name:      cs.Name,
			Path:      filepath.Join(r.basePath, cs.Path),
			DependsOn: deps,
		})
	}
	return stacks
}

// configToDeployStacks converts all config stacks to deploy stacks.
func (r *Reconciler) configToDeployStacks(configStacks []config.Stack) []deploy.Stack {
	stacks := make([]deploy.Stack, len(configStacks))
	for i, cs := range configStacks {
		stacks[i] = deploy.Stack{
			Name:      cs.Name,
			Path:      filepath.Join(r.basePath, cs.Path),
			DependsOn: cs.DependsOn,
		}
	}
	return stacks
}

// --- Metrics helpers ---

func (r *Reconciler) recordPollMetric(ctx context.Context, result string) {
	if r.metrics == nil {
		return
	}
	r.metrics.PollTotal.Add(ctx, 1,
		otelmetric.WithAttributes(
			attribute.String("host", r.hostname),
			attribute.String("result", result),
		),
	)
}

func (r *Reconciler) recordDeployMetric(ctx context.Context, result string, duration time.Duration) {
	if r.metrics == nil {
		return
	}
	attrs := otelmetric.WithAttributes(
		attribute.String("host", r.hostname),
		attribute.String("result", result),
	)
	r.metrics.DeployTotal.Add(ctx, 1, attrs)
	r.metrics.DeployDuration.Record(ctx, duration.Seconds(), attrs)
	if result == "success" {
		r.metrics.LastSyncTimestamp.Record(ctx, float64(time.Now().Unix()),
			otelmetric.WithAttributes(attribute.String("host", r.hostname)),
		)
	}
}

func (r *Reconciler) recordGitCommitMetric(ctx context.Context) {
	if r.metrics == nil {
		return
	}
	r.metrics.GitLastCommit.Record(ctx, 1,
		otelmetric.WithAttributes(
			attribute.String("host", r.hostname),
			attribute.String("commit", r.poller.LastHash()),
		),
	)
}
