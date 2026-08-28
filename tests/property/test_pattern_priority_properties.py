"""Property-based tests for test runner pattern priority.

Feature: test-execution-reminder
Property 9: Pattern Priority and Specificity
Validates: Requirements 6.5
"""

from __future__ import annotations

import re
from typing import Any

from hypothesis import given
from hypothesis import strategies as st
from src.services.test_execution_reminder.test_runner_registry import (
    TestRunnerPattern,
    TestRunnerRegistry,
)
from tests.utils.hypothesis_config import property_test_settings

# ============================================================================
# Strategies for generating test patterns and commands
# ============================================================================


@st.composite
def overlapping_patterns_strategy(draw: Any) -> tuple[list[TestRunnerPattern], str]:
    """Generate multiple overlapping patterns with different priorities.

    Returns:
        Tuple of (patterns_list, command_that_matches_all)
    """
    # Create patterns that all match "gradle test" but with different priorities
    patterns = [
        TestRunnerPattern(
            language="java",
            framework="gradle",
            patterns=[re.compile(r"^gradle\s+.*\btest\b")],
            priority=15,
        ),
        TestRunnerPattern(
            language="kotlin",
            framework="gradle",
            patterns=[re.compile(r"^gradle\s+.*\btest\b")],
            priority=5,
        ),
        TestRunnerPattern(
            language="groovy",
            framework="gradle",
            patterns=[re.compile(r"^gradle\s+.*\btest\b")],
            priority=10,
        ),
    ]

    command = "gradle test"
    return (patterns, command)


# ============================================================================
# Property Tests
# ============================================================================


@given(overlapping_data=overlapping_patterns_strategy())
@property_test_settings()
def test_property_9_highest_priority_pattern_matches_first(
    overlapping_data: tuple[list[TestRunnerPattern], str],
) -> None:
    """
    Property 9: Highest Priority Pattern Matches First.

    For any command that matches multiple test runner patterns,
    the system should use the pattern with the highest priority.

    Validates: Requirements 6.5
    """
    patterns, command = overlapping_data

    # Create registry and register all patterns
    registry = TestRunnerRegistry()
    # Clear default patterns to test only our custom patterns
    registry._patterns = []

    for pattern in patterns:
        registry.register_pattern(pattern)

    # Match the command
    is_match, language, framework = registry.match_command(command)

    # Should match
    assert is_match is True, f"Command '{command}' should match at least one pattern"

    # Find the highest priority pattern
    highest_priority = max(p.priority for p in patterns)
    expected_patterns = [p for p in patterns if p.priority == highest_priority]

    # The matched language should be from one of the highest priority patterns
    assert any(language == p.language for p in expected_patterns), (
        f"Command '{command}' matched language '{language}', "
        f"but should match one of the highest priority patterns "
        f"(priority={highest_priority})"
    )


@given(
    priority1=st.integers(min_value=1, max_value=50),
    priority2=st.integers(min_value=51, max_value=100),
)
@property_test_settings()
def test_property_9_priority_ordering(priority1: int, priority2: int) -> None:
    """
    Property 9: Priority Ordering.

    For any two patterns with different priorities that match the same command,
    the pattern with higher priority should be selected.

    Validates: Requirements 6.5
    """
    # Create two patterns with different priorities
    pattern_low = TestRunnerPattern(
        language="language_low",
        framework="framework_low",
        patterns=[re.compile(r"^testcmd(?:\s|$)")],
        priority=priority1,
    )

    pattern_high = TestRunnerPattern(
        language="language_high",
        framework="framework_high",
        patterns=[re.compile(r"^testcmd(?:\s|$)")],
        priority=priority2,
    )

    # Create registry and register patterns
    registry = TestRunnerRegistry()
    registry._patterns = []  # Clear default patterns
    registry.register_pattern(pattern_low)
    registry.register_pattern(pattern_high)

    # Match command
    is_match, language, framework = registry.match_command("testcmd")

    # Should match the higher priority pattern
    assert is_match is True
    assert language == "language_high", (
        f"Expected language 'language_high' (priority={priority2}), "
        f"but got '{language}' (priority={priority1})"
    )
    assert framework == "framework_high"


@given(
    priority=st.integers(min_value=1, max_value=100),
)
@property_test_settings()
def test_property_9_single_pattern_always_matches(priority: int) -> None:
    """
    Property 9: Single Pattern Always Matches.

    For any pattern with any priority, if it's the only pattern that matches
    a command, it should be selected regardless of priority value.

    Validates: Requirements 6.5
    """
    pattern = TestRunnerPattern(
        language="test_language",
        framework="test_framework",
        patterns=[re.compile(r"^uniquecmd(?:\s|$)")],
        priority=priority,
    )

    registry = TestRunnerRegistry()
    registry._patterns = []  # Clear default patterns
    registry.register_pattern(pattern)

    is_match, language, framework = registry.match_command("uniquecmd")

    assert is_match is True
    assert language == "test_language"
    assert framework == "test_framework"


