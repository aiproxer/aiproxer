"""Tests for check_boundary_types.py script."""

import json
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

from freezegun import freeze_time

# Add dev/scripts to path for imports
dev_scripts_path = Path(__file__).parent.parent.parent.parent / "dev" / "scripts"
sys.path.insert(0, str(dev_scripts_path))

from check_boundary_types import (
    AllowlistEntry,
    BoundaryTypeChecker,
    Violation,
    check_boundary_types,
    is_in_scope,
    is_violation_allowlisted,
    load_allowlist,
    load_scope_config,
)


class TestBoundaryTypeChecker:
    """Test BoundaryTypeChecker detection logic."""

    def test_detects_any_in_function_signature(self):
        """Test that Any in function signature is detected."""
        code = """
from typing import Any

def process_request(request: Any) -> ResponseEnvelope:
    pass
"""
        checker = BoundaryTypeChecker()
        violations = checker.check_file("src/core/interfaces/processor.py", code)
        assert len(violations) == 1
        assert (
            violations[0].message
            == "Function 'process_request' parameter 'request' uses 'Any' in signature"
        )
        assert violations[0].line == 4

    def test_detects_dict_str_any_in_function_signature(self):
        """Test that dict[str, Any] in function signature is detected."""
        code = """
from typing import Any

def process_request(request: dict[str, Any]) -> ResponseEnvelope:
    pass
"""
        checker = BoundaryTypeChecker()
        violations = checker.check_file("src/core/interfaces/processor.py", code)
        assert len(violations) == 1
        assert (
            violations[0].message
            == "Function 'process_request' parameter 'request' uses 'dict[str, Any]' in signature"
        )
        assert violations[0].line == 4

    def test_allows_dict_str_jsonvalue(self):
        """Test that dict[str, JsonValue] is allowed."""
        code = """
from pydantic.types import JsonValue

def process_request(request: dict[str, JsonValue]) -> ResponseEnvelope:
    pass
"""
        checker = BoundaryTypeChecker()
        violations = checker.check_file("src/core/interfaces/processor.py", code)
        assert len(violations) == 0

    def test_allows_typed_contracts(self):
        """Test that canonical contracts are allowed."""
        code = """
from src.core.domain.chat import CanonicalChatRequest
from src.core.domain.request_context import RequestContext

def process_request(request: CanonicalChatRequest, context: RequestContext) -> ResponseEnvelope:
    pass
"""
        checker = BoundaryTypeChecker()
        violations = checker.check_file("src/core/interfaces/processor.py", code)
        assert len(violations) == 0

    def test_respects_allowlist(self):
        """Test that allowlisted patterns are ignored."""
        code = """
from typing import Any
from dataclasses import field

class ProcessingContext:
    values: dict[str, Any] = field(default_factory=dict)
"""
        checker = BoundaryTypeChecker()
        violations = checker.check_file("src/core/domain/request_context.py", code)
        # ProcessingContext.values is allowlisted
        assert len(violations) == 0

    def test_detects_type_ignore_in_boundary_modules(self):
        """Test that type: ignore comments are detected."""
        code = """
from typing import Any

def process_request(request: Any) -> ResponseEnvelope:  # type: ignore[no-untyped-def]
    pass
"""
        checker = BoundaryTypeChecker()
        violations = checker.check_file("src/core/interfaces/processor.py", code)
        assert len(violations) >= 1
        # Should detect Any (type: ignore detection not implemented yet)
        any_violations = [v for v in violations if "Any" in v.message]
        assert len(any_violations) >= 1

    def test_ignores_test_files(self):
        """Test that test files are ignored."""
        code = """
def test_something(request: Any) -> None:
    pass
"""
        checker = BoundaryTypeChecker()
        violations = checker.check_file("test_file.py", code)
        assert len(violations) == 0

    def test_detects_any_in_method_signature(self):
        """Test that Any in method signature is detected."""
        code = """
from typing import Any

class Service:
    def process(self, request: Any) -> ResponseEnvelope:
        pass
"""
        checker = BoundaryTypeChecker()
        violations = checker.check_file("src/core/interfaces/service.py", code)
        assert len(violations) == 1
        assert "process" in violations[0].message

    def test_allows_any_in_internal_contexts(self):
        """Test that Any in internal contexts (not function signatures) is allowed."""
        code = """
from typing import Any
from src.core.domain.chat import CanonicalChatRequest

def process_request(request: CanonicalChatRequest) -> ResponseEnvelope:
    internal_var: Any = some_value
    return ResponseEnvelope(content=internal_var)
"""
        checker = BoundaryTypeChecker()
        violations = checker.check_file("src/core/interfaces/processor.py", code)
        # Internal variable assignments are not checked
        assert len(violations) == 0

    def test_detects_any_in_return_type(self):
        """Test that Any in return type is detected."""
        code = """
from typing import Any
from src.core.domain.chat import CanonicalChatRequest

def process_request(request: CanonicalChatRequest) -> Any:
    pass
"""
        checker = BoundaryTypeChecker()
        violations = checker.check_file("src/core/interfaces/processor.py", code)
        assert len(violations) == 1
        assert (
            "return type" in violations[0].message.lower()
            or "Any" in violations[0].message
        )

    def test_allows_union_with_none(self):
        """Test that Optional/Union with None is allowed."""
        code = """
from typing import Optional
from src.core.domain.chat import CanonicalChatRequest

def process_request(request: Optional[CanonicalChatRequest]) -> ResponseEnvelope:
    pass
"""
        checker = BoundaryTypeChecker()
        violations = checker.check_file("src/core/interfaces/processor.py", code)
        assert len(violations) == 0


