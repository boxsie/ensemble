package node

import (
	"sync"
	"testing"
	"time"
)

func TestSubscribeAndPublish(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe(EventTorReady)

	bus.Publish(Event{Type: EventTorReady, Payload: "ready"})

	select {
	case e := <-ch:
		if e.Payload != "ready" {
			t.Fatalf("payload: got %v, want \"ready\"", e.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestUnsubscribedTypeNotReceived(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe(EventTorReady)

	// Publish a different event type.
	bus.Publish(Event{Type: EventError, Payload: "boom"})

	select {
	case e := <-ch:
		t.Fatalf("should not receive unsubscribed event type, got %v", e)
	case <-time.After(50 * time.Millisecond):
		// expected — nothing received
	}
}

func TestMultipleSubscribers(t *testing.T) {
	bus := NewEventBus()
	ch1 := bus.Subscribe(EventChatMessage)
	ch2 := bus.Subscribe(EventChatMessage)

	bus.Publish(Event{Type: EventChatMessage, Payload: "hello"})

	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case e := <-ch:
			if e.Payload != "hello" {
				t.Fatalf("subscriber %d: got %v, want \"hello\"", i, e.Payload)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out", i)
		}
	}
}

func TestMultipleEventTypes(t *testing.T) {
	bus := NewEventBus()
	chTor := bus.Subscribe(EventTorReady)
	chChat := bus.Subscribe(EventChatMessage)

	bus.Publish(Event{Type: EventTorReady, Payload: "tor"})
	bus.Publish(Event{Type: EventChatMessage, Payload: "chat"})

	select {
	case e := <-chTor:
		if e.Payload != "tor" {
			t.Fatalf("tor channel: got %v", e.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out on tor channel")
	}

	select {
	case e := <-chChat:
		if e.Payload != "chat" {
			t.Fatalf("chat channel: got %v", e.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out on chat channel")
	}
}

func TestNonBlockingPublish(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe(EventError)

	// Fill the subscriber buffer.
	for i := range subscriberBuffer {
		bus.Publish(Event{Type: EventError, Payload: i})
	}

	// This should not block — event gets dropped.
	done := make(chan struct{})
	go func() {
		bus.Publish(Event{Type: EventError, Payload: "overflow"})
		close(done)
	}()

	select {
	case <-done:
		// expected — publish returned immediately
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on full channel")
	}

	// Drain and verify we got the buffered events, not the overflow.
	for range subscriberBuffer {
		<-ch
	}

	select {
	case e := <-ch:
		t.Fatalf("should not receive overflow event, got %v", e)
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

func TestConcurrentPublish(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe(EventPeerDiscovered)

	count := 100
	received := make(chan struct{}, count)

	// Drain subscriber concurrently so the buffer doesn't fill up.
	go func() {
		for range ch {
			received <- struct{}{}
		}
	}()

	var wg sync.WaitGroup
	wg.Add(count)
	for i := range count {
		go func(n int) {
			defer wg.Done()
			bus.Publish(Event{Type: EventPeerDiscovered, Payload: n})
		}(i)
	}
	wg.Wait()

	// Wait briefly for drain to finish.
	time.Sleep(50 * time.Millisecond)

	if got := len(received); got != count {
		t.Fatalf("received %d events, want %d", got, count)
	}
}

func TestPublishWithNoSubscribers(t *testing.T) {
	bus := NewEventBus()
	// Should not panic.
	bus.Publish(Event{Type: EventTorReady, Payload: "no one listening"})
}
