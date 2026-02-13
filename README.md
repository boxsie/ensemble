# Ensemble

Fully decentralized peer-to-peer messaging and file transfer. No central server. No accounts. Just a keypair.

Discovery and signaling happen over Tor, hiding your IP from the network. Once two peers mutually agree to connect, they exchange real IPs and switch to a direct QUIC connection for speed. Your IP is only ever revealed to people you explicitly accept.

## Features

- **Private by default** -- all discovery traffic routed through Tor hidden services
- **Bitcoin-style identity** -- Ed25519 keypair generates an `E...` address; no registration, no server
- **Direct connections** -- after mutual consent, peers connect over QUIC/TLS 1.3 for low-latency chat and file transfer
- **NAT traversal** -- automatic cascade: UPnP, UDP hole punch, circuit relay, Tor fallback
- **Quantum-resistant design** -- public keys never broadcast to the DHT; only revealed to verified contacts
- **Single binary** -- daemon, TUI, and attach mode all in one executable
- **Terminal UI** -- full-featured Bubble Tea interface with contacts, chat, file transfer, and transfer progress

## Quick Start

### Prerequisites

- [Go](https://go.dev/) 1.24+
- [Tor](https://www.torproject.org/) binary installed and in `$PATH` (or specify with `--tor-path`)
- [protoc](https://grpc.io/docs/protoc-installation/) + Go plugins (only if modifying `.proto` files)

### Build

```
make build
```

### Run

```
# Daemon + interactive TUI
./bin/ensemble

# Headless daemon (no TUI)
./bin/ensemble --headless

# Attach TUI to a running daemon
./bin/ensemble attach
```

### Options

```
ensemble [flags]
  --headless         Run daemon without TUI
  --data-dir PATH    Override data directory (default: ~/.ensemble)
  --api-addr ADDR    TCP listen address for gRPC API (headless mode)
  --tor-path PATH    Path to tor binary

ensemble attach [flags]
  --socket PATH      Daemon Unix socket path
  --addr HOST:PORT   Daemon TCP address
```

## TUI Keybindings

| Key       | Screen | Action              |
|-----------|--------|---------------------|
| `a`       | Home   | Add contact         |
| `n`       | Home   | Add bootstrap node  |
| `s`       | Home   | Settings            |
| `f`       | Home   | Send file           |
| `t`       | Home   | View transfers      |
| `Enter`   | Home   | Open chat           |
| `Esc`     | Any    | Back / cancel       |
| `q`       | Most   | Quit                |

## How It Works

```
1. DISCOVER  -- Query DHT over Tor to find peer's .onion address
2. SIGNAL    -- Dial .onion, send connection request (addresses only)
3. GATE      -- Responder checks contacts list; unknown peers prompt the user
4. VERIFY    -- 3-step handshake: mutual identity proof via Ed25519 signatures
5. IP SWAP   -- Encrypt real IPs to each other's public keys, exchange over Tor
6. NAT       -- UPnP (2s) -> hole punch (10s) -> relay (15s) -> Tor fallback
7. CONNECT   -- QUIC handshake with Noise authentication
8. DATA      -- Chat and file transfer at full speed
```

## Architecture

```
Clients:  TUI (in-process)  |  TUI (attach)  |  Mobile (future)
          Direct Go calls      gRPC socket       gRPC
------------------------------------------------------------------
gRPC API Layer  (api/proto/ensemble.proto)
------------------------------------------------------------------
Node (Daemon Core) -- orchestrates all subsystems
------------------------------------------------------------------
App Layer:     Chat, File Transfer, Contacts
Transport:     Direct QUIC/TLS 1.3 (after consent)
Signaling:     Tor hidden services (anonymous)
Discovery:     App-level Kademlia DHT over Tor + mDNS
```

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

## Development

```bash
# Run unit tests
make test

# Run tests with race detector
make test-race

# Run integration tests (requires Tor network access)
make test-integration

# Regenerate protobuf code
make proto

# Format code
make fmt

# Build for all platforms
make build-all
```

## Security Model

- **Hash-only DHT**: Peer records contain only an address hash and onion address. No public keys are ever broadcast to the network.
- **Contact-gated signaling**: Public keys are only revealed to known contacts or user-approved peers. Unknown addresses trigger a TUI prompt before any key material is exposed.
- **3-step handshake**: Addresses exchanged first, then responder proves identity, then initiator proves identity. No key material until both sides are verified.
- **Post-quantum ready**: The `Keypair` interface is designed for Ed25519 to be swapped for post-quantum signatures (e.g. CRYSTALS-Dilithium) without changing the discovery, signaling, or transport layers.

## Tech Stack

- **Go** -- single-binary, cross-platform
- **libp2p** -- QUIC, yamux, TLS 1.3, Noise, AutoNAT, DCUtR
- **Tor** -- via [bine](https://github.com/cretz/bine) with external tor binary
- **Ed25519 + Base58Check** -- Bitcoin-style cryptographic identity
- **Protobuf + gRPC** -- API and wire protocol
- **Bubble Tea** -- terminal UI framework

## License

MIT
