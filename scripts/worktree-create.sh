#!/usr/bin/env bash
# worktree-create.sh creates a task worktree at the correct, validated location
# or refuses to.
#
#   scripts/worktree-create.sh fix-short-description [--base origin/main] [--apply] [--setup]
#
# The repository layout is a constraint, not a convention: `<container>/branches/`
# holds the long-lived checkouts and `<container>/worktrees/` holds task worktrees.
# The commit-time layout guard rejects anything else, which means a wrong path is
# discovered only after the work is done. This script moves that failure to the
# moment of creation, before anything is written.
#
# Dry-run is the default; --apply explicitly authorizes creation.
# Everything is validated first: destination, branch, base, repository identity
# and worktree ownership. Only then does git run. Beyond creating the worktree,
# the script runs the layout guard, which proves the result satisfies the rule
# that would otherwise stop the commit, and installs the repository commit
# hooks where none are configured, so commits in the new worktree run the same
# gate as everywhere else instead of bypassing it silently.
#
# Exit codes:
#   0 validated dry run, created, or an identical worktree already exists
#   1 refused: a validation failed (nothing was created)
#   2 usage error
set -euo pipefail

readonly EXIT_REFUSED=1
readonly EXIT_USAGE=2

usage() {
	sed -n '2,17p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

die() {
	echo "worktree-create: $*" >&2
	exit "$EXIT_USAGE"
}

refuse() {
	echo "worktree-create: refused: $*" >&2
	exit "$EXIT_REFUSED"
}

# git_env keeps repository-location variables out of the probe. Git exports
# GIT_DIR to every hook it runs, so an inherited one would resolve the ambient
# repository instead of the fixture the caller named.
git_at() {
	local dir=$1
	shift
	git -C "$dir" "$@"
}

git_at_env() {
	local dir=$1
	shift
	env -u GIT_DIR -u GIT_WORK_TREE -u GIT_INDEX_FILE -u GIT_COMMON_DIR \
		-u GIT_OBJECT_DIRECTORY git -C "$dir" "$@"
}

# container_root derives the container from the shared git directory. A container
# checkout keeps its common dir at <container>/branches/<name>/.git, so the
# container is that directory's grandparent. Anything else is a layout this
# script does not understand, and it refuses rather than guessing.
container_root() {
	local common parent
	common=$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null) ||
		die "not inside a git repository"
	parent=$(dirname "$common")
	[[ $(basename "$(dirname "$parent")") == branches ]] ||
		die "this repository is not in the <container>/branches/<name> layout (common dir: $common); pass --path explicitly"
	dirname "$(dirname "$parent")"
}

# main_worktree is the checkout git should run `worktree add` from. Creating from
# the long-lived checkout rather than the current task worktree keeps the new
# entry point out of a disposable directory.
main_worktree() {
	dirname "$(git rev-parse --path-format=absolute --git-common-dir)"
}

# sanitize_name turns a branch name into the worktree directory name. The
# existing layout already follows this rule (fix/pathvirtual-review ->
# worktrees/fix-pathvirtual-review), so matching it keeps `git worktree list`
# readable.
sanitize_name() {
	printf '%s' "$1" | tr '/' '-' | tr -cd 'A-Za-z0-9._-'
}

