package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mohitsharma44/dockcd/internal/client"
)

func runSuspend(args []string) int {
	if len(args) < 2 || args[0] != "stack" {
		fmt.Fprintln(os.Stderr, "Usage: dockcd suspend stack <name> [flags]")
		return 2
	}
	name := args[1]
	fs := flag.NewFlagSet("suspend", flag.ExitOnError)
	srv := fs.String("server", resolveServerURL(nil), "dockcd server URL")
	fs.Parse(args[2:]) //nolint:errcheck // ExitOnError handles errors

	c := client.New(*srv)
	stack, err := c.Suspend(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("%s stack %q suspended\n", colorize("⏸", "\033[33m"), stack.Name)
	return 0
}
