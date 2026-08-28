import unittest
from unittest.mock import MagicMock

from src.core.domain.configuration.replacement_config import ReplacementConfig
from src.core.domain.request_context import RequestContext
from src.core.services.model_replacement_service import ModelReplacementService


class TestModelReplacementServiceOverride(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.backend_registry = MagicMock()
        self.backend_registry.get_registered_backends.return_value = [
            "gemini-oauth-auto",
            "other-backend",
        ]

    async def test_oauth_auto_replacement_blocked_by_default(self):
        config = ReplacementConfig(
            enabled=True,
            probability=1.0,
            replacement_rules=[],
            backend_model="other-backend:model",
            allow_oauth_auto_replacement=False,
        )
        service = ModelReplacementService(config, self.backend_registry)

        context = RequestContext(headers={}, cookies={}, state={}, app_state={})
        session_id = "test-session"

        # We need to bypass the first turn check to see the oauth-auto check
        # Turn 1
        service.should_replace(session_id, context, "gemini-oauth-auto", "model")
        # Turn 2
        result = service.should_replace(
            session_id, context, "gemini-oauth-auto", "model"
        )

        self.assertFalse(
            result, "Replacement should be blocked for gemini-oauth-auto by default"
        )

        # Test kiro-oauth-auto as well
        session_id_kiro = "test-session-kiro"
        service.should_replace(session_id_kiro, context, "kiro-oauth-auto", "model")
        result_kiro = service.should_replace(
            session_id_kiro, context, "kiro-oauth-auto", "model"
        )
        self.assertFalse(
            result_kiro, "Replacement should be blocked for kiro-oauth-auto by default"
        )

    async def test_oauth_auto_replacement_allowed_with_override(self):
        config = ReplacementConfig(
            enabled=True,
            probability=1.0,
            replacement_rules=[],
            backend_model="other-backend:model",
            allow_oauth_auto_replacement=True,
        )
        service = ModelReplacementService(config, self.backend_registry)

        context = RequestContext(headers={}, cookies={}, state={}, app_state={})
        session_id = "test-session-2"

        # Turn 1
        service.should_replace(session_id, context, "gemini-oauth-auto", "model")
        # Turn 2
        result = service.should_replace(
            session_id, context, "gemini-oauth-auto", "model"
        )

        self.assertTrue(
            result, "Replacement should be allowed for gemini-oauth-auto with override"
        )

        # Test kiro-oauth-auto as well
        session_id_kiro = "test-session-kiro-2"
        service.should_replace(session_id_kiro, context, "kiro-oauth-auto", "model")
        result_kiro = service.should_replace(
            session_id_kiro, context, "kiro-oauth-auto", "model"
        )
        self.assertTrue(
            result_kiro,
            "Replacement should be allowed for kiro-oauth-auto with override",
        )
