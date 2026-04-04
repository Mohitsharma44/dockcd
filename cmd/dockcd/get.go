package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mohitsharma44/dockcd/internal/client"
	"github.com/mohitsharma44/dockcd/internal/server"
)

func runGet(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: dockcd get <stacks|stack <name>> [flags]")
		return 2
	}
	switch args[0] {
	case "stacks":
		return runGetStacks(args[1:])
	case "stack":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: dockcd get stack <name> [flags]")
			return 2
		}
		return runGetStack(args[1], args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown resource: %s\nUsage: dockcd get <stacks|stack <name>>\n", args[0])
		return 2
	}
}

func runGetStacks(args []string) int {
	fs := flag.NewFlagSet("get stacks", flag.ExitOnError)
	outputFmt := fs.String("output", "table", "output format: table or json")
	watch := fs.Bool("watch", false, "poll and refresh every 2 seconds")
	srv := fs.String("server", resolveServerURL(nil), "dockcd server URL")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles errors

	c := client.New(*srv)
	for {
		stacks, err := c.GetStacks()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if *outputFmt == "json" {
			printJSON(stacks)
		} else {
			printStacksTable(stacks)
		}
		if !*watch {
			return 0
		}
		time.Sleep(2 * time.Second)
		fmt.Print("\033[2J\033[H") // clear screen
	}
}

func runGetStack(name string, args []string) int {
	fs := flag.NewFlagSet("get stack", flag.ExitOnError)
	outputFmt := fs.String("output", "table", "output format: table or json")
	srv := fs.String("server", resolveServerURL(nil), "dockcd server URL")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles errors

	c := client.New(*srv)
	stack, err := c.GetStack(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if *outputFmt == "json" {
		printJSON(stack)
	} else {
		printStacksTable([]server.StackState{*stack})
	}
	return 0
}
