"""Exception hierarchy for the ensemble client library."""

from __future__ import annotations


class EnsembleError(Exception):
    """Base class for all ensemble-client errors."""


class AuthError(EnsembleError):
    """Raised when admin-key signing or seed loading fails."""


class RegistrationError(EnsembleError):
    """Raised when a service manifest is rejected by the daemon."""


class ConnectionError(EnsembleError):
    """Raised for network or transport-level failures.

    The original gRPC error is available via the `__cause__` attribute when
    chained via `raise ... from e`.
    """
