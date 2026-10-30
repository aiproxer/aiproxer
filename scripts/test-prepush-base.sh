#!/usr/bin/env bash
# Proves scripts/hooks/pre-push measures the PR diff (merge-base with main)
# rather than old-remote-tip..new-head when history was rewritten by rebase,
# and honors an explicitly declared base (AIP_CHANGE_SIZE_BASE, then DEV_BASE)
# for new branches and rewritten history instead of always measuring against
# the trunk. Fast-forward pushes must keep measuring the pushed delta no
# matter which base variables are set, and invalid declared bases must fail
# closed. Uses real throwaway repositories with stubbed checker/change-size
# scripts that record the --base/--head they receive and enforce a small
# file limit over that range.
set -euo pipefail
script_dir=$(cd "$(dirname "$0")" && pwd)
hook="$script_dir/hooks/pre-push"
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT

# Never inherit a comparison base from the developer's shell: every case
# starts from the hook's own defaults unless it deliberately declares a base
# for that individual invocation.
unset AIP_CHANGE_SIZE_BASE DEV_BASE AIP_HOOK_FILE_LIMIT

export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid
export GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid

fails=0
zero=0000000000000000000000000000000000000000
export AIP_HOOK_ARGS_LOG="$fixture/args.log"

# make_repo <name> -> prints repo path with stubbed hook dependencies.
make_repo() {
	local repo="$fixture/$1"
	git init --quiet --initial-branch=main "$repo"
	echo seed >"$repo/seed.txt"
	git -C "$repo" add seed.txt
	git -C "$repo" commit --quiet -m seed
	mkdir -p "$repo/scripts"
	cat >"$repo/scripts/check-release-clean.sh" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
	cat >"$repo/scripts/check-change-size.sh" <<'EOF'
#!/usr/bin/env bash
# Stub: record the range the hook chose to measure, and enforce a file
# limit over that range like the real checker (default: effectively none).
set -euo pipefail
limit="${AIP_HOOK_FILE_LIMIT:-1000000}"
base=""; head=""
while [[ $# -gt 0 ]]; do
	case "$1" in
		--base) base="$2"; shift 2 ;;
		--head) head="$2"; shift 2 ;;
		*) shift ;;
	esac
done
echo "--base $base --head $head" >>"$AIP_HOOK_ARGS_LOG"
# An unresolvable range must fail the gate, never look like an empty diff.
# The real checker rejects invalid refs the same way.
diff_out="$(git diff --numstat "$base...$head" --)"
count="$(grep -c '\.go$' <<<"$diff_out" || true)"
if [[ "$count" -gt "$limit" ]]; then
	echo "check-change-size: $count modified Go files exceed the $limit-file limit." >&2
	exit 1
fi
exit 0
EOF
	chmod +x "$repo/scripts/check-release-clean.sh" "$repo/scripts/check-change-size.sh"
	git -C "$repo" add scripts/check-release-clean.sh scripts/check-change-size.sh
	git -C "$repo" commit --quiet -m fixture-stubs
	printf '%s\n' "$repo"
}

# run_hook <repo> <local-sha> <remote-sha> [VAR=value ...] -> prints the
# recorded --base value. Extra assignments apply only to this invocation.
# A rejecting hook or a missing measurement is a hard failure, never an empty
# comparison value that a later grep could mistake for success.
run_hook() {
	local repo="$1" local_sha="$2" remote_sha="$3" rc=0
	shift 3
	rm -f "$AIP_HOOK_ARGS_LOG"
	(cd "$repo" && printf 'refs/heads/feat %s refs/heads/feat %s\n' "$local_sha" "$remote_sha" | env "$@" bash "$hook" origin) || rc=$?
	if [[ "$rc" -ne 0 ]]; then
		echo "hook exited $rc for $local_sha (remote $remote_sha)" >&2
		return "$rc"
	fi
	if [[ ! -s "$AIP_HOOK_ARGS_LOG" ]]; then
		echo "hook recorded no change-size measurement for $local_sha" >&2
		return 1
	fi
	grep -o '\-\-base [0-9a-f]*' "$AIP_HOOK_ARGS_LOG" | head -1 | cut -d' ' -f2
}

