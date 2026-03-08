package deploy

import (
	"errors"
	"strings"
	"testing"
)

func TestBuildGraphSingleStack(t *testing.T) {
	stacks := []Stack{
		{Name: "web", Path: "web/"},
	}

	groups, err := BuildGraph(stacks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if len(groups[0]) != 1 {
		t.Fatalf("expected 1 stack in group, got %d", len(groups[0]))
	}
	assertGroupNames(t, groups[0], []string{"web"})
}

func TestBuildGraphUnknownDependency(t *testing.T) {
	stacks := []Stack{
		{Name: "app", Path: "app/", DependsOn: []string{"ghost"}},
	}

	_, err := BuildGraph(stacks)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown stack") {
		t.Fatalf("expected unknown stack error, got %v", err)
	}
}

func TestBuildGraphDuplicateName(t *testing.T) {
	stacks := []Stack{
		{Name: "app", Path: "app/"},
		{Name: "app", Path: "app2/"},
	}

	_, err := BuildGraph(stacks)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate stack name") {
		t.Fatalf("expected duplicate name error, got %v", err)
	}
}

func TestBuildGraphLinearChain(t *testing.T) {
	// a → b → c: three groups, one stack each
	stacks := []Stack{
		{Name: "a", Path: "a/"},
		{Name: "b", Path: "b/", DependsOn: []string{"a"}},
		{Name: "c", Path: "c/", DependsOn: []string{"b"}},
	}

	groups, err := BuildGraph(stacks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
	assertGroupNames(t, groups[0], []string{"a"})
	assertGroupNames(t, groups[1], []string{"b"})
	assertGroupNames(t, groups[2], []string{"c"})
}

func TestBuildGraphParallelBranches(t *testing.T) {
	// No dependencies — all stacks deploy in one parallel group
	stacks := []Stack{
		{Name: "web", Path: "web/"},
		{Name: "db", Path: "db/"},
		{Name: "cache", Path: "cache/"},
	}

	groups, err := BuildGraph(stacks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	assertGroupNames(t, groups[0], []string{"web", "db", "cache"})
}

func TestBuildGraphDiamond(t *testing.T) {
	// d → b, d → c, b → a, c → a
	// Groups: [d], [b, c], [a]
	stacks := []Stack{
		{Name: "a", Path: "a/", DependsOn: []string{"b", "c"}},
		{Name: "b", Path: "b/", DependsOn: []string{"d"}},
		{Name: "c", Path: "c/", DependsOn: []string{"d"}},
		{Name: "d", Path: "d/"},
	}

	groups, err := BuildGraph(stacks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
	assertGroupNames(t, groups[0], []string{"d"})
	assertGroupNames(t, groups[1], []string{"b", "c"})
	assertGroupNames(t, groups[2], []string{"a"})
}

func TestBuildGraphRealWorld(t *testing.T) {
	// Simulates the racknerd-aegis config from the plan:
	// gateway → pangolin → [newt, identity]
	stacks := []Stack{
		{Name: "aegis-gateway", Path: "aegis-gateway/"},
		{Name: "aegis-pangolin", Path: "pangolin/", DependsOn: []string{"aegis-gateway"}},
		{Name: "aegis-newt", Path: "newt/", DependsOn: []string{"aegis-pangolin"}},
		{Name: "aegis-identity", Path: "identity/", DependsOn: []string{"aegis-pangolin"}},
	}

	groups, err := BuildGraph(stacks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
	assertGroupNames(t, groups[0], []string{"aegis-gateway"})
	assertGroupNames(t, groups[1], []string{"aegis-pangolin"})
	assertGroupNames(t, groups[2], []string{"aegis-newt", "aegis-identity"})
}

func TestBuildGraphCycle(t *testing.T) {
	stacks := []Stack{
		{Name: "a", Path: "a/", DependsOn: []string{"b"}},
		{Name: "b", Path: "b/", DependsOn: []string{"a"}},
	}

	_, err := BuildGraph(stacks)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrCycleDetected) {
		t.Fatalf("expected ErrCycleDetected, got %v", err)
	}
}

func TestBuildGraphEmpty(t *testing.T) {
	groups, err := BuildGraph([]Stack{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("expected 0 groups, got %d", len(groups))
	}
}

// assertGroupNames checks that a group contains exactly the expected stack names
// (in any order).
func assertGroupNames(t *testing.T, group []Stack, expected []string) {
	t.Helper()
	if len(group) != len(expected) {
		names := stackNames(group)
		t.Fatalf("expected %v, got %v", expected, names)
	}
	nameSet := make(map[string]bool, len(group))
	for _, s := range group {
		nameSet[s.Name] = true
	}
	for _, name := range expected {
		if !nameSet[name] {
			names := stackNames(group)
			t.Fatalf("expected %q in group, got %v", name, names)
		}
	}
}

// stackNames extracts names from a slice of Stacks for readable error messages.
func stackNames(stacks []Stack) []string {
	names := make([]string, len(stacks))
	for i, s := range stacks {
		names[i] = s.Name
	}
	return names
}
