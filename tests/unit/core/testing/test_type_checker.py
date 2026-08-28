"""
Tests for Type Checker.

This module provides comprehensive test coverage for the type checker
that analyzes async/sync patterns in test files.
"""

import tempfile
from pathlib import Path
from unittest.mock import AsyncMock, MagicMock

import pytest
from src.core.testing.type_checker import (
    AsyncSyncPatternChecker,
    RuntimePatternChecker,
    create_pre_commit_hook,
)


class TestAsyncSyncPatternChecker:
    """Tests for AsyncSyncPatternChecker class."""

    @pytest.fixture
    def checker(self) -> AsyncSyncPatternChecker:
        """Create an AsyncSyncPatternChecker instance."""
        return AsyncSyncPatternChecker()

    def test_initialization(self, checker: AsyncSyncPatternChecker) -> None:
        """Test AsyncSyncPatternChecker initialization."""
        assert checker.issues == []

    def test_check_file_nonexistent_file(
        self, checker: AsyncSyncPatternChecker
    ) -> None:
        """Test checking a nonexistent file."""
        nonexistent_path = Path("nonexistent_file.py")

        # Should return error message instead of raising exception
        issues = checker.check_file(nonexistent_path)
        assert len(issues) > 0
        assert any("Error parsing" in issue for issue in issues)

    def test_check_file_empty_file(self, checker: AsyncSyncPatternChecker) -> None:
        """Test checking an empty file."""
        with tempfile.NamedTemporaryFile(mode="w", suffix=".py", delete=False) as f:
            f.write("")
            temp_path = Path(f.name)

        try:
            issues = checker.check_file(temp_path)
            assert issues == []
        finally:
            temp_path.unlink()

    def test_check_file_with_async_mock_usage(
        self, checker: AsyncSyncPatternChecker
    ) -> None:
        """Test checking a file with AsyncMock usage."""
        test_code = """
import pytest
from unittest.mock import AsyncMock

def test_something():
    mock = AsyncMock()
    result = mock.some_method()
"""

        with tempfile.NamedTemporaryFile(mode="w", suffix=".py", delete=False) as f:
            f.write(test_code)
            temp_path = Path(f.name)

        try:
            issues = checker.check_file(temp_path)
            assert len(issues) > 0
            assert any("AsyncMock" in issue for issue in issues)
        finally:
            temp_path.unlink()

    def test_check_file_with_problematic_stage_inheritance(
        self, checker: AsyncSyncPatternChecker
    ) -> None:
        """Test checking a file with problematic stage inheritance."""
        test_code = """
from src.core.app.stages.base import InitializationStage

class ProblematicTestStage(InitializationStage):
    def get_dependencies(self):
        return ["core_services"]
"""

        with tempfile.NamedTemporaryFile(mode="w", suffix=".py", delete=False) as f:
            f.write(test_code)
            temp_path = Path(f.name)

        try:
            issues = checker.check_file(temp_path)
            assert len(issues) > 0
            assert any(
                "should inherit from ValidatedTestStage" in issue for issue in issues
            )
        finally:
            temp_path.unlink()

    def test_check_file_with_safe_stage_inheritance(
        self, checker: AsyncSyncPatternChecker
    ) -> None:
        """Test checking a file with safe stage inheritance."""
        test_code = """
from src.core.testing.base_stage import ValidatedTestStage

class SafeTestStage(ValidatedTestStage):
    def get_dependencies(self):
        return ["core_services"]
"""

        with tempfile.NamedTemporaryFile(mode="w", suffix=".py", delete=False) as f:
            f.write(test_code)
            temp_path = Path(f.name)

        try:
            issues = checker.check_file(temp_path)
            # Should not have issues about stage inheritance
            assert not any(
                "should inherit from ValidatedTestStage" in issue for issue in issues
            )
        finally:
            temp_path.unlink()

    def test_check_file_with_async_mock_assignment(
        self, checker: AsyncSyncPatternChecker
    ) -> None:
        """Test checking a file with AsyncMock assignment to session service."""
        test_code = """
from unittest.mock import AsyncMock
from src.core.interfaces.session_service_interface import ISessionService

session_service = AsyncMock(spec=ISessionService)
"""

        with tempfile.NamedTemporaryFile(mode="w", suffix=".py", delete=False) as f:
            f.write(test_code)
            temp_path = Path(f.name)

        try:
            issues = checker.check_file(temp_path)
            assert len(issues) > 0
            assert any("session service" in issue.lower() for issue in issues)
        finally:
            temp_path.unlink()

    def test_check_file_with_async_mock_add_instance(
        self, checker: AsyncSyncPatternChecker
    ) -> None:
        """Test checking a file with AsyncMock in add_instance call."""
        test_code = """
from unittest.mock import AsyncMock

def test_something():
    services = None
    mock = AsyncMock()
    services.add_instance("test", mock)
"""

        with tempfile.NamedTemporaryFile(mode="w", suffix=".py", delete=False) as f:
            f.write(test_code)
            temp_path = Path(f.name)

        try:
            issues = checker.check_file(temp_path)
            assert len(issues) > 0
            # The checker detects AsyncMock usage in test functions
            assert any("AsyncMock" in issue for issue in issues)
            assert any("test function" in issue.lower() for issue in issues)
        finally:
            temp_path.unlink()

    def test_check_directory_with_test_files(
        self, checker: AsyncSyncPatternChecker
    ) -> None:
        """Test checking a directory with test files."""
        # Create a temporary directory with test files
        import tempfile

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)

            # Create a test file with issues
            test_file = temp_path / "test_problematic.py"
            test_file.write_text("""
from unittest.mock import AsyncMock
from src.core.interfaces.session_service_interface import ISessionService

def test_problem():
    mock = AsyncMock(spec=ISessionService)
""")

            # Create a regular Python file (should be ignored)
            regular_file = temp_path / "regular.py"
            regular_file.write_text("print('hello')")

            results = checker.check_directory(temp_path, "test_*.py")

            assert str(test_file) in results
            assert len(results[str(test_file)]) > 0
            assert "regular.py" not in results

    def test_check_directory_no_pattern_match(
        self, checker: AsyncSyncPatternChecker
    ) -> None:
        """Test checking a directory with no pattern matches."""
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)

            # Create a non-matching file
            non_matching_file = temp_path / "not_a_test.py"
            non_matching_file.write_text("print('hello')")

            results = checker.check_directory(temp_path, "test_*.py")

            assert results == {}


