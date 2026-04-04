package git

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// State is the JSON-serialized state file format.
type State struct {
	LastHash              string            `json:"last_hash"`
	LastSuccessfulCommits map[string]string `json:"last_successful_commits"`
	SuspendedStacks       []string          `json:"suspended_stacks,omitempty"`
}

type Poller struct {
	repoURL        string
	localDir       string
	lastHash       string
	stateFile      string
	branch         string
	state          State
	needsReconcile map[string]bool
	mu             sync.Mutex // protects state and file writes
}

func NewPoller(repoURL, localDir string) *Poller {
	return &Poller{
		repoURL:        repoURL,
		localDir:       localDir,
		state:          State{LastSuccessfulCommits: make(map[string]string)},
		needsReconcile: make(map[string]bool),
	}
}

func (p *Poller) Clone() error {
	cmd := exec.Command("git", "clone", p.repoURL, p.localDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone: %w\n%s", err, out)
	}

	// Detect the default branch name from the remote
	cmd = exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = p.localDir
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("detecting default branch: %w", err)
	}
	// output is like "refs/remotes/origin/main" — extract branch name
	ref := strings.TrimSpace(string(out))
	p.branch = strings.TrimPrefix(ref, "refs/remotes/origin/")

	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = p.localDir
	out, err = cmd.Output()
	if err != nil {
		return fmt.Errorf("git rev-parse HEAD after clone: %w", err)
	}
	p.lastHash = strings.TrimSpace(string(out))
	p.state.LastHash = p.lastHash
	return p.saveState()
}

func (p *Poller) LastHash() string {
	return p.lastHash
}

func (p *Poller) SetStateFile(path string) {
	p.stateFile = path
}

func (p *Poller) saveState() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.saveStateLocked()
}

// saveStateLocked writes state to disk. Caller must hold p.mu.
func (p *Poller) saveStateLocked() error {
	if p.stateFile == "" {
		return nil
	}
	p.state.LastHash = p.lastHash
	data, err := json.MarshalIndent(p.state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}
	return os.WriteFile(p.stateFile, data, 0644)
}

// LoadState reads the state file. Supports both the legacy plain-text
// format (just a commit hash) and the new JSON format.
func (p *Poller) LoadState() error {
	if p.stateFile == "" {
		return nil
	}
	data, err := os.ReadFile(p.stateFile)
	if err != nil {
		return fmt.Errorf("reading state file: %w", err)
	}

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		// Legacy format: plain text hash
		state.LastHash = strings.TrimSpace(string(data))
		state.LastSuccessfulCommits = make(map[string]string)
	}
	if state.LastSuccessfulCommits == nil {
		state.LastSuccessfulCommits = make(map[string]string)
	}
	p.state = state
	p.lastHash = state.LastHash
	return nil
}

// LastSuccessfulCommit returns the last commit at which the given stack
// was successfully deployed. Returns empty string if no history.
func (p *Poller) LastSuccessfulCommit(stack string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state.LastSuccessfulCommits[stack]
}

// SetLastSuccessfulCommit records a successful deploy for a stack and persists state.
func (p *Poller) SetLastSuccessfulCommit(stack, hash string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state.LastSuccessfulCommits[stack] = hash
	return p.saveStateLocked()
}

// SuspendStack marks a stack as suspended and persists state.
// Idempotent — suspending an already-suspended stack is a no-op.
func (p *Poller) SuspendStack(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.state.SuspendedStacks {
		if s == name {
			return nil
		}
	}
	p.state.SuspendedStacks = append(p.state.SuspendedStacks, name)
	return p.saveStateLocked()
}

// ResumeStack removes a stack from the suspended set, marks it as needing
// reconcile, and persists state.
// Idempotent — resuming a non-suspended stack is a no-op.
func (p *Poller) ResumeStack(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	filtered := p.state.SuspendedStacks[:0]
	found := false
	for _, s := range p.state.SuspendedStacks {
		if s == name {
			found = true
			continue
		}
		filtered = append(filtered, s)
	}
	if !found {
		return nil
	}
	p.state.SuspendedStacks = filtered
	p.needsReconcile[name] = true
	return p.saveStateLocked()
}

// IsSuspended returns true if the stack is currently suspended.
func (p *Poller) IsSuspended(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.state.SuspendedStacks {
		if s == name {
			return true
		}
	}
	return false
}

// NeedsReconcile returns true if the stack was recently resumed and needs
// a reconcile pass.
func (p *Poller) NeedsReconcile(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.needsReconcile[name]
}

// ClearNeedsReconcile clears the needs-reconcile flag after the stack has
// been reconciled.
func (p *Poller) ClearNeedsReconcile(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.needsReconcile, name)
}

// ExtractAtCommit writes the files under pathPrefix at the given commit to destDir.
// Uses `git show` to avoid modifying the working tree.
func (p *Poller) ExtractAtCommit(commit, pathPrefix, destDir string) error {
	// List files at the commit under the path prefix.
	cmd := exec.Command("git", "ls-tree", "-r", "--name-only", commit, pathPrefix+"/")
	cmd.Dir = p.localDir
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("listing files at %s:%s: %w", commit, pathPrefix, err)
	}

	files := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(files) == 0 || (len(files) == 1 && files[0] == "") {
		return fmt.Errorf("no files found at %s:%s", commit, pathPrefix)
	}

	for _, file := range files {
		// Extract file content.
		cmd = exec.Command("git", "show", commit+":"+file)
		cmd.Dir = p.localDir
		content, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("extracting %s:%s: %w", commit, file, err)
		}

		// Write to destDir, preserving the relative path under pathPrefix.
		relPath, err := filepath.Rel(pathPrefix, file)
		if err != nil {
			return fmt.Errorf("computing relative path for %s: %w", file, err)
		}
		destPath := filepath.Join(destDir, relPath)
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(destPath, content, 0644); err != nil {
			return err
		}
	}
	return nil
}

func (p *Poller) Fetch() (bool, error) {
	// Detect branch if not set (e.g. after restart with LoadState).
	if p.branch == "" {
		cmd := exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD")
		cmd.Dir = p.localDir
		out, err := cmd.Output()
		if err != nil {
			return false, fmt.Errorf("detecting default branch: %w", err)
		}
		p.branch = strings.TrimPrefix(strings.TrimSpace(string(out)), "refs/remotes/origin/")
	}

	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = p.localDir
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	localHead := strings.TrimSpace(string(out))

	cmd = exec.Command("git", "fetch", "origin")
	cmd.Dir = p.localDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git fetch: %w\n%s", err, out)
	}

	cmd = exec.Command("git", "rev-parse", "origin/"+p.branch)
	cmd.Dir = p.localDir
	out, err = cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git rev-parse origin/%s: %w", p.branch, err)
	}
	remoteHead := strings.TrimSpace(string(out))

	if localHead == remoteHead {
		return false, nil
	}

	cmd = exec.Command("git", "pull", "--ff-only")
	cmd.Dir = p.localDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git pull: %w\n%s", err, out)
	}

	p.lastHash = remoteHead
	if err := p.saveState(); err != nil {
		return false, fmt.Errorf("saving state: %w", err)
	}
	return true, nil
}

func (p *Poller) ChangedStacks(sinceHash string, stackPaths map[string]string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", sinceHash, "HEAD")
	cmd.Dir = p.localDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}
	changedFiles := strings.Split(strings.TrimSpace(string(out)), "\n")
	var affected []string
	for name, path := range stackPaths {
		for _, file := range changedFiles {
			if strings.HasPrefix(file, path) {
				affected = append(affected, name)
				break
			}
		}
	}
	return affected, nil
}
