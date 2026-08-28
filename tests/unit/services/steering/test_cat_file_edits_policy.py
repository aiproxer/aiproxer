"""Tests for CatFileEditsSteeringPolicy."""

import pytest
from src.core.interfaces.tool_call_reactor_interface import ToolCallContext
from src.services.steering.policies.cat_file_edits_policy import (
    DEFAULT_STEERING_MESSAGE,
    CatFileEditsSteeringPolicy,
)


@pytest.fixture
def shell_context() -> ToolCallContext:
    return ToolCallContext(
        session_id="s1",
        backend_name="b",
        model_name="m",
        full_response={},
        tool_name="run_terminal_cmd",
        tool_arguments={"command": "cat > out.txt"},
    )


@pytest.mark.asyncio
async def test_disabled_no_steering(shell_context: ToolCallContext) -> None:
    policy = CatFileEditsSteeringPolicy(enabled=False)
    r = await policy.evaluate(shell_context, "cat > out.txt")
    assert r is None


@pytest.mark.asyncio
async def test_enabled_cat_overwrite_steers(shell_context: ToolCallContext) -> None:
    policy = CatFileEditsSteeringPolicy(enabled=True)
    r = await policy.evaluate(shell_context, "cat > out.txt")
    assert r is not None
    assert r.should_block is True
    assert r.message == DEFAULT_STEERING_MESSAGE
    assert r.metadata.get("cat_redirection") == "overwrite"


@pytest.mark.asyncio
async def test_enabled_cat_append_steers(shell_context: ToolCallContext) -> None:
    policy = CatFileEditsSteeringPolicy(enabled=True)
    shell_context.tool_arguments = {"command": "cat >> log.txt"}
    r = await policy.evaluate(shell_context, "cat >> log.txt")
    assert r is not None
    assert r.metadata.get("cat_redirection") == "append"


@pytest.mark.asyncio
async def test_non_shell_tool_no_steering() -> None:
    policy = CatFileEditsSteeringPolicy(enabled=True)
    ctx = ToolCallContext(
        session_id="s1",
        backend_name="b",
        model_name="m",
        full_response={},
        tool_name="read_file",
        tool_arguments={"path": "x"},
    )
    r = await policy.evaluate(ctx, "cat > x")
    assert r is None


@pytest.mark.asyncio
async def test_cat_without_redirection_no_steering(
    shell_context: ToolCallContext,
) -> None:
    policy = CatFileEditsSteeringPolicy(enabled=True)
    r = await policy.evaluate(shell_context, "cat README.md")
    assert r is None


@pytest.mark.asyncio
async def test_custom_message_override(shell_context: ToolCallContext) -> None:
    policy = CatFileEditsSteeringPolicy(enabled=True, message="Use write_file instead.")
    r = await policy.evaluate(shell_context, "cat > out.txt")
    assert r is not None
    assert r.message == "Use write_file instead."


@pytest.mark.asyncio
async def test_word_boundary_avoids_substring_false_positive(
    shell_context: ToolCallContext,
) -> None:
    policy = CatFileEditsSteeringPolicy(enabled=True)
    r = await policy.evaluate(shell_context, "xcat > out.txt")
    assert r is None
