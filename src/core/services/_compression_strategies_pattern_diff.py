"""Output pattern matching and unified diff compaction strategies."""

from __future__ import annotations

import json
import re
import subprocess
import sys
from contextlib import suppress
from dataclasses import dataclass
from re import Pattern

from src.core.domain.configuration.dynamic_compression_config import CompressionLevel
from src.core.domain.dynamic_compression import ToolOutputContext
from src.core.services._compression_strategies_common import (
    _REGEX_EVAL_SUBPROCESS_SNIPPET,
    _preserve_trailing_newline,
    logger,
)


@dataclass(frozen=True)
class OutputPatternMatchRule:
    """Declarative full-output replacement rule."""

    pattern: str
    message: str
    unless: str | None = None
    fallback_message: str = "tool: ok"


@dataclass(frozen=True)
class _CompiledOutputPatternRule:
    pattern: Pattern[str]
    message: str
    unless: Pattern[str] | None
    fallback_message: str


class OutputPatternMatchStrategy:
    """Match full output against patterns with unless guards and fallback."""

    _max_regex_input_chars = 200_000

    def __init__(
        self,
        *,
        rules: list[OutputPatternMatchRule] | None = None,
        regex_timeout_ms: int = 25,
    ) -> None:
        self._regex_timeout_ms = max(1, int(regex_timeout_ms))
        self._rules = self._compile_rules(rules or [])

    def compress(
        self,
        content: str,
        *,
        context: ToolOutputContext,
        level: CompressionLevel,
    ) -> str:
        if not self._rules:
            return content

        source_content = content
        content_reduced_to_empty = False
        if not source_content and context.content:
            source_content = context.content
            content_reduced_to_empty = True
        if not source_content:
            return content

        full_text = (
            source_content[: self._max_regex_input_chars]
            if len(source_content) > self._max_regex_input_chars
            else source_content
        )
        for rule in self._rules:
            matched, timed_out = self._search_with_timeout(rule.pattern, full_text)
            if timed_out:
                continue
            if not matched:
                continue

            if rule.unless is not None:
                excluded, timed_out = self._search_with_timeout(rule.unless, full_text)
                if timed_out:
                    continue
                if excluded:
                    continue

            replacement = rule.message.strip()
            if replacement:
                return replacement

            fallback = rule.fallback_message.strip() or "tool: ok"
            return fallback
        if content_reduced_to_empty:
            return self._resolve_empty_fallback_message()
        return content

    def _compile_rules(
        self,
        rules: list[OutputPatternMatchRule],
    ) -> list[_CompiledOutputPatternRule]:
        compiled: list[_CompiledOutputPatternRule] = []
        for rule in rules:
            try:
                pattern = re.compile(rule.pattern)
                unless = re.compile(rule.unless) if rule.unless else None
            except re.error:
                # Fail-open: skip malformed regex rules.
                continue
            compiled.append(
                _CompiledOutputPatternRule(
                    pattern=pattern,
                    message=rule.message,
                    unless=unless,
                    fallback_message=rule.fallback_message,
                )
            )
        return compiled

    def _search_with_timeout(
        self,
        pattern: Pattern[str],
        text: str,
    ) -> tuple[bool, bool]:
        timeout_seconds = self._regex_timeout_ms / 1000.0
        if sys.platform.startswith("win"):
            timeout_seconds = max(timeout_seconds, 0.2)
        payload = json.dumps(
            {
                "pattern": pattern.pattern,
                "flags": int(pattern.flags),
                "text": text,
            }
        )
        worker: subprocess.Popen[str] | None = None
        try:
            worker = subprocess.Popen(
                [sys.executable, "-c", _REGEX_EVAL_SUBPROCESS_SNIPPET],
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                text=True,
            )
            stdout_data, _ = worker.communicate(payload, timeout=timeout_seconds)
            if worker.returncode not in (0, None):
                return False, True
            return stdout_data.strip() == "1", False
        except subprocess.TimeoutExpired:
            if worker is not None:
                with suppress(Exception):
                    worker.kill()
                with suppress(Exception):
                    worker.communicate(timeout=0.05)
            return False, True
        except Exception:
            logger.debug(
                "output_pattern_match bounded regex evaluation failed",
                exc_info=True,
            )
            return False, True
        finally:
            if worker is not None:
                with suppress(Exception):
                    worker.kill()
                with suppress(Exception):
                    worker.communicate(timeout=0.05)

    def _resolve_empty_fallback_message(self) -> str:
        for rule in self._rules:
            fallback = rule.fallback_message.strip()
            if fallback:
                return fallback
        return "tool: ok"


