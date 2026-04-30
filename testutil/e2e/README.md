# e2e — two-daemon integration smoke test

Spins up two `ensemble --headless` subprocesses in temporary data dirs,
each with its own Tor onion service, and verifies a chat message flows
from A → B over the full peer-to-peer stack (DHT discovery, signaling
3-step handshake, encrypted IP exchange, libp2p QUIC).

Unlike the in-tree unit tests, which stub Tor and the dialer, this test
exercises real Tor circuits and the production daemon binary. It is the
only thing in the tree that catches integration-level regressions in the
signaling/transport seam (e.g. the responder-side IP exchange that was
missing pre-fix).

## Run

From the repo root:

```bash
go run ./testutil/e2e
```

Add `--verbose` to stream both daemons' logs to stderr while the test
runs. Add `--keep` to leave the temp data dirs in place after exit so
you can inspect logs (`/tmp/ensemble-e2e-*/daemon.log`).

By default the runner builds `ensemble` from the working tree into a
temp file. Pass `--binary /path/to/ensemble` to test a pre-built binary.

```bash
go run ./testutil/e2e --tor-path /usr/bin/tor --verbose
```

Expected runtime is ~30-90s depending on how fast Tor publishes the two
onion descriptors. Exits 0 on success, non-zero with an explanation on
any failure.

## What it asserts

1. Both daemons start, bootstrap Tor, and publish their onion service.
2. gRPC `GetIdentity` and `GetStatus` reach each daemon.
3. `AddContact` on each side persists the peer's public key (regression
   test for the bug where the api server dropped `req.PublicKey`).
4. `Connect` from A → B completes — exercises DHT lookup, the 3-step
   signaling handshake, and the responder-side IP exchange.
5. `SendMessage` from A → B is received as a `chat_message` event on
   B's `Subscribe` stream with the same text payload.
