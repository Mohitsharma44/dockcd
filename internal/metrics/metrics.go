package metrics

import (
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// Metrics holds all the OpenTelemetry instruments used by dockcd.
type Metrics struct {
	// LastSyncTimestamp records the unix timestamp of the last successful deploy.
	LastSyncTimestamp metric.Float64Gauge

	// DeployTotal counts deploys by result (success/failure).
	DeployTotal metric.Int64Counter

	// DeployDuration records how long each deploy takes in seconds.
	DeployDuration metric.Float64Histogram

	// GitLastCommit tracks the current git commit (value=1, commit hash in label).
	GitLastCommit metric.Float64Gauge

	// PollTotal counts git poll operations by result (changed/unchanged/error).
	PollTotal metric.Int64Counter

	// StackHealthy tracks whether each stack is healthy (1) or degraded (0).
	StackHealthy metric.Float64Gauge

	// RollbackTotal counts rollbacks by result (success/failure).
	RollbackTotal metric.Int64Counter
}

// Setup creates an OTel MeterProvider backed by a Prometheus exporter.
// The Prometheus exporter automatically registers with prometheus.DefaultRegisterer,
// so promhttp.Handler() will serve all metrics created from this provider.
//
// Returns the MeterProvider so the caller can:
//  1. Create a Meter from it: provider.Meter("dockcd")
//  2. Shut it down on exit:   provider.Shutdown(ctx)
func Setup() (*sdkmetric.MeterProvider, error) {
	// Create a Prometheus exporter — this is the bridge between
	// OTel metrics and Prometheus text format.
	exporter, err := prometheus.New()
	if err != nil {
		return nil, err
	}

	// Create a MeterProvider that uses the Prometheus exporter.
	// Think of this as the "factory" that creates meters.
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(exporter),
	)

	return provider, nil
}

// New creates all dockcd metric instruments from the given meter.
// The meter is obtained from the MeterProvider: provider.Meter("dockcd").
//
// This is like a constructor — it returns a Metrics struct with all
// instruments ready to use.
func New(meter metric.Meter) (*Metrics, error) {
	lastSync, err := meter.Float64Gauge("dockcd_last_sync_timestamp_seconds",
		metric.WithDescription("Unix timestamp of last successful deploy"),
	)
	if err != nil {
		return nil, err
	}

	deployTotal, err := meter.Int64Counter("dockcd_deploy_total",
		metric.WithDescription("Total number of deploys by result"),
	)
	if err != nil {
		return nil, err
	}

	deployDuration, err := meter.Float64Histogram("dockcd_deploy_duration_seconds",
		metric.WithDescription("Duration of deploys in seconds"),
	)
	if err != nil {
		return nil, err
	}

	gitLastCommit, err := meter.Float64Gauge("dockcd_git_last_commit",
		metric.WithDescription("Current git commit (value=1, commit hash in label)"),
	)
	if err != nil {
		return nil, err
	}

	pollTotal, err := meter.Int64Counter("dockcd_poll_total",
		metric.WithDescription("Total git poll operations by result"),
	)
	if err != nil {
		return nil, err
	}

	stackHealthy, err := meter.Float64Gauge("dockcd_stack_healthy",
		metric.WithDescription("Whether stack is healthy (1) or degraded (0)"),
	)
	if err != nil {
		return nil, err
	}

	rollbackTotal, err := meter.Int64Counter("dockcd_rollback_total",
		metric.WithDescription("Total rollbacks by result (success/failure)"),
	)
	if err != nil {
		return nil, err
	}

	return &Metrics{
		LastSyncTimestamp: lastSync,
		DeployTotal:       deployTotal,
		DeployDuration:    deployDuration,
		GitLastCommit:     gitLastCommit,
		PollTotal:         pollTotal,
		StackHealthy:      stackHealthy,
		RollbackTotal:     rollbackTotal,
	}, nil
}
