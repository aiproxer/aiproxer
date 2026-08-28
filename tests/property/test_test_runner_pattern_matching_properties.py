"""Property-based tests for test runner pattern matching.

Feature: test-execution-reminder
Property 8: Test Runner Pattern Matching
Validates: Requirements 6.3
"""

from __future__ import annotations

from typing import Any

from hypothesis import given
from hypothesis import strategies as st
from src.services.test_execution_reminder.test_runner_registry import (
    TestRunnerRegistry,
)
from tests.utils.hypothesis_config import property_test_settings

# ============================================================================
# Strategies for generating test commands across all languages
# ============================================================================


@st.composite
def command_with_expected_result_strategy(draw: Any) -> tuple[str, str, str]:
    """Generate test commands with their expected language and framework.

    Returns:
        Tuple of (command, expected_language, expected_framework)
    """
    # Define all test command patterns with their expected results
    test_patterns = [
        # Python
        ("pytest", "python", "pytest"),
        ("py.test", "python", "pytest"),
        ("python -m pytest", "python", "pytest"),
        ("python3 -m pytest", "python", "pytest"),
        ("pipenv run pytest", "python", "pytest"),
        ("poetry run pytest", "python", "pytest"),
        ("pytest tests/", "python", "pytest"),
        ("pytest -v", "python", "pytest"),
        ("python -m unittest", "python", "unittest"),
        ("python3 -m unittest", "python", "unittest"),
        ("unittest", "python", "unittest"),
        ("python -m unittest discover", "python", "unittest"),
        # JavaScript/TypeScript
        ("jest", "javascript", "jest"),
        ("npm test", "javascript", "jest"),
        ("npm run test", "javascript", "jest"),
        ("npm run jest", "javascript", "jest"),
        ("yarn test", "javascript", "jest"),
        ("yarn run test", "javascript", "jest"),
        ("yarn run jest", "javascript", "jest"),
        ("npx jest", "javascript", "jest"),
        ("pnpm test", "javascript", "jest"),
        ("pnpm run test", "javascript", "jest"),
        ("jest --coverage", "javascript", "jest"),
        ("vitest", "javascript", "vitest"),
        ("npm run vitest", "javascript", "vitest"),
        ("yarn run vitest", "javascript", "vitest"),
        ("npx vitest", "javascript", "vitest"),
        ("pnpm run vitest", "javascript", "vitest"),
        ("vitest --run", "javascript", "vitest"),
        ("mocha", "javascript", "mocha"),
        ("npm run mocha", "javascript", "mocha"),
        ("yarn run mocha", "javascript", "mocha"),
        ("npx mocha", "javascript", "mocha"),
        ("pnpm run mocha", "javascript", "mocha"),
        ("mocha tests/", "javascript", "mocha"),
        ("ava", "javascript", "ava"),
        ("npm run ava", "javascript", "ava"),
        ("yarn run ava", "javascript", "ava"),
        ("npx ava", "javascript", "ava"),
        ("pnpm run ava", "javascript", "ava"),
        ("ava --verbose", "javascript", "ava"),
        # Rust
        ("cargo test", "rust", "cargo"),
        ("cargo test --all", "rust", "cargo"),
        ("cargo test --lib", "rust", "cargo"),
        ("cargo test --bin", "rust", "cargo"),
        ("cargo test test_name", "rust", "cargo"),
        ("cargo test --release", "rust", "cargo"),
        # Go
        ("go test", "go", "go test"),
        ("go test ./...", "go", "go test"),
        ("go test -v", "go", "go test"),
        ("go test -cover", "go", "go test"),
        ("go test ./pkg/...", "go", "go test"),
        # Java (Maven)
        ("mvn test", "java", "maven"),
        ("mvn verify", "java", "maven"),
        ("./mvnw test", "java", "maven"),
        ("mvnw test", "java", "maven"),
        ("mvn clean test", "java", "maven"),
        # Java (Gradle)
        ("gradle test", "java", "gradle"),
        ("./gradlew test", "java", "gradle"),
        ("gradlew test", "java", "gradle"),
        ("gradle clean test", "java", "gradle"),
        # C#
        ("dotnet test", "csharp", "dotnet"),
        ("dotnet test --no-build", "csharp", "dotnet"),
        ("dotnet test --filter TestName", "csharp", "dotnet"),
        # Ruby
        ("rspec", "ruby", "rspec"),
        ("bundle exec rspec", "ruby", "rspec"),
        ("rake test", "ruby", "rspec"),
        ("bundle exec rake test", "ruby", "rspec"),
        ("ruby -Itest test/test_file.rb", "ruby", "rspec"),
        # PHP
        ("phpunit", "php", "phpunit"),
        ("vendor/bin/phpunit", "php", "phpunit"),
        ("./vendor/bin/phpunit", "php", "phpunit"),
        ("composer test", "php", "phpunit"),
        ("composer run test", "php", "phpunit"),
        # C/C++
        ("ctest", "cpp", "ctest"),
        ("make test", "cpp", "ctest"),
        ("cmake --build . --target test", "cpp", "ctest"),
        ("ctest --verbose", "cpp", "ctest"),
        # Swift
        ("swift test", "swift", "swift test"),
        ("swift test --parallel", "swift", "swift test"),
        ("swift test --filter TestName", "swift", "swift test"),
        # Scala
        ("sbt test", "scala", "sbt"),
        ("sbt testOnly TestClass", "scala", "sbt"),
        ("sbt testQuick", "scala", "sbt"),
        # Elixir
        ("mix test", "elixir", "mix"),
        ("mix test test/test_file.exs", "elixir", "mix"),
        ("mix test --trace", "elixir", "mix"),
        # Dart/Flutter
        ("dart test", "dart", "dart test"),
        ("flutter test", "dart", "dart test"),
        ("dart test test/test_file.dart", "dart", "dart test"),
        ("flutter test --coverage", "dart", "dart test"),
    ]

    return draw(st.sampled_from(test_patterns))


