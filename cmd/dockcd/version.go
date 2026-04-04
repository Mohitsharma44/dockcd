package main

import (
	"fmt"
	"runtime"
)

func runVersion(args []string) int {
	fmt.Printf("dockcd client: %s (go: %s)\n", Version, runtime.Version())
	return 0
}
