package git

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Poller struct {
	repoURL   string
	localDir  string
	lastHash  string
	stateFile string
	branch    string
}

func NewPoller(repoURL, localDir string) *Poller {
	return &Poller{
		repoURL:  repoURL,
		localDir: localDir,
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
	return p.saveState()
}

func (p *Poller) LastHash() string {
	return p.lastHash
}

func (p *Poller) SetStateFile(path string) {
	p.stateFile = path
}

func (p *Poller) saveState() error {
	if p.stateFile == "" {
		return nil
	}
	return os.WriteFile(p.stateFile, []byte(p.lastHash), 0644)
}

func (p *Poller) LoadState() error {
	if p.stateFile == "" {
		return nil
	}
	data, err := os.ReadFile(p.stateFile)
	if err != nil {
		return fmt.Errorf("reading state file: %w", err)
	}
	p.lastHash = strings.TrimSpace(string(data))
	return nil
}

func (p *Poller) Fetch() (bool, error) {
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
		return false, fmt.Errorf("git rev-parse origin/main: %w", err)
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
