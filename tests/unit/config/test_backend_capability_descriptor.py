"""Contract tests for BackendCapabilityDescriptor and BackendConfig integration."""

from __future__ import annotations

import pytest
from pydantic import ValidationError

# ---------------------------------------------------------------------------
# Task 1: BackendCapabilityDescriptor model tests (Tests 1-7)
# ---------------------------------------------------------------------------


class TestBackendCapabilityDescriptorDefaults:
    """Test 1: safe defaults with no args."""

    def test_default_values(self) -> None:
        from src.core.domain.backend_capability_descriptor import (
            BackendCapabilityDescriptor,
        )

        d = BackendCapabilityDescriptor()
        assert d.supports_streaming is True
        assert d.supports_tool_calls is True
        assert d.supports_vision is False
        assert d.protocol_family == "openai"


class TestBackendCapabilityDescriptorFlags:
    """Test 2: setting supports_streaming=False."""

    def test_supports_streaming_false(self) -> None:
        from src.core.domain.backend_capability_descriptor import (
            BackendCapabilityDescriptor,
        )

        d = BackendCapabilityDescriptor(supports_streaming=False)
        assert d.supports_streaming is False


class TestBackendCapabilityDescriptorProtocolFamily:
    """Tests 3-5: protocol_family validation."""

    def test_anthropic_accepted(self) -> None:
        from src.core.domain.backend_capability_descriptor import (
            BackendCapabilityDescriptor,
        )

        d = BackendCapabilityDescriptor(protocol_family="anthropic")
        assert d.protocol_family == "anthropic"

    def test_gemini_accepted(self) -> None:
        from src.core.domain.backend_capability_descriptor import (
            BackendCapabilityDescriptor,
        )

        d = BackendCapabilityDescriptor(protocol_family="gemini")
        assert d.protocol_family == "gemini"

    def test_unknown_raises_validation_error(self) -> None:
        from src.core.domain.backend_capability_descriptor import (
            BackendCapabilityDescriptor,
        )

        with pytest.raises(ValidationError):
            BackendCapabilityDescriptor(protocol_family="unknown")  # type: ignore[arg-type]


class TestBackendCapabilityDescriptorPydantic:
    """Test 6: is a Pydantic BaseModel."""

    def test_model_dump(self) -> None:
        from src.core.domain.backend_capability_descriptor import (
            BackendCapabilityDescriptor,
        )

        d = BackendCapabilityDescriptor()
        dumped = d.model_dump()
        assert isinstance(dumped, dict)
        assert "supports_streaming" in dumped

    def test_model_validate(self) -> None:
        from src.core.domain.backend_capability_descriptor import (
            BackendCapabilityDescriptor,
        )

        d = BackendCapabilityDescriptor.model_validate({"supports_vision": True})
        assert d.supports_vision is True


class TestBackendCapabilityDescriptorFromDict:
    """Test 7: from_dict round-trip."""

    def test_from_dict_round_trip(self) -> None:
        from src.core.domain.backend_capability_descriptor import (
            BackendCapabilityDescriptor,
        )

        d = BackendCapabilityDescriptor.from_dict({"supports_streaming": False})
        assert d.supports_streaming is False
        assert d.supports_tool_calls is True  # default preserved


# ---------------------------------------------------------------------------
# Task 2: BackendConfig.capability_descriptor integration tests (Tests 8-12)
# ---------------------------------------------------------------------------


class TestBackendConfigCapabilityDescriptorDefault:
    """Test 8: BackendConfig() with no capability_descriptor has None."""

    def test_default_is_none(self) -> None:
        from src.core.config.models.backends import BackendConfig

        cfg = BackendConfig()
        assert cfg.capability_descriptor is None


class TestBackendConfigCapabilityDescriptorCoercion:
    """Test 9: dict is coerced to BackendCapabilityDescriptor."""

    def test_dict_coerced(self) -> None:
        from src.core.config.models.backends import BackendConfig
        from src.core.domain.backend_capability_descriptor import (
            BackendCapabilityDescriptor,
        )

        cfg = BackendConfig(capability_descriptor={"supports_streaming": False})
        assert isinstance(cfg.capability_descriptor, BackendCapabilityDescriptor)
        assert cfg.capability_descriptor.supports_streaming is False


class TestBackendConfigCapabilityDescriptorModel:
    """Test 10: BackendCapabilityDescriptor instance stored directly."""

    def test_model_stored_directly(self) -> None:
        from src.core.config.models.backends import BackendConfig
        from src.core.domain.backend_capability_descriptor import (
            BackendCapabilityDescriptor,
        )

        descriptor = BackendCapabilityDescriptor(supports_vision=True)
        cfg = BackendConfig(capability_descriptor=descriptor)
        assert isinstance(cfg.capability_descriptor, BackendCapabilityDescriptor)
        assert cfg.capability_descriptor.supports_vision is True


class TestBackendSettingsCapabilityDescriptorRoundTrip:
    """Test 11: BackendSettings with capability_descriptor dict round-trips."""

    def test_round_trip(self) -> None:
        from src.core.config.models.backends import BackendConfig, BackendSettings
        from src.core.domain.backend_capability_descriptor import (
            BackendCapabilityDescriptor,
        )

        settings = BackendSettings(
            mybackend={"capability_descriptor": {"supports_streaming": False}}
        )
        cfg = settings.lookup("mybackend")
        assert cfg is not None
        assert isinstance(cfg, BackendConfig)
        assert isinstance(cfg.capability_descriptor, BackendCapabilityDescriptor)
        assert cfg.capability_descriptor.supports_streaming is False


class TestBackendConfigModelDump:
    """Test 12: model_dump() includes capability_descriptor as dict when set."""

    def test_model_dump_includes_descriptor(self) -> None:
        from src.core.config.models.backends import BackendConfig

        cfg = BackendConfig(capability_descriptor={"supports_vision": True})
        dumped = cfg.model_dump()
        assert dumped["capability_descriptor"] is not None
        assert isinstance(dumped["capability_descriptor"], dict)
        assert dumped["capability_descriptor"]["supports_vision"] is True
