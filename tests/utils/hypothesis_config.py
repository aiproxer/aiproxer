# mypy: ignore-errors
"""
Hypothesis configuration for property-based testing.

This module provides centralized configuration for Hypothesis property-based
testing, ensuring consistent settings across all property tests.

Feature: streaming-pipeline-refactor, Task 21: Property-based test infrastructure
"""

from hypothesis import HealthCheck, Phase, Verbosity, settings

# ============================================================================
# Default Settings Profile
# ============================================================================

# Register a default profile for all property tests
# Using 50 examples for balance between coverage and speed (was 100)
settings.register_profile(
    "default",
    max_examples=50,  # Reduced from 100 for faster test execution
    deadline=None,  # No deadline for async tests
    suppress_health_check=[
        HealthCheck.too_slow,  # Allow slow tests for thorough checking
        HealthCheck.data_too_large,  # Allow large test data
    ],
    phases=[
        Phase.explicit,  # Run explicit examples
        Phase.reuse,  # Reuse previous failures
        Phase.generate,  # Generate new examples
        Phase.target,  # Target interesting examples
        Phase.shrink,  # Shrink failing examples
    ],
    verbosity=Verbosity.normal,
    print_blob=True,  # Print reproduction blob on failure
)

# Register a fast profile for quick testing during development
settings.register_profile(
    "fast",
    max_examples=10,  # Only 10 iterations for quick feedback
    deadline=None,
    suppress_health_check=[
        HealthCheck.too_slow,
        HealthCheck.data_too_large,
    ],
    phases=[
        Phase.explicit,
        Phase.generate,
        Phase.shrink,
    ],
    verbosity=Verbosity.normal,
)

# Register a thorough profile for CI/CD
settings.register_profile(
    "ci",
    max_examples=200,  # More iterations for CI
    deadline=None,
    suppress_health_check=[
        HealthCheck.too_slow,
        HealthCheck.data_too_large,
    ],
    phases=[
        Phase.explicit,
        Phase.reuse,
        Phase.generate,
        Phase.target,
        Phase.shrink,
    ],
    verbosity=Verbosity.verbose,  # More verbose output in CI
    print_blob=True,
)

# Register a debug profile for investigating failures
settings.register_profile(
    "debug",
    max_examples=100,
    deadline=None,
    suppress_health_check=[
        HealthCheck.too_slow,
        HealthCheck.data_too_large,
    ],
    phases=[
        Phase.explicit,
        Phase.reuse,
        Phase.generate,
        Phase.target,
        Phase.shrink,
    ],
    verbosity=Verbosity.debug,  # Maximum verbosity
    print_blob=True,
)

# Load the default profile
settings.load_profile("default")


# ============================================================================
# Custom Settings Decorators
# ============================================================================


def property_test_settings(**kwargs):
    """Decorator for property tests with default settings.

    This decorator applies the default property test settings and allows
    overriding specific settings as needed.

    Args:
        **kwargs: Additional settings to override

    Returns:
        A settings decorator with merged configuration
    """
    default_kwargs = {
        "max_examples": 50,  # Reduced from 100 for faster execution
        "deadline": None,
        "suppress_health_check": [
            HealthCheck.too_slow,
            HealthCheck.data_too_large,
        ],
    }
    default_kwargs.update(kwargs)
    return settings(**default_kwargs)


def fast_property_test_settings(**kwargs):
    """Decorator for fast property tests during development.

    Args:
        **kwargs: Additional settings to override

    Returns:
        A settings decorator with fast configuration
    """
    default_kwargs = {
        "max_examples": 10,
        "deadline": None,
        "suppress_health_check": [
            HealthCheck.too_slow,
            HealthCheck.data_too_large,
        ],
    }
    default_kwargs.update(kwargs)
    return settings(**default_kwargs)


def slow_property_test_settings(**kwargs):
    """Decorator for property tests with heavy I/O operations.

    Use this for tests marked @pytest.mark.slow that involve:
    - Database I/O (SQLite, etc.)
    - Real cryptographic operations (argon2, etc.)
    - Network operations
    - File system operations

    Uses reduced max_examples (20) to maintain good coverage
    while keeping test execution reasonable.

    Args:
        **kwargs: Additional settings to override

    Returns:
        A settings decorator optimized for slow operations
    """
    default_kwargs = {
        "max_examples": 20,  # Reduced from 100 for heavy I/O tests
        "deadline": None,
        "suppress_health_check": [
            HealthCheck.too_slow,
            HealthCheck.data_too_large,
            HealthCheck.filter_too_much,  # Allow filtered examples in slow tests
        ],
    }
    default_kwargs.update(kwargs)
    return settings(**default_kwargs)


def thorough_property_test_settings(**kwargs):
    """Decorator for thorough property tests in CI.

    Args:
        **kwargs: Additional settings to override

    Returns:
        A settings decorator with thorough configuration
    """
    default_kwargs = {
        "max_examples": 200,
        "deadline": None,
        "suppress_health_check": [
            HealthCheck.too_slow,
            HealthCheck.data_too_large,
        ],
        "verbosity": Verbosity.verbose,
    }
    default_kwargs.update(kwargs)
    return settings(**default_kwargs)


# ============================================================================
# Utility Functions
# ============================================================================


def get_current_profile() -> str:
    """Get the name of the currently active Hypothesis profile.

    Returns:
        The name of the active profile
    """
    # Hypothesis doesn't expose the current profile name directly
    # Return a default value
    return "default"


def set_profile(profile_name: str) -> None:
    """Set the active Hypothesis profile.

    Args:
        profile_name: Name of the profile to activate
            ("default", "fast", "ci", or "debug")

    Raises:
        ValueError: If the profile name is not recognized
    """
    valid_profiles = ["default", "fast", "ci", "debug"]
    if profile_name not in valid_profiles:
        raise ValueError(
            f"Invalid profile name: {profile_name}. "
            f"Valid profiles are: {', '.join(valid_profiles)}"
        )
    settings.load_profile(profile_name)


def get_max_examples() -> int:
    """Get the maximum number of examples for the current profile.

    Returns:
        The max_examples setting for the current profile
    """
    return settings.default.max_examples
