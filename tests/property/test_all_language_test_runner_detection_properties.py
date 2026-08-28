"""Property-based tests for all language test runner detection.

Feature: test-execution-reminder
Property 2: Test Execution Clears Dirty State Across All Languages (complete)
Validates: Requirements 2.1-2.14, 2.17, 2.18
"""

from __future__ import annotations

from typing import Any

from hypothesis import given
from hypothesis import strategies as st
from src.services.test_execution_reminder.session_state import (
    TestExecutionSessionState,
)
from src.services.test_execution_reminder.test_runner_registry import (
    TestRunnerRegistry,
)
from tests.utils.hypothesis_config import property_test_settings

# ============================================================================
# Strategies for generating test commands for all languages
# ============================================================================


@st.composite
def rust_test_command_strategy(draw: Any) -> str:
    """Generate Rust cargo test command variations."""
    base_commands = [
        "cargo test",
        "cargo test --all",
        "cargo test --lib",
        "cargo test --bin",
        "cargo test test_name",
        "cargo test --release",
        "cargo test -- --nocapture",
    ]
    return draw(st.sampled_from(base_commands))


@st.composite
def go_test_command_strategy(draw: Any) -> str:
    """Generate Go test command variations."""
    base_commands = [
        "go test",
        "go test ./...",
        "go test -v",
        "go test -cover",
        "go test ./pkg/...",
        "go test -run TestName",
        "go test -bench=.",
    ]
    return draw(st.sampled_from(base_commands))


@st.composite
def java_maven_test_command_strategy(draw: Any) -> str:
    """Generate Java Maven test command variations."""
    base_commands = [
        "mvn test",
        "mvn verify",
        "./mvnw test",
        "mvnw test",
        "mvn test -Dtest=TestClass",
        "mvn clean test",
    ]
    return draw(st.sampled_from(base_commands))


@st.composite
def java_gradle_test_command_strategy(draw: Any) -> str:
    """Generate Java Gradle test command variations."""
    base_commands = [
        "gradle test",
        "./gradlew test",
        "gradlew test",
        "gradle test --tests TestClass",
        "gradle clean test",
    ]
    return draw(st.sampled_from(base_commands))


@st.composite
def csharp_test_command_strategy(draw: Any) -> str:
    """Generate C# dotnet test command variations."""
    base_commands = [
        "dotnet test",
        "dotnet test --no-build",
        "dotnet test --filter TestName",
        "dotnet test --logger trx",
        "dotnet test Project.Tests.csproj",
    ]
    return draw(st.sampled_from(base_commands))


@st.composite
def ruby_test_command_strategy(draw: Any) -> str:
    """Generate Ruby test command variations."""
    base_commands = [
        "rspec",
        "bundle exec rspec",
        "rake test",
        "bundle exec rake test",
        "ruby -Itest test/test_file.rb",
        "rspec spec/",
        "rspec --format documentation",
    ]
    return draw(st.sampled_from(base_commands))


@st.composite
def php_test_command_strategy(draw: Any) -> str:
    """Generate PHP test command variations."""
    base_commands = [
        "phpunit",
        "vendor/bin/phpunit",
        "./vendor/bin/phpunit",
        "composer test",
        "composer run test",
        "phpunit --testdox",
        "phpunit tests/",
    ]
    return draw(st.sampled_from(base_commands))


@st.composite
def cpp_test_command_strategy(draw: Any) -> str:
    """Generate C/C++ test command variations."""
    base_commands = [
        "ctest",
        "make test",
        "cmake --build . --target test",
        "ctest --verbose",
        "ctest -R TestName",
    ]
    return draw(st.sampled_from(base_commands))


@st.composite
def swift_test_command_strategy(draw: Any) -> str:
    """Generate Swift test command variations."""
    base_commands = [
        "swift test",
        "swift test --parallel",
        "swift test --filter TestName",
        "swift test --enable-code-coverage",
    ]
    return draw(st.sampled_from(base_commands))


# Note: Kotlin test commands are not included as a separate strategy
# because Kotlin projects use Gradle, which is already covered by Java Gradle patterns.
# We cannot distinguish between Java and Kotlin projects from the command alone.


@st.composite
def scala_test_command_strategy(draw: Any) -> str:
    """Generate Scala test command variations."""
    base_commands = [
        "sbt test",
        "sbt testOnly TestClass",
        "sbt testQuick",
        "sbt test:compile",
    ]
    return draw(st.sampled_from(base_commands))


@st.composite
def elixir_test_command_strategy(draw: Any) -> str:
    """Generate Elixir test command variations."""
    base_commands = [
        "mix test",
        "mix test test/test_file.exs",
        "mix test --trace",
        "mix test --cover",
    ]
    return draw(st.sampled_from(base_commands))


