package notify

import (
	"context"
	"log/slog"
	"time"
)

// NotifierWithFilter pairs a Notifier with the set of event types it should
// receive. Events whose type is not present in the map are silently skipped
// for that notifier.
type NotifierWithFilter struct {
	Notifier Notifier
	Events   map[EventType]bool
}

// Dispatcher subscribes to an EventBus, filters events per notifier according
// to each notifier's configured event types, and routes matching events with
// exponential-backoff retry logic.
type Dispatcher struct {
	bus       *EventBus
	logger    *slog.Logger
	notifiers []NotifierWithFilter
}

// NewDispatcher returns a Dispatcher wired to bus. Call Start to begin
// processing events.
func NewDispatcher(bus *EventBus, logger *slog.Logger, notifiers []NotifierWithFilter) *Dispatcher {
	return &Dispatcher{
		bus:       bus,
		logger:    logger,
		notifiers: notifiers,
	}
}

// Start subscribes to the EventBus and dispatches events to notifiers. It
// blocks until ctx is cancelled or the EventBus is closed.
func (d *Dispatcher) Start(ctx context.Context) {
	ch := d.bus.Subscribe()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			d.dispatch(ctx, event)
		}
	}
}

// dispatch sends event to every notifier whose filter includes the event type.
// Each notifier is called in its own goroutine so that a slow or retrying
// notifier does not block others.
func (d *Dispatcher) dispatch(ctx context.Context, event Event) {
	for _, nf := range d.notifiers {
		if !nf.Events[event.Type] {
			continue
		}
		go d.sendWithRetry(ctx, nf.Notifier, event)
	}
}

// sendWithRetry attempts to deliver event to n up to four times using an
// exponential backoff schedule of 0 s, 1 s, 2 s, and 4 s between attempts.
// It respects ctx cancellation during backoff waits.
func (d *Dispatcher) sendWithRetry(ctx context.Context, n Notifier, event Event) {
	backoff := []time.Duration{0, 1 * time.Second, 2 * time.Second, 4 * time.Second}
	for attempt := 0; attempt < len(backoff); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff[attempt]):
			}
		}
		if err := n.Send(ctx, event); err != nil {
			d.logger.Warn("notification failed",
				"notifier", n.Name(),
				"event", string(event.Type),
				"attempt", attempt+1,
				"error", err,
			)
			continue
		}
		return
	}
	d.logger.Error("notification delivery failed after retries",
		"notifier", n.Name(),
		"event", string(event.Type),
		"stack", event.Stack,
	)
}
