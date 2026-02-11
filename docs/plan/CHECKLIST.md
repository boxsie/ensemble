# Ensemble — Ticket Checklist

Track progress here. On completion, each ticket gets a summary and test list.

---

## Phase 0: Project Setup

### [x] T-00: Create ticket files + CLAUDE.md
**Completed**: 2026-02-07
**Summary**: Created `CLAUDE.md` at project root with architecture overview, ticket index, coding conventions, and "how to pick up work" guide. Created `docs/plan/` with 8 plan files: `00-overview.md` (architecture, design decisions, dependencies) and `01-foundation.md` through `07-hardening.md` (per-phase ticket details).
**Tests**: None (documentation only).

---

## Phase 1: Foundation

### [x] T-01: Project scaffolding
**Completed**: 2026-02-07
**Summary**: Initialized Go module (`github.com/ensemble/ensemble`, Go 1.25.7). Created full directory tree: `api/proto/`, `internal/{daemon,api,identity,tor,discovery,signaling,transport,protocol/proto,chat,filetransfer,contacts,node,ui/{screens,components}}`, `testutil/`, `deploy/`. Created Makefile with `build`, `run`, `run-headless`, `test`, `test-integration`, `test-race`, `lint`, `proto`, and cross-compile targets. Created `.gitignore`. Minimal `main.go` placeholder. Renamed project from "ensemble_lite" to "ensemble" throughout.
**Tests**: None (scaffolding only). `go build ./...` and `go vet ./...` pass.
### [x] T-02: Ed25519 keypair generation
**Completed**: 2026-02-07
**Summary**: Ed25519 keypair generation with `Generate()`, deterministic `FromSeed()`, `Sign()`/`Verify()`, `MarshalPrivate()`/`UnmarshalPrivate()` for raw byte serialization, `Seed()` for seed extraction, and `ToLibp2pKey()` for libp2p interop. Added `github.com/libp2p/go-libp2p/core` dependency.
**Tests**: 11 tests — generation, uniqueness, deterministic seed, invalid seed rejection, sign/verify, tampered message rejection, wrong key rejection, marshal round-trip, invalid unmarshal rejection, seed round-trip, libp2p key conversion.
### [x] T-03: Bitcoin-style address derivation
**Completed**: 2026-02-07
**Summary**: Base58Check address derivation from Ed25519 public keys. Pipeline: SHA-256 → RIPEMD-160 → Base58Check. Version byte corrected from `0x45` to `0x21` to produce "E"-prefixed addresses. `DeriveAddress()`, `Validate()`, `MatchesPublicKey()`, `Short()` for truncated display. Added `mr-tron/base58` and `golang.org/x/crypto` dependencies.
**Tests**: 11 tests — E prefix, determinism, uniqueness, validation (valid/tampered/empty/garbage), public key matching (correct/wrong), Short() format, String().
### [x] T-04: Encrypted keystore
**Completed**: 2026-02-07
**Summary**: Encrypted keypair persistence using Argon2id key derivation + AES-256-GCM encryption. File format: `salt(16) || nonce(12) || ciphertext`. `NewKeystore()`, `Save()`, `Load()`, `Exists()`. Key files written with 0600 permissions, directories auto-created.
**Tests**: 7 tests — save/load round-trip, wrong passphrase rejection, exists check, missing file error, file permissions (0600), directory auto-creation, overwrite with new passphrase.
### [x] T-05: Event bus
**Completed**: 2026-02-07
**Summary**: Typed pub/sub event bus using Go channels. 9 event types (TorBootstrap, TorReady, PeerDiscovered, ConnectionReq, ConnectionState, ChatMessage, FileOffer, FileProgress, Error). `NewEventBus()`, `Subscribe()` returns receive-only channel, `Publish()` is non-blocking (drops on full buffer). Thread-safe via RWMutex.
**Tests**: 7 tests — subscribe/publish, unsubscribed type filtering, multiple subscribers, multiple event types, non-blocking overflow, concurrent publish safety, no-subscriber publish.
### [x] T-06: P2P wire protocol schema (Protobuf)
**Completed**: 2026-02-07
**Summary**: Full P2P wire protocol in protobuf. `Envelope` (type, payload, timestamp, pubkey, signature) with `MessageType` enum. Signaling: `ConnRequest`, `ConnResponse`, `IPExchangeMsg`. Chat: `ChatText`, `ChatAck`, `ChatTyping`. File: `FileOffer`, `FileAccept`, `FileReject`, `FileChunk`, `FileChunkAck`, `FileComplete`, `FileCancel`. DHT: `DHTFindNode`, `DHTNodeResponse`, `DHTPutRecord`, `PeerRecord`. Presence: `Ping`, `Pong`. Installed protoc + protoc-gen-go. Updated Makefile proto target.
**Tests**: None (schema only). `protoc` generates valid Go code, `go build ./...` passes.
### [x] T-07: Protocol codec and constants
**Completed**: 2026-02-07
**Summary**: Length-prefixed (4-byte big-endian) protobuf read/write with `WriteMsg()`/`ReadMsg()`. `WrapMessage()` creates signed envelopes (type+payload+timestamp signed with Ed25519). `VerifyEnvelope()` checks signatures. Protocol ID constants for signaling, chat, file transfer, presence, DHT. Max message size 16MB. Added `FromPublicKey()` to identity package for verify-only keypairs.
**Tests**: 8 tests — write/read round-trip, sign/verify, tampered payload/timestamp rejection, wrong key rejection, oversized read/write rejection, multi-message round-trip.
### [x] T-08: Contact store
**Completed**: 2026-02-07
**Summary**: `Contact` struct with JSON tags. `Store` with thread-safe `Add()`, `Remove()`, `Get()`, `List()`. JSON file persistence via `Save()`/`Load()`. Returns copies from `Get()`/`List()` to avoid data races. Missing file on `Load()` is not an error.
**Tests**: 9 tests — add/get, not found, update existing, remove, remove not found, list, save/load round-trip, load missing file, directory auto-creation.
### [x] T-09: gRPC API definition
**Completed**: 2026-02-07
**Summary**: Full gRPC API definition. `EnsembleService` with 12 RPCs: `GetIdentity`, `GetStatus`, `ListContacts`, `AddContact`, `RemoveContact`, `Connect`, `AcceptConnection`, `RejectConnection`, `SendMessage`, `SendFile` (server-streaming), `AcceptFile`, `RejectFile`, `Subscribe` (server-streaming events). Generated Go server/client stubs. Installed `protoc-gen-go-grpc`. Updated Makefile proto target for both API and P2P protos.
**Tests**: None (schema only). `protoc` generates valid Go code, `go build ./...` passes.
### [x] T-10: gRPC server implementation
**Completed**: 2026-02-07
**Summary**: `Server` struct implements `EnsembleServiceServer` via `NodeProvider` interface. `GetIdentity` and `GetStatus` fully wired. `Subscribe` bridges EventBus to gRPC stream via merged channel fan-in. Remaining 9 RPCs return "not implemented" (wired in later tickets). Added `FromPublicKey()` to identity. Installed `protoc-gen-go-grpc`, added `google.golang.org/grpc` dependency.
**Tests**: None (server tested via integration in T-11). `go build ./...` and `go vet ./...` pass.
### [x] T-11: Daemon lifecycle
**Completed**: 2026-02-07
**Summary**: `Daemon` with `Start()`/`Stop()`. On start: creates data dir, loads or generates identity (persisted via keystore), creates `Node`, starts gRPC server on Unix socket (or TCP via config). On stop: graceful gRPC shutdown, socket file cleanup. `Config` with defaults (`~/.ensemble/`). Also created `node.Node` struct implementing `NodeProvider` interface.
**Tests**: 4 tests — start/stop (socket + key file created), GetIdentity via gRPC (full end-to-end), identity persistence across restarts, socket cleanup on stop.
### [x] T-12: TUI client interface
**Completed**: 2026-02-07
**Summary**: `Backend` interface abstracting daemon access for the TUI (in-process or remote). `DirectBackend` wraps `node.Node` directly (no serialization). `GRPCBackend` wraps gRPC client for `attach` mode. Domain types: `IdentityInfo`, `StatusInfo`, `FileProgress`. Added `SubscribeAll(ctx)` to `EventBus` to consolidate fan-in pattern, used by both `DirectBackend` and the gRPC event bridge. Split across `backend.go`, `direct.go`, `grpc.go`.
**Tests**: 7 tests — DirectBackend identity/status/subscribe/close, GRPCBackend identity/status/subscribe (end-to-end via daemon).
### [x] T-13: TUI skeleton (splash + home + settings)
**Completed**: 2026-02-07
**Summary**: Bubble Tea TUI with `App` root model dispatching to three screens. Splash screen (centered "Starting..." placeholder). Home screen (contact list with j/k navigation, key bindings help). Settings screen (address + public key display). `StatusBar` component (address, tor state, peer count). `KeyMap` with q/esc/↑↓jk/a/s bindings. `styles.go` with color palette. Async identity/status loading from Backend transitions splash → home.
**Files**: `internal/ui/app.go`, `internal/ui/styles.go`, `internal/ui/keys.go`, `internal/ui/screens/splash.go`, `internal/ui/screens/home.go`, `internal/ui/screens/settings.go`, `internal/ui/components/statusbar.go`
**Tests**: None (TUI skeleton; verified via `go build ./...` and `go vet ./...`). All 53 existing tests pass.
### [x] T-14: main.go — entry point and mode selection
**Completed**: 2026-02-07
**Summary**: Entry point with three modes. Default: starts daemon + in-process TUI via `DirectBackend`. `--headless`: daemon only, blocks on SIGINT/SIGTERM. `attach`: TUI-only via `GRPCBackend` to existing daemon socket. Flags: `--data-dir` (override data directory), `--api-addr` (TCP listen for headless), `--socket`/`--addr` (attach target).
**Files**: `main.go`
**Tests**: None (entry point). Verified via `go build ./...` and `go vet ./...`. All 53 existing tests pass.

