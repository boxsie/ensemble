# Phase 1: Foundation — T-01 to T-14

## Goal
Generate identity, display Bitcoin-style address, daemon boots with gRPC API, TUI connects.

---

## T-01: Project scaffolding
Create Go module, directory structure, Makefile skeleton.
- `go mod init`, create all `internal/` directories
- Makefile with `build`, `run`, `test`, `proto` targets (stubs for now)
- `.gitignore` for Go projects
- **Files**: `go.mod`, `Makefile`, `.gitignore`, directory tree
- **Verify**: `go build ./...` succeeds (even if empty)

## T-02: Ed25519 keypair generation
Generate, serialize, and deserialize Ed25519 keypairs.
- `Generate() → *Keypair`, `FromSeed()`, `Sign()`, `Verify()`
- `MarshalPrivate()` / `UnmarshalPrivate()` for raw bytes
- `ToLibp2pKey()` — convert to libp2p `crypto.PrivKey`
- **Files**: `internal/identity/keypair.go`, `internal/identity/keypair_test.go`
- **Verify**: Test generates key, signs message, verifies signature. Round-trip marshal/unmarshal.

## T-03: Bitcoin-style address derivation
Derive a Base58Check address from an Ed25519 public key.
- `DeriveAddress(pubKey) → Address`: SHA-256 → RIPEMD-160 → Base58Check (version byte `0x45`)
- `Validate(addr string) → bool`: checksum verification
- `MatchesPublicKey(addr, pubKey) → bool`
- `Short() → string`: truncated display form `"EaBC12...xYz9"`
- **Files**: `internal/identity/address.go`, `internal/identity/address_test.go`
- **Verify**: Test derives address, validates it, rejects tampered address. Same key always produces same address. Addresses start with "E".

## T-04: Encrypted keystore
Store keypairs encrypted on disk with a passphrase.
- Argon2id(passphrase, salt) → AES-256-GCM key → encrypt private key
- File format: `salt(16) || nonce(12) || ciphertext`
- `Save(keypair, passphrase)`, `Load(passphrase)`, `Exists()`
- **Files**: `internal/identity/keystore.go`, `internal/identity/keystore_test.go`
- **Verify**: Test saves key with passphrase, loads it back, verifies same key. Wrong passphrase fails.

## T-05: Event bus
Typed pub/sub event system for subsystem→UI communication.
- Event types: `TorBootstrap`, `TorReady`, `PeerDiscovered`, `ConnectionReq`, `ConnectionState`, `ChatMessage`, `FileOffer`, `FileProgress`, `Error`
- `NewEventBus()`, `Subscribe(type) → <-chan Event`, `Publish(event)`
- Non-blocking publish (drop if channel full)
- **Files**: `internal/node/events.go`, `internal/node/events_test.go`
- **Verify**: Test subscribes to event type, publishes event, receives it. Unsubscribed types not received.

## T-06: P2P wire protocol schema (Protobuf)
Define all peer-to-peer message types.
- `Envelope` (type + payload + timestamp + pubkey + signature)
- Signaling: `ConnRequest`, `ConnResponse`, `IPExchangeMsg`
- Chat: `ChatText`, `ChatAck`, `ChatTyping`
- File: `FileOffer`, `FileAccept`, `FileReject`, `FileChunk`, `FileChunkAck`, `FileComplete`, `FileCancel`
- DHT: `DHTFindNode`, `DHTNodeResponse`, `DHTPutRecord`, `PeerRecordProto`
- Presence: `Ping`, `Pong`
- **Files**: `internal/protocol/proto/messages.proto`
- **Verify**: `protoc` generates valid Go code. Update Makefile `proto` target.

## T-07: Protocol codec and constants
Length-prefixed protobuf read/write and protocol ID strings.
- `WriteMsg(writer, envelope)`, `ReadMsg(reader) → envelope`
- `WrapMessage(type, payload, keypair) → signed Envelope`
- `VerifyEnvelope(envelope) → bool`
- Protocol IDs: `/ensemble/signaling/1.0.0`, `/ensemble/chat/1.0.0`, `/ensemble/filetransfer/1.0.0`, `/ensemble/presence/1.0.0`
- Max message size: 16MB
- **Files**: `internal/protocol/codec.go`, `internal/protocol/ids.go`, `internal/protocol/codec_test.go`
- **Verify**: Test writes message, reads it back, verifies signature. Oversized message rejected.