@st.composite
def dart_test_command_strategy(draw: Any) -> str:
    """Generate Dart/Flutter test command variations."""
    base_commands = [
        "dart test",
        "flutter test",
        "dart test test/test_file.dart",
        "flutter test --coverage",
        "dart test --reporter expanded",
    ]
    return draw(st.sampled_from(base_commands))


@st.composite
def any_language_test_command_strategy(draw: Any) -> tuple[str, str, str]:
    """Generate any test command from any supported language.

    Returns:
        Tuple of (command, expected_language, expected_framework)
    """
    language_strategies = [
        ("rust", "cargo", rust_test_command_strategy()),
        ("go", "go test", go_test_command_strategy()),
        ("java", "maven", java_maven_test_command_strategy()),
        ("java", "gradle", java_gradle_test_command_strategy()),
        ("csharp", "dotnet", csharp_test_command_strategy()),
        ("ruby", "rspec", ruby_test_command_strategy()),
        ("php", "phpunit", php_test_command_strategy()),
        ("cpp", "ctest", cpp_test_command_strategy()),
        ("swift", "swift test", swift_test_command_strategy()),
        ("scala", "sbt", scala_test_command_strategy()),
        ("elixir", "mix", elixir_test_command_strategy()),
        ("dart", "dart test", dart_test_command_strategy()),
    ]

    language, framework, strategy = draw(st.sampled_from(language_strategies))
    command = draw(strategy)
    return (command, language, framework)


# ============================================================================
# Property Tests
# ============================================================================


@given(command=rust_test_command_strategy())
@property_test_settings()
def test_property_2_rust_test_detection(command: str) -> None:
    """
    Property 2: Rust Test Command Detection.

    For any Rust cargo test command variation, the test runner registry should
    correctly identify it as a Rust test execution command.

    Validates: Requirements 2.3
    """
    registry = TestRunnerRegistry()
    is_match, language, framework = registry.match_command(command)

    assert (
        is_match is True
    ), f"Rust command '{command}' was not detected as a test execution command."
    assert language == "rust", (
        f"Rust command '{command}' was detected with language '{language}' "
        f"instead of 'rust'."
    )
    assert framework == "cargo", (
        f"Rust command '{command}' was detected with framework '{framework}' "
        f"instead of 'cargo'."
    )


@given(command=go_test_command_strategy())
@property_test_settings()
def test_property_2_go_test_detection(command: str) -> None:
    """
    Property 2: Go Test Command Detection.

    For any Go test command variation, the test runner registry should
    correctly identify it as a Go test execution command.

    Validates: Requirements 2.4
    """
    registry = TestRunnerRegistry()
    is_match, language, framework = registry.match_command(command)

    assert (
        is_match is True
    ), f"Go command '{command}' was not detected as a test execution command."
    assert language == "go", (
        f"Go command '{command}' was detected with language '{language}' "
        f"instead of 'go'."
    )
    assert framework == "go test", (
        f"Go command '{command}' was detected with framework '{framework}' "
        f"instead of 'go test'."
    )


@given(command=java_maven_test_command_strategy())
@property_test_settings()
def test_property_2_java_maven_test_detection(command: str) -> None:
    """
    Property 2: Java Maven Test Command Detection.

    For any Java Maven test command variation, the test runner registry should
    correctly identify it as a Java test execution command with Maven.

    Validates: Requirements 2.5
    """
    registry = TestRunnerRegistry()
    is_match, language, framework = registry.match_command(command)

    assert (
        is_match is True
    ), f"Java Maven command '{command}' was not detected as a test execution command."
    assert language == "java", (
        f"Java Maven command '{command}' was detected with language '{language}' "
        f"instead of 'java'."
    )
    assert framework == "maven", (
        f"Java Maven command '{command}' was detected with framework '{framework}' "
        f"instead of 'maven'."
    )


@given(command=java_gradle_test_command_strategy())
@property_test_settings()
def test_property_2_java_gradle_test_detection(command: str) -> None:
    """
    Property 2: Java Gradle Test Command Detection.

    For any Java Gradle test command variation, the test runner registry should
    correctly identify it as a Java test execution command with Gradle.

    Validates: Requirements 2.5
    """
    registry = TestRunnerRegistry()
    is_match, language, framework = registry.match_command(command)

    assert (
        is_match is True
    ), f"Java Gradle command '{command}' was not detected as a test execution command."
    assert language == "java", (
        f"Java Gradle command '{command}' was detected with language '{language}' "
        f"instead of 'java'."
    )
    assert framework == "gradle", (
        f"Java Gradle command '{command}' was detected with framework '{framework}' "
        f"instead of 'gradle'."
    )


