#!/usr/bin/env bash
# The task-branch prefix rule (AGENTS.md): a task branch is named fix/, feat/,
# spec/ or docs/. Sourced by worktree-create.sh and scripts/hooks/pre-push so the
# two enforcement points cannot drift. TASK_BRANCH_PREFIX_RE and
# TASK_BRANCH_PREFIXES describe the same set; the second renders it for a message.

# TASK_BRANCH_PREFIX_RE matches a task branch name: a prefix, a slash, and at
# least one non-slash character.
TASK_BRANCH_PREFIX_RE='^(fix|feat|spec|docs)/[^/]'
TASK_BRANCH_PREFIXES='fix/, feat/, spec/, or docs/'

# task_branch_has_prefix <branch> reports whether <branch> carries a task prefix.
task_branch_has_prefix() {
	[[ "$1" =~ $TASK_BRANCH_PREFIX_RE ]]
}
