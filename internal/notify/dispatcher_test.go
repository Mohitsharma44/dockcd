package notify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testLogger returns a discarding slog.Logger suitable for tests.
func testLogger(_ *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// mockNotifier records every delivered event. wg, if set, is Done once per
// successful Send — callers can wait on it to know all expected sends arrived.
type mockNotifier struct {
	name   string
	mu     sync.Mutex
	events []Event
	err    error
	wg     *sync.WaitGroup
}

func (m *mockNotifier) Name() string { return m.name }

func (m *mockNotifier) Send(_ context.Context, e Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.events = append(m.events, e)
	if m.wg != nil {
		m.wg.Done()
	}
	return nil
}

func (m *mockNotifier) received() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Event, len(m.events))
	copy(out, m.events)
	return out
}

// countingNotifier fails the first failUntil calls then succeeds, recording
// events that pass through. wg is Done once per successful Send.
type countingNotifier struct {
	name      string
	mu        sync.Mutex
	events    []Event
	failUntil int32
	calls     atomic.Int32
	wg        *sync.WaitGroup
}

func (c *countingNotifier) Name() string { return c.name }

func (c *countingNotifier) Send(_ context.Context, e Event) error {
	attempt := int(c.calls.Add(1))
	if attempt <= int(c.failUntil) {
		return errors.New("transient error")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
	if c.wg != nil {
		c.wg.Done()
	}
	return nil
}

func (c *countingNotifier) received() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Event, len(c.events))
	copy(out, c.events)
	return out
}

// startDispatcher launches d.Start in a goroutine and waits a short time for
// the subscription to be registered before returning. This avoids a race where
// an Emit arrives before Start has called bus.Subscribe.
func startDispatcher(ctx context.Context, d *Dispatcher) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Start(ctx)
	}()
	// Give the goroutine time to reach bus.Subscribe(). Under the race
	// detector the scheduler can be sluggish, so 50 ms is sufficient without
	// being noisy in CI.
	time.Sleep(50 * time.Millisecond)
	return done
}

// TestDispatcherRoutesEvents verifies that each notifier receives only the
// event types it has subscribed to.
func TestDispatcherRoutesEvents(t *testing.T) {
	t.Parallel()

	bus := NewEventBus()
	t.Cleanup(bus.Close)

	var wg sync.WaitGroup
	// Expect successNotifier to receive 1 event, failureNotifier to receive 2.
	wg.Add(3)

	successNotifier := &mockNotifier{name: "success-only", wg: &wg}
	failureNotifier := &mockNotifier{name: "failure-only", wg: &wg}

	d := NewDispatcher(bus, testLogger(t), []NotifierWithFilter{
		{
			Notifier: successNotifier,
			Events:   map[EventType]bool{EventDeploySuccess: true},
		},
		{
			Notifier: failureNotifier,
			Events:   map[EventType]bool{EventDeployFailure: true, EventRollbackFailure: true},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := startDispatcher(ctx, d)

	bus.Emit(Event{Type: EventDeploySuccess, Stack: "s1"})
	bus.Emit(Event{Type: EventDeployFailure, Stack: "s2"})
	bus.Emit(Event{Type: EventRollbackFailure, Stack: "s3"})

	// Wait for all three sends to complete (with a safety timeout).
	waitCh := make(chan struct{})
	go func() { wg.Wait(); close(waitCh) }()
	select {
	case <-waitCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for all notifications to be delivered")
	}

	cancel()
	<-done

	gotSuccess := successNotifier.received()
	if len(gotSuccess) != 1 {
		t.Fatalf("success notifier: want 1 event, got %d", len(gotSuccess))
	}
	if gotSuccess[0].Type != EventDeploySuccess {
		t.Errorf("success notifier: want deploy_success, got %s", gotSuccess[0].Type)
	}

	gotFailure := failureNotifier.received()
	if len(gotFailure) != 2 {
		t.Fatalf("failure notifier: want 2 events, got %d", len(gotFailure))
	}
}

// TestDispatcherStopsOnContextCancel verifies Start returns when the context
// is cancelled.
func TestDispatcherStopsOnContextCancel(t *testing.T) {
	t.Parallel()

	bus := NewEventBus()
	t.Cleanup(bus.Close)

	d := NewDispatcher(bus, testLogger(t), nil)

	ctx, cancel := context.WithCancel(context.Background())

	done := startDispatcher(ctx, d)

	cancel()

	select {
	case <-done:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

// TestDispatcherStopsOnBusClose verifies Start returns when the EventBus is
// closed.
func TestDispatcherStopsOnBusClose(t *testing.T) {
	t.Parallel()

	bus := NewEventBus()

	d := NewDispatcher(bus, testLogger(t), nil)

	done := startDispatcher(context.Background(), d)

	bus.Close()

	select {
	case <-done:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after bus was closed")
	}
}

// TestDispatcherRetriesOnFailure verifies that a transient notifier error
// triggers exponential-backoff retries and the event is ultimately delivered.
// This test takes ~3 s due to the backoff schedule (0 s + 1 s + 2 s).
func TestDispatcherRetriesOnFailure(t *testing.T) {
	bus := NewEventBus()
	t.Cleanup(bus.Close)

	var wg sync.WaitGroup
	wg.Add(1) // one successful delivery expected

	n := &countingNotifier{name: "flaky", failUntil: 2, wg: &wg}

	d := NewDispatcher(bus, testLogger(t), []NotifierWithFilter{
		{
			Notifier: n,
			Events:   map[EventType]bool{EventDeploySuccess: true},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := startDispatcher(ctx, d)
	_ = done

	bus.Emit(Event{Type: EventDeploySuccess, Stack: "retry-stack"})

	// Backoff schedule: attempt 1 = 0 s, attempt 2 = 1 s, attempt 3 = 2 s.
	// Allow generous wall-clock budget.
	waitCh := make(chan struct{})
	go func() { wg.Wait(); close(waitCh) }()
	select {
	case <-waitCh:
	case <-time.After(6 * time.Second):
		t.Fatalf("event not delivered after retries; calls=%d received=%d",
			n.calls.Load(), len(n.received()))
	}

	if got := n.calls.Load(); got != 3 {
		t.Errorf("want 3 Send calls (2 failures + 1 success), got %d", got)
	}
}

// TestDispatcherSkipsUnfilteredEvents verifies that a notifier does not
// receive event types outside its filter.
func TestDispatcherSkipsUnfilteredEvents(t *testing.T) {
	t.Parallel()

	bus := NewEventBus()
	t.Cleanup(bus.Close)

	n := &mockNotifier{name: "failure-only"}

	d := NewDispatcher(bus, testLogger(t), []NotifierWithFilter{
		{
			Notifier: n,
			Events:   map[EventType]bool{EventDeployFailure: true},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := startDispatcher(ctx, d)

	// Emit an event the notifier is NOT subscribed to.
	bus.Emit(Event{Type: EventDeploySuccess, Stack: "should-be-ignored"})

	// Give the dispatcher time to process the event (and confirm no send
	// happens). Under the race detector 200 ms is sufficient.
	time.Sleep(200 * time.Millisecond)

	cancel()
	<-done

	if got := len(n.received()); got != 0 {
		t.Errorf("want 0 events for unfiltered type, got %d", got)
	}
}
