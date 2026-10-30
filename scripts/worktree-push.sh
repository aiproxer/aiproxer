#!/usr/bin/env bash
# worktree-push.sh pushes a task branch with a lease pinned to the live server
# revision, surviving stale remote-tracking refs.
#
#   scripts/worktree-push.sh <branch> [--rebase] [--apply] [--remote origin]
#
# A plain `git fetch origin main` never refreshes origin/<branch>, so a later
# `git push --force-with-lease` (no explicit revision) fails with "stale info"
# even though nothing is actually stale. This script resolves the server
# revision live via ls-remote and pins the lease to it, so the push succeeds
# whenever the lease still holds at push time.
#
# Dry-run is the default; --apply explicitly authorizes the push.
# --rebase rebases the branch onto <remote>/main first (refuses on a dirty
# worktree or conflicts, leaving the branch untouched). Without --rebase the
# script only ever fast-forwards: a diverged server tip is refused, never
# force-pushed over. --rebase replays local commits onto main and force-pushes,
# so only use it when nobody else pushes to the branch.
# Everything is validated first: branch, worktree ownership, remote. Only
# then does git run.
#
# Exit codes:
#   0 validated dry run, pushed, or --self-test passed
#   1 refused: a validation failed (nothing was pushed)
#   2 usage error
set -euo pipefail

readonly EXIT_REFUSED=1
readonly EXIT_USAGE=2

