package node

import (
	"context"
	"fmt"
	"sync"

	"github.com/boxsie/ensemble/internal/contacts"
	"github.com/boxsie/ensemble/internal/discovery"
	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/signaling"
	"github.com/boxsie/ensemble/internal/transport"
)

// Node is the core orchestrator for all subsystems.
type Node struct {
	keypair  *identity.Keypair
	address  identity.Address
	eventBus *EventBus

	mu        sync.RWMutex
	torState  string // "disabled", "bootstrapping", "ready"
	onionAddr string // full .onion address

	// Subsystems (set after Tor is ready).
	contacts  *contacts.Store
	discovery *discovery.Manager
	sigServer *signaling.Server
	sigClient *signaling.Client

	// Transport layer (direct connections).
	host      *transport.Host
	connector *transport.Connector

	// Chat function (set by daemon to avoid import cycle).
	sendMsg func(ctx context.Context, addr, text string) (string, error)

	// File transfer functions (set by daemon to avoid import cycle).
	sendFile   func(ctx context.Context, peerAddr, filePath string) (string, error)
	acceptFile func(transferID, savePath string) error
	rejectFile func(transferID, reason string) error

	// Pending connection requests from unknown peers.
	pendingReqs map[string]chan signaling.ConnectionDecision
	pendingMu   sync.Mutex
}

// New creates a node with the given identity.
func New(kp *identity.Keypair) *Node {
	return &Node{
		keypair:     kp,
		address:     identity.DeriveAddress(kp.PublicKey()),
		eventBus:    NewEventBus(),
		torState:    "disabled",
		pendingReqs: make(map[string]chan signaling.ConnectionDecision),
	}
}

// Identity returns the node's keypair.
func (n *Node) Identity() *identity.Keypair {
	return n.keypair
}

// Address returns the node's ensemble address.
func (n *Node) Address() identity.Address {
	return n.address
}

// EventBus returns the node's event bus.
func (n *Node) EventBus() *EventBus {
	return n.eventBus
}

// TorState returns the current Tor state ("disabled", "bootstrapping", "ready").
func (n *Node) TorState() string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.torState
}

// SetTorState updates the Tor state.
func (n *Node) SetTorState(state string) {
	n.mu.Lock()
	n.torState = state
	n.mu.Unlock()
}

// OnionAddr returns the .onion address, or "" if not yet available.
func (n *Node) OnionAddr() string {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.onionAddr
}

// SetOnionAddr sets the .onion address.
func (n *Node) SetOnionAddr(addr string) {
	n.mu.Lock()
	n.onionAddr = addr
	n.mu.Unlock()
}

// SetContacts sets the contact store.
func (n *Node) SetContacts(cs *contacts.Store) {
	n.mu.Lock()
	n.contacts = cs
	n.mu.Unlock()
}

// Contacts returns the contact store.
func (n *Node) Contacts() *contacts.Store {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.contacts
}

// SetHost sets the libp2p host for direct connections.
func (n *Node) SetHost(h *transport.Host) {
	n.mu.Lock()
	n.host = h
	n.mu.Unlock()
}

// Host returns the libp2p host, or nil if not yet started.
func (n *Node) Host() *transport.Host {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.host
}

// SetConnector sets the connection orchestrator.
func (n *Node) SetConnector(c *transport.Connector) {
	n.mu.Lock()
	n.connector = c
	n.mu.Unlock()
}

// SetDiscovery sets the discovery manager.
func (n *Node) SetDiscovery(m *discovery.Manager) {
	n.mu.Lock()
	n.discovery = m
	n.mu.Unlock()
}

// SetSignaling sets the signaling server and client.
func (n *Node) SetSignaling(server *signaling.Server, client *signaling.Client) {
	n.mu.Lock()
	n.sigServer = server
	n.sigClient = client
	n.mu.Unlock()

	if server != nil {
		server.OnConnectionRequest(n.handleConnectionRequest)
	}
}

// AddNode bootstraps discovery by dialing a known seed node's onion address.
// Returns the number of new peers discovered.
func (n *Node) AddNode(ctx context.Context, onionAddr string) (int, error) {
	n.mu.RLock()
	disc := n.discovery
	n.mu.RUnlock()
	if disc == nil {
		return 0, fmt.Errorf("discovery not initialized")
	}
	return disc.AddNode(ctx, onionAddr)
}