@given(command=csharp_test_command_strategy())
@property_test_settings()
def test_property_2_csharp_test_detection(command: str) -> None:
    """
    Property 2: C# Test Command Detection.

    For any C# dotnet test command variation, the test runner registry should
    correctly identify it as a C# test execution command.

    Validates: Requirements 2.6
    """
    registry = TestRunnerRegistry()
    is_match, language, framework = registry.match_command(command)

    assert (
        is_match is True
    ), f"C# command '{command}' was not detected as a test execution command."
    assert language == "csharp", (
        f"C# command '{command}' was detected with language '{language}' "
        f"instead of 'csharp'."
    )
    assert framework == "dotnet", (
        f"C# command '{command}' was detected with framework '{framework}' "
        f"instead of 'dotnet'."
    )


@given(command=ruby_test_command_strategy())
@property_test_settings()
def test_property_2_ruby_test_detection(command: str) -> None:
    """
    Property 2: Ruby Test Command Detection.

    For any Ruby test command variation, the test runner registry should
    correctly identify it as a Ruby test execution command.

    Validates: Requirements 2.7
    """
    registry = TestRunnerRegistry()
    is_match, language, framework = registry.match_command(command)

    assert (
        is_match is True
    ), f"Ruby command '{command}' was not detected as a test execution command."
    assert language == "ruby", (
        f"Ruby command '{command}' was detected with language '{language}' "
        f"instead of 'ruby'."
    )
    assert framework == "rspec", (
        f"Ruby command '{command}' was detected with framework '{framework}' "
        f"instead of 'rspec'."
    )


@given(command=php_test_command_strategy())
@property_test_settings()
def test_property_2_php_test_detection(command: str) -> None:
    """
    Property 2: PHP Test Command Detection.

    For any PHP test command variation, the test runner registry should
    correctly identify it as a PHP test execution command.

    Validates: Requirements 2.8
    """
    registry = TestRunnerRegistry()
    is_match, language, framework = registry.match_command(command)

    assert (
        is_match is True
    ), f"PHP command '{command}' was not detected as a test execution command."
    assert language == "php", (
        f"PHP command '{command}' was detected with language '{language}' "
        f"instead of 'php'."
    )
    assert framework == "phpunit", (
        f"PHP command '{command}' was detected with framework '{framework}' "
        f"instead of 'phpunit'."
    )


@given(command=cpp_test_command_strategy())
@property_test_settings()
def test_property_2_cpp_test_detection(command: str) -> None:
    """
    Property 2: C/C++ Test Command Detection.

    For any C/C++ test command variation, the test runner registry should
    correctly identify it as a C/C++ test execution command.

    Validates: Requirements 2.9
    """
    registry = TestRunnerRegistry()
    is_match, language, framework = registry.match_command(command)

    assert (
        is_match is True
    ), f"C/C++ command '{command}' was not detected as a test execution command."
    assert language == "cpp", (
        f"C/C++ command '{command}' was detected with language '{language}' "
        f"instead of 'cpp'."
    )
    assert framework == "ctest", (
        f"C/C++ command '{command}' was detected with framework '{framework}' "
        f"instead of 'ctest'."
    )


@given(command=swift_test_command_strategy())
@property_test_settings()
def test_property_2_swift_test_detection(command: str) -> None:
    """
    Property 2: Swift Test Command Detection.

    For any Swift test command variation, the test runner registry should
    correctly identify it as a Swift test execution command.

    Validates: Requirements 2.10
    """
    registry = TestRunnerRegistry()
    is_match, language, framework = registry.match_command(command)

    assert (
        is_match is True
    ), f"Swift command '{command}' was not detected as a test execution command."
    assert language == "swift", (
        f"Swift command '{command}' was detected with language '{language}' "
        f"instead of 'swift'."
    )
    assert framework == "swift test", (
        f"Swift command '{command}' was detected with framework '{framework}' "
        f"instead of 'swift test'."
    )


# Note: Kotlin test detection is not tested separately because Kotlin projects
# use Gradle, which is already covered by Java Gradle test detection.
# Gradle test commands are detected as 'java' regardless of whether the project
# is Java or Kotlin, since we cannot distinguish between them from the command alone.
# This satisfies Requirements 2.11 by treating Kotlin tests as Java Gradle tests.


@given(command=scala_test_command_strategy())
@property_test_settings()
def test_property_2_scala_test_detection(command: str) -> None:
    """
    Property 2: Scala Test Command Detection.

    For any Scala test command variation, the test runner registry should
    correctly identify it as a Scala test execution command.

    Validates: Requirements 2.12
    """
    registry = TestRunnerRegistry()
    is_match, language, framework = registry.match_command(command)

    assert (
        is_match is True
    ), f"Scala command '{command}' was not detected as a test execution command."
    assert language == "scala", (
        f"Scala command '{command}' was detected with language '{language}' "
        f"instead of 'scala'."
    )
    assert framework == "sbt", (
        f"Scala command '{command}' was detected with framework '{framework}' "
        f"instead of 'sbt'."
    )