class TestCheckBoundaryTypes:
    """Test the main check_boundary_types function."""

    def test_returns_zero_exit_code_when_clean(self, tmp_path):
        """Test that clean codebase returns exit code 0."""
        # Create a clean Python file
        test_file = tmp_path / "test_clean.py"
        test_file.write_text("""
from src.core.domain.chat import CanonicalChatRequest

def process(request: CanonicalChatRequest) -> None:
    pass
""")

        # Create boundary module directory
        boundary_dir = tmp_path / "src" / "core" / "interfaces"
        boundary_dir.mkdir(parents=True)
        boundary_file = boundary_dir / "test_interface.py"
        boundary_file.write_text("""
from src.core.domain.chat import CanonicalChatRequest

def process(request: CanonicalChatRequest) -> None:
    pass
""")

        exit_code = check_boundary_types([str(tmp_path)])
        assert exit_code == 0

    def test_returns_one_exit_code_when_violations_found(self, tmp_path):
        """Test that violations return exit code 1."""
        # Create scope config
        scope_file = tmp_path / "scope.json"
        scope_file.write_text(
            json.dumps(
                {
                    "explicit_files": ["src/core/interfaces/processor.py"],
                    "include_globs": [],
                    "exclude_globs": [],
                }
            )
        )

        # Create boundary module directory
        boundary_dir = tmp_path / "src" / "core" / "interfaces"
        boundary_dir.mkdir(parents=True)
        boundary_file = boundary_dir / "processor.py"
        boundary_file.write_text("""
from typing import Any

def process(request: Any) -> None:
    pass
""")

        scope_config = load_scope_config(scope_file)
        exit_code = check_boundary_types([str(tmp_path)], scope_config=scope_config)
        assert exit_code == 1

    def test_ignores_non_boundary_modules(self, tmp_path):
        """Test that non-boundary modules are ignored."""
        # Create non-boundary directory
        other_dir = tmp_path / "src" / "other"
        other_dir.mkdir(parents=True)
        other_file = other_dir / "test_other.py"
        other_file.write_text("""
from typing import Any

def process(request: Any) -> None:
    pass
""")

        exit_code = check_boundary_types([str(tmp_path)])
        # Should not find violations in non-boundary modules
        assert exit_code == 0


