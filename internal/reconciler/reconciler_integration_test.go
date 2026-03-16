//go:build integration

package reconciler

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mohitsharma44/dockcd/internal/config"
	"github.com/mohitsharma44/dockcd/internal/git"
	"github.com/mohitsharma44/dockcd/internal/server"
	"github.com/mohitsharma44/dockcd/internal/testutil"
)

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
		if len(c.args) > 1 && c.args[0] == "compose" && c.args[1] == "up" {
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
