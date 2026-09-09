"""Live smoke test: OpenCode Go chat completions with raw wire model ids only.

Requires OPENCODE_GO_API_KEY (or --api-key). Exits 0 only if both probes return
parseable assistant text.

Usage:
  .\\.venv\\Scripts\\python.exe dev/scripts/opencode_go_verify_replies.py
"""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import sys
from typing import Any

import httpx

DEFAULT_BASE = "https://opencode.ai/zen/go/v1"


def _preview(s: str, limit: int = 400) -> str:
    one = " ".join(s.split())
    return one[:limit] + ("…" if len(one) > limit else "")


def _content_from_openai_parts(content: Any) -> str | None:
    if isinstance(content, str):
        t = content.strip()
        return t or None
    if isinstance(content, list):
        texts: list[str] = []
        for part in content:
            if isinstance(part, str):
                texts.append(part)
            elif isinstance(part, dict):
                if part.get("type") == "text" and isinstance(part.get("text"), str):
                    texts.append(part["text"])
                elif isinstance(part.get("content"), str):
                    texts.append(part["content"])
        joined = "".join(texts).strip()
        return joined or None
    return None


def _openai_assistant_text(data: dict[str, Any]) -> str | None:
    if isinstance(data.get("error"), dict):
        return None
    choices = data.get("choices")
    if not isinstance(choices, list) or not choices:
        return None
    first = choices[0]
    if not isinstance(first, dict):
        return None
    msg = first.get("message")
    if isinstance(msg, dict):
        got = _content_from_openai_parts(msg.get("content"))
        if got:
            return got
    if isinstance(first.get("text"), str):
        t = str(first["text"]).strip()
        return t or None
    delta = first.get("delta")
    if isinstance(delta, dict):
        return _content_from_openai_parts(delta.get("content"))
    return None


def _anthropic_assistant_text(data: dict[str, Any]) -> str | None:
    if isinstance(data.get("error"), dict):
        return None
    content = data.get("content")
    if isinstance(content, str):
        t = content.strip()
        return t or None
    if not isinstance(content, list):
        return None
    text_parts: list[str] = []
    for block in content:
        if not isinstance(block, dict):
            continue
        btype = block.get("type")
        if btype == "thinking":
            continue
        if isinstance(block.get("text"), str):
            text_parts.append(block["text"])
    joined = "".join(text_parts).strip()
    if joined:
        return joined
    # OpenCode Go / MiniMax may return only interleaved thinking blocks for short budgets.
    for block in content:
        if isinstance(block, dict) and isinstance(block.get("thinking"), str):
            th = block["thinking"].strip()
            if th:
                return f"[thinking-only] {_preview(th, 300)}"
    return None


async def _probe_openai(
    client: httpx.AsyncClient, base: str, api_key: str
) -> tuple[int, str | None, str | None]:
    url = f"{base.rstrip('/')}/chat/completions"
    headers = {
        "Authorization": f"Bearer {api_key}",
        "Content-Type": "application/json",
        "x-opencode-session": "verify-replies-openai",
    }
    payload = {
        "model": "glm-5.1",
        "max_tokens": 256,
        "stream": False,
        "messages": [{"role": "user", "content": "Reply with exactly: OK_OPENAI"}],
    }
    r = await client.post(url, headers=headers, json=payload)
    body_text = r.text
    if r.status_code != 200:
        return r.status_code, None, _preview(body_text)
    try:
        data = r.json()
    except Exception:
        return r.status_code, None, _preview(body_text)
    if not isinstance(data, dict):
        return r.status_code, None, _preview(body_text)
    extracted = _openai_assistant_text(data)
    hint = None if extracted else _preview(body_text)
    return r.status_code, extracted, hint


