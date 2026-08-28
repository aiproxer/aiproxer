"""Failure focus, diagnostics grouping, mutating ack, and stats extraction strategies."""

from __future__ import annotations

import logging
import re
from collections import defaultdict
from contextlib import suppress
from dataclasses import dataclass, field

from src.core.domain.configuration.dynamic_compression_config import CompressionLevel
from src.core.domain.dynamic_compression import ToolOutputContentType, ToolOutputContext
from src.core.services._compression_strategies_common import (
    _COMMIT_HASH_IN_BRACKETS_RE,
    _DELETIONS_RE,
    _FAILURE_INDICATOR_RE,
    _FILES_CHANGED_RE,
    _INSERTIONS_RE,
    _PIP_INSTALL_OK_RE,
    _POSITIVE_FAILURE_COUNT_RE,
    _REF_ARROW_RE,
    _ZERO_FAILURE_RE,
    _mutating_ack_failure_heuristic,
    _preserve_trailing_newline,
    logger,
)
from src.core.services.pytest_output_filter import (
    filter_pytest_output,
    looks_like_pytest_command,
    looks_like_pytest_output,
)


class PytestFailureFocusStrategy:
    """Pytest-focused line filter aligned with legacy ``_filter_pytest_output``."""

    _error_indicators = (
        "Traceback (most recent call last):",
        "command not found",
        "SyntaxError:",
        "ERROR: file or directory not found",
    )

    def __init__(self, min_lines: int | None = None) -> None:
        self._min_lines = min_lines

    def compress(
        self,
        content: str,
        *,
        context: ToolOutputContext,
        level: CompressionLevel,
    ) -> str:
        try:
            if not content:
                return content
            sig = context.identity.command_signature
            prefix = context.identity.command_prefix
            if not (
                looks_like_pytest_command(sig, prefix)
                or looks_like_pytest_output(content)
            ):
                return content
            if any(indicator in content for indicator in self._error_indicators):
                return content
            min_lines = self._resolve_min_lines()
            if len(content.split("\n")) < min_lines:
                return content
            return filter_pytest_output(content)
        except Exception:
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "pytest_failure_focus failed open",
                    exc_info=True,
                )
            return content

    def _resolve_min_lines(self) -> int:
        if self._min_lines is None:
            return 0
        with suppress(TypeError, ValueError):
            return max(0, int(self._min_lines))
        return 0


class FailureFocusGenericStrategy:
    """Failure-prioritizing reduction for bulky test/build/lint-style plain text."""

    _CARGO_PROGRESS_RE = re.compile(
        r"^\s*(Compiling|Checking|Downloading|Blocking|Fresh|Documenting)\s+",
        re.IGNORECASE,
    )
    _CARGO_FINISHED_RE = re.compile(
        r"^\s*Finished\s+`[^`]+`\s+profile\b",
        re.IGNORECASE,
    )

    def compress(
        self,
        content: str,
        *,
        context: ToolOutputContext,
        level: CompressionLevel,
    ) -> str:
        try:
            if not content or context.has_explicit_format:
                return content
            lines = content.split("\n")
            if len(lines) < 12:
                return content

            joined = "\n".join(lines)
            has_failure = bool(_FAILURE_INDICATOR_RE.search(joined)) or bool(
                _POSITIVE_FAILURE_COUNT_RE.search(joined)
            )

            sig = (context.identity.command_signature or "").lower()

            if not _POSITIVE_FAILURE_COUNT_RE.search(joined) and (
                _ZERO_FAILURE_RE.search(joined)
                or re.search(r"(?i)\btest result:\s*ok\.?\b", joined)
            ):
                last = lines[-1]
                return (
                    f"[failure-focus] Condensed {len(lines)} lines (no failures detected).\n"
                    f"{last}"
                )

            if sig == "cargo":
                filtered = [
                    ln
                    for ln in lines
                    if not self._CARGO_PROGRESS_RE.match(ln)
                    and not self._CARGO_FINISHED_RE.match(ln)
                ]
                merged = "\n".join(filtered)
                if not merged.strip() or merged == content:
                    return content
                if has_failure and not (
                    _FAILURE_INDICATOR_RE.search(merged)
                    or _POSITIVE_FAILURE_COUNT_RE.search(merged)
                ):
                    return content
                return merged

            failure_indexes = [
                idx for idx, ln in enumerate(lines) if _FAILURE_INDICATOR_RE.search(ln)
            ]
            if len(failure_indexes) == 1:
                idx = failure_indexes[0]
                start = max(0, idx - 3)
                end = min(len(lines), idx + 25)
                window = lines[start:end]
                candidate = "\n".join(window)
                summary_line = lines[-1]
                if summary_line and summary_line not in candidate:
                    candidate = f"{candidate}\n{summary_line}"
                if not _FAILURE_INDICATOR_RE.search(candidate):
                    return content
                if len(candidate) >= len(content):
                    return content
                return candidate

            return content
        except Exception:
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "failure_focus_generic failed open",
                    exc_info=True,
                )
            return content


