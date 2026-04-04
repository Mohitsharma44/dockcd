//go:build integration

package reconciler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mohitsharma44/dockcd/internal/config"
	"github.com/mohitsharma44/dockcd/internal/deploy"
	"github.com/mohitsharma44/dockcd/internal/git"
	"github.com/mohitsharma44/dockcd/internal/server"
	"github.com/mohitsharma44/dockcd/internal/testutil"
)

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

func TestIntegrationReconcileLoop(t *testing.T) {
	// 1. Create bare repo (the "remote").
	bareDir := testutil.InitBareRepo(t)

	// 2. Clone and push initial stack files.
	workDir := testutil.CloneAndSetup(t, bareDir)
	testutil.CommitFile(t, workDir, "stacks/web/compose.yaml", "services:\n  web:\n    image: nginx", "add web stack")
	testutil.CommitFile(t, workDir, "stacks/db/compose.yaml", "services:\n  db:\n    image: postgres", "add db stack")

	// 3. Create a real git.Poller and clone.
	cloneDir := filepath.Join(t.TempDir(), "dockcd-clone")
	poller := git.NewPoller(bareDir, cloneDir)
	poller.SetStateFile(filepath.Join(cloneDir, ".dockcd_state"))

	// 4. Recording command runner — captures (dir, command) pairs.
	var mu sync.Mutex
	var calls []deployCall
	runner := func(ctx context.Context, dir, name string, args ...string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, deployCall{dir: dir, name: name, args: args})
		return nil
	}

	// 5. Build reconciler with InitialSync disabled (we'll call methods directly).
	stacks := []config.Stack{
		{Name: "web", Path: "stacks/web"},
		{Name: "db", Path: "stacks/db", DependsOn: []string{"web"}},
	}

	status := &server.Status{}
	rec, err := New(Config{
		Poller:       poller,
		HostStacks:   stacks,
		Hostname:     "integration-test",
		BasePath:     "", // stacks are at repo root
		RepoDir:      cloneDir,
		PollInterval: 100 * time.Millisecond,
		Runner:       runner,
		Status:       status,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// 6. initRepo — exercises real git clone.
	if err := rec.initRepo(); err != nil {
		t.Fatalf("initRepo failed: %v", err)
	}

	// 7. deployAll — exercises initial sync path.
	ctx := context.Background()
	if err := rec.deployAll(ctx); err != nil {
		t.Fatalf("deployAll failed: %v", err)
	}

	mu.Lock()
	initialCalls := len(calls)
	mu.Unlock()

	// 2 stacks × 2 commands (pull + up) = 4 calls.
	if initialCalls != 4 {
		t.Fatalf("expected 4 deploy calls from deployAll, got %d", initialCalls)
	}

	// Verify dependency order: web's "up" must come before db's "up".
	mu.Lock()
	var upDirs []string
	for _, c := range calls {
		if len(c.args) > 0 && c.args[0] == "compose" && slices.Contains(c.args, "up") {
			upDirs = append(upDirs, c.dir)
		}
	}
	mu.Unlock()

	if len(upDirs) != 2 {
		t.Fatalf("expected 2 'up' calls, got %d", len(upDirs))
	}
	webDir := filepath.Join(cloneDir, "stacks/web")
	dbDir := filepath.Join(cloneDir, "stacks/db")
	if upDirs[0] != webDir || upDirs[1] != dbDir {
		t.Errorf("expected deploy order [%s, %s], got %v", webDir, dbDir, upDirs)
	}

	// 8. Push a change to only the db stack.
	mu.Lock()
	calls = nil // reset
	mu.Unlock()

	testutil.CommitFile(t, workDir, "stacks/db/compose.yaml",
		"services:\n  db:\n    image: postgres:16", "update db stack")

	// 9. reconcile — should detect the change and deploy only db.
	if err := rec.reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	mu.Lock()
	reconCalls := make([]deployCall, len(calls))
	copy(reconCalls, calls)
	mu.Unlock()

	// Only db should be deployed: 2 calls (pull + up).
	if len(reconCalls) != 2 {
		t.Fatalf("expected 2 deploy calls after incremental change, got %d: %+v", len(reconCalls), reconCalls)
	}
	for _, c := range reconCalls {
		if c.dir != dbDir {
			t.Errorf("expected deploy dir %s, got %s", dbDir, c.dir)
		}
	}

	// 10. Verify status was updated with the latest commit.
	_, commit := status.Snapshot()
	latestHash := testutil.GitOutput(t, workDir, "rev-parse", "HEAD")
	if commit != latestHash {
		t.Errorf("expected status commit %q, got %q", latestHash, commit)
	}
}

func TestIntegrationNoChangesNoDeploy(t *testing.T) {
	bareDir := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bareDir)
	testutil.CommitFile(t, workDir, "stacks/app/compose.yaml", "services:\n  app:\n    image: nginx", "add app")

	cloneDir := filepath.Join(t.TempDir(), "dockcd-clone")
	poller := git.NewPoller(bareDir, cloneDir)
	poller.SetStateFile(filepath.Join(cloneDir, ".dockcd_state"))

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
		Poller:       poller,
		HostStacks:   []config.Stack{{Name: "app", Path: "stacks/app"}},
		Hostname:     "integration-test",
		RepoDir:      cloneDir,
		PollInterval: 100 * time.Millisecond,
		Runner:       runner,
		Status:       status,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if err := rec.initRepo(); err != nil {
		t.Fatalf("initRepo failed: %v", err)
	}

	// No new commits — reconcile should be a no-op.
	if err := rec.reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 0 {
		t.Errorf("expected no deploy calls, got %d", len(calls))
	}
}