class TestRuntimePatternChecker:
    """Tests for RuntimePatternChecker class."""

    def test_check_service_registration_with_session_service(self) -> None:
        """Test checking service registration with session service."""
        from unittest.mock import AsyncMock

        from src.core.interfaces.session_service_interface import ISessionService

        mock_service = AsyncMock(spec=ISessionService)
        mock_service.get_session = AsyncMock()

        warnings = RuntimePatternChecker.check_service_registration(
            ISessionService, mock_service
        )

        assert len(warnings) > 0
        assert any("coroutine" in warning.lower() for warning in warnings)

    def test_check_service_registration_with_sync_service(self) -> None:
        """Test checking service registration with sync service."""
        from src.core.interfaces.session_service_interface import ISessionService

        mock_service = MagicMock(spec=ISessionService)
        mock_service.get_session = MagicMock()

        warnings = RuntimePatternChecker.check_service_registration(
            ISessionService, mock_service
        )

        assert len(warnings) == 0

    def test_check_service_registration_with_async_mock(self) -> None:
        """Test checking service registration with AsyncMock."""
        mock_service = AsyncMock()

        warnings = RuntimePatternChecker.check_service_registration(
            object, mock_service
        )

        assert len(warnings) > 0

    def test_validate_test_app_no_service_provider(self) -> None:
        """Test validating test app without service provider."""

        class MockApp:
            class State:
                pass

            state = State()

        app = MockApp()
        warnings = RuntimePatternChecker.validate_test_app(app)

        assert len(warnings) == 0

    def test_validate_test_app_with_service_provider(self) -> None:
        """Test validating test app with service provider."""
        from unittest.mock import MagicMock

        class MockApp:
            class State:
                service_provider = MagicMock()

            state = State()

        # Mock the service provider to not have session service
        app = MockApp()
        app.state.service_provider.get_service.return_value = None

        warnings = RuntimePatternChecker.validate_test_app(app)

        assert len(warnings) == 0


class TestCreatePreCommitHook:
    """Tests for create_pre_commit_hook function."""

    def test_create_pre_commit_hook_returns_string(self) -> None:
        """Test that create_pre_commit_hook returns a string."""
        hook_content = create_pre_commit_hook()

        assert isinstance(hook_content, str)
        assert len(hook_content) > 0

    def test_create_pre_commit_hook_contains_shebang(self) -> None:
        """Test that the hook contains a proper shebang."""
        hook_content = create_pre_commit_hook()

        assert "#!/bin/bash" in hook_content

    def test_create_pre_commit_hook_contains_python_code(self) -> None:
        """Test that the hook contains Python code for checking."""
        hook_content = create_pre_commit_hook()

        assert "python -c" in hook_content
        assert "AsyncSyncPatternChecker" in hook_content

    def test_create_pre_commit_hook_contains_error_handling(self) -> None:
        """Test that the hook contains error handling."""
        hook_content = create_pre_commit_hook()

        assert "issues_found = True" in hook_content
        assert "sys.exit(1)" in hook_content

    def test_create_pre_commit_hook_contains_helpful_messages(self) -> None:
        """Test that the hook contains helpful error messages."""
        hook_content = create_pre_commit_hook()

        assert "Consider using:" in hook_content
        assert "ValidatedTestStage" in hook_content
        assert "EnforcedMockFactory" in hook_content