@dataclass
class _DiagnosticAggregate:
    count: int = 0
    anchors: set[tuple[int, int | None]] = field(default_factory=set)


class DiagnosticsGroupingStrategy:
    """Group plain-text diagnostics by file/rule while preserving anchors."""

    _RUFF_LIKE_RE = re.compile(
        r"^(?P<path>[^:]+):(?P<line>\d+):(?P<col>\d+):\s*(?P<code>[A-Z]{1,8}\d*)\s+(?P<msg>.+)$"
    )
    _MYPY_STYLE_RE = re.compile(
        r"^(?P<path>[^:]+):(?P<line>\d+):\s*(?P<kind>error|note|warning):\s*(?P<msg>.+)$",
        re.IGNORECASE,
    )
    _TSC_STYLE_RE = re.compile(
        r"^(?P<path>.+)\((?P<line>\d+),(?P<col>\d+)\):\s*"
        r"(?P<kind>error|warning)\s+(?P<code>TS\d+):\s*(?P<msg>.+)$",
        re.IGNORECASE,
    )

    def compress(
        self,
        content: str,
        *,
        context: ToolOutputContext,
        level: CompressionLevel,
    ) -> str:
        try:
            if not content.strip() or context.has_explicit_format:
                return content
            lines = content.splitlines()
            parsed: list[tuple[str, str, str, int, int | None]] = []
            for raw in lines:
                line = raw.strip()
                if not line:
                    continue
                m = self._TSC_STYLE_RE.match(line)
                if m:
                    parsed.append(
                        (
                            m.group("path").strip(),
                            m.group("code"),
                            m.group("msg").strip(),
                            int(m.group("line")),
                            int(m.group("col")),
                        )
                    )
                    continue
                m = self._RUFF_LIKE_RE.match(line)
                if m:
                    parsed.append(
                        (
                            m.group("path").strip(),
                            m.group("code"),
                            m.group("msg").strip(),
                            int(m.group("line")),
                            int(m.group("col")),
                        )
                    )
                    continue
                m = self._MYPY_STYLE_RE.match(line)
                if m:
                    kind = m.group("kind").upper()
                    parsed.append(
                        (
                            m.group("path").strip(),
                            kind,
                            m.group("msg").strip(),
                            int(m.group("line")),
                            None,
                        )
                    )
                    continue

            if len(parsed) < 2:
                return content

            grouped: dict[str, dict[str, dict[str, _DiagnosticAggregate]]] = (
                defaultdict(
                    lambda: defaultdict(lambda: defaultdict(_DiagnosticAggregate))
                )
            )
            for path, code, msg, line_no, col_no in parsed:
                aggregate = grouped[path][code][msg]
                aggregate.count += 1
                aggregate.anchors.add((line_no, col_no))

            out_lines = ["=== grouped diagnostics ==="]
            for path in sorted(grouped.keys()):
                out_lines.append(path)
                for code in sorted(grouped[path].keys()):
                    for msg, aggregate in sorted(
                        grouped[path][code].items(), key=lambda item: item[0]
                    ):
                        anchor = self._format_primary_anchor(aggregate.anchors)
                        annotations: list[str] = []
                        if aggregate.count > 1:
                            annotations.append(f"x{aggregate.count}")
                        extra_locations = len(aggregate.anchors) - 1
                        if extra_locations > 0:
                            annotations.append(f"+{extra_locations} locations")
                        ann = f" ({', '.join(annotations)})" if annotations else ""
                        out_lines.append(f"  [{code}] {anchor} {msg}{ann}")
                out_lines.append("")

            while out_lines and not out_lines[-1].strip():
                out_lines.pop()
            result = "\n".join(out_lines)
            return _preserve_trailing_newline(original=content, transformed=result)
        except Exception:
            if logger.isEnabledFor(logging.DEBUG):
                logger.debug(
                    "diagnostics_grouping failed open",
                    exc_info=True,
                )
            return content

    @staticmethod
    def _anchor_sort_key(anchor: tuple[int, int | None]) -> tuple[int, int]:
        line_no, col_no = anchor
        return line_no, (-1 if col_no is None else col_no)

    @classmethod
    def _format_primary_anchor(cls, anchors: set[tuple[int, int | None]]) -> str:
        if not anchors:
            return "L?"
        line_no, col_no = min(anchors, key=cls._anchor_sort_key)
        if col_no is None:
            return f"L{line_no}"
        return f"L{line_no}:C{col_no}"


