package main

import (
	"os"
	"strings"
)

// resolveServerURL extracts --server flag or falls back to env/default.
func resolveServerURL(args []string) string {
	for i, arg := range args {
		if arg == "--server" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, "--server=") {
			return strings.TrimPrefix(arg, "--server=")
		}
	}
	if url := os.Getenv("DOCKCD_SERVER"); url != "" {
		return url
	}
	return "http://localhost:9092"
}
