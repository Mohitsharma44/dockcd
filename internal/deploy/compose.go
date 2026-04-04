package deploy

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mohitsharma44/dockcd/internal/hooks"
)

// CommandRunner is a function that runs a shell command in a directory.
// In production, this wraps os/exec. In tests, it can be replaced with a fake.
type CommandRunner func(ctx context.Context, dir string, name string, args ...string) error

// Deploy runs docker compose pull + up for a single stack.
// env is passed to any configured pre/post-deploy hooks.
func Deploy(ctx context.Context, stack Stack, repoDir string, run CommandRunner, env hooks.Env) error {
	if filepath.IsAbs(stack.Path) {
		return fmt.Errorf("stack %q: path must be relative, got %q", stack.Name, stack.Path)
	}

	dir := filepath.Join(repoDir, stack.Path)

	// Verify the resolved path is still within repoDir
	resolvedDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("stack %q: resolving path: %w", stack.Name, err)
	}
	resolvedRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return fmt.Errorf("stack %q: resolving repo dir: %w", stack.Name, err)
	}
	if !strings.HasPrefix(resolvedDir, resolvedRepo) {
		return fmt.Errorf("stack %q: path %q escapes repo directory", stack.Name, stack.Path)
	}

	// Run pre-deploy hook before compose commands. Failure skips compose.
	if stack.PreDeployHook != nil {
		if _, err := hooks.Run(ctx, *stack.PreDeployHook, dir, env); err != nil {
			return fmt.Errorf("stack %q: pre-deploy hook: %w", stack.Name, err)
		}
	}

	if err := run(ctx, dir, "docker", "compose", "-p", stack.Name, "pull"); err != nil {
		return fmt.Errorf("stack %q: compose pull: %w", stack.Name, err)
	}

	if err := run(ctx, dir, "docker", "compose", "-p", stack.Name, "up", "-d", "--remove-orphans"); err != nil {
		return fmt.Errorf("stack %q: compose up: %w", stack.Name, err)
	}

	// Run post-deploy hook after compose up. Compose already ran, return error if hook fails.
	if stack.PostDeployHook != nil {
		if _, err := hooks.Run(ctx, *stack.PostDeployHook, dir, env); err != nil {
			return fmt.Errorf("stack %q: post-deploy hook: %w", stack.Name, err)
		}
	}

	return nil
}
