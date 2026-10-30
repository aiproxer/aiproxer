#!/usr/bin/env bash
# main and dev are merge receivers: pull-request merges land there, and no change
# is authored there. A commit made on either branch bypasses the review this repo
# requires, so the pre-commit hook refuses it.
#
# The companion layout guard (check-worktree-layout.sh) validates where a
# worktree LIVES. This validates what is checked out in it, which that guard
# deliberately permits at branches/main.
set -euo pipefail

# Maintainer-only escape hatch, matching the change-size gate's AIP_ALLOW_LARGE_CHANGE.
# Agents never set this: the rule is that work is authored on a worktree branch.
if [[ "${AIP_ALLOW_MERGE_RECEIVER_COMMIT:-}" == "1" ]]; then
	echo "merge-receiver branch check bypassed (AIP_ALLOW_MERGE_RECEIVER_COMMIT=1)."
	exit 0
fi

branch="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || true)"
if [[ "$branch" != "main" && "$branch" != "dev" ]]; then
	exit 0
fi

container="$(cd "$(git rev-parse --show-toplevel)/../.." 2>/dev/null && pwd -P || echo '<container>')"
worktrees_dir="$container/worktrees"

cat >&2 <<EOF
error: refusing to commit on '$branch'.

  $branch is a merge receiver: it receives squashed pull-request merges and
  authors nothing. Committing here puts a change on the integration branch
  without review, which is the outcome the worktree layout exists to prevent.

  Create a worktree branch and commit there:

    git -C $container/branches/main worktree add \\
      -b fix/short-description $worktrees_dir/fix-short-description origin/main

  The path must be absolute and must sit directly under $worktrees_dir/.
  A relative path resolves against the current directory and lands the new
  worktree inside branches/main, where the layout guard rejects it later.

  Maintainers who must commit here can set AIP_ALLOW_MERGE_RECEIVER_COMMIT=1
  for a single command.
EOF
exit 1