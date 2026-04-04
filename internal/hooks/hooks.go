package hooks

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// DefaultTimeout is applied when Config.Timeout is zero or negative.
const DefaultTimeout = 30 * time.Second

// Config describes a hook command and its timeout.
type Config struct {
	Command string
	Timeout time.Duration
}

// Env holds the DOCKCD_* environment variables injected into hook commands.
type Env struct {
	StackName string
	Commit    string
	RepoDir   string
	Branch    string
}

// Result captures the stdout and stderr of a hook command.
type Result struct {
	Stdout string
	Stderr string
}

// Run executes cfg.Command via "sh -c" in the given working directory.
// It injects DOCKCD_* environment variables alongside the inherited parent
// environment. The configured timeout (or DefaultTimeout) is enforced via
// a derived context. Both stdout and stderr are captured and returned in
// Result regardless of whether the command succeeds.
func Run(ctx context.Context, cfg Config, dir string, env Env) (Result, error) {
	if cfg.Command == "" {
		return Result{}, fmt.Errorf("hooks: empty command")
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", cfg.Command)
	cmd.Dir = dir

	// Inherit parent environment and append DOCKCD_* variables.
	cmd.Env = append(os.Environ(),
		"DOCKCD_STACK_NAME="+env.StackName,
		"DOCKCD_COMMIT="+env.Commit,
		"DOCKCD_REPO_DIR="+env.RepoDir,
		"DOCKCD_BRANCH="+env.Branch,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	res := Result{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if err != nil {
		return res, fmt.Errorf("hooks: command %q: %w\nstderr: %s", cfg.Command, err, res.Stderr)
	}
	return res, nil
}
