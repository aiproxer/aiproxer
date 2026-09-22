"""ResponseExecutor retry heuristics and helper method tests."""

from __future__ import annotations

import pytest
from src.core.interfaces.response_processor_interface import ProcessedResponse


class TestResponseExecutor:
    """Test ResponseExecutor service implementation."""

    def test_build_headers(self, executor, mock_base_connector, sample_context):
        """Test header building."""
        # Pass both conversation_id and session_id (method signature requires both)
        headers = executor._build_headers(
            sample_context.session_id, sample_context.session_id
        )

        assert "Authorization" in headers
        assert headers["OpenAI-Beta"] == "responses=experimental"
        assert headers["Accept"] == "text/event-stream"
        assert headers["conversation_id"] == sample_context.session_id
        assert headers["session_id"] == sample_context.session_id
        assert headers["version"] == "0.156.0"
        assert "codex_cli_rs/0.156.0" in headers["User-Agent"]

    def test_build_headers_uses_configured_client_version(
        self, mock_base_connector, mock_credential_manager, sample_context
    ):
        """The Codex protocol version header is configurable (gates gpt-6-sol)."""
        from src.connectors.openai_codex.executor import ResponseExecutor

        executor = ResponseExecutor(
            mock_base_connector,
            mock_credential_manager,
            codex_client_version="0.155.0",
        )
        headers = executor._build_headers(
            sample_context.session_id, sample_context.session_id
        )

        assert headers["version"] == "0.155.0"
        assert "codex_cli_rs/0.155.0" in headers["User-Agent"]

    def test_should_retry_for_auth_error_with_status(self, executor):
        """Test detection of auth error in chunk."""
        chunk = ProcessedResponse(
            content={
                "error": "auth_failed",
                "details": {"status": 401},
            }
        )

        assert executor._should_retry_for_auth_error(chunk) is True

    def test_should_retry_for_auth_error_with_nested_status(self, executor):
        """Test detection of auth error in nested metadata."""
        chunk = ProcessedResponse(
            content={
                "error": "auth_failed",
                "details": {
                    "metadata": {"status_code": 403},
                },
            }
        )

        assert executor._should_retry_for_auth_error(chunk) is True

    def test_should_retry_for_auth_error_no_error(self, executor):
        """Test that normal chunks don't trigger retry."""
        chunk = ProcessedResponse(
            content={"choices": [{"delta": {"content": "normal"}}]}
        )

        assert executor._should_retry_for_auth_error(chunk) is False

    def test_should_retry_for_auth_error_with_code_heuristic(self, executor):
        """Test detection of auth error using code-based heuristics."""
        # Test various auth-related codes
        auth_codes = [
            "auth",
            "unauthorized",
            "invalid_token",
            "invalid_api_key",
            "token_expired",
            "access_denied",
            "AUTH_ERROR",
            "UnauthorizedAccess",
        ]

        for code in auth_codes:
            chunk = ProcessedResponse(
                content={
                    "error": "some_error",
                    "details": {"code": code},
                }
            )
            assert (
                executor._should_retry_for_auth_error(chunk) is True
            ), f"Should detect auth error for code: {code}"

    def test_should_retry_for_auth_error_with_code_in_content(self, executor):
        """Test detection when code is in content root instead of details."""
        chunk = ProcessedResponse(
            content={
                "code": "invalid_token",
                "message": "Token is invalid",
            }
        )

        assert executor._should_retry_for_auth_error(chunk) is True

    def test_should_retry_for_auth_error_with_message_heuristic_401(self, executor):
        """Test detection using message-based heuristics for 401."""
        chunk = ProcessedResponse(
            content={
                "error": "Request failed with status 401",
                "message": "Unauthorized access",
            }
        )

        assert executor._should_retry_for_auth_error(chunk) is True

    def test_should_retry_for_auth_error_with_message_heuristic_403(self, executor):
        """Test detection using message-based heuristics for 403."""
        chunk = ProcessedResponse(
            content={
                "error": "Request failed with status 403",
                "message": "Forbidden",
            }
        )

        assert executor._should_retry_for_auth_error(chunk) is True

    def test_should_retry_for_auth_error_with_message_heuristic_unauthorized(
        self, executor
    ):
        """Test detection using message-based heuristics for 'unauthorized' keyword."""
        chunk = ProcessedResponse(
            content={
                "error": "Unauthorized request",
                "message": "Access denied",
            }
        )

        assert executor._should_retry_for_auth_error(chunk) is True

    def test_should_retry_for_auth_error_with_message_heuristic_token_expired(
        self, executor
    ):
        """Test detection using message-based heuristics for 'token expired'."""
        chunk = ProcessedResponse(
            content={
                "error": "Token has expired",
                "message": "Please refresh your token",
            }
        )

        assert executor._should_retry_for_auth_error(chunk) is True

    def test_should_retry_for_auth_error_with_message_heuristic_in_error_flag(
        self, executor
    ):
        """Test detection when auth keywords are in error flag."""
        chunk = ProcessedResponse(
            content={
                "error": "401 Unauthorized",
                "details": {},
            }
        )

        assert executor._should_retry_for_auth_error(chunk) is True

    def test_should_retry_for_auth_error_with_message_heuristic_in_message(
        self, executor
    ):
        """Test detection when auth keywords are in message field."""
        chunk = ProcessedResponse(
            content={
                "message": "403 Forbidden - Invalid credentials",
                "details": {},
            }
        )

        assert executor._should_retry_for_auth_error(chunk) is True

    def test_should_retry_for_auth_error_non_auth_code(self, executor):
        """Test that non-auth codes don't trigger retry."""
        non_auth_codes = [
            "rate_limit",
            "invalid_request",
            "model_not_found",
            "server_error",
            "timeout",
        ]

        for code in non_auth_codes:
            chunk = ProcessedResponse(
                content={
                    "error": "some_error",
                    "details": {"code": code},
                }
            )
            assert (
                executor._should_retry_for_auth_error(chunk) is False
            ), f"Should not detect auth error for code: {code}"

    def test_should_retry_for_auth_error_non_auth_message(self, executor):
        """Test that non-auth messages don't trigger retry."""
        non_auth_messages = [
            "Rate limit exceeded",
            "Model not found",
            "Invalid request format",
            "Server timeout",
            "Network error",
        ]

        for message in non_auth_messages:
            chunk = ProcessedResponse(
                content={
                    "error": "some_error",
                    "message": message,
                    "details": {},
                }
            )
            assert (
                executor._should_retry_for_auth_error(chunk) is False
            ), f"Should not detect auth error for message: {message}"

    def test_should_retry_for_auth_error_combined_heuristics(self, executor):
        """Test detection when multiple heuristics are present."""
        # Status code + code heuristic
        chunk1 = ProcessedResponse(
            content={
                "error": "auth_failed",
                "details": {
                    "status": 401,
                    "code": "invalid_token",
                },
            }
        )
        assert executor._should_retry_for_auth_error(chunk1) is True

        # Status code + message heuristic
        chunk2 = ProcessedResponse(
            content={
                "error": "401 Unauthorized",
                "details": {
                    "status": 403,
                    "message": "Token expired",
                },
            }
        )
        assert executor._should_retry_for_auth_error(chunk2) is True

        # Code + message heuristic (no status code)
        chunk3 = ProcessedResponse(
            content={
                "error": "access_denied",
                "details": {
                    "code": "unauthorized",
                    "message": "401 error occurred",
                },
            }
        )
        assert executor._should_retry_for_auth_error(chunk3) is True

    def test_should_retry_for_auth_error_edge_cases(self, executor):
        """Test edge cases for auth error detection."""
        # Empty content
        chunk1 = ProcessedResponse(content={})
        assert executor._should_retry_for_auth_error(chunk1) is False

        # Non-dict content
        chunk2 = ProcessedResponse(content="string content")
        assert executor._should_retry_for_auth_error(chunk2) is False

        # Chunk without content attribute (raw chunk)
        chunk3 = {"error": "401", "details": {"status": 401}}
        assert executor._should_retry_for_auth_error(chunk3) is True

        # Code as integer status code (should match via status code extraction)
        chunk4 = ProcessedResponse(
            content={
                "details": {"code": 401},  # Integer status code
            }
        )
        # Integer 401 is extracted as a status code and matches auth error
        assert executor._should_retry_for_auth_error(chunk4) is True

        # Code as non-string non-status-code (should not match heuristic)
        chunk4b = ProcessedResponse(
            content={
                "details": {"code": 500},  # Integer, but not auth-related
            }
        )
        assert executor._should_retry_for_auth_error(chunk4b) is False

        # Code as non-string auth-related integer (should match via status code)
        chunk4c = ProcessedResponse(
            content={
                "details": {"code": 403},  # Integer status code
            }
        )
        assert executor._should_retry_for_auth_error(chunk4c) is True

        # Message as non-string (should not match message heuristic)
        chunk5 = ProcessedResponse(
            content={
                "message": {"text": "401 error"},  # Dict, not string
            }
        )
        assert executor._should_retry_for_auth_error(chunk5) is False

    def test_should_retry_for_auth_error_case_insensitive(self, executor):
        """Test that heuristic detection is case-insensitive."""
        # Uppercase code
        chunk1 = ProcessedResponse(
            content={
                "details": {"code": "INVALID_TOKEN"},
            }
        )
        assert executor._should_retry_for_auth_error(chunk1) is True

        # Mixed case message
        chunk2 = ProcessedResponse(
            content={
                "error": "401 UnAuThOrIzEd",
            }
        )
        assert executor._should_retry_for_auth_error(chunk2) is True

        # Lowercase with mixed case keyword
        chunk3 = ProcessedResponse(
            content={
                "message": "Token Has Expired",
            }
        )
        assert executor._should_retry_for_auth_error(chunk3) is True

    def test_get_retry_delay(self, executor):
        """Test retry delay calculation."""
        assert executor._get_retry_delay(0) == 0.1
        assert executor._get_retry_delay(1) == 0.2
        assert executor._get_retry_delay(2) == 0.2  # Uses last value
        assert executor._get_retry_delay(-1) == 0.0

    def test_extract_tool_calls_reads_responses_output_items(self, executor):
        """Responses-format output arrays should be inspected for tool calls."""
        response_like = {
            "output": [
                {"type": "reasoning", "summary": []},
                {"type": "function_call", "name": "apply_patch"},
            ]
        }

        tool_calls = executor._extract_tool_calls(response_like)

        assert tool_calls == [{"function": {"name": "apply_patch"}}]

    def test_extract_tool_calls_reads_stream_event_item(self, executor):
        """Streaming event items should surface Codex-native tool names."""
        response_like = {
            "type": "response.output_item.added",
            "item": {"type": "local_shell_call", "name": "bash"},
        }

        tool_calls = executor._extract_tool_calls(response_like)

        assert tool_calls == [{"function": {"name": "bash"}}]

    def test_chunk_has_client_visible_output_for_tool_call_events(self, executor):
        chunk = ProcessedResponse(
            content={
                "type": "response.output_item.added",
                "item": {"type": "function_call", "name": "apply_patch"},
            }
        )

        assert executor._chunk_has_client_visible_output(chunk) is True

    @pytest.mark.asyncio
    async def test_effective_rate_limit_max_retries_delegates_to_credentials(
        self, executor, mock_credential_manager
    ):
        """Executor should expand retry budget when credential manager says so."""

        async def _eff(floor: int) -> int:
            return max(floor, 5)

        mock_credential_manager.effective_max_rate_limit_retries = _eff
        assert await executor._effective_rate_limit_max_retries() == 5
