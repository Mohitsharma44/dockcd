package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/mohitsharma44/dockcd/internal/server"
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

// useColor reports whether terminal color output should be used.
// Color is disabled when the NO_COLOR environment variable is set.
func useColor() bool {
	return os.Getenv("NO_COLOR") == ""
}

// colorize wraps s in the given ANSI escape code when color is enabled.
func colorize(s, code string) string {
	if !useColor() {
		return s
	}
	return code + s + "\033[0m"
}

// statusIcon returns a colored Unicode icon for the given stack status.
func statusIcon(status string) string {
	switch status {
	case "Ready":
		return colorize("✓", "\033[32m")
	case "Failed":
		return colorize("✗", "\033[31m")
	case "Suspended":
		return colorize("⏸", "\033[33m")
	case "Reconciling":
		return colorize("↻", "\033[34m")
	default:
		return " "
	}
}

// printStacksTable writes a human-readable table of stacks to stdout.
func printStacksTable(stacks []server.StackState) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tBRANCH\tSTATUS\tHEALTH\tLAST DEPLOY\tCOMMIT")
	for _, s := range stacks {
		lastDeploy := "--"
		if !s.LastDeploy.IsZero() {
			lastDeploy = s.LastDeploy.Format("2006-01-02 15:04:05")
		}
		commit := s.Commit
		if len(commit) > 7 {
			commit = commit[:7]
		}
		health := s.Health
		if health == "" {
			health = "--"
		}
		fmt.Fprintf(w, "%s %s\t%s\t%s\t%s\t%s\t%s\n",
			statusIcon(s.Status), s.Name, s.Branch, s.Status, health, lastDeploy, commit)
	}
	w.Flush()
}

// printJSON writes v to stdout as indented JSON.
func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}
