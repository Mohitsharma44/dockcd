package git

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mohitsharma44/dockcd/internal/testutil"
)

func TestClone(t *testing.T) {
	bare := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bare)
	testutil.CommitFile(t, workDir, "stacks/app/compose.yaml", "version: '3'", "add stacks/app/compose.yaml")

	cloneDir := filepath.Join(t.TempDir(), "clone")
	p := NewPoller(bare, cloneDir)

	if err := p.Clone(); err != nil {
		t.Fatalf("Clone() error: %v", err)
	}

	// Verify the file exists in the clone
	if _, err := os.Stat(filepath.Join(cloneDir, "stacks/app/compose.yaml")); err != nil {
		t.Fatalf("expected file to exist in clone: %v", err)
	}
}

func TestFetchDetectsNewCommit(t *testing.T) {
	bare := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bare)
	testutil.CommitFile(t, workDir, "stacks/app/compose.yaml", "version: '3'", "add stacks/app/compose.yaml")

	cloneDir := filepath.Join(t.TempDir(), "clone")
	p := NewPoller(bare, cloneDir)

	if err := p.Clone(); err != nil {
		t.Fatalf("Clone() error: %v", err)
	}

	// Simulate a new push from the "developer"
	testutil.CommitFile(t, workDir, "stacks/db/compose.yaml", "version: '3'", "add db stack")

	// Poller should detect the change
	changed, err := p.Fetch()
	if err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}
	if !changed {
		t.Fatal("expected Fetch() to report changes, got false")
	}
}

func TestChangedStacks(t *testing.T) {
	bare := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bare)
	hash1 := testutil.CommitFile(t, workDir, "stacks/app/compose.yaml", "version: '3'", "add stacks/app/compose.yaml")

	cloneDir := filepath.Join(t.TempDir(), "clone")
	p := NewPoller(bare, cloneDir)

	if err := p.Clone(); err != nil {
		t.Fatalf("Clone() error: %v", err)
	}
	// Record the initial commit as the last seen
	p.lastHash = hash1

	// Push changes to only the db stack
	testutil.CommitFile(t, workDir, "stacks/db/compose.yaml", "version: '3'", "add db stack")

	changed, err := p.Fetch()
	if err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}
	if !changed {
		t.Fatal("expected changes")
	}

	// Only "db" should be affected, not "app"
	stackPaths := map[string]string{
		"app": "stacks/app",
		"db":  "stacks/db",
	}
	affected, err := p.ChangedStacks(hash1, stackPaths)
	if err != nil {
		t.Fatalf("ChangedStacks() error: %v", err)
	}

	if len(affected) != 1 {
		t.Fatalf("expected 1 affected stack, got %d: %v", len(affected), affected)
	}
	if affected[0] != "db" {
		t.Fatalf("expected affected stack 'db', got %q", affected[0])
	}
}

func TestFetchNoChanges(t *testing.T) {
	bare := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bare)
	testutil.CommitFile(t, workDir, "stacks/app/compose.yaml", "version: '3'", "add stacks/app/compose.yaml")

	cloneDir := filepath.Join(t.TempDir(), "clone")
	p := NewPoller(bare, cloneDir)

	if err := p.Clone(); err != nil {
		t.Fatalf("Clone() error: %v", err)
	}

	// Fetch without any new commits
	changed, err := p.Fetch()
	if err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}
	if changed {
		t.Fatal("expected no changes, got true")
	}
}

func TestCloneSetsLastHash(t *testing.T) {
	bare := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bare)
	expectedHash := testutil.CommitFile(t, workDir, "stacks/app/compose.yaml", "version: '3'", "add stacks/app/compose.yaml")

	cloneDir := filepath.Join(t.TempDir(), "clone")
	p := NewPoller(bare, cloneDir)

	if err := p.Clone(); err != nil {
		t.Fatalf("Clone() error: %v", err)
	}

	if p.LastHash() != expectedHash {
		t.Fatalf("expected lastHash=%q, got %q", expectedHash, p.LastHash())
	}
}

func TestStateFilePersistence(t *testing.T) {
	bare := testutil.InitBareRepo(t)
	workDir := testutil.CloneAndSetup(t, bare)
	hash1 := testutil.CommitFile(t, workDir, "stacks/app/compose.yaml", "version: '3'", "add stacks/app/compose.yaml")

	stateFile := filepath.Join(t.TempDir(), "state")
	cloneDir := filepath.Join(t.TempDir(), "clone")

	// First poller: clone and save state
	p1 := NewPoller(bare, cloneDir)
	p1.SetStateFile(stateFile)
	if err := p1.Clone(); err != nil {
		t.Fatalf("Clone() error: %v", err)
	}

	if p1.LastHash() != hash1 {
		t.Fatalf("expected lastHash=%q, got %q", hash1, p1.LastHash())
	}

	// Second poller: should load state from file
	p2 := NewPoller(bare, cloneDir)
	p2.SetStateFile(stateFile)
	if err := p2.LoadState(); err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}

	if p2.LastHash() != hash1 {
		t.Fatalf("expected loaded lastHash=%q, got %q", hash1, p2.LastHash())
	}
}
