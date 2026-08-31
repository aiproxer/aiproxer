#!/usr/bin/env python
"""
Demonstration and functional proof for the Runinfra (runinfra.ai) backend connector.

This script boots an in-process instance of the proxy, exercises all primary protocols
and cross-API endpoints with the runinfra connector, and outputs a formatted verification
report confirming functional state.

Usage:

    ./.venv/Scripts/python.exe dev/scripts/demo_runinfra_functional_proof.py
"""

from __future__ import annotations

import asyncio
import json
import os
import sys
from typing import Any

import httpx

# Ensure repo root is on sys.path
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..")))

import respx
from src.core.app.stages import (
    BackendStage,
    CommandStage,
    ControllerStage,
    CoreServicesStage,
    InfrastructureStage,
    ProcessorStage,
)
from src.core.app.test_builder import ApplicationTestBuilder
from src.core.config.app_config import (
    AppConfig,
    AuthConfig,
    BackendConfig,
    BackendSettings,
)

_BASE = "https://api.runinfra.ai/v1"
_MODEL = "meta-llama/Llama-3.3-70B-Instruct"
_PROXY_MODEL = f"runinfra/{_MODEL}"

GREEN = "\033[92m"
RED = "\033[91m"
BOLD = "\033[1m"
CYAN = "\033[96m"
RESET = "\033[0m"


def _models_payload() -> dict[str, Any]:
    return {
        "object": "list",
        "data": [
            {
                "id": _MODEL,
                "object": "model",
                "created": 1700000000,
                "owned_by": "meta",
            },
            {
                "id": "deepseek-ai/DeepSeek-V3",
                "object": "model",
                "created": 1700000000,
                "owned_by": "deepseek",
            },
            {
                "id": "Qwen/Qwen2.5-Coder-32B-Instruct",
                "object": "model",
                "created": 1700000000,
                "owned_by": "qwen",
            },
        ],
    }


def _chat_payload() -> dict[str, Any]:
    return {
        "id": "chatcmpl-runinfra-demo-001",
        "object": "chat.completion",
        "created": 1700000000,
        "model": _MODEL,
        "choices": [
            {
                "index": 0,
                "message": {
                    "role": "assistant",
                    "content": "Verification Success: Runinfra Chat Completions is functional.",
                },
                "finish_reason": "stop",
            }
        ],
        "usage": {"prompt_tokens": 12, "completion_tokens": 8, "total_tokens": 20},
    }


def _chat_sse_stream() -> bytes:
    chunks = [
        {
            "id": "chatcmpl-runinfra-stream-demo",
            "object": "chat.completion.chunk",
            "created": 1700000000,
            "model": _MODEL,
            "choices": [
                {
                    "index": 0,
                    "delta": {"role": "assistant", "content": "Verification"},
                    "finish_reason": None,
                }
            ],
        },
        {
            "id": "chatcmpl-runinfra-stream-demo",
            "object": "chat.completion.chunk",
            "created": 1700000000,
            "model": _MODEL,
            "choices": [
                {
                    "index": 0,
                    "delta": {"content": " Stream: Runinfra"},
                    "finish_reason": None,
                }
            ],
        },
        {
            "id": "chatcmpl-runinfra-stream-demo",
            "object": "chat.completion.chunk",
            "created": 1700000000,
            "model": _MODEL,
            "choices": [
                {
                    "index": 0,
                    "delta": {"content": " SSE is functional."},
                    "finish_reason": None,
                }
            ],
        },
        {
            "id": "chatcmpl-runinfra-stream-demo",
            "object": "chat.completion.chunk",
            "created": 1700000000,
            "model": _MODEL,
            "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 12, "completion_tokens": 9, "total_tokens": 21},
        },
    ]
    lines = [f"data: {json.dumps(c)}\n\n" for c in chunks]
    lines.append("data: [DONE]\n\n")
    return "".join(lines).encode("utf-8")


