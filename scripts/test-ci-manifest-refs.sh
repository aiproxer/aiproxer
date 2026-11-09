#!/usr/bin/env bash
# Proves ci-manifest-refs.sh selects the right commits for the repository
# file manifest check: schedule and manual runs validate the checked-out
# tree only (history is immutable, so scanning it can never go green once
# a past commit drifts), pushes validate the pushed range, and new-branch
# pushes validate the commits unreachable from other origin refs.
# Uses real throwaway repositories: the selection reads git plumbing, and
# a stubbed git would test the stub rather than the selection.
set -euo pipefail
script_dir=$(cd "$(dirname "$0")" && pwd)
selector="$script_dir/ci-manifest-refs.sh"
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT

export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid
export GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid

fails=0

# make_history <name> -> prints repo path holding:
#   root commit: .release-files lists F, but F does not exist (stale entry,
#     mirroring a manifest entry whose file arrived in a later commit);
#   second commit: adds F.
# No remotes: selects like a scheduled run with no other tracking refs.
make_history() {
	local repo="$fixture/$1"
	git init --quiet --initial-branch=main "$repo"
	printf '# manifest\n.release-files\nF\n' >"$repo/.release-files"
	git -C "$repo" add .release-files
	git -C "$repo" commit --quiet -m root
	echo content >"$repo/F"
	git -C "$repo" add F
	git -C "$repo" commit --quiet -m add-file
	printf '%s\n' "$repo"
}

# run_selector <repo> -> prints selected refs, with schedule-like env:
# empty BEFORE_SHA (schedule/dispatch events carry none) and main pushed ref.
run_schedule_like() {
	local repo="$1" event="$2"
	(cd "$repo" && EVENT_NAME="$event" BEFORE_SHA="" PUSHED_REF="refs/heads/main" bash "$selector")
}

# expect_empty <label> <event> <repo>: schedule-like events select nothing;
# the caller checks HEAD separately, so a stale historical commit can never
# fail the run.
expect_empty() {
	local label="$1" event="$2" repo="$3" out
	out="$(run_schedule_like "$repo" "$event")"
	if [[ -z "$out" ]]; then
		echo "ok: $label selects no historical commits"
	else
		echo "FAIL: $label selected commits:" >&2
		printf '%s\n' "$out" >&2
		fails=$((fails + 1))
	fi
}

# expect_range <label> <repo> <before> <want...>: a push with a resolvable
# BEFORE_SHA selects exactly the pushed range, oldest first.
expect_range() {
	local label="$1" repo="$2" before="$3"
	shift 3
	local out want
	out="$(cd "$repo" && EVENT_NAME="push" BEFORE_SHA="$before" PUSHED_REF="refs/heads/main" bash "$selector")"
	want="$(printf '%s\n' "$@")"
	if [[ "$out" == "$want" ]]; then
		echo "ok: $label selects the pushed range"
	else
		echo "FAIL: $label selected:" >&2
		printf '%s\n' "$out" >&2
		echo "want:" >&2
		printf '%s\n' "$want" >&2
		fails=$((fails + 1))
	fi
}

repo="$(make_history history)"
root_sha="$(git -C "$repo" rev-list --max-parents=0 HEAD)"
head_sha="$(git -C "$repo" rev-parse HEAD)"

expect_empty "schedule" "schedule" "$repo"
expect_empty "workflow_dispatch" "workflow_dispatch" "$repo"
expect_empty "pull_request" "pull_request" "$repo"
expect_empty "merge_group" "merge_group" "$repo"

# A push of the second commit selects exactly that commit, never the stale root.
expect_range "push range" "$repo" "$root_sha" "$head_sha"

# A new-branch push (all-zero BEFORE_SHA) with no other tracking refs
# selects the whole history, matching the previous behaviour for branches.
out="$(cd "$repo" && EVENT_NAME="push" BEFORE_SHA="0000000000000000000000000000000000000000" PUSHED_REF="refs/heads/main" bash "$selector")"
want="$(git -C "$repo" rev-list --reverse HEAD)"
if [[ "$out" == "$want" ]]; then
	echo "ok: new-branch push selects unreachable history"
else
	echo "FAIL: new-branch push selected:" >&2
	printf '%s\n' "$out" >&2
	fails=$((fails + 1))
fi

if ((fails > 0)); then
	echo "$fails selection case(s) failed" >&2
	exit 1
fi
echo "manifest ref selection cases passed"