func TestIntegrationRunLoopWithInitialSync(t *testing.T) {
	bareDir := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bareDir)
	testutil.CommitFile(t, workDir, "stacks/app/compose.yaml", "services:\n  app:\n    image: nginx", "add app")

	cloneDir := filepath.Join(t.TempDir(), "dockcd-clone")
	poller := git.NewPoller(bareDir, cloneDir)
	poller.SetStateFile(filepath.Join(cloneDir, ".dockcd_state"))

	// Cancel context once we observe the expected deploy calls (avoids waiting for timeout).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var mu sync.Mutex
	var calls []deployCall
	runner := func(rctx context.Context, dir, name string, args ...string) error {
		mu.Lock()
		calls = append(calls, deployCall{dir: dir, name: name, args: args})
		n := len(calls)
		mu.Unlock()
		// 1 stack × 2 commands (pull + up) = 2 calls expected.
		if n >= 2 {
			cancel()
		}
		return nil
	}

	status := &server.Status{}
	rec, err := New(Config{
		Poller:       poller,
		HostStacks:   []config.Stack{{Name: "app", Path: "stacks/app"}},
		Hostname:     "integration-test",
		RepoDir:      cloneDir,
		PollInterval: 100 * time.Millisecond,
		InitialSync:  true,
		Runner:       runner,
		Status:       status,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if err := rec.Run(ctx); err != nil {
		t.Fatalf("Run() failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Initial sync should deploy app (2 calls: pull + up).
	if len(calls) < 2 {
		t.Fatalf("expected at least 2 deploy calls from initial sync, got %d", len(calls))
	}

	// Status should be updated.
	_, commit := status.Snapshot()
	if commit == "" {
		t.Error("expected status commit to be set after initial sync")
	}
}

func TestIntegrationHealthyDeployRecordsSuccessfulCommit(t *testing.T) {
	bareDir := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bareDir)
	goodHash := testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx", "add web stack")

	cloneDir := filepath.Join(t.TempDir(), "dockcd-clone")
	poller := git.NewPoller(bareDir, cloneDir)
	poller.SetStateFile(filepath.Join(cloneDir, ".dockcd_state"))

	var mu sync.Mutex
	var calls []deployCall
	runner := func(ctx context.Context, dir, name string, args ...string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, deployCall{dir: dir, name: name, args: args})
		return nil
	}

	// Output runner that reports healthy containers.
	outputRunner := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		return []byte(`{"Name":"web-1","State":"running","Health":""}` + "\n"), nil
	}

	status := &server.Status{}
	rec, err := New(Config{
		Poller:       poller,
		HostStacks:   []config.Stack{{Name: "web", Path: "stacks/web"}},
		Hostname:     "integration-test",
		RepoDir:      cloneDir,
		PollInterval: 100 * time.Millisecond,
		Runner:       runner,
		OutputRunner: outputRunner,
		Status:       status,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if err := rec.initRepo(); err != nil {
		t.Fatalf("initRepo failed: %v", err)
	}

	// Deploy the stack with health check.
	stack := deploy.Stack{
		Name:               "web",
		Path:               "stacks/web",
		HealthCheckTimeout: 5 * time.Second,
		AutoRollback:       true,
	}

	if err := rec.deployStack(context.Background(), stack, goodHash); err != nil {
		t.Fatalf("deployStack failed: %v", err)
	}

	// Verify lastSuccessfulCommit was recorded via the real poller's state.
	if got := poller.LastSuccessfulCommit("web"); got != goodHash {
		t.Errorf("expected lastSuccessfulCommit=%q, got %q", goodHash, got)
	}
}

func TestIntegrationRollbackToLastGoodCommit(t *testing.T) {
	bareDir := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bareDir)

	// Commit 1: "good" version.
	goodHash := testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx:good", "good deploy")

	// Commit 2: "bad" version.
	testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx:bad", "bad deploy")

	cloneDir := filepath.Join(t.TempDir(), "dockcd-clone")
	poller := git.NewPoller(bareDir, cloneDir)
	poller.SetStateFile(filepath.Join(cloneDir, ".dockcd_state"))

	var mu sync.Mutex
	var calls []deployCall
	runner := func(ctx context.Context, dir, name string, args ...string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, deployCall{dir: dir, name: name, args: args})
		return nil
	}

	// Output runner: always reports unhealthy (simulates bad deploy).
	outputRunner := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		return []byte(`{"Name":"web-1","State":"exited","Health":""}` + "\n"), nil
	}

	status := &server.Status{}
	rec, err := New(Config{
		Poller:       poller,
		HostStacks:   []config.Stack{{Name: "web", Path: "stacks/web"}},
		Hostname:     "integration-test",
		RepoDir:      cloneDir,
		PollInterval: 100 * time.Millisecond,
		Runner:       runner,
		OutputRunner: outputRunner,
		Status:       status,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if err := rec.initRepo(); err != nil {
		t.Fatalf("initRepo failed: %v", err)
	}

	// Manually record the good commit as lastSuccessfulCommit (simulating a prior successful deploy).
	if err := poller.SetLastSuccessfulCommit("web", goodHash); err != nil {
		t.Fatalf("SetLastSuccessfulCommit failed: %v", err)
	}

	// Deploy the "bad" commit with health check that will fail.
	badHash := poller.LastHash()
	stack := deploy.Stack{
		Name:               "web",
		Path:               "stacks/web",
		HealthCheckTimeout: 1 * time.Millisecond, // fail fast
		AutoRollback:       true,
	}

	err = rec.deployStack(context.Background(), stack, badHash)
	// Rollback should succeed — returns ErrRolledBack.
	if !errors.Is(err, ErrRolledBack) {
		t.Fatalf("expected ErrRolledBack, got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Expect: initial deploy (pull+up) + rollback deploy (pull+up) = 4 calls.
	if len(calls) != 4 {
		t.Fatalf("expected 4 calls (deploy + rollback), got %d: %+v", len(calls), calls)
	}

	// The rollback deploy should be from a temp dir (not the repo clone dir).
	rollbackPullDir := calls[2].dir
	rollbackUpDir := calls[3].dir
	if strings.HasPrefix(rollbackPullDir, cloneDir) {
		t.Errorf("rollback should deploy from temp dir, not clone dir: %s", rollbackPullDir)
	}
	if rollbackPullDir != rollbackUpDir {
		t.Errorf("rollback pull and up should use same dir: %s vs %s", rollbackPullDir, rollbackUpDir)
	}

	// lastSuccessfulCommit should still be the good hash (not updated to bad).
	if got := poller.LastSuccessfulCommit("web"); got != goodHash {
		t.Errorf("lastSuccessfulCommit should still be %q, got %q", goodHash, got)
	}
}

func TestIntegrationNoHistoryNoRollback(t *testing.T) {
	bareDir := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bareDir)
	testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx", "first deploy")

	cloneDir := filepath.Join(t.TempDir(), "dockcd-clone")
	poller := git.NewPoller(bareDir, cloneDir)
	poller.SetStateFile(filepath.Join(cloneDir, ".dockcd_state"))

	var mu sync.Mutex
	var calls []deployCall
	runner := func(ctx context.Context, dir, name string, args ...string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, deployCall{dir: dir, name: name, args: args})
		return nil
	}

	outputRunner := func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		return []byte(`{"Name":"web-1","State":"exited","Health":""}` + "\n"), nil
	}

	status := &server.Status{}
	rec, err := New(Config{
		Poller:       poller,
		HostStacks:   []config.Stack{{Name: "web", Path: "stacks/web"}},
		Hostname:     "integration-test",
		RepoDir:      cloneDir,
		PollInterval: 100 * time.Millisecond,
		Runner:       runner,
		OutputRunner: outputRunner,
		Status:       status,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if err := rec.initRepo(); err != nil {
		t.Fatalf("initRepo failed: %v", err)
	}

	// No lastSuccessfulCommit set — first deploy ever.
	stack := deploy.Stack{
		Name:               "web",
		Path:               "stacks/web",
		HealthCheckTimeout: 1 * time.Millisecond,
		AutoRollback:       true,
	}

	err = rec.deployStack(context.Background(), stack, poller.LastHash())
	if err == nil {
		t.Fatal("expected error when no rollback history exists")
	}

	mu.Lock()
	defer mu.Unlock()

	// Should only have the initial deploy (pull+up), no rollback.
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls (deploy only), got %d", len(calls))
	}
}

func TestIntegrationPreDeployHookRuns(t *testing.T) {
	bareDir := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bareDir)

	// Commit both the compose file and a hook script into the stack directory.
	testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx", "add web stack")
	testutil.CommitFile(t, workDir, "stacks/web/hook.sh",
		"#!/bin/sh\necho hook ran\n", "add hook script")

	cloneDir := filepath.Join(t.TempDir(), "dockcd-clone")
	poller := git.NewPoller(bareDir, cloneDir)
	poller.SetStateFile(filepath.Join(cloneDir, ".dockcd_state"))

	var mu sync.Mutex
	var calls []deployCall
	runner := func(ctx context.Context, dir, name string, args ...string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, deployCall{dir: dir, name: name, args: args})
		return nil
	}

	stacks := []config.Stack{{
		Name: "web",
		Path: "stacks/web",
		Hooks: &config.Hooks{
			PreDeploy: &config.HookConfig{
				Command: "sh hook.sh",
				Timeout: 5 * time.Second,
			},
		},
	}}

	status := &server.Status{}
	rec, err := New(Config{
		Poller:       poller,
		HostStacks:   stacks,
		Hostname:     "integration-test",
		RepoDir:      cloneDir,
		PollInterval: 100 * time.Millisecond,
		Runner:       runner,
		Status:       status,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if err := rec.initRepo(); err != nil {
		t.Fatalf("initRepo failed: %v", err)
	}

	ctx := context.Background()
	if err := rec.deployAll(ctx); err != nil {
		t.Fatalf("deployAll failed (hook should have succeeded): %v", err)
	}

	// The hook runs via hooks.Run (not the recording runner), so we verify
	// compose was invoked — meaning the hook succeeded and did not block compose.
	mu.Lock()
	n := len(calls)
	mu.Unlock()

	// 1 stack × 2 commands (pull + up) = 2 calls.
	if n != 2 {
		t.Fatalf("expected 2 compose calls (hook ran, compose proceeded), got %d", n)
	}
}

func TestIntegrationPreDeployHookFailurePreventsCompose(t *testing.T) {
	bareDir := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bareDir)

	testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx", "add web stack")

	cloneDir := filepath.Join(t.TempDir(), "dockcd-clone")
	poller := git.NewPoller(bareDir, cloneDir)
	poller.SetStateFile(filepath.Join(cloneDir, ".dockcd_state"))

	var mu sync.Mutex
	var calls []deployCall
	runner := func(ctx context.Context, dir, name string, args ...string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, deployCall{dir: dir, name: name, args: args})
		return nil
	}

	stacks := []config.Stack{{
		Name: "web",
		Path: "stacks/web",
		Hooks: &config.Hooks{
			PreDeploy: &config.HookConfig{
				Command: "exit 1",
				Timeout: 5 * time.Second,
			},
		},
	}}

	status := &server.Status{}
	rec, err := New(Config{
		Poller:       poller,
		HostStacks:   stacks,
		Hostname:     "integration-test",
		RepoDir:      cloneDir,
		PollInterval: 100 * time.Millisecond,
		Runner:       runner,
		Status:       status,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if err := rec.initRepo(); err != nil {
		t.Fatalf("initRepo failed: %v", err)
	}

	ctx := context.Background()
	if err := rec.deployAll(ctx); err == nil {
		t.Fatal("expected deployAll to return error when pre-deploy hook fails")
	}

	// Hook failed — compose must never have been called.
	mu.Lock()
	n := len(calls)
	mu.Unlock()

	if n != 0 {
		t.Fatalf("expected 0 compose calls when hook fails, got %d", n)
	}
}

func TestIntegrationSuspendSkipsStack(t *testing.T) {
	bareDir := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bareDir)
	testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx", "add web stack")

	cloneDir := filepath.Join(t.TempDir(), "dockcd-clone")
	poller := git.NewPoller(bareDir, cloneDir)
	poller.SetStateFile(filepath.Join(cloneDir, ".dockcd_state"))

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
		Poller:       poller,
		HostStacks:   []config.Stack{{Name: "web", Path: "stacks/web"}},
		Hostname:     "integration-test",
		RepoDir:      cloneDir,
		PollInterval: 100 * time.Millisecond,
		Runner:       runner,
		Status:       status,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if err := rec.initRepo(); err != nil {
		t.Fatalf("initRepo failed: %v", err)
	}

	// Suspend the stack before any reconcile.
	if err := poller.SuspendStack("web"); err != nil {
		t.Fatalf("SuspendStack failed: %v", err)
	}

	// Push a change so reconcile sees a diff.
	testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx:alpine", "update web stack")

	ctx := context.Background()
	if err := rec.reconcile(ctx); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Stack is suspended — no compose calls expected.
	mu.Lock()
	n := len(calls)
	mu.Unlock()

	if n != 0 {
		t.Fatalf("expected 0 compose calls for suspended stack, got %d", n)
	}
}

