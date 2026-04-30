package transport

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/boxsie/ensemble/internal/identity"
	"github.com/boxsie/ensemble/internal/protocol"
	pb "github.com/boxsie/ensemble/internal/protocol/pb"
	"github.com/boxsie/ensemble/internal/signaling"
)

// ConnState represents the state of a peer connection.
type ConnState int

const (
	StateDiscovering   ConnState = iota // looking up peer in DHT/mDNS
	StateSignaling                      // 3-step handshake over Tor
	StateExchangingIPs                  // encrypted IP swap
	StateNATTraversal                   // attempting direct connection
	StateConnected                      // direct QUIC connection
	StateConnectedTor                   // Tor-only fallback
	StateDisconnected                   // cleanly disconnected
	StateFailed                         // connection attempt failed
	StateReconnecting                   // auto-reconnecting after loss
)

func (s ConnState) String() string {
	switch s {
	case StateDiscovering:
		return "discovering"
	case StateSignaling:
		return "signaling"
	case StateExchangingIPs:
		return "exchanging-ips"
	case StateNATTraversal:
		return "nat-traversal"
	case StateConnected:
		return "connected"
	case StateConnectedTor:
		return "connected-tor"
	case StateDisconnected:
		return "disconnected"
	case StateFailed:
		return "failed"
	case StateReconnecting:
		return "reconnecting"
	default:
		return "unknown"
	}
}

// PeerConnection represents an active or pending connection to a peer.
type PeerConnection struct {
	Address      string
	State        ConnState
	Strategy     NATStrategy
	Latency      time.Duration
	PeerID       peer.ID
	Addrs        []string
	LastActivity time.Time // updated on connect/reconnect
}

// StateEvent is emitted when a connection state changes.
type StateEvent struct {
	PeerAddr string
	State    ConnState
	Error    string
}

// PeerFinder abstracts peer discovery for the connector.
type PeerFinder interface {
	FindPeer(ctx context.Context, addr string) (PeerLookupResult, error)
}

// PeerLookupResult holds discovery results.
type PeerLookupResult struct {
	Address   string
	OnionAddr string
}

// TorDialer abstracts Tor network dialing.
type TorDialer interface {
	DialContext(ctx context.Context, address string) (net.Conn, error)
}

// Reconnection tuning constants.
const (
	ReconnectMaxRetries  = 3
	ReconnectBaseDelay   = 2 * time.Second
	CachedLookupMaxAge   = 1 * time.Hour
)

// cachedLookup stores a recent discovery result for reconnection.
type cachedLookup struct {
	Result    PeerLookupResult
	CachedAt  time.Time
}

// ConnectorConfig configures the connection orchestrator.
type ConnectorConfig struct {
	Keypair    *identity.Keypair
	Host       *Host
	Finder     PeerFinder
	Dialer     TorDialer
	NATCfg     NATConfig
	OnState    func(StateEvent) // called on each state transition
	MaxPeers   int              // max simultaneous peers (0 = 50)
	MaxXfers   int              // max concurrent file transfers (0 = 5)
}

// Connector manages the full peer connection lifecycle.
type Connector struct {
	keypair  *identity.Keypair
	address  string
	host     *Host
	finder   PeerFinder
	dialer   TorDialer
	natCfg   NATConfig
	onState  func(StateEvent)
	maxPeers int
	maxXfers int

	mu          sync.Mutex
	peers       map[string]*PeerConnection
	lookupCache map[string]*cachedLookup
	activeXfers int32 // atomic count of in-flight transfers
}

// NewConnector creates a connection orchestrator.
func NewConnector(cfg ConnectorConfig) *Connector {
	natCfg := cfg.NATCfg
	if natCfg.DirectTimeout == 0 {
		natCfg = DefaultNATConfig()
	}
	maxPeers := cfg.MaxPeers
	if maxPeers <= 0 {
		maxPeers = 50
	}
	maxXfers := cfg.MaxXfers
	if maxXfers <= 0 {
		maxXfers = 5
	}
	return &Connector{
		keypair:     cfg.Keypair,
		address:     string(identity.DeriveAddress(cfg.Keypair.PublicKey())),
		host:        cfg.Host,
		finder:      cfg.Finder,
		dialer:      cfg.Dialer,
		natCfg:      natCfg,
		onState:     cfg.OnState,
		maxPeers:    maxPeers,
		maxXfers:    maxXfers,
		peers:       make(map[string]*PeerConnection),
		lookupCache: make(map[string]*cachedLookup),
	}
}