class TestScopeFiltering:
    """Test scope-based file filtering."""

    def test_explicit_files_in_scope(self, tmp_path):
        """Test that explicit files are always in scope."""
        scope_config = {
            "explicit_files": ["src/core/interfaces/test.py"],
            "include_globs": [],
            "exclude_globs": [],
        }

        file_path = tmp_path / "src" / "core" / "interfaces" / "test.py"
        file_path.parent.mkdir(parents=True)
        file_path.touch()

        assert is_in_scope(file_path, scope_config) is True

    def test_explicit_files_override_excludes(self, tmp_path):
        """Test that explicit files override exclude globs."""
        scope_config = {
            "explicit_files": ["src/core/interfaces/test.py"],
            "include_globs": [],
            "exclude_globs": ["src/core/interfaces/*.py"],
        }

        file_path = tmp_path / "src" / "core" / "interfaces" / "test.py"
        file_path.parent.mkdir(parents=True)
        file_path.touch()

        assert is_in_scope(file_path, scope_config) is True

    def test_include_globs_match(self, tmp_path):
        """Test that include globs match files."""
        scope_config = {
            "explicit_files": [],
            "include_globs": ["src/core/interfaces/*.py"],
            "exclude_globs": [],
        }

        file_path = tmp_path / "src" / "core" / "interfaces" / "test.py"
        file_path.parent.mkdir(parents=True)
        file_path.touch()

        assert is_in_scope(file_path, scope_config) is True

    def test_exclude_globs_filter_out(self, tmp_path):
        """Test that exclude globs filter out files."""
        scope_config = {
            "explicit_files": [],
            "include_globs": ["src/core/**/*.py"],
            "exclude_globs": ["src/core/internal/*.py"],
        }

        included_file = tmp_path / "src" / "core" / "interfaces" / "test.py"
        excluded_file = tmp_path / "src" / "core" / "internal" / "test.py"
        included_file.parent.mkdir(parents=True)
        excluded_file.parent.mkdir(parents=True)
        included_file.touch()
        excluded_file.touch()

        assert is_in_scope(included_file, scope_config) is True
        assert is_in_scope(excluded_file, scope_config) is False

    def test_empty_include_globs_only_explicit(self, tmp_path):
        """Test that empty include_globs means only explicit files are in scope."""
        scope_config = {
            "explicit_files": ["src/core/interfaces/test.py"],
            "include_globs": [],
            "exclude_globs": [],
        }

        explicit_file = tmp_path / "src" / "core" / "interfaces" / "test.py"
        other_file = tmp_path / "src" / "core" / "interfaces" / "other.py"
        explicit_file.parent.mkdir(parents=True)
        explicit_file.touch()
        other_file.touch()

        assert is_in_scope(explicit_file, scope_config) is True
        assert is_in_scope(other_file, scope_config) is False

    def test_load_scope_config(self, tmp_path):
        """Test loading scope configuration from JSON."""
        scope_file = tmp_path / "scope.json"
        scope_file.write_text(
            json.dumps(
                {
                    "explicit_files": ["src/test.py"],
                    "include_globs": ["src/**/*.py"],
                    "exclude_globs": ["src/tests/*.py"],
                }
            )
        )

        config = load_scope_config(scope_file)
        assert config["explicit_files"] == ["src/test.py"]
        assert config["include_globs"] == ["src/**/*.py"]
        assert config["exclude_globs"] == ["src/tests/*.py"]


