"""
Concurrency regression tests for ToolCallRepairService.

These tests detect the race condition bug that was fixed by making the service stateless.
If the service is ever refactored to use shared mutable state (like _last_tool_snippet),
these tests will fail.
"""

import asyncio
from concurrent.futures import ThreadPoolExecutor

import pytest
from src.core.interfaces.tool_call_repair_service_interface import (
    ToolCallRepairResult,
)
from src.core.services.tool_call_repair_service import ToolCallRepairService

from tests.utils.fake_clock import FakeClockContext


class TestToolCallRepairConcurrency:
    """Tests to detect race conditions in tool call repair."""

    def test_concurrent_xml_tool_call_snippet_isolation(self) -> None:
        """
        Regression test: Verify that concurrent tool call repairs don't interfere.

        This test would FAIL if the service used shared mutable state like _last_tool_snippet.

        The bug scenario:
        1. Thread A calls repair_tool_calls() with tool X
        2. Thread B calls repair_tool_calls() with tool Y
        3. Thread B overwrites shared _last_tool_snippet
        4. Thread A tries to use snippet but gets Thread B's snippet
        5. Result: Incorrect snippet returned

        This test ensures each concurrent call gets its own correct snippet atomically.
        """
        service = ToolCallRepairService()

        # Define different tool calls with unique XML content
        tool_calls = [
            """
            <patch_file>
                <path>src/file1.py</path>
                <patch_content>print("file1")</patch_content>
            </patch_file>
            """,
            """
            <patch_file>
                <path>src/file2.py</path>
                <patch_content>print("file2")</patch_content>
            </patch_file>
            """,
            """
            <use_mcp_tool>
                <tool_name>read_file</tool_name>
                <tool_arguments>
                    <path>src/file3.py</path>
                </tool_arguments>
            </use_mcp_tool>
            """,
            """
            <execute_command>
                <command>pytest test_file4.py</command>
            </execute_command>
            """,
        ]

        def process_tool_call(content: str) -> ToolCallRepairResult:
            """Process a tool call and verify result contains correct snippet."""
            result = service.repair_tool_calls(content)
            assert (
                result is not None
            ), f"Failed to repair tool call for content: {content}"

            # CRITICAL: Verify the snippet matches the input content
            # If there's a race condition, this assertion will fail because
            # the snippet will be from a different concurrent call
            assert result.snippet in content, (
                f"Snippet isolation violated! "
                f"Expected snippet to be in:\n{content}\n"
                f"But got snippet:\n{result.snippet}"
            )

            return result

        # Execute tool calls concurrently using threads
        with ThreadPoolExecutor(max_workers=4) as executor:
            futures = [
                executor.submit(process_tool_call, content) for content in tool_calls
            ]
            results = [future.result() for future in futures]

        # Verify all results are valid
        assert len(results) == 4
        assert all(result is not None for result in results)

        # Verify each result has the correct snippet for its input
        expected_snippets = [
            "<patch_file>",
            "<patch_file>",
            "<use_mcp_tool>",
            "<execute_command>",
        ]

        for i, result in enumerate(results):
            assert (
                expected_snippets[i] in result.snippet
            ), f"Result {i} has wrong snippet"

    def test_concurrent_json_tool_call_snippet_isolation(self) -> None:
        """
        Regression test: Verify concurrent JSON tool call repairs don't interfere.

        Similar to XML test but for JSON-based tool calls.
        """
        service = ToolCallRepairService()

        # Different JSON tool calls
        tool_calls = [
            '{"function_call": {"name": "func1", "arguments": {"id": 1}}}',
            '{"function_call": {"name": "func2", "arguments": {"id": 2}}}',
            '```json\n{"tool": {"name": "func3", "arguments": {"id": 3}}}\n```',
            'TOOL CALL: func4 {"id": 4}',
        ]

        def process_tool_call(content: str) -> ToolCallRepairResult:
            result = service.repair_tool_calls(content)
            assert result is not None

            # Verify snippet is from the correct input
            assert result.snippet in content, f"Snippet mismatch for content: {content}"

            return result

        with ThreadPoolExecutor(max_workers=4) as executor:
            futures = [
                executor.submit(process_tool_call, content) for content in tool_calls
            ]
            results = [future.result() for future in futures]

        assert len(results) == 4
        assert all(result is not None for result in results)

    @pytest.mark.asyncio
    async def test_async_concurrent_snippet_isolation(self) -> None:
        """
        Regression test: Verify async concurrent calls don't interfere.

        Tests the same race condition in an async/await context.
        """
        service = ToolCallRepairService()

        tool_calls = [f"""
            <patch_file>
                <path>async_file{i}.py</path>
                <patch_content>print("async {i}")</patch_content>
            </patch_file>
            """ for i in range(10)]

        async def process_tool_call_async(content: str, index: int) -> None:
            """Process tool call in async context."""
            # Add small random delay to increase chance of race condition
            delay = 0.001 * (index % 3)
            async with FakeClockContext() as clock:
                sleep_task = asyncio.create_task(asyncio.sleep(delay))
                clock.advance(delay)
                await sleep_task

            result = service.repair_tool_calls(content)
            assert result is not None

            # CRITICAL: This will fail if there's a race condition
            assert (
                result.snippet in content
            ), f"Async snippet isolation violated for index {index}"

            # Verify the snippet contains the correct index
            assert (
                f"async_file{index}.py" in result.snippet
                or f"async {index}" in result.snippet
            )

        # Run all async tasks concurrently
        await asyncio.gather(
            *[
                process_tool_call_async(content, i)
                for i, content in enumerate(tool_calls)
            ]
        )

    def test_high_concurrency_stress_test(self) -> None:
        """
        Stress test: Many concurrent calls to detect subtle race conditions.

        Uses many threads to maximize chance of detecting race conditions.
        """
        service = ToolCallRepairService()
        num_calls = 50

        # Create unique tool calls
        tool_calls = [f"""
            <patch_file>
                <path>stress_test_{i}.py</path>
                <patch_content>
                    # Stress test {i}
                    def func_{i}():
                        return {i}
                </patch_content>
            </patch_file>
            """ for i in range(num_calls)]

        results: list[ToolCallRepairResult] = []
        errors: list[str] = []

        def process_and_collect(content: str, index: int) -> None:
            try:
                result = service.repair_tool_calls(content)
                assert result is not None

                # Verify snippet matches input
                if result.snippet not in content:
                    errors.append(
                        f"Index {index}: Snippet not in content. "
                        f"Snippet len={len(result.snippet)}, "
                        f"Content preview={content[:100]}..."
                    )

                # Verify snippet contains the unique marker
                if f"stress_test_{index}.py" not in result.snippet:
                    errors.append(
                        f"Index {index}: Snippet missing unique marker. "
                        f"Snippet: {result.snippet[:100]}..."
                    )

                results.append(result)
            except Exception as e:
                errors.append(f"Index {index}: Exception: {e}")

        # Use many workers to maximize concurrency
        with ThreadPoolExecutor(max_workers=10) as executor:
            futures = [
                executor.submit(process_and_collect, content, i)
                for i, content in enumerate(tool_calls)
            ]
            # Wait for all to complete
            for future in futures:
                future.result()

        # Assert no errors occurred
        assert not errors, "Concurrency errors detected:\n" + "\n".join(errors)
        assert len(results) == num_calls

    def test_snippet_uniqueness_across_similar_tools(self) -> None:
        """
        Regression test: Verify snippets remain unique even for similar tool calls.

        This is particularly important because similar tool calls might trigger
        the race condition more easily if shared state is used.
        """
        service = ToolCallRepairService()

        # Create very similar tool calls that differ only slightly
        similar_calls = [
            "<patch_file><path>file.py</path><patch_content>v1</patch_content></patch_file>",
            "<patch_file><path>file.py</path><patch_content>v2</patch_content></patch_file>",
            "<patch_file><path>file.py</path><patch_content>v3</patch_content></patch_file>",
        ]

        def process_and_verify(content: str, expected_version: str) -> None:
            result = service.repair_tool_calls(content)
            assert result is not None

            # Snippet must match the exact input, not a similar one
            assert (
                result.snippet == content.strip()
            ), f"Expected exact snippet match for {expected_version}"

            # Verify it contains the right version
            assert expected_version in result.snippet

        with ThreadPoolExecutor(max_workers=3) as executor:
            futures = [
                executor.submit(process_and_verify, content, f"v{i+1}")
                for i, content in enumerate(similar_calls)
            ]
            for future in futures:
                future.result()