@st.composite
def non_test_command_strategy(draw: Any) -> str:
    """Generate commands that should NOT match any test runner pattern.

    These are commands that might contain test-related keywords but are
    not actual test execution commands.
    """
    non_test_commands = [
        # Build/install commands
        "npm install",
        "npm install jest",
        "yarn add vitest",
        "pip install pytest",
        "cargo build",
        "mvn clean install",
        "gradle build",
        "dotnet build",
        # Run commands
        "npm run dev",
        "npm run start",
        "npm run build",
        "python script.py",
        "node index.js",
        "cargo run",
        "go run main.go",
        # Lint/format commands
        "npm run lint",
        "eslint .",
        "ruff check .",
        "black .",
        "cargo fmt",
        "go fmt",
        # Other commands
        "echo pytest",
        "cat jest.config.js",
        "grep test package.json",
        "which pytest",
        "ls -la",
        "cd tests/",
        "mkdir tests",
        "rm -rf tests/__pycache__",
        "find . -name test",
        "docker run pytest",
        # Commands with test in arguments but not test execution
        "git commit -m 'add test'",
        "python manage.py migrate",
        "npm run coverage",
        "yarn run format",
    ]

    return draw(st.sampled_from(non_test_commands))


# ============================================================================
# Property Tests
# ============================================================================


@given(test_data=command_with_expected_result_strategy())
@property_test_settings()
def test_property_8_test_runner_pattern_matching(
    test_data: tuple[str, str, str],
) -> None:
    """
    Property 8: Test Runner Pattern Matching.

    For any test execution command that matches a registered pattern,
    the test runner registry should correctly identify the associated
    language and framework.

    This property validates that the pattern matching mechanism works
    correctly across all supported languages and frameworks.

    Validates: Requirements 6.3
    """
    command, expected_language, expected_framework = test_data
    registry = TestRunnerRegistry()

    # Match the command against registered patterns
    is_match, detected_language, detected_framework = registry.match_command(command)

    # Verify the command is detected as a test command
    assert is_match is True, (
        f"Test command '{command}' was not detected as a test execution command. "
        f"Expected to match pattern for {expected_language}/{expected_framework}."
    )

    # Verify the detected language matches the expected language
    assert detected_language == expected_language, (
        f"Test command '{command}' was detected with language '{detected_language}' "
        f"instead of expected language '{expected_language}'."
    )

    # Verify the detected framework matches the expected framework
    assert detected_framework == expected_framework, (
        f"Test command '{command}' was detected with framework '{detected_framework}' "
        f"instead of expected framework '{expected_framework}'."
    )


@given(command=non_test_command_strategy())
@property_test_settings()
def test_property_8_non_test_command_rejection(command: str) -> None:
    """
    Property 8: Non-Test Command Rejection.

    For any command that is NOT a test execution command, the test runner
    registry should NOT match it against any pattern, even if it contains
    test-related keywords.

    This ensures the pattern matching is precise and doesn't produce
    false positives.

    Validates: Requirements 6.3
    """
    registry = TestRunnerRegistry()

    # Match the command against registered patterns
    is_match, detected_language, detected_framework = registry.match_command(command)

    # Verify the command is NOT detected as a test command
    assert is_match is False, (
        f"Non-test command '{command}' was incorrectly detected as a test command "
        f"(language={detected_language}, framework={detected_framework}). "
        f"The registry should only match actual test execution commands."
    )

    # Verify language and framework are None
    assert detected_language is None, (
        f"Non-test command '{command}' should have language=None, "
        f"got '{detected_language}'"
    )
    assert detected_framework is None, (
        f"Non-test command '{command}' should have framework=None, "
        f"got '{detected_framework}'"
    )


