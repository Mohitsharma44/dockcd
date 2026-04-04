package deploy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mohitsharma44/dockcd/internal/hooks"
)

func TestDeployRunsComposeUp(t *testing.T) {
	var ranCommands []string

	// Fake command runner that records what was called
	fakeRunner := func(ctx context.Context, dir string, name string, args ...string) error {
		cmd := name + " " + strings.Join(args, " ")
		ranCommands = append(ranCommands, cmd)
		return nil
	}

	stack := Stack{Name: "web", Path: "web/"}
	err := Deploy(context.Background(), stack, "/opt/repo", fakeRunner, hooks.Env{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have run: docker compose pull, then docker compose up
	if len(ranCommands) < 2 {
		t.Fatalf("expected at least 2 commands, got %d: %v", len(ranCommands), ranCommands)
	}

	foundPull := false
	foundUp := false
	for _, cmd := range ranCommands {
		if strings.Contains(cmd, "compose") && strings.Contains(cmd, "pull") {
			foundPull = true
		}
		if strings.Contains(cmd, "compose") && strings.Contains(cmd, "up") {
			foundUp = true
		}
	}
	if !foundPull {
		t.Errorf("expected 'docker compose ... pull', got %v", ranCommands)
	}
	if !foundUp {
		t.Errorf("expected 'docker compose ... up', got %v", ranCommands)
	}
}

func TestDeployRejectsAbsolutePath(t *testing.T) {
	fakeRunner := func(ctx context.Context, dir string, name string, args ...string) error {
		return nil
	}

	stack := Stack{Name: "evil", Path: "/etc/something"}
	err := Deploy(context.Background(), stack, "/opt/repo", fakeRunner, hooks.Env{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "must be relative") {
		t.Fatalf("expected 'must be relative' error, got %v", err)
	}
}

func TestDeployRejectsPathTraversal(t *testing.T) {
	fakeRunner := func(ctx context.Context, dir string, name string, args ...string) error {
		return nil
	}

	stack := Stack{Name: "evil", Path: "../../etc/something"}
	err := Deploy(context.Background(), stack, "/opt/repo", fakeRunner, hooks.Env{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "escapes repo") {
		t.Fatalf("expected 'escapes repo' error, got %v", err)
	}
}

// TestDeployNoHooksBackwardCompat verifies that Deploy without hooks still
// runs exactly 2 compose commands (pull + up).
func TestDeployNoHooksBackwardCompat(t *testing.T) {
	var composeCalls int
	fakeRunner := func(ctx context.Context, dir string, name string, args ...string) error {
		composeCalls++
		return nil
	}

	dir := t.TempDir()
	stack := Stack{Name: "web", Path: "."}
	err := Deploy(context.Background(), stack, dir, fakeRunner, hooks.Env{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if composeCalls != 2 {
		t.Errorf("expected 2 compose calls, got %d", composeCalls)
	}
}

// TestDeployRunsPreDeployHook verifies that a pre-deploy hook runs and compose
// still executes afterward (2 compose calls total).
func TestDeployRunsPreDeployHook(t *testing.T) {
	var composeCalls int
	fakeRunner := func(ctx context.Context, dir string, name string, args ...string) error {
		composeCalls++
		return nil
	}

	dir := t.TempDir()
	stack := Stack{
		Name: "web",
		Path: ".",
		PreDeployHook: &hooks.Config{
			Command: "true", // exits 0
			Timeout: 5 * time.Second,
		},
	}
	err := Deploy(context.Background(), stack, dir, fakeRunner, hooks.Env{
		StackName: "web",
		Commit:    "abc123",
		RepoDir:   dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if composeCalls != 2 {
		t.Errorf("expected 2 compose calls after pre-deploy hook, got %d", composeCalls)
	}
}

// TestDeployPreHookFailureSkipsCompose verifies that a failing pre-deploy hook
// causes Deploy to return an error and no compose commands are run.
func TestDeployPreHookFailureSkipsCompose(t *testing.T) {
	var composeCalls int
	fakeRunner := func(ctx context.Context, dir string, name string, args ...string) error {
		composeCalls++
		return nil
	}

	dir := t.TempDir()
	stack := Stack{
		Name: "web",
		Path: ".",
		PreDeployHook: &hooks.Config{
			Command: "exit 1",
			Timeout: 5 * time.Second,
		},
	}
	err := Deploy(context.Background(), stack, dir, fakeRunner, hooks.Env{
		StackName: "web",
		Commit:    "abc123",
		RepoDir:   dir,
	})
	if err == nil {
		t.Fatal("expected error from failing pre-deploy hook, got nil")
	}
	if !strings.Contains(err.Error(), "pre-deploy hook") {
		t.Errorf("expected error to mention 'pre-deploy hook', got: %v", err)
	}
	if composeCalls != 0 {
		t.Errorf("expected 0 compose calls when pre-deploy hook fails, got %d", composeCalls)
	}
}

// TestDeployPostHookFailureStillDeployed verifies that a failing post-deploy hook
// returns an error but compose commands were still executed (2 calls).
func TestDeployPostHookFailureStillDeployed(t *testing.T) {
	var composeCalls int
	fakeRunner := func(ctx context.Context, dir string, name string, args ...string) error {
		composeCalls++
		return nil
	}

	dir := t.TempDir()
	stack := Stack{
		Name: "web",
		Path: ".",
		PostDeployHook: &hooks.Config{
			Command: "exit 1",
			Timeout: 5 * time.Second,
		},
	}
	err := Deploy(context.Background(), stack, dir, fakeRunner, hooks.Env{
		StackName: "web",
		Commit:    "abc123",
		RepoDir:   dir,
	})
	if err == nil {
		t.Fatal("expected error from failing post-deploy hook, got nil")
	}
	if !strings.Contains(err.Error(), "post-deploy hook") {
		t.Errorf("expected error to mention 'post-deploy hook', got: %v", err)
	}
	// Compose should have run (pull + up = 2 calls) before the post hook failed.
	if composeCalls != 2 {
		t.Errorf("expected 2 compose calls (deploy ran before post hook), got %d", composeCalls)
	}
}
