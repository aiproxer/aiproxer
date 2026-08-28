from __future__ import annotations

import logging
import re
from collections.abc import Mapping
from pathlib import Path
from typing import Any

try:
    import yaml  # type: ignore
except ModuleNotFoundError:  # pragma: no cover
    yaml = None  # type: ignore

from src.core.common.backend_discovery_state import get_extracted_backend_names
from src.core.common.exceptions import ConfigurationError
from src.core.config.constrained_backend_policy import (
    match_constrained_connector_family,
)
from src.core.config.parameter_resolution import ParameterResolution, ParameterSource
from src.core.services.backend_registry import backend_registry

logger = logging.getLogger(__name__)


def _file_based_connector_names() -> frozenset[str]:
    """Connectors that use credential files; refreshed each call (not import-time).

    Import-time snapshots could miss optional plugin entry points that appear
    after the first module load, leaving defaults and duplicate-path checks wrong.
    """
    return frozenset(
        set(get_extracted_backend_names()).union({"gemini-cli-cloud-project"})
    )


def _default_backend_instances_dir() -> Path:
    return (
        Path(__file__).resolve().parents[4]
        / "config"
        / "backends"
        / "backend-instances"
    )


DEFAULT_BACKEND_INSTANCES_DIR = _default_backend_instances_dir()


class BackendInstanceEnvSource:
    """Discover backend instances from environment variables."""

    def load(
        self,
        environ: Mapping[str, str],
        *,
        existing_instance_names: set[str],
        resolution: ParameterResolution | None,
    ) -> dict[str, Any]:
        registered_backends = set(backend_registry.get_registered_backends())
        env_prefixes: dict[str, str] = {
            "openai": "OPENAI_API_KEY",
            "anthropic": "ANTHROPIC_API_KEY",
            "gemini": "GEMINI_API_KEY",
            "openrouter": "OPENROUTER_API_KEY",
            "zai": "ZAI_API_KEY",
            "zai-coding-plan": "ZAI_CODING_PLAN_API_KEY",
            "minimax": "MINIMAX_API_KEY",
            "zenmux": "ZENMUX_API_KEY",
            "kimi-code": "KIMI_API_KEY",
            "opencode-go": "OPENCODE_GO_API_KEY",
            "opencode-zen": "OPENCODE_ZEN_API_KEY",
            "commandcode-openai": "COMMANDCODE_API_KEY",
            "commandcode-anthropic": "COMMANDCODE_API_KEY",
            "nvidia": "NVIDIA_API_KEY",
            "openai-codex": "OPENAI_CODEX_API_KEY",
            "openai-codex-v2": "OPENAI_CODEX_API_KEY",
        }

        discovered: dict[str, dict[str, Any]] = {}

        for connector, prefix in env_prefixes.items():
            if connector not in registered_backends:
                continue

            for i in range(1, 100):
                env_key = f"{prefix}_{i}"
                api_key = environ.get(env_key)
                if not api_key:
                    continue
                instance_name = f"{connector}.{i}"

                if instance_name in existing_instance_names:
                    continue

                discovered[instance_name] = {"api_key": api_key, "connector": connector}
                if resolution is not None:
                    resolution.record(
                        f'backends["{instance_name}"].api_key',
                        api_key,
                        ParameterSource.ENVIRONMENT,
                        origin=env_key,
                    )

        if not discovered:
            return {}

        return {"backends": discovered}