func TestIntegrationResumeDeploysStack(t *testing.T) {
	bareDir := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bareDir)
	testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx", "add web stack")

	cloneDir := filepath.Join(t.TempDir(), "dockcd-clone")
	poller := git.NewPoller(bareDir, cloneDir)
	poller.SetStateFile(filepath.Join(cloneDir, ".dockcd_state"))

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
		Poller:       poller,
		HostStacks:   []config.Stack{{Name: "web", Path: "stacks/web"}},
		Hostname:     "integration-test",
		RepoDir:      cloneDir,
		PollInterval: 100 * time.Millisecond,
		Runner:       runner,
		Status:       status,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if err := rec.initRepo(); err != nil {
		t.Fatalf("initRepo failed: %v", err)
	}

	// Suspend the stack before any reconcile.
	if err := poller.SuspendStack("web"); err != nil {
		t.Fatalf("SuspendStack failed: %v", err)
	}

	// Push a change while suspended.
	testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx:alpine", "update web stack")

	ctx := context.Background()

	// First reconcile — stack is suspended, should skip.
	if err := rec.reconcile(ctx); err != nil {
		t.Fatalf("reconcile (suspended) failed: %v", err)
	}

	mu.Lock()
	nAfterSuspend := len(calls)
	mu.Unlock()

	if nAfterSuspend != 0 {
		t.Fatalf("expected 0 compose calls while suspended, got %d", nAfterSuspend)
	}

	// Resume the stack.
	if err := poller.ResumeStack("web"); err != nil {
		t.Fatalf("ResumeStack failed: %v", err)
	}

	// Second reconcile — the previously-fetched change is already the current
	// tip, so we push another commit to ensure reconcile detects a diff.
	testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx:latest", "post-resume update")

	if err := rec.reconcile(ctx); err != nil {
		t.Fatalf("reconcile (after resume) failed: %v", err)
	}

	mu.Lock()
	nAfterResume := len(calls)
	mu.Unlock()

	// 1 stack × 2 commands (pull + up) = 2 calls.
	if nAfterResume != 2 {
		t.Fatalf("expected 2 compose calls after resume, got %d", nAfterResume)
	}
}

