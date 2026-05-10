package discovery

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

const (
	// mdnsLookupTimeout is how long to wait for mDNS results before falling back to DHT.
	mdnsLookupTimeout = 3 * time.Second

	// peerChanBuffer is the buffer size for the unified peer channel.
	peerChanBuffer = 64

	// DefaultRTMaxAge is the default eviction window for routing-table
	// entries. Peers with LastSeen older than this are dropped on the next
	// announce-loop tick (and at startup once the routing table is loaded
	// from disk).
	DefaultRTMaxAge = 168 * time.Hour // 7 days
)

// Manager coordinates peer discovery across mDNS (LAN) and DHT (global).
// FindPeer tries mDNS first for speed, then falls back to DHT.
type Manager struct {
	dht  *DHTDiscovery
	mdns *MDNSDiscovery

	// Unified peer channel — deduplicated discovered peers.
	peerCh   chan *PeerInfo
	seen     map[string]bool // address → already emitted
	rtPath   string          // routing table persistence path
	rtMaxAge time.Duration   // eviction window for stale RT entries (0 = disabled)
	mu       sync.Mutex
}

// NewManager creates a discovery manager with DHT and optional mDNS backends.
// mdns may be nil if LAN discovery is disabled.
func NewManager(dht *DHTDiscovery, mdns *MDNSDiscovery) *Manager {
	m := &Manager{
		dht:      dht,
		mdns:     mdns,
		peerCh:   make(chan *PeerInfo, peerChanBuffer),
		seen:     make(map[string]bool),
		rtMaxAge: DefaultRTMaxAge,
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
	n, err := m.dht.Bootstrap(ctx, onionAddr)
	if err != nil {
		return 0, err
	}

	// Announce ourselves so discovered peers learn about us.
	if announceErr := m.dht.Announce(ctx); announceErr != nil {
		log.Printf("discovery: announce after bootstrap: %v", announceErr)
	}

	// Save routing table after bootstrap+announce.
	m.mu.Lock()
	rtPath := m.rtPath
	m.mu.Unlock()
	if rtPath != "" {
		if saveErr := m.dht.RT().Save(rtPath); saveErr != nil {
			log.Printf("discovery: save routing table: %v", saveErr)
		}
	}

	return n, nil
}

// RTSize returns the number of peers in the routing table.
func (m *Manager) RTSize() int {
	if m.dht == nil {
		return 0
	}
	return m.dht.RT().Size()
}

// ListPeers returns all peers from the routing table.
func (m *Manager) ListPeers() []*PeerInfo {
	if m.dht == nil {
		return nil
	}
	return m.dht.RT().AllPeers()
}

// SetRTPath sets the routing table persistence path.
func (m *Manager) SetRTPath(path string) {
	m.mu.Lock()
	m.rtPath = path
	m.mu.Unlock()
}

// SetRTMaxAge sets the routing-table eviction window. Peers with LastSeen
// older than this are dropped on the next announce-loop tick. Pass 0 to
// disable time-based eviction (failure-counter eviction still applies).
func (m *Manager) SetRTMaxAge(d time.Duration) {
	m.mu.Lock()
	m.rtMaxAge = d
	m.mu.Unlock()
}

// EvictStale walks the routing table and drops peers older than the configured
// max age. Returns the number evicted. Safe to call concurrently with the
// announce loop. Exposed primarily for the daemon's startup eviction pass.
func (m *Manager) EvictStale() int {
	if m.dht == nil {
		return 0
	}
	m.mu.Lock()
	maxAge := m.rtMaxAge
	m.mu.Unlock()
	if maxAge <= 0 {
		return 0
	}
	return m.dht.RT().Evict(maxAge)
}

// StartAnnounceLoop runs a background goroutine that announces to the DHT
// and saves the routing table periodically. Stops when ctx is cancelled.
func (m *Manager) StartAnnounceLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Minute)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Evict stale entries BEFORE announcing so we don't burn
				// Tor SOCKS time dialing peers that have been silent past
				// the configured max age.
				if n := m.EvictStale(); n > 0 {
					log.Printf("discovery: evicted %d stale routing-table peers (rt_size=%d)", n, m.dht.RT().Size())
				}
				if err := m.dht.Announce(ctx); err != nil {
					log.Printf("discovery: periodic announce: %v", err)
				} else {
					log.Printf("discovery: periodic announce succeeded (rt_size=%d)", m.dht.RT().Size())
				}
				m.mu.Lock()
				rtPath := m.rtPath
				m.mu.Unlock()
				if rtPath != "" {
					if err := m.dht.RT().Save(rtPath); err != nil {
						log.Printf("discovery: save routing table: %v", err)
					}
				}
			}
		}
	}()
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
