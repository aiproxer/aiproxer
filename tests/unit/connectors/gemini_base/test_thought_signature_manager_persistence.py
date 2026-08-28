from __future__ import annotations

from src.connectors.gemini_base.thought_signature_manager import ThoughtSignatureManager
from src.core.domain.chat import ChatMessage, FunctionCall, ToolCall


def test_thought_signature_persists_across_restarts(tmp_path, monkeypatch) -> None:
    persist_path = tmp_path / "thought_signatures.json"
    monkeypatch.setenv("LLM_PROXY_THOUGHT_SIGNATURE_PERSIST_PATH", str(persist_path))

    manager = ThoughtSignatureManager(ttl_seconds=86400)
    manager.store_signatures_from_tool_calls(
        [
            {
                "id": "t1",
                "extra_content": {"google": {"thought_signature": "sig-123"}},
            }
        ],
        session_id="s1",
    )

    # Give the background persistence thread time to write the file.
    import time

    for _ in range(50):
        if persist_path.exists():
            break
        time.sleep(0.1)

    # Simulate process restart with a new manager instance.
    manager2 = ThoughtSignatureManager(ttl_seconds=86400)
    msg = ChatMessage(
        role="assistant",
        content="",
        tool_calls=[ToolCall(id="t1", function=FunctionCall(name="x", arguments="{}"))],
    )

    request = type("Req", (), {"messages": [msg]})()
    manager2.inject_signatures(request, session_id="s2")

    injected = msg.tool_calls[0].extra_content  # type: ignore[index]
    assert isinstance(injected, dict)
    assert injected.get("google", {}).get("thought_signature") == "sig-123"


def test_namespaced_signature_persists_with_colon_namespace(
    tmp_path, monkeypatch
) -> None:
    monkeypatch.setenv("LLM_PROXY_THOUGHT_SIGNATURE_PERSIST_PATH", str(tmp_path))
    monkeypatch.setenv("LLM_PROXY_THOUGHT_SIGNATURE_PERSIST_NAMESPACED", "1")

    session_id = "sess-1|gemini-oauth-auto:acct-1"

    manager = ThoughtSignatureManager(ttl_seconds=86400)
    manager.store_signatures_from_tool_calls(
        [
            {
                "id": "t-ns",
                "extra_content": {"google": {"thought_signature": "sig-ns"}},
            }
        ],
        session_id=session_id,
    )

    # Give the background persistence thread time to write the file.
    import time

    for _ in range(50):
        # For namespaced persistence, we don't know the exact filename easily here,
        # but we can check if any json file exists in the directory.
        if any(tmp_path.glob("*.json")):
            break
        time.sleep(0.1)

    manager2 = ThoughtSignatureManager(ttl_seconds=86400)
    assert manager2.get_cached_signature(session_id, "t-ns") == "sig-ns"
