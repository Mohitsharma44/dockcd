package deploy

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func containsArg(args []string, target string) bool {
	for _, a := range args {
		if a == target {
			return true
		}
	}
	return false
}

func TestRunGroupsSequentialOrder(t *testing.T) {
	// Track the order stacks are deployed
	var mu sync.Mutex
	var order []string

	fakeRunner := func(ctx context.Context, dir string, name string, args ...string) error {
		// Only track compose up calls
		if containsArg(args, "up") {
			mu.Lock()
			order = append(order, dir)
			mu.Unlock()
		}
		return nil
	}

	groups := [][]Stack{
		{{Name: "a", Path: "a/"}},
		{{Name: "b", Path: "b/"}},
		{{Name: "c", Path: "c/"}},
	}

	err := RunGroups(context.Background(), groups, "/opt/repo", fakeRunner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Groups are sequential, so a must come before b, b before c
	if len(order) != 3 {
		t.Fatalf("expected 3 deploys, got %d: %v", len(order), order)
	}
	if !strings.HasSuffix(order[0], "/a") {
		t.Errorf("expected first deploy to be '/a', got %q", order[0])
	}
	if !strings.HasSuffix(order[2], "/c") {
		t.Errorf("expected last deploy to be '/c', got %q", order[2])
	}
}

func TestRunGroupsParallelWithinGroup(t *testing.T) {
	// Both goroutines signal when they start, then wait for a gate to open.
	// If they're sequential, the second signal would never arrive while
	// the first goroutine is still waiting on the gate.
	started := make(chan string, 2)
	gate := make(chan struct{})

	fakeRunner := func(ctx context.Context, dir string, name string, args ...string) error {
		if containsArg(args, "up") {
			started <- dir // signal: "I've started"
			<-gate         // block until gate opens
		}
		return nil
	}

	groups := [][]Stack{
		{
			{Name: "a", Path: "a/"},
			{Name: "b", Path: "b/"},
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- RunGroups(context.Background(), groups, "/opt/repo", fakeRunner)
	}()

	// Both should signal "started" since they're parallel.
	// If they were sequential, we'd only get one signal (the other
	// would be blocked waiting for the gate).
	<-started
	<-started

	// Let them finish
	close(gate)

	if err := <-done; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunGroupsStopsOnError(t *testing.T) {
	var deployCount int
	var mu sync.Mutex

	fakeRunner := func(ctx context.Context, dir string, name string, args ...string) error {
		if containsArg(args, "up") {
			mu.Lock()
			deployCount++
			mu.Unlock()
			if strings.HasSuffix(dir, "/a") {
				return fmt.Errorf("deploy failed")
			}
		}
		return nil
	}

	groups := [][]Stack{
		{{Name: "a", Path: "a/"}},
		{{Name: "b", Path: "b/"}}, // should never run
	}

	err := RunGroups(context.Background(), groups, "/opt/repo", fakeRunner)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	mu.Lock()
	defer mu.Unlock()
	if deployCount > 1 {
		t.Errorf("expected group 2 to be skipped, but %d deploys ran", deployCount)
	}
}
