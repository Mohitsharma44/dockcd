package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mohitsharma44/dockcd/internal/client"
)

func runReconcile(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: dockcd reconcile <stacks|stack <name>> [flags]")
		return 2
	}
	switch args[0] {
	case "stacks":
		return runReconcileAll(args[1:])
	case "stack":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: dockcd reconcile stack <name> [flags]")
			return 2
		}
		return runReconcileStack(args[1], args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown resource: %s\nUsage: dockcd reconcile <stacks|stack <name>>\n", args[0])
		return 2
	}
}

func runReconcileStack(name string, args []string) int {
	fs := flag.NewFlagSet("reconcile stack", flag.ExitOnError)
	noWait := fs.Bool("no-wait", false, "return immediately without waiting")
	srv := fs.String("server", resolveServerURL(nil), "dockcd server URL")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles errors

	c := client.New(*srv)
	resp, err := c.Reconcile(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("%s triggering reconciliation for stack %s\n", colorize("►", "\033[34m"), name)
	if *noWait {
		fmt.Printf("reconcile queued: %s\n", resp.ID)
		return 0
	}
	// SSE streaming will be added in a future iteration.
	fmt.Printf("reconcile started: %s (use 'dockcd get stack %s' to check status)\n", resp.ID, name)
	return 0
}

func runReconcileAll(args []string) int {
	fs := flag.NewFlagSet("reconcile stacks", flag.ExitOnError)
	noWait := fs.Bool("no-wait", false, "return immediately")
	srv := fs.String("server", resolveServerURL(nil), "dockcd server URL")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles errors

	c := client.New(*srv)
	resp, err := c.ReconcileAll()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("%s triggering reconciliation for all stacks\n", colorize("►", "\033[34m"))
	if *noWait {
		fmt.Printf("reconcile queued: %s\n", resp.ID)
		return 0
	}
	fmt.Printf("reconcile started: %s\n", resp.ID)
	return 0
}
