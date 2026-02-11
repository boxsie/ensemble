package chat

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func makeMsg(id, text string) *Message {
	now := time.Now()
	return &Message{
		ID:      id,
		From:    "Esender",
		To:      "Ereceiver",
		Text:    text,
		SentAt:  now,
		AckedAt: &now,
	}
}

func TestAppendAndGetConversation(t *testing.T) {
	h := NewHistory()
	peer := "Epeer1"

	h.Append(peer, makeMsg("1", "hello"))
	h.Append(peer, makeMsg("2", "world"))

	msgs := h.GetConversation(peer)
	if len(msgs) != 2 {
		t.Fatalf("count: got %d, want 2", len(msgs))
	}
	if msgs[0].Text != "hello" {
		t.Fatalf("first message: got %q, want %q", msgs[0].Text, "hello")
	}
	if msgs[1].Text != "world" {
		t.Fatalf("second message: got %q, want %q", msgs[1].Text, "world")
	}
}

func TestGetConversationReturnsNilForUnknown(t *testing.T) {
	h := NewHistory()
	msgs := h.GetConversation("Eunknown")
	if msgs != nil {
		t.Fatalf("expected nil, got %v", msgs)
	}
}

func TestGetConversationReturnsCopy(t *testing.T) {
	h := NewHistory()
	peer := "Epeer1"
	h.Append(peer, makeMsg("1", "hello"))

	msgs := h.GetConversation(peer)
	msgs[0] = makeMsg("modified", "tampered")

	original := h.GetConversation(peer)
	if original[0].ID != "1" {
		t.Fatal("GetConversation should return a copy")
	}
}

func TestGetLatest(t *testing.T) {
	h := NewHistory()
	peer := "Epeer1"

	for i := range 10 {
		h.Append(peer, makeMsg(fmt.Sprintf("%d", i), fmt.Sprintf("msg-%d", i)))
	}

	latest := h.GetLatest(peer, 3)
	if len(latest) != 3 {
		t.Fatalf("count: got %d, want 3", len(latest))
	}
	if latest[0].Text != "msg-7" {
		t.Fatalf("first of latest: got %q, want %q", latest[0].Text, "msg-7")
	}
	if latest[2].Text != "msg-9" {
		t.Fatalf("last of latest: got %q, want %q", latest[2].Text, "msg-9")
	}
}

func TestGetLatestMoreThanExists(t *testing.T) {
	h := NewHistory()
	peer := "Epeer1"
	h.Append(peer, makeMsg("1", "only"))

	latest := h.GetLatest(peer, 100)
	if len(latest) != 1 {
		t.Fatalf("count: got %d, want 1", len(latest))
	}
}

func TestGetLatestUnknownPeer(t *testing.T) {
	h := NewHistory()
	latest := h.GetLatest("Eunknown", 5)
	if latest != nil {
		t.Fatalf("expected nil, got %v", latest)
	}
}

func TestClear(t *testing.T) {
	h := NewHistory()
	peer := "Epeer1"

	h.Append(peer, makeMsg("1", "hello"))
	h.Clear(peer)

	msgs := h.GetConversation(peer)
	if msgs != nil {
		t.Fatalf("expected nil after Clear, got %d messages", len(msgs))
	}
}

func TestEvictionAtMax(t *testing.T) {
	h := NewHistory()
	peer := "Epeer1"

	for i := range maxMessagesPerPeer + 50 {
		h.Append(peer, makeMsg(fmt.Sprintf("%d", i), fmt.Sprintf("msg-%d", i)))
	}

	msgs := h.GetConversation(peer)
	if len(msgs) != maxMessagesPerPeer {
		t.Fatalf("count: got %d, want %d", len(msgs), maxMessagesPerPeer)
	}

	// Oldest should be message 50 (first 50 evicted).
	if msgs[0].Text != "msg-50" {
		t.Fatalf("first message after eviction: got %q, want %q", msgs[0].Text, "msg-50")
	}
	// Newest should be the last one appended.
	last := fmt.Sprintf("msg-%d", maxMessagesPerPeer+49)
	if msgs[len(msgs)-1].Text != last {
		t.Fatalf("last message: got %q, want %q", msgs[len(msgs)-1].Text, last)
	}
}

func TestSeparateConversations(t *testing.T) {
	h := NewHistory()

	h.Append("Epeer1", makeMsg("1", "for peer1"))
	h.Append("Epeer2", makeMsg("2", "for peer2"))

	msgs1 := h.GetConversation("Epeer1")
	msgs2 := h.GetConversation("Epeer2")

	if len(msgs1) != 1 || msgs1[0].Text != "for peer1" {
		t.Fatal("peer1 conversation wrong")
	}
	if len(msgs2) != 1 || msgs2[0].Text != "for peer2" {
		t.Fatal("peer2 conversation wrong")
	}
}

func TestConcurrentAccess(t *testing.T) {
	h := NewHistory()
	peer := "Epeer1"

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			h.Append(peer, makeMsg(fmt.Sprintf("%d", n), "concurrent"))
		}(i)
	}
	wg.Wait()

	msgs := h.GetConversation(peer)
	if len(msgs) != 100 {
		t.Fatalf("count: got %d, want 100", len(msgs))
	}
}