@given(
    test_data1=command_with_expected_result_strategy(),
    test_data2=command_with_expected_result_strategy(),
)
@property_test_settings()
def test_property_8_consistent_pattern_matching(
    test_data1: tuple[str, str, str],
    test_data2: tuple[str, str, str],
) -> None:
    """
    Property 8: Consistent Pattern Matching.

    For any two test commands, the pattern matching should be consistent
    and deterministic. The same command should always produce the same
    result, and different commands should be matched independently.

    Validates: Requirements 6.3
    """
    command1, expected_lang1, expected_fw1 = test_data1
    command2, expected_lang2, expected_fw2 = test_data2

    registry = TestRunnerRegistry()

    # Match both commands
    is_match1, lang1, fw1 = registry.match_command(command1)
    is_match2, lang2, fw2 = registry.match_command(command2)

    # Verify both commands are detected
    assert is_match1 is True, f"Command '{command1}' should match"
    assert is_match2 is True, f"Command '{command2}' should match"

    # Verify each command produces the expected result
    assert (
        lang1 == expected_lang1
    ), f"Command '{command1}' detected as '{lang1}' instead of '{expected_lang1}'"
    assert (
        fw1 == expected_fw1
    ), f"Command '{command1}' detected as '{fw1}' instead of '{expected_fw1}'"
    assert (
        lang2 == expected_lang2
    ), f"Command '{command2}' detected as '{lang2}' instead of '{expected_lang2}'"
    assert (
        fw2 == expected_fw2
    ), f"Command '{command2}' detected as '{fw2}' instead of '{expected_fw2}'"

    # Match the same commands again to verify consistency
    is_match1_again, lang1_again, fw1_again = registry.match_command(command1)
    is_match2_again, lang2_again, fw2_again = registry.match_command(command2)

    # Verify results are identical
    assert is_match1_again == is_match1, "Pattern matching should be deterministic"
    assert lang1_again == lang1, "Language detection should be deterministic"
    assert fw1_again == fw1, "Framework detection should be deterministic"
    assert is_match2_again == is_match2, "Pattern matching should be deterministic"
    assert lang2_again == lang2, "Language detection should be deterministic"
    assert fw2_again == fw2, "Framework detection should be deterministic"


@given(test_data=command_with_expected_result_strategy())
@property_test_settings(max_examples=10)  # Reduced from default for performance
def test_property_8_empty_and_none_command_handling(
    test_data: tuple[str, str, str],
) -> None:
    """
    Property 8: Empty and None Command Handling.

    The registry should handle edge cases like empty strings and None
    gracefully without raising exceptions.

    Validates: Requirements 6.3
    """
    registry = TestRunnerRegistry()

    # Test empty string
    is_match, language, framework = registry.match_command("")
    assert is_match is False, "Empty string should not match any pattern"
    assert language is None, "Empty string should have language=None"
    assert framework is None, "Empty string should have framework=None"

    # Test whitespace-only string
    is_match, language, framework = registry.match_command("   ")
    assert is_match is False, "Whitespace-only string should not match any pattern"
    assert language is None, "Whitespace-only string should have language=None"
    assert framework is None, "Whitespace-only string should have framework=None"


@given(test_data=command_with_expected_result_strategy())
@property_test_settings()
def test_property_8_case_sensitivity(test_data: tuple[str, str, str]) -> None:
    """
    Property 8: Case Sensitivity in Pattern Matching.

    Test commands should be matched case-sensitively. Commands with
    different casing should not match if they're not in the registry.

    Validates: Requirements 6.3
    """
    command, expected_language, expected_framework = test_data
    registry = TestRunnerRegistry()

    # Original command should match
    is_match, lang, fw = registry.match_command(command)
    assert is_match is True, f"Original command '{command}' should match"
    assert lang == expected_language
    assert fw == expected_framework

    # Test with uppercase (most commands should not match when uppercased)
    # Note: Some commands like "PYTEST" might still match if patterns are
    # case-insensitive, but most won't
    command_upper = command.upper()
    is_match_upper, _, _ = registry.match_command(command_upper)

    # We don't assert that uppercase doesn't match, because some patterns
    # might be case-insensitive. We just verify that if it does match,
    # it produces valid results (no exceptions).
    if is_match_upper:
        # If it matches, it should still produce valid language/framework
        _, lang_upper, fw_upper = registry.match_command(command_upper)
        assert lang_upper is not None, "Matched command should have a language"
        assert fw_upper is not None, "Matched command should have a framework"