usage() {
	sed -n '2,21p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

die() {
	echo "worktree-push: $*" >&2
	exit "$EXIT_USAGE"
}

refuse() {
	echo "worktree-push: refused: $*" >&2
	exit "$EXIT_REFUSED"
}

# git_env keeps repository-location variables out of probes. Git exports
# GIT_DIR to every hook it runs, so an inherited one would resolve the ambient
# repository instead of the fixture the caller named.
git_env() {
	env -u GIT_DIR -u GIT_WORK_TREE -u GIT_INDEX_FILE -u GIT_COMMON_DIR \
		-u GIT_OBJECT_DIRECTORY git "$@"
}

self_test() {
	local script fixture rc out
	script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/worktree-push.sh"
	fixture=$(mktemp -d)
	# Bound now: an EXIT trap fires after this function returns, when a local
	# would already be gone under `set -u`.
	trap "rm -rf '$fixture'" EXIT

	export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid
	export GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid

	# Fixture: a bare remote plus a container checkout (branches/main) with
	# one task worktree, all addressable over the local filesystem.
	git init --quiet --bare "$fixture/remote.git"
	git_env clone --quiet "$fixture/remote.git" "$fixture/container/branches/main" 2>/dev/null
	local main="$fixture/container/branches/main"
	local wt="$fixture/container/worktrees/feat-demo"
	git_env -C "$main" checkout --quiet -b main
	printf 'fixture\n' >"$main/README.md"
	git_env -C "$main" add README.md
	git_env -C "$main" commit --quiet -m fixture
	git_env -C "$main" push --quiet origin main
	git_env -C "$main" worktree add --quiet "$wt" -b feat/demo
	printf 'work\n' >"$wt/work.txt"
	git_env -C "$wt" add work.txt
	git_env -C "$wt" commit --quiet -m slice

	# Claim 1: dry run is the default; it reports the plan and pushes nothing.
	out=$(cd "$main" && bash "$script" feat/demo 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 0 ]] || { echo "FAIL: dry run returned $rc" >&2; echo "$out" >&2; return 1; }
	grep -q "branch: feat/demo" <<<"$out" || { echo "FAIL: dry run does not name the branch" >&2; echo "$out" >&2; return 1; }
	grep -q "server: absent" <<<"$out" || { echo "FAIL: dry run does not report absent server ref" >&2; echo "$out" >&2; return 1; }
	if git_env --git-dir="$fixture/remote.git" show-ref --verify --quiet refs/heads/feat/demo; then
		echo "FAIL: dry run pushed" >&2; return 1
	fi

	# Claim 2: --apply pushes a new branch and sets upstream.
	out=$(cd "$main" && bash "$script" feat/demo --apply 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 0 ]] || { echo "FAIL: apply returned $rc" >&2; echo "$out" >&2; return 1; }
	git_env --git-dir="$fixture/remote.git" show-ref --verify --quiet refs/heads/feat/demo ||
		{ echo "FAIL: branch not pushed" >&2; return 1; }

	# Claim 3 (the stale-info bug): age the tracking ref the way months of
	# `fetch origin main` do, then prove the naive lease fails while the
	# script succeeds on a fast-forward tip.
	parent="$(git_env -C "$wt" rev-parse 'HEAD^')"
	git_env -C "$main" update-ref refs/remotes/origin/feat/demo "$parent"
	printf 'local\n' >"$wt/local.txt"
	git_env -C "$wt" add local.txt
	git_env -C "$wt" commit --quiet -m local
	if git_env -C "$wt" push --force-with-lease origin feat/demo >/dev/null 2>&1; then
		echo "FAIL: fixture does not reproduce stale-info (naive lease succeeded)" >&2; return 1
	fi
	out=$(cd "$main" && bash "$script" feat/demo --apply 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 0 ]] || { echo "FAIL: push with stale tracking ref returned $rc" >&2; echo "$out" >&2; return 1; }
	git_env --git-dir="$fixture/remote.git" show-ref --verify --quiet refs/heads/feat/demo ||
		{ echo "FAIL: branch not pushed" >&2; return 1; }

	# Claim 4: --rebase moves the branch onto the latest main before pushing.
	git_env clone --quiet "$fixture/remote.git" "$fixture/other" 2>/dev/null
	git_env -C "$fixture/other" checkout --quiet main
	printf 'upstream\n' >"$fixture/other/upstream.txt"
	git_env -C "$fixture/other" add upstream.txt
	git_env -C "$fixture/other" commit --quiet -m upstream
	git_env -C "$fixture/other" push --quiet origin main
	out=$(cd "$main" && bash "$script" feat/demo --rebase --apply 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 0 ]] || { echo "FAIL: rebase push returned $rc" >&2; echo "$out" >&2; return 1; }
	git_env -C "$main" fetch --quiet origin feat/demo
	if ! git_env -C "$main" merge-base --is-ancestor origin/main "origin/feat/demo"; then
		echo "FAIL: pushed branch is not based on latest main" >&2; return 1
	fi

	# Claim 5: refusals. Unknown branch, and a diverged server tip without
	# --rebase (which must never be force-pushed over), push nothing.
	# (Rebase rewrote feat/demo in claim 4, so resync the peer first.)
	git_env -C "$fixture/other" fetch --quiet origin
	git_env -C "$fixture/other" checkout --quiet feat/demo
	git_env -C "$fixture/other" reset --quiet --hard origin/feat/demo
	printf 'peer\n' >"$fixture/other/peer.txt"
	git_env -C "$fixture/other" add peer.txt
	git_env -C "$fixture/other" commit --quiet -m peer
	git_env -C "$fixture/other" push --quiet origin feat/demo
	peer_tip="$(git_env --git-dir="$fixture/remote.git" rev-parse refs/heads/feat/demo)"
	out=$(cd "$main" && bash "$script" feat/nope --apply 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 1 ]] || { echo "FAIL: unknown branch not refused (rc=$rc)" >&2; echo "$out" >&2; return 1; }
	out=$(cd "$main" && bash "$script" feat/demo --apply 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 1 ]] || { echo "FAIL: diverged server tip without --rebase not refused (rc=$rc)" >&2; echo "$out" >&2; return 1; }
	[[ "$(git_env --git-dir="$fixture/remote.git" rev-parse refs/heads/feat/demo)" == "$peer_tip" ]] ||
		{ echo "FAIL: refused push moved the server tip" >&2; return 1; }
	printf 'dirty\n' >"$wt/dirty.txt"
	before="$(git_env -C "$main" rev-parse feat/demo)"
	out=$(cd "$main" && bash "$script" feat/demo --rebase --apply 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 1 ]] || { echo "FAIL: dirty worktree with --rebase not refused (rc=$rc)" >&2; echo "$out" >&2; return 1; }
	[[ "$(git_env -C "$main" rev-parse feat/demo)" == "$before" ]] ||
		{ echo "FAIL: refused rebase moved the branch" >&2; return 1; }

	echo "PASS: worktree-push pins the lease to the live server revision."
}