// Connect initiates a connection to a peer by address.
// When a Connector is available, runs the full flow:
// discovery → signaling → IP exchange → NAT traversal → direct connection.
// Falls back to signaling-only when no transport layer is configured.
func (n *Node) Connect(ctx context.Context, peerAddr string) (*signaling.HandshakeResult, error) {
	n.mu.RLock()
	disc := n.discovery
	client := n.sigClient
	conn := n.connector
	n.mu.RUnlock()

	if disc == nil || client == nil {
		return nil, fmt.Errorf("node not ready (discovery/signaling not initialized)")
	}

	// Full flow via Connector (discovery → signaling → IP exchange → NAT).
	if conn != nil {
		pc, err := conn.Connect(ctx, peerAddr)
		if err != nil {
			return nil, fmt.Errorf("connecting: %w", err)
		}
		_ = pc
		return &signaling.HandshakeResult{PeerAddr: peerAddr}, nil
	}

	// Fallback: signaling only (no transport layer).
	peer, err := disc.FindPeer(ctx, peerAddr)
	if err != nil {
		return nil, fmt.Errorf("looking up peer: %w", err)
	}
	result, err := client.RequestConnection(ctx, peer.OnionAddr, peerAddr)
	if err != nil {
		return nil, fmt.Errorf("signaling handshake: %w", err)
	}
	return result, nil
}

// AcceptConnection accepts a pending connection request from an unknown peer.
func (n *Node) AcceptConnection(addr string) error {
	n.pendingMu.Lock()
	ch, ok := n.pendingReqs[addr]
	delete(n.pendingReqs, addr)
	n.pendingMu.Unlock()

	if !ok {
		return fmt.Errorf("no pending request from %s", addr)
	}

	ch <- signaling.ConnectionDecision{Accepted: true}
	return nil
}

// RejectConnection rejects a pending connection request from an unknown peer.
func (n *Node) RejectConnection(addr string, reason string) error {
	n.pendingMu.Lock()
	ch, ok := n.pendingReqs[addr]
	delete(n.pendingReqs, addr)
	n.pendingMu.Unlock()

	if !ok {
		return fmt.Errorf("no pending request from %s", addr)
	}

	ch <- signaling.ConnectionDecision{Accepted: false, Reason: reason}
	return nil
}

// RTSize returns the DHT routing table size.
func (n *Node) RTSize() int {
	n.mu.RLock()
	disc := n.discovery
	n.mu.RUnlock()
	if disc != nil {
		return disc.RTSize()
	}
	return 0
}

// DebugInfo holds diagnostic information about the node.
type DebugInfo struct {
	RTSize      int
	RTPeers     []DebugPeer
	Connections []DebugConnection
	OnionAddr   string
}

// DebugPeer represents a peer in the routing table for debug output.
type DebugPeer struct {
	Address   string
	OnionAddr string
	LastSeen  int64 // Unix millis
}

// DebugConnection represents a connection for debug output.
type DebugConnection struct {
	Address string
	State   string
	Error   string
}

// GetDebugInfo aggregates diagnostic info from subsystems.
func (n *Node) GetDebugInfo() *DebugInfo {
	n.mu.RLock()
	disc := n.discovery
	conn := n.connector
	onion := n.onionAddr
	n.mu.RUnlock()

	info := &DebugInfo{OnionAddr: onion}

	if disc != nil {
		peers := disc.ListPeers()
		info.RTSize = len(peers)
		for _, p := range peers {
			info.RTPeers = append(info.RTPeers, DebugPeer{
				Address:   p.Address,
				OnionAddr: p.OnionAddr,
				LastSeen:  p.LastSeen.UnixMilli(),
			})
		}
	}

	if conn != nil {
		for _, pc := range conn.ActivePeers() {
			info.Connections = append(info.Connections, DebugConnection{
				Address: pc.Address,
				State:   pc.State.String(),
			})
		}
	}

	return info
}

// PeerCount returns the number of active peer connections.
func (n *Node) PeerCount() int32 {
	n.mu.RLock()
	conn := n.connector
	n.mu.RUnlock()

	if conn != nil {
		return int32(len(conn.ActivePeers()))
	}
	return 0
}

// MaxPeers returns the configured max peer limit, or 0 if no connector.
func (n *Node) MaxPeers() int {
	n.mu.RLock()
	conn := n.connector
	n.mu.RUnlock()
	if conn != nil {
		return conn.MaxPeers()
	}
	return 0
}

// ActiveTransfers returns the number of in-flight file transfers.
func (n *Node) ActiveTransfers() int {
	n.mu.RLock()
	conn := n.connector
	n.mu.RUnlock()
	if conn != nil {
		return conn.ActiveTransfers()
	}
	return 0
}

