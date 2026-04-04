package git

import (
	"encoding/json"
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

func TestSuspendResumePersistence(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")

	p1 := NewPoller("", "")
	p1.SetStateFile(stateFile)

	// Suspend "app".
	if err := p1.SuspendStack("app"); err != nil {
		t.Fatalf("SuspendStack error: %v", err)
	}
	if !p1.IsSuspended("app") {
		t.Fatal("expected app to be suspended")
	}

	// Reload state in a new poller — suspension must survive restart.
	p2 := NewPoller("", "")
	p2.SetStateFile(stateFile)
	if err := p2.LoadState(); err != nil {
		t.Fatalf("LoadState error: %v", err)
	}
	if !p2.IsSuspended("app") {
		t.Fatal("expected app to still be suspended after reload")
	}
	if p2.NeedsReconcile("app") {
		t.Fatal("freshly loaded poller should not have needsReconcile set")
	}

	// Resume "app" and verify state.
	if err := p2.ResumeStack("app"); err != nil {
		t.Fatalf("ResumeStack error: %v", err)
	}
	if p2.IsSuspended("app") {
		t.Fatal("app should not be suspended after resume")
	}
	if !p2.NeedsReconcile("app") {
		t.Fatal("expected needsReconcile to be set after resume")
	}

	// Reload again — app must no longer appear in SuspendedStacks.
	p3 := NewPoller("", "")
	p3.SetStateFile(stateFile)
	if err := p3.LoadState(); err != nil {
		t.Fatalf("LoadState error: %v", err)
	}
	if p3.IsSuspended("app") {
		t.Fatal("app should not be suspended in p3 after resume+persist")
	}
}

func TestSuspendIdempotent(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")
	p := NewPoller("", "")
	p.SetStateFile(stateFile)

	if err := p.SuspendStack("web"); err != nil {
		t.Fatalf("first SuspendStack error: %v", err)
	}
	if err := p.SuspendStack("web"); err != nil {
		t.Fatalf("second SuspendStack error: %v", err)
	}

	// Should appear exactly once in the suspended list.
	if !p.IsSuspended("web") {
		t.Fatal("web should be suspended")
	}
	if len(p.state.SuspendedStacks) != 1 {
		t.Fatalf("expected 1 entry in SuspendedStacks, got %d", len(p.state.SuspendedStacks))
	}
}

func TestResumeNotSuspended(t *testing.T) {
	p := NewPoller("", "")

	// Resume a stack that was never suspended — should be a no-op with no error.
	if err := p.ResumeStack("ghost"); err != nil {
		t.Fatalf("ResumeStack on non-suspended stack error: %v", err)
	}
	if p.IsSuspended("ghost") {
		t.Fatal("ghost should not be suspended")
	}
	// needsReconcile must NOT be set for a no-op resume.
	if p.NeedsReconcile("ghost") {
		t.Fatal("needsReconcile should not be set for a non-suspended stack resume")
	}
}

func TestLastHashForBranch(t *testing.T) {
	dir := t.TempDir()
	p := NewPoller("", dir)
	p.state.Branches = map[string]string{"main": "aaa", "canary": "bbb"}

	if got := p.LastHashForBranch("main"); got != "aaa" {
		t.Errorf("expected 'aaa', got %q", got)
	}
	if got := p.LastHashForBranch("canary"); got != "bbb" {
		t.Errorf("expected 'bbb', got %q", got)
	}
	if got := p.LastHashForBranch("unknown"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestSetTrackedBranches(t *testing.T) {
	p := NewPoller("", t.TempDir())
	p.SetTrackedBranches([]string{"main", "canary"})
	if len(p.trackedBranches) != 2 {
		t.Fatalf("expected 2 tracked branches, got %d", len(p.trackedBranches))
	}
}

func TestStateMigrationAddsBranches(t *testing.T) {
	dir := t.TempDir()
	stateFile := filepath.Join(dir, ".dockcd_state")

	// Old-format state without branches field.
	data := `{"last_hash":"abc123","last_successful_commits":{"web":"abc123"}}`
	if err := os.WriteFile(stateFile, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	p := NewPoller("", dir)
	p.SetStateFile(stateFile)
	p.branch = "main"
	if err := p.LoadState(); err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if p.state.Branches == nil {
		t.Fatal("expected Branches to be initialized")
	}
	if p.state.Branches["main"] != "abc123" {
		t.Errorf("expected Branches[main]='abc123', got %q", p.state.Branches["main"])
	}
}

func TestBackwardCompatNoSuspendedField(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")

	// Write a state file that lacks the suspended_stacks field entirely
	// (simulating state written by an older version of dockcd).
	oldState := map[string]interface{}{
		"last_hash": "abc123",
		"last_successful_commits": map[string]string{
			"web": "abc123",
		},
	}
	data, err := json.MarshalIndent(oldState, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(stateFile, data, 0644); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	p := NewPoller("", "")
	p.SetStateFile(stateFile)
	if err := p.LoadState(); err != nil {
		t.Fatalf("LoadState error: %v", err)
	}

	if p.IsSuspended("web") {
		t.Fatal("no stack should be suspended when loading old state format")
	}
	if len(p.state.SuspendedStacks) != 0 {
		t.Fatalf("expected empty SuspendedStacks, got %v", p.state.SuspendedStacks)
	}
	if p.LastHash() != "abc123" {
		t.Fatalf("expected LastHash=abc123, got %q", p.LastHash())
	}
}