class TestTypeCheckerIntegration:
    """Integration tests for type checker functionality."""

    def test_complete_file_analysis_workflow(self) -> None:
        """Test complete file analysis workflow."""
        checker = AsyncSyncPatternChecker()

        test_code = """
import pytest
from unittest.mock import AsyncMock, MagicMock
from src.core.testing.interfaces import EnforcedMockFactory

def test_good_usage():
    mock = EnforcedMockFactory.create_session_service_mock()
    session = mock.get_session("test_id")
    assert session.session_id == "test_id"

def test_bad_usage():
    mock = AsyncMock()  # This should be flagged
    result = mock.some_method()
"""

        with tempfile.NamedTemporaryFile(mode="w", suffix=".py", delete=False) as f:
            f.write(test_code)
            temp_path = Path(f.name)

        try:
            issues = checker.check_file(temp_path)
            # Should find at least one issue (the AsyncMock usage)
            assert len(issues) >= 1
        finally:
            temp_path.unlink()

    def test_ast_node_checking_methods_exist(self) -> None:
        """Test that all AST checking methods exist."""
        checker = AsyncSyncPatternChecker()

        # Check that private methods exist
        assert hasattr(checker, "_check_ast_node")
        assert hasattr(checker, "_check_function_def")
        assert hasattr(checker, "_check_class_def")
        assert hasattr(checker, "_check_assignment")
        assert hasattr(checker, "_check_function_call")

    def test_checker_can_parse_complex_code(self) -> None:
        """Test that checker can parse complex code without errors."""
        checker = AsyncSyncPatternChecker()

        complex_code = """
import asyncio
from unittest.mock import AsyncMock, MagicMock
from typing import Optional, List
import pytest

class TestComplexService:
    def __init__(self):
        self.value = 42

    def sync_method(self, param: str) -> Optional[str]:
        return param.upper() if param else None

    async def async_method(self, items: List[str]) -> List[str]:
        return [item.lower() for item in items]

@pytest.fixture
def service():
    return TestComplexService()

@pytest.mark.asyncio
async def test_complex_workflow(service):
    # Good usage
    result1 = service.sync_method("hello")
    assert result1 == "HELLO"

    # Good usage
    result2 = await service.async_method(["A", "B", "C"])
    assert result2 == ["a", "b", "c"]

            # Bad usage (should be flagged)
        mock = AsyncMock()

        def test_bad():
            mock = AsyncMock()
            result = mock.some_method()
"""

        with tempfile.NamedTemporaryFile(mode="w", suffix=".py", delete=False) as f:
            f.write(complex_code)
            temp_path = Path(f.name)

        try:
            issues = checker.check_file(temp_path)
            # Should find the AsyncMock usage in test
            assert len(issues) >= 1
        finally:
            temp_path.unlink()

    def test_checker_handles_malformed_python(self) -> None:
        """Test that checker handles malformed Python gracefully."""
        checker = AsyncSyncPatternChecker()

        malformed_code = """
def test_broken(
    # Missing closing paren
    mock = AsyncMock()
    return mock
"""

        with tempfile.NamedTemporaryFile(mode="w", suffix=".py", delete=False) as f:
            f.write(malformed_code)
            temp_path = Path(f.name)

        try:
            issues = checker.check_file(temp_path)
            # Should contain error about parsing
            assert len(issues) > 0
            assert any("Error parsing" in issue for issue in issues)
        finally:
            temp_path.unlink()

    def test_directory_checking_with_mixed_files(self) -> None:
        """Test directory checking with mixed file types."""
        checker = AsyncSyncPatternChecker()

        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir)

            # Test file with issues
            test_file = temp_path / "test_with_issues.py"
            test_file.write_text("""
from unittest.mock import AsyncMock

def test_problem():
    mock = AsyncMock()
""")

            # Test file without issues
            clean_test_file = temp_path / "test_clean.py"
            clean_test_file.write_text("""
def test_clean():
    assert True
""")

            # Non-test file (should be ignored)
            non_test_file = temp_path / "utils.py"
            non_test_file.write_text("""
from unittest.mock import AsyncMock

def helper():
    mock = AsyncMock()
""")

            results = checker.check_directory(temp_path, "test_*.py")

            # Should only analyze test files and only include files with issues
            assert str(test_file) in results
            assert len(results[str(test_file)]) > 0  # Should have issues
            assert str(clean_test_file) not in results  # Clean files not included
            assert str(non_test_file) not in results  # Non-test files not analyzed