class BackendInstanceFileSource:
    """Load backend instance configs from per-instance YAML files."""

    _filename_pattern = re.compile(r"^(?P<connector>[^.]+)\.(?P<name>.+)\.yaml$")

    def __init__(self, *, instances_dir: Path = DEFAULT_BACKEND_INSTANCES_DIR) -> None:
        self._instances_dir = instances_dir

    def load(
        self,
        *,
        existing_instance_names: set[str],
        resolution: ParameterResolution | None,
    ) -> dict[str, Any]:
        registered_backends = set(backend_registry.get_registered_backends())

        discovered: dict[str, dict[str, Any]] = {}

        if self._instances_dir.exists():
            for config_file in self._instances_dir.glob("*.yaml"):
                match = self._filename_pattern.match(config_file.name)
                if not match:
                    continue

                connector = match.group("connector")
                if connector not in registered_backends:
                    logger.warning(
                        "Skipping config file %s: connector '%s' not registered",
                        config_file.name,
                        connector,
                    )
                    continue

                instance_name = f"{connector}.{match.group('name')}"

                file_config = _load_backend_instance_file(
                    config_file=config_file,
                    connector=connector,
                    instance_name=instance_name,
                )
                if file_config is None:
                    continue

                discovered[instance_name] = file_config

                if resolution is not None:
                    for key, value in file_config.items():
                        resolution.record(
                            f'backends["{instance_name}"].{key}',
                            value,
                            ParameterSource.CONFIG_FILE,
                            origin=str(config_file),
                        )

                logger.info("Loaded backend instance config: %s", instance_name)

        self._validate_credentials_uniqueness(discovered)
        self._ensure_file_based_defaults(
            discovered,
            existing_instance_names=existing_instance_names,
            registered_backends=registered_backends,
        )

        if not discovered:
            return {}
        return {"backends": discovered}

    def _validate_credentials_uniqueness(
        self, discovered: dict[str, dict[str, Any]]
    ) -> None:
        connector_paths: dict[str, dict[str, str]] = {}

        for instance_name, config in discovered.items():
            connector = str(config.get("connector") or instance_name.split(".")[0])
            if connector not in _file_based_connector_names():
                continue

            creds_path = config.get("credentials_path")
            if not creds_path:
                continue

            normalized_path = str(Path(str(creds_path)).resolve())
            connector_paths.setdefault(connector, {})
            if normalized_path in connector_paths[connector]:
                prev_instance = connector_paths[connector][normalized_path]
                raise ConfigurationError(
                    message="Duplicate credentials path detected for backend instances",
                    details={
                        "connector": connector,
                        "path": str(creds_path),
                        "instances": [prev_instance, instance_name],
                    },
                )

            connector_paths[connector][normalized_path] = instance_name

    def _ensure_file_based_defaults(
        self,
        discovered: dict[str, dict[str, Any]],
        *,
        existing_instance_names: set[str],
        registered_backends: set[str],
    ) -> None:
        available_connectors = sorted(
            _file_based_connector_names().intersection(registered_backends)
        )
        all_instance_names = existing_instance_names.union(discovered.keys())
        claimed_constrained_families: set[str] = set()

        for connector in available_connectors:
            constrained_family = match_constrained_connector_family(connector)
            if constrained_family:
                if constrained_family in claimed_constrained_families:
                    continue
                has_any_instance = any(
                    match_constrained_connector_family(name) == constrained_family
                    for name in all_instance_names
                )
                if has_any_instance:
                    claimed_constrained_families.add(constrained_family)
                    continue
            else:
                has_any_instance = any(
                    name.startswith(f"{connector}.") for name in all_instance_names
                )
                if has_any_instance:
                    continue

            instance_name = f"{connector}.1"
            discovered[instance_name] = {"connector": connector}
            all_instance_names.add(instance_name)
            if constrained_family:
                claimed_constrained_families.add(constrained_family)
            logger.info(
                "Created default instance '%s' for file-based connector '%s'",
                instance_name,
                connector,
            )


def _load_backend_instance_file(
    *,
    config_file: Path,
    connector: str,
    instance_name: str,
) -> dict[str, Any] | None:
    if yaml is None:
        raise ConfigurationError(
            message="PyYAML is not installed",
            details={
                "path": str(config_file),
                "instance": instance_name,
                "connector": connector,
            },
        )

    try:
        with config_file.open(encoding="utf-8") as f:
            loaded = yaml.safe_load(f) or {}

        if not isinstance(loaded, dict):
            logger.warning(
                "Skipping invalid backend instance config file %s: top-level is not a mapping",
                config_file.name,
            )
            return None

        file_config: dict[str, Any] = dict(loaded)
        file_config["connector"] = connector
        return file_config
    except (KeyboardInterrupt, SystemExit, GeneratorExit):
        # Re-raise system-level exceptions that should never be silently caught
        raise
    except (OSError, yaml.YAMLError, TypeError, ValueError) as exc:
        # Handle expected errors: file I/O, YAML parsing, data type issues
        raise ConfigurationError(
            message="Failed to load backend instance configuration file",
            details={
                "path": str(config_file),
                "instance": instance_name,
                "connector": connector,
            },
        ) from exc
