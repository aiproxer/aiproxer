from __future__ import annotations

import re
import time

import pytest
from src.core.domain.configuration.dynamic_compression_config import CompressionLevel
from src.core.domain.dynamic_compression import ToolOutputContext
from src.core.services.compression_strategies import (
    AnsiNormalizeStrategy,
    DiffCompactStrategy,
    DirectoryTreeSummaryStrategy,
    FailurePreservingTruncateStrategy,
    FileDetailLevelsStrategy,
    LineDedupeStrategy,
    MutatingSuccessAckStrategy,
    OutputPatternMatchRule,
    OutputPatternMatchStrategy,
    SearchResultsGroupingStrategy,
    SimilarityGroupingStrategy,
    StatsExtractionSummaryStrategy,
)


def _context_for(
    content: str,
    *,
    command_signature: str = "pytest",
    command_prefix: str | None = None,
    explicit_format_flags: list[str] | None = None,
    has_explicit_format: bool = False,
) -> ToolOutputContext:
    context = ToolOutputContext.for_text(
        tool_name="shell",
        tool_category="command_execution",
        command_signature=command_signature,
        command_prefix=command_prefix,
        content=content,
    )
    if explicit_format_flags or has_explicit_format:
        identity = context.identity.model_copy(
            update={"explicit_format_flags": explicit_format_flags or []}
        )
        context = context.model_copy(
            update={
                "identity": identity,
                "has_explicit_format": has_explicit_format
                or bool(explicit_format_flags),
            }
        )
    return context


def test_ansi_normalize_strips_osc_and_control_noise() -> None:
    strategy = AnsiNormalizeStrategy()
    content = (
        "\x1b]8;;https://example.com\x1b\\click here\x1b]8;;\x1b\\\n"
        "\x1b[31mERROR\x1b[0m\x00\x07\x1b[?25l\n"
    )

    compressed = strategy.compress(
        content,
        context=_context_for(content),
        level=CompressionLevel.BALANCED,
    )

    assert "click here" in compressed
    assert "https://example.com" not in compressed
    assert "\x1b" not in compressed
    assert "\x00" not in compressed
    assert "\x07" not in compressed
    assert compressed.endswith("\n")


def test_ansi_normalize_collapses_carriage_return_spinner_frames() -> None:
    strategy = AnsiNormalizeStrategy()
    content = "|\r/\r-\r\\\rDone\nnext\n"

    compressed = strategy.compress(
        content,
        context=_context_for(content),
        level=CompressionLevel.CONSERVATIVE,
    )

    assert compressed == "Done\nnext\n"


def test_ansi_normalize_drops_spinner_only_lines() -> None:
    strategy = AnsiNormalizeStrategy()
    content = "|\n/\n-\n\\\nready\n"

    compressed = strategy.compress(
        content,
        context=_context_for(content),
        level=CompressionLevel.AGGRESSIVE,
    )

    assert compressed == "ready\n"


def test_ansi_normalize_spinner_only_lines_are_level_aware() -> None:
    strategy = AnsiNormalizeStrategy()
    content = "|\n/\n-\n\\\nready\n"

    conservative = strategy.compress(
        content,
        context=_context_for(content),
        level=CompressionLevel.CONSERVATIVE,
    )
    aggressive = strategy.compress(
        content,
        context=_context_for(content),
        level=CompressionLevel.AGGRESSIVE,
    )

    assert conservative.startswith("|\n/\n-\n\\\n")
    assert aggressive == "ready\n"


def test_line_dedupe_is_level_aware() -> None:
    strategy = LineDedupeStrategy()
    content = "repeat\nrepeat\nrepeat\n"

    conservative = strategy.compress(
        content,
        context=_context_for(content),
        level=CompressionLevel.CONSERVATIVE,
    )
    balanced = strategy.compress(
        content,
        context=_context_for(content),
        level=CompressionLevel.BALANCED,
    )
    aggressive = strategy.compress(
        content,
        context=_context_for(content),
        level=CompressionLevel.AGGRESSIVE,
    )

    assert conservative == "repeat (x3)\n"
    assert balanced == "repeat (x3)\n"
    assert aggressive == "repeat (x3)\n"

    mixed = "a\na\nb\nb\nb\n"
    conservative_mixed = strategy.compress(
        mixed,
        context=_context_for(mixed),
        level=CompressionLevel.CONSERVATIVE,
    )
    balanced_mixed = strategy.compress(
        mixed,
        context=_context_for(mixed),
        level=CompressionLevel.BALANCED,
    )
    assert conservative_mixed == "a\na\nb (x3)\n"
    assert balanced_mixed == "a (x2)\nb (x3)\n"


