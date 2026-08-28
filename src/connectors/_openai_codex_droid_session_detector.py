"""Droid client session detector.

Detects Factory Droid clients from request metadata (headers, system prompt, tools).
Used to enable Droid-specific tool translation when routing through Codex backend.
"""

from __future__ import annotations

import logging
import re
from collections.abc import Mapping
from dataclasses import dataclass, field
from typing import Any

from src.core.app.constants.logging_constants import TRACE_LEVEL

logger = logging.getLogger(__name__)


@dataclass
class DroidDetectionResult:
    """Result of Droid client detection."""

    is_droid: bool = False
    detection_method: str = ""
    confidence: float = 0.0
    details: dict[str, Any] = field(default_factory=dict)


class DroidSessionDetector:
    """Detects Factory Droid clients from request metadata.

    Detection methods (in order of priority):
    1. User-Agent header containing 'factory-cli' or 'droid'
    2. System prompt mentioning 'Droid'
    3. Presence of Droid-specific tool names (Read, LS, Execute, etc.)

    Detection is case-insensitive.
    """

    # User-Agent patterns that indicate Droid
    DROID_USER_AGENT_PATTERNS = [
        "factory-cli",
        "factory_cli",
        "factorydroid",
        "droid",
    ]

    # Keywords in system prompt that indicate Droid
    DROID_SYSTEM_PROMPT_KEYWORDS = [
        "you are droid",
        "droid, an ai",
        "factory droid",
    ]

    # Tool names that are unique to Droid (PascalCase)
    DROID_TOOL_NAMES = {
        "Read",
        "LS",
        "Execute",
        "Edit",
        "Grep",
        "Glob",
        "Create",
        "TodoWrite",
        "WebSearch",
        "FetchUrl",
        "ExitSpecMode",
    }

    def detect(
        self,
        *,
        headers: Mapping[str, str] | None = None,
        messages: list[dict[str, Any]] | None = None,
        tools: list[dict[str, Any]] | None = None,
    ) -> DroidDetectionResult:
        """Detect if the request is from a Droid client.

        Args:
            headers: HTTP headers from the request
            messages: Chat messages (to check system prompt)
            tools: Tool definitions from the request

        Returns:
            DroidDetectionResult with is_droid flag and detection details
        """
        # Try User-Agent detection first (most reliable)
        if headers:
            result = self._detect_from_user_agent(headers)
            if result.is_droid:
                return result

        # Try system prompt detection
        if messages:
            result = self._detect_from_system_prompt(messages)
            if result.is_droid:
                return result

        # Try tool names detection
        if tools:
            result = self._detect_from_tool_names(tools)
            if result.is_droid:
                return result

        # No detection
        return DroidDetectionResult(is_droid=False)

    def _detect_from_user_agent(
        self, headers: Mapping[str, str]
    ) -> DroidDetectionResult:
        """Detect Droid from User-Agent header using token-based matching.

        Tokenizes the user agent to avoid false positives from substring matches
        (e.g., 'my_factory_client' should not match 'factory_cli').
        """
        user_agent = headers.get("User-Agent", "")
        if not user_agent:
            # Try lowercase key
            user_agent = headers.get("user-agent", "")

        user_agent_lower = user_agent.lower()
        user_agent_tokens = {
            token for token in re.split(r"[^a-z0-9]+", user_agent_lower) if token
        }

        for pattern in self.DROID_USER_AGENT_PATTERNS:
            pattern_tokens = {
                token for token in re.split(r"[^a-z0-9]+", pattern) if token
            }
            if pattern_tokens.issubset(user_agent_tokens):
                if logger.isEnabledFor(TRACE_LEVEL):
                    logger.log(
                        TRACE_LEVEL,
                        "Detected Droid from User-Agent: %s (matched: %s)",
                        user_agent,
                        pattern,
                    )
                return DroidDetectionResult(
                    is_droid=True,
                    detection_method="user_agent",
                    confidence=1.0,
                    details={"user_agent": user_agent, "matched_pattern": pattern},
                )

        return DroidDetectionResult(is_droid=False)

    def _detect_from_system_prompt(
        self, messages: list[dict[str, Any]]
    ) -> DroidDetectionResult:
        """Detect Droid from system prompt content."""
        for message in messages:
            if message.get("role") == "system":
                content = message.get("content", "")
                if isinstance(content, str):
                    content_lower = content.lower()
                    for keyword in self.DROID_SYSTEM_PROMPT_KEYWORDS:
                        if keyword in content_lower:
                            if logger.isEnabledFor(TRACE_LEVEL):
                                logger.log(
                                    TRACE_LEVEL,
                                    "Detected Droid from system prompt (matched: %s)",
                                    keyword,
                                )
                            return DroidDetectionResult(
                                is_droid=True,
                                detection_method="system_prompt",
                                confidence=0.9,
                                details={"matched_keyword": keyword},
                            )

        return DroidDetectionResult(is_droid=False)

    def _detect_from_tool_names(
        self, tools: list[dict[str, Any]]
    ) -> DroidDetectionResult:
        """Detect Droid from tool names.

        Droid uses PascalCase tool names like Read, LS, Execute.
        If we find multiple Droid-specific tools, it's likely a Droid client.
        """
        found_droid_tools: list[str] = []

        for tool in tools:
            if tool.get("type") == "function":
                func = tool.get("function", {})
                name = func.get("name", "")
                if name in self.DROID_TOOL_NAMES:
                    found_droid_tools.append(name)

        # Require at least 2 Droid-specific tools for detection
        # (to avoid false positives from common tool names)
        if len(found_droid_tools) >= 2:
            if logger.isEnabledFor(TRACE_LEVEL):
                logger.log(
                    TRACE_LEVEL,
                    "Detected Droid from tool names: %s",
                    found_droid_tools,
                )
            return DroidDetectionResult(
                is_droid=True,
                detection_method="tool_names",
                confidence=0.8,
                details={"found_tools": found_droid_tools},
            )

        return DroidDetectionResult(is_droid=False)
