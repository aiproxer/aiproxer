#!/usr/bin/env bash
# Proves check-local-specifics.sh refuses staged additions and commit messages
# carrying local-environment strings, allows clean content and pure deletions,
# honours the path allowlist, and survives unknown flags and missing files.
# Uses real throwaway repositories: the guard reads git plumbing, and a stubbed
# git would test the stub rather than the guard.
set -euo pipefail
script_dir=$(cd "$(dirname "$0")" && pwd)
guard="$script_dir/check-local-specifics.sh"
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

# expect_block <label> <repo> [guard args...]
expect_block() {
	local label="$1" repo="$2"
	shift 2
	if (cd "$repo" && bash "$guard" "$@" >/dev/null 2>&1); then
		echo "FAIL: $label was allowed" >&2
		fails=$((fails + 1))
	else
		echo "ok: $label blocked"
	fi
}

# expect_allow <label> <repo> [guard args...]
expect_allow() {
	local label="$1" repo="$2"
	shift 2
	if (cd "$repo" && bash "$guard" "$@" >/dev/null 2>&1); then
		echo "ok: $label allowed"
	else
		echo "FAIL: $label was blocked" >&2
		fails=$((fails + 1))
	fi
}

# Every forbidden literal must block a staged addition.
# shellcheck disable=SC2088
# '~/source/' is a literal forbidden string, never a path to expand.
literals=('devbox-local' '/home/mateusz/' '~/source/' '/source/repos/')
for i in "${!literals[@]}"; do
	repo="$(make_repo "staged$i")"
	printf 'path prefix %s suffix\n' "${literals[$i]}" >"$repo/notes.txt"
	git -C "$repo" add notes.txt
	expect_block "staged '${literals[$i]}'" "$repo"
done

repo="$(make_repo clean)"
echo "hello world" >"$repo/notes.txt"
git -C "$repo" add notes.txt
expect_allow "clean staged change" "$repo"

# Deleting a historical leak must stay easy: only additions are scanned.
repo="$(make_repo del)"
printf 'leak devbox-local here\nkeep me\n' >"$repo/notes.txt"
git -C "$repo" add notes.txt
git -C "$repo" commit --quiet -m "seed leak" --no-verify
printf 'keep me\n' >"$repo/notes.txt"
git -C "$repo" add notes.txt
expect_allow "deletion-only of forbidden line" "$repo"

# Untouched forbidden lines elsewhere in a modified file must not re-flag.
repo="$(make_repo ctx)"
printf 'leak /home/mateusz/ here\nline2\nline3\n' >"$repo/notes.txt"
git -C "$repo" add notes.txt
git -C "$repo" commit --quiet -m "seed leak" --no-verify
printf 'leak /home/mateusz/ here\nline2 changed\nline3\n' >"$repo/notes.txt"
git -C "$repo" add notes.txt
expect_allow "edit near (not touching) forbidden line" "$repo"

# The allowlist excuses seeded paths and nothing else.
repo="$(make_repo allow)"
mkdir -p "$repo/scripts"
printf 'scripts/check-local-specifics.sh\n' >"$repo/scripts/local-specifics-allowlist.txt"
printf 'I define devbox-local handling\n' >"$repo/scripts/check-local-specifics.sh"
git -C "$repo" add scripts/check-local-specifics.sh
expect_allow "allowlisted path" "$repo"
printf 'devbox-local elsewhere\n' >"$repo/other.txt"
git -C "$repo" add other.txt
expect_block "non-allowlisted path still" "$repo"

# Commit-message pass: literals block, comments and clean text pass.
repo="$(make_repo msg)"
printf 'feat: wire thing on devbox-local\n\nBody.\n' >"$repo/msg.txt"
expect_block "message with devbox-local" "$repo" --message "$repo/msg.txt"
printf 'docs: see /source/repos/team/notes\n' >"$repo/msg-path.txt"
expect_block "message with /source/repos/" "$repo" --message "$repo/msg-path.txt"
printf 'chore: path ~/source/x referenced\n' >"$repo/msg-tilde.txt"
expect_block "message with ~/source/" "$repo" --message "$repo/msg-tilde.txt"
printf 'feat: clean subject\n\nClean body.\n' >"$repo/msg-clean.txt"
expect_allow "clean message" "$repo" --message "$repo/msg-clean.txt"
printf 'feat: clean\n\n# devbox-local in a git comment line\n' >"$repo/msg-comment.txt"
expect_allow "message #-comment ignored" "$repo" --message "$repo/msg-comment.txt"

if bash "$guard" --bogus >/dev/null 2>&1; then
	echo "FAIL: unknown flag was accepted" >&2
	fails=$((fails + 1))
else
	echo "ok: unknown flag rejected"
fi
if bash "$guard" --message "$fixture/nonexistent" >/dev/null 2>&1; then
	echo "FAIL: missing message file was accepted" >&2
	fails=$((fails + 1))
else
	echo "ok: missing message file rejected"
fi
if bash "$guard" --range 'main...nosuchref' >/dev/null 2>&1; then
	echo "FAIL: bogus --range was accepted" >&2
	fails=$((fails + 1))
else
	echo "ok: bogus --range fails closed"
fi

# Remote-CI modes: --range scans the diff, --commits scans message bodies.
repo="$(make_repo range)"
printf 'clean line\n' >"$repo/file.txt"
git -C "$repo" add file.txt
git -C "$repo" commit --quiet -m "clean PR commit"
expect_allow "clean range diff" "$repo" --range 'HEAD~1...HEAD'
expect_allow "clean commits bodies" "$repo" --commits 'HEAD~1..HEAD'
printf 'leak from ~/source/x\n' >>"$repo/file.txt"
git -C "$repo" add file.txt
git -C "$repo" commit --quiet -m "another clean commit"
expect_block "leaking range diff" "$repo" --range 'HEAD~1...HEAD'
expect_allow "clean bodies despite leaking diff" "$repo" --commits 'HEAD~1..HEAD'
git -C "$repo" commit --quiet --allow-empty -m "docs: notes from /source/repos/team"
expect_block "leaking commit body" "$repo" --commits 'HEAD~1..HEAD'
expect_block "combined range and commits" "$repo" --range 'HEAD~2...HEAD' --commits 'HEAD~2..HEAD'

if ((fails > 0)); then
	echo "FAIL: $fails case(s) behaved incorrectly" >&2
	exit 1
fi
echo "PASS: local-environment strings are refused in staged content and commit messages."
