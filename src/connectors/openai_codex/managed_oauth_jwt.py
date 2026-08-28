"""JWT helpers for OpenAI Codex managed OAuth accounts."""

from __future__ import annotations

import base64
import json
from typing import Any


def decode_jwt_claims(token: str) -> dict[str, Any] | None:
    """Best-effort JWT payload decode without signature verification."""
    try:
        parts = token.split(".")
        if len(parts) < 2:
            return None
        payload_b64 = parts[1]
        padded = payload_b64 + "=" * (-len(payload_b64) % 4)
        payload_bytes = base64.urlsafe_b64decode(padded.encode("ascii"))
        payload = json.loads(payload_bytes.decode("utf-8", errors="replace"))
        if not isinstance(payload, dict):
            return None
        return payload
    except Exception:
        return None


def extract_chatgpt_account_id_from_token(token: str) -> str | None:
    """Extract ChatGPT account ID claim from access token payload."""
    payload = decode_jwt_claims(token)
    if payload is None:
        return None
    direct_account_id = payload.get("chatgpt_account_id")
    if isinstance(direct_account_id, str) and direct_account_id:
        return direct_account_id
    auth_claim = payload.get("https://api.openai.com/auth")
    if not isinstance(auth_claim, dict):
        return None
    account_id = auth_claim.get("chatgpt_account_id")
    if isinstance(account_id, str) and account_id:
        return account_id
    return None


def extract_email_from_token(token: str) -> str | None:
    """Extract email claim when present."""
    payload = decode_jwt_claims(token)
    if payload is None:
        return None
    email = payload.get("email")
    if isinstance(email, str) and email:
        return email
    return None


def extract_expiry_ms_from_token(token: str) -> int | None:
    """Extract exp claim and convert to milliseconds."""
    payload = decode_jwt_claims(token)
    if payload is None:
        return None
    exp = payload.get("exp")
    if isinstance(exp, int | float):
        exp_ms = int(float(exp) * 1000)
        if exp_ms > 0:
            return exp_ms
    return None
