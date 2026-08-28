"""Configuration for random model replacement feature."""

from __future__ import annotations

import logging
from typing import Any

from src.core.domain.base import ValueObject
from src.core.domain.configuration.replacement_rule import ReplacementRule

logger = logging.getLogger(__name__)


class ReplacementConfig(ValueObject):
    """Configuration for random model replacement.

    This configuration controls probabilistic swapping of user-specified
    backend:model pairs with alternative replacement pairs during a session.
    Supports conditional replacement rules with pattern matching.
    """

    enabled: bool = False
    probability: float = 0.0
    replacement_rules: list[ReplacementRule] = []
    # Legacy field for backward compatibility
    backend_model: str = ""
    turn_count: int = 1
    allow_oauth_auto_replacement: bool = False

    def model_post_init(self, __context: Any) -> None:  # type: ignore[override]
        """Run post-initialization validation."""
        super().model_post_init(__context)
        # Convert legacy backend_model to replacement_rules if needed
        self._migrate_legacy_config()
        self.validate_config()

    def _migrate_legacy_config(self) -> None:
        """Migrate legacy backend_model to replacement_rules format.

        If backend_model is set but replacement_rules is empty, convert
        the single backend_model to a wildcard replacement rule.
        Note: This modifies the object during initialization (before freezing).
        """
        if self.backend_model and not self.replacement_rules:
            if ":" not in self.backend_model:
                return  # Invalid format, will be caught by validation

            backend, model = self.backend_model.split(":", 1)
            rule = ReplacementRule(
                from_pattern="*",
                to_backend=backend,
                to_model=model,
            )
            # Use object.__setattr__ to bypass frozen restriction during init
            object.__setattr__(self, "replacement_rules", [rule])
            if logger.isEnabledFor(logging.WARNING):
                logger.warning(
                    "Legacy replacement.backend_model format detected. "
                    "Migrated to replacement_rules with wildcard pattern. "
                    "Please update your configuration to use replacement_rules format."
                )

    def validate_config(self) -> None:
        """Validate configuration parameters.

        Raises:
            ValueError: If any configuration parameter is invalid.
        """
        if self.enabled:
            if not (0.0 <= self.probability <= 1.0):
                raise ValueError(
                    f"replacement_probability must be between 0.0 and 1.0, got {self.probability}"
                )
            if not self.replacement_rules:
                raise ValueError(
                    "replacement_rules must be provided when enabled. "
                    "At least one replacement rule is required."
                )
            if self.turn_count < 1:
                raise ValueError(
                    f"replacement_turn_count must be at least 1, got {self.turn_count}"
                )

            # Check for wildcard exclusivity
            wildcard_rules = [
                i
                for i, rule in enumerate(self.replacement_rules)
                if rule.from_pattern == "*"
            ]
            if wildcard_rules and len(self.replacement_rules) > 1:
                raise ValueError(
                    f"Invalid replacement rules configuration: "
                    f"Wildcard rule at index {wildcard_rules[0]} cannot be combined with other rules. "
                    f"Wildcard '*' matches all models, making other rules redundant or unreachable. "
                    f"Either use only a single wildcard rule OR use multiple specific rules without wildcards."
                )

            # Validate each replacement rule
            for i, rule in enumerate(self.replacement_rules):
                if not rule.to_backend or not rule.to_model:
                    raise ValueError(
                        f"replacement_rules[{i}]: to_backend and to_model must be provided"
                    )
                if ":" not in f"{rule.to_backend}:{rule.to_model}":
                    raise ValueError(
                        f"replacement_rules[{i}]: to_backend:to_model must be in format 'backend:model'"
                    )
                # Validate that replacement target is not a wildcard
                if rule.to_backend == "*" or rule.to_model == "*":
                    raise ValueError(
                        f"replacement_rules[{i}]: Replacement target cannot use wildcard '*'. "
                        f"Found to_backend='{rule.to_backend}', to_model='{rule.to_model}'. "
                        f"Only the source pattern (from_pattern) can be a wildcard."
                    )

    def find_matching_rule(self, backend: str, model: str) -> ReplacementRule | None:
        """Find the first replacement rule that matches the given backend:model.

        Rules are evaluated in order, and the first matching rule is returned.
        This allows for more specific rules to be placed before wildcard rules.

        Args:
            backend: The backend identifier
            model: The model identifier

        Returns:
            The first matching ReplacementRule, or None if no rule matches
        """
        for rule in self.replacement_rules:
            if rule.matches(backend, model):
                return rule
        return None