// MaxTransfers returns the configured concurrent transfer limit.
func (n *Node) MaxTransfers() int {
	n.mu.RLock()
	conn := n.connector
	n.mu.RUnlock()
	if conn != nil {
		return conn.MaxTransfers()
	}
	return 0
}

// AcquireTransferSlot tries to acquire a concurrent transfer slot.
func (n *Node) AcquireTransferSlot() bool {
	n.mu.RLock()
	conn := n.connector
	n.mu.RUnlock()
	if conn != nil {
		return conn.AcquireTransferSlot()
	}
	return true // no limits if no connector
}

// ReleaseTransferSlot returns a concurrent transfer slot.
func (n *Node) ReleaseTransferSlot() {
	n.mu.RLock()
	conn := n.connector
	n.mu.RUnlock()
	if conn != nil {
		conn.ReleaseTransferSlot()
	}
}

// ConnectionInfo returns the state of a specific peer connection, or nil.
func (n *Node) ConnectionInfo(addr string) *transport.PeerConnection {
	n.mu.RLock()
	conn := n.connector
	n.mu.RUnlock()

	if conn == nil {
		return nil
	}
	return conn.GetPeer(addr)
}

// ActiveConnections returns all active peer connections.
func (n *Node) ActiveConnections() []*transport.PeerConnection {
	n.mu.RLock()
	conn := n.connector
	n.mu.RUnlock()

	if conn == nil {
		return nil
	}
	return conn.ActivePeers()
}

// SetSendMessage sets the chat send function (injected by daemon to avoid import cycle).
func (n *Node) SetSendMessage(fn func(ctx context.Context, addr, text string) (string, error)) {
	n.mu.Lock()
	n.sendMsg = fn
	n.mu.Unlock()
}

// SendMessage sends a chat message to a connected peer.
func (n *Node) SendMessage(ctx context.Context, addr, text string) (string, error) {
	n.mu.RLock()
	fn := n.sendMsg
	n.mu.RUnlock()

	if fn == nil {
		return "", fmt.Errorf("chat not initialized")
	}
	return fn(ctx, addr, text)
}

// SetSendFile sets the file send function (injected by daemon to avoid import cycle).
func (n *Node) SetSendFile(fn func(ctx context.Context, peerAddr, filePath string) (string, error)) {
	n.mu.Lock()
	n.sendFile = fn
	n.mu.Unlock()
}

// SendFile initiates a file transfer to a connected peer. Returns the transfer ID.
func (n *Node) SendFile(ctx context.Context, peerAddr, filePath string) (string, error) {
	n.mu.RLock()
	fn := n.sendFile
	n.mu.RUnlock()

	if fn == nil {
		return "", fmt.Errorf("file transfer not initialized")
	}
	return fn(ctx, peerAddr, filePath)
}

// SetAcceptFile sets the file accept function (injected by daemon to avoid import cycle).
func (n *Node) SetAcceptFile(fn func(transferID, savePath string) error) {
	n.mu.Lock()
	n.acceptFile = fn
	n.mu.Unlock()
}

// AcceptFile accepts an incoming file offer.
func (n *Node) AcceptFile(transferID, savePath string) error {
	n.mu.RLock()
	fn := n.acceptFile
	n.mu.RUnlock()

	if fn == nil {
		return fmt.Errorf("file transfer not initialized")
	}
	return fn(transferID, savePath)
}

// SetRejectFile sets the file reject function (injected by daemon to avoid import cycle).
func (n *Node) SetRejectFile(fn func(transferID, reason string) error) {
	n.mu.Lock()
	n.rejectFile = fn
	n.mu.Unlock()
}

// RejectFile rejects an incoming file offer.
func (n *Node) RejectFile(transferID, reason string) error {
	n.mu.RLock()
	fn := n.rejectFile
	n.mu.RUnlock()

	if fn == nil {
		return fmt.Errorf("file transfer not initialized")
	}
	return fn(transferID, reason)
}

// handleConnectionRequest is called by the signaling server when an unknown peer connects.
func (n *Node) handleConnectionRequest(cr *signaling.ConnectionRequest) {
	n.pendingMu.Lock()
	n.pendingReqs[cr.FromAddress] = cr.Decision
	n.pendingMu.Unlock()

	// Emit event for TUI/gRPC clients.
	n.eventBus.Publish(Event{
		Type:    EventConnectionReq,
		Payload: cr.FromAddress,
	})
}