class MutatingSuccessAckStrategy:
    """Compact successful side-effect command noise while keeping key outcomes."""

    def compress(
        self,
        content: str,
        *,
        context: ToolOutputContext,
        level: CompressionLevel,
    ) -> str:
        if not content:
            return content
        if context.content_type is not ToolOutputContentType.TEXT:
            return content
        if context.has_explicit_format:
            return content
        if _mutating_ack_failure_heuristic(content):
            return content

        sig = (context.identity.command_signature or "").lower()
        prefix = (context.identity.command_prefix or "").lower()

        if sig == "git":
            summary = self._summarize_git_mutating(content, prefix, level=level)
            if summary is None:
                return content
            out = _preserve_trailing_newline(original=content, transformed=summary)
            return (
                out
                if len(out.encode("utf-8")) < len(content.encode("utf-8"))
                else content
            )

        if sig in {"pip", "pip3"} and "install" in prefix:
            summary = self._summarize_pip_install(content)
            if summary is None:
                return content
            out = _preserve_trailing_newline(original=content, transformed=summary)
            return (
                out
                if len(out.encode("utf-8")) < len(content.encode("utf-8"))
                else content
            )

        if sig in {"npm", "pnpm", "yarn"} and (
            "install" in prefix or prefix.endswith(" ci")
        ):
            summary = self._summarize_npm_family_install(content, tool=sig, level=level)
            if summary is None:
                return content
            out = _preserve_trailing_newline(original=content, transformed=summary)
            return (
                out
                if len(out.encode("utf-8")) < len(content.encode("utf-8"))
                else content
            )

        return content

    def _summarize_git_mutating(
        self,
        content: str,
        prefix: str,
        *,
        level: CompressionLevel,
    ) -> str | None:
        if not prefix.startswith("git "):
            return None

        sub = prefix[4:].strip()
        if sub in {"commit"}:
            return self._git_commit_ack(content)
        if sub in {"push", "pull", "fetch"}:
            return self._git_transport_ack(content, level=level)
        if sub in {
            "add",
            "stash",
            "merge",
            "rebase",
            "cherry-pick",
            "checkout",
            "restore",
        }:
            return self._git_simple_ack(content, verb=sub)
        if sub in {"rm", "mv"}:
            return self._git_simple_ack(content, verb=sub)
        return None

    @staticmethod
    def _git_commit_ack(content: str) -> str | None:
        m = _COMMIT_HASH_IN_BRACKETS_RE.search(content)
        branch = m.group(1) if m else None
        h = m.group(2) if m else None
        if not h:
            m2 = re.search(r"\bcommit\s+([0-9a-f]{7,40})\b", content, re.IGNORECASE)
            h = m2.group(1) if m2 else None
        fc_m = _FILES_CHANGED_RE.search(content)
        ins_m = _INSERTIONS_RE.search(content)
        del_m = _DELETIONS_RE.search(content)
        parts = ["git commit: ok"]
        if branch:
            parts.append(f"branch={branch}")
        if h:
            parts.append(f"hash={h}")
        if fc_m:
            parts.append(f"files={fc_m.group(1)}")
        if ins_m or del_m:
            delta = []
            if ins_m:
                delta.append(f"+{ins_m.group(1)}")
            if del_m:
                delta.append(f"-{del_m.group(1)}")
            parts.append("delta=" + "/".join(delta))
        if len(parts) <= 1:
            return None
        return " | ".join(parts) + "\n"

    def _git_transport_ack(
        self, content: str, *, level: CompressionLevel
    ) -> str | None:
        if re.search(r"Already up to date\.|Everything up-to-date", content, re.I):
            return "git: ok (no remote changes)\n"

        matches = list(_REF_ARROW_RE.finditer(content))
        if matches:
            m = matches[-1]
            parts = ["git: ok", f"ref={m.group(2)}->{m.group(3)}"]
            if level != CompressionLevel.AGGRESSIVE and m.group(1):
                parts.append(f"range={m.group(1).strip()}")
            return " | ".join(parts) + "\n"

        if len(content.splitlines()) < 12:
            return None
        last_meaningful = ""
        for line in reversed(content.splitlines()):
            stripped = line.strip()
            if not stripped or stripped.startswith("remote:"):
                continue
            if "->" in stripped or "up to date" in stripped.lower():
                last_meaningful = stripped
                break
        if not last_meaningful:
            return None
        return f"git: ok | tail={last_meaningful[:200]}\n"

    @staticmethod
    def _git_simple_ack(content: str, *, verb: str) -> str | None:
        lines = [ln for ln in content.splitlines() if ln.strip()]
        if len(lines) < 8:
            return None
        return f"git {verb}: ok | lines={len(lines)} (output condensed)\n"

    @staticmethod
    def _summarize_pip_install(content: str) -> str | None:
        if "error" in content.lower() or "failed" in content.lower():
            return None
        m = _PIP_INSTALL_OK_RE.search(content)
        if m:
            pkgs = m.group(1).strip()
            if len(pkgs) > 160:
                pkgs = pkgs[:157] + "..."
            return f"pip install: ok | packages={pkgs}\n"
        if (
            "Requirement already satisfied" in content
            and len(content.splitlines()) > 12
        ):
            return "pip install: ok (requirements already satisfied)\n"
        return None

    @staticmethod
    def _summarize_npm_family_install(
        content: str, *, tool: str, level: CompressionLevel
    ) -> str | None:
        added = re.search(r"added\s+(\d+)\s+packages?", content, re.IGNORECASE)
        audited = re.search(
            r"(\d+)\s+packages?\s+are looking for funding", content, re.I
        )
        if not added and not audited and len(content.splitlines()) < 15:
            return None
        parts = [f"{tool} install: ok"]
        if added:
            parts.append(f"added={added.group(1)}")
        if audited and level != CompressionLevel.AGGRESSIVE:
            parts.append("funding_notice=1")
        if len(parts) == 1:
            return None
        return " | ".join(parts) + "\n"