// Connect initiates the full connection flow to a peer.
// Discovery → Signaling → IP Exchange → NAT Traversal → Connected.
func (c *Connector) Connect(ctx context.Context, peerAddr string) (*PeerConnection, error) {
	pc := &PeerConnection{Address: peerAddr, LastActivity: time.Now()}

	c.mu.Lock()
	if existing, ok := c.peers[peerAddr]; ok &&
		(existing.State == StateConnected || existing.State == StateConnectedTor) {
		c.mu.Unlock()
		return existing, nil
	}
	// Evict LRU peer if at capacity.
	c.evictIfNeededLocked()
	c.peers[peerAddr] = pc
	c.mu.Unlock()

	if err := c.connectFlow(ctx, pc); err != nil {
		c.setState(pc, StateFailed, err.Error())
		return nil, err
	}
	pc.LastActivity = time.Now()
	return pc, nil
}

// Reconnect attempts to re-establish a connection to a peer that went offline.
// Uses cached discovery info if fresh, otherwise performs a full DHT lookup.
func (c *Connector) Reconnect(ctx context.Context, peerAddr string) (*PeerConnection, error) {
	pc := &PeerConnection{Address: peerAddr, LastActivity: time.Now()}

	c.mu.Lock()
	if existing, ok := c.peers[peerAddr]; ok &&
		(existing.State == StateConnected || existing.State == StateConnectedTor) {
		c.mu.Unlock()
		return existing, nil
	}
	// Close stale libp2p connections for this peer.
	if existing, ok := c.peers[peerAddr]; ok && existing.PeerID != "" && c.host != nil {
		c.host.Inner().Network().ClosePeer(existing.PeerID)
	}
	c.peers[peerAddr] = pc
	c.mu.Unlock()

	c.setState(pc, StateReconnecting, "")

	var lastErr error
	for attempt := 0; attempt < ReconnectMaxRetries; attempt++ {
		if attempt > 0 {
			delay := ReconnectBaseDelay * time.Duration(1<<(attempt-1))
			select {
			case <-ctx.Done():
				c.setState(pc, StateFailed, ctx.Err().Error())
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		if err := c.connectFlow(ctx, pc); err != nil {
			lastErr = err
			continue
		}
		pc.LastActivity = time.Now()
		return pc, nil
	}

	c.setState(pc, StateFailed, lastErr.Error())
	return nil, fmt.Errorf("reconnection failed after %d attempts: %w", ReconnectMaxRetries, lastErr)
}

// Disconnect closes the connection to a peer.
func (c *Connector) Disconnect(peerAddr string) {
	c.mu.Lock()
	pc, ok := c.peers[peerAddr]
	if !ok {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	if pc.PeerID != "" {
		c.host.Inner().Network().ClosePeer(pc.PeerID)
	}
	c.setState(pc, StateDisconnected, "")

	c.mu.Lock()
	delete(c.peers, peerAddr)
	c.mu.Unlock()
}

// GetPeer returns the current state of a peer connection.
func (c *Connector) GetPeer(addr string) *PeerConnection {
	c.mu.Lock()
	defer c.mu.Unlock()
	if pc, ok := c.peers[addr]; ok {
		cp := *pc
		return &cp
	}
	return nil
}

// ActivePeers returns all active (connected) peers.
func (c *Connector) ActivePeers() []*PeerConnection {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*PeerConnection
	for _, pc := range c.peers {
		if pc.State == StateConnected || pc.State == StateConnectedTor {
			cp := *pc
			out = append(out, &cp)
		}
	}
	return out
}

func (c *Connector) connectFlow(ctx context.Context, pc *PeerConnection) error {
	// 1. Discover peer (use cache if fresh).
	c.setState(pc, StateDiscovering, "")
	lookup, err := c.cachedOrFreshLookup(ctx, pc.Address)
	if err != nil {
		return fmt.Errorf("discovering peer: %w", err)
	}

	// 2. Dial via Tor + Handshake.
	c.setState(pc, StateSignaling, "")
	conn, err := c.dialer.DialContext(ctx, lookup.OnionAddr)
	if err != nil {
		return fmt.Errorf("dialing peer: %w", err)
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}

	hsResult, err := performClientHandshake(conn, c.address, pc.Address, c.keypair)
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}

	// 3. Encrypted IP exchange on same Tor connection.
	c.setState(pc, StateExchangingIPs, "")
	// Reset deadline for IP exchange.
	conn.SetDeadline(time.Time{})
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}

	ourInfo := &signaling.IPInfo{Addrs: c.host.Addrs()}
	peerIPInfo, err := signaling.ExchangeIPs(ctx, conn, ourInfo, c.keypair, hsResult.PeerPubKey)
	if err != nil {
		return fmt.Errorf("IP exchange: %w", err)
	}

	// Derive libp2p peer ID from peer's Ed25519 public key.
	peerID, err := peerIDFromEd25519(hsResult.PeerPubKey)
	if err != nil {
		return fmt.Errorf("deriving peer ID: %w", err)
	}
	pc.PeerID = peerID

	// 4. NAT traversal (attempt direct QUIC connection).
	if len(peerIPInfo.Addrs) > 0 && c.host != nil {
		c.setState(pc, StateNATTraversal, "")

		peerAddrInfo, err := buildPeerAddrInfo(peerID, peerIPInfo.Addrs)
		if err != nil {
			return fmt.Errorf("parsing peer multiaddrs: %w", err)
		}

		natResult := NATTraverse(ctx, c.host, peerAddrInfo, c.natCfg)

		pc.Strategy = natResult.Strategy
		pc.Latency = natResult.Latency
		pc.Addrs = natResult.Addrs

		if natResult.Strategy != StrategyTorOnly {
			pc.LastActivity = time.Now()
			c.setState(pc, StateConnected, "")
			return nil
		}
	}

	// Tor-only fallback.
	pc.Strategy = StrategyTorOnly
	pc.LastActivity = time.Now()
	c.setState(pc, StateConnectedTor, "")
	return nil
}

// ReconnectWithFallback attempts to reconnect, starting from a lower NAT strategy
// if the previous one failed. Falls through: Direct → HolePunch → Relay → TorOnly.
func (c *Connector) ReconnectWithFallback(ctx context.Context, peerAddr string) (*PeerConnection, error) {
	c.mu.Lock()
	prev, hasPrev := c.peers[peerAddr]
	prevStrategy := StrategyDirect
	if hasPrev {
		prevStrategy = prev.Strategy
		// Close stale connections.
		if prev.PeerID != "" && c.host != nil {
			c.host.Inner().Network().ClosePeer(prev.PeerID)
		}
	}
	c.mu.Unlock()

	pc := &PeerConnection{Address: peerAddr, LastActivity: time.Now()}
	c.mu.Lock()
	c.peers[peerAddr] = pc
	c.mu.Unlock()

	c.setState(pc, StateReconnecting, "")

	// Try to reconnect with the same strategy first, then fall back.
	startStrategy := prevStrategy
	if err := c.connectFlowFromStrategy(ctx, pc, startStrategy); err == nil {
		return pc, nil
	}

	// If we started above TorOnly, try each lower strategy.
	for s := startStrategy + 1; s <= StrategyTorOnly; s++ {
		if err := c.connectFlowFromStrategy(ctx, pc, s); err == nil {
			return pc, nil
		}
	}

	c.setState(pc, StateFailed, "all NAT strategies exhausted")
	return nil, fmt.Errorf("all NAT strategies exhausted for %s", peerAddr)
}

// connectFlowFromStrategy runs the connection flow, skipping NAT strategies
// above the given startStrategy. If peerID is known, skip discovery/signaling.
func (c *Connector) connectFlowFromStrategy(ctx context.Context, pc *PeerConnection, startStrategy NATStrategy) error {
	// 1. Discover peer.
	c.setState(pc, StateDiscovering, "")
	lookup, err := c.cachedOrFreshLookup(ctx, pc.Address)
	if err != nil {
		return fmt.Errorf("discovering peer: %w", err)
	}

	// 2. Dial via Tor + Handshake (always needed for IP exchange).
	c.setState(pc, StateSignaling, "")
	conn, err := c.dialer.DialContext(ctx, lookup.OnionAddr)
	if err != nil {
		return fmt.Errorf("dialing peer: %w", err)
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}

	hsResult, err := performClientHandshake(conn, c.address, pc.Address, c.keypair)
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}

	// 3. Encrypted IP exchange.
	c.setState(pc, StateExchangingIPs, "")
	conn.SetDeadline(time.Time{})
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}

	ourInfo := &signaling.IPInfo{Addrs: c.host.Addrs()}
	peerIPInfo, err := signaling.ExchangeIPs(ctx, conn, ourInfo, c.keypair, hsResult.PeerPubKey)
	if err != nil {
		return fmt.Errorf("IP exchange: %w", err)
	}

	peerID, err := peerIDFromEd25519(hsResult.PeerPubKey)
	if err != nil {
		return fmt.Errorf("deriving peer ID: %w", err)
	}
	pc.PeerID = peerID

	// 4. NAT traversal from the specified start strategy.
	if len(peerIPInfo.Addrs) > 0 && c.host != nil {
		c.setState(pc, StateNATTraversal, "")

		peerAddrInfo, err := buildPeerAddrInfo(peerID, peerIPInfo.Addrs)
		if err != nil {
			return fmt.Errorf("parsing peer multiaddrs: %w", err)
		}

		natResult := NATTraverseFrom(ctx, c.host, peerAddrInfo, c.natCfg, startStrategy)
		pc.Strategy = natResult.Strategy
		pc.Latency = natResult.Latency
		pc.Addrs = natResult.Addrs

		if natResult.Strategy != StrategyTorOnly {
			pc.LastActivity = time.Now()
			c.setState(pc, StateConnected, "")
			return nil
		}
	}

	pc.Strategy = StrategyTorOnly
	pc.LastActivity = time.Now()
	c.setState(pc, StateConnectedTor, "")
	return nil
}