# expect_base <label> <expected> <repo> <local-sha> <remote-sha> [VAR=value ...]
expect_base() {
	local label="$1" want="$2" repo="$3" local_sha="$4" remote_sha="$5" base
	shift 5
	if ! base="$(run_hook "$repo" "$local_sha" "$remote_sha" "$@")"; then
		echo "FAIL: $label: hook rejected the push" >&2
		fails=$((fails + 1))
		return
	fi
	if [[ "$base" == "$want" ]]; then
		echo "ok: $label"
	else
		echo "FAIL: $label base=$base, want $want" >&2
		fails=$((fails + 1))
	fi
}

repo=$(make_repo origin)
git -C "$repo" checkout --quiet -b feat
echo slice >"$repo/slice.txt"
git -C "$repo" add slice.txt
git -C "$repo" commit --quiet -m slice
old_tip="$(git -C "$repo" rev-parse HEAD)"

# Upstream moves main forward (the drift that poisoned the old measurement).
git -C "$repo" checkout --quiet main
echo upstream >"$repo/upstream.txt"
git -C "$repo" add upstream.txt
git -C "$repo" commit --quiet -m upstream
pre_drift="$(git -C "$repo" rev-parse main~1)"

# Rebase the slice onto the new main (rewritten history: old tip not ancestor).
git -C "$repo" checkout --quiet feat
git -C "$repo" rebase --quiet main
new_head="$(git -C "$repo" rev-parse HEAD)"

# Case 1: force-push after rebase must measure the PR diff, not old-tip..new-head.
# (Throwaway repo has no origin/main ref; merge against local main.)
want="$(git -C "$repo" merge-base main "$new_head")"
expect_base "rebased push measures PR diff base" "$want" "$repo" "$new_head" "$old_tip"

# Case 2: fast-forward push keeps measuring old-tip..new-head (unchanged behavior).
git -C "$repo" checkout --quiet -b ff main
echo more >>"$repo/slice.txt"
git -C "$repo" add slice.txt
git -C "$repo" commit --quiet -m more
ff_head="$(git -C "$repo" rev-parse HEAD)"
ff_base="$(git -C "$repo" rev-parse main)"
expect_base "fast-forward push still measures from old tip" "$ff_base" "$repo" "$ff_head" "$ff_base"

# Case 3: new branch (zero remote sha) measures the PR diff by default.
expect_base "new branch push measures PR diff base" "$want" "$repo" "$new_head" "$zero"

# Case 4: a genuinely oversized PR still trips the gate after rebase.
git -C "$repo" checkout --quiet -b big main
for i in 1 2 3; do echo "package p$i" >"$repo/p$i.go"; done
git -C "$repo" add .
git -C "$repo" commit --quiet -m big
big_old="$(git -C "$repo" rev-parse HEAD)"
git -C "$repo" checkout --quiet main
echo more-upstream >>"$repo/upstream.txt"
git -C "$repo" add upstream.txt
git -C "$repo" commit --quiet -m upstream2
git -C "$repo" checkout --quiet big
git -C "$repo" rebase --quiet main
big_new="$(git -C "$repo" rev-parse HEAD)"
rc=0
(cd "$repo" && printf 'refs/heads/big %s refs/heads/big %s\n' "$big_new" "$big_old" | AIP_HOOK_FILE_LIMIT=2 bash "$hook" origin) 2>"$fixture/err4.log" || rc=$?
if [[ "$rc" -eq 0 ]]; then
	echo "FAIL: oversized rebased PR was allowed" >&2
	fails=$((fails + 1))
elif grep -q "exceed" "$fixture/err4.log"; then
	echo "ok: oversized rebased PR still refused"
else
	echo "FAIL: oversized rebased PR refused without size explanation" >&2
	fails=$((fails + 1))
fi

# Stack fixture: the inherited stack adds three Go files, the feature slice
# adds one. A two-file limit therefore separates the slice from the stack.
git -C "$repo" checkout --quiet main
git -C "$repo" checkout --quiet -b stack
for i in 1 2 3; do echo "package stack$i" >"$repo/stack$i.go"; done
echo stack >>"$repo/slice.txt"
git -C "$repo" add .
git -C "$repo" commit --quiet -m stack
stack_base="$(git -C "$repo" rev-parse HEAD)"
git -C "$repo" checkout --quiet -b stacked-feat
echo "package feature" >"$repo/feature.go"
echo feat >>"$repo/slice.txt"
git -C "$repo" add .
git -C "$repo" commit --quiet -m feat
stacked_head="$(git -C "$repo" rev-parse HEAD)"
other_base="$(git -C "$repo" rev-parse "$stack_base~1")"

