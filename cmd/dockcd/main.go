package main

import (
	"fmt"
	"os"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		// No subcommand — default to serve (backward compat).
		os.Exit(runServe(os.Args[1:]))
	}

	switch os.Args[1] {
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	case "version":
		os.Exit(runVersion(os.Args[2:]))
	case "get":
		os.Exit(runGet(os.Args[2:]))
	case "reconcile":
		os.Exit(runReconcile(os.Args[2:]))
	case "suspend":
		os.Exit(runSuspend(os.Args[2:]))
	case "resume":
		os.Exit(runResume(os.Args[2:]))
	case "logs":
		os.Exit(runLogs(os.Args[2:]))
	case "stats":
		os.Exit(runStats(os.Args[2:]))
	default:
		// Check if it looks like a flag — treat as serve for backward compat.
		if len(os.Args[1]) > 0 && os.Args[1][0] == '-' {
			os.Exit(runServe(os.Args[1:]))
		}
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `Usage: dockcd <command> [flags]

Commands:
  serve       Start the dockcd daemon (default if no command given)
  get         Get stack status (get stacks | get stack <name>)
  reconcile   Trigger reconciliation (reconcile stacks | reconcile stack <name>)
  suspend     Suspend a stack (suspend stack <name>)
  resume      Resume a stack (resume stack <name>)
  logs        Stream server logs
  stats       Show metrics summary
  version     Show client and server version`)
}
