"""Tests for ensemble.auth.SignedCredentials.

Covers the round-trip: sign a known timestamp, verify the produced metadata
matches what the Go server's `verifyAuth` expects (header names + payload).
"""

from __future__ import annotations

import os
import tempfile

import pytest
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import ed25519

from ensemble.auth import (
    HEADER_PUBKEY,
    HEADER_SIGNATURE,
    HEADER_TIMESTAMP,
    SignedCredentials,
)
from ensemble.errors import AuthError


def _fresh_seed() -> bytes:
    """Generate a fresh 32-byte Ed25519 seed."""
    priv = ed25519.Ed25519PrivateKey.generate()
    return priv.private_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PrivateFormat.Raw,
        encryption_algorithm=serialization.NoEncryption(),
    )


def test_metadata_round_trip_signature_verifies():
    seed = _fresh_seed()
    creds = SignedCredentials(seed_bytes=seed)
    md = dict(creds.metadata(now=1714000000))

    assert HEADER_PUBKEY in md
    assert HEADER_TIMESTAMP in md
    assert HEADER_SIGNATURE in md
    assert md[HEADER_TIMESTAMP] == "1714000000"

    # Reconstruct the public key from the metadata and verify the signature
    # over the timestamp string — exactly what the Go server does.
    pub_bytes = bytes.fromhex(md[HEADER_PUBKEY])
    pub = ed25519.Ed25519PublicKey.from_public_bytes(pub_bytes)
    sig = bytes.fromhex(md[HEADER_SIGNATURE])
    pub.verify(sig, md[HEADER_TIMESTAMP].encode("ascii"))


def test_pubkey_hex_matches_seed():
    seed = _fresh_seed()
    creds = SignedCredentials(seed_bytes=seed)

    expected_pub = (
        ed25519.Ed25519PrivateKey.from_private_bytes(seed)
        .public_key()
        .public_bytes(
            encoding=serialization.Encoding.Raw,
            format=serialization.PublicFormat.Raw,
        )
        .hex()
    )
    assert creds.pubkey_hex == expected_pub


def test_metadata_signs_distinct_timestamps_distinctly():
    seed = _fresh_seed()
    creds = SignedCredentials(seed_bytes=seed)
    md_a = dict(creds.metadata(now=1714000000))
    md_b = dict(creds.metadata(now=1714000001))
    assert md_a[HEADER_TIMESTAMP] != md_b[HEADER_TIMESTAMP]
    assert md_a[HEADER_SIGNATURE] != md_b[HEADER_SIGNATURE]


def test_seed_path_raw_bytes(tmp_path):
    seed = _fresh_seed()
    p = tmp_path / "seed.bin"
    p.write_bytes(seed)
    creds = SignedCredentials(seed_path=str(p))
    md = dict(creds.metadata(now=42))
    pub = ed25519.Ed25519PublicKey.from_public_bytes(bytes.fromhex(md[HEADER_PUBKEY]))
    pub.verify(bytes.fromhex(md[HEADER_SIGNATURE]), b"42")


def test_seed_path_hex_text(tmp_path):
    seed = _fresh_seed()
    p = tmp_path / "seed.hex"
    p.write_text(seed.hex())
    creds = SignedCredentials(seed_path=str(p))
    # Both raw + hex paths must yield the same public key for the same seed.
    assert creds.pubkey_hex == SignedCredentials(seed_bytes=seed).pubkey_hex


def test_rejects_seed_with_wrong_length():
    with pytest.raises(AuthError):
        SignedCredentials(seed_bytes=b"\x00" * 31)


def test_rejects_both_seed_args():
    with pytest.raises(AuthError):
        SignedCredentials(seed_bytes=_fresh_seed(), seed_path="/nonexistent")


def test_rejects_neither_seed_arg():
    with pytest.raises(AuthError):
        SignedCredentials()


def test_callable_dispatches_via_callback():
    """SignedCredentials is a grpc.AuthMetadataPlugin — verify __call__ contract."""
    seed = _fresh_seed()
    creds = SignedCredentials(seed_bytes=seed)

    captured: dict = {}

    def cb(md, err):
        captured["md"] = list(md)
        captured["err"] = err

    creds(context=None, callback=cb)  # type: ignore[arg-type]
    assert captured["err"] is None
    keys = {k for k, _ in captured["md"]}
    assert keys == {HEADER_PUBKEY, HEADER_TIMESTAMP, HEADER_SIGNATURE}


def test_header_names_match_go_server():
    """Lock the header names to the Go server's expectations (auth.go constants)."""
    assert HEADER_PUBKEY == "x-auth-pubkey"
    assert HEADER_TIMESTAMP == "x-auth-timestamp"
    assert HEADER_SIGNATURE == "x-auth-signature"
