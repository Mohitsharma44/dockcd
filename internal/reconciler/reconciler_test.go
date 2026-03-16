package reconciler

import (
	"context"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/mohitsharma44/dockcd/internal/config"
	"github.com/mohitsharma44/dockcd/internal/server"
)

// --- Mock GitPoller ---

type mockPoller struct {
	cloneErr      error
	fetchChanged  bool
	fetchErr      error
	changedStacks []string
	changedErr    error
	lastHash      string

	cloneCalled bool
	fetchCalled bool
	loadCalled  bool
}

func (m *mockPoller) Clone() error {
	m.cloneCalled = true
	return m.cloneErr
}

func (m *mockPoller) Fetch() (bool, error) {
	m.fetchCalled = true
	return m.fetchChanged, m.fetchErr
}

func (m *mockPoller) ChangedStacks(_ string, _ map[string]string) ([]string, error) {
	return m.changedStacks, m.changedErr
}

func (m *mockPoller) LastHash() string {
	return m.lastHash
}

func (m *mockPoller) LoadState() error {
	m.loadCalled = true
	return nil
}

// --- Mock CommandRunner ---

type deployCall struct {
	dir  string
	name string
	args []string
}

func mockRunner(mu *sync.Mutex, calls *[]deployCall) func(ctx context.Context, dir, name string, args ...string) error {
	return func(ctx context.Context, dir, name string, args ...string) error {
		mu.Lock()
		defer mu.Unlock()
		*calls = append(*calls, deployCall{dir: dir, name: name, args: args})
		return nil
	}
}

// --- Helper to create a Reconciler for tests ---