---

## Phase 2: Tor

### [x] T-15: Tor engine (external binary)
**Completed**: 2026-02-07
**Summary**: Tor engine using external `tor` binary via bine's `process.NewCreator()`. All go-libtor forks (ipsn, berty, gen2brain, ooni, alexballas) have `arc4random_buf` conflicts with glibc 2.36+, so we use the system tor binary instead. `NewEngine(dataDir, exePath)` with `exec.LookPath` fallback. `Start(ctx)` launches Tor with `EnableNetwork: false`, spawns `bootstrap()` goroutine. Monitors `STATUS_CLIENT` events, enables network via `SetConf("DisableNetwork", "0")`, sends progress to buffered channel, closes `ready` on 100%. `SOCKSAddr()` queries via `GetInfo("net/listeners/socks")`. Idempotent `Stop()`. Uses `--defaults-torrc /dev/null` to avoid system torrc interference.
**Files**: `internal/tor/engine.go`
**Tests**: Integration test (`//go:build integration`) in `internal/tor/engine_test.go` — bootstrap progress, ready channel, SOCKS address, double-stop safety. Requires `tor` on PATH and network access.
### [x] T-16: Tor hidden service
**Completed**: 2026-02-07
**Summary**: `Engine.CreateOnionService(ctx, port, keyPath)` creates v3 hidden services with Ed25519 key persistence. Loads existing key from PEM/PKCS8 file, or saves newly generated key for stable `.onion` address across restarts. `OnionService` wrapper with `OnionID()`, `OnionAddr()`, `Listener()`, `Close()`. Helper functions `loadOnionKey()` / `saveOnionKey()`.
**Files**: `internal/tor/onion.go`
**Tests**: Integration test in `internal/tor/engine_test.go` — `TestOnionServicePersistence` verifies key persistence produces same `.onion` address after restart.
### [x] T-17: Tor SOCKS dialer
**Completed**: 2026-02-07
**Summary**: `Engine.Dialer(ctx)` creates SOCKS5 dialer wrapping bine's `tor.Dialer()`. `Dialer.DialOnion(ctx, onionAddr, port)` dials `.onion:port` through Tor SOCKS proxy with context support.
**Files**: `internal/tor/dialer.go`
**Tests**: Integration test (shares `engine_test.go`). Requires Tor network access.
### [x] T-18: Wire Tor into daemon + TUI
**Completed**: 2026-02-07
**Summary**: Full Tor integration across daemon, node, API, and TUI. Node gains `TorState()`/`SetTorState()`, `OnionAddr()`/`SetOnionAddr()` with RWMutex. `NodeProvider` interface extended. `GetStatus` RPC returns actual tor state and onion address. Daemon `startTor()` creates engine, bridges `ProgressChan()` → EventBus (`EventTorBootstrap`), creates onion service on ready (`EventTorReady`). `DisableTor` config flag for tests. TUI splash shows progress bar during bootstrap. `App` subscribes to backend events via `eventChanMsg`/`waitForEvent` chaining pattern. Splash→home transition requires both identity AND tor ready. StatusBar shows onion address.
**Files**: `internal/node/node.go`, `internal/api/server.go`, `internal/daemon/daemon.go`, `internal/daemon/config.go`, `internal/ui/app.go`, `internal/ui/direct.go`, `internal/ui/screens/splash.go`, `internal/ui/components/statusbar.go`, `internal/ui/client_test.go`, `internal/daemon/daemon_test.go`
**Tests**: All 64 existing unit tests pass. Daemon/UI tests use `DisableTor: true`.

