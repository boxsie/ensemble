"""High-level ensemble daemon client.

Wraps the gRPC `EnsembleService.RegisterService` bidi stream into an idiomatic
asyncio API. A single Client manages the channel + auth credentials; calling
`register()` produces a ServiceHandle that owns one bidi stream and exposes
event iteration plus action helpers (send_message, accept/reject connection).
"""

from __future__ import annotations

import asyncio
import enum
from typing import AsyncIterator

import grpc

from ._proto import ensemble_pb2 as pb
from ._proto import ensemble_pb2_grpc as pbg
from .auth import SignedCredentials
from .errors import ConnectionError, EnsembleError, RegistrationError
from .events import ServiceEvent, decode_event


class ACL(enum.IntEnum):
    """Access-control tier for a registered service.

    Mirrors the proto enum. Only `CONTACTS` is enforced at runtime in v0;
    `PUBLIC` and `ALLOWLIST` are accepted but the daemon currently treats
    them as advisory (see T07 limitations).
    """

    PUBLIC = pb.ACL_PUBLIC
    CONTACTS = pb.ACL_CONTACTS
    ALLOWLIST = pb.ACL_ALLOWLIST


def _build_channel(
    *,
    socket_path: str | None,
    addr: str | None,
    tls: bool,
    tls_insecure: bool,
    auth_seed: bytes | str | None,
) -> grpc.aio.Channel:
    if (socket_path is None) == (addr is None):
        raise EnsembleError("provide exactly one of socket_path or addr")

    target = f"unix://{socket_path}" if socket_path else addr
    assert target is not None

    call_creds: grpc.CallCredentials | None = None
    if auth_seed is not None:
        if isinstance(auth_seed, (bytes, bytearray)):
            plugin = SignedCredentials(seed_bytes=bytes(auth_seed))
        else:
            plugin = SignedCredentials(seed_path=auth_seed)
        call_creds = grpc.metadata_call_credentials(plugin)

    if tls:
        # grpc-python's standard TLS uses system root certificates. There's no
        # clean built-in "skip verification" knob equivalent to Go's
        # InsecureSkipVerify; if `tls_insecure` is set, callers must arrange
        # for the daemon's CA to be trusted some other way (env var
        # GRPC_DEFAULT_SSL_ROOTS_FILE_PATH, or installing the CA into the
        # system trust store). We document this in the README rather than
        # silently misleading the user.
        channel_creds = grpc.ssl_channel_credentials()
        if call_creds is not None:
            composite = grpc.composite_channel_credentials(channel_creds, call_creds)
            return grpc.aio.secure_channel(target, composite)
        return grpc.aio.secure_channel(target, channel_creds)

    if call_creds is not None:
        # Plaintext + per-RPC creds. grpc requires a "local" channel cred for this.
        local_creds = grpc.local_channel_credentials()
        composite = grpc.composite_channel_credentials(local_creds, call_creds)
        return grpc.aio.secure_channel(target, composite)

    return grpc.aio.insecure_channel(target)


class Client:
    """Connection to a local or remote ensemble daemon.

    Opens a single gRPC channel; one or more services may be registered
    against it. Use `async with Client(...)` for automatic cleanup.
    """

    def __init__(
        self,
        *,
        socket_path: str | None = None,
        addr: str | None = None,
        tls: bool = False,
        tls_insecure: bool = False,
        auth_seed: bytes | str | None = None,
    ):
        self._channel = _build_channel(
            socket_path=socket_path,
            addr=addr,
            tls=tls,
            tls_insecure=tls_insecure,
            auth_seed=auth_seed,
        )
        self._stub = pbg.EnsembleServiceStub(self._channel)
        self._closed = False

    async def register(
        self,
        name: str,
        acl: ACL = ACL.CONTACTS,
        allowlist: list[str] | None = None,
        keypair_seed: bytes | None = None,
        description: str = "",
    ) -> "ServiceHandle":
        """Register a service with the daemon.

        Returns a ServiceHandle once the daemon replies with ServiceRegistered.
        Raises RegistrationError if the manifest is rejected.

        Note: `keypair_seed` is currently advisory. The daemon's keystore is
        append-only and ignores externally-supplied seeds in v0; pin to the
        server-issued `address` instead.
        """
        manifest = pb.ServiceManifest(
            name=name,
            description=description,
            acl=int(acl),
            allowlist=list(allowlist or []),
            keypair_seed=keypair_seed or b"",
        )
        handle = ServiceHandle(self._stub)
        await handle._start(manifest)
        return handle

    async def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        await self._channel.close()

    async def __aenter__(self) -> "Client":
        return self

    async def __aexit__(self, exc_type, exc, tb) -> None:
        await self.close()


