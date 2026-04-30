"""Per-RPC Ed25519 admin-key signing for the ensemble gRPC API.

Mirrors the Go SignedCredentials in `internal/api/auth.go`. Each RPC attaches
three metadata headers signed against the current Unix timestamp; the daemon
verifies that the public key matches the configured admin key and that the
signature is fresh (5-minute window).
"""

from __future__ import annotations

import time
from pathlib import Path
from typing import Callable, Sequence

import grpc
from cryptography.hazmat.primitives.asymmetric import ed25519

from .errors import AuthError


# Header names must match `internal/api/auth.go` exactly.
HEADER_PUBKEY = "x-auth-pubkey"
HEADER_TIMESTAMP = "x-auth-timestamp"
HEADER_SIGNATURE = "x-auth-signature"


def _load_seed(seed: bytes | str | Path) -> bytes:
    """Resolve a seed argument to 32 raw bytes.

    Accepts either an in-memory 32-byte seed, or a path to a file whose
    contents are the seed (raw 32 bytes, or hex-encoded — both common).
    """
    if isinstance(seed, (str, Path)):
        path = Path(seed)
        try:
            data = path.read_bytes()
        except OSError as e:
            raise AuthError(f"reading seed file {path}: {e}") from e
        # Strip whitespace for hex/text-format seeds.
        stripped = data.strip()
        if len(stripped) == 64:
            try:
                return bytes.fromhex(stripped.decode("ascii"))
            except (ValueError, UnicodeDecodeError) as e:
                raise AuthError(f"seed file {path} is not valid hex") from e
        if len(data) == 32:
            return data
        raise AuthError(
            f"seed file {path} has unexpected length {len(data)} (want 32 bytes raw or 64 hex chars)"
        )
    if isinstance(seed, (bytes, bytearray)):
        if len(seed) != 32:
            raise AuthError(f"seed bytes have length {len(seed)} (want 32)")
        return bytes(seed)
    raise AuthError(f"unsupported seed type {type(seed).__name__}")


class SignedCredentials(grpc.AuthMetadataPlugin):
    """gRPC call credentials that sign each RPC's timestamp with Ed25519.

    Attaches `x-auth-pubkey`, `x-auth-timestamp`, `x-auth-signature` metadata
    in the same shape the Go server's AdminKey interceptor expects.
    """

    def __init__(self, *, seed_bytes: bytes | None = None, seed_path: str | Path | None = None):
        if (seed_bytes is None) == (seed_path is None):
            raise AuthError("provide exactly one of seed_bytes or seed_path")
        seed = seed_bytes if seed_bytes is not None else seed_path
        assert seed is not None
        seed_raw = _load_seed(seed)
        self._priv = ed25519.Ed25519PrivateKey.from_private_bytes(seed_raw)
        self._pub = self._priv.public_key()
        # Cache the 32-byte public key as a hex string — it's invariant.
        from cryptography.hazmat.primitives import serialization

        pub_raw = self._pub.public_bytes(
            encoding=serialization.Encoding.Raw,
            format=serialization.PublicFormat.Raw,
        )
        self._pub_hex = pub_raw.hex()

    @property
    def pubkey_hex(self) -> str:
        """Hex-encoded Ed25519 public key, useful for matching against the daemon's `--admin-key`."""
        return self._pub_hex

    def _sign(self, timestamp: str) -> str:
        sig = self._priv.sign(timestamp.encode("ascii"))
        return sig.hex()

    def metadata(self, now: float | None = None) -> tuple[tuple[str, str], ...]:
        """Build the auth metadata tuple. Exposed for tests; production use goes via __call__."""
        ts = str(int(now if now is not None else time.time()))
        return (
            (HEADER_PUBKEY, self._pub_hex),
            (HEADER_TIMESTAMP, ts),
            (HEADER_SIGNATURE, self._sign(ts)),
        )

    def __call__(
        self,
        context: grpc.AuthMetadataContext,
        callback: Callable[[Sequence[tuple[str, str]], Exception | None], None],
    ) -> None:
        try:
            md = self.metadata()
        except Exception as e:  # pragma: no cover - defensive
            callback((), AuthError(f"signing failed: {e}"))
            return
        callback(md, None)
