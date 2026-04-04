package deploy

import (
	"context"
	"fmt"

	"github.com/mohitsharma44/dockcd/internal/hooks"
	"golang.org/x/sync/errgroup"
)

// RunGroups deploys stacks group by group. Groups run sequentially,
// but stacks within a group run in parallel. Stops on first error.
func RunGroups(ctx context.Context, groups [][]Stack, repoDir string, run CommandRunner, env hooks.Env) error {
	for i, group := range groups {
		g, ctx := errgroup.WithContext(ctx)

		for _, stack := range group {
			// capture loop variable since it's a goroutine
			stack := stack
			g.Go(func() error {
				return Deploy(ctx, stack, repoDir, run, env)
			})
		}
		if err := g.Wait(); err != nil {
			return fmt.Errorf("group %d: %w", i+1, err)
		}
	}
	return nil
}
