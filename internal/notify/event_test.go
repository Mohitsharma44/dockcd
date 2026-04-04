package notify

import (
	"sync"
	"testing"
	"time"
)

func TestEventBusEmitToSubscriber(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe()

	want := Event{
		Type:   EventDeploySuccess,
		Stack:  "mystack",
		Commit: "abc123",
	}
	bus.Emit(want)

	select {
	case got := <-ch:
		if got.Type != want.Type || got.Stack != want.Stack || got.Commit != want.Commit {
			t.Errorf("got %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestEventBusMultipleSubscribers(t *testing.T) {
	bus := NewEventBus()
	ch1 := bus.Subscribe()
	ch2 := bus.Subscribe()

	event := Event{Type: EventRollbackSuccess, Stack: "stack1"}
	bus.Emit(event)

	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Type != event.Type {
				t.Errorf("subscriber %d: got type %q, want %q", i, got.Type, event.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out waiting for event", i)
		}
	}
}

func TestEventBusEmitNoSubscribers(t *testing.T) {
	bus := NewEventBus()
	// Must not panic or block.
	bus.Emit(Event{Type: EventReconcileStart})
}

func TestEventBusCloseClosesChannels(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe()

	bus.Close()

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed, but received a value")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel to be closed")
	}
}

func TestEventBusEmitAfterClose(t *testing.T) {
	bus := NewEventBus()
	_ = bus.Subscribe()
	bus.Close()

	// Must not panic.
	bus.Emit(Event{Type: EventDeployFailure})
}

func TestEventBusSlowSubscriberDoesNotBlock(t *testing.T) {
	bus := NewEventBus()
	slow := bus.Subscribe()
	fast := bus.Subscribe()

	// Fill the slow subscriber's buffer completely.
	for i := 0; i < subscriberBuffer; i++ {
		bus.Emit(Event{Type: EventReconcileComplete, Message: "fill"})
	}

	// The fast subscriber should have received all events.
	for i := 0; i < subscriberBuffer; i++ {
		select {
		case <-fast:
		case <-time.After(time.Second):
			t.Fatalf("fast subscriber timed out on event %d", i)
		}
	}

	// One more emit should not block even though slow's buffer is full.
	done := make(chan struct{})
	go func() {
		bus.Emit(Event{Type: EventForceDeploy})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Emit blocked with a full slow subscriber")
	}

	// Drain slow to avoid goroutine leak in test.
	for len(slow) > 0 {
		<-slow
	}
}

func TestEventBusSubscribeAfterClose(t *testing.T) {
	bus := NewEventBus()
	bus.Close()

	ch := bus.Subscribe()

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected already-closed channel, got open channel with value")
		}
	default:
		// Channel should be closed; a closed channel is always ready to receive.
		t.Error("expected closed channel to be immediately readable")
	}
}

func TestEventBusCloseIdempotent(t *testing.T) {
	bus := NewEventBus()
	_ = bus.Subscribe()

	// Must not panic on double close.
	bus.Close()
	bus.Close()
}

func TestEventBusConcurrentEmitSubscribeClose(t *testing.T) {
	bus := NewEventBus()

	var wg sync.WaitGroup

	// Concurrent subscribers.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = bus.Subscribe()
		}()
	}

	// Concurrent emitters.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus.Emit(Event{Type: EventDeploySuccess})
		}()
	}

	wg.Wait()
	// Close should not panic regardless of concurrent activity.
	bus.Close()
}