# validate_branch rejects names that cannot become a directory name or that git
# would treat as something other than a branch.
validate_branch() {
	local branch=$1 name
	[[ -n "$branch" ]] || die "branch name is required"
	[[ "$branch" != -* ]] || die "branch name must not start with -: $branch"
	[[ "$branch" != *..* ]] || die "branch name must not contain '..': $branch"
	[[ "$branch" != */ ]] || die "branch name must not end with a slash: $branch"
	[[ "$branch" != *//* ]] || die "branch name must not contain an empty component: $branch"
	name=$(sanitize_name "$branch")
	[[ -n "$name" ]] || die "branch name has no usable characters: $branch"
	git check-ref-format --branch "$branch" >/dev/null 2>&1 || die "invalid branch name: $branch"
	printf '%s' "$name"
}

# validate_destination proves the path is inside <container>/worktrees before git
# is asked to create anything there. A relative or escaping path is the exact
# mistake the commit-time guard exists to catch, so it must never reach git.
validate_destination() {
	local container=$1 name=$2 dest=$3

	[[ "$dest" == /* ]] || refuse "destination must be absolute: $dest"
	case "$dest/" in
	"$container/worktrees/"*) ;;
	*) refuse "destination must be under $container/worktrees/: $dest" ;;
	esac
	# Resolve symlinks on the parent: a symlinked parent could still land the
	# worktree somewhere else entirely.
	local parent real_parent
	parent=$(dirname "$dest")
	[[ -d "$parent" ]] || refuse "destination parent does not exist: $parent"
	real_parent=$(cd "$parent" && pwd -P)
	case "$real_parent/" in
	"$container/worktrees/"*) ;;
	*) refuse "destination parent resolves outside worktrees/: $real_parent" ;;
	esac
	[[ $(basename "$real_parent/$name") == "$name" ]] ||
		refuse "destination basename must be $name"
}

# owned_by_this_repo reports whether an existing path is already a worktree of
# this repository. Refusing is not enough: creating over someone else's directory,
# or over a worktree of a different repository, destroys work.
existing_worktree_for() {
	local repo=$1 dest=$2
	# `path` is not carried between records: matching must compare the worktree
	# line itself, or every following line matches too and the caller receives a
	# multi-line path.
	git_at "$repo" worktree list --porcelain 2>/dev/null |
		awk -v want="$dest" '
			/^worktree / && substr($0, 10) == want { found = 1; print substr($0, 10) }
			END                                    { exit(found ? 0 : 1) }'
}

branch_exists_locally() {
	local repo=$1 branch=$2
	git_at_env "$repo" show-ref --verify --quiet "refs/heads/$branch"
}

branch_exists_remotely() {
	local repo=$1 branch=$2
	git_at_env "$repo" ls-remote --exit-code --heads origin "$branch" >/dev/null 2>&1
}

base_ref_exists() {
	local repo=$1 ref=$2
	# A remote-tracking ref must be fetched to be verifiable; without it the
	# command reports nothing and the worktree would be created from a wrong base.
	if [[ "$ref" == origin/* ]]; then
		git_at_env "$repo" rev-parse --verify --quiet "refs/remotes/${ref}" >/dev/null 2>&1 || return 1
		return 0
	fi
	git_at_env "$repo" rev-parse --verify --quiet "refs/heads/$ref" >/dev/null 2>&1
}

# branch_checked_out_elsewhere prevents the confusing failure where git refuses
# to check out a branch another worktree already holds.
branch_checked_out_elsewhere() {
	local repo=$1 branch=$2
	git_at "$repo" worktree list --porcelain 2>/dev/null |
		awk -v want="refs/heads/$branch" '
			/^branch /  { held = substr($0, 8) }
			held == want { found = 1 }
			END          { exit(found ? 0 : 1) }'
}

# The self-test builds a real container fixture and drives the real script
# against it. Stubs would only prove the script calls the commands it intends to;
# the properties that matter here -- that nothing is created before validation
# passes, and that a refusal leaves no directory behind -- are about git's actual
# behaviour.
self_test() {
	local script fixture container rc out

	script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/worktree-create.sh"
	fixture=$(mktemp -d)
	# Bound now: an EXIT trap fires after this function returns, when a local
	# would already be gone under `set -u`.
	trap "rm -rf '$fixture'" EXIT

	container="$fixture/container"
	mkdir -p "$container/branches/main" "$container/worktrees"
	git -C "$container/branches/main" init -q -b main >/dev/null 2>&1
	printf 'fixture\n' >"$container/branches/main/README.md"
	git -C "$container/branches/main" add README.md
	git -C "$container/branches/main" \
		-c 'user.name=Worktree Test' -c user.email=worktree@example.invalid \
		commit -qm fixture
	# A stand-in for the commit-time guard, recording that the script consulted
	# it instead of assuming the layout it created is acceptable.
	mkdir -p "$container/branches/main/scripts"
	printf '#!/usr/bin/env bash\necho ran >>"%s/guard-calls"\nexit 0\n' "$container" \
		>"$container/branches/main/scripts/check-worktree-layout.sh"
	chmod +x "$container/branches/main/scripts/check-worktree-layout.sh"
	git -C "$container/branches/main" add scripts/check-worktree-layout.sh
	git -C "$container/branches/main" \
		-c 'user.name=Worktree Test' -c user.email=worktree@example.invalid \
		commit -qm guard

	out=$(cd "$container/branches/main" && bash "$script" fix/dry-demo --base main 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 0 && ! -e "$container/worktrees/fix-dry-demo" ]] ||
		{ echo "FAIL: default worktree creation was not dry-run-only" >&2; return 1; }
	if git -C "$container/branches/main" show-ref --verify --quiet refs/heads/fix/dry-demo; then
		echo "FAIL: dry run created a branch" >&2; return 1
	fi

	# Claim: the worktree is created at the derived absolute path on a new branch,
	# and the repository's own layout guard was consulted.
	out=$(cd "$container/branches/main" && bash "$script" fix/demo --base main --apply 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 0 ]] || { echo "FAIL: create returned $rc" >&2; echo "$out" >&2; return 1; }
	[[ -d "$container/worktrees/fix-demo/.git" || -f "$container/worktrees/fix-demo/.git" ]] ||
		{ echo "FAIL: worktree not created at $container/worktrees/fix-demo" >&2; return 1; }
	grep -q "branch: fix/demo" <<<"$out" || { echo "FAIL: branch not reported" >&2; echo "$out" >&2; return 1; }
	[[ -s "$container/guard-calls" ]] || { echo "FAIL: layout guard was not consulted" >&2; return 1; }
	# Claim: a new worktree gets the repository commit hooks when nothing is
	# configured; a base without the installer is left alone.
	out=$(cd "$container/branches/main" && bash "$script" fix/plain --base main --apply 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 0 ]] || { echo "FAIL: plain create returned $rc" >&2; echo "$out" >&2; return 1; }
	if git -C "$container/worktrees/fix-plain" config core.hooksPath >/dev/null 2>&1; then
		echo "FAIL: hooks configured without an installer present" >&2; return 1
	fi
	printf '#!/usr/bin/env bash\nset -euo pipefail\nrepo_root="$(git rev-parse --show-toplevel)"\ncd "$repo_root"\ngit config core.hooksPath scripts/hooks\n' \
		>"$container/branches/main/scripts/setup-hooks.sh"
	chmod +x "$container/branches/main/scripts/setup-hooks.sh"
	git -C "$container/branches/main" add scripts/setup-hooks.sh
	git -C "$container/branches/main" \
		-c 'user.name=Worktree Test' -c user.email=worktree@example.invalid \
		commit -qm hooks
	out=$(cd "$container/branches/main" && bash "$script" fix/hooked --base main --apply 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 0 ]] || { echo "FAIL: hooked create returned $rc" >&2; echo "$out" >&2; return 1; }
	[[ $(git -C "$container/worktrees/fix-hooked" config core.hooksPath) == scripts/hooks ]] ||
		{ echo "FAIL: new worktree has no commit hooks installed" >&2; return 1; }
	grep -q 'hooks:' <<<"$out" || { echo "FAIL: hook installation not reported" >&2; echo "$out" >&2; return 1; }
	# Claim: a deliberate hooksPath override is respected, not overwritten.
	git -C "$container/branches/main" config core.hooksPath custom/hooks
	out=$(cd "$container/branches/main" && bash "$script" fix/custom-hooks --base main --apply 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 0 ]] || { echo "FAIL: override create returned $rc" >&2; echo "$out" >&2; return 1; }
	[[ $(git -C "$container/worktrees/fix-custom-hooks" config core.hooksPath) == custom/hooks ]] ||
		{ echo "FAIL: existing hooksPath override was overwritten" >&2; return 1; }
	git -C "$container/branches/main" config --unset core.hooksPath
	# An existing relative parent must not turn a relative destination into an
	# accepted absolute one.
	out=$(cd "$container/branches/main" && bash "$script" fix/relative-existing --path ../../worktrees/fix-relative-existing --base main 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 1 && ! -e "$container/worktrees/fix-relative-existing" ]] ||
		{ echo "FAIL: relative destination with existing parent accepted" >&2; return 1; }
	# Folding slash to dash must not reuse a different branch's worktree.
	out=$(cd "$container/branches/main" && bash "$script" fix-demo --base main 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 1 ]] || { echo "FAIL: reused a worktree holding a different branch" >&2; return 1; }

	# Claim: creating again is safe and idempotent. It must report the existing
	# worktree rather than discard whatever that worktree now holds.
	printf 'work in progress\n' >"$container/worktrees/fix-demo/scratch.txt"
	out=$(cd "$container/branches/main" && bash "$script" fix/demo --base main 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 0 ]] || { echo "FAIL: repeat returned $rc, want 0" >&2; echo "$out" >&2; return 1; }
	grep -q 'worktree already exists' <<<"$out" || { echo "FAIL: repeat not idempotent" >&2; return 1; }
	[[ -f "$container/worktrees/fix-demo/scratch.txt" ]] ||
		{ echo "FAIL: repeat destroyed worktree contents" >&2; return 1; }

	# Claim: a branch that already exists is refused rather than silently reused.
	git -C "$container/branches/main" branch fix/demo2 >/dev/null 2>&1
	out=$(cd "$container/branches/main" && bash "$script" fix/demo2 --base main 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 1 ]] || { echo "FAIL: existing branch returned $rc, want refusal" >&2; echo "$out" >&2; return 1; }
	grep -q 'branch already exists locally' <<<"$out" || { echo "FAIL: existing branch not explained" >&2; return 1; }
	[[ ! -e "$container/worktrees/fix-demo2" ]] ||
		{ echo "FAIL: refused run still created a worktree" >&2; return 1; }

	# Claim: a destination outside <container>/worktrees/ is refused before git
	# runs. This is the mistake the commit-time guard exists to catch, and the
	# whole point of validating first.
	mkdir -p "$container/elsewhere"
	out=$(cd "$container/branches/main" && bash "$script" fix/escape --path "$container/elsewhere/x" 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 1 ]] || { echo "FAIL: escaping path returned $rc, want refusal" >&2; echo "$out" >&2; return 1; }
	grep -q 'must be under' <<<"$out" || { echo "FAIL: escaping path not explained" >&2; echo "$out" >&2; return 1; }
	[[ ! -e "$container/elsewhere/x" ]] || { echo "FAIL: escaping path created a directory" >&2; return 1; }

	# Claim: a relative destination is refused rather than resolved against the
	# current directory, which is how a worktree ends up inside branches/main.
	out=$(cd "$container/branches/main" && bash "$script" fix/relative --path "worktrees/rel" 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 1 ]] || { echo "FAIL: relative path returned $rc, want refusal" >&2; echo "$out" >&2; return 1; }

	# Claim: an existing directory that is not this repository's worktree is
	# refused. Creating over it would destroy someone else's files. The derived
	# directory name is the branch with slashes folded to dashes, matching the
	# repository's existing layout.
	mkdir -p "$container/worktrees/fix-occupied"
	printf 'not a worktree\n' >"$container/worktrees/fix-occupied/keep.txt"
	out=$(cd "$container/branches/main" && bash "$script" fix/occupied --base main 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 1 ]] || { echo "FAIL: occupied path returned $rc, want refusal" >&2; echo "$out" >&2; return 1; }
	[[ -f "$container/worktrees/fix-occupied/keep.txt" ]] ||
		{ echo "FAIL: refusal destroyed an unrelated directory" >&2; return 1; }
	grep -q 'is not a worktree of this repository' <<<"$out" ||
		{ echo "FAIL: occupied path not explained" >&2; echo "$out" >&2; return 1; }

	# Claim: an unresolvable base is refused; a worktree created from it would
	# start from the wrong commit.
	out=$(cd "$container/branches/main" && bash "$script" fix/badbase --base origin/nope 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 1 ]] || { echo "FAIL: bad base returned $rc, want refusal" >&2; echo "$out" >&2; return 1; }
	grep -q 'base ref does not resolve' <<<"$out" || { echo "FAIL: bad base not explained" >&2; return 1; }

	# Claim: a branch name that could escape its directory is rejected outright.
	out=$(cd "$container/branches/main" && bash "$script" '../escape' 2>&1) && rc=0 || rc=$?
	[[ $rc -eq 2 ]] || { echo "FAIL: escaping branch returned $rc, want usage error" >&2; echo "$out" >&2; return 1; }

	echo "PASS: worktree created at the validated absolute path; layout guard consulted; repeats, escaping paths, occupied directories, existing branches, unresolvable bases and unsafe branch names all refused without creating anything"
}

# ensure_hooks installs the repository commit hooks for the new worktree when
# nothing is configured. Without them, commits there bypass the gate silently.
# A deliberate override is left alone, and a base without the installer is left
# alone too: both states are someone's choice, not this script's to repair.
ensure_hooks() {
	local dest=$1 current
	current=$(git_at_env "$dest" config core.hooksPath || true)
	if [[ -n "$current" ]]; then
		return 0
	fi
	[[ -f "$dest/scripts/setup-hooks.sh" ]] || return 0
	(cd "$dest" && bash scripts/setup-hooks.sh) >/dev/null ||
		refuse "created $dest but its commit hooks failed to install"
	echo "  hooks:   installed repository hooks (core.hooksPath=scripts/hooks)"
}

main() {
	local branch="" base=origin/main want_path="" setup=false apply=false
	# No subcommand to skip: the outer dispatch already handled --self-test, so
	# the first argument is the branch name.
	while [[ $# -gt 0 ]]; do
		case "$1" in
		--base)
			base=${2:?--base needs a value}
			shift 2
			;;
		--path)
			want_path=${2:?--path needs a value}
			shift 2
			;;
		--setup)
			setup=true
			shift
			;;
		--apply)
			apply=true
			shift
			;;
		-h | --help | help)
			usage
			return 0
			;;
		-*)
			die "unknown option $1"
			;;
		*)
			[[ -z "$branch" ]] || die "only one branch name is allowed"
			branch=$1
			shift
			;;
		esac
	done
	[[ -n "$branch" ]] || {
		usage >&2
		die "branch name is required"
	}

	local container repo name
	container=$(container_root)
	repo=$(main_worktree)
	name=$(validate_branch "$branch")
	if [[ -n "$want_path" ]]; then
		[[ "$want_path" == /* ]] || refuse "destination must be absolute: $want_path"
		dest=$(cd "$(dirname "$want_path")" 2>/dev/null && pwd -P)/$(basename "$want_path") ||
			die "cannot resolve --path parent: $want_path"
	else
		dest="$container/worktrees/$name"
	fi

	# Validation order matters only for the message a reader gets, but every
	# check runs before git does.
	validate_destination "$container" "$name" "$dest"

	if existing=$(existing_worktree_for "$repo" "$dest"); then
		[[ $(git_at_env "$existing" symbolic-ref --quiet --short HEAD) == "$branch" ]] ||
			refuse "destination is a worktree holding another branch: $dest"
		# Already the right thing: report and succeed rather than recreate it.
		# Recreating would discard whatever the worktree already holds.
		echo "worktree already exists: $existing"
		git_at "$existing" status --porcelain=v1 --branch | head -n1
		return 0
	fi
	if [[ -e "$dest" ]]; then
		refuse "destination exists and is not a worktree of this repository: $dest"
	fi
	if [[ -n "$(git_at "$repo" worktree list --porcelain | awk '/^worktree /{print substr($0,10)}' | sed "s|^$container/worktrees/||" | grep -Fx "$name")" ]] &&
		existing_worktree_for "$repo" "$container/worktrees/$name" >/dev/null; then
		refuse "a worktree named $name already exists at another path"
	fi
	if branch_exists_locally "$repo" "$branch"; then
		refuse "branch already exists locally: $branch (delete it, or reuse its worktree)"
	fi
	if branch_exists_remotely "$repo" "$branch"; then
		refuse "branch already exists on origin: $branch (fetch and inspect it before reusing the name)"
	fi
	if branch_checked_out_elsewhere "$repo" "$branch"; then
		refuse "branch is checked out in another worktree: $branch"
	fi
	if ! base_ref_exists "$repo" "$base"; then
		refuse "base ref does not resolve locally: $base (fetch it first)"
	fi
	if [[ "$apply" != true ]]; then
		printf 'dry run: create branch %s from %s at %s\n' "$branch" "$base" "$dest"
		printf 'rerun with --apply to create it; no files or refs changed\n'
		return 0
	fi

	# Absolute paths on both ends. A relative destination resolves against the
	# current directory, so issuing this from branches/main lands the new
	# worktree inside it.
	git_at "$repo" worktree add -b "$branch" "$dest" "$base"

	# The same guard that would stop the commit, run now so a failure is
	# immediate and attributable.
	if [[ -f "$dest/scripts/check-worktree-layout.sh" ]]; then
		(cd "$dest" && bash scripts/check-worktree-layout.sh) >/dev/null ||
			refuse "created $dest but it fails the repository layout guard"
	fi

	ensure_hooks "$dest"

	printf 'created worktree\n'
	printf '  path:   %s\n' "$dest"
	printf '  branch: %s (from %s)\n' "$branch" "$base"
	printf '  next:   git -C %s status --porcelain=v1 -b\n' "$dest"
	printf '          codegraph init   # if .codegraph/ is absent (see AGENTS.md)\n'
	printf '          make dev-doctor  # effective toolchain and cache configuration\n'

	if [[ "$setup" == true ]]; then
		# The repository's own prerequisite diagnostic. It is the only thing this
		# script does beyond creating the worktree, and only on request.
		echo
		echo "development prerequisites:"
		make -C "$dest" dev-doctor
	fi
}

case "${1:-}" in
--self-test | self-test)
	shift
	self_test "$@"
	;;
*)
	main "$@"
	;;
esac
