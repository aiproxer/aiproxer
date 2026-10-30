#!/usr/bin/env bash
# Refuse local-environment strings in staged additions, a revision-range diff,
# and/or commit messages. Hostnames, home-directory paths, and container-layout
# paths must never land in the shared history: they leak the author's machine
# layout and break every other checkout that reads the commit.
#
# Local hooks: default (no flags) scans staged additions; the commit-msg hook
# passes --message <file>. Remote CI on a pull request passes
# --range <base>...<head> --commits <base>..<head> so pushed PRs get the same
# verdict without a local checkout. Flags may be combined for a manual full run.
#
# Only ADDED lines are scanned: removing a historical leak must stay easy,
# and already-committed lines are not re-flagged on every commit.
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null)" || {
	echo "check-local-specifics: not inside a git repository" >&2
	exit 2
}
cd "$REPO_ROOT"

# shellcheck disable=SC2088
# The tilde below is a literal forbidden string, never a path to expand.
FORBIDDEN=(
	'devbox-local'
	'/home/mateusz/'
	'~/source/'
	'/source/repos/'
)

check_staged=false
message_file=""
diff_range=""
commits_range=""
while (($# > 0)); do
	case "$1" in
		--staged)
			check_staged=true
			shift
			;;
		--message)
			[[ $# -ge 2 ]] || {
				echo "check-local-specifics: --message needs a file argument" >&2
				exit 2
			}
			message_file="$2"
			shift 2
			;;
		--range)
			[[ $# -ge 2 ]] || {
				echo "check-local-specifics: --range needs a git revision range" >&2
				exit 2
			}
			diff_range="$2"
			shift 2
			;;
		--commits)
			[[ $# -ge 2 ]] || {
				echo "check-local-specifics: --commits needs a git revision range" >&2
				exit 2
			}
			commits_range="$2"
			shift 2
			;;
		-h | --help)
			echo "usage: check-local-specifics.sh [--staged] [--message <file>] [--range <rev-range>] [--commits <rev-range>]"
			exit 0
			;;
		*)
			echo "check-local-specifics: unknown argument '$1'" >&2
			exit 2
			;;
	esac
done
if [[ "$check_staged" == false && -z "$message_file" && -z "$diff_range" && -z "$commits_range" ]]; then
	check_staged=true
fi

patterns_file="$(mktemp "${TMPDIR:-/tmp}/aip-local-specifics-patterns.XXXXXX")"
tmp_files=("$patterns_file")
trap 'rm -f "${tmp_files[@]}"' EXIT
printf '%s\n' "${FORBIDDEN[@]}" >"$patterns_file"

# Build the shared pathspec list: everything except allowlisted paths.
PATHSPECS=(.)
ALLOWLIST="$REPO_ROOT/scripts/local-specifics-allowlist.txt"
if [[ -f "$ALLOWLIST" ]]; then
	while IFS= read -r line || [[ -n "$line" ]]; do
		[[ -z "$line" || "$line" =~ ^[[:space:]]*# ]] && continue
		line="${line#"${line%%[![:space:]]*}"}"
		line="${line%"${line##*[![:space:]]}"}"
		[[ -z "$line" ]] && continue
		PATHSPECS+=(":(exclude)$line")
	done <"$ALLOWLIST"
fi

violations=0
patch_file=""

# scan_patch <label> <patch-file>: attribute added lines to files, match literals.
scan_patch() {
	local label="$1" file="$2"
	set +e
	matches="$(
		awk '
			/^diff --git / { file = $NF; sub(/^b\//, "", file); next }
			/^\+\+\+/ { next }
			/^\+/ { print file "\t" $0 }
		' "$file" | grep -F -f "$patterns_file"
	)"
	grep_rc=$?
	set -e
	if [[ "$grep_rc" -eq 0 ]]; then
		echo "" >&2
		echo "check-local-specifics: $label contain local-environment strings." >&2
		printf '%s\n' "$matches" | sed 's/\t/: /' >&2
		echo "" >&2
		violations=$((violations + 1))
	elif [[ "$grep_rc" -ne 1 ]]; then
		echo "check-local-specifics: content grep failed (exit $grep_rc)" >&2
		exit "$grep_rc"
	fi
}

if [[ "$check_staged" == true ]]; then
	if ! git diff --cached --quiet 2>/dev/null; then
		patch_file="$(mktemp "${TMPDIR:-/tmp}/aip-local-specifics-staged.XXXXXX")"
		tmp_files+=("$patch_file")
		if ! git diff --cached -U0 --no-color -- "${PATHSPECS[@]}" >"$patch_file"; then
			echo "check-local-specifics: git diff --cached failed" >&2
			exit 1
		fi
		scan_patch "staged additions" "$patch_file"
	fi
fi

if [[ -n "$diff_range" ]]; then
	patch_file="$(mktemp "${TMPDIR:-/tmp}/aip-local-specifics-range.XXXXXX")"
	tmp_files+=("$patch_file")
	if ! git diff -U0 --no-color "$diff_range" -- "${PATHSPECS[@]}" >"$patch_file"; then
		echo "check-local-specifics: git diff '$diff_range' failed" >&2
		exit 1
	fi
	scan_patch "changes in range '$diff_range'" "$patch_file"
fi

if [[ -n "$message_file" ]]; then
	if [[ ! -f "$message_file" ]]; then
		echo "check-local-specifics: message file not found: $message_file" >&2
		exit 2
	fi
	set +e
	msg_matches="$(grep -v '^#' -- "$message_file" | grep -F -f "$patterns_file" | grep -v '^$' || true)"
	set -e
	if [[ -n "$msg_matches" ]]; then
		echo "" >&2
		echo "check-local-specifics: commit message contains local-environment strings." >&2
		printf '%s\n' "$msg_matches" >&2
		echo "" >&2
		violations=$((violations + 1))
	fi
fi

if [[ -n "$commits_range" ]]; then
	bodies_file="$(mktemp "${TMPDIR:-/tmp}/aip-local-specifics-bodies.XXXXXX")"
	tmp_files+=("$bodies_file")
	if ! git log --format=%B "$commits_range" >"$bodies_file"; then
		echo "check-local-specifics: git log '$commits_range' failed" >&2
		exit 1
	fi
	set +e
	range_matches="$(grep -F -f "$patterns_file" -- "$bodies_file" || true)"
	set -e
	if [[ -n "$range_matches" ]]; then
		echo "" >&2
		echo "check-local-specifics: commit messages in range '$commits_range' contain local-environment strings." >&2
		printf '%s\n' "$range_matches" >&2
		echo "" >&2
		violations=$((violations + 1))
	fi
fi

if ((violations > 0)); then
	echo "Remove the local hostname/paths, or allowlist a path that must name them" >&2
	echo "in scripts/local-specifics-allowlist.txt (one pathspec per line, '#' comments)." >&2
	echo "Forbidden literals: ${FORBIDDEN[*]}" >&2
	exit 1
fi

exit 0
