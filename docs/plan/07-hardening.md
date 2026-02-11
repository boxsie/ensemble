# Phase 7: Hardening & Cross-Platform — T-45 to T-49

## Goal
Robust, production-ready, runs on Pi/desktop/containers.

---

## T-45: Auto-reconnection
Detect connection loss and reconnect automatically.
- Ping every 30s, 3 missed pongs → connection dead
- On disconnect: attempt reconnect using cached PeerRecord (skip DHT)
- If cached connection info stale → full DHT re-lookup
- Emit reconnection events to TUI
- **Files**: Update `internal/transport/connector.go`, `internal/node/node.go`
- **Verify**: Kill peer's network → detect disconnect → peer comes back → auto-reconnect.

## T-46: NAT fallback on connection drop
Gracefully degrade through NAT strategies when connection drops.
- Direct connection fails → try relay → try Tor-only
- Upgrade back to direct when possible
- **Files**: Update `internal/transport/nat.go`, `internal/transport/connector.go`
- **Verify**: Direct connection drops → falls back to relay → direct comes back → upgrades.

## T-47: Multi-peer connection management
Handle multiple simultaneous peer connections.
- Connection pool with configurable max (default: 50)
- Resource accounting per connection
- Concurrent transfers capped (default: 5)
- **Files**: Update `internal/node/node.go`, `internal/transport/connector.go`
- **Verify**: Connect to 3+ peers simultaneously, chat and transfer with each.

## T-48: Test suite
Comprehensive unit + integration tests.
- `testutil/testhost.go` — in-memory libp2p hosts for testing
- `testutil/fixtures.go` — deterministic test keypairs
- Integration tests (tagged `//go:build integration`): full Tor + DHT + signaling flow
- `make test`, `make test-integration`, `make test-race`
- **Files**: `testutil/`, test files throughout
- **Verify**: `make test` all green. `make test-race` no races. Code coverage >70%.

## T-49: Cross-compilation + Dockerfile + systemd
Build for all platforms, container and Pi deployment.
- Makefile targets: `build-linux-amd64`, `build-linux-arm64`, `build-darwin-arm64`, `build-windows-amd64`, `build-all`
- `Dockerfile`: multi-stage build, headless mode, expose gRPC port
- `ensemble.service`: systemd unit for `ensemble --headless`
- **Files**: Update `Makefile`, new `Dockerfile`, new `deploy/ensemble.service`
- **Verify**: Build arm64 binary, run on Pi. Docker container starts headless. systemd service starts/stops cleanly.