// cachedOrFreshLookup returns a cached discovery result if recent, or performs a new lookup.
func (c *Connector) cachedOrFreshLookup(ctx context.Context, addr string) (PeerLookupResult, error) {
	c.mu.Lock()
	cached, ok := c.lookupCache[addr]
	c.mu.Unlock()

	if ok && time.Since(cached.CachedAt) < CachedLookupMaxAge {
		return cached.Result, nil
	}

	result, err := c.finder.FindPeer(ctx, addr)
	if err != nil {
		// If fresh lookup fails but we have a stale cache, try it.
		if ok {
			return cached.Result, nil
		}
		return PeerLookupResult{}, err
	}

	c.mu.Lock()
	c.lookupCache[addr] = &cachedLookup{Result: result, CachedAt: time.Now()}
	c.mu.Unlock()

	return result, nil
}

// evictIfNeededLocked removes the least-recently-active peer if at capacity.
// Must be called with c.mu held.
func (c *Connector) evictIfNeededLocked() {
	if len(c.peers) < c.maxPeers {
		return
	}
	var oldest *PeerConnection
	var oldestAddr string
	for addr, pc := range c.peers {
		if pc.State != StateConnected && pc.State != StateConnectedTor {
			continue
		}
		if oldest == nil || pc.LastActivity.Before(oldest.LastActivity) {
			oldest = pc
			oldestAddr = addr
		}
	}
	if oldest != nil && c.host != nil && oldest.PeerID != "" {
		c.host.Inner().Network().ClosePeer(oldest.PeerID)
	}
	if oldestAddr != "" {
		delete(c.peers, oldestAddr)
	}
}

