package metrics

import (
	"context"
	"testing"
)

func TestSetupReturnsProvider(t *testing.T) {
	provider, err := Setup()
	if err != nil {
		t.Fatalf("Setup() returned error: %v", err)
	}
	if provider == nil {
		t.Fatal("Setup() returned nil provider")
	}
	defer provider.Shutdown(context.Background())
}

func TestNewCreatesAllInstruments(t *testing.T) {
	provider, err := Setup()
	if err != nil {
		t.Fatalf("Setup() returned error: %v", err)
	}
	defer provider.Shutdown(context.Background())

	meter := provider.Meter("dockcd-test")
	m, err := New(meter)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if m == nil {
		t.Fatal("New() returned nil Metrics")
	}
	if m.LastSyncTimestamp == nil {
		t.Error("LastSyncTimestamp is nil")
	}
	if m.DeployTotal == nil {
		t.Error("DeployTotal is nil")
	}
	if m.DeployDuration == nil {
		t.Error("DeployDuration is nil")
	}
	if m.GitLastCommit == nil {
		t.Error("GitLastCommit is nil")
	}
	if m.PollTotal == nil {
		t.Error("PollTotal is nil")
	}
	if m.StackHealthy == nil {
		t.Error("StackHealthy is nil")
	}
}

func TestMetricsCanRecord(t *testing.T) {
	provider, err := Setup()
	if err != nil {
		t.Fatalf("Setup() returned error: %v", err)
	}
	defer provider.Shutdown(context.Background())

	meter := provider.Meter("dockcd-test")
	m, err := New(meter)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}

	ctx := context.Background()

	// Counters use Add()
	m.DeployTotal.Add(ctx, 1)
	m.PollTotal.Add(ctx, 1)

	// Histograms use Record()
	m.DeployDuration.Record(ctx, 1.5)

	// Gauges use Record()
	m.LastSyncTimestamp.Record(ctx, 1710000000.0)
	m.GitLastCommit.Record(ctx, 1.0)
	m.StackHealthy.Record(ctx, 1.0)

	// No panic = pass. We're testing the wiring, not the values.
}