---

## Phase 3: Discovery & Signaling

> **Design change**: Hash-only DHT (no public keys broadcast), contact-gated 3-step handshake,
> quantum-resistant key exposure model. See [Phase 3 plan](docs/plan/03-discovery-signaling.md).

### [x] T-19: Kademlia routing table
**Completed**: 2026-02-09
**Summary**: Kademlia routing table with 160 K-buckets (K=20), XOR distance metric on 20-byte RIPEMD-160 address hashes. `NodeIDFromPublicKey()` and `NodeIDFromAddress()` for ID derivation. `AddPeer()` with update-and-move-to-tail for existing peers, `RemovePeer()` by address, `FindClosest()` returns K nearest by XOR distance. Anti-Sybil: per-source insertion cap + sliding window rate limiter. `Save()`/`Load()` for JSON persistence — on shutdown saves all peers with timestamps, on startup loads most-recently-seen first for bootstrap.
**Tests**: 19 tests — NodeID determinism, address round-trip, invalid address, XOR symmetry, XOR identity, common prefix length, add/reject-self/update/bucket-full, remove, find-closest sorting, find-closest correctness, per-source cap, window rate limit, save/load round-trip, missing file, most-recent-first ordering, concurrent safety.
### [x] T-20: DHT protocol (announce + lookup)
**Completed**: 2026-02-09
**Summary**: DHT protocol with hash-only records (no public keys). `DHTDiscovery` with `Announce()` (push self to K closest nodes), `Lookup()` (iterative closest-node search), `HandleConn()` (incoming DHT queries). `Dialer` interface for testable Tor dialing. Wire protocol uses unsigned envelopes (no key material in transit). Record expiry at 24h. Conversion helpers between `PeerInfo` and protobuf `PeerRecord`.
**Tests**: 8 tests — announce and lookup between two nodes, lookup via intermediary (3-node chain), expired record rejection, not-found error, empty routing table announce error, put-record storage verification, nil/short record rejection.
### [x] T-21: mDNS LAN discovery
**Completed**: 2026-02-09
**Summary**: mDNS LAN discovery using zeroconf. `MDNSDiscovery` registers service `_ensemble._tcp.local.` with TXT records (addr + onion), browses for peers in background loop. `OnPeerFound(callback)` for discovered peers. `ParseMDNSEntry()` exported for testable TXT record parsing. Self-discovery filtered. Continuous browsing with configurable interval.
**Tests**: 6 tests — valid entry parsing, self-skip, missing addr/onion, invalid address, empty TXT. Full mDNS network test requires LAN (integration).
### [x] T-22: Discovery manager
**Completed**: 2026-02-09
**Summary**: Unified discovery coordinator. `Manager` with `FindPeer()` — tries mDNS first (3s timeout) then falls back to DHT. `PeerChan()` provides deduplicated channel of all discovered peers. `Start()`/`Stop()` lifecycle. `emitIfNew()` deduplication by address.
**Tests**: 5 tests — DHT fallback lookup, not-found error, deduplication (same peer emitted once), multiple distinct peers, no-backends error.
### [x] T-23: Signaling handshake protocol
**Completed**: 2026-02-09
**Summary**: 3-step quantum-resistant handshake. Step 1: `CreateConnRequest` sends addresses (hashes only) + nonce. Step 2: `CreateConnResponse` — responder proves identity with pub_key + sign(nonce), or rejects without revealing key. Step 3: `CreateConnConfirm` — initiator proves identity. `ValidateConnRequest/Response/Confirm` verify signatures, address derivation (pub_key hashes to claimed address), timestamp freshness (5min), nonce length. `NonceCache` for replay protection with TTL-based expiry.
**Tests**: 9 tests — full 3-step handshake, replay rejection, stale timestamp, rejection without key reveal, imposter key rejection, tampered signature, wrong address on confirm, nonce cache expiry, invalid nonce length.
### [x] T-24: Signaling server (inbound)
**Completed**: 2026-02-09
**Summary**: Signaling server listens for inbound connections. Contact-gated: known contacts auto-proceed, unknown addresses trigger `OnConnectionRequest` callback (→ TUI prompt). `ConnectionRequest` has `Decision` channel for async accept/reject. Rejections send `ConnResponse(accepted=false)` with no public key. Accepted connections complete the full 3-step handshake. `Serve(ctx, listener)` / `Stop()` lifecycle.
**Tests**: 6 tests (shared with T-25) — known contact auto-accept, unknown accepted via callback, unknown rejected, no handler = reject, no key reveal on rejection, retry on transient error.
### [x] T-25: Signaling client (outbound)
**Completed**: 2026-02-09
**Summary**: Signaling client performs outbound 3-step handshake with connection deadlines and retry logic. `RequestConnection` retries up to 3 times with exponential backoff on transient errors (timeouts, connection failures). Explicit rejections (`ErrRejected`) are not retried. Each attempt uses `HandshakeTimeout` (30s) deadline set on the connection. `attemptHandshake` handles the full Step 1→2→3 flow with deadline-aware reads/writes.
**Tests**: Shared with T-24 above.
### [x] T-26: Wire discovery + signaling into node
**Completed**: 2026-02-09
**Summary**: Integrated discovery and signaling subsystems into the core node, API server, and TUI backend. Node gains `SetContacts()`, `SetDiscovery()`, `SetSignaling()`, `Connect()` (discovery lookup → signaling handshake), `AcceptConnection()`/`RejectConnection()` (pending request channels), `PeerCount()`, `handleConnectionRequest()` (bridges signaling server → event bus). `NodeProvider` and `directNode` interfaces expanded with all new methods. API server wires `ListContacts`, `AddContact`, `RemoveContact`, `Connect`, `AcceptConnection`, `RejectConnection` RPCs. Daemon initializes contacts store on startup (`Load()` from disk). `mockNode` in test updated with stub implementations.
**Tests**: All 124 tests pass (no new tests — this is a wiring ticket).
### [x] T-27: TUI add contact + connection prompt
**Completed**: 2026-02-09
**Summary**: TUI screens for adding contacts and handling connection requests. `AddContact` screen with address and alias text inputs (tab to switch fields, enter to save, esc to cancel, address validation). `ConnPrompt` screen shown when `EventConnectionReq` arrives — displays peer address with warning that accepting reveals public key, y/n to accept/reject. App loads contacts from backend on transition to home screen and after saves. Connection prompt interrupts any screen and returns to previous on decision. Keyboard handling adjusted: q/esc suppressed during text input and prompt screens.
**Files**: `internal/ui/screens/addcontact.go`, `internal/ui/screens/connprompt.go`, `internal/ui/app.go`
**Tests**: All 124 tests pass (TUI screens verified via `go build`; no new unit tests — screens are visual).

