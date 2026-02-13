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

Single binary: `ensemble` (daemon+TUI) | `ensemble --headless` (daemon only) | `ensemble attach` (TUI→remote daemon) | `ensemble debug` (CLI diagnostics)

## Project Structure

```
ensemble/
  main.go                             Entry point (daemon/TUI/attach/debug)
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
    ui/screens/debug.go               Debug/diagnostics screen
  testutil/                           Shared test helpers
```

## Key Patterns

- **Circular import avoidance**: `node` cannot import `chat`/`filetransfer`. Instead, node holds callback functions (e.g. `sendMsg`, `sendFile`) that the daemon wires up. Same pattern for any app-layer → node dependency.
- **Backend interface**: `ui.Backend` abstracts daemon access. `DirectBackend` wraps the node for in-process TUI. `GRPCBackend` wraps a gRPC client for attach mode. Both implement the same interface so TUI code is identical.
- **Event streaming**: Bubble Tea uses a channel-based pattern — `eventChanMsg` delivers the channel, `waitForEvent` reads one event at a time, each handler returns a new `waitForEvent` cmd to chain reads.
- **Tor integration**: Uses external `tor` binary via bine's `process.NewCreator(torPath)`. No CGO needed. System torrc interference avoided by passing `--defaults-torrc <empty-file>` in ExtraArgs.
- **Subsystem wiring**: Discovery, signaling, and connector are initialized in the daemon's `wireSubsystems` callback after Tor is ready. Adapter types bridge between packages (e.g. `torDialerAdapter`, `discoveryFinderAdapter`).

## Discovery & Announce

- **Auto-announce after bootstrap**: `Manager.AddNode()` calls `Announce()` after `Bootstrap()` populates the routing table, so discovered peers learn about us. This was the core bug — without it, two nodes bootstrapping from the same seed couldn't find each other.
- **Startup announce**: `daemon.wireSubsystems()` announces immediately if the routing table has peers loaded from disk.
- **Periodic re-announce**: `Manager.StartAnnounceLoop()` runs every 15 minutes, announcing to the DHT and saving the routing table to disk.
- **Routing table persistence**: `Manager.SetRTPath()` sets the save path. RT is saved after bootstrap+announce and in the periodic loop. Loaded at startup in `wireSubsystems()`.

## Observability

### Logging
- **DHT handlers**: `handleFindNode()` and `handlePutRecord()` log who asked and what was stored. `Bootstrap()`, `Announce()`, and `Lookup()` log start/success/failure with peer counts.
- **Signaling**: `HandleEnvelope()` logs incoming requests, contact-gating decisions, and validation failures.

### TUI
- **Status bar**: Shows `dht:N` alongside `peers:N` and `tor:STATE`. RTSize flows through the full chain: `Manager.RTSize()` → `Node.RTSize()` → `GetStatus` gRPC → Backend → TUI status bar.
- **Connection states per contact**: `ContactItem.Status` field shows connection state (e.g. "discovering...", "signaling...") in amber, overriding the online/offline indicator. Populated from `EventConnectionState` events.
- **Debug screen** (`d` key from home): Shows full onion address, routing table peers (address + onion + last seen), and active connections with states. Data flows via `GetDebugInfo` Backend method.

### gRPC
- **`GetStatus`**: Now includes `rt_size` field (routing table size).
- **`GetDebugInfo`**: New RPC returning routing table peers, active connections, and onion address. Proto messages: `GetDebugInfoRequest/Response`, `DHTNode`, `PeerState`.

### CLI
- **`ensemble debug [--addr|--socket]`**: Connects via gRPC, calls `GetDebugInfo`, prints routing table + connections to stdout. Useful for SSH debugging GCP seeds or local diagnostics without TUI.

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
ensemble debug --addr localhost:9090  # CLI diagnostics
```
