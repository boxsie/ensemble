package transport

import (
	"context"
	"fmt"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/boxsie/ensemble/internal/identity"
)

// HostConfig configures the libp2p host.
type HostConfig struct {
	// Keypair is the Ed25519 identity for this node.
	Keypair *identity.Keypair
	// ListenPort is the UDP port for QUIC. 0 picks a random port.
	ListenPort int
	// DisableNATMap disables UPnP/NAT-PMP port mapping.
	DisableNATMap bool
	// DisableHolePunch disables DCUtR hole punching.
	DisableHolePunch bool
}

// Host wraps a libp2p host for direct QUIC connections.
type Host struct {
	host host.Host
}

// NewHost creates a libp2p host with QUIC transport, Noise security,
// yamux muxer, AutoNAT, optional NATPortMap, hole punching, and relay client.
func NewHost(cfg HostConfig) (*Host, error) {
	privKey, err := cfg.Keypair.ToLibp2pKey()
	if err != nil {
		return nil, fmt.Errorf("converting identity key: %w", err)
	}

	listenAddr := fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", cfg.ListenPort)
	listenAddr6 := fmt.Sprintf("/ip6/::/udp/%d/quic-v1", cfg.ListenPort)

	opts := []libp2p.Option{
		libp2p.Identity(privKey),
		libp2p.ListenAddrStrings(listenAddr, listenAddr6),
		libp2p.EnableNATService(),
		libp2p.EnableAutoNATv2(),
	}

	if !cfg.DisableNATMap {
		opts = append(opts, libp2p.NATPortMap())
	}
	if !cfg.DisableHolePunch {
		opts = append(opts, libp2p.EnableHolePunching())
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("creating libp2p host: %w", err)
	}

	return &Host{host: h}, nil
}

// ID returns the libp2p peer ID.
func (h *Host) ID() peer.ID {
	return h.host.ID()
}

// Addrs returns the multiaddrs this host is listening on.
func (h *Host) Addrs() []string {
	addrs := h.host.Addrs()
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}
	return out
}

// AddrInfo returns the full peer.AddrInfo for connecting to this host.
func (h *Host) AddrInfo() peer.AddrInfo {
	return peer.AddrInfo{
		ID:    h.host.ID(),
		Addrs: h.host.Addrs(),
	}
}

// Connect dials a remote peer.
func (h *Host) Connect(ctx context.Context, pi peer.AddrInfo) error {
	return h.host.Connect(ctx, pi)
}

// Inner returns the underlying libp2p host for stream handling.
func (h *Host) Inner() host.Host {
	return h.host
}

// Close shuts down the host.
func (h *Host) Close() error {
	return h.host.Close()
}