func testReconciler(t *testing.T, poller *mockPoller, stacks []config.Stack, runner func(ctx context.Context, dir, name string, args ...string) error) (*Reconciler, *server.Status) {
	t.Helper()
	if runner == nil {
		runner = func(ctx context.Context, dir, name string, args ...string) error { return nil }
	}
	status := &server.Status{}
	r, err := New(Config{
		Poller:       poller,
		HostStacks:   stacks,
		Hostname:     "test-host",
		BasePath:     "docker/stacks",
		RepoDir:      "/opt/repo",
		PollInterval: 50 * time.Millisecond,
		Runner:       runner,
		Status:       status,
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	return r, status
}

// --- Tests ---

func TestNewValidatesRequiredDeps(t *testing.T) {
	_, err := New(Config{})
	if err == nil {
		t.Fatal("expected error for missing Poller")
	}

	_, err = New(Config{
		Poller: &mockPoller{},
	})
	if err == nil {
		t.Fatal("expected error for missing Runner")
	}

	_, err = New(Config{
		Poller: &mockPoller{},
		Runner: func(ctx context.Context, dir, name string, args ...string) error { return nil },
	})
	if err == nil {
		t.Fatal("expected error for missing Status")
	}

	_, err = New(Config{
		Poller:       &mockPoller{},
		Runner:       func(ctx context.Context, dir, name string, args ...string) error { return nil },
		Status:       &server.Status{},
		PollInterval: 0,
	})
	if err == nil {
		t.Fatal("expected error for zero PollInterval")
	}
}

func TestInitRepoClones(t *testing.T) {
	poller := &mockPoller{lastHash: "abc123"}
	r, _ := testReconciler(t, poller,nil, nil)
	// Use a temp dir that doesn't exist to trigger clone.
	r.repoDir = "/tmp/dockcd-test-nonexistent-" + t.Name()

	if err := r.initRepo(); err != nil {
		t.Fatalf("initRepo failed: %v", err)
	}
	if !poller.cloneCalled {
		t.Error("expected Clone() to be called")
	}
}

func TestInitRepoLoadsState(t *testing.T) {
	poller := &mockPoller{lastHash: "abc123"}
	r, _ := testReconciler(t, poller,nil, nil)

	// Use a temp dir that exists to trigger LoadState.
	dir := t.TempDir()
	r.repoDir = dir

	if err := r.initRepo(); err != nil {
		t.Fatalf("initRepo failed: %v", err)
	}
	if !poller.loadCalled {
		t.Error("expected LoadState() to be called")
	}
	if poller.cloneCalled {
		t.Error("did not expect Clone() to be called")
	}
}

func TestReconcileNoChanges(t *testing.T) {
	poller := &mockPoller{fetchChanged: false, lastHash: "abc123"}
	var mu sync.Mutex
	var calls []deployCall
	r, _ := testReconciler(t, poller,nil, mockRunner(&mu, &calls))

	if err := r.reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("expected no deploys, got %d", len(calls))
	}
}

func TestReconcileDeploysChangedStacks(t *testing.T) {
	stacks := []config.Stack{
		{Name: "traefik", Path: "server04/traefik"},
		{Name: "vault", Path: "server04/vault", DependsOn: []string{"traefik"}},
	}

	poller := &mockPoller{
		fetchChanged:  true,
		lastHash:      "def456",
		changedStacks: []string{"traefik", "vault"},
	}

	var mu sync.Mutex
	var calls []deployCall
	r, status := testReconciler(t, poller,stacks, mockRunner(&mu, &calls))

	if err := r.reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Should have compose pull + compose up for each stack (4 calls total).
	if len(calls) != 4 {
		t.Fatalf("expected 4 deploy calls, got %d: %+v", len(calls), calls)
	}

	// Verify status was updated.
	_, commit := status.Snapshot()
	if commit != "def456" {
		t.Errorf("expected status commit 'def456', got %q", commit)
	}
}

func TestReconcileOnlyDeploysChangedStacks(t *testing.T) {
	stacks := []config.Stack{
		{Name: "traefik", Path: "server04/traefik"},
		{Name: "vault", Path: "server04/vault", DependsOn: []string{"traefik"}},
	}

	// Only vault changed, not traefik.
	poller := &mockPoller{
		fetchChanged:  true,
		lastHash:      "def456",
		changedStacks: []string{"vault"},
	}

	var mu sync.Mutex
	var calls []deployCall
	r, _ := testReconciler(t, poller,stacks, mockRunner(&mu, &calls))

	if err := r.reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Only vault should be deployed (2 calls: pull + up).
	if len(calls) != 2 {
		t.Fatalf("expected 2 deploy calls, got %d: %+v", len(calls), calls)
	}

	// Verify the deploy dir includes vault's path.
	for _, c := range calls {
		if c.dir != "/opt/repo/docker/stacks/server04/vault" {
			t.Errorf("unexpected deploy dir: %s", c.dir)
		}
	}
}

func TestReconcileStripsDepsNotInChangedSet(t *testing.T) {
	stacks := []config.Stack{
		{Name: "traefik", Path: "server04/traefik"},
		{Name: "vault", Path: "server04/vault", DependsOn: []string{"traefik"}},
	}

	// Only vault changed. Its dependency (traefik) didn't change, so
	// it should be stripped from DependsOn to avoid BuildGraph error.
	poller := &mockPoller{
		fetchChanged:  true,
		lastHash:      "def456",
		changedStacks: []string{"vault"},
	}

	r, _ := testReconciler(t, poller,stacks, func(ctx context.Context, dir, name string, args ...string) error {
		return nil
	})

	filtered := r.filterChangedStacks([]string{"vault"})
	if len(filtered) != 1 {
		t.Fatalf("expected 1 stack, got %d", len(filtered))
	}
	if len(filtered[0].DependsOn) != 0 {
		t.Errorf("expected empty DependsOn, got %v", filtered[0].DependsOn)
	}
}

func TestReconcileFetchError(t *testing.T) {
	poller := &mockPoller{
		fetchErr: os.ErrNotExist,
		lastHash: "abc123",
	}

	r, _ := testReconciler(t, poller,nil, nil)

	err := r.reconcile(context.Background())
	if err == nil {
		t.Fatal("expected error from reconcile")
	}
}

func TestReconcileNoAffectedStacks(t *testing.T) {
	stacks := []config.Stack{
		{Name: "traefik", Path: "server04/traefik"},
	}

	// Git changed but no stack paths were affected.
	poller := &mockPoller{
		fetchChanged:  true,
		lastHash:      "def456",
		changedStacks: []string{},
	}

	var mu sync.Mutex
	var calls []deployCall
	r, _ := testReconciler(t, poller,stacks, mockRunner(&mu, &calls))

	if err := r.reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("expected no deploys, got %d", len(calls))
	}
}

func TestDeployAllDeploysEverything(t *testing.T) {
	stacks := []config.Stack{
		{Name: "traefik", Path: "server04/traefik"},
		{Name: "vault", Path: "server04/vault", DependsOn: []string{"traefik"}},
		{Name: "grafana", Path: "server04/grafana"},
	}

	var mu sync.Mutex
	var calls []deployCall
	r, _ := testReconciler(t, &mockPoller{lastHash: "abc"}, stacks, mockRunner(&mu, &calls))

	if err := r.deployAll(context.Background()); err != nil {
		t.Fatalf("deployAll failed: %v", err)
	}

	// 3 stacks × 2 commands (pull + up) = 6 calls.
	if len(calls) != 6 {
		t.Fatalf("expected 6 deploy calls, got %d", len(calls))
	}
}

func TestBuildStackPaths(t *testing.T) {
	stacks := []config.Stack{
		{Name: "traefik", Path: "server04/traefik"},
		{Name: "vault", Path: "server04/vault"},
	}

	r, _ := testReconciler(t, &mockPoller{}, stacks, nil)
	paths := r.buildStackPaths()

	if paths["traefik"] != "docker/stacks/server04/traefik" {
		t.Errorf("unexpected traefik path: %s", paths["traefik"])
	}
	if paths["vault"] != "docker/stacks/server04/vault" {
		t.Errorf("unexpected vault path: %s", paths["vault"])
	}
}

