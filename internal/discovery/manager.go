package discovery

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	// mdnsLookupTimeout is how long to wait for mDNS results before falling back to DHT.
	mdnsLookupTimeout = 3 * time.Second

	// peerChanBuffer is the buffer size for the unified peer channel.
	peerChanBuffer = 64
)

// Manager coordinates peer discovery across mDNS (LAN) and DHT (global).
// FindPeer tries mDNS first for speed, then falls back to DHT.
type Manager struct {
	dht  *DHTDiscovery
	mdns *MDNSDiscovery

	// Unified peer channel — deduplicated discovered peers.
	peerCh chan *PeerInfo
	seen   map[string]bool // address → already emitted
	mu     sync.Mutex
}

// NewManager creates a discovery manager with DHT and optional mDNS backends.
// mdns may be nil if LAN discovery is disabled.
func NewManager(dht *DHTDiscovery, mdns *MDNSDiscovery) *Manager {
	m := &Manager{
		dht:    dht,
		mdns:   mdns,
		peerCh: make(chan *PeerInfo, peerChanBuffer),
		seen:   make(map[string]bool),
	}

	// Wire mDNS discoveries into the unified channel.
	if mdns != nil {
		mdns.OnPeerFound(func(p *PeerInfo) {
			m.emitIfNew(p)
		})
	}

	return m
}

// Start begins background discovery. Call Stop via context cancellation.
func (m *Manager) Start(ctx context.Context) error {
	if m.mdns != nil {
		if err := m.mdns.Start(ctx); err != nil {
			return fmt.Errorf("starting mDNS: %w", err)
		}
	}
	return nil
}

// Stop shuts down discovery backends.
func (m *Manager) Stop() {
	if m.mdns != nil {
		m.mdns.Stop()
	}
}

// FindPeer looks up a peer by address. Tries mDNS first (fast, LAN), then DHT (slower, global).
func (m *Manager) FindPeer(ctx context.Context, addr string) (*PeerInfo, error) {
	// Try mDNS first with a short timeout.
	if m.mdns != nil {
		result := make(chan *PeerInfo, 1)
		m.mdns.OnPeerFound(func(p *PeerInfo) {
			if p.Address == addr {
				select {
				case result <- p:
				default:
				}
			}
			// Still emit to unified channel.
			m.emitIfNew(p)
		})

		select {
		case p := <-result:
			return p, nil
		case <-time.After(mdnsLookupTimeout):
			// mDNS timeout, fall through to DHT.
		case <-ctx.Done():
			return nil, ctx.Err()
		}

		// Restore general callback.
		m.mdns.OnPeerFound(func(p *PeerInfo) {
			m.emitIfNew(p)
		})
	}

	// Fall back to DHT.
	if m.dht != nil {
		return m.dht.Lookup(ctx, addr)
	}

	return nil, fmt.Errorf("peer not found: %s (no discovery backends available)", addr)
}

// PeerChan returns a channel of discovered peers (deduplicated).
func (m *Manager) PeerChan() <-chan *PeerInfo {
	return m.peerCh
}

// AddNode bootstraps the DHT by dialing a known seed node's onion address.
// Returns the number of new peers discovered.
func (m *Manager) AddNode(ctx context.Context, onionAddr string) (int, error) {
	if m.dht == nil {
		return 0, fmt.Errorf("DHT not initialized")
	}
	return m.dht.Bootstrap(ctx, onionAddr)
}

// emitIfNew sends a peer to the unified channel if not already seen.
func (m *Manager) emitIfNew(p *PeerInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.seen[p.Address] {
		return
	}
	m.seen[p.Address] = true

	select {
	case m.peerCh <- p:
	default:
		// Channel full, drop oldest seen entry to allow new discoveries.
	}
}
