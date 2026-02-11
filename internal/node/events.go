package node

import (
	"context"
	"sync"
)

// EventType identifies the kind of event.
type EventType int

const (
	EventTorBootstrap EventType = iota + 1
	EventTorReady
	EventPeerDiscovered
	EventConnectionReq
	EventConnectionState
	EventChatMessage
	EventFileOffer
	EventFileProgress
	EventPeerOnline
	EventError
)

// Event is a message published through the event bus.
type Event struct {
	Type    EventType
	Payload any
}

const subscriberBuffer = 64

// EventBus is a typed pub/sub system for internal communication.
type EventBus struct {
	mu   sync.RWMutex
	subs map[EventType][]chan Event
}

// NewEventBus creates a new event bus.
func NewEventBus() *EventBus {
	return &EventBus{
		subs: make(map[EventType][]chan Event),
	}
}

// Subscribe returns a channel that receives events of the given type.
func (eb *EventBus) Subscribe(t EventType) <-chan Event {
	ch := make(chan Event, subscriberBuffer)
	eb.mu.Lock()
	eb.subs[t] = append(eb.subs[t], ch)
	eb.mu.Unlock()
	return ch
}

// allEventTypes lists every defined event type.
var allEventTypes = []EventType{
	EventTorBootstrap,
	EventTorReady,
	EventPeerDiscovered,
	EventConnectionReq,
	EventConnectionState,
	EventChatMessage,
	EventFileOffer,
	EventFileProgress,
	EventPeerOnline,
	EventError,
}

// SubscribeAll returns a single channel that receives all event types.
// The returned channel is closed when ctx is cancelled.
func (eb *EventBus) SubscribeAll(ctx context.Context) <-chan Event {
	merged := make(chan Event, subscriberBuffer)
	for _, et := range allEventTypes {
		ch := eb.Subscribe(et)
		go forwardUntilDone(ctx, ch, merged)
	}
	return merged
}

func forwardUntilDone(ctx context.Context, src <-chan Event, dst chan<- Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-src:
			if !ok {
				return
			}
			select {
			case dst <- e:
			case <-ctx.Done():
				return
			}
		}
	}
}

// Publish sends an event to all subscribers of its type.
// Non-blocking: if a subscriber's channel is full, the event is dropped for that subscriber.
func (eb *EventBus) Publish(e Event) {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	for _, ch := range eb.subs[e.Type] {
		select {
		case ch <- e:
		default:
			// subscriber channel full — drop
		}
	}
}
