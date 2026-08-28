from __future__ import annotations

from src.connectors._openai_codex_connector import OpenAICodexConnector
from src.connectors.gemini_base.connector import GeminiOAuthBaseConnector
from src.connectors.gemini_cloud_project import GeminiCloudProjectConnector
from src.core.services.connector_invoker import ConnectorInvoker


def test_all_legacy_connectors_are_canonical() -> None:
    """Test that formerly legacy connectors now implement the canonical signature.
    We check the classes directly to avoid instantiation complexities.
    """
    invoker = ConnectorInvoker()

    # We can check the class method signature directly by mocking the hasattr check internally
    # ConnectorInvoker._is_canonical_backend checks if the instance has the method and callable.
    # We can pass the class but it checks hasattr(backend, "chat_completions"). The class has it!
    # And inspect.signature handles unbound methods returning the first param as `self`.
    # Wait, ConnectorInvoker._is_canonical_backend filters out `self`? Actually, inspect.signature on a bound method omits `self`. On an unbound method, `self` is present!
    # Let's create dummy types that inherit from the classes to satisfy instantiation without calling __init__

    class DummyCodex(OpenAICodexConnector):
        def __init__(self):
            pass

    class DummyGCP(GeminiCloudProjectConnector):
        def __init__(self):
            pass

    class DummyOAuth(GeminiOAuthBaseConnector):
        def __init__(self):
            pass

        def _discover_project_id(self):
            pass

    codex = DummyCodex()
    gcp = DummyGCP()
    gem_oauth = DummyOAuth()

    # These should all be True
    assert invoker._is_canonical_backend(codex), "OpenAICodexConnector is not canonical"
    assert invoker._is_canonical_backend(
        gcp
    ), "GeminiCloudProjectConnector is not canonical"
    assert invoker._is_canonical_backend(
        gem_oauth
    ), "GeminiOAuthBaseConnector is not canonical"