## T-08: Contact store
Contact type and JSON file persistence.
- `Contact { Address, Alias, PublicKey, OnionAddr, AddedAt, LastSeen }`
- `Store.Add()`, `Store.Remove()`, `Store.Get()`, `Store.List()`, `Store.Save()`, `Store.Load()`
- Store file: `~/.ensemble/contacts.json`
- **Files**: `internal/contacts/contact.go`, `internal/contacts/store.go`, `internal/contacts/store_test.go`
- **Verify**: Test adds contact, saves to disk, loads from disk, contacts match.

## T-09: gRPC API definition
Define the public API contract for the daemon.
- `GetIdentity`, `GetStatus`, `ListContacts`, `AddContact`, `RemoveContact`
- `Connect`, `AcceptConnection`, `RejectConnection`
- `SendMessage`
- `SendFile` (server-streaming → TransferProgress), `AcceptFile`, `RejectFile`
- `Subscribe` (server-streaming → Event) — real-time event stream
- **Files**: `api/proto/ensemble.proto`
- **Verify**: `protoc` generates valid Go server/client stubs. Update Makefile.

## T-10: gRPC server implementation
Thin wrapper that translates gRPC calls → node methods.
- Implement `EnsembleServiceServer` interface
- `GetIdentity` → return address + public key from node
- `GetStatus` → return node status (Tor state, peer count, etc.)
- `Subscribe` → bridge EventBus events to gRPC stream
- Other RPCs return "not implemented" for now (wired up in later tickets)
- **Files**: `internal/api/server.go`, `internal/api/events.go`
- **Verify**: Start gRPC server, `grpcurl` calls `GetIdentity`, gets back address.

## T-11: Daemon lifecycle
Start/stop node + gRPC server, manage Unix socket.
- `Daemon.Start()`: create data dir → load/generate identity → start gRPC server on Unix socket
- `Daemon.Stop()`: graceful shutdown
- Config: data dir, passphrase (prompt or env), API socket path, optional TCP addr
- **Files**: `internal/daemon/daemon.go`, `internal/daemon/config.go`
- **Verify**: Daemon starts, socket file created at `~/.ensemble/ensemble.sock`, `grpcurl` connects.

## T-12: TUI client interface
Abstraction so TUI doesn't know if it's in-process or remote.
- `Backend` interface: `GetIdentity()`, `GetStatus()`, `Subscribe() → <-chan Event`, etc.
- `DirectBackend` — wraps `node.Node` directly (in-process, no serialization)
- `GRPCBackend` — wraps gRPC client (for `attach` mode)
- **Files**: `internal/ui/client.go`
- **Verify**: Both backends implement the same interface. Unit test with mock node.

## T-13: TUI skeleton (splash + home + settings)
Bubble Tea app with basic screen routing.
- `App` root model with screen enum, Update/View dispatch
- Splash screen: placeholder "Starting..." (real Tor progress in Phase 2)
- Home screen: contact list (empty initially), key bindings shown
- Settings screen: show own address, public key
- Status bar component: node status
- Styles: color palette, borders
- Key bindings: `q` quit, `esc` back, `↑/↓/j/k` navigate, `a` add contact, `s` settings
- **Files**: `internal/ui/app.go`, `internal/ui/styles.go`, `internal/ui/keys.go`, `internal/ui/screens/splash.go`, `internal/ui/screens/home.go`, `internal/ui/screens/settings.go`, `internal/ui/components/statusbar.go`
- **Verify**: TUI launches, shows splash → home screen, settings shows address, `q` quits.

## T-14: main.go — entry point and mode selection
Wire everything together with flag parsing.
- `--headless` flag → daemon mode (no TUI)
- `--data-dir` flag → override data directory
- `--api-addr` flag → TCP listen address for gRPC (headless mode)
- `attach` subcommand → TUI-only mode, connect to existing daemon socket
- Default (no flags) → start daemon + TUI in one process
- **Files**: `main.go`
- **Verify**: `ensemble` → TUI with address. `ensemble --headless` → daemon logs. `ensemble attach` → TUI connects to running daemon.