func TestIntegrationTriggerReconcile(t *testing.T) {
	bareDir := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bareDir)
	testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx", "add web stack")

	cloneDir := filepath.Join(t.TempDir(), "dockcd-clone")
	poller := git.NewPoller(bareDir, cloneDir)
	poller.SetStateFile(filepath.Join(cloneDir, ".dockcd_state"))

	// Use a channel to signal when the async deploy has finished.
	done := make(chan struct{})
	var mu sync.Mutex
	var calls []deployCall
	runner := func(ctx context.Context, dir, name string, args ...string) error {
		mu.Lock()
		calls = append(calls, deployCall{dir: dir, name: name, args: args})
		n := len(calls)
		mu.Unlock()
		// 1 stack × 2 commands (pull + up) = 2 calls.
		if n >= 2 {
			select {
			case <-done: // already closed
			default:
				close(done)
			}
		}
		return nil
	}

	status := &server.Status{}
	rec, err := New(Config{
		Poller:       poller,
		HostStacks:   []config.Stack{{Name: "web", Path: "stacks/web"}},
		Hostname:     "integration-test",
		RepoDir:      cloneDir,
		PollInterval: 100 * time.Millisecond,
		Runner:       runner,
		Status:       status,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if err := rec.initRepo(); err != nil {
		t.Fatalf("initRepo failed: %v", err)
	}

	ctx := context.Background()
	id, err := rec.TriggerReconcile(ctx, "web")
	if err != nil {
		t.Fatalf("TriggerReconcile failed: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty reconcile ID")
	}

	// Wait for the async goroutine to finish, with a generous timeout.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("TriggerReconcile goroutine did not complete in time")
	}

	mu.Lock()
	n := len(calls)
	mu.Unlock()

	// Expect pull + up = 2 calls.
	if n != 2 {
		t.Fatalf("expected 2 compose calls from TriggerReconcile, got %d", n)
	}
}

