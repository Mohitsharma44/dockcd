package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// InitBareRepo creates a bare git repo and returns its path.
func InitBareRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	cmd := exec.Command("git", "init", "--bare", "--initial-branch=main", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %s\n%s", err, out)
	}
	return dir
}

// GitRun executes a git command in the given directory.
func GitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s\n%s", args, err, out)
	}
}

// GitOutput executes a git command and returns its trimmed stdout.
func GitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// CloneAndSetup clones a bare repo, configures git user, and returns the work dir.
func CloneAndSetup(t *testing.T, bareDir string) string {
	t.Helper()
	workDir := filepath.Join(t.TempDir(), "work")
	GitRun(t, "", "clone", bareDir, workDir)
	GitRun(t, workDir, "config", "user.email", "test@test.com")
	GitRun(t, workDir, "config", "user.name", "Test")
	return workDir
}

// CommitFile writes a file, stages, commits, and pushes. Returns the commit hash.
func CommitFile(t *testing.T, workDir, filePath, content, msg string) string {
	t.Helper()
	fullPath := filepath.Join(workDir, filePath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	GitRun(t, workDir, "add", ".")
	GitRun(t, workDir, "commit", "-m", msg)
	GitRun(t, workDir, "push", "-u", "origin", "HEAD")
	return GitOutput(t, workDir, "rev-parse", "HEAD")
}
