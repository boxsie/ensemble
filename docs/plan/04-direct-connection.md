# Phase 4: Direct Connection — T-28 to T-32

## Goal
After mutual consent, establish fast direct QUIC connection.

---

## T-28: Encrypted IP exchange
Swap real IPs over Tor after mutual consent.
- Convert Ed25519 keys → X25519 (RFC 8032 → RFC 7748)
- ECDH shared secret → HKDF-SHA256 → AES-256-GCM key
- `IPInfo { ip, port, multiaddrs, peerID }`
- `EncryptIPInfo(info, ourPriv, peerPub)`, `DecryptIPInfo(cipher, ourPriv, peerPub)`
- `ExchangeIPs(ctx, conn, ourInfo, keypair, peerPub) → peerInfo` — bidirectional exchange
- **Files**: `internal/signaling/ipexchange.go`, `internal/signaling/ipexchange_test.go`
- **Verify**: Test encrypts IP info, decrypts with peer key, data matches. Wrong key fails.

## T-29: libp2p host setup
Create libp2p host for direct QUIC connections.
- QUIC transport (TLS 1.3 + UDP)
- yamux stream multiplexer
- Noise security for authentication
- AutoNAT for reachability detection
- NATPortMap (UPnP/NAT-PMP)
- Circuit Relay v2 client
- DCUtR (hole punching)
- **Files**: `internal/transport/host.go`, `internal/transport/host_test.go`
- **Verify**: Host starts, listens on QUIC, reports its multiaddrs. Two hosts on same machine can connect.

## T-30: NAT traversal cascade
Try each strategy in priority order.
- Strategy 1: UPnP/NAT-PMP (2s timeout)
- Strategy 2: UDP hole punch via DCUtR (10s timeout), coordinated over Tor signaling channel
- Strategy 3: Circuit Relay v2 through a willing peer (15s timeout)
- Strategy 4: Tor-only fallback (use existing Tor connection for data)
- Return `NATResult { strategy, multiaddrs, latency }`
- **Files**: `internal/transport/nat.go`, `internal/transport/nat_test.go`
- **Verify**: Test each strategy individually. Cascade falls through when higher priority fails.

## T-31: Connection orchestrator
Full lifecycle from discovery to established connection.
- `Connector.Connect(ctx, peerAddr) → PeerConnection`
- State machine: Discovering → Signaling → NATTraversal → Connected / ConnectedTor
- State changes emit events → gRPC → TUI
- Manage active connections map
- `Connector.Disconnect(peerAddr)`
- **Files**: `internal/transport/connector.go`, `internal/transport/streams.go`
- **Verify**: Full flow: discover → signal → IP swap → NAT → connected. State events received.

## T-32: Wire transport into node
Integrate libp2p host and connector into daemon.
- Node creates libp2p host on startup
- After signaling acceptance → IP exchange → NAT traversal → direct connection
- `GetStatus` RPC returns per-peer info (state, NAT strategy, latency)
- Connection state events flow to TUI
- **Files**: Update `internal/node/node.go`, `internal/api/server.go`
- **Verify**: Two nodes on different networks: full Tor discovery → signaling → direct QUIC. `GetStatus` shows strategy and latency.
