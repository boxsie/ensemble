package chat

import "sync"

const maxMessagesPerPeer = 1000

// History stores per-conversation message lists in memory.
type History struct {
	mu      sync.RWMutex
	convos  map[string][]*Message // keyed by peer address
}

// NewHistory creates an empty history store.
func NewHistory() *History {
	return &History{
		convos: make(map[string][]*Message),
	}
}

// Append adds a message to the conversation with the given peer.
// Evicts the oldest message if the conversation exceeds maxMessagesPerPeer.
func (h *History) Append(peerAddr string, msg *Message) {
	h.mu.Lock()
	defer h.mu.Unlock()

	msgs := h.convos[peerAddr]
	msgs = append(msgs, msg)
	if len(msgs) > maxMessagesPerPeer {
		msgs = msgs[len(msgs)-maxMessagesPerPeer:]
	}
	h.convos[peerAddr] = msgs
}

// GetConversation returns all messages for a peer in chronological order.
// Returns nil if no conversation exists.
func (h *History) GetConversation(peerAddr string) []*Message {
	h.mu.RLock()
	defer h.mu.RUnlock()

	msgs := h.convos[peerAddr]
	if msgs == nil {
		return nil
	}
	out := make([]*Message, len(msgs))
	copy(out, msgs)
	return out
}

// GetLatest returns the last n messages for a peer.
// Returns fewer if the conversation has less than n messages.
func (h *History) GetLatest(peerAddr string, n int) []*Message {
	h.mu.RLock()
	defer h.mu.RUnlock()

	msgs := h.convos[peerAddr]
	if msgs == nil {
		return nil
	}
	if n >= len(msgs) {
		out := make([]*Message, len(msgs))
		copy(out, msgs)
		return out
	}
	out := make([]*Message, n)
	copy(out, msgs[len(msgs)-n:])
	return out
}

// Clear removes all messages for a peer.
func (h *History) Clear(peerAddr string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.convos, peerAddr)
}