func TestIntegrationBranchPassedToHookEnv(t *testing.T) {
	bareDir := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bareDir)

	// Use t.Name() in the temp file path to avoid collisions with parallel tests.
	tmpFile := "/tmp/dockcd-test-branch-" + strings.ReplaceAll(t.Name(), "/", "_")
	defer os.Remove(tmpFile)

	// Commit compose file and a hook script that writes $DOCKCD_BRANCH to a temp file.
	testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx", "add web stack")
	testutil.CommitFile(t, workDir, "stacks/web/branch-hook.sh",
		"#!/bin/sh\necho $DOCKCD_BRANCH > "+tmpFile+"\n", "add branch hook")

	cloneDir := filepath.Join(t.TempDir(), "dockcd-clone")
	poller := git.NewPoller(bareDir, cloneDir)
	poller.SetStateFile(filepath.Join(cloneDir, ".dockcd_state"))

	var mu sync.Mutex
	var calls []deployCall
	runner := func(ctx context.Context, dir, name string, args ...string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, deployCall{dir: dir, name: name, args: args})
		return nil
	}

	stacks := []config.Stack{{
		Name:   "web",
		Path:   "stacks/web",
		Branch: "main",
		Hooks: &config.Hooks{
			PreDeploy: &config.HookConfig{
				Command: "sh branch-hook.sh",
				Timeout: 5 * time.Second,
			},
		},
	}}

	status := &server.Status{}
	rec, err := New(Config{
		Poller:        poller,
		HostStacks:    stacks,
		Hostname:      "integration-test",
		RepoDir:       cloneDir,
		DefaultBranch: "main",
		PollInterval:  100 * time.Millisecond,
		Runner:        runner,
		Status:        status,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	if err := rec.initRepo(); err != nil {
		t.Fatalf("initRepo failed: %v", err)
	}

	ctx := context.Background()
	if err := rec.deployAll(ctx); err != nil {
		t.Fatalf("deployAll failed: %v", err)
	}

	// Read the temp file written by the hook and verify it contains "main".
	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("hook did not write temp file %q: %v", tmpFile, err)
	}
	got := strings.TrimSpace(string(data))
	if got != "main" {
		t.Errorf("expected DOCKCD_BRANCH=%q, hook wrote %q", "main", got)
	}
}

