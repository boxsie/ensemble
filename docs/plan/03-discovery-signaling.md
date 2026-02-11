# Phase 3: Discovery & Signaling — T-19 to T-27

## Goal
Two nodes find each other and exchange connection requests over Tor.

## Design Principles

**Hash-only DHT**: PeerRecords in the DHT contain only the address hash (quantum-safe) and onion address. No public keys are broadcast to the network. This prevents quantum key harvesting — even with Shor's algorithm, attackers cannot reverse the RIPEMD-160 hash to recover public keys.

**Contact-gated handshake**: The signaling server only reveals the node's public key to peers whose address is in the local contact list or explicitly accepted by the user. Unknown addresses trigger a TUI prompt before any key material is exposed.

**3-step identity verification**: Public keys are exchanged only after mutual verification via a challenge-response handshake:
1. Initiator → Responder: addresses (hashes only) + nonce
2. Responder → Initiator: public key + sign(nonce) — proves ownership of target address
3. Initiator → Responder: public key + sign(responder_nonce) — proves ownership of sender address

**Post-quantum ready**: The `Keypair` interface is designed so Ed25519 can be swapped for post-quantum signatures (e.g. CRYSTALS-Dilithium) without changing discovery, signaling, or transport layers.

**Bootstrap cascade**: On startup, nodes rejoin the DHT using this priority order:
1. Saved routing table peers (most recently seen first) — persisted to disk on shutdown
2. Contacts' stored onion addresses — contacts are implicit bootstrap nodes
3. mDNS — find anyone on LAN without config
4. Hardcoded bootstrap nodes — last resort, only needed for brand new users with no history

This means hardcoded bootstrap nodes are training wheels for a new network. Once a user has connected to anyone, their saved peers and contacts let them rejoin without centralized help.

---

## T-19: Kademlia routing table
Core DHT data structure: K-buckets, XOR distance, with disk persistence.
- `RoutingTable` with 160 K-buckets (one per bit of address hash)
- K = 20 (peers per bucket)
- `AddPeer(record)`, `RemovePeer(addr)`, `FindClosest(targetHash, count) → []PeerInfo`
- XOR distance metric on 20-byte address hashes (RIPEMD-160)
- `NodeIDFromPublicKey(pubKey)` and `NodeIDFromAddress(addr)` for ID derivation
- Anti-Sybil: rate-limit insertions per source, cap per source
- `Save(path)` / `Load(path)` — persist routing table to JSON for bootstrap on restart
- On shutdown: save all known peers with last-seen timestamps
- On startup: load saved peers, use as initial bootstrap candidates (most recently seen first)
- **Files**: `internal/discovery/routing.go`, `internal/discovery/routing_test.go`
- **Verify**: Test inserts peers, finds K closest to target by XOR distance, rejects when bucket full, rate limiting works. Save/load round-trip preserves peers.

## T-20: DHT protocol (announce + lookup)
Announce self and look up peers over Tor. Records are hash-only (no public keys).
- `DHTDiscovery.Announce(ctx, address, onionAddr)` — push PeerRecord (address + onion only) to K closest nodes
- `DHTDiscovery.Lookup(ctx, address) → PeerRecord` — iterative lookup
- PeerRecord: `{address, onionAddr, timestamp}` — no public key, no signature
- DHT records are unverified at the network level; trust is established at the signaling handshake layer
- Record expiry: reject records older than 24h
- Uses `tor.Dialer` for all DHT connections (`.onion` to `.onion`)
- **Files**: `internal/discovery/dht.go`, `internal/discovery/dht_test.go`
- **Verify**: Two nodes running — A announces, B looks up A by address, finds correct `.onion`. Expired records rejected.

## T-21: mDNS LAN discovery
Find peers on local network without Tor.
- Uses libp2p's mDNS or manual mDNS with service name `_ensemble._tcp.local.`
- `MDNSDiscovery.Start()`, `MDNSDiscovery.OnPeerFound(callback)`
- **Files**: `internal/discovery/mdns.go`, `internal/discovery/mdns_test.go`
- **Verify**: Two nodes on same LAN discover each other within seconds.