branch=""
rebase=false
apply=false
remote="origin"
selftest=false

while [[ $# -gt 0 ]]; do
	case "$1" in
		--rebase) rebase=true; shift ;;
		--apply) apply=true; shift ;;
		--remote) remote="${2:?missing remote name}"; shift 2 ;;
		--self-test | self-test) selftest=true; shift ;;
		-h | --help) usage; exit 0 ;;
		-*) die "unknown flag $1" ;;
		*) [[ -n "$branch" ]] && die "only one branch expected"; branch="$1"; shift ;;
	esac
done

if [[ "$selftest" == true ]]; then
	self_test "$@"
	exit "$?"
fi

[[ -n "$branch" ]] || { usage >&2; die "missing branch"; }

# Resolve the container from the shared git directory, the way
# worktree-create.sh does: a container checkout keeps its common dir at
# <container>/branches/<name>/.git.
common="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" ||
	die "not inside a git repository"
[[ $(basename "$(dirname "$(dirname "$common")")") == branches ]] ||
	die "this repository is not in the <container>/branches/<name> layout; refusing to guess ownership"
container="$(dirname "$(dirname "$(dirname "$common")")")"

# Locate the branch's task worktree and refuse when ownership is unknown.
worktree=""
while read -r key value; do
	if [[ "$key" == worktree ]]; then
		current_path="$value"
	elif [[ "$key" == branch ]]; then
		if [[ "$value" == "refs/heads/$branch" ]]; then
			worktree="$current_path"
		fi
	fi
done < <(git worktree list --porcelain)
[[ -n "$worktree" ]] || refuse "no worktree holds branch $branch"
[[ "$worktree" == "$container"/worktrees/* ]] || refuse "worktree $worktree is outside $container/worktrees"

tip="$(git rev-parse --verify --quiet "refs/heads/$branch")" ||
	refuse "branch $branch does not exist"
server="$(git ls-remote "$remote" "refs/heads/$branch" | cut -f1)"
if [[ -z "$server" ]]; then
	server_label="absent"
else
	server_label="$server"
fi

if [[ "$apply" != true ]]; then
	printf 'branch: %s\nworktree: %s\nserver: %s\nrebase: %s\napply: no (dry run)\n' \
		"$branch" "$worktree" "$server_label" "$rebase"
	exit 0
fi

# Refresh main for the rebase decision. The branch ref itself is resolved
# live via ls-remote at push time, never from a possibly stale tracking ref.
git fetch --quiet "$remote" main 2>/dev/null || git fetch --quiet "$remote" 2>/dev/null || true

if [[ "$rebase" == true ]]; then
	[[ -z "$(git -C "$worktree" status --porcelain)" ]] ||
		refuse "worktree $worktree is dirty; commit or stash before --rebase"
	if ! git -C "$worktree" rebase --quiet "$remote/main"; then
		git -C "$worktree" rebase --abort 2>/dev/null || true
		refuse "rebase onto $remote/main conflicted; branch left untouched"
	fi
	tip="$(git rev-parse --verify "refs/heads/$branch")"
fi

if [[ "$rebase" == true ]]; then
	if [[ -z "$server" ]]; then
		git push -u "$remote" "$branch"
		exit 0
	fi
	git push --force-with-lease="$branch:$server" "$remote" "$branch"
	exit 0
fi

if [[ -z "$server" ]]; then
	git push -u "$remote" "$branch"
	exit 0
fi

# Without --rebase the script only ever fast-forwards, mirroring native push
# semantics: a diverged server tip is refused, never force-pushed over.
if ! git merge-base --is-ancestor "$server" "$tip" 2>/dev/null; then
	refuse "server tip $server is not in local history; re-run with --rebase (only when nobody else pushes to $branch)"
fi
git push "$remote" "$branch"