// AcquireTransferSlot tries to acquire a concurrent transfer slot.
// Returns false if the limit is reached.
func (c *Connector) AcquireTransferSlot() bool {
	for {
		cur := atomic.LoadInt32(&c.activeXfers)
		if int(cur) >= c.maxXfers {
			return false
		}
		if atomic.CompareAndSwapInt32(&c.activeXfers, cur, cur+1) {
			return true
		}
	}
}

// ReleaseTransferSlot returns a concurrent transfer slot.
func (c *Connector) ReleaseTransferSlot() {
	atomic.AddInt32(&c.activeXfers, -1)
}

// ActiveTransfers returns the number of in-flight file transfers.
func (c *Connector) ActiveTransfers() int {
	return int(atomic.LoadInt32(&c.activeXfers))
}

// MaxPeers returns the configured peer limit.
func (c *Connector) MaxPeers() int { return c.maxPeers }

// MaxTransfers returns the configured concurrent transfer limit.
func (c *Connector) MaxTransfers() int { return c.maxXfers }

// StartAutoReconnect monitors connected peers via the presence service and
// automatically attempts reconnection when a peer goes offline.
// Blocks until ctx is cancelled.
func (c *Connector) StartAutoReconnect(ctx context.Context, presence *Presence) {
	ticker := time.NewTicker(defaultPingEvery)
	defer ticker.Stop()

	// Track consecutive failures per peer.
	failures := make(map[string]int)
	const maxMissed = 3

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			peers := make([]*PeerConnection, 0, len(c.peers))
			for _, pc := range c.peers {
				if pc.State == StateConnected || pc.State == StateConnectedTor {
					cp := *pc
					peers = append(peers, &cp)
				}
			}
			c.mu.Unlock()

			for _, pc := range peers {
				if pc.PeerID == "" {
					continue
				}
				_, err := presence.PingPeer(ctx, pc.PeerID)
				if err != nil {
					failures[pc.Address]++
					if failures[pc.Address] >= maxMissed {
						log.Printf("reconnect: peer %s missed %d pings, reconnecting", pc.Address[:12], maxMissed)
						delete(failures, pc.Address)

						go func(addr string) {
							rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
							defer cancel()
							if _, err := c.ReconnectWithFallback(rctx, addr); err != nil {
								log.Printf("reconnect: %s failed: %v", addr[:12], err)
							} else {
								log.Printf("reconnect: %s succeeded", addr[:12])
							}
						}(pc.Address)
					}
				} else {
					delete(failures, pc.Address)
				}
			}
		}
	}
}