## T-22: Discovery manager
Unified discovery coordinator.
- `Manager.FindPeer(addr)` — try mDNS first (fast, LAN), then DHT (slower, global)
- `Manager.PeerChan()` — unified channel of discovered peers
- Deduplication: same peer found via mDNS and DHT → emit once
- **Files**: `internal/discovery/manager.go`
- **Verify**: FindPeer returns result from mDNS when on LAN, falls back to DHT when not.

## T-23: Signaling handshake protocol
3-step connection handshake over Tor. Public keys exchanged only after contact verification.
- **Step 1** — `ConnRequest { from_address, target_address, nonce }` — addresses (hashes) only, no public keys
- **Step 2** — `ConnResponse { accepted, pub_key, signature, nonce, reason }` — responder proves identity (only if accepted)
- **Step 3** — `ConnConfirm { pub_key, signature }` — initiator proves identity
- Contact gating: responder only reveals pub_key if initiator's address is in contacts or user accepts via TUI prompt
- Validate signatures, verify address derivation (pub_key hashes to claimed address)
- Verify nonce echo, check timestamp freshness (reject >5min)
- Replay protection: short-lived nonce cache
- **Files**: `internal/signaling/handshake.go`, `internal/signaling/handshake_test.go`
- **Verify**: Test creates request, validates it. Responder proves identity. Initiator proves identity. Tampered request rejected. Replayed nonce rejected. Unknown address rejected without key reveal.

## T-24: Signaling server (inbound)
Listen on Tor hidden service for connection requests.
- Accept TCP connections on hidden service listener
- Read `ConnRequest`, check if from_address is in contacts
- If known contact: proceed with identity proof (step 2)
- If unknown: emit `ConnectionReq` event → TUI prompt → wait for user accept/reject
- If rejected (or unknown + user rejects): send `ConnResponse(accepted=false)` — no public key revealed
- If accepted: send `ConnResponse` with identity proof, wait for `ConnConfirm`
- **Files**: `internal/signaling/server.go`
- **Verify**: Server running on `.onion`. Known contact dials → auto-proceeds. Unknown dials → event emitted → user accepts → handshake completes. Unknown rejected → no key revealed.

## T-25: Signaling client (outbound)
Send connection requests to peer's `.onion`.
- `Client.RequestConnection(ctx, peerOnion, port, localAddr, targetAddr) → (ConnResponse, error)`
- Dial via `tor.Dialer`, send `ConnRequest` (addresses + nonce only)
- Receive `ConnResponse`, verify responder's identity (pub_key hashes to target_address, signature valid)
- Send `ConnConfirm` with own identity proof
- **Files**: `internal/signaling/client.go`
- **Verify**: Client sends request to server from T-24, receives verified response, completes handshake.

## T-26: Wire discovery + signaling into node
Integrate discovery and signaling into daemon, expose via gRPC.
- Node starts discovery manager + signaling server on boot
- `Connect` RPC: lookup peer via discovery → send signaling request
- `AcceptConnection`/`RejectConnection` RPCs for inbound requests from unknown addresses
- Connection requests from unknown peers appear as events in TUI
- Known contacts auto-proceed through handshake
- **Files**: Update `internal/node/node.go`, `internal/api/server.go`
- **Verify**: Full flow: Node A calls `Connect(bob_address)` via gRPC → DHT lookup → signaling → B's TUI shows prompt (if unknown) → B accepts → handshake completes.

## T-27: TUI add contact + connection prompt
UI for adding contacts and accepting/rejecting connections.
- Add contact screen: enter address, optional alias, save to contacts
- Connection request notification: "Unknown peer (EaBC12...) wants to connect. [y] Accept [n] Reject"
- Note: accepting reveals your public key to that peer
- Contact list shows connection status
- **Files**: `internal/ui/screens/addcontact.go`, update `internal/ui/screens/home.go`
- **Verify**: Add contact by address. Receive connection request from unknown, accept from TUI. Known contacts connect without prompt.
