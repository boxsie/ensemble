# Phase 2: Tor — T-15 to T-18

## Goal
Embedded Tor starts, hidden service is reachable, progress streams to clients.

---

## T-15: Embedded Tor engine
Start/stop embedded Tor, report bootstrap progress.
- `Engine.Start(ctx)` — launch Tor via go-libtor + bine
- `Engine.ProgressChan()` — channel of `{Percent, Summary}` updates
- `Engine.SOCKSPort()` — Tor SOCKS5 proxy port
- `Engine.Ready()` — channel closed when bootstrapped
- `Engine.Stop()` — graceful shutdown
- **Files**: `internal/tor/engine.go`, `internal/tor/engine_test.go`
- **Verify**: Engine starts, progress events arrive, SOCKS port is usable, clean shutdown.

## T-16: Tor hidden service
Create a persistent hidden service for inbound connections.
- `Engine.CreateOnionService(ctx, port, keyPath) → *OnionService`
- Persists hidden service key so `.onion` address survives restarts
- `OnionService.OnionID()` → `.onion` address string
- `OnionService.Listener()` → `net.Listener` for accepting connections
- **Files**: `internal/tor/onion.go`, `internal/tor/onion_test.go`
- **Verify**: Create service, `.onion` address printed. Restart → same `.onion` address. Second node can TCP-dial the `.onion`.

## T-17: Tor SOCKS dialer
Outbound connections through Tor.
- `Dialer.DialOnion(ctx, onionAddr, port) → net.Conn`
- Uses Tor's SOCKS5 proxy for all outbound connections
- **Files**: `internal/tor/dialer.go`
- **Verify**: Dial a known `.onion` address, send/receive data.

## T-18: Wire Tor into daemon + TUI
Integrate Tor engine into the daemon lifecycle and show progress in TUI.
- Daemon starts Tor engine as part of `Node.Start()`
- Bootstrap progress → EventBus → gRPC Subscribe stream
- TUI splash screen shows real Tor bootstrap percentage + spinner
- `GetStatus` RPC returns Tor state and `.onion` address
- Transition splash → home screen when Tor is ready
- **Files**: Update `internal/daemon/daemon.go`, `internal/node/node.go`, `internal/ui/screens/splash.go`, `internal/api/server.go`
- **Verify**: Launch app, splash shows "Starting Tor... 45%", transitions to home when ready. `GetStatus` returns `tor_ready: true`.