class TestAllowlist:
    """Test allowlist mechanism."""

    def test_allowlist_entry_matches_violation(self):
        """Test that allowlist entry matches violations correctly."""
        entry = AllowlistEntry(
            file="src/core/interfaces/test.py",
            symbol="process_request",
            violation="Any-in-signature",
            reason="Test",
            expires_at="2025-12-31T00:00:00Z",
            tracking="test-123",
        )

        violation = Violation(
            file_path="src/core/interfaces/test.py",
            line=10,
            column=0,
            message="Function 'process_request' parameter 'request' uses 'Any' in signature",
            symbol="process_request",
        )

        is_allowed, matched_entry = is_violation_allowlisted(
            violation, "Any-in-signature", [entry]
        )
        assert is_allowed is True
        assert matched_entry == entry

    def test_allowlist_entry_without_symbol_matches(self):
        """Test that allowlist entry without symbol matches any symbol."""
        entry = AllowlistEntry(
            file="src/core/interfaces/test.py",
            symbol=None,
            violation="Any-in-signature",
            reason="Test",
            expires_at="2025-12-31T00:00:00Z",
            tracking="test-123",
        )

        violation = Violation(
            file_path="src/core/interfaces/test.py",
            line=10,
            column=0,
            message="Function 'other_func' parameter 'request' uses 'Any' in signature",
            symbol="other_func",
        )

        is_allowed, matched_entry = is_violation_allowlisted(
            violation, "Any-in-signature", [entry]
        )
        assert is_allowed is True

    def test_allowlist_entry_expired(self):
        """Test that expired allowlist entries are detected."""
        with freeze_time("2024-01-15T12:00:00Z"):
            past_date = (datetime.now(timezone.utc) - timedelta(days=1)).isoformat()
            entry = AllowlistEntry(
                file="src/core/interfaces/test.py",
                symbol="process_request",
                violation="Any-in-signature",
                reason="Test",
                expires_at=past_date,
                tracking="test-123",
            )

            assert entry.is_expired() is True

    def test_allowlist_entry_not_expired(self):
        """Test that non-expired allowlist entries are valid."""
        with freeze_time("2024-01-15T12:00:00Z"):
            future_date = (datetime.now(timezone.utc) + timedelta(days=30)).isoformat()
            entry = AllowlistEntry(
                file="src/core/interfaces/test.py",
                symbol="process_request",
                violation="Any-in-signature",
                reason="Test",
                expires_at=future_date,
                tracking="test-123",
            )

            assert entry.is_expired() is False

    def test_load_allowlist_filters_expired(self, tmp_path):
        """Test that loading allowlist filters out expired entries."""
        with freeze_time("2024-01-15T12:00:00Z"):
            future_date = (datetime.now(timezone.utc) + timedelta(days=30)).isoformat()
            past_date = (datetime.now(timezone.utc) - timedelta(days=1)).isoformat()

            allowlist_file = tmp_path / "allowlist.json"
            allowlist_file.write_text(
                json.dumps(
                    {
                        "version": "1.0",
                        "entries": [
                            {
                                "file": "src/core/interfaces/valid.py",
                                "symbol": "func1",
                                "violation": "Any-in-signature",
                                "reason": "Valid entry",
                                "expires_at": future_date,
                                "tracking": "test-1",
                            },
                            {
                                "file": "src/core/interfaces/expired.py",
                                "symbol": "func2",
                                "violation": "Any-in-signature",
                                "reason": "Expired entry",
                                "expires_at": past_date,
                                "tracking": "test-2",
                            },
                        ],
                    }
                )
            )

            entries, has_expired = load_allowlist(allowlist_file)
            assert len(entries) == 1
            assert entries[0].file == "src/core/interfaces/valid.py"
            assert has_expired is True

    def test_allowlist_matches_dict_violation(self):
        """Test that allowlist matches dict[str, Any] violations."""
        entry = AllowlistEntry(
            file="src/core/interfaces/test.py",
            symbol="process_request",
            violation="dict[str, Any]",
            reason="Test",
            expires_at="2025-12-31T00:00:00Z",
            tracking="test-123",
        )

        violation = Violation(
            file_path="src/core/interfaces/test.py",
            line=10,
            column=0,
            message="Function 'process_request' parameter 'request' uses 'dict[str, Any]' in signature",
            symbol="process_request",
        )

        is_allowed, matched_entry = is_violation_allowlisted(
            violation, "dict[str, Any]", [entry]
        )
        assert is_allowed is True
        assert matched_entry == entry

    def test_allowlist_no_match_wrong_file(self):
        """Test that allowlist doesn't match wrong file."""
        entry = AllowlistEntry(
            file="src/core/interfaces/other.py",
            symbol="process_request",
            violation="Any-in-signature",
            reason="Test",
            expires_at="2025-12-31T00:00:00Z",
            tracking="test-123",
        )

        violation = Violation(
            file_path="src/core/interfaces/test.py",
            line=10,
            column=0,
            message="Function 'process_request' parameter 'request' uses 'Any' in signature",
            symbol="process_request",
        )

        is_allowed, matched_entry = is_violation_allowlisted(
            violation, "Any-in-signature", [entry]
        )
        assert is_allowed is False
        assert matched_entry is None

    def test_check_boundary_types_with_allowlist(self, tmp_path):
        """Test that check_boundary_types respects allowlist."""
        # Create scope config
        scope_file = tmp_path / "scope.json"
        scope_file.write_text(
            json.dumps(
                {
                    "explicit_files": ["src/core/interfaces/test.py"],
                    "include_globs": [],
                    "exclude_globs": [],
                }
            )
        )

        # Create file with violation
        test_file = tmp_path / "src" / "core" / "interfaces" / "test.py"
        test_file.parent.mkdir(parents=True)
        test_file.write_text("""
from typing import Any

def process_request(request: Any) -> None:
    pass
""")

        # Create allowlist
        with freeze_time("2024-01-15T12:00:00Z"):
            future_date = (datetime.now(timezone.utc) + timedelta(days=30)).isoformat()
            allowlist_file = tmp_path / "allowlist.json"
            allowlist_file.write_text(
                json.dumps(
                    {
                        "version": "1.0",
                        "entries": [
                            {
                                "file": "src/core/interfaces/test.py",
                                "symbol": "process_request",
                                "violation": "Any-in-signature",
                                "reason": "Test allowlist",
                                "expires_at": future_date,
                                "tracking": "test-123",
                            }
                        ],
                    }
                )
            )

            # Load configs
            scope_config = load_scope_config(scope_file)
            allowlist, _ = load_allowlist(allowlist_file)

            # Check should pass (violation is allowlisted)
            exit_code = check_boundary_types(
                [str(tmp_path)], scope_config=scope_config, allowlist=allowlist
            )
            assert exit_code == 0
