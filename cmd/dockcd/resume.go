package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mohitsharma44/dockcd/internal/client"
)

func runResume(args []string) int {
	if len(args) < 2 || args[0] != "stack" {
		fmt.Fprintln(os.Stderr, "Usage: dockcd resume stack <name> [flags]")
		return 2
	}
	name := args[1]
	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	srv := fs.String("server", resolveServerURL(nil), "dockcd server URL")
	fs.Parse(args[2:]) //nolint:errcheck // ExitOnError handles errors

	c := client.New(*srv)
	stack, err := c.Resume(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("%s stack %q resumed\n", colorize("✓", "\033[32m"), stack.Name)
	return 0
}
