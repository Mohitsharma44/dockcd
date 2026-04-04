package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
)

func runStats(args []string) int {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	srv := fs.String("server", resolveServerURL(nil), "dockcd server URL")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles errors

	resp, err := http.Get(*srv + "/metrics")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
		fmt.Fprintf(os.Stderr, "error reading response: %v\n", err)
		return 1
	}
	return 0
}