---

## Phase 4: Direct Connection

### [x] T-28: Encrypted IP exchange
**Completed**: 2026-02-09
**Summary**: Ed25519→X25519 key conversion (birational map for public keys, SHA-512+clamp for private keys) added to identity package. ECDH shared secret via `curve25519.X25519()`, HKDF-SHA256 key derivation with sorted-pubkey salt, AES-256-GCM encryption. `EncryptIPInfo()`/`DecryptIPInfo()` for standalone use, `ExchangeIPs()` for bidirectional exchange over existing signaling connection using concurrent read/write. Unsigned envelope transport (no key material in envelope).
**Files**: `internal/identity/x25519.go`, `internal/signaling/ipexchange.go`
**Tests**: 13 tests — X25519 key length, conversion consistency (birational map matches ECDH derivation), deterministic from seed, invalid key rejection, encrypt/decrypt round-trip, wrong key fails, shared key symmetry, ciphertext tampering, ciphertext too short, empty addrs, bidirectional exchange (net.Pipe), multiple addrs exchange.
### [x] T-29: libp2p host setup
**Completed**: 2026-02-09
**Summary**: libp2p host wrapper using `go-libp2p v0.47.0`. QUIC-v1 transport (UDP, TLS 1.3 built-in), Noise security, yamux muxer (all libp2p defaults). Configurable options: `NATPortMap()` (UPnP/NAT-PMP), `EnableHolePunching()` (DCUtR), `EnableAutoNATv2()`, `EnableNATService()`. `Host` wrapper exposes `ID()`, `Addrs()`, `AddrInfo()`, `Connect()`, `Inner()`, `Close()`. Migrated from separate `go-libp2p/core` module to monorepo `go-libp2p v0.47.0`.
**Files**: `internal/transport/host.go`
**Tests**: 4 tests — host starts and listens on QUIC, two hosts connect on same machine, two hosts exchange data over a stream, host close cleans up.
### [x] T-30: NAT traversal cascade
**Completed**: 2026-02-09
**Summary**: Priority-ordered NAT traversal cascade: (1) Direct QUIC via UPnP/NAT-PMP with 2s timeout, (2) DCUtR hole punching with 10s timeout (libp2p auto-upgrades relay connections), (3) Circuit Relay v2 with 15s timeout, (4) Tor-only fallback. `NATTraverse()` returns `NATResult{Strategy, Addrs, Latency}`. Helper functions `filterNonRelay()`/`filterRelay()` for multiaddr classification, `hasDirectConn()` for upgrade detection, `connAddrs()` for connection info. Configurable timeouts via `NATConfig`.
**Files**: `internal/transport/nat.go`
**Tests**: 5 tests — direct connection success, fallback to tor-only on unreachable peer, strategy String(), filter non-relay addrs, filter relay addrs.
### [x] T-31: Connection orchestrator
**Completed**: 2026-02-09
**Summary**: Full connection lifecycle orchestrator. `Connector.Connect(ctx, peerAddr)` runs the state machine: Discovering → Signaling → ExchangingIPs → NATTraversal → Connected/ConnectedTor. Performs initiator-side 3-step handshake directly on the Tor connection, followed by encrypted IP exchange, then closes Tor and attempts direct QUIC via NAT cascade. `Connector.Disconnect(peerAddr)` closes libp2p connections. `GetPeer()`/`ActivePeers()` for connection state queries. `StateEvent` callback for real-time state change notifications. `PeerConnection` tracks address, state, strategy, latency, peer ID. Stream helpers: `OpenStream()`, `SetStreamHandler()`, `SendBytes()`, `ReadAll()`.
**Files**: `internal/transport/connector.go`, `internal/transport/streams.go`
**Tests**: 5 tests — full flow (discovery→signaling→IP exchange→NAT→connected with state tracking), discovery failure (→failed state), disconnect removes peer, ConnState.String(), GetPeer not found.
### [x] T-32: Wire transport into node
**Completed**: 2026-02-09
**Summary**: Integrated libp2p host and Connector into the daemon, node, API, and TUI layers. Node gains `SetHost()`, `SetConnector()`, `ConnectionInfo()`, `ActiveConnections()`. `Connect()` delegates to Connector when available (full flow: discovery→handshake→IP exchange→NAT→QUIC) with signaling-only fallback. `PeerCount()` now reflects active connections. Daemon creates libp2p host on startup (configurable via `DisableP2P`, `P2PPort`) and shuts it down on stop. `NodeProvider` and `directNode` interfaces extended. All existing tests updated with `DisableP2P: true`.
**Files**: Updated `internal/node/node.go`, `internal/api/server.go`, `internal/daemon/daemon.go`, `internal/daemon/config.go`, `internal/ui/direct.go`
**Tests**: All 158 tests pass (no new tests — wiring ticket).

