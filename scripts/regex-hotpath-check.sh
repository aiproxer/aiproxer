#!/usr/bin/env bash
# Fail if regexp.MustCompile / regexp.Compile appears in hot-path packages without
# an entry in regex-hotpath-allowlist.txt. Run from repository root.
# Requires ripgrep (rg); if missing, skips (same pattern as check-adhoc-goroutines.sh).

set -euo pipefail

script_dir=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$script_dir/.." && pwd)
allowlist_file="$script_dir/regex-hotpath-allowlist.txt"

allowed_paths=()
if [[ -f "$allowlist_file" ]]; then
	while IFS= read -r line || [[ -n "$line" ]]; do
		[[ "$line" =~ ^[[:space:]]*# ]] && continue
		line="${line%%#*}"
		line="${line#"${line%%[![:space:]]*}"}"
		line="${line%"${line##*[![:space:]]}"}"
		[[ -z "$line" ]] && continue
		line=${line//\\//}
		allowed_paths+=("$line")
	done <"$allowlist_file"
fi

is_allowed_file() {
	local rel=$1
	local a
	for a in "${allowed_paths[@]}"; do
		if [[ "$rel" == "$a" ]]; then
			return 0
		fi
	done
	return 1
}

cd "$root"

scan_hotpath_regex() {
	if command -v rg >/dev/null 2>&1; then
		rg -n --glob '*.go' --glob '!*_test.go' \
			'regexp\.(MustCompile|Compile)\(' \
			internal/plugins/frontends internal/core/runtime 2>/dev/null || true
		return 0
	fi
	# Fallback when ripgrep is unavailable (CI images may not ship `rg`).
	if grep --version >/dev/null 2>&1; then
		grep -RIn --include='*.go' --exclude='*_test.go' \
			-E 'regexp\.(MustCompile|Compile)\(' \
			internal/plugins/frontends internal/core/runtime 2>/dev/null || true
		return 0
	fi
	echo "regex-hotpath-check: neither ripgrep (rg) nor grep found; skipping." >&2
	exit 0
}

mapfile -t raw < <(scan_hotpath_regex)

violations=()
for row in "${raw[@]}"; do
	[[ -z "$row" ]] && continue
	file=${row%%:*}
	file=${file//\\//}
	rel=${file#"$root/"}
	if [[ "$rel" == "$file" ]] && [[ "$file" != /* ]]; then
		rel=$file
	fi
	if is_allowed_file "$rel"; then
		continue
	fi
	violations+=("$row")
done

if [[ ${#violations[@]} -gt 0 ]]; then
	echo "ERROR: regexp.MustCompile / regexp.Compile in hot paths (internal/plugins/frontends, internal/core/runtime)."
	echo "Hoist fixed patterns to package-level vars, cache config-driven patterns, or add the file to scripts/regex-hotpath-allowlist.txt with justification:"
	printf '%s\n' "${violations[@]}"
	exit 1
fi

echo "OK: regex hot-path check passed"