def test_line_dedupe_aggressive_dedupes_non_consecutive_lines() -> None:
    strategy = LineDedupeStrategy()
    content = "repeat\ninfo\nrepeat\ninfo\n"

    compressed = strategy.compress(
        content,
        context=_context_for(content),
        level=CompressionLevel.AGGRESSIVE,
    )

    assert compressed == "repeat (x2)\ninfo (x2)\n"


def test_line_dedupe_reduces_repeated_blocks_and_preserves_failure_lines() -> None:
    strategy = LineDedupeStrategy()
    content = (
        "step A\n"
        "step B\n"
        "step A\n"
        "step B\n"
        "step A\n"
        "step B\n"
        "ERROR: failed to connect\n"
        "ERROR: failed to connect\n"
    )

    compressed = strategy.compress(
        content,
        context=_context_for(content),
        level=CompressionLevel.BALANCED,
    )

    assert "... (previous 2-line block repeated x3)" in compressed
    assert compressed.count("ERROR: failed to connect") == 2
    assert compressed.endswith("\n")


def test_line_dedupe_preserves_trailing_newline_semantics() -> None:
    strategy = LineDedupeStrategy()

    with_newline = "dup\ndup\n"
    without_newline = "dup\ndup"

    compressed_with_newline = strategy.compress(
        with_newline,
        context=_context_for(with_newline),
        level=CompressionLevel.BALANCED,
    )
    compressed_without_newline = strategy.compress(
        without_newline,
        context=_context_for(without_newline),
        level=CompressionLevel.BALANCED,
    )

    assert compressed_with_newline.endswith("\n")
    assert not compressed_without_newline.endswith("\n")


def test_failure_preserving_truncate_keeps_error_windows_and_tail() -> None:
    strategy = FailurePreservingTruncateStrategy()
    total_lines = 260
    lines = [f"line {idx}" for idx in range(total_lines)]
    lines[120] = "ERROR: migration failed"
    lines[121] = "Traceback (most recent call last):"
    lines[122] = "ValueError: cannot continue"
    content = "\n".join(lines) + "\n"

    compressed = strategy.compress(
        content,
        context=_context_for(content),
        level=CompressionLevel.AGGRESSIVE,
    )
    compressed_lines = compressed.splitlines()

    assert len(compressed_lines) <= strategy._level_limits[CompressionLevel.AGGRESSIVE]
    assert "ERROR: migration failed" in compressed
    assert "Traceback (most recent call last):" in compressed
    assert "ValueError: cannot continue" in compressed
    assert "line 0" in compressed
    assert f"line {total_lines - 1}" in compressed
    assert any("lines omitted" in line for line in compressed_lines)
    assert compressed.endswith("\n")


def test_failure_indicator_ignores_zero_failure_summary_lines() -> None:
    strategy = FailurePreservingTruncateStrategy()

    assert (
        strategy._line_indicates_failure("== 120 passed, 0 failed in 1.20s ==") is False
    )
    assert (
        strategy._line_indicates_failure("== 1 failed, 120 passed in 1.20s ==") is True
    )


def test_similarity_grouping_is_level_aware_and_deterministic() -> None:
    strategy = SimilarityGroupingStrategy()
    content = (
        "src/app/main.py:10: E501 line too long\n"
        "src/app/main.py:11: E402 import not at top\n"
        "src/lib/util.py:3: E501 line too long\n"
        "src/app/main.py:18: F401 unused import\n"
        "README.md:1: note\n"
    )

    conservative = strategy.compress(
        content,
        context=_context_for(content),
        level=CompressionLevel.CONSERVATIVE,
    )
    balanced = strategy.compress(
        content,
        context=_context_for(content),
        level=CompressionLevel.BALANCED,
    )
    aggressive = strategy.compress(
        content,
        context=_context_for(content),
        level=CompressionLevel.AGGRESSIVE,
    )

    assert conservative == content
    assert "[group src/app/main.py] (3 items)" in balanced
    assert "[group src/lib/util.py]" not in balanced
    assert balanced.index("[group src/app/main.py]") < balanced.index(
        "README.md:1: note"
    )
    assert "[group src/lib/util.py] (1 items)" in aggressive