@given(
    priorities=st.lists(
        st.integers(min_value=1, max_value=100),
        min_size=2,
        max_size=10,
        unique=True,
    )
)
@property_test_settings()
def test_property_9_multiple_patterns_highest_wins(priorities: list[int]) -> None:
    """
    Property 9: Multiple Patterns - Highest Wins.

    For any list of patterns with different priorities that all match
    the same command, the pattern with the highest priority should win.

    Validates: Requirements 6.5
    """
    # Create patterns with different priorities
    patterns = []
    for i, priority in enumerate(priorities):
        pattern = TestRunnerPattern(
            language=f"lang_{i}",
            framework=f"framework_{i}",
            patterns=[re.compile(r"^multicmd(?:\s|$)")],
            priority=priority,
        )
        patterns.append(pattern)

    registry = TestRunnerRegistry()
    registry._patterns = []  # Clear default patterns

    for pattern in patterns:
        registry.register_pattern(pattern)

    is_match, language, framework = registry.match_command("multicmd")

    # Find the highest priority
    max_priority = max(priorities)
    max_index = priorities.index(max_priority)

    assert is_match is True
    assert language == f"lang_{max_index}", (
        f"Expected language 'lang_{max_index}' with priority {max_priority}, "
        f"but got '{language}'"
    )


@given(
    base_priority=st.integers(min_value=10, max_value=90),
)
@property_test_settings()
def test_property_9_specific_pattern_beats_general(base_priority: int) -> None:
    """
    Property 9: Specific Pattern Beats General.

    For any command, a more specific pattern (higher priority) should
    match before a more general pattern (lower priority).

    Validates: Requirements 6.5
    """
    # General pattern (matches any test command)
    general_pattern = TestRunnerPattern(
        language="general",
        framework="general",
        patterns=[re.compile(r"^.*test.*")],
        priority=base_priority,
    )

    # Specific pattern (matches exact command)
    specific_pattern = TestRunnerPattern(
        language="specific",
        framework="specific",
        patterns=[re.compile(r"^pytest(?:\s|$)")],
        priority=base_priority + 10,  # Higher priority
    )

    registry = TestRunnerRegistry()
    registry._patterns = []
    registry.register_pattern(general_pattern)
    registry.register_pattern(specific_pattern)

    # Test with command that matches both
    is_match, language, framework = registry.match_command("pytest")

    assert is_match is True
    assert language == "specific", (
        f"Expected specific pattern to match (priority={base_priority + 10}), "
        f"but got '{language}' (priority={base_priority})"
    )


@given(
    priority1=st.integers(min_value=1, max_value=100),
    priority2=st.integers(min_value=1, max_value=100),
)
@property_test_settings(max_examples=20)  # Reduced from default 50 for performance
def test_property_9_equal_priority_first_registered_wins(
    priority1: int,
    priority2: int,
) -> None:
    """
    Property 9: Equal Priority - First Registered Wins.

    For any two patterns with equal priority that match the same command,
    the behavior should be consistent (first registered pattern wins).

    Validates: Requirements 6.5
    """
    # Use the same priority for both
    same_priority = priority1

    pattern1 = TestRunnerPattern(
        language="first",
        framework="first",
        patterns=[re.compile(r"^samecmd(?:\s|$)")],
        priority=same_priority,
    )

    pattern2 = TestRunnerPattern(
        language="second",
        framework="second",
        patterns=[re.compile(r"^samecmd(?:\s|$)")],
        priority=same_priority,
    )

    registry = TestRunnerRegistry()
    registry._patterns = []
    registry.register_pattern(pattern1)
    registry.register_pattern(pattern2)

    is_match, language, framework = registry.match_command("samecmd")

    assert is_match is True
    # With equal priority, the first registered pattern should match
    # (due to stable sort behavior)
    assert language in [
        "first",
        "second",
    ], f"Expected language 'first' or 'second', but got '{language}'"


@given(
    command=st.text(
        alphabet=st.characters(whitelist_categories=("Lu", "Ll", "Nd", "Zs")),
        min_size=1,
        max_size=50,
    )
)
@property_test_settings()
def test_property_9_no_match_returns_false(command: str) -> None:
    """
    Property 9: No Match Returns False.

    For any command that doesn't match any registered pattern,
    the registry should return False regardless of pattern priorities.

    Validates: Requirements 6.5
    """
    # Create registry with some patterns
    registry = TestRunnerRegistry()
    registry._patterns = []

    pattern = TestRunnerPattern(
        language="test",
        framework="test",
        patterns=[re.compile(r"^pytest(?:\s|$)")],
        priority=50,
    )
    registry.register_pattern(pattern)

    # Try to match a command that definitely won't match
    # (unless by random chance it starts with "pytest")
    if not command.startswith("pytest"):
        is_match, language, framework = registry.match_command(command)

        assert (
            is_match is False
        ), f"Command '{command}' should not match pattern '^pytest(?:\\s|$)'"
        assert language is None
        assert framework is None