---

## Phase 5: Chat

### [x] T-33: Chat service
**Completed**: 2026-02-09
**Summary**: Chat service using libp2p streams over `/ensemble/chat/1.0.0` protocol. `SendMessage(ctx, peerAddr, text)` opens stream, writes signed `ChatText` envelope, waits for signed `ChatAck` with matching message ID. Incoming handler reads `ChatText`, verifies envelope signature, derives sender address from public key, emits `EventChatMessage`, sends `ChatAck`. Message struct with ID, From, To, Text, SentAt, AckedAt, Direction. `PeerResolver` interface for address→peer ID mapping. 16-byte random hex message IDs.
**Files**: `internal/chat/service.go`
**Tests**: 6 tests — send and receive with ACK verification, bidirectional exchange, multiple sequential messages, unconnected peer error, unique message IDs, invalid peer ID error.
### [x] T-34: Chat history
**Completed**: 2026-02-09
**Summary**: In-memory per-conversation message store. `History.Append(peerAddr, msg)` with FIFO eviction at 1000 messages per peer. `GetConversation(peer)` returns full chronological copy. `GetLatest(peer, n)` returns last n messages. `Clear(peer)` removes all messages. Thread-safe via RWMutex.
**Files**: `internal/chat/history.go`
**Tests**: 10 tests — append/get, unknown peer nil, copy isolation, get latest, get latest exceeding count, unknown peer latest, clear, eviction at 1000, separate conversations, concurrent access safety.
### [x] T-35: Wire chat into node + gRPC
**Completed**: 2026-02-09
**Summary**: Chat service wired into node, API server, daemon, and TUI backend. Node gains `SetSendMessage()` / `SendMessage()` using callback function pattern to avoid import cycle (chat→node→chat). `NodeProvider` and `directNode` interfaces extended with `SendMessage(ctx, addr, text) (string, error)`. API `SendMessage` RPC returns message ID. Daemon creates `chat.Service` with `History` when p2p host is available, using `nodeResolver` adapter for peer lookup. Chat service auto-records sent and received messages in History.
**Files**: Updated `internal/node/node.go`, `internal/api/server.go`, `internal/daemon/daemon.go`, `internal/ui/direct.go`
**Tests**: All 174 tests pass (wiring ticket).
### [x] T-36: TUI chat screen
**Completed**: 2026-02-09
**Summary**: Conversation view with `viewport` (scrollable message history) and `textarea` (multi-line input, Enter to send). `MessageList` component formats messages with timestamps, sender (You/Them), and ACK status (✓/…). Header shows peer name/address and connection method. Enter on home screen contact list opens chat. Esc returns to home. `ChatSendMsg` flows from screen → App → backend. Incoming `EventChatMessage` updates active chat viewport. Optimistic send: message shown immediately, marked acked on `chatSentMsg` response.
**Files**: `internal/ui/screens/chat.go`, `internal/ui/components/messagelist.go`, updated `internal/ui/app.go`, `internal/ui/screens/home.go`
**Tests**: All 174 tests pass (TUI screens verified via `go build`).
### [x] T-37: TUI contact list online status
**Completed**: 2026-02-09
**Summary**: Presence service using `/ensemble/presence/1.0.0` protocol. `PingPeer(ctx, peerID)` sends signed `Ping{timestamp}`, waits for `Pong{echo_timestamp}` with 5s timeout. `Start(ctx, lister, onChange)` pings all connected peers every 30s, calls `onChange(OnlineStatus)` when status changes. `EventPeerOnline` event type added. Daemon starts presence pinger in background goroutine. TUI handles `EventPeerOnline` to update `home.Contacts[].Online`. Contact list shows green "online" / gray "offline" per contact.
**Files**: `internal/transport/presence.go`, updated `internal/node/events.go`, `internal/api/events.go`, `internal/daemon/daemon.go`, `internal/ui/app.go`
**Tests**: 4 tests — ping/pong round-trip with RTT, unconnected peer error, online callback with status change, IsOnline defaults to false. 178 total tests pass.