def _git_porcelain_path_line(line: str) -> str | None:
    """Return path from a git status --porcelain line (two status columns + path)."""
    s = line.rstrip("\n")
    if len(s) < 4 or s.startswith("##"):
        return None
    if s[2] not in {" ", "\t"}:
        return None
    path = s[3:].lstrip()
    return path or None


_GIT_STATUS_AHEAD_RE = re.compile(r"\[ahead\s+(\d+)\]")
_GIT_STATUS_BEHIND_RE = re.compile(r"\[behind\s+(\d+)\]")
_GIT_LONG_PATH_RE = re.compile(
    r"^\s+(?:new file|modified|deleted|renamed|copied|both modified):\s+(.+?)\s*$",
    re.IGNORECASE,
)


def _git_status_strip_bracket_suffixes(text: str) -> str:
    return re.sub(r"\s*\[[^\]]+\]\s*$", "", text).strip()


def _parse_git_status_header_meta(lines: list[str]) -> dict[str, str]:
    meta: dict[str, str] = {}
    for line in lines[:40]:
        if line.startswith("## "):
            rest = line[3:].strip()
            if "..." in rest:
                left, _, right = rest.partition("...")
                branch = left.strip().split()[0] if left.strip() else ""
                tr = _git_status_strip_bracket_suffixes(right.strip())
                meta["branch"] = branch
                meta["upstream"] = tr.split()[0] if tr else ""
            else:
                meta["branch"] = rest.split()[0] if rest else ""
            am = _GIT_STATUS_AHEAD_RE.search(line)
            bm = _GIT_STATUS_BEHIND_RE.search(line)
            if am:
                meta["ahead"] = am.group(1)
            if bm:
                meta["behind"] = bm.group(1)
            break
        m = re.match(r"^On branch\s+(\S+)", line)
        if m:
            meta["branch"] = m.group(1)
        m2 = re.search(
            r"ahead of\s+['\"]([^'\"]+)['\"]\s+by\s+(\d+)\s+commit",
            line,
            re.IGNORECASE,
        )
        if m2:
            meta["upstream"] = m2.group(1)
            meta["ahead"] = m2.group(2)
        m3 = re.search(
            r"behind\s+['\"]([^'\"]+)['\"]\s+by\s+(\d+)\s+commit",
            line,
            re.IGNORECASE,
        )
        if m3:
            meta["upstream"] = m3.group(1)
            meta["behind"] = m3.group(2)
    return meta