# Case 5: new-branch push honors an explicitly declared stack base instead of
# the trunk merge-base (docs/development-iteration.md canonical order).
expect_base "new branch push honors declared AIP_CHANGE_SIZE_BASE" "$stack_base" "$repo" "$stacked_head" "$zero" \
	AIP_CHANGE_SIZE_BASE="$stack_base"

# Case 6: DEV_BASE is the fallback when no explicit base is declared.
expect_base "new branch push falls back to DEV_BASE" "$stack_base" "$repo" "$stacked_head" "$zero" \
	DEV_BASE="$stack_base"

# Case 7: AIP_CHANGE_SIZE_BASE outranks DEV_BASE when both are declared.
expect_base "AIP_CHANGE_SIZE_BASE wins over DEV_BASE" "$stack_base" "$repo" "$stacked_head" "$zero" \
	AIP_CHANGE_SIZE_BASE="$stack_base" DEV_BASE="$other_base"

# Case 8: rewritten history also honors a declared base over the trunk fallback.
expect_base "rewritten history honors declared stack base" "$pre_drift" "$repo" "$new_head" "$old_tip" \
	AIP_CHANGE_SIZE_BASE="$pre_drift" DEV_BASE="$old_tip"

# Case 9: a fast-forward push still measures the pushed delta when both base
# variables are set; neither may override the old remote tip.
expect_base "fast-forward push ignores declared bases" "$ff_base" "$repo" "$ff_head" "$ff_base" \
	AIP_CHANGE_SIZE_BASE="$pre_drift" DEV_BASE="$old_tip"

# Case 10: the declared base keeps a small slice inside the gate even when the
# inherited stack exceeds the same limit. Without the declaration the same
# push must be refused.
rc=0
(cd "$repo" && printf 'refs/heads/stacked-feat %s refs/heads/stacked-feat %s\n' "$stacked_head" "$zero" | AIP_CHANGE_SIZE_BASE="$stack_base" AIP_HOOK_FILE_LIMIT=2 bash "$hook" origin) || rc=$?
if [[ "$rc" -ne 0 ]]; then
	echo "FAIL: declared-base push refused a slice within the limit" >&2
	fails=$((fails + 1))
else
	echo "ok: declared stack base lets the slice pass the size gate"
fi
rc=0
(cd "$repo" && printf 'refs/heads/stacked-feat %s refs/heads/stacked-feat %s\n' "$stacked_head" "$zero" | AIP_HOOK_FILE_LIMIT=2 bash "$hook" origin) 2>"$fixture/err10.log" || rc=$?
if [[ "$rc" -eq 0 ]]; then
	echo "FAIL: unconfigured push inherited the oversized stack and was allowed" >&2
	fails=$((fails + 1))
elif grep -q "exceed" "$fixture/err10.log"; then
	echo "ok: unconfigured push refuses the inherited oversized stack"
else
	echo "FAIL: unconfigured push refused without size explanation" >&2
	fails=$((fails + 1))
fi

# Case 11: an unresolvable declared base must reject the push instead of
# silently falling back to the trunk.
rc=0
(cd "$repo" && printf 'refs/heads/stacked-feat %s refs/heads/stacked-feat %s\n' "$stacked_head" "$zero" | AIP_CHANGE_SIZE_BASE=refs/heads/does-not-exist bash "$hook" origin) 2>"$fixture/err11.log" || rc=$?
if [[ "$rc" -eq 0 ]]; then
	echo "FAIL: invalid declared base was allowed" >&2
	fails=$((fails + 1))
elif grep -Eqi "unknown revision|bad revision|ambiguous argument|fatal" "$fixture/err11.log"; then
	echo "ok: invalid declared base refuses the push"
else
	echo "FAIL: invalid declared base refused without a diff error" >&2
	fails=$((fails + 1))
fi

if [[ "$fails" -ne 0 ]]; then
	echo "FAIL: $fails case(s) failed" >&2
	exit 1
fi
echo "PASS: pre-push measures the declared or PR-diff base and enforces the size gate."
