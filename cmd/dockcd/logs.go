package main

import (
	"bufio"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
)

func runLogs(args []string) int {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	srv := fs.String("server", resolveServerURL(nil), "dockcd server URL")
	fs.Parse(args) //nolint:errcheck // ExitOnError handles errors

	resp, err := http.Get(*srv + "/api/v1/logs")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		select {
		case <-sig:
			return 0
		default:
		}
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			fmt.Println(strings.TrimPrefix(line, "data:"))
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "error reading log stream: %v\n", err)
		return 1
	}
	return 0
}
