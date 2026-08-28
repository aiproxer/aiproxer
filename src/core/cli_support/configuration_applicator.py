"""ConfigurationApplicator - Coordinates applying CLI arguments to AppConfig.

**Feature: cli-god-object-refactoring, Task 5: ConfigurationApplicator (TDD)**

This module implements the ConfigurationApplicator class which coordinates
domain-specific applicators to transform parsed CLI arguments into a complete
AppConfig instance.

Requirements satisfied:
- 1.2: CLI module delegates to ConfigurationApplicator for applying arguments
- 1.3: ConfigurationApplicator records parameter sources via ParameterResolution
- 6.1: Coordinates domain-specific applicators
- 7.1: Backward compatibility with existing apply_cli_args behavior
- 8.3: No direct file I/O in ConfigurationApplicator (delegates to services)
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Any, Literal, overload

from src.core.cli_support.protocols import CliArgs, CliOverrides, DomainApplicator
from src.core.config.parameter_resolution import ParameterResolution, ParameterSource

if TYPE_CHECKING:
    from src.core.config.app_config import AppConfig


def _merge_dicts(target: dict[str, Any], source: dict[str, Any]) -> dict[str, Any]:
    """Recursively merge source dict into target dict.

    Values in source override values in target. Nested dicts are merged recursively.

    Args:
        target: Base dictionary to merge into
        source: Dictionary with values to merge

    Returns:
        Merged dictionary (modifies target in place and returns it)
    """
    for key, value in source.items():
        if key in target and isinstance(target[key], dict) and isinstance(value, dict):
            _merge_dicts(target[key], value)
        else:
            target[key] = value
    return target


class ConfigurationApplicator:
    """Coordinates applying CLI arguments to AppConfig.

    This class implements the Template Method pattern, defining the skeleton
    for applying CLI arguments while delegating domain-specific logic to
    DomainApplicator instances.

    The applicator:
    - Loads base configuration from file (if specified)
    - Delegates to domain applicators to collect CLI overrides
    - Merges CLI overrides onto base configuration
    - Handles default values (log file, command prefix)
    - Records parameter sources via ParameterResolution

    Requirements satisfied:
    - 1.2: CLI module delegates to ConfigurationApplicator for applying arguments
    - 1.3: ConfigurationApplicator records parameter sources via ParameterResolution
    - 6.1: Coordinates domain-specific applicators

    Example usage:
        applicator = ConfigurationApplicator()
        config = applicator.apply(parsed_args)

        # Or with resolution tracking:
        config, resolution = applicator.apply(parsed_args, return_resolution=True)
    """

    _applicators: list[DomainApplicator]

    def __init__(
        self,
        domain_applicators: list[DomainApplicator] | None = None,
    ) -> None:
        """Initialize the ConfigurationApplicator.

        Args:
            domain_applicators: Optional list of domain applicators.
                If None, uses default applicators for all domains.
        """
        self._applicators = (
            domain_applicators
            if domain_applicators is not None
            else self._default_applicators()
        )

    @overload
    def apply(
        self,
        args: CliArgs,
        *,
        return_resolution: Literal[False] = False,
        resolution: ParameterResolution | None = None,
    ) -> AppConfig: ...

    @overload
    def apply(
        self,
        args: CliArgs,
        *,
        return_resolution: Literal[True],
        resolution: ParameterResolution | None = None,
    ) -> tuple[AppConfig, ParameterResolution]: ...

    def apply(
        self,
        args: CliArgs,
        *,
        return_resolution: bool = False,
        resolution: ParameterResolution | None = None,
    ) -> AppConfig | tuple[AppConfig, ParameterResolution]:
        """Apply CLI arguments to create configuration.

        This method coordinates the full configuration application process:
        1. Load base configuration from file
        2. Collect CLI overrides from domain applicators
        3. Apply default log file if needed
        4. Merge CLI overrides onto base config
        5. Validate and apply command prefix
        6. Return final configuration

        Args:
            args: Parsed command-line arguments namespace
            return_resolution: If True, return (config, resolution) tuple
            resolution: Optional pre-existing resolution tracker

        Returns:
            AppConfig if return_resolution is False
            tuple (AppConfig, ParameterResolution) if return_resolution is True
        """
        # Import at runtime to avoid circular imports
        from src.core.config.app_config import load_config

        # Create or use provided resolution tracker
        res = resolution or ParameterResolution()

        # Load base configuration
        config_path = getattr(args, "config_file", None)
        base_cfg: AppConfig = load_config(config_path, resolution=res)

        final_cfg = self.apply_overrides(args, base_cfg, resolution=res)

        if return_resolution:
            return final_cfg, res
        return final_cfg

    def apply_overrides(
        self,
        args: CliArgs,
        base_cfg: AppConfig,
        *,
        resolution: ParameterResolution | None = None,
    ) -> AppConfig:
        """Apply CLI overrides on top of an existing base configuration.

        This is primarily used by compatibility layers that load configuration
        elsewhere (e.g., tests patching `src.core.cli.load_config`) and need
        to preserve that behavior while still using the domain applicators.
        """
        from src.command_prefix import validate_command_prefix
        from src.constants import DEFAULT_COMMAND_PREFIX
        from src.core.config.app_config import AppConfig

        res = resolution or ParameterResolution()

        # Collect CLI overrides from all domain applicators
        cli_overrides: CliOverrides = {}
        self._set_auxiliary_routing_base_config_disable(args, base_cfg)
        for applicator in self._applicators:
            applicator.apply(args, cli_overrides, res)

        # Handle default log file if none specified
        self._apply_default_log_file(args, base_cfg, cli_overrides, res)

        # Merge CLI overrides onto base config
        config_dict = base_cfg.model_dump(mode="json")
        _merge_dicts(config_dict, cli_overrides)

        if config_dict.get("command_prefix") is None:
            config_dict["command_prefix"] = DEFAULT_COMMAND_PREFIX
            res.record(
                "command_prefix",
                DEFAULT_COMMAND_PREFIX,
                ParameterSource.DEFAULT,
                origin="default",
            )

        # Create new AppConfig from merged dict
        final_cfg = AppConfig.model_validate(config_dict)

        # Validate and apply command prefix (ensures it's never None)
        final_cfg = self._validate_and_apply_prefix(final_cfg, validate_command_prefix)
        return final_cfg

    def _set_auxiliary_routing_base_config_disable(
        self,
        args: CliArgs,
        base_cfg: AppConfig,
    ) -> None:
        disable_from_base = False
        aux_cfg = getattr(base_cfg, "auxiliary_routing", None)
        if aux_cfg is not None:
            disable_from_base = bool(getattr(aux_cfg, "disable", False))
        args.auxiliary_routing_disabled_from_base_config = disable_from_base

    def _apply_default_log_file(
        self,
        args: CliArgs,
        base_cfg: AppConfig,
        overrides: CliOverrides,
        resolution: ParameterResolution,
    ) -> None:
        """Apply default log file if none specified in CLI or config.

        The default log file is ./var/logs/proxy.log. This ensures logging
        is always configured even if neither CLI nor config specifies a log file.

        Args:
            args: Parsed CLI arguments
            base_cfg: Base configuration loaded from file
            overrides: Dictionary collecting CLI overrides
            resolution: Parameter resolution tracker
        """
        # Check if log file is already specified
        cli_log_file = getattr(args, "log_file", None)
        config_log_file = (
            base_cfg.logging.log_file if hasattr(base_cfg, "logging") else None
        )

        # Check if LoggingApplicator already set a log file in overrides
        logging_overrides = overrides.get("logging", {})
        override_log_file = (
            logging_overrides.get("log_file")
            if isinstance(logging_overrides, dict)
            else None
        )

        if (
            cli_log_file is None
            and config_log_file is None
            and override_log_file is None
        ):
            # Apply default log file
            default_log_file = "./var/logs/proxy.log"

            # Ensure logging overrides dict exists
            if "logging" not in overrides:
                overrides["logging"] = {}
            if isinstance(overrides["logging"], dict):
                overrides["logging"]["log_file"] = default_log_file
                resolution.record(
                    "logging.log_file",
                    default_log_file,
                    ParameterSource.DEFAULT,
                    origin="default",
                )

    def _validate_and_apply_prefix(
        self,
        cfg: AppConfig,
        validate_fn: Any,
    ) -> AppConfig:
        """Validate command prefix configuration and apply defaults safely.

        Args:
            cfg: AppConfig to validate
            validate_fn: Function to validate command prefix string

        Returns:
            AppConfig with validated command prefix
        """
        prefix = str(cfg.command_prefix)
        err = validate_fn(prefix)
        if err:
            raise ValueError(f"Invalid command prefix {prefix!r}: {err}")
        return cfg

    def _default_applicators(self) -> list[DomainApplicator]:
        """Return default set of domain applicators.

        Returns all domain applicators in the order they should be applied.
        This order is important for proper configuration merging.

        Returns:
            List of domain applicator instances
        """
        from src.core.cli_support.applicators import (
            AccessModeApplicator,
            AuthApplicator,
            AuxiliaryRoutingApplicator,
            BackendApplicator,
            CompactionApplicator,
            DynamicCompressionApplicator,
            EditPrecisionApplicator,
            EndOfSessionApplicator,
            FailureHandlingApplicator,
            IdentityApplicator,
            LoggingApplicator,
            MemoryApplicator,
            ModelRegistryApplicator,
            NotificationApplicator,
            ReplacementApplicator,
            ResilienceApplicator,
            RoutingApplicator,
            SandboxingApplicator,
            ServerApplicator,
            SessionApplicator,
        )

        return [
            ServerApplicator(),
            LoggingApplicator(),
            AccessModeApplicator(),  # Must be before BackendApplicator (access mode needed for OAuth filtering)
            NotificationApplicator(),
            BackendApplicator(),
            SessionApplicator(),
            AuthApplicator(),
            MemoryApplicator(),
            ModelRegistryApplicator(),
            FailureHandlingApplicator(),
            ReplacementApplicator(),
            ResilienceApplicator(),
            EditPrecisionApplicator(),
            IdentityApplicator(),
            RoutingApplicator(),
            AuxiliaryRoutingApplicator(),
            CompactionApplicator(),
            DynamicCompressionApplicator(),
            SandboxingApplicator(),
            EndOfSessionApplicator(),
        ]


__all__ = ["ConfigurationApplicator"]
