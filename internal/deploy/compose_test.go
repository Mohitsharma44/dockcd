package deploy

import (
	"context"
	"strings"
	"testing"
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
	err := Deploy(context.Background(), stack, "/opt/repo", fakeRunner)
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
		if strings.Contains(cmd, "compose pull") {
			foundPull = true
		}
		if strings.Contains(cmd, "compose up") {
			foundUp = true
		}
	}
	if !foundPull {
		t.Errorf("expected 'docker compose pull', got %v", ranCommands)
	}
	if !foundUp {
		t.Errorf("expected 'docker compose up', got %v", ranCommands)
	}
}

func TestDeployRejectsAbsolutePath(t *testing.T) {
	fakeRunner := func(ctx context.Context, dir string, name string, args ...string) error {
		return nil
	}

	stack := Stack{Name: "evil", Path: "/etc/something"}
	err := Deploy(context.Background(), stack, "/opt/repo", fakeRunner)
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
	err := Deploy(context.Background(), stack, "/opt/repo", fakeRunner)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "escapes repo") {
		t.Fatalf("expected 'escapes repo' error, got %v", err)
	}
}