func TestIntegrationStateFileMigration(t *testing.T) {
	bareDir := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bareDir)
	hash := testutil.CommitFile(t, workDir, "stacks/web/compose.yaml",
		"services:\n  web:\n    image: nginx", "add web")

	cloneDir := filepath.Join(t.TempDir(), "dockcd-clone")
	stateFile := filepath.Join(cloneDir, ".dockcd_state")

	// Create a legacy plain-text state file.
	poller1 := git.NewPoller(bareDir, cloneDir)
	if err := poller1.Clone(); err != nil {
		t.Fatalf("Clone failed: %v", err)
	}

	// Write legacy format (just the hash).
	if err := writeFile(stateFile, hash); err != nil {
		t.Fatalf("writing legacy state: %v", err)
	}

	// New poller should load legacy format successfully.
	poller2 := git.NewPoller(bareDir, cloneDir)
	poller2.SetStateFile(stateFile)
	if err := poller2.LoadState(); err != nil {
		t.Fatalf("LoadState failed: %v", err)
	}

	if got := poller2.LastHash(); got != hash {
		t.Errorf("expected LastHash=%q, got %q", hash, got)
	}

	// SetLastSuccessfulCommit should upgrade to JSON format.
	if err := poller2.SetLastSuccessfulCommit("web", hash); err != nil {
		t.Fatalf("SetLastSuccessfulCommit failed: %v", err)
	}

	// Load again — should read JSON format.
	poller3 := git.NewPoller(bareDir, cloneDir)
	poller3.SetStateFile(stateFile)
	if err := poller3.LoadState(); err != nil {
		t.Fatalf("LoadState (JSON) failed: %v", err)
	}

	if got := poller3.LastHash(); got != hash {
		t.Errorf("expected LastHash=%q after JSON migration, got %q", hash, got)
	}
	if got := poller3.LastSuccessfulCommit("web"); got != hash {
		t.Errorf("expected LastSuccessfulCommit(web)=%q, got %q", hash, got)
	}
}
