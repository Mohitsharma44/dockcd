package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initBareRepo creates a bare git repo and returns its path.
// This is our "remote" - like github
func initBareRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	cmd := exec.Command("git", "init", "--bare", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %s\n%s", err, out)
	}
	return dir
}

// cloneAndCommit clones the bare repo, adds a file, and commits.
// Returns the clone dir and the commit hash.
func cloneAndCommit(t *testing.T, bareDir, filePath, content string) (string, string) {
	t.Helper()
	workDir := filepath.Join(t.TempDir(), "work")
	run(t, "", "git", "clone", bareDir, workDir)
	run(t, workDir, "git", "config", "user.email", "test@test.com")
	run(t, workDir, "git", "config", "user.name", "Test")

	fullPath := filepath.Join(workDir, filePath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add "+filePath)
	run(t, workDir, "git", "push")

	hash := runOutput(t, workDir, "git", "rev-parse", "HEAD")
	return workDir, hash
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %s\n%s", name, args, err, out)
	}
}

func runOutput(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v: %s", name, args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestClone(t *testing.T) {
	bare := initBareRepo(t)
	cloneAndCommit(t, bare, "stacks/app/compose.yaml", "version: '3'")

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
	bare := initBareRepo(t)
	workDir, _ := cloneAndCommit(t, bare, "stacks/app/compose.yaml", "version: '3'")

	cloneDir := filepath.Join(t.TempDir(), "clone")
	p := NewPoller(bare, cloneDir)

	if err := p.Clone(); err != nil {
		t.Fatalf("Clone() error: %v", err)
	}

	// Simulate a new push from the "developer"
	newFile := filepath.Join(workDir, "stacks/db/compose.yaml")
	if err := os.MkdirAll(filepath.Dir(newFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("version: '3'"), 0644); err != nil {
		t.Fatal(err)
	}
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add db stack")
	run(t, workDir, "git", "push")

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
	bare := initBareRepo(t)
	workDir, hash1 := cloneAndCommit(t, bare, "stacks/app/compose.yaml", "version: '3'")

	cloneDir := filepath.Join(t.TempDir(), "clone")
	p := NewPoller(bare, cloneDir)

	if err := p.Clone(); err != nil {
		t.Fatalf("Clone() error: %v", err)
	}
	// Record the initial commit as the last seen
	p.lastHash = hash1

	// Push changes to only the db stack
	newFile := filepath.Join(workDir, "stacks/db/compose.yaml")
	if err := os.MkdirAll(filepath.Dir(newFile), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte("version: '3'"), 0644); err != nil {
		t.Fatal(err)
	}
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "add db stack")
	run(t, workDir, "git", "push")

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
	bare := initBareRepo(t)
	cloneAndCommit(t, bare, "stacks/app/compose.yaml", "version: '3'")

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
	bare := initBareRepo(t)
	_, expectedHash := cloneAndCommit(t, bare, "stacks/app/compose.yaml", "version: '3'")

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
	bare := initBareRepo(t)
	_, hash1 := cloneAndCommit(t, bare, "stacks/app/compose.yaml", "version: '3'")

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
