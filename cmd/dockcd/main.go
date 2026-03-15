package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/mohitsharma44/dockcd/internal/config"
	"github.com/mohitsharma44/dockcd/internal/git"
	"github.com/mohitsharma44/dockcd/internal/metrics"
	"github.com/mohitsharma44/dockcd/internal/reconciler"
	"github.com/mohitsharma44/dockcd/internal/server"
)

// tracerShutdown is implemented by both sdktrace.TracerProvider and noopShutdown.
type tracerShutdown interface {
	Shutdown(ctx context.Context) error
}

// noopShutdown wraps a noop TracerProvider with a no-op Shutdown method.
type noopShutdown struct{}

func (noopShutdown) Shutdown(context.Context) error { return nil }

// setupTracing creates an OTel TracerProvider. If DOCKCD_OTEL_ENDPOINT is set,
// traces are exported via OTLP gRPC (e.g. to Alloy → Tempo). If unset, a true
// no-op provider is used (zero overhead).
func setupTracing(ctx context.Context, logger *slog.Logger) (tracerShutdown, error) {
	endpoint := os.Getenv("DOCKCD_OTEL_ENDPOINT")

	// No endpoint configured — use a true no-op TracerProvider (zero overhead).
	if endpoint == "" {
		logger.Info("tracing disabled (DOCKCD_OTEL_ENDPOINT not set)")
		otel.SetTracerProvider(noop.NewTracerProvider())
		return noopShutdown{}, nil
	}

	// Create OTLP gRPC exporter pointing at the configured endpoint.
	// WithInsecure() because internal networks (Alloy) typically don't use TLS.
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("creating OTLP trace exporter: %w", err)
	}

	// Resource describes "who is sending these traces" — like adding
	// service.name and service.version tags to every span.
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("dockcd"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("creating trace resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	logger.Info("tracing enabled", "endpoint", endpoint)
	return tp, nil
}

// execCommand runs a shell command in the given directory.
// Used as the production deploy.CommandRunner.
func execCommand(ctx context.Context, dir string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w\n%s", name, args, err, out)
	}
	return nil
}

func main() {
	configPath := flag.String("config", "gitops.yaml", "path to gitops config file")
	host := flag.String("host", "", "host name (overrides DOCKCD_HOST env and hostname)")
	logFormat := flag.String("log-format", "text", "log format: text or json")
	metricsPort := flag.Int("metrics-port", 0, "port for metrics and health HTTP server (default 9092, or DOCKCD_METRICS_PORT)")
	repoDir := flag.String("repo-dir", "", "local git clone path (default /opt/dockcd/repo, or DOCKCD_REPO_DIR)")
	pollInterval := flag.Duration("poll-interval", 0, "git poll interval (default 30s, or DOCKCD_POLL_INTERVAL)")
	initialSync := flag.Bool("initial-sync", false, "deploy all stacks on first run (or DOCKCD_INITIAL_SYNC=true)")
	flag.Parse()

	// Resolve metrics port: flag > env var > default 9092.
	if *metricsPort == 0 {
		if envPort := os.Getenv("DOCKCD_METRICS_PORT"); envPort != "" {
			p, err := strconv.Atoi(envPort)
			if err != nil {
				slog.Error("invalid DOCKCD_METRICS_PORT", "value", envPort, "error", err)
				os.Exit(1)
			}
			*metricsPort = p
		} else {
			*metricsPort = 9092
		}
	}

	// Resolve repo dir: flag > env var > default.
	if *repoDir == "" {
		if envDir := os.Getenv("DOCKCD_REPO_DIR"); envDir != "" {
			*repoDir = envDir
		} else {
			*repoDir = "/opt/dockcd/repo"
		}
	}

	// Resolve poll interval: flag > env var > default 30s.
	if *pollInterval == 0 {
		if envInterval := os.Getenv("DOCKCD_POLL_INTERVAL"); envInterval != "" {
			d, err := time.ParseDuration(envInterval)
			if err != nil {
				slog.Error("invalid DOCKCD_POLL_INTERVAL", "value", envInterval, "error", err)
				os.Exit(1)
			}
			*pollInterval = d
		} else {
			*pollInterval = 30 * time.Second
		}
	}

	// Resolve initial sync: flag > env var.
	if !*initialSync {
		*initialSync = os.Getenv("DOCKCD_INITIAL_SYNC") == "true"
	}

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

	// Setup metrics — creates OTel MeterProvider backed by Prometheus exporter.
	meterProvider, err := metrics.Setup()
	if err != nil {
		logger.Error("failed to setup metrics", "error", err)
		os.Exit(1)
	}

	meter := meterProvider.Meter("dockcd")
	m, err := metrics.New(meter)
	if err != nil {
		logger.Error("failed to create metrics", "error", err)
		os.Exit(1)
	}

	// Setup tracing — exports spans via OTLP gRPC if DOCKCD_OTEL_ENDPOINT is set.
	// If unset, tracing is a no-op (zero overhead).
	tracerProvider, err := setupTracing(context.Background(), logger)
	if err != nil {
		logger.Error("failed to setup tracing", "error", err)
		os.Exit(1)
	}

	// Setup HTTP server for /metrics and /healthz.
	status := &server.Status{}
	srv := server.New(fmt.Sprintf(":%d", *metricsPort), status, logger)

	// Create git poller and reconciler.
	poller := git.NewPoller(cfg.Repo, *repoDir)
	poller.SetStateFile(filepath.Join(*repoDir, ".dockcd_state"))

	rec, err := reconciler.New(reconciler.Config{
		Poller:       poller,
		HostStacks:   hostCfg.Stacks,
		Hostname:     hostname,
		BasePath:     cfg.BasePath,
		RepoDir:      *repoDir,
		PollInterval: *pollInterval,
		InitialSync:  *initialSync,
		Runner:       execCommand,
		Metrics:      m,
		Status:       status,
		Logger:       logger,
	})
	if err != nil {
		logger.Error("failed to create reconciler", "error", err)
		os.Exit(1)
	}

	// Start HTTP server in a goroutine (non-blocking).
	go func() {
		logger.Info("http server starting", "port", *metricsPort)
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "error", err)
		}
	}()

	// Setup signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("dockcd starting",
		"host", hostname,
		"repo", cfg.Repo,
		"stacks", len(hostCfg.Stacks),
		"poll_interval", *pollInterval,
		"initial_sync", *initialSync,
	)

	// Run the reconcile loop — blocks until context is cancelled.
	if err := rec.Run(ctx); err != nil {
		logger.Error("reconciler failed", "error", err)
		// Non-zero exit so systemd/k8s can detect the failure.
		defer os.Exit(1)
	}
	logger.Info("shutting down")

	// Graceful shutdown: stop HTTP server, then flush metrics.
	// context.Background() here because the signal context is already cancelled.
	shutdownCtx := context.Background()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http server shutdown failed", "error", err)
	}

	if err := meterProvider.Shutdown(shutdownCtx); err != nil {
		logger.Error("meter provider shutdown failed", "error", err)
	}

	if err := tracerProvider.Shutdown(shutdownCtx); err != nil {
		logger.Error("tracer provider shutdown failed", "error", err)
	}
}
