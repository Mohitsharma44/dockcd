package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/errgroup"

	"github.com/mohitsharma44/dockcd/internal/config"
	"github.com/mohitsharma44/dockcd/internal/deploy"
	"github.com/mohitsharma44/dockcd/internal/metrics"
	"github.com/mohitsharma44/dockcd/internal/server"
)

// ErrRolledBack is returned when a stack was rolled back to its last known
// good version. The stack is running but degraded (not at HEAD).
var ErrRolledBack = fmt.Errorf("stack rolled back")

// GitPoller abstracts git operations so the reconciler can be tested
// without real git repos.
type GitPoller interface {
	Clone() error
	Fetch() (bool, error)
	ChangedStacks(sinceHash string, stackPaths map[string]string) ([]string, error)
	LastHash() string
	LoadState() error
	LastSuccessfulCommit(stack string) string
	SetLastSuccessfulCommit(stack, hash string) error
	ExtractAtCommit(commit, pathPrefix, destDir string) error
	SuspendStack(name string) error
	ResumeStack(name string) error
	IsSuspended(name string) bool
	NeedsReconcile(name string) bool
	ClearNeedsReconcile(name string)
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
	OutputRunner deploy.OutputRunner
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
	outputRunner deploy.OutputRunner
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
		outputRunner: cfg.OutputRunner,
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
	commitHash := r.poller.LastHash()
	err = r.deployGroups(ctx, groups, commitHash)
	duration := time.Since(start)

	if err != nil && !errors.Is(err, ErrRolledBack) {
		r.recordDeployMetric(ctx, "failure", duration)
		return fmt.Errorf("deploy: %w", err)
	}

	if errors.Is(err, ErrRolledBack) {
		r.recordDeployMetric(ctx, "partial", duration)
		r.status.Update(time.Now(), r.poller.LastHash())
		r.logger.Warn("deploy complete with rollbacks",
			"duration", duration,
			"stacks", changedNames,
		)
		return nil
	}

	r.recordDeployMetric(ctx, "success", duration)
	r.status.Update(time.Now(), r.poller.LastHash())
	r.logger.Info("deploy complete",
		"duration", duration,
		"stacks", changedNames,
	)

	return nil
}

// deployGroups deploys groups sequentially, stacks within a group in parallel.
// Each stack gets health-checked and optionally rolled back on failure.
// A successful rollback (ErrRolledBack) does not stop deployment of downstream groups.
// Only hard failures (deploy error, failed rollback) halt the pipeline.
func (r *Reconciler) deployGroups(ctx context.Context, groups [][]deploy.Stack, commitHash string) error {
	var rolledBack bool
	for i, group := range groups {
		// Use a plain errgroup (no context cancellation) so that a
		// rollback in one stack doesn't cancel sibling deploys.
		// ErrRolledBack is treated as a non-error inside the goroutine
		// (tracked via atomic flag) so it doesn't mask real failures.
		var g errgroup.Group
		var groupRolledBack atomic.Bool
		for _, stack := range group {
			g.Go(func() error {
				err := r.deployStack(ctx, stack, commitHash)
				if errors.Is(err, ErrRolledBack) {
					groupRolledBack.Store(true)
					return nil // don't mask real errors
				}
				return err
			})
		}
		if err := g.Wait(); err != nil {
			return fmt.Errorf("group %d: %w", i+1, err)
		}
		if groupRolledBack.Load() {
			rolledBack = true
		}
	}
	if rolledBack {
		return ErrRolledBack
	}
	return nil
}

// deployStack deploys a single stack, health-checks it, and rolls back on failure.
func (r *Reconciler) deployStack(ctx context.Context, stack deploy.Stack, commitHash string) error {
	if err := deploy.Deploy(ctx, stack, r.repoDir, r.runner); err != nil {
		r.markStackHealth(ctx, stack.Name, false)
		return err
	}

	// Health check (skip if no OutputRunner or timeout is 0).
	if r.outputRunner != nil && stack.HealthCheckTimeout > 0 {
		dir := filepath.Join(r.repoDir, stack.Path)
		if err := deploy.HealthCheck(ctx, dir, stack.Name, stack.HealthCheckTimeout, r.outputRunner); err != nil {
			r.logger.Warn("health check failed",
				"stack", stack.Name, "error", err)

			if !stack.AutoRollback {
				r.markStackHealth(ctx, stack.Name, false)
				return fmt.Errorf("stack %q unhealthy, auto_rollback disabled: %w", stack.Name, err)
			}

			return r.rollback(ctx, stack, commitHash)
		}
	}

	// Healthy — record successful commit.
	if err := r.poller.SetLastSuccessfulCommit(stack.Name, commitHash); err != nil {
		r.logger.Error("failed to save last successful commit", "stack", stack.Name, "error", err)
	}
	r.markStackHealth(ctx, stack.Name, true)
	return nil
}

