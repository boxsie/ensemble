// Package services owns the runtime registry of services hosted by the daemon.
//
// A "service" is a triple of (Ed25519 identity, .onion endpoint, handler set).
// Both the in-process built-ins (chat, filetransfer once migrated) and the
// future gRPC-registered external services (T07+) flow through the same
// Registry so the rest of the daemon — signaling demux, debug screens, ACL
// enforcement — has a single source of truth.
//
// The Registry composes the keystore (internal/identity) and the Tor engine
// (internal/tor): Register loads or generates an Ed25519 keypair under the
// service name, publishes an onion service for it, derives the Bitcoin-style
// address from the public key, and stores an immutable Service record.
// Unregister tears down the running listener; the keypair stays in the
// keystore (T01/T02 contract — both subsystems keep keys on disk).
//
// LookupByOnion is the hot path used by the signaling layer (T04) to route
// inbound envelopes to the correct service handler set. It is O(1).
//
// Handlers use composition rather than a single god-interface. A handler set
// is an opaque value that may implement any combination of ChatHandler,
// FileHandler, and ConnectionHandler. Helpers (AsChat, AsFile, AsConnection)
// recover the role-specific interface via type assertion, returning ok=false
// when the service does not implement that role. This lets external services
// register with only the roles they care about without forcing them to stub
// out the rest, and keeps the registry free of imports from chat/filetransfer.
package services