def _git_status_porcelain_bucket(line: str) -> tuple[str, str] | None:
    s = line.rstrip("\n")
    if _git_porcelain_path_line(line) is None:
        return None
    xy = s[:2]
    if xy == "??":
        return "untracked", s
    if xy == "!!":
        return "ignored", s
    x, y = xy[0], xy[1]
    if x == "U" or y == "U" or xy in {"DD", "AA", "TT"}:
        return "unmerged", s
    if x != " " and y != " ":
        return "mixed", s
    if x != " ":
        return "staged", s
    if y != " ":
        return "unstaged", s
    return None


def _git_status_collect_long_format(
    lines: list[str],
) -> tuple[list[tuple[str, str]], dict[str, str]] | None:
    if not any("On branch" in ln for ln in lines[:8]):
        return None
    meta: dict[str, str] = {}
    entries: list[tuple[str, str]] = []
    section: str | None = None
    for line in lines:
        m = re.match(r"^On branch\s+(\S+)", line)
        if m:
            meta["branch"] = m.group(1)
        m2 = re.search(
            r"ahead of\s+['\"]([^'\"]+)['\"]\s+by\s+(\d+)\s+commit",
            line,
            re.IGNORECASE,
        )
        if m2:
            meta["upstream"] = m2.group(1)
            meta["ahead"] = m2.group(2)
        if line.startswith("Changes to be committed"):
            section = "staged"
            continue
        if "Changes not staged for commit" in line:
            section = "unstaged"
            continue
        if line.startswith("Untracked files"):
            section = "untracked"
            continue
        if line.startswith(("All conflicts fixed", "Unmerged paths")):
            section = "unmerged"
            continue
        pm = _GIT_LONG_PATH_RE.match(line)
        if pm and section:
            path = pm.group(1).strip()
            if path:
                entries.append((section, path))
    if not entries:
        return None
    return entries, meta


