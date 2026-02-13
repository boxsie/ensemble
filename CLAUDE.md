# Ensemble

Fully decentralized P2P messaging and file transfer. No central server. Discovery over Tor (IP hidden from the network), direct connections after mutual consent (fast). Bitcoin-style cryptographic identity — no accounts, just a keypair.

## Tech Stack

Go, libp2p (QUIC/yamux/TLS 1.3), Tor (bine + external binary), Ed25519 + Base58Check identity, Protobuf, gRPC API, Bubble Tea TUI.

## Architecture

```
Clients:  TUI (in-process) | TUI (attach) | Mobile (future)
          Direct Go calls    gRPC socket     gRPC
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

## Project Structure

```
ensemble/
  main.go                             Entry point (daemon/TUI/attach)
  Makefile                            Build, test, proto, cross-compile
  api/proto/ensemble.proto            gRPC service definition (public API)
  internal/
    daemon/                           Daemon lifecycle and config
    api/                              gRPC server implementation
    identity/                         Keypair, address, keystore
    tor/                              Tor engine, hidden service, dialer
    discovery/                        Kademlia DHT, mDNS, manager
    signaling/                        3-step handshake, IP exchange
    transport/                        libp2p host, NAT traversal, connector
    protocol/proto/messages.proto     P2P wire protocol (internal)
    chat/                             Message service and history
    filetransfer/                     Chunking, Merkle tree, sender/receiver
    contacts/                         Contact store
    node/                             Core orchestrator, event bus
    ui/                               Bubble Tea TUI and screens
  testutil/                           Shared test helpers
```

## Key Patterns

- **Circular import avoidance**: `node` cannot import `chat`/`filetransfer`. Instead, node holds callback functions (e.g. `sendMsg`, `sendFile`) that the daemon wires up. Same pattern for any app-layer → node dependency.
- **Backend interface**: `ui.Backend` abstracts daemon access. `DirectBackend` wraps the node for in-process TUI. `GRPCBackend` wraps a gRPC client for attach mode. Both implement the same interface so TUI code is identical.
- **Event streaming**: Bubble Tea uses a channel-based pattern — `eventChanMsg` delivers the channel, `waitForEvent` reads one event at a time, each handler returns a new `waitForEvent` cmd to chain reads.
- **Tor integration**: Uses external `tor` binary via bine's `process.NewCreator(torPath)`. No CGO needed. System torrc interference avoided by passing `--defaults-torrc <empty-file>` in ExtraArgs.
- **Subsystem wiring**: Discovery, signaling, and connector are initialized in the daemon's `wireSubsystems` callback after Tor is ready. Adapter types bridge between packages (e.g. `torDialerAdapter`, `discoveryFinderAdapter`).

## Security Design

- **Hash-only DHT**: PeerRecords contain only address hash + onion address. No public keys broadcast to the network.
- **Contact-gated signaling**: Public keys only revealed to known contacts or user-approved peers.
- **3-step handshake**: Addresses exchanged first, then responder proves identity, then initiator proves identity.
- **Post-quantum ready**: `Keypair` interface designed for Ed25519 → post-quantum swap without changing upper layers.

## Coding Conventions

- **Go style**: Follow standard Go conventions (`gofmt`, `golint`, effective Go)
- **Packages**: All application code under `internal/` (not importable externally)
- **Public API**: Only `api/proto/ensemble.proto` is the public contract
- **Errors**: Wrap errors with context using `fmt.Errorf("doing thing: %w", err)`
- **Tests**: Every package gets `*_test.go`. Integration tests use `//go:build integration`
- **Protobuf**: Two separate schemas — `api/proto/` (gRPC service) and `internal/protocol/proto/` (P2P wire)
- **Naming**: Bitcoin-style addresses start with "E" (version byte `0x21` produces "E" prefix in Base58Check)
- **Proto regeneration**: `make proto` — runs protoc for both API and wire protocol schemas

## Useful Commands

```bash
make build          # Build binary to bin/ensemble
make test           # Unit tests
make test-race      # Tests with race detector
make test-integration  # Integration tests (requires Tor)
make proto          # Regenerate protobuf Go code
make build-all      # Cross-compile for all platforms
```
