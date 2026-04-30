#!/usr/bin/env python3
"""Echo service — registers `echo` and replies to every chat with `echo: <text>`.

Doubles as the manual smoke test for the ensemble Python client.

Usage:
    python echo.py --socket /run/ensemble/sock --auth-seed /path/to/seed
    python echo.py --addr localhost:9090 --auth-seed /path/to/seed
"""

from __future__ import annotations

import argparse
import asyncio
import sys

from ensemble import (
    ACL,
    ChatMessage,
    Client,
    ConnectionRequest,
)


async def main() -> int:
    parser = argparse.ArgumentParser()
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--socket", help="Path to the daemon's gRPC unix socket")
    group.add_argument("--addr", help="Daemon gRPC TCP address, e.g. localhost:9090")
    parser.add_argument("--auth-seed", help="Path to the admin-key seed file")
    parser.add_argument("--name", default="echo", help="Service name to register (default: echo)")
    parser.add_argument("--tls", action="store_true", help="Use TLS for the gRPC channel")
    args = parser.parse_args()

    async with Client(
        socket_path=args.socket,
        addr=args.addr,
        tls=args.tls,
        auth_seed=args.auth_seed,
    ) as client:
        async with await client.register(
            args.name,
            acl=ACL.CONTACTS,
            description="echoes incoming chat messages",
        ) as svc:
            print(f"Registered '{args.name}' as {svc.address} ({svc.onion})", flush=True)
            async for ev in svc.events():
                if isinstance(ev, ChatMessage):
                    print(f"  <- {ev.from_addr}: {ev.text}", flush=True)
                    reply = f"echo: {ev.text}"
                    await svc.send_message(ev.from_addr, reply)
                    print(f"  -> {ev.from_addr}: {reply}", flush=True)
                elif isinstance(ev, ConnectionRequest):
                    print(f"  accepting connection from {ev.from_addr}", flush=True)
                    await svc.accept_connection(ev.request_id)
    return 0


if __name__ == "__main__":
    try:
        sys.exit(asyncio.run(main()))
    except KeyboardInterrupt:
        sys.exit(130)
