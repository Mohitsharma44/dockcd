package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/mohitsharma44/dockcd/internal/config"
)

func main() {
	configPath := flag.String("config", "gitops.yaml", "path to gitops config file")
	host := flag.String("host", "", "host name (overrides DOCKCD_HOST env and hostname)")
	logFormat := flag.String("log-format", "text", "log format: text or json")
	flag.Parse()

	// Setup logger
	var handler slog.Handler
	switch *logFormat {
	case "json":
		handler = slog.NewJSONHandler(os.Stdout, nil)
	default:
		handler = slog.NewTextHandler(os.Stdout, nil)
	}
	logger := slog.New(handler)

	// Load config
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Resolve hostname
	hostname := *host
	if hostname == "" {
		hostname = os.Getenv("DOCKCD_HOST")
	}
	if hostname == "" {
		hostname, err = os.Hostname()
		if err != nil {
			logger.Error("failed to get hostname", "error", err)
			os.Exit(1)
		}
	}

	// Verify host exists in config
	hostCfg, ok := cfg.Hosts[hostname]
	if !ok {
		logger.Error("host not found in config", "host", hostname)
		os.Exit(1)
	}

	// Setup signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("dockcd starting",
		"host", hostname,
		"repo", cfg.Repo,
		"stacks", len(hostCfg.Stacks),
	)

	// Wait for shutdown signal
	<-ctx.Done()
	logger.Info("shutting down")
}
