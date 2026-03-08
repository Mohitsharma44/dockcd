package deploy

import "fmt"

// ErrCycleDetected is returned when stacks have circular dependencies.
var ErrCycleDetected = fmt.Errorf("dependency cycle detected")

// Stack represents a deployable unit with optional dependencies.
type Stack struct {
	Name      string   `yaml:"name"`
	Path      string   `yaml:"path"`
	DependsOn []string `yaml:"depends_on"`
}

// BuildGraph takes a list of stacks and returns parallel deployment groups
// using Kahn's topological sort. Each inner slice can be deployed in parallel,
// while outer slices must run sequentially.
func BuildGraph(stacks []Stack) ([][]Stack, error) {
	if len(stacks) == 0 {
		return nil, nil
	}

	// how many things is this stack waiting on?
	waitCount := make(map[string]int, len(stacks))
	for _, st := range stacks {
		waitCount[st.Name] = len(st.DependsOn)
	}

	// Map of stack of dependents
	dependents := make(map[string][]string)
	for _, st := range stacks {
		for _, dep := range st.DependsOn {
			dependents[dep] = append(dependents[dep], st.Name)
		}
	}

	stackByName := make(map[string]Stack, len(stacks))
	for _, st := range stacks {
		stackByName[st.Name] = st
	}

	// Seed queue with stacks that have no dependencies
	var queue []Stack
	for _, st := range stacks {
		if waitCount[st.Name] == 0 {
			queue = append(queue, st)
		}
	}

	// groups collects the result: each entry is a set of stacks that can
	// deploy in parallel. processed tracks how many stacks we've placed
	var groups [][]Stack
	processed := 0
	for len(queue) > 0 {

		// first group is the queue since it has zero dependencies
		// so they can all deploy at the same time.
		groups = append(groups, queue)
		processed += len(queue)

		// For each completed stack, decrement waitCount of its dependents.
		// Any dependent whose waitCount reaches 0 is ready to deploy next.
		var nextQueue []Stack
		for _, stack := range queue {
			for _, dep := range dependents[stack.Name] {
				waitCount[dep]--
				if waitCount[dep] == 0 {
					nextQueue = append(nextQueue, stackByName[dep])
				}
			}
		}
		queue = nextQueue
	}

	// Cycle detection: if some stacks never reached waitCount 0, they're
	// stuck waiting on each other
	if processed != len(stacks) {
		return nil, fmt.Errorf("%w: not all stacks could be ordered", ErrCycleDetected)
	}

	return groups, nil
}
