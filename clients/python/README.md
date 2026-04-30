# ensemble-client (Python)

Python client library for the [Ensemble](https://github.com/boxsie/ensemble)
decentralized P2P daemon. Wraps the `RegisterService` bidi gRPC stream so an
external Python process can register a service against a local daemon and
exchange chat messages with peers.

## Installation

Install directly from the ensemble repo (no PyPI release yet):

```bash
pip install "ensemble-client @ git+https://github.com/boxsie/ensemble.git#subdirectory=clients/python"
```

Or in development:

```bash
git clone https://github.com/boxsie/ensemble.git
pip install -e ensemble/clients/python
```

Requirements: Python 3.10+. Depends on `grpcio`, `protobuf`, `cryptography`.

## Quick start

```python
import asyncio
from ensemble import ACL, ChatMessage, Client

async def main():
    async with Client(
        socket_path="/run/ensemble/sock",
        auth_seed="/etc/ensemble/admin.seed",
    ) as client:
        async with await client.register("echo", acl=ACL.CONTACTS) as svc:
            print(f"Registered: {svc.address} {svc.onion}")
            async for ev in svc.events():
                if isinstance(ev, ChatMessage):
                    await svc.send_message(ev.from_addr, f"echo: {ev.text}")

asyncio.run(main())
```

See [`examples/echo.py`](examples/echo.py) for a complete runnable example.

## Connecting

Pass exactly one of `socket_path` or `addr` to `Client`:

- `socket_path="/run/ensemble/sock"` — Unix socket (the typical k8s sidecar setup).
- `addr="localhost:9090"` — TCP, optionally with `tls=True`.

The `auth_seed` argument may be either raw bytes or a path to a file
containing the seed (32 raw bytes, or 64 ASCII hex characters). It must match
the daemon's configured admin key (see `ensemble keygen` in the main repo).

### TLS

Pass `tls=True` to use TLS. The client uses the system trust store;
self-signed daemon certs (e.g. behind a LAN CA) need either the CA installed
in the trust store or `GRPC_DEFAULT_SSL_ROOTS_FILE_PATH` pointing at it.
There is no clean equivalent to the Go CLI's `--tls-insecure` flag in
grpc-python; the parameter exists for API parity but does not currently
disable verification.

## Events

`ServiceHandle.events()` yields decoded dataclasses, not raw protobuf:

- `ChatMessage(type, from_addr, text, ts)` — inbound chat.
- `ConnectionRequest(type, request_id, from_addr)` — inbound connection
  awaiting accept/reject. Respond with
  `svc.accept_connection(request_id)` or
  `svc.reject_connection(request_id, reason)`.
- `UnknownEvent(type, payload)` — forward-compat fallback for event types
  the client version doesn't recognise.

The daemon enforces backpressure with a 256-deep per-stream queue and drops
oldest events under sustained load (no on-wire signal). Consume events
promptly.

## Caveats

- `keypair_seed` on the manifest is currently advisory: the daemon's
  keystore is append-only and ignores externally-supplied seeds. Pin to the
  server-issued `address` from `ServiceRegistered` for stability across
  restarts. (T07 limitation; tracked for follow-up.)
- Outbound chat (`send_message`) currently travels under the daemon's
  primary node identity, not the registered service's identity. Inbound
  chat correctly carries the service address. (Also T07.)
- Async-only API. There's no synchronous wrapper; use `asyncio.run` or
  embed in your existing event loop.

## Regenerating the gRPC stubs

The `ensemble/_proto/*.py` files are checked in. Regenerate via the
top-level `Makefile`:

```bash
make proto
```

This regenerates Go stubs and Python stubs together.
