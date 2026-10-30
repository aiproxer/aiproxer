#!/usr/bin/env bash
# Proves check-merge-receiver-branch.sh refuses a commit authored on main or dev,
# accepts a commit on a worktree branch, and honours the maintainer override.
# Uses real throwaway repositories: the guard reads git plumbing, and a stubbed
# git would test the stub rather than the guard.
set -euo pipefail
script_dir=$(cd "$(dirname "$0")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT

export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid
export GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid

fails=0

# make_repo <name> -> prints repo path, with one commit on branch main
make_repo() {
	local repo="$fixture/$1"
	git init --quiet --initial-branch=main "$repo"
	echo seed >"$repo/seed.txt"
	git -C "$repo" add seed.txt
	git -C "$repo" commit --quiet -m seed
	printf '%s\n' "$repo"
}

# expect_refuse <label> <repo> <branch>
expect_refuse() {
	local label="$1" repo="$2" branch="$3"
	local out status
	out="$(git -C "$repo" checkout --quiet "$branch" 2>/dev/null
		(cd "$repo" && bash "$script_dir/check-merge-receiver-branch.sh") 2>&1)" && status=0 || status=$?
	if [[ "$status" -eq 0 ]]; then
		echo "FAIL: $label was allowed" >&2
		fails=$((fails + 1))
	elif ! grep -q 'merge receiver' <<< "$out"; then
		echo "FAIL: $label refused without explaining the rule; got:" >&2
		echo "$out" >&2
		fails=$((fails + 1))
	else
		echo "ok: $label refused"
	fi
}

expect_allow() {
	local label="$1" repo="$2" branch="$3"
	git -C "$repo" checkout --quiet "$branch" 2>/dev/null || git -C "$repo" checkout --quiet -b "$branch"
	local out
	if out="$(cd "$repo" && bash "$script_dir/check-merge-receiver-branch.sh" 2>&1)"; then
		echo "ok: $label allowed"
	else
		echo "FAIL: $label was refused: $out" >&2
		fails=$((fails + 1))
	fi
}

main_repo="$(make_repo main)"
dev_repo="$(make_repo dev)"
work_repo="$(make_repo work)"

expect_refuse "commit on main" "$main_repo" main
expect_refuse "commit on dev" "$dev_repo" dev
expect_allow "commit on a fix/ branch" "$work_repo" fix/some-change
expect_allow "commit on a feat/ branch" "$work_repo" feat/some-change

# A detached HEAD is not a merge receiver and must not be blocked.
if (cd "$work_repo" && git checkout --quiet --detach && bash "$script_dir/check-merge-receiver-branch.sh") >/dev/null 2>&1; then
	echo "ok: detached HEAD allowed"
else
	echo "FAIL: detached HEAD was refused" >&2
	fails=$((fails + 1))
fi

# Maintainer escape hatch, and only for the exact value.
if out="$(cd "$main_repo" && AIP_ALLOW_MERGE_RECEIVER_COMMIT=1 bash "$script_dir/check-merge-receiver-branch.sh" 2>&1)"; then
	grep -q 'bypassed' <<< "$out" || {
		echo "FAIL: override allowed the commit without reporting the bypass" >&2
		fails=$((fails + 1))
	}
	echo "ok: maintainer override honours and reports the bypass"
else
	echo "FAIL: maintainer override did not allow the commit: $out" >&2
	fails=$((fails + 1))
fi

if (cd "$main_repo" && AIP_ALLOW_MERGE_RECEIVER_COMMIT=true bash "$script_dir/check-merge-receiver-branch.sh") >/dev/null 2>&1; then
	echo "FAIL: override accepted a non-'1' value" >&2
	fails=$((fails + 1))
else
	echo "ok: override rejects any value but 1"
fi

if ((fails > 0)); then
	echo "FAIL: $fails case(s) behaved incorrectly" >&2
	exit 1
fi
echo "PASS: merge receivers refuse commits, worktree branches allow them."