---

## Phase 6: File Transfer

### [x] T-38: File chunker
**Completed**: 2026-02-10
**Summary**: Fixed-size file chunker with SHA-256 hashes. Default 256KB chunks. `NewChunker(path)`, `ReadChunk(index)`, `TotalChunks()`, `FileSize()`, `AllChunkHashes()`. Handles partial last chunk, empty files, directories. Custom chunk size via `NewChunkerWithSize()`.
**Files**: `internal/filetransfer/chunker.go`
**Tests**: 12 tests — basic chunking, partial last chunk, hash determinism, hash correctness, out-of-range, empty file, directory, missing file, invalid chunk size, all chunk hashes, exact multiple, small chunk size.
### [x] T-39: Merkle tree
**Completed**: 2026-02-10
**Summary**: Binary Merkle tree using flat heap-indexed array. `BuildMerkleTree(chunkHashes)`, `Root()`, `Proof(chunkIndex)`, `VerifyProof()`. Odd leaf counts padded by duplicating last leaf to next power of 2. Single leaf gets duplicated (minimum tree width 2). SHA-256 pair hashing.
**Files**: `internal/filetransfer/merkle.go`
**Tests**: 12 tests — root determinism, different data, single leaf, proof for all leaves (2-16), tampered chunk/proof/index, out-of-range, empty input, nil inputs, odd leaf duplication, integration with chunker.
### [x] T-40: File transfer sender
**Completed**: 2026-02-10
**Summary**: Sender opens libp2p stream on `/ensemble/filetransfer/1.0.0`, sends signed `FileOffer` (transfer_id, filename, size, Merkle root, chunk_size, chunk_count), waits for accept/reject. On accept, sends chunks with sliding window (8 in-flight), each with SHA-256 hash + Merkle proof. Retransmits on invalid ACK (max 3 retries). Context cancellation interrupts blocking reads via goroutine. After `FileComplete`, closes write side and waits for receiver to finish (read until EOF).
**Files**: `internal/filetransfer/sender.go`
**Tests**: Shared with T-41.
### [x] T-41: File transfer receiver
**Completed**: 2026-02-10
**Summary**: Receiver registers stream handler, reads `FileOffer`, publishes `EventFileOffer` to event bus with `Decision` channel. Accept/reject via callback (2min timeout). Per-chunk: verify SHA-256 hash + Merkle proof against root, send `ChunkAck(valid=true/false)`. On `FileComplete`, reassembles file in order, creates output directory if needed. Added `valid` field to `FileChunkAck` proto and `proof` field to `FileChunk` proto.
**Files**: `internal/filetransfer/receiver.go`, updated `internal/protocol/proto/messages.proto`
**Tests**: 6 tests (shared sender/receiver) — end-to-end send/receive with SHA-256 verification, rejection, peer not connected, progress callback, cancel mid-transfer, file offer event.
### [x] T-42: Transfer progress tracking
**Completed**: 2026-02-10
**Summary**: Rolling-window speed calculation with 20-sample window. `NewProgress(totalBytes)`, `Update(bytes)`, `Percent()`, `BytesPerSecond()`, `ETA()`, `Complete()`, `String()` → "45% (12.3 MB/s, ~2m left)". Auto-formats speed (B/s, KB/s, MB/s, GB/s) and duration (seconds, minutes, hours).
**Files**: `internal/filetransfer/progress.go`
**Tests**: 12 tests — percent, complete, sent bytes, speed calculation, ETA, ETA complete, string format, string complete, zero total, format speed, format duration, window trimming.
### [x] T-43: Wire file transfer into node + gRPC
**Completed**: 2026-02-10
**Summary**: Node gains `SetSendFile()`, `SendFile()`, `SetAcceptFile()`, `AcceptFile()`, `SetRejectFile()`, `RejectFile()` using callback pattern (same as chat). `NodeProvider` and `directNode` interfaces extended. API server implements `SendFile` (streaming), `AcceptFile`, `RejectFile` RPCs. Daemon creates `Sender` + `Receiver` when p2p host available, wires callbacks. `DirectBackend` wired with async send via goroutine.
**Files**: Updated `internal/node/node.go`, `internal/api/server.go`, `internal/daemon/daemon.go`, `internal/ui/direct.go`, `internal/ui/client_test.go`
**Tests**: All 214 tests pass (wiring ticket).
### [x] T-44: TUI file picker + transfer screens
**Completed**: 2026-02-10
**Summary**: File picker (`f` from home): browse filesystem, directories first, hidden files filtered, scrolling, enter to select/open dir, esc to cancel. Transfer screen (`t` from home): active transfers with progress bars, speed, ETA, cancel with `c`. File offer prompt: shown on `EventFileOffer`, displays sender/filename/size, y/n to accept/reject. New screens: `ScreenFilePicker`, `ScreenFileOffer`, `ScreenTransfer`. Home screen updated with `f` (send file) and `t` (transfers) keybinds.
**Files**: `internal/ui/screens/filepicker.go`, `internal/ui/screens/transfer.go`, `internal/ui/screens/fileoffer.go`, updated `internal/ui/app.go`, `internal/ui/screens/home.go`
**Tests**: All 214 tests pass (TUI screens verified via `go build`).

