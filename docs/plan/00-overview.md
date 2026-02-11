# Ensemble — Overview

## What This Is

A fully decentralized P2P messaging and file transfer application. No central server. Privacy-first.

**Key insight**: Discovery and signaling happen over Tor (hiding your IP from the network), but once two peers mutually agree to talk, they swap real IPs and connect directly for speed. Your IP is only ever revealed to people you explicitly accept.

## Connection Flow

```
1. DISCOVER — Query DHT over Tor → find peer's .onion address (hash-only records, no public keys)
2. SIGNAL   — Dial .onion, send ConnRequest (addresses only, no public keys yet)
3. GATE     — Responder checks if initiator is a known contact; unknown = prompt user
4. VERIFY   — 3-step handshake: responder proves identity first, then initiator
               Public keys exchanged only after mutual address verification
5. IP SWAP  — Encrypt real IPs to each other's pubkeys, exchange over Tor
6. NAT      — UPnP (2s) → hole punch (10s) → relay → Tor fallback
7. CONNECT  — QUIC handshake, Noise auth with same Ed25519 keys
8. DATA     — Chat and files at full speed
```

## Identity System

```
Generate:  32-byte random seed → Ed25519 keypair (32B pub, 64B priv)
Address:   pubkey → SHA-256 → RIPEMD-160 → Base58Check(version=0x45)
           Result: ~34 char string starting with "E"
Storage:   Argon2id(passphrase) → AES-256-GCM encrypt private key → file
libp2p:    Same Ed25519 key → libp2p PeerID (single identity across all layers)
```

New identity = generate new keypair. No registration, no server.

## NAT Traversal Cascade

| Priority | Strategy | Timeout | Success Rate | Latency |
|----------|----------|---------|--------------|---------|
| 1 | UPnP/NAT-PMP | 2s | Router-dependent | None |
| 2 | UDP hole punch (DCUtR) | 10s | ~70% | Low |
| 3 | Circuit Relay v2 | 15s | 100% if relay available | Medium |
| 4 | Tor-only fallback | Always | 100% | High (~300ms) |

## Protocol Messages

**Signaling** (over Tor): `ConnRequest`, `ConnResponse`, `IPExchangeMsg`
**Chat** (over direct): `ChatText`, `ChatAck`, `ChatTyping`
**File transfer** (over direct): `FileOffer`, `FileAccept/Reject`, `FileChunk`, `FileChunkAck`, `FileComplete`, `FileCancel`
**DHT** (over Tor): `DHTFindNode`, `DHTNodeResponse`, `DHTPutRecord`
**Presence**: `Ping`, `Pong`

All wrapped in a signed `Envelope` (type + payload + timestamp + pubkey + Ed25519 signature). Length-prefixed framing (4-byte big-endian + protobuf). Max message: 16MB. Note: DHT messages use unsigned envelopes (no public key in transit); signing happens at the handshake layer.

## Key Dependencies

```
# Networking
github.com/libp2p/go-libp2p          # Core: QUIC, yamux, Noise, AutoNAT, DCUtR

# Tor
github.com/cretz/bine                 # Tor hidden service management
github.com/ipsn/go-libtor             # Embedded Tor (no external daemon)

# Identity & Crypto
github.com/btcsuite/btcutil           # Base58Check encoding
golang.org/x/crypto/ripemd160         # Address hashing
golang.org/x/crypto/argon2            # Key derivation for keystore

# API & Serialization
google.golang.org/grpc                # gRPC server + client
google.golang.org/protobuf            # Protobuf

# TUI
github.com/charmbracelet/bubbletea    # TUI framework
github.com/charmbracelet/bubbles      # TUI components
github.com/charmbracelet/lipgloss     # TUI styling

# Misc
github.com/google/uuid                # Message/transfer IDs
```

## Design Decisions

**Daemon + gRPC API**: Node is a long-running process (Tor, DHT, connections). gRPC makes it a proper daemon — TUI, mobile, scripts all connect via the same API. In-process TUI bypasses gRPC (zero overhead). `Subscribe` streaming RPC bridges async P2P events to any client.

**Single binary, three modes**: `ensemble` = interactive. `--headless` = server. `attach` = remote.

**App-level DHT over Tor**: libp2p's DHT over Tor requires a custom transport that could leak IPs. Our DHT uses direct `.onion`-to-`.onion` connections — IP leaks impossible by construction. DHT records are hash-only (address + onion, no public keys) so they cannot be used for quantum key harvesting. Anti-Sybil rate limiting. Anti-eclipse bucket diversification. 24h record expiry.

**Quantum-resistant design**: Public keys are never broadcast to the DHT or revealed to unverified peers. Addresses (RIPEMD-160 hash of public key) are quantum-safe. Public keys are only exchanged during the 3-step signaling handshake, and only after the responder verifies the initiator is a known contact or the user explicitly accepts. The identity system is behind a `Keypair` interface so Ed25519 can be swapped for post-quantum signatures (e.g. CRYSTALS-Dilithium) without changing the rest of the stack.

**Contact-gated signaling**: The signaling server only reveals identity (public key) to peers whose address is in the local contact list or explicitly accepted by the user via TUI prompt. This prevents both quantum key harvesting and spam.

**Bootstrap cascade**: Nodes rejoin the DHT on restart via: (1) saved routing table peers from disk, (2) contacts' stored onion addresses, (3) mDNS LAN discovery, (4) hardcoded bootstrap `.onion` nodes. Hardcoded nodes are only needed for brand new users — once you've connected to anyone, saved peers and contacts handle future bootstrapping.

**Same Ed25519 key for everything**: Address, signaling, Noise handshakes — one keypair. Swappable for post-quantum signatures in the future.

**Embedded Tor only**: External `tor` binary via bine. Works on Pi, desktop, containers. ~50-100MB RAM, 30-60s bootstrap.

**Expandable for video**: yamux multiplexing + new protocol IDs + protobuf evolution = video streams later without breaking changes.

**Both online only**: No store-and-forward. Honest P2P constraint. Simple and trustless.

## Project Structure

```
ensemble/
├── go.mod
├── main.go
├── Makefile
├── CLAUDE.md
├── api/proto/ensemble.proto          # gRPC service (public API)
├── internal/
│   ├── daemon/                       # Daemon lifecycle + config
│   ├── api/                          # gRPC server implementation
│   ├── identity/                     # Keypair, address, keystore
│   ├── tor/                          # Engine, hidden service, dialer
│   ├── discovery/                    # DHT, mDNS, manager
│   ├── signaling/                    # Handshake, IP exchange
│   ├── transport/                    # libp2p host, NAT, connector
│   ├── protocol/proto/messages.proto # P2P wire protocol (internal)
│   ├── chat/                         # Message service, history
│   ├── filetransfer/                 # Chunker, Merkle, sender, receiver
│   ├── contacts/                     # Contact store
│   ├── node/                         # Orchestrator, event bus
│   └── ui/                           # Bubble Tea TUI
└── testutil/                         # Test helpers
```