// rollback deploys the last known good version of a stack.
func (r *Reconciler) rollback(ctx context.Context, stack deploy.Stack, currentCommit string) error {
	lastGood := r.poller.LastSuccessfulCommit(stack.Name)
	if lastGood == "" {
		r.markStackHealth(ctx, stack.Name, false)
		r.recordRollbackMetric(ctx, stack.Name, "failure")
		return fmt.Errorf("stack %q: no previous successful commit to rollback to", stack.Name)
	}
	if lastGood == currentCommit {
		r.markStackHealth(ctx, stack.Name, false)
		r.recordRollbackMetric(ctx, stack.Name, "failure")
		return fmt.Errorf("stack %q: current commit IS the last successful commit", stack.Name)
	}

	r.logger.Info("rolling back",
		"stack", stack.Name,
		"from_commit", currentCommit,
		"to_commit", lastGood,
	)

	tmpDir, err := os.MkdirTemp("", "dockcd-rollback-*")
	if err != nil {
		r.markStackHealth(ctx, stack.Name, false)
		r.recordRollbackMetric(ctx, stack.Name, "failure")
		return fmt.Errorf("creating temp dir for rollback: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := r.poller.ExtractAtCommit(lastGood, stack.Path, tmpDir); err != nil {
		r.markStackHealth(ctx, stack.Name, false)
		r.recordRollbackMetric(ctx, stack.Name, "failure")
		return fmt.Errorf("extracting files at %s: %w", lastGood, err)
	}

	// Deploy from temp dir using a stack with path "." since files are at root.
	rollbackStack := deploy.Stack{Name: stack.Name, Path: "."}
	if err := deploy.Deploy(ctx, rollbackStack, tmpDir, r.runner); err != nil {
		r.markStackHealth(ctx, stack.Name, false)
		r.recordRollbackMetric(ctx, stack.Name, "failure")
		return fmt.Errorf("rollback deploy failed for %q: %w", stack.Name, err)
	}

	// Rollback succeeded — stack is running but degraded (not at HEAD).
	r.markStackHealth(ctx, stack.Name, false)
	r.recordRollbackMetric(ctx, stack.Name, "success")
	r.logger.Warn("rollback complete, stack is degraded",
		"stack", stack.Name, "running_commit", lastGood)
	return fmt.Errorf("stack %q: %w to %s", stack.Name, ErrRolledBack, lastGood)
}

// deployAll deploys all configured stacks in dependency order.
// Uses deployGroups to run health checks and establish rollback history.
func (r *Reconciler) deployAll(ctx context.Context) error {
	stacks := r.configToDeployStacks(r.hostStacks)
	groups, err := deploy.BuildGraph(stacks)
	if err != nil {
		return fmt.Errorf("building deploy graph: %w", err)
	}
	commitHash := r.poller.LastHash()
	err = r.deployGroups(ctx, groups, commitHash)
	if errors.Is(err, ErrRolledBack) {
		return nil // some stacks rolled back but are running
	}
	return err
}

// buildStackPaths returns a map of stack name → path relative to repo root.
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
		if r.poller.IsSuspended(cs.Name) {
			r.logger.Info("skipping suspended stack", "stack", cs.Name)
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
			Name:               cs.Name,
			Path:               filepath.Join(r.basePath, cs.Path),
			DependsOn:          deps,
			HealthCheckTimeout: cs.HealthTimeout(),
			AutoRollback:       cs.RollbackEnabled(),
		})
	}
	return stacks
}

// configToDeployStacks converts all config stacks to deploy stacks.
func (r *Reconciler) configToDeployStacks(configStacks []config.Stack) []deploy.Stack {
	stacks := make([]deploy.Stack, len(configStacks))
	for i, cs := range configStacks {
		stacks[i] = deploy.Stack{
			Name:               cs.Name,
			Path:               filepath.Join(r.basePath, cs.Path),
			DependsOn:          cs.DependsOn,
			HealthCheckTimeout: cs.HealthTimeout(),
			AutoRollback:       cs.RollbackEnabled(),
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

func (r *Reconciler) markStackHealth(ctx context.Context, stackName string, healthy bool) {
	if r.metrics == nil {
		return
	}
	val := 0.0
	if healthy {
		val = 1.0
	}
	r.metrics.StackHealthy.Record(ctx, val,
		otelmetric.WithAttributes(
			attribute.String("host", r.hostname),
			attribute.String("stack", stackName),
		),
	)
}

func (r *Reconciler) recordRollbackMetric(ctx context.Context, stackName, result string) {
	if r.metrics == nil {
		return
	}
	r.metrics.RollbackTotal.Add(ctx, 1,
		otelmetric.WithAttributes(
			attribute.String("host", r.hostname),
			attribute.String("stack", stackName),
			attribute.String("result", result),
		),
	)
}