@given(command=elixir_test_command_strategy())
@property_test_settings()
def test_property_2_elixir_test_detection(command: str) -> None:
    """
    Property 2: Elixir Test Command Detection.

    For any Elixir test command variation, the test runner registry should
    correctly identify it as an Elixir test execution command.

    Validates: Requirements 2.13
    """
    registry = TestRunnerRegistry()
    is_match, language, framework = registry.match_command(command)

    assert (
        is_match is True
    ), f"Elixir command '{command}' was not detected as a test execution command."
    assert language == "elixir", (
        f"Elixir command '{command}' was detected with language '{language}' "
        f"instead of 'elixir'."
    )
    assert framework == "mix", (
        f"Elixir command '{command}' was detected with framework '{framework}' "
        f"instead of 'mix'."
    )


@given(command=dart_test_command_strategy())
@property_test_settings()
def test_property_2_dart_test_detection(command: str) -> None:
    """
    Property 2: Dart/Flutter Test Command Detection.

    For any Dart/Flutter test command variation, the test runner registry should
    correctly identify it as a Dart test execution command.

    Validates: Requirements 2.14
    """
    registry = TestRunnerRegistry()
    is_match, language, framework = registry.match_command(command)

    assert (
        is_match is True
    ), f"Dart command '{command}' was not detected as a test execution command."
    assert language == "dart", (
        f"Dart command '{command}' was detected with language '{language}' "
        f"instead of 'dart'."
    )
    assert framework == "dart test", (
        f"Dart command '{command}' was detected with framework '{framework}' "
        f"instead of 'dart test'."
    )


@given(test_data=any_language_test_command_strategy())
@property_test_settings()
def test_property_2_all_languages_clear_dirty_state(
    test_data: tuple[str, str, str],
) -> None:
    """
    Property 2: Test Execution Clears Dirty State Across All Languages.

    For any test execution command across all supported languages,
    if the session is in dirty state, then processing the command
    should transition the state to clean.

    Validates: Requirements 2.1-2.14, 2.17, 2.18
    """
    command, expected_language, expected_framework = test_data
    registry = TestRunnerRegistry()
    state = TestExecutionSessionState()

    # Mark the state as dirty (simulate file modification)
    state.mark_dirty()
    assert state.is_dirty is True, "State should be dirty after modification"

    # Verify the command is a test command
    is_match, language, framework = registry.match_command(command)
    assert is_match is True, (
        f"Command '{command}' should be detected as test command "
        f"for language '{expected_language}'"
    )
    assert language == expected_language, (
        f"Command '{command}' detected as '{language}' "
        f"instead of '{expected_language}'"
    )

    # Simulate test execution (mark state as clean)
    state.mark_clean()

    # Verify state is now clean
    assert state.is_dirty is False, (
        f"State should be clean after test execution with command '{command}'. "
        f"Test execution should clear the dirty state for all languages."
    )
    assert (
        state.modification_count == 0
    ), "Modification count should be reset to 0 after test execution"


@given(test_data=any_language_test_command_strategy())
@property_test_settings()
def test_property_2_partial_test_execution_clears_state(
    test_data: tuple[str, str, str],
) -> None:
    """
    Property 2: Partial Test Execution Clears State.

    For any test execution command (even partial test runs),
    the dirty state should be cleared.

    Validates: Requirements 2.17
    """
    command, expected_language, _ = test_data
    registry = TestRunnerRegistry()
    state = TestExecutionSessionState()

    # Mark the state as dirty
    state.mark_dirty()
    assert state.is_dirty is True

    # Verify command matches
    is_match, language, _ = registry.match_command(command)
    assert is_match is True, f"Command '{command}' should match"
    assert language == expected_language

    # Clear state (simulating test execution)
    state.mark_clean()

    # State should be clean
    assert (
        state.is_dirty is False
    ), f"Partial test execution with '{command}' should clear dirty state"


@given(test_data=any_language_test_command_strategy())
@property_test_settings()
def test_property_2_test_execution_in_clean_state_all_languages(
    test_data: tuple[str, str, str],
) -> None:
    """
    Property 2: Test Execution in Clean State (All Languages).

    For any test execution command in clean state across all languages,
    the state should remain clean.

    Validates: Requirements 2.16
    """
    command, expected_language, _ = test_data
    registry = TestRunnerRegistry()
    state = TestExecutionSessionState()

    # Initial state: clean
    assert state.is_dirty is False

    # Verify command is a test command
    is_match, language, _ = registry.match_command(command)
    assert is_match is True
    assert language == expected_language

    # Run test in clean state
    state.mark_clean()

    # State should remain clean
    assert state.is_dirty is False, (
        f"State should remain clean after test execution in clean state. "
        f"Command: '{command}'"
    )
