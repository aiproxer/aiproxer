#!/usr/bin/env bash
# Fail unless every file in the candidate repository revision matches an approved pattern in .release-files.
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "$repo_root"

usage() {
	cat >&2 <<'EOF'
Usage: scripts/check-release-clean.sh [--staged | --ref <git-ref>]

  (default)    Check all tracked files in the working tree (for CI).
  --staged     Check the complete staged index (for pre-commit).
  --ref <ref>  Check the complete tree at a Git ref (for pre-push).
EOF
}

mode="tracked"
ref=""
case "${1:-}" in
	"") ;;
	--staged)
		[[ $# -eq 1 ]] || { usage; exit 2; }
		mode="staged"
		;;
	--ref)
		[[ $# -eq 2 ]] || { usage; exit 2; }
		mode="ref"
		ref="$2"
		;;
	*)
		usage
		exit 2
		;;
esac

manifest=".release-files"
case "$mode" in
	tracked)
		[[ -f "$manifest" ]] || { echo "error: missing release file manifest: $manifest" >&2; exit 1; }
		mapfile -t raw_lines < <(sed '/^[[:space:]]*$/d; /^[[:space:]]*#/d' "$manifest")
		mapfile -t files < <(git ls-files | LC_ALL=C sort -u)
		;;
	staged)
		git cat-file -e ":$manifest" 2>/dev/null || { echo "error: $manifest is missing from the staged index" >&2; exit 1; }
		mapfile -t raw_lines < <(git show ":$manifest" | sed '/^[[:space:]]*$/d; /^[[:space:]]*#/d')
		mapfile -t files < <(git ls-files --cached | LC_ALL=C sort -u)
		;;
	ref)
		git cat-file -e "$ref^{tree}" 2>/dev/null || { echo "error: invalid Git ref: $ref" >&2; exit 1; }
		git cat-file -e "$ref:$manifest" 2>/dev/null || { echo "error: $manifest is missing from $ref" >&2; exit 1; }
		mapfile -t raw_lines < <(git show "$ref:$manifest" | sed '/^[[:space:]]*$/d; /^[[:space:]]*#/d')
		mapfile -t files < <(git ls-tree -r --name-only "$ref" | LC_ALL=C sort -u)
		;;
esac

declare -A exact_rules
prefix_patterns=()
glob_patterns=()
declare -A rule_matched
declare -A is_exact_rule

for rule in "${raw_lines[@]}"; do
	rule_matched["$rule"]=0
	if [[ "$rule" == *"/**" ]]; then
		prefix_patterns+=("${rule%"/**"}/")
	elif [[ "$rule" == *"/*" ]]; then
		prefix_patterns+=("${rule%"/*"}/")
	elif [[ "$rule" == *"/" ]]; then
		prefix_patterns+=("$rule")
	elif [[ "$rule" == *"*"* ]]; then
		glob_patterns+=("$rule")
	else
		exact_rules["$rule"]=1
		is_exact_rule["$rule"]=1
	fi
done

violations=()

for file in "${files[@]}"; do
	[[ -z "$file" ]] && continue

	if [[ -n "${exact_rules["$file"]:-}" ]]; then
		rule_matched["$file"]=1
		continue
	fi

	matched=0
	for p in "${prefix_patterns[@]}"; do
		if [[ "$file" == "$p"* ]]; then
			matched=1
			orig_rule="${p%/}/**"
			if [[ -n "${rule_matched["$orig_rule"]:-}" ]]; then
				rule_matched["$orig_rule"]=1
			elif [[ -n "${rule_matched["${p%/}/"]:-}" ]]; then
				rule_matched["${p%/}/"]=1
			elif [[ -n "${rule_matched["${p%/}/*"]:-}" ]]; then
				rule_matched["${p%/}/*"]=1
			fi
			break
		fi
	done
	if [[ $matched -eq 1 ]]; then
		continue
	fi

	for g in "${glob_patterns[@]}"; do
		case "$file" in
			$g)
				matched=1
				rule_matched["$g"]=1
				break
				;;
		esac
	done
	if [[ $matched -eq 1 ]]; then
		continue
	fi

	violations+=("$file")
done

# Check for stale exact file rules (exact file paths that no longer exist)
stale_rules=()
for rule in "${raw_lines[@]}"; do
	if [[ -n "${is_exact_rule["$rule"]:-}" && "${rule_matched["$rule"]:-0}" -eq 0 ]]; then
		stale_rules+=("$rule")
	fi
done

if ((${#violations[@]} > 0)); then
	echo "error: files not approved by $manifest:" >&2
	printf '  - %s\n' "${violations[@]}" >&2
	echo "Review the files, then update patterns or entries in $manifest." >&2
	exit 1
fi

if ((${#stale_rules[@]} > 0)); then
	echo "error: stale exact entries in $manifest (file does not exist):" >&2
	printf '  - %s\n' "${stale_rules[@]}" >&2
	echo "Remove stale exact entries or restore the corresponding files." >&2
	exit 1
fi

# A manifest pattern such as `connectors/**` approves any path under it,
# including a compiled command binary. Every Go binary exceeds this cap and no
# source, fixture, or document comes close, so the cap rejects build outputs that
# a broad `git add` sweeps up before they become permanent history.
max_blob_bytes=$((1024 * 1024))
case "$mode" in
	tracked | staged) blob_sizes="$(git ls-files --cached --format='%(objectname) %(path)' | git cat-file --batch-check='%(objectsize) %(rest)')" ;;
	ref) blob_sizes="$(git ls-tree -r --format='%(objectsize) %(path)' "$ref")" ;;
esac
mapfile -t oversized < <(awk -v max="$max_blob_bytes" '$1 ~ /^[0-9]+$/ && $1 > max { size = $1; sub(/^[0-9]+ /, ""); printf "%s (%d bytes)\n", $0, size }' <<< "$blob_sizes")
if ((${#oversized[@]} > 0)); then
	echo "error: files larger than $max_blob_bytes bytes (build outputs or other generated artifacts?):" >&2
	printf '  - %s\n' "${oversized[@]}" >&2
	echo "Remove them from the index; ignore build outputs in .gitignore." >&2
	exit 1
fi

echo "release manifest check passed (${#files[@]} approved files, mode=$mode${ref:+, ref=$ref})"
