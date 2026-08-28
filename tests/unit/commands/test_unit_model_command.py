from unittest.mock import Mock

import pytest
from src.core.domain.commands.model_command import ModelCommand
from src.core.domain.session import BackendConfiguration, Session, SessionState


@pytest.fixture
def command() -> ModelCommand:
    return ModelCommand()


@pytest.fixture
def mock_session() -> Mock:
    mock = Mock(spec=Session)
    mock.state = SessionState(backend_config=BackendConfiguration(model="old_model"))
    return mock


def test_set_model_simple(command: ModelCommand, mock_session: Mock):
    # Act
    result = command._set_model("new_model", mock_session)

    # Assert
    assert result.success is True
    assert result.message == "Model changed to new_model"
    assert result.new_state.backend_config.model == "new_model"
    assert result.new_state.backend_config.backend_type is None  # Should not change


def test_set_model_with_backend(command: ModelCommand, mock_session: Mock):
    # Act
    result = command._set_model("new_backend:new_model", mock_session)

    # Assert
    assert result.success is True
    assert (
        result.message == "Backend changed to new_backend; Model changed to new_model"
    )
    assert result.new_state.backend_config.model == "new_model"
    assert result.new_state.backend_config.backend_type == "new_backend"


def test_set_model_colon_after_slash_stays_model_only(
    command: ModelCommand, mock_session: Mock
):
    # Act
    result = command._set_model(
        "openrouter/anthropic/claude-3-haiku:free", mock_session
    )

    # Assert
    assert result.success is True
    assert result.message == "Model changed to openrouter/anthropic/claude-3-haiku:free"
    assert (
        result.new_state.backend_config.model
        == "openrouter/anthropic/claude-3-haiku:free"
    )
    assert result.new_state.backend_config.backend_type is None


def test_unset_model(command: ModelCommand, mock_session: Mock):
    # Act
    result = command._unset_model(mock_session)

    # Assert
    assert result.success is True
    assert result.message == "Model unset"
    assert result.new_state.backend_config.model is None