class StatsExtractionSummaryStrategy:
    """Stats-first summaries with bounded representative lines."""

    def compress(
        self,
        content: str,
        *,
        context: ToolOutputContext,
        level: CompressionLevel,
    ) -> str:
        if not content:
            return content
        if context.content_type is not ToolOutputContentType.TEXT:
            return content
        if context.has_explicit_format:
            return content
        if _mutating_ack_failure_heuristic(content):
            return content
        if context.has_diff_markers:
            lines = content.splitlines()
            has_hunk_headers = any(line.startswith("@@ ") for line in lines)
            has_git_header = any(line.startswith("diff --git ") for line in lines)
            has_unified_headers = any(
                line.startswith("--- ") for line in lines
            ) and any(line.startswith("+++ ") for line in lines)
            if has_hunk_headers and (has_git_header or has_unified_headers):
                return content

        sig = (context.identity.command_signature or "").lower()
        prefix = (context.identity.command_prefix or "").lower()

        summary: str | None = None
        if sig == "git":
            summary = self._summarize_git(content, prefix, level=level)
        elif sig in {"pip", "pip3"} or prefix.startswith(("pip ", "pip3 ")):
            summary = self._summarize_dependency_list(content, kind="pip", level=level)
        elif sig in {"npm", "pnpm", "yarn"}:
            summary = self._summarize_dependency_list(content, kind="node", level=level)

        if summary is None:
            return content
        out = _preserve_trailing_newline(original=content, transformed=summary)
        if (
            sig == "git"
            and prefix == "git status"
            and summary.strip().startswith("git status")
        ):
            return out
        if len(out.encode("utf-8")) >= len(content.encode("utf-8")):
            return content
        return out

    @staticmethod
    def _sample_limit(level: CompressionLevel) -> int:
        if level == CompressionLevel.CONSERVATIVE:
            return 8
        if level == CompressionLevel.AGGRESSIVE:
            return 3
        return 5

    def _summarize_git(
        self, content: str, prefix: str, *, level: CompressionLevel
    ) -> str | None:
        if prefix == "git status":
            return self._git_status_stats(content, level=level)
        if prefix == "git log":
            return self._git_log_stats(content, level=level)
        if prefix == "git branch":
            return self._git_branch_stats(content, level=level)
        return None

    def _git_status_section_cap(self, level: CompressionLevel) -> int:
        if level == CompressionLevel.CONSERVATIVE:
            return 24
        if level == CompressionLevel.AGGRESSIVE:
            return 6
        return 14

    def _git_status_render_grouped(
        self,
        grouped_lines: list[tuple[str, str]],
        meta: dict[str, str],
        *,
        level: CompressionLevel,
        paths_total: int,
    ) -> str:
        cap = self._git_status_section_cap(level)
        order = ["unmerged", "staged", "mixed", "unstaged", "untracked", "ignored"]
        grouped: dict[str, list[str]] = {k: [] for k in order}
        for bucket, disp in grouped_lines:
            if bucket in grouped:
                grouped[bucket].append(disp)

        headline = ["git status", f"paths={paths_total}"]
        if meta.get("branch"):
            headline.append(f"branch={meta['branch']}")
        if meta.get("upstream") and level != CompressionLevel.AGGRESSIVE:
            headline.append(f"upstream={meta['upstream'][:80]}")
        if meta.get("ahead"):
            headline.append(f"ahead={meta['ahead']}")
        if meta.get("behind") and level != CompressionLevel.AGGRESSIVE:
            headline.append(f"behind={meta['behind']}")
        parts_out: list[str] = [" | ".join(headline)]
        labels = {
            "unmerged": "unmerged",
            "staged": "staged",
            "mixed": "staged+unstaged",
            "unstaged": "unstaged",
            "untracked": "untracked",
            "ignored": "ignored",
        }
        for key in order:
            if key == "ignored" and level == CompressionLevel.AGGRESSIVE:
                continue
            rows = grouped[key]
            if not rows:
                continue
            shown = rows[:cap]
            more = len(rows) - len(shown)
            parts_out.append(f"[{labels[key]}] ({len(rows)})")
            parts_out.extend(shown)
            if more:
                parts_out.append(f"… {more} more")
        return "\n".join(parts_out) + "\n"

    def _git_status_stats(self, content: str, *, level: CompressionLevel) -> str | None:
        lines = content.splitlines()
        meta = _parse_git_status_header_meta(lines)

        porcelain_rows: list[tuple[str, str]] = []
        for line in lines:
            bucket = _git_status_porcelain_bucket(line)
            if bucket:
                porcelain_rows.append(bucket)

        if porcelain_rows:
            n = len(porcelain_rows)
            if n < 6 and len(lines) < 18:
                return None
            return self._git_status_render_grouped(
                porcelain_rows, meta, level=level, paths_total=n
            )

        long_fmt = _git_status_collect_long_format(lines)
        if long_fmt:
            raw_entries, meta_long = long_fmt
            merged = {**meta, **meta_long}
            grouped_lines = [(bucket, f"  · {path}") for bucket, path in raw_entries]
            n = len(grouped_lines)
            if n < 6 and len(lines) < 18:
                return None
            return self._git_status_render_grouped(
                grouped_lines, merged, level=level, paths_total=n
            )

        paths: list[str] = []
        for line in lines:
            porcelain_path = _git_porcelain_path_line(line)
            if porcelain_path:
                paths.append(porcelain_path.strip())
                continue
            if "\t" in line and not line.startswith("#"):
                tail = line.split("\t")[-1].strip()
                if tail and (
                    "/" in tail or tail.endswith((".py", ".ts", ".js", ".go"))
                ):
                    paths.append(tail)

        if len(paths) < 6 and len(lines) < 18:
            return None

        limit = self._sample_limit(level)
        sample = sorted(set(paths))[:limit]
        headline = ["git status", f"paths={len(paths)}"]
        if meta.get("branch"):
            headline.append(f"branch={meta['branch']}")
        if meta.get("upstream") and level != CompressionLevel.AGGRESSIVE:
            headline.append(f"upstream={meta['upstream'][:80]}")
        if meta.get("ahead"):
            headline.append(f"ahead={meta['ahead']}")
        if meta.get("behind") and level != CompressionLevel.AGGRESSIVE:
            headline.append(f"behind={meta['behind']}")
        body = " | ".join(headline) + "\n"
        body += "\n".join(f"  {p}" for p in sample)
        if len(paths) > len(sample):
            body += f"\n… {len(paths) - len(sample)} more paths"
        return body + "\n"

    def _git_log_stats(self, content: str, *, level: CompressionLevel) -> str | None:
        commit_lines = [ln for ln in content.splitlines() if ln.startswith("commit ")]
        n = len(commit_lines)
        if n < 4 and len(content.splitlines()) < 24:
            return None

        hashes: list[str] = []
        for ln in commit_lines[:50]:
            tok = ln.split()
            if len(tok) >= 2 and re.fullmatch(r"[0-9a-f]{7,40}", tok[1], re.I):
                hashes.append(tok[1][:12])

        subjects: list[str] = []
        blocks = re.split(
            r"(?=^commit\s+[0-9a-f]{7,40}\b)", content, flags=re.MULTILINE
        )
        for block in blocks[1 : 1 + self._sample_limit(level)]:
            lines = [ln.rstrip() for ln in block.splitlines()]
            subj = ""
            blank_pending = False
            for ln in lines[1:]:
                if not ln.strip():
                    blank_pending = True
                    continue
                if blank_pending and not ln.startswith(("Author:", "Date:", "Merge:")):
                    subj = ln.strip()
                    break
            if subj:
                subjects.append(subj[:120])

        limit = self._sample_limit(level)
        sample_h = hashes[:limit]
        body = f"git log: commits={n}\n--- sample (hash + subject) ---\n"
        for idx, h in enumerate(sample_h):
            sub = subjects[idx] if idx < len(subjects) else ""
            body += f"  {h}  {sub}\n"
        if n > len(sample_h):
            body += f"… {n - len(sample_h)} more commits\n"
        return body

    def _git_branch_stats(self, content: str, *, level: CompressionLevel) -> str | None:
        names: list[str] = []
        current = None
        for line in content.splitlines():
            s = line.strip()
            if not s:
                continue
            if s.startswith("*"):
                current = s[1:].strip().split()[0]
                names.append(current)
            else:
                names.append(s.split()[0])
        if len(names) < 10:
            return None
        limit = self._sample_limit(level)
        sample = sorted(set(names))[:limit]
        parts = [f"git branch: count={len(names)}"]
        if current:
            parts.append(f"current={current}")
        body = " | ".join(parts) + "\n--- sample ---\n"
        body += "\n".join(f"  {n}" for n in sample)
        if len(names) > len(sample):
            body += f"\n… {len(names) - len(sample)} more branches\n"
        return body

    def _summarize_dependency_list(
        self,
        content: str,
        *,
        kind: str,
        level: CompressionLevel,
    ) -> str | None:
        lines = [ln.strip() for ln in content.splitlines() if ln.strip()]
        if len(lines) < 12:
            return None

        entries: list[str] = []
        for ln in lines:
            if ln.startswith(("#", "Package")):
                continue
            if kind == "pip":
                if re.match(r"^[A-Za-z0-9_.-]+", ln):
                    pkg = ln.split()[0]
                    entries.append(pkg)
            else:
                m = re.search(r"([\w@./-]+@[0-9][0-9a-z.\-]*)", ln)
                if m:
                    entries.append(m.group(1))

        if len(entries) < 10:
            return None

        limit = self._sample_limit(level)
        uniq = sorted(set(entries))
        sample = uniq[:limit]
        label = "pip list" if kind == "pip" else "node deps"
        body = f"{label}: entries={len(entries)}\n--- sample ---\n"
        body += "\n".join(f"  {e}" for e in sample)
        if len(uniq) > len(sample):
            body += f"\n… {len(uniq) - len(sample)} more entries\n"
        return body