def test_failure_preserving_truncate_keeps_only_actionable_failure_context(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    strategy = FailurePreservingTruncateStrategy()
    monkeypatch.setitem(strategy._level_limits, CompressionLevel.AGGRESSIVE, 4)
    monkeypatch.setitem(strategy._failure_window, CompressionLevel.AGGRESSIVE, 3)
    lines = [
        "header",
        "pre context",
        "ERROR: operation failed",
        "Traceback (most recent call last):",
        "ValueError: bad input",
        "tail detail",
        "epilogue",
    ]
    content = "\n".join(lines) + "\n"

    compressed = strategy.compress(
        content,
        context=_context_for(content),
        level=CompressionLevel.AGGRESSIVE,
    )

    assert compressed == content


def test_output_pattern_match_uses_replacement_unless_and_non_empty_fallback(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        OutputPatternMatchStrategy,
        "_search_with_timeout",
        lambda _self, pattern, text: (pattern.search(text) is not None, False),
    )
    rule = OutputPatternMatchRule(
        pattern=r"(?is)build succeeded.*0 errors",
        message="build: ok",
        unless=r"(?i)\berror\b",
        fallback_message="build: ok",
    )
    strategy = OutputPatternMatchStrategy(rules=[rule], regex_timeout_ms=50)
    success = "Build succeeded in 2.0s with 0 errors"
    guarded = "Build succeeded in 2.0s with 0 errors\nerror: flaky network"

    success_result = strategy.compress(
        success,
        context=_context_for(success),
        level=CompressionLevel.BALANCED,
    )
    guarded_result = strategy.compress(
        guarded,
        context=_context_for(guarded),
        level=CompressionLevel.BALANCED,
    )

    assert success_result == "build: ok"
    assert guarded_result == guarded

    empty_rule = OutputPatternMatchRule(
        pattern=r"(?i)all checks passed",
        message="",
        fallback_message="checks: ok",
    )
    empty_strategy = OutputPatternMatchStrategy(rules=[empty_rule], regex_timeout_ms=50)
    empty_result = empty_strategy.compress(
        "All checks passed",
        context=_context_for("All checks passed"),
        level=CompressionLevel.BALANCED,
    )
    assert empty_result == "checks: ok"


def test_output_pattern_match_emits_fallback_when_prior_stages_empty_output() -> None:
    strategy = OutputPatternMatchStrategy(
        rules=[
            OutputPatternMatchRule(
                pattern=r"(?i)all checks passed",
                message="",
                fallback_message="checks: ok",
            )
        ],
        regex_timeout_ms=25,
    )
    original_output = "All checks passed in 2.1s"
    context = _context_for(original_output)

    compressed = strategy.compress(
        "",
        context=context,
        level=CompressionLevel.BALANCED,
    )

    assert compressed == "checks: ok"


def test_output_pattern_match_bounded_regex_search_times_out_deterministically() -> (
    None
):
    strategy = OutputPatternMatchStrategy(
        rules=[
            OutputPatternMatchRule(
                pattern=r"(a+)+$",
                message="matched",
                fallback_message="fallback",
            )
        ],
        regex_timeout_ms=10,
    )
    catastrophic_pattern = re.compile(r"(a+)+$")
    text = ("a" * 12_000) + "!"

    started_at = time.perf_counter()
    matched, timed_out = strategy._search_with_timeout(catastrophic_pattern, text)
    elapsed_ms = (time.perf_counter() - started_at) * 1000.0

    assert matched is False
    assert timed_out is True
    assert elapsed_ms < 3_000


def test_output_pattern_match_timeout_fails_open(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    strategy = OutputPatternMatchStrategy(
        rules=[
            OutputPatternMatchRule(
                pattern=r"(a+)+$",
                message="matched",
                fallback_message="fallback",
            )
        ],
        regex_timeout_ms=1,
    )

    monkeypatch.setattr(
        strategy,
        "_search_with_timeout",
        lambda *_args, **_kwargs: (False, True),
    )
    content = "a" * 2048
    compressed = strategy.compress(
        content,
        context=_context_for(content),
        level=CompressionLevel.BALANCED,
    )

    assert compressed == content


def test_diff_compact_preserves_hunk_headers_stats_and_limits() -> None:
    strategy = DiffCompactStrategy()
    diff_lines = [
        "diff --git a/src/main.py b/src/main.py",
        "--- a/src/main.py",
        "+++ b/src/main.py",
        "@@ -1,2 +1,122 @@ def build():",
        " context line",
    ]
    diff_lines.extend([f"+added line {idx}" for idx in range(120)])
    content = "\n".join(diff_lines) + "\n"

    compressed = strategy.compress(
        content,
        context=_context_for(
            content,
            command_signature="git",
            command_prefix="git diff",
        ),
        level=CompressionLevel.BALANCED,
    )

    assert "src/main.py" in compressed
    assert "@@ -1,2 +1,122 @@ def build():" in compressed
    assert "+120 -0" in compressed


def test_diff_compact_multi_file_truncates_with_actionable_hints() -> None:
    strategy = DiffCompactStrategy(max_hunk_lines=2, max_total_lines=8)
    parts = []
    for i in range(10):
        parts.append(f"diff --git a/src/file_{i}.py b/src/file_{i}.py")
        parts.append(f"--- a/src/file_{i}.py")
        parts.append(f"+++ b/src/file_{i}.py")
        parts.append(f"@@ -1,3 +1,{3 + 20} @@")
        for j in range(20):
            parts.append(f"+file_{i}_added_{j}")
    content = "\n".join(parts) + "\n"

    compressed = strategy.compress(
        content,
        context=_context_for(
            content,
            command_signature="git",
            command_prefix="git diff",
        ),
        level=CompressionLevel.AGGRESSIVE,
    )

    assert "file_0" in compressed
    assert len(compressed) < len(content)


def test_diff_compact_passthrough_when_explicit_stat_format_requested() -> None:
    strategy = DiffCompactStrategy()
    content = (
        "diff --git a/src/app.py b/src/app.py\n"
        "--- a/src/app.py\n"
        "+++ b/src/app.py\n"
        "@@ -1,1 +1,1 @@\n"
        "-old\n"
        "+new\n"
    )
    context = _context_for(
        content,
        command_signature="git",
        command_prefix="git diff",
        explicit_format_flags=["--stat"],
        has_explicit_format=True,
    )

    compressed = strategy.compress(
        content,
        context=context,
        level=CompressionLevel.BALANCED,
    )

    assert compressed == content


def test_diff_compact_supports_generic_unified_diffs_without_git_header() -> None:
    strategy = DiffCompactStrategy()
    content = (
        "--- a/src/module.py\n"
        "+++ b/src/module.py\n"
        "@@ -1,2 +1,4 @@\n"
        " context line\n"
        "-old value\n"
        "+new value\n"
        "+extra value\n"
    )
    context = _context_for(
        content,
        command_signature="diff",
        command_prefix="diff -u before after",
    )

    compressed = strategy.compress(
        content,
        context=context,
        level=CompressionLevel.BALANCED,
    )

    assert "src/module.py" in compressed
    assert "@@ -1,2 +1,4 @@" in compressed
    assert "+2 -1" in compressed


def test_directory_tree_summary_filters_noise_dirs_and_keeps_hierarchy() -> None:
    strategy = DirectoryTreeSummaryStrategy(
        noise_directories=["node_modules", ".git", "__pycache__"]
    )
    content = (
        "src/main.py\n"
        "src/utils/helpers.py\n"
        "tests/test_main.py\n"
        "node_modules/pkg/index.js\n"
        ".git/config\n"
        "README.md\n"
    )
    context = _context_for(
        content,
        command_signature="ls",
        command_prefix="ls -la",
    )

    compressed = strategy.compress(
        content,
        context=context,
        level=CompressionLevel.BALANCED,
    )
    compressed_repeat = strategy.compress(
        content,
        context=context,
        level=CompressionLevel.BALANCED,
    )

    assert compressed == compressed_repeat
    assert "src/" in compressed
    assert "utils/" in compressed
    assert "helpers.py" in compressed
    assert "tests/" in compressed
    assert "README.md" in compressed
    assert "node_modules" not in compressed
    assert ".git" not in compressed
    assert "Summary:" in compressed
    assert compressed.endswith("\n")


def test_directory_tree_summary_noise_filter_is_configurable() -> None:
    strategy = DirectoryTreeSummaryStrategy(noise_directories=["vendor"])
    content = "vendor/cache.bin\n" "node_modules/pkg/index.js\n" "src/app.py\n"
    context = _context_for(
        content,
        command_signature="ls",
        command_prefix="ls -la",
    )

    compressed = strategy.compress(
        content,
        context=context,
        level=CompressionLevel.CONSERVATIVE,
    )

    assert "vendor/" not in compressed
    assert "node_modules/" in compressed
    assert "src/" in compressed


def test_search_results_grouping_preserves_anchors_and_truncates_context() -> None:
    strategy = SearchResultsGroupingStrategy(
        max_matches_per_file=5,
        context_lines=1,
    )
    content = (
        "src/a.py:10:def target()\n"
        "src/a.py:10:def target()\n"
        "src/a.py-11-    context one\n"
        "src/a.py-12-    context two\n"
        "src/b.py:5:def second()\n"
    )
    context = _context_for(
        content,
        command_signature="rg",
        command_prefix="rg target src",
    )

    compressed = strategy.compress(
        content,
        context=context,
        level=CompressionLevel.BALANCED,
    )

    assert "[file] src/a.py" in compressed
    assert "10: def target()" in compressed
    assert "duplicate lines removed" in compressed
    assert "11: context one" in compressed
    assert "context two" not in compressed
    assert "context lines omitted" in compressed
    assert "[file] src/b.py" in compressed


def test_search_results_grouping_retains_bounded_leading_and_trailing_context() -> None:
    strategy = SearchResultsGroupingStrategy(
        max_matches_per_file=5,
        context_lines=1,
    )
    content = (
        "src/a.py-8-    preface\n"
        "src/a.py-9-    setup\n"
        "src/a.py:10:def target()\n"
        "src/a.py-11-    teardown\n"
        "src/a.py-12-    epilogue\n"
    )
    context = _context_for(
        content,
        command_signature="rg",
        command_prefix="rg target src",
    )

    compressed = strategy.compress(
        content,
        context=context,
        level=CompressionLevel.BALANCED,
    )

    assert "9: setup" in compressed
    assert "10: def target()" in compressed
    assert "11: teardown" in compressed
    assert "8: preface" not in compressed
    assert "12: epilogue" not in compressed
    assert "2 context lines omitted" in compressed


def test_search_results_grouping_parses_hyphenated_file_paths_for_context_lines() -> (
    None
):
    strategy = SearchResultsGroupingStrategy(
        max_matches_per_file=5,
        context_lines=2,
    )
    content = (
        "src/my-feature/a-test-file.py-20-    leading context\n"
        "src/my-feature/a-test-file.py:21:def target()\n"
    )
    context = _context_for(
        content,
        command_signature="rg",
        command_prefix="rg target src",
    )

    compressed = strategy.compress(
        content,
        context=context,
        level=CompressionLevel.BALANCED,
    )

    assert "[file] src/my-feature/a-test-file.py" in compressed
    assert "20: leading context" in compressed
    assert "21: def target()" in compressed


def test_file_detail_levels_supports_full_structure_and_signatures_modes() -> None:
    content = (
        "def alpha(x):\n"
        "    y = x + 1\n"
        "    return y\n"
        "\n"
        "class Demo:\n"
        "    def beta(self):\n"
        "        print('x')\n"
        "        return 7\n"
    )
    context = _context_for(
        content,
        command_signature="cat",
        command_prefix="cat src/sample.py",
    )

    full = FileDetailLevelsStrategy(detail_mode="full").compress(
        content,
        context=context,
        level=CompressionLevel.BALANCED,
    )
    structure = FileDetailLevelsStrategy(detail_mode="structure").compress(
        content,
        context=context,
        level=CompressionLevel.BALANCED,
    )
    signatures = FileDetailLevelsStrategy(detail_mode="signatures").compress(
        content,
        context=context,
        level=CompressionLevel.AGGRESSIVE,
    )

    assert full == content
    assert "def alpha(x):" in structure
    assert "class Demo:" in structure
    assert "def beta(self):" in structure
    assert "return y" not in structure
    assert "lines omitted" in structure
    assert "def alpha(x):" in signatures
    assert "class Demo:" in signatures
    assert "print('x')" not in signatures
    assert "lines omitted" in signatures


def test_file_detail_levels_auto_selection_uses_size_and_file_type_heuristics() -> None:
    strategy = FileDetailLevelsStrategy(
        detail_mode="auto",
        auto_full_max_lines=20,
        auto_structure_max_lines=60,
    )
    small_python = "def tiny():\n    return 1\n"
    large_python = "".join(f"def f{idx}():\n    return {idx}\n\n" for idx in range(80))

    small_context = _context_for(
        small_python,
        command_signature="cat",
        command_prefix="cat src/small.py",
    )
    large_context = _context_for(
        large_python,
        command_signature="cat",
        command_prefix="cat src/large.py",
    )

    small_output = strategy.compress(
        small_python,
        context=small_context,
        level=CompressionLevel.BALANCED,
    )
    large_output = strategy.compress(
        large_python,
        context=large_context,
        level=CompressionLevel.AGGRESSIVE,
    )

    assert small_output == small_python
    assert large_output != large_python
    assert "def f0():" in large_output
    assert "lines omitted" in large_output

    json_payload = (
        "{\n"
        '  "workspaces": ["packages/*"],\n'
        '  "scripts": {"build": "bun run --workspaces build"},\n'
        '  "lint-staged": {"**/package.json": ["sort-package-json"]}\n'
        "}\n"
    )
    json_context = _context_for(
        json_payload,
        command_signature="cat",
        command_prefix="cat package.json",
    )
    json_output = strategy.compress(
        json_payload,
        context=json_context,
        level=CompressionLevel.AGGRESSIVE,
    )

    assert "packages/*" in json_output
    assert '"lint-staged"' in json_output
    assert json_output.strip().startswith("{")


def test_file_detail_levels_falls_back_to_safe_output_on_extraction_failure(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    strategy = FileDetailLevelsStrategy(
        detail_mode="structure",
        fallback_mode="full",
    )
    content = "def alpha(x):\n" "    return x\n"
    context = _context_for(
        content,
        command_signature="cat",
        command_prefix="cat src/example.py",
    )

    def _raise_extraction_error(*_args: object, **_kwargs: object) -> str:
        raise RuntimeError("boom")

    monkeypatch.setattr(strategy, "_extract_structure", _raise_extraction_error)
    compressed = strategy.compress(
        content,
        context=context,
        level=CompressionLevel.BALANCED,
    )

    assert compressed == content


def test_file_detail_levels_prefers_known_tools_and_skips_unknown_file_workflows() -> (
    None
):
    strategy = FileDetailLevelsStrategy(detail_mode="signatures")
    content = "def alpha(x):\n" "    return x\n" "\n" "def beta(y):\n" "    return y\n"
    known_context = _context_for(
        content,
        command_signature="cat",
        command_prefix="cat src/example.py",
    )
    unknown_context = _context_for(
        content,
        command_signature="custom_reader",
        command_prefix="custom_reader src/example.py",
    )

    known_output = strategy.compress(
        content,
        context=known_context,
        level=CompressionLevel.AGGRESSIVE,
    )
    unknown_output = strategy.compress(
        content,
        context=unknown_context,
        level=CompressionLevel.AGGRESSIVE,
    )

    assert known_output != content
    assert "lines omitted" in known_output
    assert unknown_output == content


def test_file_detail_levels_applies_deterministic_line_windows_with_markers() -> None:
    strategy = FileDetailLevelsStrategy(
        detail_mode="full",
        max_lines=3,
        last_n_lines=2,
    )
    content = "\n".join(f"line {idx}" for idx in range(1, 11)) + "\n"
    context = _context_for(
        content,
        command_signature="cat",
        command_prefix="cat src/lines.txt",
    )

    compressed_first = strategy.compress(
        content,
        context=context,
        level=CompressionLevel.CONSERVATIVE,
    )
    compressed_second = strategy.compress(
        content,
        context=context,
        level=CompressionLevel.CONSERVATIVE,
    )

    assert compressed_first == compressed_second
    assert compressed_first.startswith("line 1\nline 2\nline 3\n")
    assert "line 9" in compressed_first
    assert "line 10" in compressed_first
    assert "... (5 lines omitted) ..." in compressed_first
    assert compressed_first.endswith("\n")


def test_file_detail_levels_can_include_line_numbers_when_enabled() -> None:
    strategy = FileDetailLevelsStrategy(
        detail_mode="signatures",
        include_line_numbers=True,
    )
    content = (
        "import os\n"
        "\n"
        "def alpha(x):\n"
        "    return x\n"
        "\n"
        "class Demo:\n"
        "    pass\n"
    )
    context = _context_for(
        content,
        command_signature="cat",
        command_prefix="cat src/example.py",
    )

    compressed = strategy.compress(
        content,
        context=context,
        level=CompressionLevel.AGGRESSIVE,
    )

    assert "3: def alpha(x):" in compressed
    assert "6: class Demo:" in compressed
    assert "return x" not in compressed


def test_mutating_success_ack_git_commit_is_deterministic_and_preserves_hash() -> None:
    strategy = MutatingSuccessAckStrategy()
    content = (
        "[feat/parser a1b2c3d4] Add parser support\n"
        " 3 files changed, 10 insertions(+), 2 deletions(-)\n"
        + "remote: counting objects\n" * 20
    )
    ctx = _context_for(
        content,
        command_signature="git",
        command_prefix="git commit",
    )
    first = strategy.compress(content, context=ctx, level=CompressionLevel.BALANCED)
    second = strategy.compress(content, context=ctx, level=CompressionLevel.BALANCED)
    assert first == second
    assert "a1b2c3d4" in first
    assert "feat/parser" in first
    assert "files=3" in first
    assert len(first.encode("utf-8")) < len(content.encode("utf-8"))


def test_mutating_success_ack_fail_open_on_fatal() -> None:
    strategy = MutatingSuccessAckStrategy()
    content = "fatal: not a git repository (or any of the parent directories): .git\n"
    ctx = _context_for(
        content,
        command_signature="git",
        command_prefix="git commit",
    )
    assert (
        strategy.compress(content, context=ctx, level=CompressionLevel.BALANCED)
        == content
    )


def test_mutating_success_ack_passthrough_explicit_format_flags() -> None:
    strategy = MutatingSuccessAckStrategy()
    content = '{"ref": "refs/heads/main"}\n' * 30
    ctx = _context_for(
        content,
        command_signature="git",
        command_prefix="git push",
        has_explicit_format=True,
        explicit_format_flags=["--format"],
    )
    assert (
        strategy.compress(content, context=ctx, level=CompressionLevel.BALANCED)
        == content
    )


def test_stats_extraction_summary_git_status_is_deterministic() -> None:
    strategy = StatsExtractionSummaryStrategy()
    lines = ["## main...origin/main [ahead 2]"]
    lines += [f" M src/module_{i}.py" for i in range(8)]
    content = "\n".join(lines) + "\n"
    ctx = _context_for(
        content,
        command_signature="git",
        command_prefix="git status",
    )
    out = strategy.compress(content, context=ctx, level=CompressionLevel.BALANCED)
    out2 = strategy.compress(content, context=ctx, level=CompressionLevel.BALANCED)
    assert out == out2
    assert "git status" in out
    assert "paths=8" in out
    assert "branch=main" in out
    assert "ahead=2" in out
    assert "upstream=origin/main" in out
    assert "unstaged" in out.lower()
    assert " M src/module_0.py" in out


def test_stats_extraction_summary_git_status_groups_staged_unstaged_untracked() -> None:
    strategy = StatsExtractionSummaryStrategy()
    extra = [f" M src/extra_module_{i:02d}.py" for i in range(20)]
    content = (
        "## feat/x...origin/feat/x [ahead 1]\n"
        "M  src/staged_only.py\n"
        " M src/unstaged_only.py\n"
        "MM src/both.py\n"
        "A  src/new_staged.py\n"
        "?? notes/scratch.md\n"
        "!! build/tmp.bin\n" + "\n".join(extra) + "\n"
    )
    ctx = _context_for(
        content,
        command_signature="git",
        command_prefix="git status",
    )
    out = strategy.compress(content, context=ctx, level=CompressionLevel.BALANCED)
    assert "branch=feat/x" in out
    assert "staged" in out.lower()
    assert "unstaged" in out.lower()
    assert "untracked" in out.lower()
    assert "M  src/staged_only.py" in out
    assert " M src/unstaged_only.py" in out
    assert "MM src/both.py" in out
    assert "?? notes/scratch.md" in out
    assert len(out.encode("utf-8")) < len(content.encode("utf-8"))


def test_diff_compact_keeps_leading_context_and_file_hunk_summary() -> None:
    strategy = DiffCompactStrategy()
    diff_lines = [
        "diff --git a/src/main.py b/src/main.py",
        "--- a/src/main.py",
        "+++ b/src/main.py",
        "@@ -10,3 +10,4 @@ def build():",
        " context one",
        " context two",
        "-old",
        "+new",
    ]
    content = "\n".join(diff_lines) + "\n"
    compressed = strategy.compress(
        content,
        context=_context_for(
            content,
            command_signature="git",
            command_prefix="git diff",
        ),
        level=CompressionLevel.BALANCED,
    )
    assert "src/main.py" in compressed
    assert "context one" in compressed
    assert "context two" in compressed
    assert "+1 -1" in compressed
    assert "hunk" in compressed.lower()


def test_stats_extraction_summary_skips_unified_diff_payloads() -> None:
    strategy = StatsExtractionSummaryStrategy()
    content = (
        "diff --git a/a.py b/a.py\n"
        "--- a/a.py\n+++ b/a.py\n"
        "@@ -1,2 +1,3 @@\n"
        " a\n+b\n c\n" * 15
    )
    ctx = _context_for(
        content,
        command_signature="git",
        command_prefix="git log",
    )
    ctx = ctx.model_copy(update={"has_diff_markers": True})
    assert (
        strategy.compress(content, context=ctx, level=CompressionLevel.BALANCED)
        == content
    )


def test_stats_extraction_summary_git_log_preserves_sample_subjects() -> None:
    strategy = StatsExtractionSummaryStrategy()
    blocks = []
    for i in range(6):
        h = f"{i:040x}"
        blocks.append(
            f"commit {h}\n"
            f"Author: Dev <d@example.com>\n"
            f"Date:   Mon Jan {i+1} 00:00:00 2024 +0000\n"
            f"\n"
            f"    subject line {i} with detail\n"
        )
    content = "\n".join(blocks)
    ctx = _context_for(
        content,
        command_signature="git",
        command_prefix="git log",
    )
    out = strategy.compress(content, context=ctx, level=CompressionLevel.BALANCED)
    assert "commits=6" in out
    assert "subject line 0" in out
    assert len(out.encode("utf-8")) < len(content.encode("utf-8"))


def test_stats_extraction_summary_pip_list_packages_sample() -> None:
    strategy = StatsExtractionSummaryStrategy()
    lines = [f"package_{i:03d}==1.0.{i}" for i in range(20)]
    content = "\n".join(lines) + "\n"
    ctx = _context_for(
        content,
        command_signature="pip",
        command_prefix="pip list",
    )
    out = strategy.compress(content, context=ctx, level=CompressionLevel.BALANCED)
    assert "pip list: entries=" in out
    assert "package_000==1.0.0" in out
    assert len(out.encode("utf-8")) < len(content.encode("utf-8"))
