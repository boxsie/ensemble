"""Ensemble Python client library.

Public API surface — import these directly from `ensemble`. The `_proto/`
subpackage contains generated gRPC stubs and is internal; consumers should
not import from it.
"""

from .auth import SignedCredentials
from .client import ACL, Client, ServiceHandle
from .errors import (
    AuthError,
    ConnectionError,
    EnsembleError,
    RegistrationError,
)
from .events import (
    ChatMessage,
    ConnectionRequest,
    ServiceEvent,
    UnknownEvent,
)

__all__ = [
    "ACL",
    "AuthError",
    "ChatMessage",
    "Client",
    "ConnectionError",
    "ConnectionRequest",
    "EnsembleError",
    "RegistrationError",
    "ServiceEvent",
    "ServiceHandle",
    "SignedCredentials",
    "UnknownEvent",
]

__version__ = "0.1.0"
