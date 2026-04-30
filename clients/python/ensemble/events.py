"""Typed dataclasses for events streamed from the daemon to a registered service.

Events arrive on the wire as `ServiceEvent{type, payload}` with `payload` a
proto-encoded inner message keyed by `type`. We decode these into friendly
dataclasses so callers don't touch raw protobuf.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Union

from ._proto import ensemble_pb2 as pb


@dataclass(frozen=True)
class ChatMessage:
    """An inbound chat message addressed to the registered service."""

    type: str  # always "chat_message"
    from_addr: str
    text: str
    ts: int  # Unix millis


@dataclass(frozen=True)
class ConnectionRequest:
    """An inbound connection request awaiting accept/reject.

    Use `ServiceHandle.accept_connection(request_id)` /
    `ServiceHandle.reject_connection(request_id, reason)` to respond.
    """

    type: str  # always "connection_request"
    request_id: str
    from_addr: str


@dataclass(frozen=True)
class UnknownEvent:
    """Fallback for event types the client does not recognise.

    Forward-compatibility: future daemon versions may emit new event types;
    we surface them without crashing. The raw payload is exposed for callers
    that want to decode it themselves.
    """

    type: str
    payload: bytes


ServiceEvent = Union[ChatMessage, ConnectionRequest, UnknownEvent]


def decode_event(raw: pb.ServiceEvent) -> ServiceEvent:
    """Decode a proto ServiceEvent into a typed dataclass.

    Unknown event types fall through to UnknownEvent so the iterator never
    surprises a caller running against a newer daemon.
    """
    if raw.type == "chat_message":
        inner = pb.ServiceChatMessageEvent()
        inner.ParseFromString(raw.payload)
        return ChatMessage(
            type=raw.type,
            from_addr=inner.from_addr,
            text=inner.text,
            ts=inner.ts,
        )
    if raw.type == "connection_request":
        inner = pb.ServiceConnectionRequestEvent()
        inner.ParseFromString(raw.payload)
        return ConnectionRequest(
            type=raw.type,
            request_id=inner.request_id,
            from_addr=inner.from_addr,
        )
    return UnknownEvent(type=raw.type, payload=bytes(raw.payload))