---

## Phase 7: Hardening & Cross-Platform

### [x] T-45: Auto-reconnection
**Completed**: 2026-02-10
**Summary**: `StateReconnecting` connection state. `Connector.Reconnect()` with exponential backoff (3 retries, 2s base). Cached discovery results (`cachedOrFreshLookup`) with 1-hour TTL, stale cache fallback when DHT unavailable. `StartAutoReconnect()` monitors connected peers via presence pings, triggers reconnection after 3 missed pings. Wired full subsystem initialization into daemon Tor-ready callback: discovery (DHT + mDNS), signaling (server + client), connector with state event bridge. `torDialerAdapter` and `discoveryFinderAdapter` bridge interface mismatches between packages.
**Files**: Updated `internal/transport/connector.go`, `internal/daemon/daemon.go`, `internal/signaling/server.go`
**Tests**: 5 new tests — reconnect with retry, reconnect uses cache, all retries fail, cached lookup falls back to stale, reconnecting state string. 235 total tests pass.
### [x] T-46: NAT fallback on connection drop
**Completed**: 2026-02-10
**Summary**: `NATTraverseFrom()` starts NAT cascade from a given strategy (skipping higher-priority). `ReconnectWithFallback()` tries same strategy first, then falls through: Direct → HolePunch → Relay → TorOnly. `connectFlowFromStrategy()` runs full signaling+IP exchange with strategy-specific NAT cascade. `StartAutoReconnect` uses `ReconnectWithFallback` for automatic degradation.
**Files**: Updated `internal/transport/nat.go`, `internal/transport/connector.go`
**Tests**: 3 new tests — NATTraverseFrom skips direct, NATTraverseFrom direct still works, ReconnectWithFallback success.
### [x] T-47: Multi-peer connection management
**Completed**: 2026-02-10
**Summary**: Connection pool with configurable `MaxPeers` (default 50) and LRU eviction (`evictIfNeededLocked`). `LastActivity` timestamp on `PeerConnection` updated on connect/reconnect. Concurrent file transfer limit with `MaxXfers` (default 5) using atomic CAS (`AcquireTransferSlot`/`ReleaseTransferSlot`/`ActiveTransfers`). Node-level accessors: `MaxPeers()`, `ActiveTransfers()`, `MaxTransfers()`, `AcquireTransferSlot()`, `ReleaseTransferSlot()`.
**Files**: Updated `internal/transport/connector.go`, `internal/node/node.go`
**Tests**: 3 new tests — LRU eviction, default limits (50 peers/5 transfers), transfer slot limiting.
### [x] T-48: Test suite
**Completed**: 2026-02-10
**Summary**: `testutil/fixtures.go`: deterministic keypairs from numeric seed (`DeterministicKeypair()`), fixed identities (`Alice`/`Bob`/`Carol`), address helpers. `testutil/testhost.go`: `NewTestHost()` with auto-cleanup, `NewConnectedPair()`, `ContextWithTimeout()`, `WaitFor()` polling helper, `MustGenerate()`. Fixed data race in signaling `Server.Serve()`/`Stop()` (fields protected by mutex) and `testDialer` map (added `sync.RWMutex`). `make test-race` passes clean. Per-package coverage: chat 83%, contacts 91%, discovery 75%, filetransfer 76%, identity 85%, protocol 82%, signaling 80%, tor 73%, transport 69%, testutil 83%.
**Files**: Created `testutil/fixtures.go`, `testutil/testhost.go`, `testutil/fixtures_test.go`, `testutil/testhost_test.go`. Updated `internal/signaling/server.go`, `internal/signaling/server_test.go`.
**Tests**: 10 new testutil tests + race fix. 235 total tests pass. `make test-race` clean.
### [ ] T-49: Cross-compilation + Dockerfile + systemd (deferred)

---

## Progress: 49/50 tickets complete