func TestConfigToDeployStacks(t *testing.T) {
	stacks := []config.Stack{
		{Name: "a", Path: "p/a", DependsOn: []string{"b"}},
		{Name: "b", Path: "p/b"},
	}

	r, _ := testReconciler(t, &mockPoller{}, stacks, nil)
	result := r.configToDeployStacks(stacks)

	if len(result) != 2 {
		t.Fatalf("expected 2 stacks, got %d", len(result))
	}
	if result[0].Name != "a" || result[0].Path != "docker/stacks/p/a" {
		t.Errorf("unexpected stack[0]: %+v", result[0])
	}
	if len(result[0].DependsOn) != 1 || result[0].DependsOn[0] != "b" {
		t.Errorf("expected DependsOn [b], got %v", result[0].DependsOn)
	}
}

func TestRunLoopStopsOnContextCancel(t *testing.T) {
	poller := &mockPoller{
		fetchChanged: false,
		lastHash:     "abc123",
	}

	dir := t.TempDir()
	var mu sync.Mutex
	var calls []deployCall
	r, _ := testReconciler(t, poller,nil, mockRunner(&mu, &calls))
	r.repoDir = dir // exists, so LoadState path

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := r.Run(ctx)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestRunInitialSync(t *testing.T) {
	stacks := []config.Stack{
		{Name: "traefik", Path: "server04/traefik"},
	}

	poller := &mockPoller{lastHash: "abc123"}
	var mu sync.Mutex
	var calls []deployCall
	r, status := testReconciler(t, poller,stacks, mockRunner(&mu, &calls))
	r.repoDir = t.TempDir()
	r.initialSync = true

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_ = r.Run(ctx)

	mu.Lock()
	defer mu.Unlock()
	// Initial sync should deploy traefik (2 calls: pull + up).
	if len(calls) != 2 {
		t.Fatalf("expected 2 deploy calls from initial sync, got %d", len(calls))
	}

	// Status should be updated.
	_, commit := status.Snapshot()
	if commit != "abc123" {
		t.Errorf("expected status commit 'abc123', got %q", commit)
	}
}

func TestDeployOrderRespectsDependencies(t *testing.T) {
	stacks := []config.Stack{
		{Name: "gateway", Path: "gw"},
		{Name: "pangolin", Path: "pg", DependsOn: []string{"gateway"}},
		{Name: "newt", Path: "nt", DependsOn: []string{"pangolin"}},
	}

	poller := &mockPoller{
		fetchChanged:  true,
		lastHash:      "xyz",
		changedStacks: []string{"gateway", "pangolin", "newt"},
	}

	// Track the order stacks are deployed by recording the dir.
	var mu sync.Mutex
	var order []string
	runner := func(ctx context.Context, dir, name string, args ...string) error {
		// Only track "compose up" calls, not "compose pull".
		if len(args) > 0 && args[0] == "compose" && len(args) > 1 && args[1] == "up" {
			mu.Lock()
			order = append(order, dir)
			mu.Unlock()
		}
		return nil
	}

	r, _ := testReconciler(t, poller,stacks, runner)

	if err := r.reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(order) != 3 {
		t.Fatalf("expected 3 'up' calls, got %d: %v", len(order), order)
	}

	// gateway must come before pangolin, pangolin before newt.
	indexOf := func(dir string) int {
		for i, d := range order {
			if d == dir {
				return i
			}
		}
		return -1
	}

	gwIdx := indexOf("/opt/repo/docker/stacks/gw")
	pgIdx := indexOf("/opt/repo/docker/stacks/pg")
	ntIdx := indexOf("/opt/repo/docker/stacks/nt")

	if gwIdx >= pgIdx {
		t.Errorf("gateway (idx=%d) should deploy before pangolin (idx=%d)", gwIdx, pgIdx)
	}
	if pgIdx >= ntIdx {
		t.Errorf("pangolin (idx=%d) should deploy before newt (idx=%d)", pgIdx, ntIdx)
	}
}

func TestFilterChangedStacksReturnsCorrectSet(t *testing.T) {
	stacks := []config.Stack{
		{Name: "a", Path: "p/a"},
		{Name: "b", Path: "p/b", DependsOn: []string{"a"}},
		{Name: "c", Path: "p/c"},
	}

	r, _ := testReconciler(t, &mockPoller{}, stacks, nil)
	result := r.filterChangedStacks([]string{"c", "a"})

	names := make([]string, len(result))
	for i, s := range result {
		names[i] = s.Name
	}
	sort.Strings(names)

	if len(names) != 2 || names[0] != "a" || names[1] != "c" {
		t.Errorf("expected [a, c], got %v", names)
	}
}