class ServiceHandle:
    """One registered service. Owns a single bidi RegisterService stream."""

    address: str
    onion: str

    def __init__(self, stub: pbg.EnsembleServiceStub):
        self._stub = stub
        self._send_queue: asyncio.Queue[pb.ServiceClientMessage | None] = asyncio.Queue()
        self._call: grpc.aio.StreamStreamCall | None = None
        self._closed = False
        self.address = ""
        self.onion = ""

    async def _request_iter(self) -> AsyncIterator[pb.ServiceClientMessage]:
        while True:
            msg = await self._send_queue.get()
            if msg is None:
                return
            yield msg

    async def _start(self, manifest: pb.ServiceManifest) -> None:
        # Prime the send queue with the manifest before opening the stream:
        # the server reads the first client message synchronously and replies
        # with ServiceRegistered, so the queue must have something to feed.
        await self._send_queue.put(pb.ServiceClientMessage(manifest=manifest))

        try:
            self._call = self._stub.RegisterService(self._request_iter())
            first = await self._call.read()
        except grpc.aio.AioRpcError as e:
            raise ConnectionError(f"opening RegisterService stream: {e.details()}") from e

        if first == grpc.aio.EOF:
            raise RegistrationError("daemon closed stream before sending ServiceRegistered")

        if first.WhichOneof("msg") == "error":
            raise RegistrationError(first.error.message)
        if first.WhichOneof("msg") != "registered":
            raise RegistrationError(
                f"unexpected first message from daemon: {first.WhichOneof('msg')}"
            )
        self.address = first.registered.address
        self.onion = first.registered.onion

    async def events(self) -> AsyncIterator[ServiceEvent]:
        """Yield decoded events as they arrive from the daemon.

        Drops are surfaced as a gap in event order (the daemon's 256-deep
        per-stream queue evicts oldest under sustained backpressure); there
        is no on-wire signal, so consume promptly.
        """
        if self._call is None:
            raise EnsembleError("events() called before register()")
        # grpc.aio forbids mixing read() and async-iter on one call; we use
        # read() exclusively (the first message was already consumed in _start).
        try:
            while True:
                msg = await self._call.read()
                if msg is grpc.aio.EOF:
                    return
                kind = msg.WhichOneof("msg")
                if kind == "event":
                    yield decode_event(msg.event)
                elif kind == "error":
                    # Recoverable handler-side error per T07; keep the stream
                    # alive. Hard errors close the stream and EOF will fire.
                    continue
                # `registered` only appears as the first message (consumed in _start).
        except grpc.aio.AioRpcError as e:
            if e.code() == grpc.StatusCode.CANCELLED:
                return
            raise ConnectionError(f"reading events: {e.details()}") from e

    async def send_message(self, to_addr: str, text: str) -> None:
        """Send a chat message from the registered service to a peer.

        v0: outbound chat travels under the daemon's primary node identity,
        not the registered service's identity (see T07 follow-ups).
        """
        await self._send(
            pb.ServiceClientMessage(
                send_message=pb.ServiceSendMessage(to_addr=to_addr, text=text),
            )
        )

    async def accept_connection(self, request_id: str) -> None:
        """Accept an inbound connection request received as a ConnectionRequest event."""
        await self._send(
            pb.ServiceClientMessage(
                accept=pb.ServiceAcceptConnection(request_id=request_id),
            )
        )

    async def reject_connection(self, request_id: str, reason: str = "") -> None:
        """Reject an inbound connection request received as a ConnectionRequest event."""
        await self._send(
            pb.ServiceClientMessage(
                reject=pb.ServiceRejectConnection(request_id=request_id, reason=reason),
            )
        )

    async def _send(self, msg: pb.ServiceClientMessage) -> None:
        if self._closed:
            raise EnsembleError("ServiceHandle is closed")
        await self._send_queue.put(msg)

    async def close(self) -> None:
        if self._closed:
            return
        self._closed = True
        # Half-close the send side; the server's stream context fires when
        # the channel goes away, so we also cancel the call to wake any
        # pending reads.
        await self._send_queue.put(None)
        if self._call is not None:
            self._call.cancel()

    async def __aenter__(self) -> "ServiceHandle":
        return self

    async def __aexit__(self, exc_type, exc, tb) -> None:
        await self.close()
