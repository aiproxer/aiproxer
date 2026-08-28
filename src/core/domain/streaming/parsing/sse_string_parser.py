"""
SSE string parser.

This parser handles strings containing Server-Sent Events format.
"""

from __future__ import annotations

import json
from typing import Any

from src.core.domain.streaming.parsing.parser_strategy import IParserStrategy
from src.core.domain.streaming.streaming_content import StreamingContent


class SSEStringParser(IParserStrategy):
    """Parser for SSE strings.

    Handles strings starting with 'data: ' prefix, including '[DONE]' markers.
    """

    def can_parse(self, raw_data: Any) -> bool:
        """Check if input is a string with SSE format.

        Args:
            raw_data: The raw data to check

        Returns:
            True if raw_data is a string starting with 'data: '
        """
        if not isinstance(raw_data, str):
            return False

        # Check if it starts with JSON-like structure (handled by JSONStringParser)
        # Use lstrip to check prefix without modifying the original string
        check_str = raw_data.lstrip()
        if check_str.startswith(("{", "[")):
            return False

        # SSE format starts with 'data: '
        return check_str.startswith("data: ")

    def parse(self, raw_data: Any) -> StreamingContent:
        """Parse SSE string into StreamingContent.

        Args:
            raw_data: String containing SSE format

        Returns:
            StreamingContent with parsed content

        Raises:
            ValueError: If raw_data is not a string
        """
        if not isinstance(raw_data, str):
            raise ValueError(f"Expected str, got {type(raw_data).__name__}")

        # Handle Server-Sent Events format
        # Strip only leading whitespace to find the data: prefix
        # but be careful to only remove at most one space after 'data:'
        # Actually, simpler: find the first "data: " and take everything after it
        # or follow SSE spec properly.

        # Current implementation is a bit naive but we should at least not strip everything.
        # Find where data: starts
        data_idx = raw_data.find("data:")
        if data_idx == -1:
            return StreamingContent(content=raw_data, raw_data=raw_data)

        # Extract the line(s)
        # For simplicity in this parser, we assume it's one data block
        sse_part = raw_data[data_idx + 5 :]
        if sse_part.startswith(" "):
            sse_part = sse_part[1:]

        # We still need to handle multiple data: lines if they exist,
        # but usually this parser handles single chunks.

        if sse_part.strip() == "[DONE]":
            return StreamingContent(is_done=True, raw_data=raw_data)
        else:
            try:
                # SSE spec says we should strip the trailing CRLF/LF that terminates the event
                # but we should be careful not to strip internal whitespaces.
                # Usually single chunks end with \n\n or \n.
                content_to_parse = sse_part
                if content_to_parse.endswith("\n\n"):
                    content_to_parse = content_to_parse[:-2]
                elif content_to_parse.endswith("\n"):
                    content_to_parse = content_to_parse[:-1]

                parsed_json = json.loads(content_to_parse)
                # Recursively parse the JSON using StreamingContent.from_raw
                return StreamingContent.from_raw(parsed_json)
            except json.JSONDecodeError:
                # If it's not JSON, it's plain text. We should still handle the SSE terminator.
                text_content = sse_part
                if text_content.endswith("\n\n"):
                    text_content = text_content[:-2]
                elif text_content.endswith("\n"):
                    text_content = text_content[:-1]
                return StreamingContent(content=text_content, raw_data=raw_data)


__all__ = ["SSEStringParser"]
