package discovery

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

const (
	mdnsService = "_ensemble._tcp"
	mdnsDomain  = "local."
	mdnsPort    = 0 // no direct TCP port — we use Tor for connections
	mdnsBrowse  = 5 * time.Second
)

// MDNSDiscovery discovers ensemble peers on the local network via mDNS.
type MDNSDiscovery struct {
	localAddr string // our ensemble address
	onionAddr string // our .onion address

	mu       sync.Mutex
	server   *zeroconf.Server
	callback func(*PeerInfo)
}

// NewMDNSDiscovery creates a new mDNS discovery instance.
func NewMDNSDiscovery(localAddr, onionAddr string) *MDNSDiscovery {
	return &MDNSDiscovery{
		localAddr: localAddr,
		onionAddr: onionAddr,
	}
}

// OnPeerFound sets the callback invoked when a peer is discovered via mDNS.
func (m *MDNSDiscovery) OnPeerFound(cb func(*PeerInfo)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callback = cb
}

// Start registers our service and begins browsing for peers.
func (m *MDNSDiscovery) Start(ctx context.Context) error {
	// Register ourselves as an mDNS service.
	txt := []string{
		"addr=" + m.localAddr,
		"onion=" + m.onionAddr,
	}
	server, err := zeroconf.Register(
		m.localAddr, // instance name
		mdnsService,
		mdnsDomain,
		mdnsPort,
		txt,
		nil, // all interfaces
	)
	if err != nil {
		return fmt.Errorf("registering mDNS service: %w", err)
	}
	m.mu.Lock()
	m.server = server
	m.mu.Unlock()

	// Browse for peers in background.
	go m.browse(ctx)

	return nil
}

// Stop shuts down the mDNS service.
func (m *MDNSDiscovery) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.server != nil {
		m.server.Shutdown()
		m.server = nil
	}
}

// browse continuously scans for mDNS peers until ctx is cancelled.
func (m *MDNSDiscovery) browse(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		resolver, err := zeroconf.NewResolver(nil)
		if err != nil {
			time.Sleep(mdnsBrowse)
			continue
		}

		entries := make(chan *zeroconf.ServiceEntry)
		go func() {
			for entry := range entries {
				peer := m.entryToPeer(entry)
				if peer != nil {
					m.mu.Lock()
					cb := m.callback
					m.mu.Unlock()
					if cb != nil {
						cb(peer)
					}
				}
			}
		}()

		browseCtx, cancel := context.WithTimeout(ctx, mdnsBrowse)
		resolver.Browse(browseCtx, mdnsService, mdnsDomain, entries)
		cancel()

		// Brief pause before next scan.
		select {
		case <-ctx.Done():
			return
		case <-time.After(mdnsBrowse):
		}
	}
}

// entryToPeer converts an mDNS service entry to a PeerInfo.
func (m *MDNSDiscovery) entryToPeer(entry *zeroconf.ServiceEntry) *PeerInfo {
	if entry == nil {
		return nil
	}

	var addr, onion string
	for _, txt := range entry.Text {
		if v, ok := strings.CutPrefix(txt, "addr="); ok {
			addr = v
		}
		if v, ok := strings.CutPrefix(txt, "onion="); ok {
			onion = v
		}
	}

	if addr == "" || onion == "" {
		return nil
	}

	// Skip ourselves.
	if addr == m.localAddr {
		return nil
	}

	nodeID, err := NodeIDFromAddress(addr)
	if err != nil {
		return nil
	}

	return &PeerInfo{
		ID:        nodeID,
		Address:   addr,
		OnionAddr: onion,
		LastSeen:  time.Now(),
	}
}

// ParseMDNSEntry is exported for testing — converts TXT records to PeerInfo.
func ParseMDNSEntry(localAddr string, txtRecords []string) *PeerInfo {
	m := &MDNSDiscovery{localAddr: localAddr}
	entry := &zeroconf.ServiceEntry{Text: txtRecords}
	return m.entryToPeer(entry)
}
