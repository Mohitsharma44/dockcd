// Package notify provides an in-process event bus for distributing lifecycle
// events to subscribers such as SSE endpoints and notification dispatchers.
package notify

import (
	"sync"
	"time"
)

// EventType identifies the kind of lifecycle event.
type EventType string

const (
	EventDeploySuccess     EventType = "deploy_success"
	EventDeployFailure     EventType = "deploy_failure"
	EventRollbackSuccess   EventType = "rollback_success"
	EventRollbackFailure   EventType = "rollback_failure"
	EventReconcileStart    EventType = "reconcile_start"
	EventReconcileComplete EventType = "reconcile_complete"
	EventForceDeploy       EventType = "force_deploy"
	EventStackSuspended    EventType = "stack_suspended"
	EventStackResumed      EventType = "stack_resumed"
	EventHookFailure       EventType = "hook_failure"
)

// Event carries information about a single lifecycle occurrence.
type Event struct {
	Type      EventType         `json:"type"`
	Timestamp time.Time         `json:"timestamp"`
	Stack     string            `json:"stack,omitempty"`
	Host      string            `json:"host,omitempty"`
	Commit    string            `json:"commit,omitempty"`
	Branch    string            `json:"branch,omitempty"`
	Message   string            `json:"message,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// subscriberBuffer is the capacity of each subscriber's channel. A slow
// subscriber will have events dropped rather than blocking Emit or other
// subscribers.
const subscriberBuffer = 64

// EventBus distributes events to all current subscribers. All methods are
// safe for concurrent use.
type EventBus struct {
	mu          sync.RWMutex
	subscribers []chan Event
	closed      bool
}

// NewEventBus returns an initialised, open EventBus.
func NewEventBus() *EventBus {
	return &EventBus{}
}

// Subscribe returns a buffered channel that receives every event emitted
// after the call returns. If the bus is already closed, the returned channel
// is already closed.
func (b *EventBus) Subscribe() <-chan Event {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan Event, subscriberBuffer)
	if b.closed {
		close(ch)
		return ch
	}
	b.subscribers = append(b.subscribers, ch)
	return ch
}

// Emit sends event to every subscriber using a non-blocking send. If a
// subscriber's buffer is full the event is silently dropped for that
// subscriber. Emit is a no-op when the bus is closed.
func (b *EventBus) Emit(event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return
	}
	for _, ch := range b.subscribers {
		select {
		case ch <- event:
		default:
			// Subscriber buffer full; drop the event for this subscriber.
		}
	}
}

// Close closes all subscriber channels and marks the bus as closed.
// Subsequent calls to Close are no-ops. Subsequent calls to Emit are no-ops.
// Subsequent calls to Subscribe return an already-closed channel.
func (b *EventBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return
	}
	b.closed = true
	for _, ch := range b.subscribers {
		close(ch)
	}
	b.subscribers = nil
}