class DiffCompactStrategy:
    """Compact unified diff output while preserving key review context."""

    _stat_format_flags = {"--stat", "--numstat", "--shortstat"}

    def __init__(
        self,
        *,
        max_hunk_lines: int = 100,
        max_total_lines: int = 500,
        single_file_hunk_boost: int = 60,
    ) -> None:
        self._max_hunk_lines = max(1, int(max_hunk_lines))
        self._max_total_lines = max(10, int(max_total_lines))
        self._single_file_hunk_boost = max(0, int(single_file_hunk_boost))

    def compress(
        self,
        content: str,
        *,
        context: ToolOutputContext,
        level: CompressionLevel,
    ) -> str:
        if not content:
            return content

        if context.has_explicit_format and any(
            flag in self._stat_format_flags
            for flag in context.identity.explicit_format_flags
        ):
            return content

        if not self._looks_like_unified_diff(content):
            return content

        max_hunk_lines, max_total_lines = self._limits_for_level(level)

        file_count = content.count("diff --git ")
        if file_count <= 1:
            max_hunk_lines += self._single_file_hunk_boost
            max_total_lines += self._single_file_hunk_boost * 4

        compacted = self._compact_diff(
            diff=content,
            max_hunk_lines=max_hunk_lines,
            max_total_lines=max_total_lines,
        )
        return _preserve_trailing_newline(original=content, transformed=compacted)

    def _limits_for_level(self, level: CompressionLevel) -> tuple[int, int]:
        if level == CompressionLevel.CONSERVATIVE:
            return self._max_hunk_lines + 40, self._max_total_lines + 300
        if level == CompressionLevel.AGGRESSIVE:
            return max(20, self._max_hunk_lines - 20), max(
                120, self._max_total_lines - 150
            )
        return self._max_hunk_lines + 20, self._max_total_lines + 100

    @staticmethod
    def _looks_like_unified_diff(content: str) -> bool:
        lines = content.splitlines()
        has_hunk_headers = any(line.startswith("@@ ") for line in lines)
        if not has_hunk_headers:
            return False
        has_git_header = any(line.startswith("diff --git ") for line in lines)
        has_unified_headers = any(line.startswith("--- ") for line in lines) and any(
            line.startswith("+++ ") for line in lines
        )
        return has_git_header or has_unified_headers

    @staticmethod
    def _extract_file_name(diff_header: str) -> str:
        if " b/" in diff_header:
            return diff_header.split(" b/", maxsplit=1)[1].strip()
        return "unknown"

    @staticmethod
    def _extract_file_name_from_unified_header(header: str) -> str | None:
        payload = header[4:].strip() if len(header) >= 4 else ""
        if not payload:
            return None
        if "\t" in payload:
            payload = payload.split("\t", maxsplit=1)[0].strip()
        payload = payload.strip('"')
        if payload.startswith(("a/", "b/")):
            payload = payload[2:]
        if payload == "/dev/null":
            return None
        return payload or None

    def _compact_diff(
        self,
        *,
        diff: str,
        max_hunk_lines: int,
        max_total_lines: int,
    ) -> str:
        result: list[str] = []
        current_file = ""
        added = 0
        removed = 0
        in_hunk = False
        hunk_shown = 0
        hunk_skipped = 0
        truncated = False
        file_name_line_index: int | None = None
        hunk_count = 0
        leading_context_left = 0
        seen_change_in_hunk = False

        def flush_hunk() -> None:
            nonlocal hunk_skipped, truncated
            if hunk_skipped > 0:
                result.append(
                    f"  ... ({hunk_skipped} lines skipped in this hunk; "
                    f"use 'git diff <file>' for full content)"
                )
                truncated = True
                hunk_skipped = 0

        def flush_file_stats() -> None:
            nonlocal file_name_line_index
            if file_name_line_index is None:
                return
            if current_file and (added or removed or hunk_count):
                parts: list[str] = []
                if added or removed:
                    parts.append(f"+{added} -{removed}")
                if hunk_count:
                    parts.append(f"{hunk_count} hunks")
                if parts:
                    result[file_name_line_index] = (
                        f"{current_file}  ({' | '.join(parts)})"
                    )
            file_name_line_index = None

        def begin_file(file_name: str) -> None:
            nonlocal current_file, added, removed, in_hunk, hunk_shown, hunk_count, file_name_line_index
            current_file = file_name or "unknown"
            if result:
                result.append("")
            file_name_line_index = len(result)
            result.append(current_file)
            hunk_count = 0
            added = 0
            removed = 0
            in_hunk = False
            hunk_shown = 0

        lines = diff.splitlines()
        index = 0
        while index < len(lines):
            line = lines[index]
            if line.startswith("diff --git "):
                flush_hunk()
                flush_file_stats()
                begin_file(self._extract_file_name(line))
            elif line.startswith("--- ") and index + 1 < len(lines):
                next_line = lines[index + 1]
                if next_line.startswith("+++ "):
                    old_name = self._extract_file_name_from_unified_header(line)
                    new_name = (
                        self._extract_file_name_from_unified_header(next_line)
                        or old_name
                        or "unknown"
                    )
                    if not current_file:
                        begin_file(new_name)
                    elif new_name != current_file:
                        flush_hunk()
                        flush_file_stats()
                        begin_file(new_name)
                    else:
                        in_hunk = False
                        hunk_shown = 0
                    index += 2
                    if len(result) >= max_total_lines:
                        result.append(
                            f"... (remaining {current_file} changes and "
                            f"other files omitted; use specific 'git diff "
                            f"<file>' for focused review)"
                        )
                        truncated = True
                        break
                    continue
            elif line.startswith("@@ "):
                flush_hunk()
                if not current_file:
                    begin_file("unknown")
                in_hunk = True
                hunk_shown = 0
                leading_context_left = 2
                seen_change_in_hunk = False
                hunk_count += 1
                result.append(f"  {line}")
            elif in_hunk:
                is_added = line.startswith("+") and not line.startswith("+++")
                is_removed = line.startswith("-") and not line.startswith("---")
                is_context = (
                    not is_added and not is_removed and not line.startswith("\\")
                )

                if is_added:
                    added += 1
                    seen_change_in_hunk = True
                elif is_removed:
                    removed += 1
                    seen_change_in_hunk = True

                if not (is_added or is_removed or is_context):
                    index += 1
                    continue

                if is_context:
                    if leading_context_left > 0 and hunk_shown < max_hunk_lines:
                        result.append(f"  {line}")
                        leading_context_left -= 1
                        hunk_shown += 1
                    elif not seen_change_in_hunk:
                        pass
                    elif hunk_shown < max_hunk_lines:
                        result.append(f"  {line}")
                        hunk_shown += 1
                    else:
                        hunk_skipped += 1
                elif hunk_shown < max_hunk_lines:
                    result.append(f"  {line}")
                    hunk_shown += 1
                else:
                    hunk_skipped += 1

            if len(result) >= max_total_lines:
                result.append(
                    "... (remaining files omitted; "
                    "use 'git diff --stat' for overview or "
                    "'git diff <file>' for focused review)"
                )
                truncated = True
                break
            index += 1

        flush_hunk()
        flush_file_stats()

        if truncated:
            result.append(
                "[diff output was compacted; use 'git diff <file>' "
                "to see full changes for a specific file]"
            )

        return "\n".join(result)
