package transport

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// NATStrategy identifies which traversal method succeeded.
type NATStrategy int

const (
	StrategyDirect     NATStrategy = iota // UPnP/NAT-PMP or already public
	StrategyHolePunch                     // DCUtR UDP hole punching
	StrategyRelay                         // Circuit Relay v2
	StrategyTorOnly                       // Tor fallback (no direct connection)
)

func (s NATStrategy) String() string {
	switch s {
	case StrategyDirect:
		return "direct"
	case StrategyHolePunch:
		return "holepunch"
	case StrategyRelay:
		return "relay"
	case StrategyTorOnly:
		return "tor-only"
	default:
		return "unknown"
	}
}

// NATResult describes the outcome of a NAT traversal attempt.
type NATResult struct {
	Strategy NATStrategy
	Addrs    []string      // multiaddrs of the established connection
	Latency  time.Duration // round-trip latency estimate
}

// NATConfig holds timeouts for each traversal strategy.
type NATConfig struct {
	DirectTimeout    time.Duration // UPnP/NAT-PMP + direct connect timeout
	HolePunchTimeout time.Duration // DCUtR timeout
	RelayTimeout     time.Duration // Circuit Relay v2 timeout
}

// DefaultNATConfig returns the standard cascade timeouts.
func DefaultNATConfig() NATConfig {
	return NATConfig{
		DirectTimeout:    2 * time.Second,
		HolePunchTimeout: 10 * time.Second,
		RelayTimeout:     15 * time.Second,
	}
}

// NATTraverseFrom is like NATTraverse but starts from the given strategy,
// skipping higher-priority strategies.
func NATTraverseFrom(ctx context.Context, h *Host, peerInfo peer.AddrInfo, cfg NATConfig, from NATStrategy) NATResult {
	if from <= StrategyDirect {
		if result, ok := tryDirect(ctx, h, peerInfo, cfg.DirectTimeout); ok {
			return result
		}
	}
	if from <= StrategyHolePunch {
		if result, ok := tryHolePunch(ctx, h, peerInfo, cfg.HolePunchTimeout); ok {
			return result
		}
	}
	if from <= StrategyRelay {
		if result, ok := tryRelay(ctx, h, peerInfo, cfg.RelayTimeout); ok {
			return result
		}
	}
	return NATResult{Strategy: StrategyTorOnly}
}

// NATTraverse attempts to connect to peerInfo using a cascade of strategies.
// It tries each strategy in priority order and returns the first that succeeds.
// If all strategies fail, it returns StrategyTorOnly as a fallback.
func NATTraverse(ctx context.Context, h *Host, peerInfo peer.AddrInfo, cfg NATConfig) NATResult {
	// Strategy 1: Direct connection (works when UPnP mapped or both public).
	if result, ok := tryDirect(ctx, h, peerInfo, cfg.DirectTimeout); ok {
		return result
	}

	// Strategy 2: Hole punch (DCUtR — handled by libp2p internally when
	// a relay connection exists). We attempt relay first, then libp2p
	// auto-upgrades via hole punching if both peers support it.
	if result, ok := tryHolePunch(ctx, h, peerInfo, cfg.HolePunchTimeout); ok {
		return result
	}

	// Strategy 3: Circuit Relay v2 — connect via a relay peer.
	if result, ok := tryRelay(ctx, h, peerInfo, cfg.RelayTimeout); ok {
		return result
	}

	// Strategy 4: Tor-only fallback.
	return NATResult{Strategy: StrategyTorOnly}
}

// tryDirect attempts a direct QUIC connection.
func tryDirect(ctx context.Context, h *Host, pi peer.AddrInfo, timeout time.Duration) (NATResult, bool) {
	// Filter to only direct (non-relay) addresses.
	directAddrs := filterNonRelay(pi)
	if len(directAddrs.Addrs) == 0 {
		return NATResult{}, false
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	if err := h.Connect(ctx, directAddrs); err != nil {
		return NATResult{}, false
	}

	addrs := connAddrs(h, directAddrs.ID)
	return NATResult{
		Strategy: StrategyDirect,
		Addrs:    addrs,
		Latency:  time.Since(start),
	}, true
}

// tryHolePunch attempts a connection via hole punching.
// libp2p handles DCUtR automatically when a relayed connection exists,
// so we first connect via relay, then check if an upgrade happened.
func tryHolePunch(ctx context.Context, h *Host, pi peer.AddrInfo, timeout time.Duration) (NATResult, bool) {
	relayAddrs := filterRelay(pi)
	if len(relayAddrs.Addrs) == 0 {
		return NATResult{}, false
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	if err := h.Connect(ctx, relayAddrs); err != nil {
		return NATResult{}, false
	}

	// Wait briefly for libp2p to auto-upgrade via DCUtR.
	deadline := time.After(timeout - time.Since(start))
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			// Timed out — no upgrade happened. Close the relay connection
			// so tryRelay can re-establish if needed.
			h.Inner().Network().ClosePeer(pi.ID)
			return NATResult{}, false
		case <-ticker.C:
			if hasDirectConn(h, pi.ID) {
				addrs := connAddrs(h, pi.ID)
				return NATResult{
					Strategy: StrategyHolePunch,
					Addrs:    addrs,
					Latency:  time.Since(start),
				}, true
			}
		}
	}
}

// tryRelay connects via a circuit relay address.
func tryRelay(ctx context.Context, h *Host, pi peer.AddrInfo, timeout time.Duration) (NATResult, bool) {
	relayAddrs := filterRelay(pi)
	if len(relayAddrs.Addrs) == 0 {
		return NATResult{}, false
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	if err := h.Connect(ctx, relayAddrs); err != nil {
		return NATResult{}, false
	}

	addrs := connAddrs(h, pi.ID)
	return NATResult{
		Strategy: StrategyRelay,
		Addrs:    addrs,
		Latency:  time.Since(start),
	}, true
}

// filterNonRelay returns only direct (non-relay) addrs from a peer.AddrInfo.
func filterNonRelay(pi peer.AddrInfo) peer.AddrInfo {
	filtered := peer.AddrInfo{ID: pi.ID}
	for _, a := range pi.Addrs {
		if !strings.Contains(a.String(), "/p2p-circuit") {
			filtered.Addrs = append(filtered.Addrs, a)
		}
	}
	return filtered
}

// filterRelay returns only relay (circuit) addrs from a peer.AddrInfo.
func filterRelay(pi peer.AddrInfo) peer.AddrInfo {
	filtered := peer.AddrInfo{ID: pi.ID}
	for _, a := range pi.Addrs {
		if strings.Contains(a.String(), "/p2p-circuit") {
			filtered.Addrs = append(filtered.Addrs, a)
		}
	}
	return filtered
}

// hasDirectConn checks if any connection to peerID is direct (non-relay).
func hasDirectConn(h *Host, peerID peer.ID) bool {
	for _, c := range h.Inner().Network().ConnsToPeer(peerID) {
		ra := c.RemoteMultiaddr().String()
		if !strings.Contains(ra, "/p2p-circuit") {
			return true
		}
	}
	return false
}

// connAddrs returns the remote multiaddrs of all connections to a peer.
func connAddrs(h *Host, peerID peer.ID) []string {
	conns := h.Inner().Network().ConnsToPeer(peerID)
	out := make([]string, 0, len(conns))
	for _, c := range conns {
		out = append(out, fmt.Sprintf("%s/p2p/%s", c.RemoteMultiaddr(), peerID))
	}
	return out
}
