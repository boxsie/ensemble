# Ensemble

Fully decentralized P2P messaging and file transfer. No central server. Discovery over Tor (IP hidden from the network), direct connections after mutual consent (fast). Bitcoin-style cryptographic identity — no accounts, just a keypair.

## Tech Stack

Go, libp2p (QUIC/yamux/TLS 1.3), embedded Tor (bine + go-libtor), Ed25519 + Base58Check identity, Protobuf, gRPC API, Bubble Tea TUI.

## Architecture

```
Clients:  TUI (in-process) | TUI (attach) | Mobile (future)
          ↕ Direct Go calls  ↕ gRPC socket   ↕ gRPC
─────────────────────────────────────────────────────
gRPC API Layer  (api/proto/ensemble.proto)
─────────────────────────────────────────────────────
Node (Daemon Core)  — orchestrates all subsystems
─────────────────────────────────────────────────────
App Layer:     Chat, File Transfer, Contacts
Transport:     Direct QUIC/TLS 1.3 (after consent)
Signaling:     Tor hidden services (anonymous)
Discovery:     App-level Kademlia DHT over Tor + mDNS
```

## Deployment

Single binary: `ensemble` (daemon+TUI) | `ensemble --headless` (daemon only) | `ensemble attach` (TUI→remote daemon)

## Plan Documents

**[CHECKLIST](docs/plan/CHECKLIST.md)** — live progress tracker. Check here first to see what's done and what's next.

Work through tickets in order. Each ticket has files to create, what to implement, and a verification step.

- [Overview](docs/plan/00-overview.md) — architecture, connection flow, design decisions
- [Phase 1: Foundation](docs/plan/01-foundation.md) — T-01 to T-14: identity, protobuf, gRPC, daemon, TUI
- [Phase 2: Tor](docs/plan/02-tor.md) — T-15 to T-18: embedded Tor, hidden services, dialer
- [Phase 3: Discovery & Signaling](docs/plan/03-discovery-signaling.md) — T-19 to T-27: DHT, mDNS, handshake
- [Phase 4: Direct Connection](docs/plan/04-direct-connection.md) — T-28 to T-32: IP exchange, libp2p, NAT traversal
- [Phase 5: Chat](docs/plan/05-chat.md) — T-33 to T-37: messaging, history, TUI
- [Phase 6: File Transfer](docs/plan/06-file-transfer.md) — T-38 to T-44: chunking, Merkle tree, sender/receiver
- [Phase 7: Hardening](docs/plan/07-hardening.md) — T-45 to T-49: reconnection, multi-peer, cross-platform

## Dependency Graph

```
T-01 (scaffolding)
 ├→ T-02 (keypair) → T-03 (address) → T-04 (keystore)
 ├→ T-05 (event bus)
 ├→ T-06 (wire proto) → T-07 (codec)
 ├→ T-08 (contacts)
 ├→ T-09 (gRPC proto) → T-10 (gRPC server) → T-11 (daemon)
 ├→ T-12 (TUI client) → T-13 (TUI skeleton)
 └→ T-14 (main.go) — depends on T-11, T-12, T-13

T-15 (Tor engine) → T-16 (hidden service) → T-17 (dialer) → T-18 (wire into daemon)

T-19 (routing table) → T-20 (DHT protocol)
T-23 (handshake) → T-24 (signaling server) + T-25 (signaling client)
T-20 + T-21 (mDNS) → T-22 (discovery manager)
T-22 + T-24 + T-25 → T-26 (wire into node) → T-27 (TUI)

T-28 (IP exchange) → T-29 (libp2p host) → T-30 (NAT) → T-31 (connector) → T-32 (wire into node)

T-33 (chat service) → T-34 (history) → T-35 (wire into node) → T-36 (TUI chat) → T-37 (online status)

T-38 (chunker) → T-39 (Merkle) → T-40 (sender) + T-41 (receiver) → T-42 (progress) → T-43 (wire into node) → T-44 (TUI)

T-45 + T-46 + T-47 + T-48 + T-49 (hardening — all independent)
```

## Security Design

- **Hash-only DHT**: PeerRecords contain only address hash + onion address. No public keys broadcast to the network. Quantum-safe by design.
- **Contact-gated signaling**: Public keys only revealed to known contacts or user-approved peers. Unknown addresses trigger TUI prompt before key exposure.
- **3-step handshake**: Addresses (hashes) exchanged first, then responder proves identity, then initiator proves identity. No key material until both sides verified.
- **Post-quantum ready**: `Keypair` interface designed for Ed25519 → post-quantum swap (e.g. CRYSTALS-Dilithium) without changing discovery/signaling/transport layers.

## Coding Conventions

- **Go style**: Follow standard Go conventions (`gofmt`, `golint`, effective Go)
- **Packages**: All application code under `internal/` (not importable externally)
- **Public API**: Only `api/proto/ensemble.proto` is the public contract
- **Errors**: Wrap errors with context using `fmt.Errorf("doing thing: %w", err)`
- **Tests**: Every package gets `*_test.go`. Integration tests use `//go:build integration`
- **Protobuf**: Two separate schemas — `api/proto/` (gRPC service) and `internal/protocol/proto/` (P2P wire)
- **Naming**: Bitcoin-style addresses start with "E" (version byte `0x45`)

## How to Pick Up Work

1. Check which phase you're on by looking at what exists in `internal/`
2. Read the corresponding plan file in `docs/plan/`
3. Find the next unfinished ticket
4. Implement it, run the verification step
5. Move to the next ticket