func (c *Connector) setState(pc *PeerConnection, state ConnState, errMsg string) {
	pc.State = state
	if c.onState != nil {
		c.onState(StateEvent{
			PeerAddr: pc.Address,
			State:    state,
			Error:    errMsg,
		})
	}
}

// performClientHandshake runs the initiator side of the 3-step handshake on
// an existing connection, leaving it open for subsequent IP exchange.
func performClientHandshake(conn net.Conn, ourAddr, targetAddr string, kp *identity.Keypair) (*signaling.HandshakeResult, error) {
	// Step 1: Send ConnRequest.
	reqData, reqNonce, err := signaling.CreateConnRequest(ourAddr, targetAddr)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	if err := protocol.WriteMsg(conn, unsignedEnvelope(pb.MessageType_CONN_REQUEST, reqData)); err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}

	// Step 2: Read ConnResponse.
	respEnv, err := protocol.ReadMsg(conn)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if respEnv.Type != pb.MessageType_CONN_RESPONSE {
		return nil, fmt.Errorf("unexpected message type: %v", respEnv.Type)
	}

	resp, err := signaling.ValidateConnResponse(respEnv.Payload, reqNonce, targetAddr)
	if err != nil {
		return nil, fmt.Errorf("validating response: %w", err)
	}
	if !resp.Accepted {
		reason := resp.Reason
		if reason == "" {
			reason = "rejected"
		}
		return nil, fmt.Errorf("%w: %s", signaling.ErrRejected, reason)
	}

	// Step 3: Send ConnConfirm.
	confirmData, err := signaling.CreateConnConfirm(kp, resp.Nonce)
	if err != nil {
		return nil, fmt.Errorf("creating confirm: %w", err)
	}
	if err := protocol.WriteMsg(conn, unsignedEnvelope(pb.MessageType_CONN_CONFIRM, confirmData)); err != nil {
		return nil, fmt.Errorf("sending confirm: %w", err)
	}

	return &signaling.HandshakeResult{
		PeerPubKey: ed25519.PublicKey(resp.PubKey),
		PeerAddr:   targetAddr,
	}, nil
}

func unsignedEnvelope(t pb.MessageType, payload []byte) *pb.Envelope {
	return &pb.Envelope{Type: t, Payload: payload}
}

func peerIDFromEd25519(pub ed25519.PublicKey) (peer.ID, error) {
	libKey, err := libp2pcrypto.UnmarshalEd25519PublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("unmarshaling ed25519 key: %w", err)
	}
	return peer.IDFromPublicKey(libKey)
}

func buildPeerAddrInfo(id peer.ID, addrs []string) (peer.AddrInfo, error) {
	pi := peer.AddrInfo{ID: id}
	for _, s := range addrs {
		a, err := ma.NewMultiaddr(s)
		if err != nil {
			continue // skip unparseable addresses
		}
		pi.Addrs = append(pi.Addrs, a)
	}
	if len(pi.Addrs) == 0 {
		return pi, fmt.Errorf("no valid multiaddrs from peer")
	}
	return pi, nil
}