async def _probe_omen_alpha(
    client: httpx.AsyncClient, base: str, api_key: str
) -> tuple[int, str | None, str | None]:
    url = f"{base.rstrip('/')}/chat/completions"
    headers = {
        "Authorization": f"Bearer {api_key}",
        "Content-Type": "application/json",
        "x-opencode-session": "verify-replies-omen-alpha",
    }
    payload = {
        "model": "omen-alpha",
        "max_tokens": 256,
        "stream": False,
        "messages": [{"role": "user", "content": "Reply with exactly: OK_OMEN_ALPHA"}],
    }
    r = await client.post(url, headers=headers, json=payload)
    body_text = r.text
    if r.status_code != 200:
        return r.status_code, None, _preview(body_text)
    try:
        data = r.json()
    except Exception:
        return r.status_code, None, _preview(body_text)
    if not isinstance(data, dict):
        return r.status_code, None, _preview(body_text)
    extracted = _openai_assistant_text(data)
    hint = None if extracted else _preview(body_text)
    return r.status_code, extracted, hint


async def _probe_anthropic(
    client: httpx.AsyncClient, base: str, api_key: str
) -> tuple[int, str | None, str | None]:
    url = f"{base.rstrip('/')}/messages"
    headers = {
        "x-api-key": api_key,
        "anthropic-version": "2023-06-01",
        "Content-Type": "application/json",
        "x-opencode-session": "verify-replies-anthropic",
    }
    payload = {
        "model": "minimax-m2.7",
        "max_tokens": 512,
        "stream": False,
        "messages": [{"role": "user", "content": "Reply with exactly: OK_ANTHROPIC"}],
    }
    r = await client.post(url, headers=headers, json=payload)
    body_text = r.text
    if r.status_code != 200:
        return r.status_code, None, _preview(body_text)
    try:
        data = r.json()
    except Exception:
        return r.status_code, None, _preview(body_text)
    if not isinstance(data, dict):
        return r.status_code, None, _preview(body_text)
    extracted = _anthropic_assistant_text(data)
    hint = None if extracted else _preview(body_text)
    return r.status_code, extracted, hint


async def amain() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--api-key", default=os.environ.get("OPENCODE_GO_API_KEY"))
    p.add_argument(
        "--base-url", default=os.environ.get("OPENCODE_GO_API_BASE_URL", DEFAULT_BASE)
    )
    p.add_argument("--timeout", type=float, default=60.0)
    args = p.parse_args()
    key = args.api_key
    if not key:
        print(
            "Missing API key: set OPENCODE_GO_API_KEY or pass --api-key",
            file=sys.stderr,
        )
        return 2

    base = str(args.base_url).strip()
    async with httpx.AsyncClient(timeout=args.timeout, follow_redirects=True) as client:
        o_status, o_text, o_hint = await _probe_openai(client, base, key)
        omen_status, omen_text, omen_hint = await _probe_omen_alpha(client, base, key)
        a_status, a_text, a_hint = await _probe_anthropic(client, base, key)

    def _prev(x: str | None) -> str | None:
        if not x:
            return None
        return (x[:200] + "…") if len(x) > 200 else x

    result = {
        "base_url": base,
        "openai_chat_completions": {
            "model_sent": "kimi-k2.5",
            "http_status": o_status,
            "assistant_preview": _prev(o_text),
            "body_hint_if_unparsed": o_hint,
            "ok": bool(o_text),
        },
        "omen_alpha": {
            "model_sent": "omen-alpha",
            "http_status": omen_status,
            "assistant_preview": _prev(omen_text),
            "body_hint_if_unparsed": omen_hint,
            "ok": bool(omen_text),
        },
        "anthropic_messages": {
            "model_sent": "minimax-m2.7",
            "http_status": a_status,
            "assistant_preview": _prev(a_text),
            "body_hint_if_unparsed": a_hint,
            "ok": bool(a_text),
        },
    }
    print(json.dumps(result, indent=2))

    if not o_text:
        print(
            f"\nOpenAI path failed: status={o_status} (expected 200 and choices[0].message.content)",
            file=sys.stderr,
        )
    if not omen_text:
        print(f"\nOmen Alpha probe failed: status={omen_status}", file=sys.stderr)
    if not a_text:
        print(
            f"\nAnthropic path failed: status={a_status} (expected 200 and text blocks)",
            file=sys.stderr,
        )

    return 0 if (o_text and omen_text and a_text) else 1


if __name__ == "__main__":
    raise SystemExit(asyncio.run(amain()))
