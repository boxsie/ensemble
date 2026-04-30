"""Tests for ensemble.Client and ServiceHandle.

Construction-level tests (no daemon required) plus end-to-end coverage of the
RegisterService bidi stream against an in-process mock server. The real-daemon
smoke test lives in `examples/echo.py`.
"""

from __future__ import annotations

import asyncio
from concurrent import futures

import grpc
import pytest

from ensemble import (
    ACL,
    ChatMessage,
    Client,
    ConnectionRequest,
    EnsembleError,
    RegistrationError,
)
from ensemble._proto import ensemble_pb2 as pb
from ensemble._proto import ensemble_pb2_grpc as pbg


# ---------- Construction / option validation ----------


def test_acl_enum_matches_proto():
    """ACL enum values must match the proto enum exactly — the daemon parses by integer."""
    assert ACL.PUBLIC == pb.ACL_PUBLIC
    assert ACL.CONTACTS == pb.ACL_CONTACTS
    assert ACL.ALLOWLIST == pb.ACL_ALLOWLIST


def test_client_requires_exactly_one_target():
    with pytest.raises(EnsembleError):
        Client()  # neither
    with pytest.raises(EnsembleError):
        Client(socket_path="/tmp/x", addr="localhost:9090")  # both


async def test_client_opens_and_closes_cleanly():
    """Construction shouldn't perform I/O; close() must be idempotent."""
    client = Client(addr="localhost:0")
    await client.close()
    await client.close()  # second close is a no-op


async def test_client_async_context_manager():
    async with Client(addr="localhost:0") as client:
        assert client is not None


# ---------- End-to-end with a mock server ----------


class _MockServicer(pbg.EnsembleServiceServicer):
    """Minimal mock that handles RegisterService for client-shape tests.

    Behavior:
      - Reads the first ServiceClientMessage. If it's not a manifest -> error.
      - Otherwise replies ServiceRegistered(address="EFAKE", onion="x.onion").
      - Optionally emits a chat_message event.
      - Echoes any inbound ServiceSendMessage into a captured list for the
        test to assert against.
    """

    def __init__(self, *, fail_manifest: bool = False, emit_event: bool = False):
        self.fail_manifest = fail_manifest
        self.emit_event = emit_event
        self.captured_sends: list[pb.ServiceSendMessage] = []
        self.captured_accepts: list[pb.ServiceAcceptConnection] = []
        self.event_emitted = asyncio.Event()

    def RegisterService(self, request_iterator, context):
        first = next(request_iterator)
        if first.WhichOneof("msg") != "manifest":
            yield pb.ServiceServerMessage(
                error=pb.ServiceError(message="first message must be manifest"),
            )
            return
        if self.fail_manifest:
            yield pb.ServiceServerMessage(error=pb.ServiceError(message="manifest rejected"))
            return

        yield pb.ServiceServerMessage(
            registered=pb.ServiceRegistered(address="EFAKE", onion="fake.onion"),
        )

        if self.emit_event:
            chat = pb.ServiceChatMessageEvent(
                from_addr="EPEER", text="hello", ts=1714000000000
            )
            yield pb.ServiceServerMessage(
                event=pb.ServiceEvent(
                    type="chat_message", payload=chat.SerializeToString()
                ),
            )

        for msg in request_iterator:
            kind = msg.WhichOneof("msg")
            if kind == "send_message":
                self.captured_sends.append(msg.send_message)
            elif kind == "accept":
                self.captured_accepts.append(msg.accept)


@pytest.fixture
async def mock_server():
    """Start a real gRPC server on a localhost port, yield (servicer, addr)."""
    servicer = _MockServicer(emit_event=True)
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=2))
    pbg.add_EnsembleServiceServicer_to_server(servicer, server)
    port = server.add_insecure_port("127.0.0.1:0")
    server.start()
    try:
        yield servicer, f"127.0.0.1:{port}"
    finally:
        server.stop(grace=0)


async def test_register_returns_service_handle(mock_server):
    servicer, addr = mock_server
    async with Client(addr=addr) as client:
        async with await client.register("echo", acl=ACL.CONTACTS) as svc:
            assert svc.address == "EFAKE"
            assert svc.onion == "fake.onion"


async def test_events_decode_chat_message(mock_server):
    servicer, addr = mock_server
    async with Client(addr=addr) as client:
        async with await client.register("echo") as svc:
            ev = await asyncio.wait_for(anext(svc.events()), timeout=2.0)
            assert isinstance(ev, ChatMessage)
            assert ev.from_addr == "EPEER"
            assert ev.text == "hello"
            assert ev.ts == 1714000000000


async def test_send_message_round_trips_to_server(mock_server):
    servicer, addr = mock_server
    async with Client(addr=addr) as client:
        async with await client.register("echo") as svc:
            await svc.send_message("EALICE", "hi")
            # Drain until the server captures the action.
            for _ in range(50):
                if servicer.captured_sends:
                    break
                await asyncio.sleep(0.02)
    assert len(servicer.captured_sends) == 1
    assert servicer.captured_sends[0].to_addr == "EALICE"
    assert servicer.captured_sends[0].text == "hi"


async def test_accept_connection_round_trips_to_server(mock_server):
    servicer, addr = mock_server
    async with Client(addr=addr) as client:
        async with await client.register("echo") as svc:
            await svc.accept_connection("req-123")
            for _ in range(50):
                if servicer.captured_accepts:
                    break
                await asyncio.sleep(0.02)
    assert len(servicer.captured_accepts) == 1
    assert servicer.captured_accepts[0].request_id == "req-123"


async def test_registration_error_when_manifest_rejected():
    servicer = _MockServicer(fail_manifest=True)
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=2))
    pbg.add_EnsembleServiceServicer_to_server(servicer, server)
    port = server.add_insecure_port("127.0.0.1:0")
    server.start()
    try:
        async with Client(addr=f"127.0.0.1:{port}") as client:
            with pytest.raises(RegistrationError, match="manifest rejected"):
                await client.register("echo")
    finally:
        server.stop(grace=0)


async def test_connection_request_event_decoded(mock_server):
    """Verify the ConnectionRequest variant decodes through events()."""
    # Reuse the mock but inject a connection_request event by patching emit.
    servicer, addr = mock_server

    # Add a second servicer instance configured to send connection_request.
    cr_servicer = _MockServicer(emit_event=False)

    def custom_register(request_iterator, context):
        first = next(request_iterator)
        assert first.WhichOneof("msg") == "manifest"
        yield pb.ServiceServerMessage(
            registered=pb.ServiceRegistered(address="ECR", onion="cr.onion"),
        )
        cr = pb.ServiceConnectionRequestEvent(request_id="r1", from_addr="EBOB")
        yield pb.ServiceServerMessage(
            event=pb.ServiceEvent(
                type="connection_request", payload=cr.SerializeToString()
            ),
        )
        for _ in request_iterator:
            pass

    cr_servicer.RegisterService = custom_register  # type: ignore[method-assign]

    server = grpc.server(futures.ThreadPoolExecutor(max_workers=2))
    pbg.add_EnsembleServiceServicer_to_server(cr_servicer, server)
    port = server.add_insecure_port("127.0.0.1:0")
    server.start()
    try:
        async with Client(addr=f"127.0.0.1:{port}") as client:
            async with await client.register("echo") as svc:
                ev = await asyncio.wait_for(anext(svc.events()), timeout=2.0)
                assert isinstance(ev, ConnectionRequest)
                assert ev.request_id == "r1"
                assert ev.from_addr == "EBOB"
    finally:
        server.stop(grace=0)
