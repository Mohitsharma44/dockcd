package deploy

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunGroupsSequentialOrder(t *testing.T) {
	// Track the order stacks are deployed
	var mu sync.Mutex
	var order []string

	fakeRunner := func(ctx context.Context, dir string, name string, args ...string) error {
		// Only track compose up calls
		if len(args) > 1 && args[1] == "up" {
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
	// Two stacks in the same group should run concurrently
	var mu sync.Mutex
	startTimes := make(map[string]time.Time)

	fakeRunner := func(ctx context.Context, dir string, name string, args ...string) error {
		if len(args) > 1 && args[1] == "up" {
			mu.Lock()
			startTimes[dir] = time.Now()
			mu.Unlock()
			time.Sleep(50 * time.Millisecond) // simulate work
		}
		return nil
	}

	groups := [][]Stack{
		{
			{Name: "a", Path: "a/"},
			{Name: "b", Path: "b/"},
		},
	}

	err := RunGroups(context.Background(), groups, "/opt/repo", fakeRunner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Both should have started within a small window (parallel)
	tA := startTimes["/opt/repo/a/"]
	tB := startTimes["/opt/repo/b/"]
	diff := tA.Sub(tB)
	if diff < 0 {
		diff = -diff
	}
	if diff > 30*time.Millisecond {
		t.Errorf("expected parallel start, but gap was %v", diff)
	}
}

func TestRunGroupsStopsOnError(t *testing.T) {
	var deployCount int
	var mu sync.Mutex

	fakeRunner := func(ctx context.Context, dir string, name string, args ...string) error {
		if len(args) > 1 && args[1] == "up" {
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