def run_proof() -> int:
    print(
        f"\n{BOLD}{CYAN}================================================================={RESET}"
    )
    print(
        f"{BOLD}{CYAN}      RUNINFRA.AI BACKEND CONNECTOR FUNCTIONAL STATE PROOF      {RESET}"
    )
    print(
        f"{BOLD}{CYAN}================================================================={RESET}\n"
    )

    from starlette.testclient import TestClient

    passed_tests = 0
    total_tests = 6

    respx.start()
    try:
        models_route = respx.get(f"{_BASE}/models")
        models_route.mock(return_value=httpx.Response(200, json=_models_payload()))

        chat_route = respx.post(f"{_BASE}/chat/completions")
        chat_route.mock(return_value=httpx.Response(200, json=_chat_payload()))

        # 1. Initialize Proxy
        print(f"[{BOLD}1/6{RESET}] Initializing Proxy with Runinfra backend...")
        backends_dict = {
            "default_backend": "runinfra",
            "runinfra": BackendConfig(
                api_key="demo-runinfra-key",
                api_url=_BASE,
            ),
        }
        backends = BackendSettings.model_validate(backends_dict)
        config = AppConfig(backends=backends, auth=AuthConfig(disable_auth=True))

        builder = ApplicationTestBuilder()
        builder.add_stage(CoreServicesStage())
        builder.add_stage(InfrastructureStage())
        builder.add_stage(BackendStage())
        builder.add_stage(CommandStage())
        builder.add_stage(ProcessorStage())
        builder.add_stage(ControllerStage())

        app = asyncio.run(builder.build(config))
        with TestClient(app) as client:
            # 2. Proof 1: Models Discovery
            print(f"\n[{BOLD}1/6{RESET}] Testing /v1/models endpoint discovery...")
            res = client.get("/v1/models")
            assert res.status_code == 200, f"Models discovery failed: {res.text}"
            data = res.json()
            models = [m["id"] for m in data.get("data", [])]
            print(f"  Status: {res.status_code} OK")
            print(
                f"  Discovered Runinfra Models: {[m for m in models if m.startswith('runinfra/')]}"
            )
            assert _PROXY_MODEL in models
            print(
                f"  {GREEN}[PASS]{RESET} Models endpoint accurately advertises runinfra models."
            )
            passed_tests += 1

            # 3. Proof 2: OpenAI Non-Streaming Chat Completions
            print(
                f"\n[{BOLD}2/6{RESET}] Testing OpenAI Chat Completions (Non-Streaming)..."
            )
            chat_route.mock(return_value=httpx.Response(200, json=_chat_payload()))
            res = client.post(
                "/v1/chat/completions",
                json={
                    "model": _PROXY_MODEL,
                    "messages": [{"role": "user", "content": "Ping"}],
                    "stream": False,
                },
            )
            assert res.status_code == 200, f"Non-streaming failed: {res.text}"
            chat_data = res.json()
            content = chat_data["choices"][0]["message"]["content"]
            print(f"  Status: {res.status_code} OK")
            print(f'  Response: "{content}"')
            # Check outbound stripped model name
            upstream_req = respx.calls.last.request
            req_body = json.loads(upstream_req.content.decode("utf-8"))
            assert req_body["model"] == _MODEL
            assert upstream_req.headers["authorization"] == "Bearer demo-runinfra-key"
            print(f"  Outbound Model: \"{req_body['model']}\" (prefix correctly stripped)")
            print(f"  Outbound Auth: \"{upstream_req.headers['authorization'][:18]}...\"")
            print(
                f"  {GREEN}[PASS]{RESET} Non-streaming chat completions & outbound transformation verified."
            )
            passed_tests += 1

            # 4. Proof 3: OpenAI Streaming Chat Completions
            print(
                f"\n[{BOLD}3/6{RESET}] Testing OpenAI Chat Completions (Streaming SSE)..."
            )
            chat_route.mock(
                return_value=httpx.Response(
                    200,
                    headers={"content-type": "text/event-stream"},
                    content=_chat_sse_stream(),
                )
            )
            res = client.post(
                "/v1/chat/completions",
                json={
                    "model": _PROXY_MODEL,
                    "messages": [{"role": "user", "content": "Stream ping"}],
                    "stream": True,
                },
            )
            assert res.status_code == 200, f"Streaming failed: {res.text}"
            stream_chunks = []
            for line in res.text.splitlines():
                if line.startswith("data: ") and line != "data: [DONE]":
                    c = json.loads(line[6:])
                    delta = c["choices"][0].get("delta", {}).get("content")
                    if delta:
                        stream_chunks.append(delta)
            assembled = "".join(stream_chunks)
            print(f"  Status: {res.status_code} OK")
            print(f'  Assembled Stream: "{assembled}"')
            assert "Runinfra" in assembled
            print(f"  {GREEN}[PASS]{RESET} SSE Streaming protocol verified.")
            passed_tests += 1

            # 5. Proof 4: Anthropic Messages Frontend (Non-Streaming)
            print(
                f"\n[{BOLD}4/6{RESET}] Testing Anthropic Messages Cross-API Translation (Non-Streaming)..."
            )
            chat_route.mock(return_value=httpx.Response(200, json=_chat_payload()))
            res = client.post(
                "/anthropic/v1/messages",
                json={
                    "model": _PROXY_MODEL,
                    "messages": [{"role": "user", "content": "Anthropic format ping"}],
                    "max_tokens": 100,
                    "stream": False,
                },
                headers={"anthropic-version": "2023-06-01"},
            )
            assert res.status_code == 200, f"Anthropic translation failed: {res.text}"
            anthropic_data = res.json()
            print(f"  Status: {res.status_code} OK")
            print(f"  Type: {anthropic_data.get('type')}")
            print(f"  Content: \"{anthropic_data['content'][0]['text']}\"")
            assert anthropic_data.get("type") == "message"
            print(
                f"  {GREEN}[PASS]{RESET} Anthropic Messages to Runinfra translation verified."
            )
            passed_tests += 1

            # 6. Proof 5: Anthropic Messages Frontend (Streaming)
            print(
                f"\n[{BOLD}5/6{RESET}] Testing Anthropic Messages Cross-API Translation (Streaming)..."
            )
            chat_route.mock(
                return_value=httpx.Response(
                    200,
                    headers={"content-type": "text/event-stream"},
                    content=_chat_sse_stream(),
                )
            )
            res = client.post(
                "/anthropic/v1/messages",
                json={
                    "model": _PROXY_MODEL,
                    "messages": [{"role": "user", "content": "Anthropic stream"}],
                    "max_tokens": 100,
                    "stream": True,
                },
                headers={"anthropic-version": "2023-06-01"},
            )
            assert res.status_code == 200, f"Anthropic streaming failed: {res.text}"
            event_types = [
                line.split(": ", 1)[1].strip()
                for line in res.text.splitlines()
                if line.startswith("event: ")
            ]
            print(f"  Status: {res.status_code} OK")
            print(f"  Observed Anthropic SSE Events: {event_types}")
            assert "message_start" in event_types
            assert "content_block_delta" in event_types
            assert "message_stop" in event_types
            print(f"  {GREEN}[PASS]{RESET} Anthropic streaming translation verified.")
            passed_tests += 1

            # 7. Proof 6: Gemini generateContent Cross-API Translation
            print(
                f"\n[{BOLD}6/6{RESET}] Testing Gemini generateContent Cross-API Translation..."
            )
            chat_route.mock(return_value=httpx.Response(200, json=_chat_payload()))
            res = client.post(
                "/v1beta/models/test-model:generateContent",
                json={
                    "model": _PROXY_MODEL,
                    "contents": [
                        {
                            "role": "user",
                            "parts": [{"text": "Gemini format ping"}],
                        }
                    ],
                },
            )
            assert res.status_code == 200, f"Gemini translation failed: {res.text}"
            gemini_data = res.json()
            print(f"  Status: {res.status_code} OK")
            print(f"  Candidates count: {len(gemini_data.get('candidates', []))}")
            print(
                f"  Candidate text: \"{gemini_data['candidates'][0]['content']['parts'][0]['text']}\""
            )
            assert "candidates" in gemini_data
            candidate_parts = gemini_data["candidates"][0]["content"]["parts"]
            assert len(candidate_parts) > 0 and len(candidate_parts[0].get("text", "")) > 0
            print(f"  {GREEN}[PASS]{RESET} Gemini generateContent translation verified.")
            passed_tests += 1

        print(
            f"\n{BOLD}{GREEN}================================================================={RESET}"
        )
        print(
            f"{BOLD}{GREEN}   PROVEN: {passed_tests}/{total_tests} FUNCTIONAL TESTS PASSED SUCCESSFULLY!   {RESET}"
        )
        print(
            f"{BOLD}{GREEN}================================================================={RESET}\n"
        )
        return 0
    finally:
        respx.stop()


def main() -> None:
    raise SystemExit(run_proof())


if __name__ == "__main__":
    main()
