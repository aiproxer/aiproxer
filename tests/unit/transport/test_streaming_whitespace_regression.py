import pytest
from src.core.domain.chat import CanonicalStreamChunk
from src.core.domain.streaming.parsing.sse_bytes_parser import SSEBytesParser
from src.core.domain.streaming.parsing.sse_string_parser import SSEStringParser
from src.core.domain.translators.gemini.streaming import gemini_to_domain_stream_chunk
from src.core.transport.fastapi.adapters.sse.decoder import SSEDecoder


class TestStreamingWhitespaceRegression:
    """
    Regression tests to ensure whitespace and newlines are preserved throughout
    the streaming pipeline. These tests prevent bugs where '.strip()' or '.lstrip()'
    accidentally remove significant formatting.
    """

    def test_sse_decoder_preserves_significant_whitespace(self):
        """
        Verify that SSEDecoder follow the SSE spec: remove AT MOST one leading space
        after 'data:' and preserve all other whitespaces, including trailing ones.
        """
        decoder = SSEDecoder()

        # Test 1: Trailing space preservation
        chunk1 = "data: word \n\n"
        decoded1 = decoder.decode_payload(chunk1)
        assert decoded1.content == "word ", "Should preserve trailing space"

        # Test 2: Multiple leading spaces (only first is removed)
        chunk2 = "data:  leading space\n\n"
        decoded2 = decoder.decode_payload(chunk2)
        assert (
            decoded2.content == " leading space"
        ), "Should only remove one leading space"

        # Test 3: Newline preservation between multiple data lines
        chunk3 = "data: line1\ndata: \ndata: line3\n\n"
        decoded3 = decoder.decode_payload(chunk3)
        # SSE spec: multiple data lines are joined by LF
        assert (
            decoded3.content == "line1\n\nline3"
        ), "Should preserve empty data lines as newlines"

        # Test 4: Leading newline in payload
        chunk4 = "data: \ndata: payload\n\n"
        decoded4 = decoder.decode_payload(chunk4)
        assert (
            decoded4.content == "\npayload"
        ), "Should preserve leading newline via empty data line"

    def test_sse_string_parser_preserves_whitespace(self):
        """Verify SSEStringParser does not strip content."""
        parser = SSEStringParser()

        # Leading/trailing whitespace in the raw SSE string should be handled carefully
        raw = "  data:  spaced content  \n\n"
        assert parser.can_parse(raw)

        parsed = parser.parse(raw)
        # It recursively calls from_raw -> PlainStringParser if not JSON
        assert (
            parsed.content == " spaced content  "
        ), "Should preserve spaces in extracted SSE part"

    def test_sse_bytes_parser_preserves_whitespace(self):
        """Verify SSEBytesParser does not strip content."""
        parser = SSEBytesParser()

        # Bytes with significant whitespace
        raw = b"data:  bytes with spaces  \n\n"
        assert parser.can_parse(raw)

        parsed = parser.parse(raw)
        assert (
            parsed.content == " bytes with spaces  "
        ), "Should preserve spaces in extracted bytes SSE part"

    def test_gemini_translator_preserves_reasoning_whitespace(self):
        """
        Verify that gemini_to_domain_stream_chunk does not strip whitespace
        from reasoning content pieces.
        """
        # Simulated Gemini chunk with reasoning part containing significant spaces
        gemini_chunk = {
            "candidates": [
                {
                    "content": {
                        "parts": [
                            {"type": "reasoning", "text": " thinking about "},
                            {"text": "final answer"},
                        ]
                    }
                }
            ]
        }

        domain_chunk = gemini_to_domain_stream_chunk(gemini_chunk)
        assert isinstance(domain_chunk, CanonicalStreamChunk)

        delta = domain_chunk.choices[0].delta
        assert (
            delta.reasoning_content == " thinking about "
        ), "Should preserve spaces in reasoning content"
        assert delta.content == "final answer"

    def test_gemini_translator_joins_reasoning_without_stripping(self):
        """
        Verify that multiple reasoning segments are joined with newlines
        but individual segments are not stripped.
        """
        gemini_chunk = {
            "candidates": [
                {
                    "content": {
                        "parts": [
                            {"type": "reasoning", "text": "part 1 "},
                            {"type": "reasoning", "text": " part 2"},
                        ]
                    }
                }
            ]
        }

        domain_chunk = gemini_to_domain_stream_chunk(gemini_chunk)
        delta = domain_chunk.choices[0].delta
        # They are joined with \n currently
        assert (
            delta.reasoning_content == "part 1 \n part 2"
        ), "Should preserve internal spaces when joining"

    def test_openai_dict_parser_preserves_whitespace(self):
        """Verify OpenAIDictParser preserves whitespace in deltas."""
        from src.core.domain.streaming.parsing.openai_dict_parser import (
            OpenAIDictParser,
        )

        parser = OpenAIDictParser()

        raw_dict = {
            "choices": [
                {
                    "delta": {
                        "content": "  leading and trailing  ",
                        "reasoning_content": "\nline break\n",
                    }
                }
            ]
        }

        parsed = parser.parse(raw_dict)
        assert parsed.content == "  leading and trailing  "
        assert parsed.metadata["reasoning_content"] == "\nline break\n"


if __name__ == "__main__":
    # Allow running directly for quick verification
    pytest.main([__file__])
