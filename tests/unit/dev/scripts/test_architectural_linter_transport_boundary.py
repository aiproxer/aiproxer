"""Tests for strict core->transport architectural boundary enforcement."""

from __future__ import annotations

import importlib.util
from pathlib import Path
from types import ModuleType


def _load_architectural_linter_module() -> ModuleType:
    repo_root = Path(__file__).resolve().parents[4]
    module_path = repo_root / "dev" / "scripts" / "architectural_linter.py"
    spec = importlib.util.spec_from_file_location("architectural_linter", module_path)
    if spec is None or spec.loader is None:
        raise RuntimeError("Unable to load architectural_linter.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_services_layer_importing_transport_is_error(tmp_path: Path) -> None:
    """Files under src/core/services must not import src/core/transport."""
    linter_module = _load_architectural_linter_module()
    sample_file = tmp_path / "src" / "core" / "services" / "sample.py"
    sample_file.parent.mkdir(parents=True, exist_ok=True)
    sample_file.write_text(
        "from src.core.transport.session_key_resolver import "
        "resolve_session_key_from_request_context\n",
        encoding="utf-8",
    )

    violations = linter_module.lint_file(str(sample_file))

    assert violations
    assert any(
        violation.severity == "error"
        and "Core import boundary violation" in violation.message
        for violation in violations
    )


def test_common_layer_importing_transport_is_error(tmp_path: Path) -> None:
    """Files under src/core/common must not import src/core/transport."""
    linter_module = _load_architectural_linter_module()
    sample_file = tmp_path / "src" / "core" / "common" / "sample.py"
    sample_file.parent.mkdir(parents=True, exist_ok=True)
    sample_file.write_text(
        "from src.core.transport.session_key_resolver import "
        "resolve_session_key_from_request_context\n",
        encoding="utf-8",
    )

    violations = linter_module.lint_file(str(sample_file))

    assert violations
    assert any(
        violation.severity == "error"
        and "Core import boundary violation" in violation.message
        for violation in violations
    )


def test_services_layer_importing_frontend_controller_is_error(tmp_path: Path) -> None:
    """Files under src/core/services must not import frontend controller modules."""
    linter_module = _load_architectural_linter_module()
    sample_file = tmp_path / "src" / "core" / "services" / "sample.py"
    sample_file.parent.mkdir(parents=True, exist_ok=True)
    sample_file.write_text(
        "from src.core.app.controllers.chat_controller import ChatController\n",
        encoding="utf-8",
    )

    violations = linter_module.lint_file(str(sample_file))

    assert violations
    assert any(
        violation.severity == "error"
        and "Core frontend boundary violation" in violation.message
        for violation in violations
    )


def test_connectors_layer_importing_core_services_is_error(tmp_path: Path) -> None:
    """Connector modules must not depend directly on core service modules."""
    linter_module = _load_architectural_linter_module()
    sample_file = tmp_path / "src" / "connectors" / "sample.py"
    sample_file.parent.mkdir(parents=True, exist_ok=True)
    sample_file.write_text(
        "from src.core.services.command_handler import CommandHandler\n",
        encoding="utf-8",
    )

    violations = linter_module.lint_file(str(sample_file))

    assert violations
    assert any(
        violation.severity == "error"
        and "Connector import boundary violation" in violation.message
        for violation in violations
    )


def test_connectors_layer_allows_boundary_validation_import(tmp_path: Path) -> None:
    """Connector boundary allows the explicit boundary-validation helper."""
    linter_module = _load_architectural_linter_module()
    sample_file = tmp_path / "src" / "connectors" / "sample.py"
    sample_file.parent.mkdir(parents=True, exist_ok=True)
    sample_file.write_text(
        "from src.core.services.boundary_validation import "
        "log_boundary_validation_failure\n",
        encoding="utf-8",
    )

    violations = linter_module.lint_file(str(sample_file))

    assert not any(
        "Connector import boundary violation" in violation.message
        for violation in violations
    )


def test_plugin_discovery_entry_points_call_outside_boundary_is_error(
    tmp_path: Path,
) -> None:
    """Only canonical plugin discovery service may enumerate entry points."""
    linter_module = _load_architectural_linter_module()
    sample_file = tmp_path / "src" / "core" / "services" / "plugin_scan.py"
    sample_file.parent.mkdir(parents=True, exist_ok=True)
    sample_file.write_text(
        "from importlib import metadata\n"
        "def scan() -> None:\n"
        "    metadata.entry_points(group='llm_proxy_backends')\n",
        encoding="utf-8",
    )

    violations = linter_module.lint_file(str(sample_file))

    assert any(
        violation.severity == "error"
        and "Plugin discovery DRY violation" in violation.message
        for violation in violations
    )


def test_plugin_discovery_entry_points_call_within_boundary_is_allowed(
    tmp_path: Path,
) -> None:
    """Canonical plugin discovery service may enumerate entry points."""
    linter_module = _load_architectural_linter_module()
    sample_file = tmp_path / "src" / "core" / "services" / "backend_plugin_discovery.py"
    sample_file.parent.mkdir(parents=True, exist_ok=True)
    sample_file.write_text(
        "from importlib import metadata\n"
        "def scan() -> None:\n"
        "    metadata.entry_points(group='llm_proxy_backends')\n",
        encoding="utf-8",
    )

    violations = linter_module.lint_file(str(sample_file))

    assert not any(
        "Plugin discovery DRY violation" in violation.message
        for violation in violations
    )
