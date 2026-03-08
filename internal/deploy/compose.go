package deploy

import (
	"context"
	"fmt"
	"path/filepath"
)

// CommandRunner is a function that runs a shell command in a directory.
// In production, this wraps os/exec. In tests, it can be replaced with a fake.
type CommandRunner func(ctx context.Context, dir string, name string, args ...string) error

// Deploy runs docker compose pull + up for a single stack.
func Deploy(ctx context.Context, stack Stack, repoDir string, run CommandRunner) error {
	dir := filepath.Join(repoDir, stack.Path)

	if err := run(ctx, dir, "docker", "compose", "pull"); err != nil {
		return fmt.Errorf("stack %q: compose pull: %w", stack.Name, err)
	}

	if err := run(ctx, dir, "docker", "compose", "up", "-d", "--remove-orphans"); err != nil {
		return fmt.Errorf("stack %q: compose up: %w", stack.Name, err)
	}

	return nil
}
